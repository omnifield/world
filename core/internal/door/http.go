package door

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Prefix — под каким путём висит реестр. Ручки перечислены в core/README.md.
const Prefix = "/api/locations"

// Handler — ручки реестра плюс построенные маршруты к локациям.
// Маршрутизация живёт здесь же (см. Route в proxy.go), и это не смешение
// обязанностей, а инвариант: регистрация И ЕСТЬ подключение к двери
// (`kb:WORLD-53`), у них общее состояние, и разводить их по разным владельцам
// значит завести вторую истину о том, кто в поле.
type Handler struct {
	reg   *Registry
	logf  func(string, ...any)
	now   func() time.Time
	probe Probe
	mux   *http.ServeMux

	mu      sync.Mutex
	proxies map[string]*proxyEntry
}

// NewHandler собирает дверь. logf — куда идёт строка трассировки (nil даёт
// log.Printf); now подменяется в тестах, чтобы длительность не плавала; probe
// проверяет адрес на регистрации (nil даёт DialProbe).
func NewHandler(reg *Registry, logf func(string, ...any), now func() time.Time, probe Probe) *Handler {
	if logf == nil {
		logf = log.Printf
	}
	if now == nil {
		now = time.Now
	}
	if probe == nil {
		probe = DialProbe(2 * time.Second)
	}
	h := &Handler{
		reg:     reg,
		logf:    logf,
		now:     now,
		probe:   probe,
		mux:     http.NewServeMux(),
		proxies: map[string]*proxyEntry{},
	}

	h.mux.HandleFunc("POST "+Prefix, h.wrap(h.postLocation))
	h.mux.HandleFunc("GET "+Prefix, h.wrap(h.getLocations))
	h.mux.HandleFunc("DELETE "+Prefix+"/{name}", h.wrap(h.deleteLocation))
	h.mux.HandleFunc(Prefix, h.wrap(unknownEndpoint))
	h.mux.HandleFunc(Prefix+"/", h.wrap(unknownEndpoint))

	return h
}

func unknownEndpoint(_ *trace, _ http.ResponseWriter, _ *http.Request) *apiError {
	return errf(http.StatusNotFound, "unknown-endpoint",
		"такой ручки у реестра нет; есть POST/GET "+Prefix+" и DELETE "+Prefix+"/{name} — список в core/README.md")
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// trace — то, что попадёт в строку журнала запроса. Имя проставляет сама
// ручка: до разбора тела мы его не знаем, а без него строка бесполезна.
type trace struct {
	name  string
	bytes int
	code  int
}

// wrap даёт каждой ручке общий хвост: трассировка и единый формат отказа.
// Ручка возвращает ошибку — и не думает, как её печатать.
func (h *Handler) wrap(fn func(*trace, http.ResponseWriter, *http.Request) *apiError) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		started := h.now()
		t := &trace{}
		rec := &recorder{ResponseWriter: w, code: http.StatusOK}

		if e := fn(t, rec, r); e != nil {
			writeErr(rec, e)
		}
		t.code = rec.code

		name := t.name
		if name == "" {
			name = "-"
		}
		h.logf("door: %s %s name=%s bytes=%d code=%d dur=%s",
			r.Method, r.URL.Path, name, t.bytes, t.code, h.now().Sub(started).Round(time.Microsecond))
	}
}

// ── ручки ────────────────────────────────────────────────────────────────────

// registration — тело регистрации. Ровно три поля: больше двери не нужно, а
// форма объявления участка у мира ещё открыта (`kb:WORLD-50`) — опережать её
// здесь нельзя.
type registration struct {
	Name  string `json:"name"`
	Addr  string `json:"addr"`
	Gives string `json:"gives"`
}

