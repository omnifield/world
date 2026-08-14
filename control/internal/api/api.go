// Пакет api — ручки контроллера. То, чем с ним говорит пульт (`web`) и человек курлом.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ Контроллер отдаёт ДАННЫЕ и делает ДЕЙСТВИЯ. Лица он не рисует: лицо — зона `web`,    │
// │ и она говорит с контроллером, а не с дверью (`WORLD2` 3.7).                          │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
//	POST   /api/session            вход: адрес скоупа и креды (или create — завести здесь)
//	GET    /api/me                 кто я сейчас
//	GET    /api/resources          источники ресурса: имя, адрес, жив ли
//	POST   /api/resources          добавить ресурс — на нём встаёт дверь
//	DELETE /api/resources/{имя}    снять ресурс; в ответе — что осталось на той машине
//	GET    /api/fields             поля юзера
//	POST   /api/fields             завести поле
//
// Отказ у всех ручек один и тот же — тройка `code` · `why` · `ways[]` (`WORLD2` 2.3).
// Форма ответа держится одинаковой с зоной `web` по таблице из `WORLD2-101`.
//
// Сессия одна. Второй юзер в эту итерацию не входит (`WORLD2-75`), и заводить под него
// хранилище сессий заранее значило бы городить на пустоту. Живёт она в памяти процесса:
// контроллер перезапустили — юзер входит заново, а скоуп его при этом цел, потому что
// лежит не здесь. Это названо вслух в README, а не оставлено сюрпризом.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/omnifield/world/control/internal/refusal"
	"github.com/omnifield/world/control/internal/resource"
	"github.com/omnifield/world/control/internal/run"
	"github.com/omnifield/world/control/internal/scope"
)

// cookieName — имя печенья с токеном сессии. Токен возвращается и телом: пульт берёт
// печенье, а человек курлом — заголовок; ходить в контроллер из терминала должно быть
// можно, иначе проверять его придётся только глазами через пульт.
const cookieName = "control-session"

// Options — всё, что контроллеру дают снаружи. Ни одного значения ручки не выдумывают
// сами: подменяемость — то, чем проба проверяет поведение там, где нет ни докера, ни
// второго ресурса.
type Options struct {
	Runner     run.Runner
	RemoteSh   string
	Docker     string
	KeysDir    string
	DoorPort   int
	SSHTimeout int
	Logf       func(string, ...any)
	Now        func() time.Time
	// NewToken — откуда берётся токен сессии. Подменяется в тестах, чтобы ответ был
	// предсказуем; в жизни — crypto/rand.
	NewToken func() (string, error)
}

// Handler — ручки контроллера.
type Handler struct {
	opt Options
	res *resource.Manager
	mux *http.ServeMux

	mu   sync.Mutex
	sess *session
}

// session — активный вход. Хранит открытый скоуп, а не копию личности: личность читается
// из скоупа при каждом вопросе, потому что скоуп мог измениться с другой машины, а мы
// обещали связь, а не копию (`WORLD2` 1.6).
type session struct {
	token string
	sc    *scope.Scope
	since time.Time
}

