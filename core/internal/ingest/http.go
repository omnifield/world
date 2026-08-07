package ingest

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Prefix — под каким путём висит приём. Ручки перечислены в core/README.md.
const Prefix = "/api/stand/"

// Handler — все ручки приёма плюс трассировка и CORS одним слоем.
// Монтируется целиком по префиксу, поэтому и неизвестный путь под /api/stand/,
// и preflight отвечают в том же формате, что и рабочие ручки.
type Handler struct {
	store *Store
	logf  func(string, ...any)
	mux   *http.ServeMux
	now   func() time.Time
}

// NewHandler собирает приём. logf — куда идёт строка трассировки; nil даёт
// log.Printf. now подменяется в тестах, чтобы длительность не плавала.
func NewHandler(store *Store, logf func(string, ...any), now func() time.Time) *Handler {
	if logf == nil {
		logf = log.Printf
	}
	if now == nil {
		now = time.Now
	}
	h := &Handler{store: store, logf: logf, mux: http.NewServeMux(), now: now}

	h.mux.HandleFunc("POST "+Prefix+"runs", h.wrap(h.postRun))
	h.mux.HandleFunc("GET "+Prefix+"runs", h.wrap(h.getRuns))
	h.mux.HandleFunc("POST "+Prefix+"events", h.wrap(h.postEvents))
	h.mux.HandleFunc("GET "+Prefix+"events", h.wrap(h.getEvents))
	h.mux.HandleFunc("POST "+Prefix+"state", h.wrap(h.postState))
	h.mux.HandleFunc("GET "+Prefix+"state", h.wrap(h.getState))
	h.mux.HandleFunc(Prefix, h.wrap(func(*trace, http.ResponseWriter, *http.Request) *apiError {
		return errf(http.StatusNotFound, "unknown-endpoint", "такой ручки нет, список — в core/README.md")
	}))

	return h
}

// trace — то, что попадёт в строку журнала запроса. Сессию проставляет сама
// ручка: до разбора тела мы её не знаем, а без неё строка бесполезна.
type trace struct {
	session string
	bytes   int
	code    int
}

// счётчик кода ответа: без него в трассировке нечего писать.
type recorder struct {
	http.ResponseWriter
	code int
}

func (r *recorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// ДЕВ-РЕЖИМ. Открытый CORS стоит здесь сознательно: стенд сегодня
	// десктопный, но переезжает в браузер, и без этих заголовков отказ
	// выглядит как «сервер не работает». Перед чем-либо настоящим —
	// пересмотреть: origin в белый список, иначе это классическая дыра.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Max-Age", "600")

	if r.Method == http.MethodOptions {
		started := h.now()
		w.WriteHeader(http.StatusNoContent)
		h.trace(r, &trace{code: http.StatusNoContent}, started)
		return
	}

	h.mux.ServeHTTP(w, r)
}

// wrap даёт каждой ручке общий хвост: трассировка запроса и единый формат
// отказа. Ручка возвращает ошибку — и не думает, как её печатать.
func (h *Handler) wrap(fn func(*trace, http.ResponseWriter, *http.Request) *apiError) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		started := h.now()
		t := &trace{}
		rec := &recorder{ResponseWriter: w, code: http.StatusOK}

		if e := fn(t, rec, r); e != nil {
			writeJSON(rec, e.status, e)
		}
		t.code = rec.code
		h.trace(r, t, started)
	}
}

// trace — строка на запрос: метод, путь, сессия, байты, код, длительность.
// Стенд стоит на другой машине; без этой строки непонятно, дошло ли вообще.
func (h *Handler) trace(r *http.Request, t *trace, started time.Time) {
	session := t.session
	if session == "" {
		session = "-"
	}
	h.logf("stand: %s %s session=%s bytes=%d code=%d dur=%s",
		r.Method, r.URL.Path, session, t.bytes, t.code, h.now().Sub(started).Round(time.Microsecond))
}

// ── ручки ────────────────────────────────────────────────────────────────────

