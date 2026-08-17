// Пакет api — две ручки раздачи и всё, что стоит между ними и человеком.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ РУЧЕК РОВНО ДВЕ, И ОБЕ ПО ОДНОМУ АДРЕСУ (`WORLD2` 3.4):                              │
// │                                                                                      │
// │   GET  /   отдать файл состояния ЦЕЛИКОМ                                             │
// │   PUT  /   принять файл состояния ЦЕЛИКОМ                                            │
// │                                                                                      │
// │ Третьей нет и не заводится. Ни «здоровье», ни «кто я», ни «версия» — каждая такая     │
// │ ручка была бы опознавательным знаком: мир начал бы узнавать СВОЮ раздачу, и чужая     │
// │ («хоть на ардуино») перестала бы быть равноправной (`WORLD2` 0.3, 3.4).               │
// │ Стук в живость поэтому идёт в ту же `GET /` — см. `cmd/share/knock.go`.               │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// ФОРМА ВЗЯТА У РЫНКА, А НЕ ПРИДУМАНА (сверка 2026-08-17):
//
//   - `GET`/`PUT` над одним адресом — обычная семантика HTTP: PUT просит заменить
//     состояние ресурса тем, что приехало телом (RFC 9110, §9.3.4), и он идемпотентен.
//     Это ровно «принять файл целиком», записанное стандартом двадцатилетней давности;
//   - пароль — `Basic` (RFC 7617): одна строка заголовка, реализуется на чём угодно;
//   - WebDAV (RFC 4918) СМОТРЕЛИ И НЕ БЕРЁМ: он про каталоги, свойства и блокировки —
//     `PROPFIND`, `LOCK`, `MKCOL`. Нам нужен один файл и две ручки, а чужой вилке
//     пришлось бы повторять весь набор, чтобы считаться раздачей. Это ровно та цена,
//     которую канон запрещает перекладывать на изготовителя вилки.
//
// Своего протокола, своих заголовков и своих полей у раздачи нет НИ ОДНОГО.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/omnifield/world/share/internal/refusal"
	"github.com/omnifield/world/share/internal/state"
)

// Options — чем раздача поднята. Пароль сюда приходит ЗНАЧЕНИЕМ, а не путём к файлу
// состояния: им закрыта сама раздача, и внутри файла его нет и быть не может — это значило
// бы запереть ключ внутри замка (`WORLD2` 3.4).
type Options struct {
	Store    *state.Store
	Password string
	// Log — журнал. Значением, а не глобальным `log`: проба читает его строки и не должна
	// для этого ловить чужой вывод.
	Log func(format string, v ...any)
	// Now — часы. Значением ради проб: длительность в журнале иначе не проверить.
	Now func() time.Time
}

type handler struct {
	store    *state.Store
	password string
	logf     func(format string, v ...any)
	now      func() time.Time
}