func New(opt Options) *Handler {
	if opt.Logf == nil {
		opt.Logf = log.Printf
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.NewToken == nil {
		opt.NewToken = newToken
	}
	if opt.Docker == "" {
		opt.Docker = "docker"
	}

	h := &Handler{
		opt: opt,
		res: &resource.Manager{
			Runner:   opt.Runner,
			RemoteSh: opt.RemoteSh,
			Docker:   opt.Docker,
			KeysDir:  opt.KeysDir,
			Port:     opt.DoorPort,
		},
		mux: http.NewServeMux(),
	}

	h.mux.HandleFunc("POST /api/session", h.wrap("session", h.postSession))
	h.mux.HandleFunc("GET /api/me", h.wrap("me", h.getMe))
	h.mux.HandleFunc("GET /api/resources", h.wrap("resources", h.getResources))
	h.mux.HandleFunc("POST /api/resources", h.wrap("resource-add", h.postResource))
	h.mux.HandleFunc("DELETE /api/resources/{name}", h.wrap("resource-drop", h.deleteResource))
	h.mux.HandleFunc("GET /api/fields", h.wrap("fields", h.getFields))
	h.mux.HandleFunc("POST /api/fields", h.wrap("field-add", h.postField))

	// Тот же путь другим методом — это не «нет такой ручки», а «не тем глаголом», и
	// сказать об этом надо разными словами: иначе человек ищет опечатку в пути.
	for _, p := range []string{"/api/session", "/api/me", "/api/resources", "/api/resources/{name}", "/api/fields"} {
		h.mux.HandleFunc(p, h.wrap("wrong-method", wrongMethod))
	}
	h.mux.HandleFunc("/", h.wrap("unknown", unknownEndpoint))

	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// ── трасса ───────────────────────────────────────────────────────────────────

// wrap даёт каждой ручке общий хвост: трасса и единый формат отказа. Ручка возвращает
// отказ и не думает, как его печатать. Строка трассы пишется ВСЕГДА, в том числе на
// отказе: «не получилось» без следа в журнале — это разбор по памяти.
func (h *Handler) wrap(name string, fn func(http.ResponseWriter, *http.Request) *refusal.Refusal) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		started := h.opt.Now()
		rec := &recorder{ResponseWriter: w, code: http.StatusOK}

		var code string
		if ref := fn(rec, r); ref != nil {
			code = ref.Code
			writeJSON(rec, ref.Status, ref)
		}
		if code == "" {
			code = "-"
		}
		h.opt.Logf("control: %s %s name=%s http=%d refusal=%s dur=%s",
			r.Method, r.URL.Path, name, rec.code, code,
			h.opt.Now().Sub(started).Round(time.Microsecond))
	}
}

type recorder struct {
	http.ResponseWriter
	code int
}

func (rc *recorder) WriteHeader(code int) {
	rc.code = code
	rc.ResponseWriter.WriteHeader(code)
}

// ── вход ─────────────────────────────────────────────────────────────────────

type sessionBody struct {
	Addr  string `json:"addr"`
	Creds string `json:"creds"`
	// Create — «скоупа по этому адресу ещё нет, заведи». Отдельным полем, а не
	// догадкой по отсутствию файла: завести личность молча, потому что «ничего не
	// нашлось», значит однажды завести её на опечатке в пути.
	Create bool   `json:"create"`
	Name   string `json:"name"`
	Brand  string `json:"brand"`
}

func (h *Handler) postSession(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	var body sessionBody
	if ref := decode(r, &body); ref != nil {
		return ref
	}

	addr, ref := scope.Parse(body.Addr)
	if ref != nil {
		return ref
	}

	key := ""
	if body.Creds != "" {
		if addr.Here {
			// Молча проглотить креды нельзя: человек их назвал и вправе знать, что они
			// никуда не пошли.
			return refusal.New(http.StatusBadRequest, "creds-here",
				"скоуп лежит на ресурсе контроллера — до него он дотягивается своими правами, кредам тут некуда приложиться",
				"убери creds",
				"или назови адрес на другом ресурсе: user@10.8.0.5:/srv/scope")
		}
		var ref *refusal.Refusal
		if key, ref = h.writeScopeKey(body.Creds); ref != nil {
			return ref
		}
	}

	sc := scope.Open(addr, h.opt.Runner, key, h.opt.SSHTimeout, h.opt.Now)

	var id *scope.Identity
	created := false
	if body.Create {
		if id, ref = sc.Create(r.Context(), body.Name, body.Brand); ref != nil {
			return ref
		}
		created = true
	} else {
		if id, ref = sc.Enter(r.Context()); ref != nil {
			return ref
		}
	}

	token, err := h.opt.NewToken()
	if err != nil {
		return refusal.New(http.StatusInternalServerError, "no-token",
			"вход состоялся, а метку сессии выдать не вышло: "+err.Error(),
			"попробуй ещё раз",
			"если повторяется — это дефект контроллера, заведи задачу зоне control")
	}

	h.mu.Lock()
	h.sess = &session{token: token, sc: sc, since: h.opt.Now()}
	h.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{
		"name":    id.Name,
		"brand":   id.Brand,
		"scope":   scopeView(addr),
		"created": created,
		"token":   token,
	})
	return nil
}