func (h *Handler) postRun(t *trace, w http.ResponseWriter, r *http.Request) *apiError {
	body, e := readBody(t, r)
	if e != nil {
		return e
	}
	d, e := parseObject(body)
	if e != nil {
		return e
	}
	session, e := sessionOf(d)
	if e != nil {
		return e
	}
	t.session = session

	line, e := compact(body)
	if e != nil {
		return e
	}
	if err := h.store.SaveRun(session, line); err != nil {
		return storeError(err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": session, "file": runFile})
	return nil
}

func (h *Handler) getRuns(t *trace, w http.ResponseWriter, r *http.Request) *apiError {
	runs, err := h.store.ListRuns()
	if err != nil {
		return storeError(err)
	}
	writeRaw(w, http.StatusOK, `{"count":`+strconv.Itoa(len(runs))+`,"runs":`+joinRaw(runs)+`}`)
	return nil
}

// postEvents принимает объект либо массив объектов. Пачка целиком либо
// ложится, либо отвергается: проверяем все строки ДО первой записи, иначе
// журнал получит половину пачки и отказ одновременно.
func (h *Handler) postEvents(t *trace, w http.ResponseWriter, r *http.Request) *apiError {
	body, e := readBody(t, r)
	if e != nil {
		return e
	}
	raws, e := splitEvents(body)
	if e != nil {
		return e
	}

	var session string
	lines := make([][]byte, 0, len(raws))
	for i, raw := range raws {
		where := "событие " + strconv.Itoa(i+1)
		d, e := parseObject(raw)
		if e != nil {
			return errf(e.status, e.Code, "%s: %s", where, e.Detail)
		}
		name, e := sessionOf(d)
		if e != nil {
			return errf(e.status, e.Code, "%s: %s", where, e.Detail)
		}
		if session == "" {
			session = name
			t.session = name
		} else if name != session {
			return errf(http.StatusBadRequest, "session-mixed",
				"%s: в пачке две сессии — %q и %q; пачка идёт одним прогоном", where, session, name)
		}
		if e := checkEvent(d, where); e != nil {
			return e
		}
		line, e := compact(raw)
		if e != nil {
			return errf(e.status, e.Code, "%s: %s", where, e.Detail)
		}
		lines = append(lines, line)
	}

	if err := h.store.AppendEvents(session, lines); err != nil {
		return storeError(err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": session, "accepted": len(lines), "file": eventsFile})
	return nil
}

func (h *Handler) getEvents(t *trace, w http.ResponseWriter, r *http.Request) *apiError {
	session := r.URL.Query().Get("session")
	if e := validSession(session); e != nil {
		return e
	}
	t.session = session

	var since int64
	if raw := r.URL.Query().Get("since"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return errf(http.StatusBadRequest, "since-invalid", "параметр «since» должен быть числом, получено %q", raw)
		}
		since = n
	}

	events, err := h.store.ReadEvents(session, since)
	if err != nil {
		return storeError(err)
	}
	writeRaw(w, http.StatusOK,
		`{"session":`+strconv.Quote(session)+`,"count":`+strconv.Itoa(len(events))+`,"events":`+joinRaw(events)+`}`)
	return nil
}

func (h *Handler) postState(t *trace, w http.ResponseWriter, r *http.Request) *apiError {
	body, e := readBody(t, r)
	if e != nil {
		return e
	}
	d, e := parseObject(body)
	if e != nil {
		return e
	}
	session, e := sessionOf(d)
	if e != nil {
		return e
	}
	t.session = session
	if _, ok := d["world"]; !ok {
		return errf(http.StatusBadRequest, "field-missing", "снимок: нет обязательного поля «world»")
	}

	line, e := compact(body)
	if e != nil {
		return e
	}
	if err := h.store.SaveState(session, line); err != nil {
		return storeError(err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": session, "file": stateFile})
	return nil
}

func (h *Handler) getState(t *trace, w http.ResponseWriter, r *http.Request) *apiError {
	session := r.URL.Query().Get("session")
	if e := validSession(session); e != nil {
		return e
	}
	t.session = session

	raw, err := h.store.ReadState(session)
	if err != nil {
		return storeError(err)
	}
	writeRaw(w, http.StatusOK, string(raw))
	return nil
}

// ── общее ────────────────────────────────────────────────────────────────────

// readBody читает тело с потолком. Потолок проверяется чтением лишнего байта,
// а не заголовком Content-Length: заголовок присылает клиент, и верить ему
// в вопросе «сколько мы согласны положить на диск» нельзя.
func readBody(t *trace, r *http.Request) ([]byte, *apiError) {
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	t.bytes = len(body)
	if err != nil {
		return nil, errf(http.StatusBadRequest, "body-unreadable", "тело запроса не дочитано: %v", err)
	}
	if len(body) > MaxBody {
		return nil, errf(http.StatusRequestEntityTooLarge, "body-too-large",
			"тело больше %d байт — журнал шлют строками, а не файлами", MaxBody)
	}
	return body, nil
}

// splitEvents разводит два разрешённых вида тела: объект и массив объектов.
func splitEvents(body []byte) ([]json.RawMessage, *apiError) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, errf(http.StatusBadRequest, "body-empty", "тело запроса пустое")
	}
	if trimmed[0] != '[' {
		return []json.RawMessage{[]byte(trimmed)}, nil
	}
	var batch []json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &batch); err != nil {
		return nil, errf(http.StatusBadRequest, "json-invalid", "пачка не разбирается как JSON: %v", err)
	}
	if len(batch) == 0 {
		return nil, errf(http.StatusBadRequest, "events-empty", "пачка пустая — записывать нечего")
	}
	return batch, nil
}

// storeError переводит отказ диска в ответ. «Нет сессии» — 404 и не ошибка
// сервиса; всё остальное — 500, и причина названа, а не спрятана.
func storeError(err error) *apiError {
	if errors.Is(err, errNotFound) {
		return errf(http.StatusNotFound, "session-unknown", "%v", err)
	}
	return errf(http.StatusInternalServerError, "write-failed", "%v", err)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("stand: ответ не записан: %v", err)
	}
}

// writeRaw отдаёт уже готовый JSON. Нужен там, где тело собрано из сырых
// строк с диска: пересобирать их через структуры значит потерять поля.
func writeRaw(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if _, err := io.WriteString(w, body+"\n"); err != nil {
		log.Printf("stand: ответ не записан: %v", err)
	}
}

func joinRaw(items [][]byte) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, string(it))
	}
	return "[" + strings.Join(parts, ",") + "]"
}
