// Package field — сторона ЛОКАЦИИ у двери: вход в поле, выход и «кто в поле».
//
// Регистрация локации И ЕСТЬ подключение к двери (`kb:WORLD-53`), поэтому здесь
// нет ни «настроить маршрут», ни «прописать в список»: одно действие — Join, и
// маршрут приезжает ответом на него.
//
// Правила имени, адреса и описания живут в двери (`internal/door`) и здесь НЕ
// дублируются. Это не экономия строк: вторая копия правила разъезжается с
// первой молча, и локация начинает получать «ок» от клиента и отказ от двери.
// Клиент проверяет ровно одно — то, чего дверь проверить не может:
//
//	ДОСТИЖИМ ЛИ Я САМ по тому адресу, который объявляю.
//
// Отказы двери передаются как есть — вместе с причиной и выходом (`kb:WORLD-31`):
// придумывать им свои формулировки значит терять выход, который дверь уже назвала.
package field

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/omnifield/world/core/internal/door"
)

const (
	// DefaultDoor — контрактное имя двери в общей сети (`kb:WORLD-53`,
	// `kb:FUND-5`). Алиас объявляет файл запуска мира (зона `deploy`), здесь он
	// только УМОЛЧАНИЕ клиента: адрес двери — настройка локации, а не константа
	// кода, и меняется он `WORLD_DOOR` без пересборки.
	DefaultDoor = "door:8080"

	// SelfProbeTimeout — сколько ждём собственный сторож. Себя ждём меньше, чем
	// чужого: сторож в том же контейнере отвечает мгновенно либо не отвечает
	// вовсе, и длинное ожидание здесь только оттягивает отказ.
	SelfProbeTimeout = 2 * time.Second

	// DoorTimeout — потолок разговора с дверью. Регистрация — три поля, ответ
	// маленький; всё, что дольше, — это не «медленно», а «двери нет», и сказать
	// об этом надо, пока человек ещё смотрит в терминал.
	DoorTimeout = 10 * time.Second

	// MaxAnswer — сколько байт ответа мы согласны прочитать. По адресу двери
	// может стоять что угодно (в том числе раздача гигабайтного файла), и
	// читать это целиком, чтобы потом сказать «ответила не дверь», незачем.
	MaxAnswer = 64 << 10
)

// Refusal — отказ, названный по `kb:WORLD-31`: причина И выход. Форма одна для
// своих отказов и для отказов двери — снаружи не должно быть видно, кто из двоих
// сказал «нет», важно, что делать дальше.
type Refusal struct {
	Code   string `json:"error"`
	Detail string `json:"detail"`
	// Status — код двери; 0 значит «до двери дело не дошло», отказ наш.
	Status int `json:"-"`
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Detail }