func (h *Handler) getMe(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	sess, ref := h.current(r)
	if ref != nil {
		return ref
	}
	// Личность перечитывается из скоупа, а не отдаётся из памяти: скоуп мог измениться с
	// другой машины, а мы обещали связь, а не снимок.
	id, ref := sess.sc.Enter(r.Context())
	if ref != nil {
		return ref
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":  id.Name,
		"brand": id.Brand,
		"scope": scopeView(sess.sc.Addr),
		"since": sess.since.UTC().Format(time.RFC3339),
	})
	return nil
}

// ── источники ресурса ────────────────────────────────────────────────────────

func (h *Handler) getResources(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	if _, ref := h.current(r); ref != nil {
		return ref
	}
	list, ref := h.res.List(r.Context())
	if ref != nil {
		return ref
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": list})
	return nil
}

type resourceBody struct {
	Name  string `json:"name"`
	Addr  string `json:"addr"`
	Creds string `json:"creds"`
}

func (h *Handler) postResource(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	if _, ref := h.current(r); ref != nil {
		return ref
	}
	var body resourceBody
	if ref := decode(r, &body); ref != nil {
		return ref
	}

	added, ref := h.res.Add(r.Context(), body.Name, body.Addr, body.Creds)
	if ref != nil {
		return ref
	}
	// Список отдаётся тем же ответом: главное, что должен увидеть человек, — что
	// источников стало два (`WORLD2-80`), и ради этого не надо спрашивать второй раз.
	list, ref := h.res.List(r.Context())
	if ref != nil {
		return ref
	}
	writeJSON(w, http.StatusCreated, map[string]any{"resource": added, "resources": list})
	return nil
}

func (h *Handler) deleteResource(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	if _, ref := h.current(r); ref != nil {
		return ref
	}
	q := r.URL.Query()
	dropped, ref := h.res.Drop(r.Context(), r.PathValue("name"),
		flag(q.Get("with-state")), flag(q.Get("with-image")))
	if ref != nil {
		return ref
	}
	list, ref := h.res.List(r.Context())
	if ref != nil {
		return ref
	}
	writeJSON(w, http.StatusOK, map[string]any{"dropped": dropped, "resources": list})
	return nil
}

// ── поля ─────────────────────────────────────────────────────────────────────

func (h *Handler) getFields(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	sess, ref := h.current(r)
	if ref != nil {
		return ref
	}
	fields, ref := sess.sc.Fields(r.Context())
	if ref != nil {
		return ref
	}
	writeJSON(w, http.StatusOK, map[string]any{"fields": fields})
	return nil
}

func (h *Handler) postField(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	sess, ref := h.current(r)
	if ref != nil {
		return ref
	}
	var body struct {
		Name string `json:"name"`
	}
	if ref := decode(r, &body); ref != nil {
		return ref
	}
	field, fields, ref := sess.sc.AddField(r.Context(), body.Name)
	if ref != nil {
		return ref
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"field":  field,
		"fields": fields,
		// Говорим вслух, чего НЕ произошло: поле записано в список, но нигде не поднято.
		// Человек, не увидевший этой строки, ждал бы поднятого поля и не получил бы ни
		// его, ни отказа — а это молчание, которое мир себе не позволяет.
		"note": "поле записано в твой список; само поле пока не поднимается — это следующая итерация",
	})
	return nil
}

// ── общее ────────────────────────────────────────────────────────────────────