func New(o Options) http.Handler {
	h := &handler{store: o.Store, password: o.Password, logf: o.Log, now: o.Now}
	if h.logf == nil {
		h.logf = log.Printf
	}
	if h.now == nil {
		h.now = time.Now
	}
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := h.now()

	// ПОРЯДОК ПРОВЕРОК НЕ СЛУЧАЕН: пароль первым. Иначе посторонний, перебирая пути,
	// узнавал бы по разным отказам, что за этим адресом вообще что-то лежит.
	if ref := h.check(r); ref != nil {
		h.refuse(w, r, started, 0, ref)
		return
	}
	// Адрес один — корень. Раздача не каталог: у неё нет ни разделов, ни вложенных имён
	// (`WORLD2` 3.4, «ручек по разделам нет»). Две раздачи — два скоупа, а не два пути.
	if r.URL.Path != "/" {
		h.refuse(w, r, started, 0, refusal.New(http.StatusNotFound, "no-such-address",
			"по этому адресу у раздачи ничего нет — она отдаёт состояние в корне и только там",
			"возьми состояние: GET / по этому же хосту и порту",
			"разделов и вложенных путей у раздачи нет: файл отдаётся целиком"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.read(w, r, started)
	case http.MethodPut:
		h.write(w, r, started)
	default:
		// `Allow` обязателен при 405 по стандарту (RFC 9110, §15.5.6) — и он же самый
		// короткий выход: клиент видит, что тут можно, не читая доки.
		w.Header().Set("Allow", "GET, PUT")
		h.refuse(w, r, started, 0, refusal.New(http.StatusMethodNotAllowed, "bad-handle",
			"у раздачи две ручки, и «"+r.Method+"» не из них",
			"взять состояние целиком: GET /",
			"положить состояние целиком: PUT /"))
	}
}

// check — пароль. Раздача проверяет его САМА (`WORLD2` 3.4, «базово: везде пароль»):
// контроллер тут посредник, а не сторож.
//
// Имя в `Basic` не смотрим намеренно: личность определяется АДРЕСОМ, а не именем в
// заголовке (`WORLD2` 3.4, «Копий нет — есть адреса»). Спрашивать здесь ещё и имя значило
// бы завести вторую личность поверх той, что уже названа адресом.
func (h *handler) check(r *http.Request) *refusal.Refusal {
	_, password, ok := r.BasicAuth()
	if !ok {
		return refusal.New(http.StatusUnauthorized, "no-creds",
			"состояние закрыто паролем, а пароль не назван",
			"назови его: curl -u :пароль <адрес раздачи>",
			"пароль называет тот, кто поднимал раздачу; внутри файла состояния его нет")
	}
	// Сравнение постоянного времени: обычное `==` отвечает тем быстрее, чем раньше
	// разошлись строки, и по этой разнице пароль подбирают побайтно.
	if subtle.ConstantTimeCompare([]byte(password), []byte(h.password)) != 1 {
		return refusal.New(http.StatusUnauthorized, "bad-creds",
			"пароль не подошёл — это состояние закрыто другим",
			"проверь пароль скоупа: его называют при подъёме раздачи",
			"адрес и пароль — разные вещи: по верному адресу может лежать чужая личность")
	}
	return nil
}

func (h *handler) read(w http.ResponseWriter, r *http.Request, started time.Time) {
	data, ref := h.store.Read()
	if ref != nil {
		h.refuse(w, r, started, 0, ref)
		return
	}
	// Тип — «байты», а не `application/json`, и это честность, а не мелочь: внутрь файла
	// раздача не смотрит (`WORLD2` 3.4) и объявлять его разбор не вправе. Клиент на тип
	// не смотрит вовсе — чужая раздача назовёт его как ей угодно.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	// Личность — живое состояние, а не файл на раздачу кэшам: закэшированный скоуп это
	// прошлая личность, показанная как нынешняя.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		// Ответ уже пошёл — отказать нечем, но промолчать нельзя: юзер решит, что взял
		// состояние целиком.
		h.logf("share: GET / оборвалась на отдаче %d байт: %v", len(data), err)
		return
	}
	h.trace(r, started, http.StatusOK, len(data))
}

func (h *handler) write(w http.ResponseWriter, r *http.Request, started time.Time) {
	n, ref := h.store.Write(r.Body)
	if ref != nil {
		h.refuse(w, r, started, 0, ref)
		return
	}
	// 204 без тела: PUT заменил состояние тем, что приехало (RFC 9110, §9.3.4), и
	// возвращать копию только что присланного незачем. Своего поля «ок» тоже не заводим —
	// это был бы опознавательный знак нашей раздачи.
	w.WriteHeader(http.StatusNoContent)
	h.trace(r, started, http.StatusNoContent, int(n))
}

func (h *handler) refuse(w http.ResponseWriter, r *http.Request, started time.Time, n int, ref *refusal.Refusal) {
	if ref.Status == http.StatusUnauthorized {
		// Стандартный заголовок вызова (RFC 9110, §11.6.1): без него `401` не отказ, а
		// молчание с текстом. `realm` — слово, а не наша метка: узнавать по нему «свою»
		// раздачу клиенту запрещено (`WORLD2` 0.3).
		w.Header().Set("WWW-Authenticate", `Basic realm="scope", charset="UTF-8"`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(ref.Status)
	if err := json.NewEncoder(w).Encode(ref); err != nil {
		h.logf("share: отказ %s не доехал до человека: %v", ref.Code, err)
	}
	h.traceRefusal(r, started, ref, n)
}

// ── журнал ───────────────────────────────────────────────────────────────────
//
// Строка на каждый запрос: ЧТО спросили, ЧЕМ ответили, СКОЛЬКО байт и СКОЛЬКО времени.
// Личность лежит на чужой машине, и когда «состояние не приезжает», разбирать это будут
// по журналу — другого следа нет. Пароль в журнал не попадает ни в каком виде: он приходит
// заголовком, а заголовки мы не печатаем.

func (h *handler) trace(r *http.Request, started time.Time, status, n int) {
	h.logf("share: %s %s → %d, %d байт, %s, от %s",
		r.Method, r.URL.Path, status, n, h.since(started), from(r))
}

func (h *handler) traceRefusal(r *http.Request, started time.Time, ref *refusal.Refusal, n int) {
	h.logf("share: %s %s → %d %s, %d байт, %s, от %s",
		r.Method, r.URL.Path, ref.Status, ref.Code, n, h.since(started), from(r))
}

func (h *handler) since(started time.Time) time.Duration {
	return h.now().Sub(started).Round(time.Millisecond)
}

// from — кто спрашивал. Без обратного разбора чужих заголовков (`X-Forwarded-For` и
// прочего): их ставит кто угодно, и доверять им значило бы писать в журнал сочинение.
func from(r *http.Request) string {
	if r.RemoteAddr == "" {
		return "(неизвестно)"
	}
	return r.RemoteAddr
}
