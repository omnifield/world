package door

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Пробы этого пакета устроены так, чтобы КРАСНЕТЬ (`kb:WORLD-32`): на каждый
// отказ есть тест, который вызывает его нарочно, а не только счастливый путь.
// Отказ проверяется тремя вещами сразу — код HTTP, машинный код и ВЫХОД в
// тексте: отказ без выхода каноном считается провалом (`kb:WORLD-31`), и
// проверка «просто не 200» этого не поймала бы.

// журнал — собиратель строк трассировки. С мьютексом: часть тестов шлёт
// запросы параллельно, и гонка в самом тесте маскировала бы гонку в коде.
type журнал struct {
	mu     sync.Mutex
	строки []string
}

func (ж *журнал) записать(format string, args ...any) {
	ж.mu.Lock()
	defer ж.mu.Unlock()
	ж.строки = append(ж.строки, fmt.Sprintf(format, args...))
}

func (ж *журнал) всё() string {
	ж.mu.Lock()
	defer ж.mu.Unlock()
	return strings.Join(ж.строки, "\n")
}

func фиксНоль() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }

// идущиеЧасы — время, которое ИДЁТ. Тождество места проверяется датами, и на
// застывших часах «первый вход» и «возвращение» неразличимы: проба позеленела
// бы, даже если бы дверь переписывала историю каждой регистрацией.
func идущиеЧасы() func() time.Time {
	var mu sync.Mutex
	т := фиксНоль()
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		т = т.Add(time.Minute)
		return т
	}
}

// дверь поднимает реестр во временном файле и ручки над ним.
// probe == nil — настоящая проба соединением: в тестах локации живые, и
// подменять здесь нечего.
func дверь(t *testing.T, probe Probe) (*Handler, *журнал, string) {
	t.Helper()
	return дверьСЧасами(t, probe, фиксНоль)
}

func дверьСЧасами(t *testing.T, probe Probe, now func() time.Time) (*Handler, *журнал, string) {
	t.Helper()
	файл := filepath.Join(t.TempDir(), "field", "locations.json")
	reg, err := Open(файл)
	if err != nil {
		t.Fatalf("реестр не поднялся: %v", err)
	}
	ж := &журнал{}
	return NewHandler(reg, ж.записать, now, probe), ж, файл
}

// эхо — живая локация за дверью. Отвечает тем, ЧТО до неё доехало: иначе
// «префикс срезался» проверяется на слово.
type эхо struct {
	сервер *httptest.Server
	mu     sync.Mutex
	путь   string
	метод  string
	хост   string
	тело   string
}

func локация(t *testing.T, ответ string) *эхо {
	t.Helper()
	e := &эхо{}
	e.сервер = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		тело := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(тело)
		}
		e.mu.Lock()
		e.путь = r.URL.RequestURI()
		e.метод = r.Method
		e.хост = r.Header.Get("X-Forwarded-Host")
		e.тело = string(тело)
		e.mu.Unlock()
		fmt.Fprint(w, ответ)
	}))
	t.Cleanup(e.сервер.Close)
	return e
}

func (e *эхо) адрес() string { return strings.TrimPrefix(e.сервер.URL, "http://") }

// снести гасит локацию посреди прогона: пересоздание контейнера иначе не
// изобразить, а именно оно — главный случай тождества места.
func (e *эхо) снести() { e.сервер.Close() }

func (e *эхо) видела() (путь, метод, хост, тело string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.путь, e.метод, e.хост, e.тело
}

// закрытыйАдрес — адрес, по которому заведомо никто не отвечает. Берётся у
// живого слушателя и тут же гасится: выдуманный порт мог бы оказаться занят
// чужим процессом, и проба позеленела бы не на том.
func закрытыйАдрес(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("слушатель не поднялся: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("слушатель не гасится: %v", err)
	}
	return addr
}

