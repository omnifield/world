// Пакет scope — скоуп юзера: его личность, лежащая ТАМ, где он её положил.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ СКОУП ОПРЕДЕЛЯЕТСЯ АДРЕСОМ (`WORLD2` 3.4, «Копий нет — есть адреса»).                │
// │                                                                                      │
// │ Дело контроллера ровно два: взять то, что даёт раздача по названному адресу, и        │
// │ поменять то, что меняет ручка по тому же адресу. Он не сличает копии, не ищет         │
// │ «настоящую» и не хранит тождества — их не существует. Две раздачи это два скоупа.     │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// Адрес скоупа — ОБЫЧНЫЙ HTTP-АДРЕС, и состояние лежит в корне:
//
//	http://10.8.0.5:8070/
//
// Форма стыковки — розетка мира (`0.3`), и она вся здесь: `GET /` отдаёт состояние
// целиком, `PUT /` принимает его целиком, пароль проверяет сама раздача (`Authorization:
// Basic`). Больше в ней ничего нет, и это её главное свойство: **чужая раздача равноправна
// нашей** — отдал файл, принял файл, проверил пароль, «хоть на ардуино».
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ КЛИЕНТ ЧИТАЕТ СТАТУС, А ТЕЛО — КАК ПОДРОБНОСТЬ (`WORLD2` 3.4, «Розетка раздачи»).    │
// │                                                                                      │
// │ Наша раздача отвечает отказом-тройкой, но чужая вправе ответить голым `401` без       │
// │ тела — и это законная раздача. Узнавать «свою» по заголовку, телу или `realm` нельзя  │
// │ вовсе: узнав, мир перестал бы принимать чужие вилки (`0.3`).                          │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// Прежняя редакция этого пакета брала скоуп по ssh с пути на машине контроллера. Так
// личность лежала ТОМОМ КОНТРОЛЛЕРА: снёс контроллер — потерял себя (`WORLD2-124`,
// найдено живым прогоном 2026-08-16). Контроллеру положено быть времянкой (`1.9`, `3.7`),
// и состояния он не держит — он до него дотягивается.
package scope

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/omnifield/world/control/internal/refusal"
	"github.com/omnifield/world/control/internal/state"
)

// readLimit — сколько байт состояния мы согласны прочитать. Не мнение о содержимом, а
// граница памяти: без неё раздача, отдающая бесконечный поток, съела бы контроллер молча.
// Предел записи у нашей раздачи того же порядка (`share/PROTOCOL.md`).
const readLimit = 8 << 20

// Address — разобранный адрес скоупа.
type Address struct {
	Raw  string
	URL  *url.URL
	Host string
}

func (a Address) String() string { return a.Raw }

// Port — порт, по которому юзер назвал свой скоуп.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ЭТО НЕ НАСТРОЙКА, А ТО, ЧТО ЮЗЕР УЖЕ СКАЗАЛ (`WORLD2-150` B3).                       │
// │                                                                                      │
// │ Он назвал `http://машина:8071/` — значит раздача обязана слушать ТАМ. Выдумать свой   │
// │ порт значит отдать ему адрес, которого он не называл, и «личность лежит по адресу»    │
// │ перестанет быть правдой на первом же втором скоупе.                                   │
// │                                                                                      │
// │ Порт не назван — его называет схема, и это тоже сказанное юзером, а не догадка: он     │
// │ написал `http://машина/`, и в этом адресе стоит 80. Раздача встанет там, и адрес       │
// │ останется тем самым, по которому он потом войдёт.                                     │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func (a Address) Port() int {
	if a.URL == nil {
		return 0
	}
	if p := a.URL.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			return n
		}
	}
	if a.URL.Scheme == "https" {
		return 443
	}
	return 80
}