func (h *Handler) postLocation(t *trace, w http.ResponseWriter, r *http.Request) *apiError {
	body, e := readBody(t, r)
	if e != nil {
		return e
	}
	var reg registration
	if e := decodeObject(body, &reg); e != nil {
		return e
	}
	t.name = reg.Name

	if e := validName(reg.Name); e != nil {
		return e
	}
	if e := validAddr(reg.Addr); e != nil {
		return e
	}
	if strings.TrimSpace(reg.Gives) == "" {
		return errf(http.StatusBadRequest, "field-missing",
			"поле «gives» обязательно: локация без описания попадёт в список за дверью безымянной, и взять её осмысленно будет нельзя")
	}

	// ТОЖДЕСТВО МЕСТА. Имя занято другим адресом — вопрос не «пускать ли», а КТО
	// пришёл: то же место вернулось после пересоздания или чужой встал на занятое
	// имя. Мерка одна и честная — отвечает ли прежний по своему адресу
	// (`kb:WORLD-53`: присутствие проверяется достижимостью). Граница мерки
	// названа в доке пакета и в core/README.md.
	//
	// Спрашиваем это РАНЬШЕ пробы нового адреса. Причина не в экономии: оба
	// отказа верны, но блокирует именно занятое имя, а мир обязан называть
	// ПРИЧИНУ (`kb:WORLD-31`). Иначе локация с занятым именем и не поднятым
	// адресом узнаёт про адрес, чинит его — и упирается в то же имя.
	// Решающая проверка остаётся в Registry.Put, под тем же замком, что и
	// запись: здесь мы только выбираем, что сказать, а не кому достанется имя.
	var прежнийАдрес string
	var прежнийМолчит error
	if prev, занято := h.reg.Lookup(reg.Name); занято && prev.Addr != reg.Addr {
		err := h.probe(prev.Addr)
		if err == nil {
			return errf(http.StatusConflict, "name-taken",
				"имя %q занято локацией по адресу %s, и она отвечает прямо сейчас — значит место стоит, "+
					"и это не его возвращение, а другое место под тем же именем. Возьми другое имя либо сними стоящую: DELETE %s/%s",
				reg.Name, prev.Addr, Prefix, reg.Name)
		}
		прежнийАдрес, прежнийМолчит = prev.Addr, err
	}

	// Адрес проверяем ДО записи: реестр — состояние поля, и запись о локации,
	// которой нет, делает список ровно тем, чем он был раньше, — чьей-то
	// памятью, разошедшейся с действительностью.
	if err := h.probe(reg.Addr); err != nil {
		return errf(http.StatusBadGateway, "addr-unreachable",
			"по адресу %s никто не отвечает (%v); подними локацию и повтори регистрацию — дверь ходит к соседу по имени в общей сети, а не через хост",
			reg.Addr, err)
	}
	if loopback(reg.Addr) {
		// Предупреждение, не отказ (`kb:WORLD-31`): в одном контейнере с
		// дверью и в тестах петля законна, а соседу по сети она не видна.
		h.logf("door: предупреждение: адрес %s локации %q — петля; соседу по общей сети такой адрес не виден (kb:FUND-5), снаружи маршрут работать не будет",
			reg.Addr, reg.Name)
	}

	loc, исход, err := h.reg.Put(Location{
		Name:  reg.Name,
		Addr:  reg.Addr,
		Gives: strings.TrimSpace(reg.Gives),
		Since: h.now().UTC().Format(time.RFC3339),
	}, прежнийАдрес != "")
	if err != nil {
		return storeError(err)
	}

	if исход == OutcomeReturned {
		// Место вернулось по НОВОМУ адресу — построенный маршрут ведёт к
		// ушедшему соседу, и его пул соединений держит того, кого уже нет.
		// proxyFor и сам сверяет адрес, но забыть здесь надо явно: место ушло
		// из поля целиком, и держать его соединения незачем.
		h.forget(loc.Name)
		// Возвращение — единственный случай, когда дверь МЕНЯЕТ смысл записи, а
		// не только её поля. Строка в журнале называет, чем это подтверждено:
		// без неё «то же место» остаётся словом.
		h.logf("door: возвращение: %q сменила адрес %s → %s (прежний не ответил: %v); в поле с %s",
			loc.Name, прежнийАдрес, loc.Addr, прежнийМолчит, loc.Since)
	}

	code := http.StatusOK
	if исход == OutcomeCreated {
		code = http.StatusCreated
	}
	// В ответе — МАРШРУТ, а не только «ок»: регистрация и есть подключение,
	// и локация должна узнать из ответа, где она теперь достижима. И ИСХОД: три
	// исхода в один флаг `created` не влезают, а вернувшееся место обязано
	// узнать, что оно вернулось, а не создалось. `created` при этом остаётся —
	// его читают уже написанные потребители, и ломать их нельзя.
	writeJSON(w, code, map[string]any{
		"location": loc,
		"route":    loc.Route(),
		"created":  исход == OutcomeCreated,
		"outcome":  исход,
	})
	return nil
}