func запрос(h http.Handler, метод, путь, тело string) *httptest.ResponseRecorder {
	var r *http.Request
	if тело == "" {
		r = httptest.NewRequest(метод, путь, nil)
	} else {
		r = httptest.NewRequest(метод, путь, strings.NewReader(тело))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func регистрация(имя, адрес, даёт string) string {
	b, _ := json.Marshal(registration{Name: имя, Addr: адрес, Gives: даёт})
	return string(b)
}

// отказ разбирает тело как отказ мира и проверяет форму целиком: код, машинное
// имя и НАЗВАННЫЙ ВЫХОД. Проверять только код бессмысленно — по канону отказ
// без выхода это провал, а не мелочь.
func отказ(t *testing.T, rec *httptest.ResponseRecorder, код int, имя, выход string) {
	t.Helper()
	if rec.Code != код {
		t.Fatalf("код ответа: получено %d (%s), ожидалось %d", rec.Code, rec.Body.String(), код)
	}
	var got apiError
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("отказ не разбирается как JSON: %v (тело: %s)", err, rec.Body.String())
	}
	if got.Code != имя {
		t.Errorf("код отказа: получено %q, ожидалось %q (detail: %s)", got.Code, имя, got.Detail)
	}
	if got.Detail == "" {
		t.Fatalf("отказ %q не назвал причину — по kb:WORLD-31 это провал, а не мелочь", got.Code)
	}
	if !strings.Contains(got.Detail, выход) {
		t.Errorf("отказ %q не называет выход: получено %q, ожидалась подстрока %q", got.Code, got.Detail, выход)
	}
}

// ── счастливый путь: регистрация и есть подключение ──────────────────────────

func TestРегистрацияДаётМаршрутСразу(t *testing.T) {
	h, _, _ := дверь(t, nil)
	e := локация(t, "я локация")

	rec := запрос(h, http.MethodPost, Prefix, регистрация("baser", e.адрес(), "консоль сборки"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("регистрация: получено %d (%s), ожидалось %d", rec.Code, rec.Body.String(), http.StatusCreated)
	}

	var ответ struct {
		Location Location `json:"location"`
		Route    string   `json:"route"`
		Created  bool     `json:"created"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ответ); err != nil {
		t.Fatalf("ответ не разбирается: %v (%s)", err, rec.Body.String())
	}
	// Локация обязана узнать из ОТВЕТА, где она теперь достижима: иначе
	// «регистрация = подключение» держится на договорённости, а не на ответе.
	if ответ.Route != "/baser/" {
		t.Errorf("маршрут в ответе: получено %q, ожидалось %q", ответ.Route, "/baser/")
	}
	if !ответ.Created || ответ.Location.Since != "2026-08-10T12:00:00Z" {
		t.Errorf("ответ: получено %+v, ожидалась новая локация с временем регистрации", ответ)
	}

	маршрут := h.Route(статикаНеОтвечает(t))
	if rec := запрос(маршрут, http.MethodGet, "/baser/api/x?q=1", ""); rec.Body.String() != "я локация" {
		t.Fatalf("через дверь пришло %q, ожидалось %q (код %d)", rec.Body.String(), "я локация", rec.Code)
	}
	if путь, _, _, _ := e.видела(); путь != "/api/x?q=1" {
		t.Errorf("локация увидела путь %q, ожидалось %q — префикс имени обязан срезаться", путь, "/api/x?q=1")
	}
}

// статикаНеОтвечает — заглушка fallback'а: если запрос до неё дошёл, значит
// маршрут локации НЕ сработал, и тест обязан это увидеть, а не проглотить.
func статикаНеОтвечает(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "статика: "+r.URL.Path, http.StatusNotFound)
	})
}

func TestЗапросДоезжаетДоЛокацииЦеликом(t *testing.T) {
	h, _, _ := дверь(t, nil)
	e := локация(t, "принято")
	запрос(h, http.MethodPost, Prefix, регистрация("baser", e.адрес(), "консоль сборки"))

	маршрут := h.Route(статикаНеОтвечает(t))
	rec := запрос(маршрут, http.MethodPost, "/baser/api/build?force=1", `{"схема":"дом"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("код ответа: получено %d (%s), ожидалось %d", rec.Code, rec.Body.String(), http.StatusOK)
	}

	путь, метод, хост, тело := e.видела()
	if путь != "/api/build?force=1" {
		t.Errorf("путь: получено %q, ожидалось %q", путь, "/api/build?force=1")
	}
	if метод != http.MethodPost {
		t.Errorf("метод: получено %q, ожидалось %q", метод, http.MethodPost)
	}
	if тело != `{"схема":"дом"}` {
		t.Errorf("тело: получено %q, ожидалось %q", тело, `{"схема":"дом"}`)
	}
	// Локация живёт у себя в корне и о своём имени за дверью не знает — но
	// узнать, из-за какой двери пришёл запрос, обязана мочь.
	if хост == "" {
		t.Error("X-Forwarded-Host пуст: локация не может узнать, через какую дверь к ней пришли")
	}
}

func TestИмяБезЗавершающегоСлэшаВедётВЛокацию(t *testing.T) {
	h, _, _ := дверь(t, nil)
	e := локация(t, "я локация")
	запрос(h, http.MethodPost, Prefix, регистрация("baser", e.адрес(), "консоль сборки"))

	rec := запрос(h.Route(статикаНеОтвечает(t)), http.MethodGet, "/baser?q=1", "")
	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("код ответа: получено %d, ожидалось %d", rec.Code, http.StatusPermanentRedirect)
	}
	// Без слэша относительные ссылки внутри локации считались бы от корня
	// двери и уехали бы мимо — вопрос ровно в этом, а не в косметике адреса.
	if куда := rec.Header().Get("Location"); куда != "/baser/?q=1" {
		t.Errorf("редирект ведёт в %q, ожидалось %q", куда, "/baser/?q=1")
	}
}

func TestСнятиеУбираетМаршрут(t *testing.T) {
	h, _, _ := дверь(t, nil)
	e := локация(t, "я локация")
	запрос(h, http.MethodPost, Prefix, регистрация("baser", e.адрес(), "консоль сборки"))

	маршрут := h.Route(статикаНеОтвечает(t))
	if rec := запрос(маршрут, http.MethodGet, "/baser/", ""); rec.Code != http.StatusOK {
		t.Fatalf("до снятия маршрут обязан работать: получено %d (%s)", rec.Code, rec.Body.String())
	}

	if rec := запрос(h, http.MethodDelete, Prefix+"/baser", ""); rec.Code != http.StatusOK {
		t.Fatalf("снятие: получено %d (%s), ожидалось %d", rec.Code, rec.Body.String(), http.StatusOK)
	}

	rec := запрос(маршрут, http.MethodGet, "/baser/", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "статика:") {
		t.Errorf("после снятия маршрут обязан исчезнуть: получено %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestСписокПереживаетРестарт(t *testing.T) {
	h, _, файл := дверь(t, nil)
	e := локация(t, "я локация")
	запрос(h, http.MethodPost, Prefix, регистрация("baser", e.адрес(), "консоль сборки"))

	// Второй Handler поверх ТОГО ЖЕ файла — это и есть рестарт двери:
	// прежний экземпляр ничего ему не передаёт, всё знание лежит на диске.
	reg, err := Open(файл)
	if err != nil {
		t.Fatalf("реестр после рестарта не поднялся: %v", err)
	}
	после := NewHandler(reg, func(string, ...any) {}, фиксНоль, nil)

	locs := reg.List()
	if len(locs) != 1 || locs[0].Name != "baser" || locs[0].Addr != e.адрес() {
		t.Fatalf("поле после рестарта: получено %+v, ожидалась одна локация baser", locs)
	}
	if rec := запрос(после.Route(статикаНеОтвечает(t)), http.MethodGet, "/baser/", ""); rec.Body.String() != "я локация" {
		t.Errorf("маршрут после рестарта: получено %q (код %d)", rec.Body.String(), rec.Code)
	}
}

func TestСписокОтдаётПолеПоИменам(t *testing.T) {
	h, _, _ := дверь(t, nil)
	a, b := локация(t, "a"), локация(t, "b")
	запрос(h, http.MethodPost, Prefix, регистрация("zeta", a.адрес(), "первая"))
	запрос(h, http.MethodPost, Prefix, регистрация("alpha", b.адрес(), "вторая"))

	rec := запрос(h, http.MethodGet, Prefix, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("список: получено %d (%s)", rec.Code, rec.Body.String())
	}
	var ответ struct {
		Count     int `json:"count"`
		Locations []struct {
			Name, Route, Gives string
		} `json:"locations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ответ); err != nil {
		t.Fatalf("список не разбирается: %v (%s)", err, rec.Body.String())
	}
	if ответ.Count != 2 || ответ.Locations[0].Name != "alpha" || ответ.Locations[1].Name != "zeta" {
		t.Fatalf("список: получено %+v, ожидались alpha и zeta по именам", ответ)
	}
	// Список за дверью рисуется по этому ответу — без маршрута ссылку из него
	// не построить, и страница снова стала бы ручной разметкой.
	if ответ.Locations[0].Route != "/alpha/" {
		t.Errorf("маршрут в списке: получено %q, ожидалось %q", ответ.Locations[0].Route, "/alpha/")
	}
}

func TestПеререгистрацияТемЖеАдресомНеОтказ(t *testing.T) {
	h, _, _ := дверь(t, nil)
	e := локация(t, "я локация")
	запрос(h, http.MethodPost, Prefix, регистрация("baser", e.адрес(), "старое описание"))

	rec := запрос(h, http.MethodPost, Prefix, регистрация("baser", e.адрес(), "новое описание"))
	if rec.Code != http.StatusOK {
		t.Fatalf("перерегистрация: получено %d (%s), ожидалось %d — локация переобъявляется на каждом подъёме",
			rec.Code, rec.Body.String(), http.StatusOK)
	}
	var ответ struct {
		Location Location `json:"location"`
		Created  bool     `json:"created"`
	}
	json.Unmarshal(rec.Body.Bytes(), &ответ)
	if ответ.Created || ответ.Location.Gives != "новое описание" {
		t.Errorf("перерегистрация: получено %+v, ожидалось created=false и обновлённое описание", ответ)
	}
}

// ── тождество места: то же оно или новое (`tasker:WORLD-81`) ─────────────────

// ответРегистрации — всё, что дверь говорит о месте на входе. Исход отдельным
// полем: три исхода в флаг `created` не влезают.
type ответРегистрации struct {
	Location Location `json:"location"`
	Route    string   `json:"route"`
	Created  bool     `json:"created"`
	Outcome  Outcome  `json:"outcome"`
}

func зарегистрировать(t *testing.T, h *Handler, имя, адрес, даёт string, ждём int) ответРегистрации {
	t.Helper()
	rec := запрос(h, http.MethodPost, Prefix, регистрация(имя, адрес, даёт))
	if rec.Code != ждём {
		t.Fatalf("регистрация %q по адресу %s: получено %d (%s), ожидалось %d",
			имя, адрес, rec.Code, rec.Body.String(), ждём)
	}
	var ответ ответРегистрации
	if err := json.Unmarshal(rec.Body.Bytes(), &ответ); err != nil {
		t.Fatalf("ответ регистрации не разбирается: %v (%s)", err, rec.Body.String())
	}
	return ответ
}

// вПоле — реестр глазами читателя: ровно то, что переживёт рестарт двери.
func вПоле(t *testing.T, файл string) []Location {
	t.Helper()
	reg, err := Open(файл)
	if err != nil {
		t.Fatalf("реестр не перечитывается: %v", err)
	}
	return reg.List()
}

// ГЛАВНАЯ проба задачи: пересоздали контейнер, адрес другой — место ТО ЖЕ.
// В поле обязана остаться ОДНА запись со своей историей, а не вторая рядом.
func TestПересозданиеСДругимАдресомДаётТоЖеМесто(t *testing.T) {
	h, ж, файл := дверьСЧасами(t, nil, идущиеЧасы())
	прежняя := локация(t, "старый дом")

	первый := зарегистрировать(t, h, "baser", прежняя.адрес(), "консоль сборки", http.StatusCreated)
	if первый.Outcome != OutcomeCreated || !первый.Created {
		t.Fatalf("первый вход: получено %+v, ожидался исход %q", первый, OutcomeCreated)
	}
	// Маршрут строим ДО пересоздания: иначе проверка «маршрут перевёлся» прошла
	// бы на пустом кэше, то есть не проверила бы ничего.
	if rec := запрос(h.Route(статикаНеОтвечает(t)), http.MethodGet, "/baser/", ""); rec.Body.String() != "старый дом" {
		t.Fatalf("до пересоздания маршрут ведёт в %q (код %d)", rec.Body.String(), rec.Code)
	}

	// Пересоздание: прежний контейнер снесён, новый встал по ДРУГОМУ адресу.
	прежняя.снести()
	новая := локация(t, "новый дом")
	вернулась := зарегистрировать(t, h, "baser", новая.адрес(), "консоль сборки", http.StatusOK)

	if вернулась.Outcome != OutcomeReturned {
		t.Fatalf("исход: получено %q, ожидалось %q — пересоздание это ВОЗВРАЩЕНИЕ, а не создание",
			вернулась.Outcome, OutcomeReturned)
	}
	if вернулась.Created {
		t.Error("возвращение выдано за создание: по этому полю печатается «вошла», а место уже было в поле")
	}
	if вернулась.Location.Since != первый.Location.Since {
		t.Errorf("время первого входа: получено %q, ожидалось %q — возвращение обязано его сохранить",
			вернулась.Location.Since, первый.Location.Since)
	}
	if вернулась.Location.Returned == "" || вернулась.Location.Returned == вернулась.Location.Since {
		t.Errorf("метка возвращения: получено %q при since=%q — без неё «то же место» нечем подтвердить",
			вернулась.Location.Returned, вернулась.Location.Since)
	}
	if вернулась.Location.Addr != новая.адрес() {
		t.Errorf("адрес: получено %q, ожидалось %q", вернулась.Location.Addr, новая.адрес())
	}

	// Прежний сосед ушёл из поля целиком — держать его пул соединений незачем.
	// Снаружи это не видно (proxyFor и сам сверит адрес), поэтому смотрим внутрь:
	// маршрут построен ДО пересоздания и обязан быть забыт вместе с адресом.
	if len(h.proxies) != 0 {
		t.Errorf("после возвращения осталось %d построенных маршрутов к ушедшему адресу, ожидалось 0", len(h.proxies))
	}

	// Одна запись, а не две: ради этого всё и делается. Проверяем ДИСК —
	// реестр, разошедшийся с файлом, переживёт рестарт неправдой.
	поле := вПоле(t, файл)
	if len(поле) != 1 || поле[0].Addr != новая.адрес() || поле[0].Since != первый.Location.Since {
		t.Fatalf("поле после пересоздания: получено %+v, ожидалась одна запись baser по новому адресу с прежним since", поле)
	}
	if поле[0].Returned != вернулась.Location.Returned {
		t.Errorf("история на диске: получено %q, в ответе %q", поле[0].Returned, вернулась.Location.Returned)
	}

	// Маршрут обязан перевестись на новый адрес — иначе «то же место» верно
	// только на бумаге, а запросы уезжают в снесённый контейнер.
	if rec := запрос(h.Route(статикаНеОтвечает(t)), http.MethodGet, "/baser/", ""); rec.Body.String() != "новый дом" {
		t.Errorf("после возвращения маршрут ведёт в %q, ожидалось %q (код %d)", rec.Body.String(), "новый дом", rec.Code)
	}
	// Возвращение — единственный случай, когда дверь меняет смысл записи;
	// без строки в журнале это остаётся словом.
	if !strings.Contains(ж.всё(), "возвращение") {
		t.Errorf("возвращение не названо в трассировке:\n%s", ж.всё())
	}
}

// Второй исход того же места: чужой пришёл на имя, за которым СТОИТ живая
// локация. Отказ прежний, и место в поле от этого не меняется ничем.
func TestЧужойНаЗанятомИмениНеПодменяетМесто(t *testing.T) {
	h, _, файл := дверьСЧасами(t, nil, идущиеЧасы())
	своя, чужая := локация(t, "своя"), локация(t, "чужая")
	первый := зарегистрировать(t, h, "baser", своя.адрес(), "консоль сборки", http.StatusCreated)

	rec := запрос(h, http.MethodPost, Prefix, регистрация("baser", чужая.адрес(), "чужая консоль"))
	отказ(t, rec, http.StatusConflict, "name-taken", "DELETE /api/locations/baser")
	// Отказ обязан называть ПРИЧИНУ, а не только выход: разница между «место
	// стоит» и «место ушло» — это вся суть задачи, и человек должен её видеть.
	if !strings.Contains(rec.Body.String(), "отвечает прямо сейчас") {
		t.Errorf("отказ не называет, ПОЧЕМУ это не возвращение: %s", rec.Body.String())
	}

	поле := вПоле(t, файл)
	if len(поле) != 1 || поле[0].Addr != своя.адрес() || поле[0].Gives != "консоль сборки" {
		t.Fatalf("поле после отказа: получено %+v, ожидалась нетронутая запись baser", поле)
	}
	if поле[0].Since != первый.Location.Since || поле[0].Returned != "" {
		t.Errorf("отказ тронул историю места: получено %+v", поле[0])
	}
}

// Третий исход: тот же адрес — подтверждение присутствия. История при этом НЕ
// сбрасывается: иначе после каждого рестарта место выглядело бы новорождённым,
// и «в поле с» врало бы ровно в тот момент, когда на него смотрят.
func TestПодтверждениеПрисутствияНеСбрасываетИсторию(t *testing.T) {
	h, _, файл := дверьСЧасами(t, nil, идущиеЧасы())
	своя := локация(t, "я локация")

	первый := зарегистрировать(t, h, "baser", своя.адрес(), "старое описание", http.StatusCreated)
	второй := зарегистрировать(t, h, "baser", своя.адрес(), "новое описание", http.StatusOK)

	if второй.Outcome != OutcomeConfirmed || второй.Created {
		t.Fatalf("исход: получено %+v, ожидалось %q", второй, OutcomeConfirmed)
	}
	if второй.Location.Since != первый.Location.Since {
		t.Errorf("время первого входа в ОТВЕТЕ: получено %q, ожидалось %q — ответ обязан говорить то же, что реестр",
			второй.Location.Since, первый.Location.Since)
	}
	if второй.Location.Returned != "" {
		t.Errorf("подтверждение выдано за возвращение: returned=%q — адрес не менялся", второй.Location.Returned)
	}
	if второй.Location.Gives != "новое описание" {
		t.Errorf("описание: получено %q, ожидалось обновлённое", второй.Location.Gives)
	}
	if поле := вПоле(t, файл); len(поле) != 1 || поле[0].Since != первый.Location.Since || поле[0].Returned != "" {
		t.Errorf("поле после подтверждения: получено %+v", поле)
	}
}

// Снятие — это снятие: мир не помнит снятое место и не притворяется, что помнит.
// Вернувшийся под тем же именем после DELETE — НОВАЯ локация со своей историей.
func TestПослеСнятияТоЖеИмяДаётНовоеМесто(t *testing.T) {
	h, _, файл := дверьСЧасами(t, nil, идущиеЧасы())
	первая := локация(t, "первый дом")

	было := зарегистрировать(t, h, "baser", первая.адрес(), "консоль", http.StatusCreated)
	if rec := запрос(h, http.MethodDelete, Prefix+"/baser", ""); rec.Code != http.StatusOK {
		t.Fatalf("снятие: получено %d (%s)", rec.Code, rec.Body.String())
	}

	вторая := локация(t, "второй дом")
	стало := зарегистрировать(t, h, "baser", вторая.адрес(), "консоль", http.StatusCreated)

	if стало.Outcome != OutcomeCreated {
		t.Fatalf("исход после снятия: получено %q, ожидалось %q — снятое место миру неизвестно",
			стало.Outcome, OutcomeCreated)
	}
	if стало.Location.Since == было.Location.Since {
		t.Errorf("время входа: получено прежнее %q — значит от снятого места осталась запись", стало.Location.Since)
	}
	if стало.Location.Returned != "" {
		t.Errorf("новое место выдано за вернувшееся: returned=%q", стало.Location.Returned)
	}
	if поле := вПоле(t, файл); len(поле) != 1 || поле[0].Returned != "" {
		t.Errorf("поле после снятия и новой регистрации: получено %+v", поле)
	}
}

// Прежний молчит, но и новый адрес не отвечает — это НЕ возвращение, а мёртвая
// запись. Место в поле обязано остаться прежним: потерять стоящую локацию из-за
// чужой неудачной попытки хуже, чем отказать.
func TestВозвращениеСМёртвымАдресомНеТрогаетПоле(t *testing.T) {
	h, _, файл := дверьСЧасами(t, nil, идущиеЧасы())
	прежняя := локация(t, "старый дом")
	первый := зарегистрировать(t, h, "baser", прежняя.адрес(), "консоль", http.StatusCreated)
	прежняя.снести()

	rec := запрос(h, http.MethodPost, Prefix, регистрация("baser", закрытыйАдрес(t), "консоль"))
	отказ(t, rec, http.StatusBadGateway, "addr-unreachable", "подними локацию")

	поле := вПоле(t, файл)
	if len(поле) != 1 || поле[0].Addr != прежняя.адрес() || поле[0].Since != первый.Location.Since {
		t.Fatalf("поле после отказа: получено %+v, ожидалась нетронутая запись baser", поле)
	}
	if поле[0].Returned != "" {
		t.Errorf("неудавшаяся регистрация отметилась возвращением: %+v", поле[0])
	}
}

// История видна в СПИСКЕ, а не только в ответе на регистрацию: пульт и `world
// who` читают именно его, и «то же место» подтверждается там же, где смотрят.
func TestСписокПоказываетИсториюМеста(t *testing.T) {
	h, _, _ := дверьСЧасами(t, nil, идущиеЧасы())
	прежняя := локация(t, "старый дом")
	зарегистрировать(t, h, "baser", прежняя.адрес(), "консоль", http.StatusCreated)

	// Пока место не возвращалось, поля возвращения в списке нет вовсе: пустая
	// дата читается как «вернулось в никогда», а мир не пишет того, чего не знает.
	rec := запрос(h, http.MethodGet, Prefix, "")
	if strings.Contains(rec.Body.String(), "returned") {
		t.Errorf("в списке есть метка возвращения у места, которое не возвращалось: %s", rec.Body.String())
	}

	прежняя.снести()
	новая := локация(t, "новый дом")
	вернулась := зарегистрировать(t, h, "baser", новая.адрес(), "консоль", http.StatusOK)

	rec = запрос(h, http.MethodGet, Prefix, "")
	var ответ struct {
		Count     int `json:"count"`
		Locations []struct {
			Name, Addr, Gives, Since, Returned, Route string
		} `json:"locations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ответ); err != nil {
		t.Fatalf("список не разбирается: %v (%s)", err, rec.Body.String())
	}
	if ответ.Count != 1 {
		t.Fatalf("в поле %d записей, ожидалась одна: %s", ответ.Count, rec.Body.String())
	}
	l := ответ.Locations[0]
	if l.Since != вернулась.Location.Since || l.Returned != вернулась.Location.Returned {
		t.Errorf("история в списке: получено since=%q returned=%q, в реестре since=%q returned=%q",
			l.Since, l.Returned, вернулась.Location.Since, вернулась.Location.Returned)
	}
	// Прежние поля списка не тронуты: пульт читает их и ломать его нельзя.
	if l.Name != "baser" || l.Route != "/baser/" || l.Addr != новая.адрес() || l.Gives != "консоль" {
		t.Errorf("список потерял прежние поля: %+v", l)
	}
}

func TestСнятиеЗабываетПостроенныйМаршрут(t *testing.T) {
	h, _, _ := дверь(t, nil)
	e := локация(t, "я локация")
	запрос(h, http.MethodPost, Prefix, регистрация("baser", e.адрес(), "консоль"))
	запрос(h.Route(статикаНеОтвечает(t)), http.MethodGet, "/baser/", "")

	if len(h.proxies) != 1 {
		t.Fatalf("построенных маршрутов %d, ожидался один", len(h.proxies))
	}
	запрос(h, http.MethodDelete, Prefix+"/baser", "")

	// Ответ ручки этого не покажет — снаружи маршрут исчезает в любом случае.
	// Проверяется то, что остаётся ВНУТРИ: запись об ушедшем соседе и его пул
	// соединений жили бы до перезапуска двери, а поле уже другое.
	if len(h.proxies) != 0 {
		t.Errorf("после снятия осталось %d построенных маршрутов, ожидалось 0", len(h.proxies))
	}
}

func TestПереездЛокацииЧерезСнятиеПереводитМаршрут(t *testing.T) {
	h, _, _ := дверь(t, nil)
	старая, новая := локация(t, "старый дом"), локация(t, "новый дом")
	запрос(h, http.MethodPost, Prefix, регистрация("baser", старая.адрес(), "консоль"))
	запрос(h, http.MethodDelete, Prefix+"/baser", "")
	запрос(h, http.MethodPost, Prefix, регистрация("baser", новая.адрес(), "консоль"))

	// Проверяется не реестр, а ПОСТРОЕННЫЙ маршрут: он кэшируется между
	// запросами, и забыть его при снятии — ровно тот дефект, из-за которого
	// переехавшая локация продолжала бы отвечать со старого адреса.
	rec := запрос(h.Route(статикаНеОтвечает(t)), http.MethodGet, "/baser/", "")
	if rec.Body.String() != "новый дом" {
		t.Errorf("после переезда получено %q, ожидалось %q", rec.Body.String(), "новый дом")
	}
}

func TestПостроенныйМаршрутПересобираетсяПриСменеАдреса(t *testing.T) {
	// Проба прямая, по самому proxyFor, а не через ручки: снаружи этот путь
	// сегодня недостижим (смена адреса идёт только через снятие). Но кэш
	// маршрутов обязан быть верным САМ, а не потому, что снятие не забыло
	// его почистить: такая связь не переживёт первой же правки соседнего кода.
	h, _, _ := дверь(t, nil)
	старая, новая := локация(t, "старый дом"), локация(t, "новый дом")

	loc := Location{Name: "baser", Addr: старая.адрес(), Gives: "консоль"}
	rp := h.proxyFor(loc)
	loc.Addr = новая.адрес()

	rec := httptest.NewRecorder()
	h.proxyFor(loc).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/baser/", nil))
	if rec.Body.String() != "новый дом" {
		t.Errorf("маршрут после смены адреса ведёт в %q, ожидалось %q", rec.Body.String(), "новый дом")
	}

	rec = httptest.NewRecorder()
	rp.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/baser/", nil))
	if rec.Body.String() != "старый дом" {
		t.Errorf("прежний маршрут подменён на месте: получено %q — старые запросы уехали бы не туда", rec.Body.String())
	}
}

func TestЧужоеИмяУходитВСтатику(t *testing.T) {
	h, _, _ := дверь(t, nil)

	rec := запрос(h.Route(статикаНеОтвечает(t)), http.MethodGet, "/незнакомец/", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "статика:") {
		t.Errorf("незарегистрированное имя: получено %d (%s), ожидался уход в статику", rec.Code, rec.Body.String())
	}
}

// ── отказы: каждый вызывается нарочно ────────────────────────────────────────

func TestОтказИмяЗанятоДругойЛокацией(t *testing.T) {
	h, _, _ := дверь(t, nil)
	первая, вторая := локация(t, "первая"), локация(t, "вторая")
	запрос(h, http.MethodPost, Prefix, регистрация("baser", первая.адрес(), "консоль"))

	rec := запрос(h, http.MethodPost, Prefix, регистрация("baser", вторая.адрес(), "другая консоль"))
	отказ(t, rec, http.StatusConflict, "name-taken", "DELETE /api/locations/baser")

	// Отказ обязан быть СНИМАЕМЫМ (`kb:WORLD-31`): снял ту локацию — то же
	// действие проходит. Отказ, который не снимается, неотличим от закрытой двери.
	if rec := запрос(h, http.MethodDelete, Prefix+"/baser", ""); rec.Code != http.StatusOK {
		t.Fatalf("снятие: получено %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := запрос(h, http.MethodPost, Prefix, регистрация("baser", вторая.адрес(), "другая консоль")); rec.Code != http.StatusCreated {
		t.Errorf("после снятия та же регистрация обязана пройти: получено %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestЗанятоеИмяНазываетсяРаньшеМёртвогоАдреса(t *testing.T) {
	h, _, _ := дверь(t, nil)
	первая := локация(t, "первая")
	запрос(h, http.MethodPost, Prefix, регистрация("baser", первая.адрес(), "консоль"))

	// Оба отказа верны, но блокирует ИМЯ: узнав про адрес, автор починит адрес
	// и упрётся в то же самое имя. Мир обязан называть причину, а не первое,
	// обо что споткнулся.
	rec := запрос(h, http.MethodPost, Prefix, регистрация("baser", закрытыйАдрес(t), "другая консоль"))
	отказ(t, rec, http.StatusConflict, "name-taken", "DELETE /api/locations/baser")
}

func TestОтказАдресНеОтвечает(t *testing.T) {
	h, _, файл := дверь(t, nil)

	rec := запрос(h, http.MethodPost, Prefix, регистрация("baser", закрытыйАдрес(t), "консоль"))
	отказ(t, rec, http.StatusBadGateway, "addr-unreachable", "подними локацию")

	// Реестр — состояние поля: запись о локации, которой нет, вернула бы список
	// в прежнее состояние «чья-то память».
	if locs := h.reg.List(); len(locs) != 0 {
		t.Errorf("в поле осталась запись о непроверенном адресе: %+v", locs)
	}
	if _, err := os.Stat(файл); err == nil {
		t.Errorf("файл реестра создан отказанной регистрацией: %s", файл)
	}
}

func TestОтказЛокацияНеОтвечаетНаМаршруте(t *testing.T) {
	// Проба согласна, а локация к моменту запроса легла: это не ошибка
	// регистрации, и отказ обязан отличаться от неё и называть свою локацию.
	мёртвый := закрытыйАдрес(t)
	h, _, _ := дверь(t, func(string) error { return nil })
	запрос(h, http.MethodPost, Prefix, регистрация("baser", мёртвый, "консоль"))

	rec := запрос(h.Route(статикаНеОтвечает(t)), http.MethodGet, "/baser/", "")
	отказ(t, rec, http.StatusBadGateway, "location-unreachable", "DELETE /api/locations/baser")
	if !strings.Contains(rec.Body.String(), "baser") {
		t.Errorf("отказ не назвал локацию: %s", rec.Body.String())
	}
}

func TestОтказИмяНеПоПравилуМаршрута(t *testing.T) {
	h, _, _ := дверь(t, func(string) error { return nil })

	tests := []struct {
		name, имя, код, выход string
	}{
		{"пустое", "", "field-missing", "маршрут"},
		{"заглавные", "Baser", "name-invalid", "строчные латинские"},
		{"подчёркивание", "ba_ser", "name-invalid", "строчные латинские"},
		{"начинается с дефиса", "-baser", "name-invalid", "строчные латинские"},
		{"кириллица", "база", "name-invalid", "строчные латинские"},
		{"слэш внутри", "baser/x", "name-invalid", "строчные латинские"},
		{"длиннее метки в сети", strings.Repeat("a", 64), "name-invalid", "строчные латинские"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := запрос(h, http.MethodPost, Prefix, регистрация(tt.имя, "baser:3500", "консоль"))
			отказ(t, rec, http.StatusBadRequest, tt.код, tt.выход)
		})
	}
}

func TestОтказИмяЗанятоДверью(t *testing.T) {
	h, _, _ := дверь(t, func(string) error { return nil })

	for _, имя := range ReservedNames {
		t.Run(имя, func(t *testing.T) {
			rec := запрос(h, http.MethodPost, Prefix, регистрация(имя, "baser:3500", "консоль"))
			// Такая локация зарегистрировалась бы и была бы недостижима —
			// худший исход из возможных: снаружи это выглядит как «мир врёт».
			отказ(t, rec, http.StatusBadRequest, "name-reserved", "возьми другое имя")
		})
	}
}

func TestОтказАдресНеПоФорме(t *testing.T) {
	h, _, _ := дверь(t, func(string) error { return nil })

	tests := []struct {
		name, адрес, код, выход string
	}{
		{"пустой", "", "field-missing", "маршрут вести некуда"},
		{"со схемой", "http://baser:3500", "addr-invalid", "убери «http://»"},
		{"без порта", "baser", "addr-invalid", "имя:порт"},
		{"порт пустой", "baser:", "addr-invalid", "не число от 1 до 65535"},
		{"порт ноль", "baser:0", "addr-invalid", "не число от 1 до 65535"},
		{"порт за границей", "baser:70000", "addr-invalid", "не число от 1 до 65535"},
		{"порт не число", "baser:восемь", "addr-invalid", "не число от 1 до 65535"},
		{"без хоста", ":3500", "addr-invalid", "резолвит соседа по имени"},
		{"путь в адресе", "baser:3500/api", "addr-invalid", "не число от 1 до 65535"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := запрос(h, http.MethodPost, Prefix, регистрация("baser", tt.адрес, "консоль"))
			отказ(t, rec, http.StatusBadRequest, tt.код, tt.выход)
		})
	}
}

func TestОтказБезОписания(t *testing.T) {
	h, _, _ := дверь(t, func(string) error { return nil })

	for _, даёт := range []string{"", "   "} {
		rec := запрос(h, http.MethodPost, Prefix, регистрация("baser", "baser:3500", даёт))
		отказ(t, rec, http.StatusBadRequest, "field-missing", "безымянной")
	}
}

func TestОтказТелоНеТо(t *testing.T) {
	h, _, _ := дверь(t, func(string) error { return nil })

	tests := []struct {
		name, тело, код, выход string
	}{
		{"пустое", "", "body-empty", `{"name"`},
		{"массив", `[{"name":"baser"}]`, "body-not-object", `{"name"`},
		{"не json", `{не json}`, "json-invalid", "три поля"},
		{"опечатка в поле", `{"name":"baser","adress":"baser:3500","gives":"к"}`, "json-invalid", "три поля"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := запрос(h, http.MethodPost, Prefix, tt.тело)
			отказ(t, rec, http.StatusBadRequest, tt.код, tt.выход)
		})
	}
}

func TestОтказТелоБольшеПотолка(t *testing.T) {
	h, _, _ := дверь(t, func(string) error { return nil })

	тело := регистрация("baser", "baser:3500", strings.Repeat("я", MaxBody))
	rec := запрос(h, http.MethodPost, Prefix, тело)
	отказ(t, rec, http.StatusRequestEntityTooLarge, "body-too-large", "три поля")
}

func TestОтказСнятиеНесуществующей(t *testing.T) {
	h, _, _ := дверь(t, nil)

	rec := запрос(h, http.MethodDelete, Prefix+"/prizrak", "")
	отказ(t, rec, http.StatusNotFound, "location-unknown", "GET "+Prefix)

	// Имя не по правилу — это отдельный отказ, и он срабатывает РАНЬШЕ похода
	// в реестр: «не подходит под правило» и «такой локации нет» — разные
	// причины, и подсказывают они разное.
	rec = запрос(h, http.MethodDelete, Prefix+"/Призрак", "")
	отказ(t, rec, http.StatusBadRequest, "name-invalid", "строчные латинские")
}

func TestОтказНеизвестнаяРучкаРеестра(t *testing.T) {
	h, _, _ := дверь(t, nil)

	tests := []struct{ name, метод, путь string }{
		{"чтение одной локации", http.MethodGet, Prefix + "/baser"},
		{"снятие вложенным путём", http.MethodDelete, Prefix + "/baser/сейчас"},
		{"неизвестный метод", http.MethodPut, Prefix},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := запрос(h, tt.метод, tt.путь, "")
			отказ(t, rec, http.StatusNotFound, "unknown-endpoint", "core/README.md")
		})
	}
}

// ── реестр на диске ──────────────────────────────────────────────────────────

func TestРеестрОтвергаетИспорченныйФайл(t *testing.T) {
	tests := []struct {
		name, содержимое, вОшибке string
	}{
		{"не json", `{локации}`, "почини файл либо удали его"},
		{"имя не по правилу маршрута", `{"locations":[{"name":"Baser","addr":"baser:3500"}]}`, "строчные латинские"},
		{"адрес не по форме", `{"locations":[{"name":"baser","addr":"http://baser"}]}`, "убери «http://»"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			файл := filepath.Join(t.TempDir(), "locations.json")
			if err := os.WriteFile(файл, []byte(tt.содержимое), 0o644); err != nil {
				t.Fatalf("подготовка не удалась: %v", err)
			}
			// Файл правят руками, и через него в маршрут заезжает то, что
			// через ручку не прошло бы. Падаем на старте: молча подняться с
			// половиной поля значит потерять маршруты без единого слова.
			_, err := Open(файл)
			if err == nil {
				t.Fatalf("испорченный реестр принят молча")
			}
			if !strings.Contains(err.Error(), tt.вОшибке) {
				t.Errorf("ошибка не называет выход: получено %q, ожидалась подстрока %q", err, tt.вОшибке)
			}
			if !strings.Contains(err.Error(), файл) {
				t.Errorf("ошибка не называет файл: %q", err)
			}
		})
	}
}

func TestПустойРеестрНеОшибка(t *testing.T) {
	// Дверь стартует БЕЗ единой поднятой локации (`kb:FUND-5`) и подхватывает
	// их по мере появления — иначе поле нельзя развернуть с нуля.
	reg, err := Open(filepath.Join(t.TempDir(), "нет-такого", "locations.json"))
	if err != nil {
		t.Fatalf("пустое поле отвергнуто: %v", err)
	}
	if locs := reg.List(); len(locs) != 0 {
		t.Errorf("пустое поле: получено %+v", locs)
	}
}

func TestПараллельныеРегистрацииНеТеряются(t *testing.T) {
	h, _, файл := дверь(t, func(string) error { return nil })

	const сколько = 20
	var wg sync.WaitGroup
	for i := range сколько {
		wg.Add(1)
		go func() {
			defer wg.Done()
			имя := fmt.Sprintf("loc-%02d", i)
			rec := запрос(h, http.MethodPost, Prefix, регистрация(имя, fmt.Sprintf("%s:3500", имя), "консоль"))
			if rec.Code != http.StatusCreated {
				t.Errorf("регистрация %s: получено %d (%s)", имя, rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()

	if locs := h.reg.List(); len(locs) != сколько {
		t.Errorf("в поле %d локаций, ожидалось %d", len(locs), сколько)
	}
	// Диск — не память: реестр, разошедшийся с файлом, переживёт рестарт неправдой.
	reg, err := Open(файл)
	if err != nil {
		t.Fatalf("реестр после параллельной записи не читается: %v", err)
	}
	if locs := reg.List(); len(locs) != сколько {
		t.Errorf("на диске %d локаций, ожидалось %d", len(locs), сколько)
	}
}

func TestОдноИмяПараллельноДостаётсяОдному(t *testing.T) {
	h, _, _ := дверь(t, func(string) error { return nil })

	const сколько = 8
	var wg sync.WaitGroup
	коды := make([]int, сколько)
	for i := range сколько {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Разные адреса на одно имя: победить обязан ровно один, иначе два
			// соседа считают себя одним маршрутом.
			rec := запрос(h, http.MethodPost, Prefix, регистрация("baser", fmt.Sprintf("сосед-%d:3500", i), "консоль"))
			коды[i] = rec.Code
		}()
	}
	wg.Wait()

	победители := 0
	for _, код := range коды {
		switch код {
		case http.StatusCreated:
			победители++
		case http.StatusConflict:
		default:
			t.Errorf("неожиданный код регистрации: %d", код)
		}
	}
	if победители != 1 {
		t.Errorf("имя досталось %d регистрациям, ожидалась ровно одна", победители)
	}
}

// То же самое, но окно гонки ОТКРЫТО НАРОЧНО, а не поймано удачей: проба адреса
// стоит ровно между «имя свободно» и записью, и барьер держит в ней всех разом.
// Все входящие видят ПУСТОЙ реестр и упираются в запись одновременно — то есть
// приходят в Registry.Put с решением «это не возвращение», принятым на пустоте.
//
// Проба, срабатывающая случайно, пробой не является (`kb:WORLD-32`): без барьера
// решающая проверка под замком может остаться зелёной, даже если её выломать.
func TestОдноИмяДостаётсяОдномуСквозьОткрытоеОкно(t *testing.T) {
	const сколько = 8
	var барьер sync.WaitGroup
	барьер.Add(сколько)
	// Проба зовётся ровно один раз на регистрацию, поэтому счётчик сходится:
	// свободное имя — проба своего адреса, занятое — проба прежнего.
	h, _, _ := дверь(t, func(string) error {
		барьер.Done()
		барьер.Wait()
		return nil
	})

	var wg sync.WaitGroup
	коды := make([]int, сколько)
	for i := range сколько {
		wg.Add(1)
		go func() {
			defer wg.Done()
			коды[i] = запрос(h, http.MethodPost, Prefix, регистрация("baser", fmt.Sprintf("сосед-%d:3500", i), "консоль")).Code
		}()
	}
	wg.Wait()

	победители := 0
	for i, код := range коды {
		switch код {
		case http.StatusCreated:
			победители++
		case http.StatusConflict:
		default:
			// 200 здесь — это «возвращение», выданное живому соседу: место
			// подменили бы молча, и оба считали бы себя одним маршрутом.
			t.Errorf("регистрация сосед-%d: получен код %d, ожидались 201 либо 409", i, код)
		}
	}
	if победители != 1 {
		t.Errorf("имя досталось %d регистрациям, ожидалась ровно одна", победители)
	}
	if поле := h.reg.List(); len(поле) != 1 {
		t.Errorf("в поле %d записей, ожидалась одна: %+v", len(поле), поле)
	}
}

// ── предупреждения ───────────────────────────────────────────────────────────

func TestПетляПредупреждаетНоНеЗапрещает(t *testing.T) {
	h, ж, _ := дверь(t, nil)
	e := локация(t, "я локация") // httptest всегда на петле

	rec := запрос(h, http.MethodPost, Prefix, регистрация("baser", e.адрес(), "консоль"))
	// Мир не запрещает то, что случается само собой, — он называет это вслух
	// в момент, когда это делают (`kb:WORLD-31`). Запрет здесь просто обходили бы.
	if rec.Code != http.StatusCreated {
		t.Fatalf("петля отвергнута, а должна предупреждать: %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(ж.всё(), "петля") {
		t.Errorf("предупреждения о петле нет в журнале:\n%s", ж.всё())
	}
}

func TestТрассировкаНазываетИмяИКодОтвета(t *testing.T) {
	h, ж, _ := дверь(t, func(string) error { return nil })
	запрос(h, http.MethodPost, Prefix, регистрация("baser", "baser:3500", "консоль"))

	// Локация поднимается на другой машине; без этой строки непонятно, дошла
	// ли регистрация вообще и чем кончилась.
	строка := ж.всё()
	for _, кусок := range []string{"door: POST " + Prefix, "name=baser", "code=201", "dur="} {
		if !strings.Contains(строка, кусок) {
			t.Errorf("в трассировке нет %q:\n%s", кусок, строка)
		}
	}
}

func TestТрассировкаМаршрутаНазываетАдресЛокации(t *testing.T) {
	h, ж, _ := дверь(t, nil)
	e := локация(t, "я локация")
	запрос(h, http.MethodPost, Prefix, регистрация("baser", e.адрес(), "консоль"))
	запрос(h.Route(статикаНеОтвечает(t)), http.MethodGet, "/baser/x", "")

	for _, кусок := range []string{"door: → baser GET /baser/x", "addr=" + e.адрес(), "code=200"} {
		if !strings.Contains(ж.всё(), кусок) {
			t.Errorf("в трассировке маршрута нет %q:\n%s", кусок, ж.всё())
		}
	}
}

// ── сама проба ───────────────────────────────────────────────────────────────

func TestПробаАдресаКраснеетНаЗакрытомПорту(t *testing.T) {
	// Проба, которую ни разу не заставили упасть, пробой не является
	// (`kb:WORLD-32`) — здесь это буквально про саму пробу адреса.
	probe := DialProbe(200 * time.Millisecond)

	e := локация(t, "живая")
	if err := probe(e.адрес()); err != nil {
		t.Errorf("живой адрес отвергнут: %v", err)
	}
	if err := probe(закрытыйАдрес(t)); err == nil {
		t.Error("закрытый адрес принят — проба не краснеет, и регистрация пропустит мёртвую локацию")
	}
}