// Parse разбирает адрес ОДИН раз и в одном месте. Разъедутся два разбора — вход пойдёт по
// одному адресу, а запись по другому, и юзер обнаружит два скоупа вместо одного.
func Parse(raw string) (Address, *refusal.Refusal) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Address{}, refusal.New(http.StatusBadRequest, "no-address",
			"адрес скоупа не назван, а угадать его нельзя: личность лежит там, где её положил юзер",
			"назови адрес раздачи: http://10.8.0.5:8070/",
			"скоупа ещё нет — заведи его: POST /api/scope")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return Address{}, refusal.New(http.StatusBadRequest, "bad-address",
			fmt.Sprintf("адрес %q не разобрать: %v", raw, err),
			"адрес скоупа — обычный HTTP-адрес: http://10.8.0.5:8070/")
	}
	switch u.Scheme {
	case "http", "https":
	case "":
		return Address{}, refusal.New(http.StatusBadRequest, "bad-address",
			fmt.Sprintf("в адресе %q не назван протокол, а достраивать его за юзера мир не берётся", raw),
			"назови целиком: http://"+raw,
			"скоуп на пути в файловой системе больше не заводится — он раздаётся по адресу (`WORLD2` 3.4)")
	default:
		return Address{}, refusal.New(http.StatusBadRequest, "bad-address",
			fmt.Sprintf("адрес %q говорит по %q, а раздача скоупа — это HTTP", raw, u.Scheme),
			"назови http- или https-адрес: http://10.8.0.5:8070/")
	}
	if u.Host == "" {
		return Address{}, refusal.New(http.StatusBadRequest, "bad-address",
			fmt.Sprintf("в адресе %q не названа машина", raw),
			"назови целиком: http://10.8.0.5:8070/")
	}
	if u.User != nil {
		// Пароль скоупа называется ОТДЕЛЬНЫМ полем, а не прячется в адрес: адрес юзер
		// показывает и пересылает, а пароль — нет.
		return Address{}, refusal.New(http.StatusBadRequest, "bad-address",
			"в адресе скоупа не место паролю — он называется отдельным полем",
			"убери часть до собачки: http://10.8.0.5:8070/",
			"пароль пришли полем password")
	}
	if path := strings.Trim(u.Path, "/"); path != "" || u.RawQuery != "" || u.Fragment != "" {
		return Address{}, refusal.New(http.StatusBadRequest, "bad-address",
			fmt.Sprintf("в адресе %q назван путь внутри раздачи, а состояние лежит в корне и только в корне", raw),
			"назови корень: "+u.Scheme+"://"+u.Host+"/",
			"две раздачи — два скоупа, а не два пути в одной (`WORLD2` 3.4)")
	}

	u.Path = "/"
	return Address{Raw: raw, URL: u, Host: u.Hostname()}, nil
}

// Scope — доступ к состоянию по адресу. Ни копии, ни кэша внутри нет: каждое чтение и
// каждая запись идут туда, где скоуп лежит (`WORLD2` 1.6). Копия завела бы вторую истину о
// личности, и заметно это стало бы ровно тогда, когда человек вошёл с другой машины.
type Scope struct {
	Addr Address

	password string
	client   *http.Client
}

// Open готовит доступ. Связь при этом НЕ проверяется: проверка — отдельное действие
// (`Read`), и сливать их значило бы получить «открыли, но не знаем, есть ли что».
func Open(addr Address, password string, timeout int) *Scope {
	if timeout <= 0 {
		timeout = 10
	}
	return &Scope{
		Addr:     addr,
		password: password,
		client:   &http.Client{Timeout: time.Duration(timeout) * time.Second},
	}
}

// Presence — что стоит по адресу. Три состояния, и они разные: раздачи нет вовсе · раздача
// есть, состояния ещё нет · состояние лежит. Заведение скоупа опирается ровно на это.
type Presence int

const (
	// PresenceNone — по адресу никто не ответил. Раздачу надо поднять.
	PresenceNone Presence = iota
	// PresenceEmpty — раздача отвечает, а состояния в ней ещё нет (`404 no-state`). Это
	// не поломка, а развилка: свежая раздача так и отвечает.
	PresenceEmpty
	// PresenceState — по адресу лежит состояние.
	PresenceState
)

// Look — что по адресу: без разбора содержимого и без отказа на «никого нет». Заведение
// скоупа обязано различать «поднимать раздачу» и «писать в готовую».
func (s *Scope) Look(ctx context.Context) (Presence, *refusal.Refusal) {
	res, ref := s.do(ctx, http.MethodGet, nil)
	if ref != nil {
		if ref.Code == "no-route" || ref.Code == "no-answer" || ref.Code == "scope-silent" {
			return PresenceNone, nil
		}
		return PresenceNone, ref
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusOK:
		return PresenceState, nil
	case http.StatusNotFound:
		return PresenceEmpty, nil
	default:
		return PresenceNone, s.answerFailure(res)
	}
}