func refuse(code, format string, args ...any) *Refusal {
	return &Refusal{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// Announce — то, чем локация объявляется двери. Ровно три поля: больше двери
// не нужно, а форма объявления участка у мира ещё открыта (`kb:WORLD-50`).
type Announce struct {
	Name  string `json:"name"`
	Addr  string `json:"addr"`
	Gives string `json:"gives"`
}

// Joined — ответ двери на вход: где локация теперь достижима и ЧТО это было.
type Joined struct {
	Location door.Location `json:"location"`
	Route    string        `json:"route"`
	// Created=false — локация уже была в поле. Поле осталось прежним ради тех,
	// кто читал только его; чем именно кончился вход, называет Outcome.
	Created bool `json:"created"`
	// Outcome — исход входа: created · confirmed · returned (`tasker:WORLD-81`).
	// Возвращение (то же место по новому адресу) в флаг Created не влезает, а
	// напечатать его человеку надо иначе, чем первый вход.
	Outcome door.Outcome `json:"outcome"`
}

// Client — дверь, какой её видит локация.
type Client struct {
	door  string
	http  *http.Client
	probe door.Probe
	logf  func(string, ...any)
	now   func() time.Time
}

// New собирает клиента. doorAddr — «имя:порт» без схемы, ровно в той форме, в
// какой дверь сама говорит об адресах соседей. probe проверяет СВОЙ адрес
// (nil даёт ту же пробу, которой дверь проверяет чужие, — значит «достижим»
// означает у обоих одно и то же); logf — куда идёт трассировка (nil даёт
// log.Printf); now подменяется в тестах.
func New(doorAddr string, probe door.Probe, logf func(string, ...any), now func() time.Time) *Client {
	if probe == nil {
		probe = door.DialProbe(SelfProbeTimeout)
	}
	if logf == nil {
		logf = log.Printf
	}
	if now == nil {
		now = time.Now
	}
	return &Client{
		door:  doorAddr,
		http:  &http.Client{Timeout: DoorTimeout},
		probe: probe,
		logf:  logf,
		now:   now,
	}
}

// Door — с какой дверью работает клиент. Нужен тем, кто печатает результат
// человеку: без адреса двери «локация в поле» не отвечает на вопрос «в каком».
func (c *Client) Door() string { return c.door }

// Join — вход в поле: проверить себя, объявиться двери, получить маршрут.
//
// Порядок «сначала себя, потом дверь» — не косметика. Дверь тоже проверяет
// адрес и отвечает `addr-unreachable`, но её отказ означает «я до тебя не
// дошла» и не отличает «сторож не поднят» от «мы в разных сетях». Локация про
// себя знает больше, и потому свой отказ точнее — а отказ обязан называть
// ПРИЧИНУ, а не следствие (`kb:WORLD-31`).
func (c *Client) Join(a Announce) (*Joined, error) {
	if err := c.probe(a.Addr); err != nil {
		return nil, refuse("self-unreachable",
			"по объявленному адресу %s никто не отвечает (%v) — войти с адресом, по которому тебя нет, "+
				"значит положить в реестр поля запись, ведущую в никуда. Подними сторожа раньше, чем входишь: "+
				"world guard (он слушает WORLD_ADDR, умолчание :8080); адрес объявления задаётся WORLD_SELF_ADDR либо -addr",
			a.Addr, err)
	}

	body, err := json.Marshal(a)
	if err != nil {
		return nil, refuse("announce-invalid", "объявление не сериализуется: %v", err)
	}

	raw, err := c.do(http.MethodPost, door.Prefix, body)
	if err != nil {
		return nil, err
	}

	var out Joined
	if err := json.Unmarshal(raw, &out); err != nil || out.Route == "" {
		return nil, notADoor(c.door, http.StatusOK, raw,
			"ответ на регистрацию не содержит маршрута")
	}
	if out.Outcome == "" {
		// Дверь старше этой редакции: трёх исходов она не называет. Локация и
		// дверь — РАЗНЫЕ образы с разной судьбой (`kb:WORLD-53`), и новый образ
		// локации законно приезжает к старой двери. Выводим исход из прежнего
		// поля, а не молчим: молчание напечаталось бы как «вошла».
		out.Outcome = door.OutcomeCreated
		if !out.Created {
			out.Outcome = door.OutcomeConfirmed
		}
	}
	return &out, nil
}

// Leave — снятие с поля: маршрут уходит вместе с локацией.
func (c *Client) Leave(name string) error {
	if strings.TrimSpace(name) == "" {
		return refuse("name-missing",
			"не сказано, кого снимать с поля; имя задаётся WORLD_NAME либо -name, а кто сейчас в поле — world who")
	}
	// Имя уезжает в путь, и его алфавит закрыт самой дверью (`internal/door`).
	// Экранировать здесь нечего: всё, что не проходит по алфавиту, дверь
	// отвергнет названным отказом — и это единственная истина о правиле имени.
	_, err := c.do(http.MethodDelete, door.Prefix+"/"+name, nil)
	return err
}

// Who — кто сейчас в поле. Список за дверью — это СОСТОЯНИЕ ПОЛЯ (`kb:WORLD-28`),
// а не чья-то память: локации появляются регистрацией и уходят со сносом.
func (c *Client) Who() ([]door.Location, error) {
	raw, err := c.do(http.MethodGet, door.Prefix, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Count     *int            `json:"count"`
		Locations []door.Location `json:"locations"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Count == nil {
		return nil, notADoor(c.door, http.StatusOK, raw, "ответ не похож на список поля")
	}
	return out.Locations, nil
}

// do — один разговор с дверью: запрос, трассировка, разбор отказа.
//
// Строка трассировки уходит на КАЖДЫЙ запрос по той же причине, что и у самой
// двери: собеседник на другой машине, и без строки непонятно, дошли ли мы до
// него вообще и сколько это заняло.
func (c *Client) do(method, path string, body []byte) ([]byte, error) {
	url := "http://" + c.door + path

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return nil, refuse("door-addr-invalid",
			"адрес двери %q не складывается в запрос (%v); ожидается «имя:порт» без схемы, например %s — задаётся WORLD_DOOR либо -door",
			c.door, err, DefaultDoor)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	started := c.now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.logf("field: %s %s дверь не ответила: %v dur=%s", method, url, err, c.now().Sub(started).Round(time.Microsecond))
		return nil, refuse("door-unreachable",
			"двери по адресу %s не видно (%v). Проверь, что мир поднят (docker compose -f deploy/compose.yaml ps) "+
				"и что ты в общей сети с ним (docker network omnifield-gateway); адрес двери — WORLD_DOOR либо -door, умолчание %s",
			c.door, err, DefaultDoor)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxAnswer))
	c.logf("field: %s %s code=%d bytes=%d dur=%s",
		method, url, resp.StatusCode, len(raw), c.now().Sub(started).Round(time.Microsecond))
	if err != nil {
		return nil, refuse("door-answer-unreadable",
			"ответ двери %s не дочитан: %v; повтори — поле от неудавшегося чтения не изменилось", c.door, err)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return nil, doorRefusal(c.door, resp.StatusCode, raw)
	}
	return raw, nil
}

// doorRefusal переводит отказ двери в наш. Причина и выход берутся ЕЁ словами:
// дверь знает про своё правило больше клиента, и переписывать её текст здесь
// значит терять выход, который она уже назвала.
func doorRefusal(doorAddr string, status int, raw []byte) *Refusal {
	var e struct {
		Code   string `json:"error"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &e); err != nil || e.Code == "" {
		return notADoor(doorAddr, status, raw, "отказ пришёл не в форме двери")
	}
	return &Refusal{Code: e.Code, Detail: e.Detail, Status: status}
}

// notADoor — по адресу двери ответил кто-то другой. Отдельный отказ, а не
// «неверный JSON»: самая частая причина — в WORLD_DOOR стоит адрес локации или
// хост-порт вместо контрактного имени, и человеку надо чинить настройку, а не
// искать ошибку в разборе.
func notADoor(doorAddr string, status int, raw []byte, что string) *Refusal {
	отрывок := strings.TrimSpace(string(raw))
	if len(отрывок) > 200 {
		отрывок = отрывок[:200] + "…"
	}
	return &Refusal{
		Code: "not-a-door",
		Detail: fmt.Sprintf(
			"по адресу %s отвечает не дверь мира: %s (код %d, ответ: %q). "+
				"В WORLD_DOOR (либо -door) нужен адрес ДВЕРИ в общей сети — «имя:порт» без схемы, умолчание %s",
			doorAddr, что, status, отрывок, DefaultDoor),
		Status: status,
	}
}