// current — активная сессия. Единственное место, где решается «вошёл или нет».
func (h *Handler) current(r *http.Request) (*session, *refusal.Refusal) {
	h.mu.Lock()
	sess := h.sess
	h.mu.Unlock()

	notSignedIn := refusal.New(http.StatusUnauthorized, "not-signed-in",
		"в скоуп ещё не входили — до входа ни ресурсов, ни полей не существует",
		"войди: POST /api/session с полями addr и creds",
		"скоупа нет вовсе — заведи здесь: POST /api/session с create=true, name и brand")

	if sess == nil {
		return nil, notSignedIn
	}
	if token(r) != sess.token {
		return nil, notSignedIn
	}
	return sess, nil
}

func token(r *http.Request) string {
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		return c.Value
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return ""
}

// writeScopeKey кладёт ключ к скоупу в связку контроллера. Файлом, а не переменной, потому
// что ключ берёт ssh, а он читает его с диска. Один ключ на активный вход: сессия одна.
func (h *Handler) writeScopeKey(creds string) (string, *refusal.Refusal) {
	if h.opt.KeysDir == "" {
		return "", refusal.New(http.StatusInternalServerError, "no-keyring",
			"контроллеру некуда положить ключ: связка не названа",
			"это дефект подъёма контроллера — см. control/README.md, CONTROL_KEYS")
	}
	if err := os.MkdirAll(h.opt.KeysDir, 0o700); err != nil {
		return "", refusal.New(http.StatusInternalServerError, "no-keyring",
			"связка контроллера не завелась: "+err.Error(),
			"проверь том, смонтированный под связку: control/README.md")
	}
	if !strings.HasSuffix(creds, "\n") {
		creds += "\n"
	}
	path := filepath.Join(h.opt.KeysDir, "scope-key")
	if err := os.WriteFile(path, []byte(creds), 0o600); err != nil {
		return "", refusal.New(http.StatusInternalServerError, "no-keyring",
			"ключ к скоупу не записался: "+err.Error(),
			"проверь права на связку контроллера")
	}
	return path, nil
}

func scopeView(a scope.Address) map[string]any {
	return map[string]any{"addr": a.Raw, "here": a.Here, "path": a.Path}
}

func decode(r *http.Request, v any) *refusal.Refusal {
	// Предел на тело — не про безопасность, а про честность отказа: без него запрос на
	// гигабайт съел бы память контроллера молча.
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return refusal.New(http.StatusRequestEntityTooLarge, "body-too-big",
				"тело запроса больше мегабайта — столько контроллеру не нужно ни для кред, ни для имён",
				"пришли только то, что названо в control/README.md")
		}
		return refusal.New(http.StatusBadRequest, "no-body",
			"тело запроса не прочиталось: "+err.Error(),
			"пришли объект JSON")
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return refusal.New(http.StatusBadRequest, "no-body",
			"тело запроса пустое, а без него неизвестно, что делать",
			"пришли объект JSON — состав полей в control/README.md")
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	// Лишнее поле — это опечатка либо чужой контракт. И то и другое лучше назвать, чем
	// принять и промолчать: молча проглоченное поле выглядит как сработавшее.
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return refusal.New(http.StatusBadRequest, "bad-body",
			"тело запроса разобрать не вышло: "+err.Error(),
			"пришли объект JSON — состав полей в control/README.md")
	}
	return nil
}

func flag(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "да":
		return true
	}
	return false
}

func wrongMethod(_ http.ResponseWriter, r *http.Request) *refusal.Refusal {
	return refusal.New(http.StatusMethodNotAllowed, "wrong-method",
		"путь такой есть, а глагол "+r.Method+" у него не заведён",
		"какие глаголы у каких путей — в control/README.md")
}

func unknownEndpoint(_ http.ResponseWriter, r *http.Request) *refusal.Refusal {
	return refusal.New(http.StatusNotFound, "unknown-endpoint",
		"такой ручки у контроллера нет: "+r.URL.Path,
		"список ручек — в control/README.md",
		"их семь: /api/session, /api/me, /api/resources, /api/fields")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		log.Printf("control: ответ не записан: %v", err)
	}
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

var _ http.Handler = (*Handler)(nil)