// Read — состояние по адресу, прочитанное как состояние. Всё, что могло не сойтись,
// названо ступенями: дорога · ответ · пароль · формат.
func (s *Scope) Read(ctx context.Context) (*state.State, *refusal.Refusal) {
	res, ref := s.do(ctx, http.MethodGet, nil)
	if ref != nil {
		return nil, ref
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// Раздача отвечает, а состояния в ней нет. Это развилка, а не поломка: свежая
		// раздача отвечает именно так, и заводящий скоуп на это опирается.
		return nil, refusal.New(http.StatusNotFound, "no-scope",
			fmt.Sprintf("по адресу %s раздача отвечает, а состояния в ней нет", s.Addr),
			"заведи его здесь же: POST /api/scope с этим адресом, паролем и именем",
			"или назови адрес, где состояние уже лежит")
	default:
		return nil, s.answerFailure(res)
	}

	data, err := io.ReadAll(io.LimitReader(res.Body, readLimit+1))
	if err != nil {
		return nil, refusal.New(http.StatusBadGateway, "scope-unreachable",
			fmt.Sprintf("раздача по адресу %s оборвала ответ на полпути: %v", s.Addr, err),
			"повтори попытку",
			"если повторяется — смотри журнал самой раздачи: она на машине юзера, а не здесь")
	}
	if len(data) > readLimit {
		return nil, refusal.New(http.StatusBadGateway, "state-too-big",
			fmt.Sprintf("по адресу %s отдают больше %d байт — столько состояние не весит", s.Addr, readLimit),
			"проверь, что по адресу стоит раздача скоупа, а не что-то другое")
	}
	return state.Parse(data)
}

// Write — состояние целиком. Ручек по разделам нет и не заводится: раздача принимает файл,
// и точка (`WORLD2` 3.4). Цена названа вслух там же: **последний пишущий затирает
// предыдущего**. Пока пульт один, этого не случится.
func (s *Scope) Write(ctx context.Context, st *state.State) *refusal.Refusal {
	data, err := st.Bytes()
	if err != nil {
		return refusal.New(http.StatusInternalServerError, "state-unwritable",
			"состояние не собралось в файл: "+err.Error(),
			"это дефект контроллера — заведи задачу зоне control")
	}

	res, ref := s.do(ctx, http.MethodPut, data)
	if ref != nil {
		return ref
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))

	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return nil
	default:
		return s.answerFailure(res)
	}
}

// ── разговор с раздачей ──────────────────────────────────────────────────────

func (s *Scope) do(ctx context.Context, method string, body []byte) (*http.Response, *refusal.Refusal) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.Addr.URL.String(), reader)
	if err != nil {
		return nil, refusal.New(http.StatusBadRequest, "bad-address",
			fmt.Sprintf("по адресу %s запрос не собрался: %v", s.Addr, err),
			"назови адрес раздачи целиком: http://10.8.0.5:8070/")
	}
	// Имя в `Basic` не смотрится — личность определяется адресом, а не именем (`WORLD2`
	// 3.4, «Розетка раздачи»). Пустое имя названо здесь явно, чтобы это было видно.
	req.SetBasicAuth("", s.password)
	if body != nil {
		// Тип называем честно: внутрь файла мы не смотрим, а раздача не разбирает его тем
		// более. Свой заголовок сюда не ставится ни один — узнавать «свою» раздачу нельзя.
		req.Header.Set("Content-Type", "application/octet-stream")
	}

	res, err := s.client.Do(req)
	if err != nil {
		return nil, s.reachFailure(err)
	}
	return res, nil
}