// listed — место в списке за дверью: сама запись плюс маршрут. Запись
// ВСТРАИВАЕТСЯ, а не пересобирается по полю: список и реестр обязаны говорить
// об одном месте одно и то же, а вторая копия перечня полей разъезжается с
// первой молча — новое поле появилось бы в реестре и не появилось в списке.
type listed struct {
	Location
	Route string `json:"route"`
}

func (h *Handler) getLocations(_ *trace, w http.ResponseWriter, _ *http.Request) *apiError {
	locs := h.reg.List()
	out := make([]listed, 0, len(locs))
	for _, loc := range locs {
		out = append(out, listed{Location: loc, Route: loc.Route()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "locations": out})
	return nil
}

func (h *Handler) deleteLocation(t *trace, w http.ResponseWriter, r *http.Request) *apiError {
	name := r.PathValue("name")
	t.name = name
	if e := validName(name); e != nil {
		return e
	}

	if err := h.reg.Remove(name); err != nil {
		return storeError(err)
	}
	h.forget(name)

	writeJSON(w, http.StatusOK, map[string]any{"name": name, "removed": true})
	return nil
}

// ── общее ────────────────────────────────────────────────────────────────────

// readBody читает тело с потолком. Потолок проверяется чтением лишнего байта,
// а не заголовком Content-Length: заголовок присылает клиент, и верить ему в
// вопросе «сколько мы согласны прочитать» нельзя.
func readBody(t *trace, r *http.Request) ([]byte, *apiError) {
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	t.bytes = len(body)
	if err != nil {
		return nil, errf(http.StatusBadRequest, "body-unreadable", "тело запроса не дочитано: %v", err)
	}
	if len(body) > MaxBody {
		return nil, errf(http.StatusRequestEntityTooLarge, "body-too-large",
			"тело больше %d байт — регистрация это три поля (name · addr · gives), а не файл", MaxBody)
	}
	return body, nil
}

// decodeObject разбирает тело как JSON-объект. Массив, число и строка здесь —
// ошибка формы, и названа она именно так, а не «invalid json».
func decodeObject(body []byte, v any) *apiError {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return errf(http.StatusBadRequest, "body-empty",
			"тело запроса пустое; регистрация — это {\"name\":…,\"addr\":…,\"gives\":…}")
	}
	if trimmed[0] != '{' {
		return errf(http.StatusBadRequest, "body-not-object",
			"ожидался JSON-объект, тело начинается с %q; регистрация — это {\"name\":…,\"addr\":…,\"gives\":…}",
			string(trimmed[0]))
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	// Лишнее поле — не молчаливая опечатка: «adress» вместо «addr» иначе
	// уехал бы в отказ «поле addr обязательно», и автор искал бы не там.
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errf(http.StatusBadRequest, "json-invalid",
			"тело не разбирается: %v; у регистрации ровно три поля — name · addr · gives", err)
	}
	return nil
}

// storeError переводит отказ реестра в ответ. Отказ, уже названный по канону
// (занятое имя), едет как есть; «нет локации» — 404 и не ошибка сервиса;
// всё прочее — 500, и причина названа, а не спрятана.
func storeError(err error) *apiError {
	var e *apiError
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, errUnknown) {
		return errf(http.StatusNotFound, "location-unknown",
			"такой локации в поле нет — снимать нечего; кто сейчас в поле, видно в GET "+Prefix)
	}
	return errf(http.StatusInternalServerError, "write-failed",
		"реестр локаций не записан: %v; поле осталось прежним, регистрацию можно повторить", err)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("door: ответ не записан: %v", err)
	}
}

// writeErr печатает отказ. Отдельно от writeJSON, потому что у отказа есть код
// состояния внутри самой ошибки — и путать эти два числа нельзя.
func writeErr(w http.ResponseWriter, e *apiError) {
	writeJSON(w, e.status, e)
}

var _ http.Handler = (*Handler)(nil)