// reachFailure — ступени связи: дорога · ответ · тишина. Чинят их разные люди, и общий
// отказ «не дотянулись» отправляет человека чинить наугад (`WORLD2` 2.3). Ступени те же,
// что у шлюза (`gate/README.md`) и у соседних зон.
func (s *Scope) reachFailure(err error) *refusal.Refusal {
	where := s.Addr.String()

	var dns *net.DNSError
	if errors.As(err, &dns) {
		return refusal.New(http.StatusBadGateway, "no-route",
			fmt.Sprintf("имя %s не разрешается — до раздачи скоупа нет дороги", s.Addr.Host),
			"проверь адрес: "+where,
			"если машина за туннелем — подними его: это хозяйство машины, не мира")
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return refusal.New(http.StatusBadGateway, "no-answer",
			fmt.Sprintf("до %s дорога есть, а раздача по адресу %s не отвечает — порт закрыт", s.Addr.Host, where),
			"проверь, что раздача на той машине поднята: docker ps на ней покажет её контейнер",
			"подними её: POST /api/scope с этим адресом и машиной, где раздача должна стоять",
			"адрес назван с другим портом — проверь его: раздача по умолчанию слушает 8070")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return refusal.New(http.StatusGatewayTimeout, "scope-silent",
			fmt.Sprintf("раздача по адресу %s не ответила за отведённое время", where),
			"дай больше времени: CONTROL_SCOPE_TIMEOUT=30",
			"проверь, что машина с раздачей жива и её порт виден отсюда")
	}
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return refusal.New(http.StatusBadGateway, "no-route",
			fmt.Sprintf("до машины %s нет дороги", s.Addr.Host),
			"проверь адрес: "+where,
			"если машина за туннелем — подними его: это хозяйство машины, не мира")
	}
	return refusal.New(http.StatusBadGateway, "scope-unreachable",
		fmt.Sprintf("до раздачи по адресу %s дотянуться не вышло: %v", where, err),
		"проверь адрес и то, что раздача на той машине поднята",
		"подробность выше — она от сети, а не от мира")
}

// answerFailure — раздача ОТВЕТИЛА, но не тем. Разбираем СТАТУС, а тело берём подробностью:
// чужая вилка вправе ответить голым кодом без тела, и требовать от неё наших слов нельзя.
func (s *Scope) answerFailure(res *http.Response) *refusal.Refusal {
	where := s.Addr.String()
	detail := body(res)

	switch res.StatusCode {
	case http.StatusUnauthorized:
		return refusal.New(http.StatusUnauthorized, "bad-password",
			fmt.Sprintf("раздача по адресу %s пароль не приняла%s", where, detail),
			"проверь пароль скоупа — им закрыта раздача, и внутри файла состояния его нет",
			"пароль называется при подъёме раздачи (SHARE_PASSWORD у нашей вилки)",
			"адрес мог указывать на ЧУЖУЮ раздачу — проверь его")
	case http.StatusForbidden:
		return refusal.New(http.StatusForbidden, "access-denied",
			fmt.Sprintf("раздача по адресу %s пускать отказалась%s", where, detail),
			"это решение той раздачи, а не мира: смотри её правила и её журнал")
	case http.StatusRequestEntityTooLarge:
		return refusal.New(http.StatusRequestEntityTooLarge, "state-too-big",
			fmt.Sprintf("раздача по адресу %s не приняла состояние: оно больше её предела записи%s", where, detail),
			"подними предел у раздачи (SHARE_LIMIT у нашей вилки)",
			"или убери из скоупа лишнее — ключи и территории, которыми не пользуешься")
	case http.StatusMethodNotAllowed:
		return refusal.New(http.StatusBadGateway, "not-a-share",
			fmt.Sprintf("по адресу %s кто-то отвечает, но двух ручек скоупа у него нет%s", where, detail),
			"проверь адрес: раздача отдаёт состояние по GET и принимает по PUT в корне",
			"форма стыковки целиком — `WORLD2` 3.4, раздел «Розетка раздачи»")
	default:
		return refusal.New(http.StatusBadGateway, "share-failed",
			fmt.Sprintf("раздача по адресу %s ответила %d%s", where, res.StatusCode, detail),
			"это ответ той раздачи, а не мира — смотри её журнал: она на машине юзера",
			"проверь, что по адресу стоит раздача скоупа, а не другая вещь")
	}
}

// body — тело чужого ответа коротким куском. Тело здесь ПОДРОБНОСТЬ, а не источник кода:
// решение принято по статусу выше, и голое тело ничего не меняет.
func body(res *http.Response) string {
	data, err := io.ReadAll(io.LimitReader(res.Body, 400))
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return ""
	}
	return " — она сказала: " + strings.ReplaceAll(text, "\n", " ")
}
