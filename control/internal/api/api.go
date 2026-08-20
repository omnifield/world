// Пакет api — ручки контроллера. То, чем с ним говорит пульт (`web`) и человек курлом.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ Контроллер отдаёт ДАННЫЕ и делает ДЕЙСТВИЯ. Лица он не рисует: лицо — зона `web`,    │
// │ и она говорит с контроллером, а не с дверью (`WORLD2` 3.7).                          │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
//	POST   /api/scope              завести скоуп: две пары — машина и скоуп — плюс личность
//	DELETE /api/scope              снять скоуп по адресу: то же тело, что у заведения
//	POST   /api/session            вход: АДРЕС и ПАРОЛЬ, и больше ничего
//	DELETE /api/session            выход: времянки контроллера снимаются
//	GET    /api/me                 кто я сейчас
//	GET    /api/progress           каким путём идёт начатое ПРЯМО СЕЙЧАС — до того, как оно кончилось
//	GET    /api/resources          территории юзера: имя, адрес, отвечает ли, что на ней стоит
//	POST   /api/resources          завести территорию — на ней встаёт вещь, названная рецептом
//	DELETE /api/resources/{имя}    снять территорию; в ответе — что осталось на той машине
//	GET    /api/recipes            чем контроллер умеет поднимать: каталог рецептов
//	GET    /api/fields             поля юзера
//	POST   /api/fields             завести поле
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ХОДА «ЗАВЕСТИ ЗДЕСЬ» НЕ СУЩЕСТВУЕТ (`WORLD2` 3.7, решение user 2026-08-16).          │
// │                                                                                      │
// │ Вход — это всегда адрес и пароль. Скоупа по адресу нет — юзер называет ТО ЖЕ САМОЕ,   │
// │ разница только в исходе: контроллер либо застаёт состояние, либо заводит его ТАМ.     │
// │ Заведи он личность у себя — стал бы держателем чужого состояния, а держать его вправе │
// │ только владелец (`1.9`); и в чужое состояние можно было бы попасть, ничего не         │
// │ предъявив. Защита состояния лежит в устройстве, а не в сетевой настройке.             │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// Отказ у всех ручек один и тот же — тройка `code` · `why` · `ways[]` (`WORLD2` 2.3).
//
// Сессия одна. Второй юзер в эту итерацию не входит (`WORLD2-75`), и заводить под него
// хранилище сессий заранее значило бы городить на пустоту. Живёт она в памяти процесса:
// контроллер перезапустили — юзер входит заново, а скоуп его при этом цел, потому что
// лежит не здесь. Это названо вслух в README, а не оставлено сюрпризом.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/omnifield/world/control/internal/creds"
	"github.com/omnifield/world/control/internal/progress"
	"github.com/omnifield/world/control/internal/pult"
	"github.com/omnifield/world/control/internal/recipe"
	"github.com/omnifield/world/control/internal/refusal"
	"github.com/omnifield/world/control/internal/resource"
	"github.com/omnifield/world/control/internal/run"
	"github.com/omnifield/world/control/internal/scope"
	"github.com/omnifield/world/control/internal/state"
)

// cookieName — имя печенья с токеном сессии. Токен возвращается и телом: пульт берёт
// печенье, а человек курлом — заголовок; ходить в контроллер из терминала должно быть
// можно, иначе проверять его придётся только глазами через пульт.
const cookieName = "control-session"

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ТРИ ЗНАЧЕНИЯ, КОТОРЫЕ КОНТРОЛЛЕР НАЗЫВАЕТ ПОДЪЁМУ РАЗДАЧИ. ЭТО ШОВ С ЗОНОЙ `share`.   │
// │                                                                                      │
// │ Имена принадлежат ЕЁ рецепту (`share/compose.yaml`), а не нам: ими рецепт с нами и    │
// │ разговаривает. Повтор стережётся пробой — переименуй их сосед молча, и раздача встала │
// │ бы с умолчаниями вместо того, что назвал юзер.                                        │
// │                                                                                      │
// │ Знать про раздачу контроллеру приходится ровно это и ровно здесь: заведение скоупа —  │
// │ подъём раздачи, а адрес и пароль называет тот, кто её поднимает.                       │
// └─────────────────────────────────────────────────────────────────────────────────────┘
const (
	// sharePasswordEnv — пароль скоупа. Им закрыта раздача, и внутри файла состояния его
	// нет: запирать ключ внутри замка нельзя (`WORLD2` 3.4).
	sharePasswordEnv = "SHARE_PASSWORD"
	// shareNameEnv — имя проекта, контейнера и тома раздачи. Разное у разных скоупов на
	// одной машине — иначе второй сядет поверх первого.
	shareNameEnv = "SHARE_NAME"
	// sharePortEnv — публикация раздачи. Тот порт, который юзер назвал в адресе скоупа.
	sharePortEnv = "SHARE_PORT"
)

// Options — всё, что контроллеру дают снаружи. Ни одного значения ручки не выдумывают
// сами: подменяемость — то, чем проба проверяет поведение там, где нет ни докера, ни
// второй машины.
type Options struct {
	Runner   run.Runner
	RemoteSh string
	// RecipesDir — каталог рецептов: ландшафт машины, куда хозяин кладёт свои вещи.
	// Пустой каталог — законное состояние: остаётся дверь.
	RecipesDir string
	// DoorRecipe — файл запуска двери, приехавший в образе рядом с подъёмом.
	DoorRecipe string
	// ShareRecipe — файл запуска РАЗДАЧИ СКОУПА. Им контроллер поднимает личность юзера на
	// названной машине. Рецепт, а не код: раздача — обычная вещь мира, и поднимается она
	// тем же путём, что все прочие (`WORLD2` 3.7, «залил-поднял»).
	ShareRecipe string
	Docker      string
	KeysDir     string
	DoorPort    int
	// СВОЕЙ ВЕРСИИ СБОРКИ ЗДЕСЬ НЕТ (`WORLD2-146`): ручкам она не нужна, потому что вещи
	// поднимаются тем, что назвал рецепт, а не тем, чем собран контроллер. Своя версия
	// называется там, где она про кого-то — в журнале подъёма самого контроллера.
	//
	// ScopeTimeout — сколько секунд ждём ответа раздачи скоупа.
	ScopeTimeout int
	// SSHTimeout — сколько секунд ждём машину, когда заходим на неё ПАРОЛЕМ, чтобы завести
	// ключ (`WORLD2-141`). Дальше по ssh ходит докер, и это уже его время.
	SSHTimeout int
	// PultDir — где лежит СОБРАННЫЙ пульт. Пусто — раздавать нечего, и контроллер
	// скажет об этом кодом `no-pult`, а не пустой страницей.
	PultDir string
	Logf    func(string, ...any)
	Now     func() time.Time
	// NewToken — откуда берётся токен сессии. Подменяется в тестах, чтобы ответ был
	// предсказуем; в жизни — crypto/rand.
	NewToken func() (string, error)
}

// Handler — ручки контроллера.
type Handler struct {
	opt     Options
	res     *resource.Manager
	recipes *recipe.Catalog
	pult    *pult.Handler
	mux     *http.ServeMux
	// live — ход текущего действия. Не состояние контроллера, а то, что ждущий вправе
	// прочитать ПОКА он ждёт (`internal/progress`).
	live *progress.Live

	mu   sync.Mutex
	sess *session
}

// session — активный вход. Хранит открытый скоуп, а не копию состояния: состояние
// читается по адресу при каждом вопросе, потому что оно могло измениться с другой машины,
// а мы обещали связь, а не копию (`WORLD2` 1.6).
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

	recipes := &recipe.Catalog{Dir: opt.RecipesDir, Door: opt.DoorRecipe}
	live := &progress.Live{Now: opt.Now}
	h := &Handler{
		opt: opt,
		res: &resource.Manager{
			Runner:   opt.Runner,
			RemoteSh: opt.RemoteSh,
			Recipes:  recipes,
			Docker:   opt.Docker,
			KeysDir:  opt.KeysDir,
			Port:     opt.DoorPort,
			Logf:     opt.Logf,
			// Метка пути соседа уходит отсюда прямо в ход действия — той же горутиной, что
			// её прочитала. Через ответ ручки её было бы не довезти: ответ приходит, когда
			// путь уже пройден (`WORLD2-150` B2).
			OnPath: func(_, path string) { live.Path(path) },
		},
		recipes: recipes,
		pult:    pult.New(opt.PultDir),
		live:    live,
		mux:     http.NewServeMux(),
	}

	h.mux.HandleFunc("POST /api/scope", h.wrap("scope-create", h.postScope))
	// СНЯТИЕ СИММЕТРИЧНО ЗАВЕДЕНИЮ, и это не украшение таблицы: что мир умеет завести, он
	// обязан уметь и убрать. Иначе поднятая раздача снимается только руками на той машине —
	// то есть мир заводит вещи, за которые потом не отвечает (решение user 2026-08-20).
	h.mux.HandleFunc("DELETE /api/scope", h.wrap("scope-drop", h.deleteScope))
	h.mux.HandleFunc("POST /api/session", h.wrap("session", h.postSession))
	h.mux.HandleFunc("DELETE /api/session", h.wrap("session-out", h.deleteSession))
	h.mux.HandleFunc("GET /api/me", h.wrap("me", h.getMe))
	h.mux.HandleFunc("GET /api/progress", h.wrap("progress", h.getProgress))
	h.mux.HandleFunc("GET /api/resources", h.wrap("resources", h.getResources))
	h.mux.HandleFunc("POST /api/resources", h.wrap("resource-add", h.postResource))
	h.mux.HandleFunc("DELETE /api/resources/{name}", h.wrap("resource-drop", h.deleteResource))
	h.mux.HandleFunc("GET /api/recipes", h.wrap("recipes", h.getRecipes))
	h.mux.HandleFunc("GET /api/fields", h.wrap("fields", h.getFields))
	h.mux.HandleFunc("POST /api/fields", h.wrap("field-add", h.postField))

	// Тот же путь другим методом — это не «нет такой ручки», а «не тем глаголом», и
	// сказать об этом надо разными словами: иначе человек ищет опечатку в пути.
	for _, p := range []string{"/api/scope", "/api/session", "/api/me", "/api/progress", "/api/resources", "/api/resources/{name}", "/api/recipes", "/api/fields"} {
		h.mux.HandleFunc(p, h.wrap("wrong-method", wrongMethod))
	}

	// ГРАНИЦА МЕЖДУ РУЧКАМИ И ПУЛЬТОМ — две строки ниже, и они не симметричны намеренно.
	//
	// Всё, что начинается на `/api/`, остаётся ручками ДО КОНЦА: неизвестный путь под этим
	// префиксом — это промах машины, и отвечать ему страницей нельзя (клиент ждёт JSON и
	// получил бы HTML). Всё остальное — пульт. Без явного `/api/` пульт перехватывал бы
	// опечатки в ручках, а человек видел бы «страницы нет» там, где ошибся в имени ручки.
	//
	// `/api` без косой черты записан отдельно: без него `ServeMux` отвечает на него
	// перенаправлением на `/api/`, и клиент получил бы 301 вместо внятного отказа.
	h.mux.HandleFunc("/api/", h.wrap("unknown", unknownEndpoint))
	h.mux.HandleFunc("/api", h.wrap("unknown", unknownEndpoint))
	h.mux.HandleFunc("/", h.wrap("pult", h.servePult))

	return h
}

// servePult отдаёт лицо для человека. Само лицо зона не рисует — оно приезжает собранным
// из зоны `web`; здесь только раздача и внятный отказ, когда раздавать нечего.
func (h *Handler) servePult(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	return h.pult.Serve(w, r)
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
			writeRefusal(rec, r, ref)
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

// ── завести скоуп ────────────────────────────────────────────────────────────

// scopeBody — ДВЕ ПАРЫ, названные раздельно, и это не украшение формы (`WORLD2` 3.4, «Два
// адреса, и путать их дорого»):
//
//	машина  адрес и креды РЕСУРСА — по ним контроллер туда дотянется и поднимет раздачу;
//	скоуп   адрес, по которому состояние будет раздаваться, и пароль — по ним потом входят.
//
// Креды машины юзер даёт РУКАМИ, а не берёт из скоупа: скоупа в этот момент ещё нет. И
// после заведения их там тоже нет — мир поднял раздачу и ушёл (`WORLD2-152`). Территорию
// заводит отдельное решение юзера (`POST /api/resources`), и вот тогда креды ложатся в
// скоуп, потому что мир на ту машину ХОДИТ.
//
// На слиянии этих двух пар в одну выросла мёртвая `WORLD2-77`.
type scopeBody struct {
	Scope    scopePair `json:"scope"`
	Identity struct {
		Name  string `json:"name"`
		Brand string `json:"brand"`
	} `json:"identity"`
	// Machine — где поднять раздачу. Не назвали — значит раздача по адресу уже стоит
	// (юзер поднял её сам: это его вилка, и мир в неё не смотрит — `0.3`).
	Machine *machinePair `json:"machine"`
}

// scopePair — ПАРА СКОУПА: где он будет раздаваться и чем закрыт. Пароль здесь не «поле
// формы», а доказательство: им и заводят, и входят, и снимают.
type scopePair struct {
	Addr     string `json:"addr"`
	Password string `json:"password"`
}

// machinePair — ПАРА МАШИНЫ: как её зовёт юзер, где она и чем до неё дотянуться. Названа
// один раз на обе ручки: заведение и снятие принимают ОДНО И ТО ЖЕ, и разъехаться этим
// двум описаниям было бы нечем, только если описание одно.
type machinePair struct {
	Name  string    `json:"name"`
	Addr  string    `json:"addr"`
	Creds credsBody `json:"creds"`
}

func (h *Handler) postScope(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	var body scopeBody
	if ref := decode(r, &body); ref != nil {
		return ref
	}

	addr, ref := scope.Parse(body.Scope.Addr)
	if ref != nil {
		return ref
	}
	if body.Scope.Password == "" {
		return noPassword()
	}
	if strings.TrimSpace(body.Identity.Name) == "" {
		return refusal.New(http.StatusBadRequest, "no-name",
			"личность заводится с именем, а имя не названо",
			"назови его: identity.name = «егор»")
	}
	// Пустой бренд — ЗАКОННОЕ состояние, а не поломка (`WORLD2-135`): свежесозданный
	// скоуп это имя и пустота. Проверки на него здесь нет намеренно.

	sc := scope.Open(addr, body.Scope.Password, h.opt.ScopeTimeout)
	presence, ref := sc.Look(r.Context())
	if ref != nil {
		return ref
	}
	if presence == scope.PresenceState {
		return refusal.New(http.StatusConflict, "scope-exists",
			fmt.Sprintf("по адресу %s состояние уже раздаётся — заводить поверх него значит стереть личность", addr),
			"войди в него: POST /api/session с этим адресом и паролем",
			"или назови другой адрес: две раздачи — два скоупа (`WORLD2` 3.4)")
	}
	if presence == scope.PresenceEmpty && body.Machine != nil {
		return refusal.New(http.StatusConflict, "share-already",
			fmt.Sprintf("по адресу %s раздача уже отвечает, а её просят поднять на машине %s", addr, body.Machine.Addr),
			"убери machine: состояние ляжет в ту раздачу, что уже стоит",
			"или назови адрес, по которому раздачи ещё нет")
	}
	if presence == scope.PresenceNone && body.Machine == nil {
		return refusal.New(http.StatusBadGateway, "no-share",
			fmt.Sprintf("по адресу %s раздачи нет — состояние класть некуда", addr),
			"назови машину, и контроллер поднимет раздачу там: machine = {name, addr, creds}",
			"либо подними раздачу сам и повтори: форма стыковки — `WORLD2` 3.4",
			"проверь адрес и порт: наша раздача по умолчанию слушает 8070")
	}

	// ┌─────────────────────────────────────────────────────────────────────────────────────┐
	// │ СВЕЖИЙ СКОУП — ЭТО ЛИЧНОСТЬ И ПУСТОТА (`WORLD2` 3.4, «Почему так», п. 5).            │
	// │                                                                                      │
	// │ Ни территории, ни ключа сюда не пишется, и это ПРИЧИНА, а не косметика. Записывая    │
	// │ машину раздачи территорией, заведение делало два дела вместо одного: у скоупа        │
	// │ появлялся «дом», снятие такого участка приходилось запрещать (`drop-scope-home`), а  │
	// │ ключ мира оставался на чужой машине навсегда — убрать его через мир было нельзя ПО   │
	// │ УСТРОЙСТВУ. Живой прогон 2026-08-20: одиннадцать строк на одной машине.               │
	// │                                                                                      │
	// │ «В документах юзера не пишут, где эти документы лежат» — решение user 2026-08-20     │
	// │ (`WORLD2-152`). Территорию заводит он сам, отдельным ходом, и та же машина там        │
	// │ законна: это его решение, а не побочный след заведения.                               │
	// └─────────────────────────────────────────────────────────────────────────────────────┘
	st := state.New(strings.TrimSpace(body.Identity.Name), body.Identity.Brand)
	// завелось — дошли ли до конца. Читается отложенной уборкой ниже: пока здесь `false`,
	// всё, что мы успели оставить на ЧУЖОЙ машине, подлежит снятию.
	завелось := false
	raised := ""
	// shareEnv — ТЕ ЖЕ значения, которыми раздача поднята. Держим их, потому что снимать её
	// придётся ими же: рецепт собирает из `SHARE_NAME` имя проекта, контейнера и тома, и
	// снятие без него сняло бы раздачу ПО УМОЛЧАНИЮ — то есть чужую, а нашу оставило бы.
	var shareEnv []string
	// уход — чем мир уходит с машины: убирает свою строку из её `~/.ssh/authorized_keys`.
	// Пусто — класть было нечего (юзер дал свой ключ), и трогать там нечего тем более.
	var уход func() *refusal.Refusal
	// цена — что контроллер изменил на ЧУЖОЙ машине и что там осталось. Пусто — не заходил
	// и ничего не менял.
	цена := ""
	if m := body.Machine; m != nil {
		if ref := resource.ValidName(m.Name); ref != nil {
			return ref
		}
		if _, _, ref := resource.CheckAddr(m.Addr); ref != nil {
			return ref
		}
		shareRecipe, ref := h.shareRecipe()
		if ref != nil {
			return ref
		}

		// Ход начинается ЗДЕСЬ, до первого касания чужой машины: заход паролем и подъём —
		// оба длинные, и ждущий вправе видеть «идёт» с самого начала, а не с середины.
		h.live.Start("scope-create", m.Name)
		defer h.live.Done()

		// Креды двух видов: свой ключ либо пароль машины. Паролем контроллер один раз
		// заходит и заводит ключ — сам пароль дальше не живёт (`WORLD2-141`).
		ключ, строка, ref := h.ключРаздачи(r.Context(), sc, m.Addr, m.Creds)
		if ref != nil {
			return ref
		}

		// ┌───────────────────────────────────────────────────────────────────────────────┐
		// │ СТРОКА, КОТОРУЮ МЫ ПОЛОЖИЛИ, УХОДИТ В ЛЮБОМ ИСХОДЕ.                            │
		// │                                                                                │
		// │ Заведение состоялось — мир на эту машину больше не ходит, и оставленный ключ    │
		// │ был бы доступом, о котором юзер не просил. Не состоялось — тем более: скоуп не  │
		// │ записан, приватной половины у юзера нет, опознать строку и убрать её ему нечем  │
		// │ (живой прогон 2026-08-20 — шесть строк за несколько неудачных попыток).          │
		// │                                                                                │
		// │ Снимаем ровно то, что положили, и только когда положили ПАРОЛЕМ: свой ключ      │
		// │ юзера мы на машину не клали и трогать его не смеем.                             │
		// └───────────────────────────────────────────────────────────────────────────────┘
		if строка != "" {
			user, host, port, ref := resource.SplitAddr(m.Addr)
			if ref != nil {
				return ref
			}
			машина, пароль := creds.Machine{User: user, Host: host, Port: port}, m.Creds.Value
			уход = func() *refusal.Refusal {
				return creds.Remove(r.Context(), машина, пароль, строка,
					filepath.Join(h.opt.KeysDir, "known_hosts"), h.opt.SSHTimeout)
			}
			defer func() {
				if завелось {
					return // ушли выше, на удачном пути, и сказали об этом юзеру
				}
				if ref := уход(); ref != nil {
					h.opt.Logf("control: заведение не состоялось, и строку с машины %s убрать не вышло: %s", m.Addr, ref.Why)
					return
				}
				h.opt.Logf("control: заведение не состоялось — снял свою строку из ~/.ssh/authorized_keys машины %s", m.Addr)
			}()
		}

		// Ключ кладётся ДО подъёма: докер пойдёт по ssh сам и возьмёт его из связки.
		if ref := h.res.PutKey(m.Name, m.Addr, ключ); ref != nil {
			return ref
		}
		shareEnv = shareVars(m.Name, addr, body.Scope.Password)
		// Имя поднятой вещи сосед называет, а помнить его тут некому и незачем: территории у
		// свежего скоупа нет. Имя вещи запоминает тот, кто ЗАВОДИТ участок.
		if _, ref := h.res.Raise(r.Context(), m.Name, m.Addr, shareRecipe, shareEnv); ref != nil {
			h.res.DropKey(m.Name)
			return ref
		}
		raised = m.Name

		// ┌───────────────────────────────────────────────────────────────────────────────┐
		// │ РАЗДАЧА ВСТАЛА НА СТАРЫЙ ТОМ, И В НЁМ УЖЕ ЛЕЖИТ ЛИЧНОСТЬ — НЕ ПИШЕМ ПОВЕРХ.    │
		// │                                                                                │
		// │ Спрашивать надо ЗДЕСЬ: до подъёма по адресу не отвечал никто, и «пусто» тогда   │
		// │ значило лишь «раздачи нет». Том её переживает нарочно (`DELETE /api/scope` без  │
		// │ `with-state`), и поднятая заново раздача отдаёт то, что в нём лежало. Пиши мы   │
		// │ поверх — обещание «том остался, личность цела» было бы ложью через один вызов.  │
		// │                                                                                │
		// │ Раздачу при этом НЕ снимаем: она уже отдаёт ту личность, и снять её значило бы  │
		// │ отобрать у юзера единственный вход в неё. Мир уходит с машины иначе — снимает    │
		// │ свою строку и времянки, что и делает отложенная уборка ниже.                     │
		// └───────────────────────────────────────────────────────────────────────────────┘
		if встало, ref := sc.Look(r.Context()); ref == nil && встало == scope.PresenceState {
			h.res.DropKey(m.Name)
			h.res.DropContext(r.Context(), m.Name)
			return refusal.New(http.StatusConflict, "scope-exists",
				fmt.Sprintf("раздача по адресу %s поднята, и в её томе уже лежит личность — заводить поверх значит стереть её", addr),
				"войди в неё: POST /api/session с этим адресом и паролем — раздача стоит и отвечает",
				"это не твоя личность — назови другое имя тома у рецепта раздачи либо другое имя участка",
				"стереть её насовсем и завести заново: DELETE /api/scope?with-state=1, потом POST /api/scope")
		}
	}

	if ref := sc.Write(r.Context(), st); ref != nil {
		// Отказ не вправе ничего оставлять за собой (`WORLD2` 2.3 п. 5): раздачу, в
		// которую состояние не легло, снимаем вместе с ключом. Не снять её значило бы
		// оставить на чужой машине вещь, о которой в скоупе не написано ничего.
		if raised != "" {
			h.lowerQuietly(r.Context(), raised, shareEnv)
		}
		return ref
	}

	// ┌─────────────────────────────────────────────────────────────────────────────────────┐
	// │ МИР ПОДНЯЛ РАЗДАЧУ И УШЁЛ С МАШИНЫ. Здесь, а не раньше: до записи состояния уборка   │
	// │ за неудачей ещё ходит на ту машину нашим же ключом (`lowerQuietly`), и сняв строку   │
	// │ до этого, мы отрезали бы себе руку, которой убираем.                                 │
	// └─────────────────────────────────────────────────────────────────────────────────────┘
	var осталось *refusal.Refusal
	if уход != nil {
		if осталось = уход(); осталось == nil {
			цена = "на машину " + body.Machine.Addr + " заходили паролем ОДИН раз: положили строку в её ~/.ssh/authorized_keys и убрали " +
				"её сразу после подъёма раздачи — мир туда больше не ходит, своего на ней не осталось; пароль нигде не сохранён"
			h.opt.Logf("control: раздача поднята — снял свою строку из ~/.ssh/authorized_keys машины %s: мир туда больше не ходит", body.Machine.Addr)
		} else {
			цена = осталось.Why
			h.opt.Logf("control: раздача поднята, а строку с машины %s убрать не вышло: %s", body.Machine.Addr, осталось.Why)
		}
	}
	// Времянки заведения (ключ в связке, блок в `config`, контекст докера) снимает `Bind`:
	// он раскладывает связку ЦЕЛИКОМ из скоупа, а в свежем скоупе территорий нет.
	if ref := h.res.Bind(r.Context(), st); ref != nil {
		return ref
	}
	token, ref := h.remember(w, sc)
	if ref != nil {
		return ref
	}
	ответ := map[string]any{
		"name":    st.Identity.Name,
		"brand":   st.Identity.Brand,
		"scope":   scopeView(addr),
		"created": true,
		"token":   token,
	}
	if цена != "" {
		ответ["note"] = цена
	}
	if осталось != nil {
		// Выходы уезжают человеку целиком: строка на ЕГО машине, и убрать её теперь может
		// только он — значит обязан знать, какая именно и чем.
		ответ["ways"] = осталось.Ways
	}
	завелось = true
	writeJSON(w, http.StatusCreated, ответ)
	return nil
}

// ключРаздачи — чем контроллер дотянется до машины, чтобы поднять на ней раздачу, и какую
// строку он на этой машине оставил (пусто — не оставлял).
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ В СКОУП ЭТОТ КЛЮЧ НЕ ПОПАДАЕТ — этим он и отличается от ключа ТЕРРИТОРИИ.            │
// │                                                                                      │
// │ Ключ территории живёт в скоупе, потому что мир на ту машину ХОДИТ: поднимает вещи,   │
// │ спрашивает, что стоит, снимает. К машине раздачи он идёт РОВНО ОДИН раз — поднять её │
// │ — и уходит. Ключ, положенный в скоуп «на всякий случай», был бы доступом, о котором  │
// │ юзер не просил, а свежий скоуп обязан быть пустым (`WORLD2` 3.4).                     │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func (h *Handler) ключРаздачи(ctx context.Context, sc *scope.Scope, addr string, body credsBody) (string, string, *refusal.Refusal) {
	kind, ref := creds.ParseKind(body.Kind)
	if ref != nil {
		return "", "", ref
	}
	if strings.TrimSpace(body.Value) == "" {
		return "", "", noCreds(kind)
	}
	if kind == creds.Key {
		// Свой ключ юзера: идём им как есть. На его машине не появляется ни одной строки, и
		// убирать потом нечего — трогать чужие строки мир не смеет.
		return body.Value, "", nil
	}

	user, host, port, ref := resource.SplitAddr(addr)
	if ref != nil {
		return "", "", ref
	}
	pair, ref := creds.Generate(creds.Sign(sc.Addr.String()))
	if ref != nil {
		return "", "", ref
	}
	// ЦЕНА НАЗЫВАЕТСЯ ДО ДЕЙСТВИЯ, а не после. Пароля в этой строке нет и быть не может.
	h.opt.Logf("control: машина %s: захожу паролем ОДИН раз и кладу ключ в её ~/.ssh/authorized_keys — уберу его, как только раздача встанет", addr)
	if ref := creds.Install(ctx, creds.Machine{User: user, Host: host, Port: port},
		body.Value, pair.Authorized, filepath.Join(h.opt.KeysDir, "known_hosts"), h.opt.SSHTimeout); ref != nil {
		return "", "", ref
	}
	return pair.Private, pair.Authorized, nil
}

// shareRecipe — рецепт раздачи скоупа. Своего перечня вещей у зоны нет и здесь тоже: это
// путь, названный при подъёме контроллера, а не знание о том, как раздача устроена.
func (h *Handler) shareRecipe() (string, *refusal.Refusal) {
	if h.opt.ShareRecipe == "" {
		return "", refusal.New(http.StatusInternalServerError, "no-share-recipe",
			"контроллеру не назвали рецепт раздачи скоупа — поднимать личность нечем",
			"это дефект подъёма контроллера: путь называется CONTROL_SHARE_RECIPE",
			"см. control/README.md, раздел «завести скоуп»")
	}
	if _, err := os.Stat(h.opt.ShareRecipe); err != nil {
		return "", refusal.New(http.StatusInternalServerError, "no-share-recipe",
			fmt.Sprintf("рецепта раздачи по пути %s нет: %v", h.opt.ShareRecipe, err),
			"это дефект образа контроллера — рецепт раздачи едет в нём рядом с подъёмом",
			"назови свой: CONTROL_SHARE_RECIPE=/путь/к/compose.yaml")
	}
	return h.opt.ShareRecipe, nil
}

// lowerQuietly — убрать за собой то, что мы только что подняли. Отказ уже собран и уедет
// человеку; вторым отказом поверх первого его перебивать нельзя — причина у неудачи одна.
//
// `env` — ТЕ ЖЕ значения, которыми поднимали, и это здесь главное. Без `SHARE_NAME` компоуз
// взял бы имя проекта по умолчанию и снял бы раздачу СОСЕДНЕГО скоупа, стоящую на той же
// машине, а нашу оставил бы жить (`WORLD2-150` B3).
func (h *Handler) lowerQuietly(ctx context.Context, name string, env []string) {
	if recipePath, ref := h.shareRecipe(); ref == nil {
		// Ручка здесь не названа намеренно: это уборка за неудачей, её выходы человеку не
		// уезжают вовсе — ему уже сказана настоящая причина.
		if _, ref := h.res.Lower(ctx, resource.Drop{Name: name, RecipePath: recipePath, Env: env}); ref != nil {
			h.opt.Logf("control: раздачу %s снять за собой не вышло: %s", name, ref.Why)
		}
	}
	h.res.DropKey(name)
}

// shareVars — чем контроллер называет раздаче, КТО она и ГДЕ ей слушать.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ НИ ОДНО ИЗ ТРЁХ ЗНАЧЕНИЙ КОНТРОЛЛЕР НЕ ВЫДУМЫВАЕТ — ВСЕ ТРИ ЮЗЕР УЖЕ НАЗВАЛ.          │
// │                                                                                      │
// │ Машина не единица личности, единица — АДРЕС (`WORLD2` 3.4, «Копий нет — есть          │
// │ адреса»): скоупов на одной машине может быть сколько угодно, и разводить их обязан    │
// │ тот, кто их поднимает.                                                                │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func shareVars(territory string, addr scope.Address, password string) []string {
	return []string{
		sharePasswordEnv + "=" + password,
		shareNameEnv + "=" + shareName(territory, addr),
		sharePortEnv + "=" + strconv.Itoa(addr.Port()),
	}
}

// shareName — имя раздачи: им рецепт называет проект, контейнер и том.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ИМЯ УЧАСТКА + ПОРТ СКОУПА, И ОБА КУСКА СКАЗАНЫ ЮЗЕРОМ.                                │
// │                                                                                      │
// │ Порт здесь не украшение, а ЕДИНСТВЕННОЕ, чем два скоупа на ОДНОЙ машине отличаются:   │
// │ машина у них общая, а адрес — нет. Имя участка стоит первым, потому что искать вещь   │
// │ человек будет тем словом, которое сам придумал (`docker ps` покажет `vps-8071`).      │
// │                                                                                      │
// │ ЧЕГО ЗДЕСЬ НЕТ НАМЕРЕННО:                                                             │
// │   счётчика — он теряется при снятии и переподъёме, и второй заход занял бы чужое имя; │
// │   случайного суффикса — человек не найдёт такую вещь руками;                          │
// │   слова «share» — так называет раздачу РЕЦЕПТ соседа, и повторить его имя здесь       │
// │     значило бы завести вторую копию чужого знания, которая разъедется молча.           │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// Имя участка проверено (`resource.ValidName`) до этого вызова, порт — число: собранное
// годится и в имя проекта компоуза, и в имя контейнера.
// вещи — имя, названное соседом, списком. Пусто — сосед промолчал: значит и помнить нам
// нечего, и список вещей этой территории показывается целиком, как раньше. Выдумывать имя
// за него нельзя: оно принадлежит рецепту (`WORLD2` 3.7).
func вещи(имя string) []string {
	if strings.TrimSpace(имя) == "" {
		return nil
	}
	return []string{имя}
}

func shareName(territory string, addr scope.Address) string {
	return territory + "-" + strconv.Itoa(addr.Port())
}

// credsBody — КРЕДЫ К МАШИНЕ, и вид их называется ЯВНО (`WORLD2-141`, решение user).
// Два вида, как в PuTTY: свой ключ либо пароль машины. Угадывать вид по виду строки нельзя
// — угаданный однажды примет ключ за пароль, и разбираться человек будет с отказом ssh, а
// не с нашей догадкой.
type credsBody struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// machineKey — ключ, которым контроллер будет ходить на машину, и цена, названная вслух.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ПАРОЛЬ — НЕ ТРАНСПОРТ, А СПОСОБ ПОЛУЧИТЬ КЛЮЧ (`WORLD2-141`). Докер ходит системным  │
// │ ssh, а тот пароля не берёт; подменять его обёрткой или писать свой транспорт —       │
// │ запрещено. Поэтому паролем контроллер заходит ОДИН раз и кладёт публичный ключ юзера │
// │ в `~/.ssh/authorized_keys` машины. Дальше всё по ключу.                              │
// │                                                                                      │
// │ ПАРОЛЬ ЖИВЁТ РОВНО ЭТОТ ВЫЗОВ: в скоуп уходит КЛЮЧ, а сам пароль не попадает ни в    │
// │ состояние, ни в связку, ни в журнал, ни в отказ.                                     │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// Ключ юзера ОДИН на скоуп: тот же ключ, положенный второй раз, строки не плодит.
//
// РАЗДАЧА ЗДЕСЬ ЗАВЕДОМО ОТВЕЧАЕТ, и поэтому ключ пишется в скоуп ДО того, как ляжет на
// чужую машину: территорию заводят из сессии, а сессии без живой раздачи не бывает. Прежде
// сюда ходило и заведение скоупа — с оговоркой «а если раздачи ещё нет, писать некуда»; с
// `WORLD2-152` оно ходит своим путём (`ключРаздачи`) и в скоуп не пишет вовсе, так что
// оговорки больше нет — вместе с полем, которым она держалась.
func (h *Handler) machineKey(ctx context.Context, sc *scope.Scope, st *state.State, name, addr string, body credsBody) (state.Key, string, *refusal.Refusal) {
	kind, ref := creds.ParseKind(body.Kind)
	if ref != nil {
		return state.Key{}, "", ref
	}
	if strings.TrimSpace(body.Value) == "" {
		return state.Key{}, "", noCreds(kind)
	}

	if kind == creds.Key {
		// Прежний путь: ключ юзера — его собственный, кладём как есть, ничего на его машине
		// не трогая. Ни одной строки на той стороне не появляется.
		return state.Key{Name: name, Kind: state.KindSSH, Value: body.Value}, "", nil
	}

	user, host, port, ref := resource.SplitAddr(addr)
	if ref != nil {
		return state.Key{}, "", ref
	}

	key, есть := st.Key(state.UserKeyName)
	if !есть || strings.TrimSpace(key.Value) == "" {
		pair, ref := creds.Generate(creds.Sign(sc.Addr.String()))
		if ref != nil {
			return state.Key{}, "", ref
		}
		key = state.Key{Name: state.UserKeyName, Kind: state.KindSSH, Value: pair.Private}
		st.SetKey(key)
		// ┌──────────────────────────────────────────────────────────────────────────────┐
		// │ КЛЮЧ ПИШЕТСЯ В СКОУП ДО ТОГО, КАК ПОПАДЁТ НА МАШИНУ. Неудача на полпути иначе │
		// │ оставила бы на чужой машине строку с ключом, которого у юзера нет, — и убрать │
		// │ её было бы нечем.                                                             │
		// └──────────────────────────────────────────────────────────────────────────────┘
		if ref := sc.Write(ctx, st); ref != nil {
			return state.Key{}, "", ref
		}
	}
	authorized, ref := creds.Authorized(key.Value, creds.Sign(sc.Addr.String()))
	if ref != nil {
		return state.Key{}, "", ref
	}

	// ЦЕНА НАЗЫВАЕТСЯ ДО ДЕЙСТВИЯ, а не после: контроллер сейчас изменит файл на ЧУЖОЙ
	// машине. Пароля в этой строке нет и быть не может.
	h.opt.Logf("control: машина %s: захожу паролем ОДИН раз и кладу публичный ключ юзера в её ~/.ssh/authorized_keys — дальше только по ключу", addr)
	if ref := creds.Install(ctx, creds.Machine{User: user, Host: host, Port: port},
		body.Value, authorized, filepath.Join(h.opt.KeysDir, "known_hosts"), h.opt.SSHTimeout); ref != nil {
		return state.Key{}, "", ref
	}

	return key, "на машину " + addr + " положен публичный ключ юзера (одна строка в её ~/.ssh/authorized_keys, подпись world-control) — " +
		"пароль дальше не нужен и нигде не сохранён; убрать доступ можно, удалив эту строку", nil
}

// ── снять скоуп ──────────────────────────────────────────────────────────────

// scopeDropBody — то же, что у заведения, МИНУС личность: снимают то, что лежит по адресу,
// а не того, кем себя назвали. Личность в этом теле не поле, а недоразумение — и поэтому
// она здесь не игнорируется молча, а краснеет лишним полем (`decode`).
type scopeDropBody struct {
	Scope scopePair `json:"scope"`
	// Machine — ГДЕ снимать, и назвать её обязательно: в скоупе кред нет и не было (мир их
	// туда не клал), а до машины надо дотянуться, чтобы снять с неё раздачу.
	Machine *machinePair `json:"machine"`
}

// deleteScope — СНЯТЬ СКОУП ПО АДРЕСУ.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ СИММЕТРИЯ ЗАВЕДЕНИЮ — ГЛАВНОЕ ТРЕБОВАНИЕ (решение user 2026-08-20, `WORLD2-152`):    │
// │ «есть ручка создать скоуп на машине по адресу — значит есть и снять по адресу».      │
// │ Мир, умеющий заводить и не умеющий убирать, оставляет за собой вещи, за которые       │
// │ отвечать некому: снять раздачу можно было только руками на той машине.                │
// │                                                                                      │
// │ Три дела, и о каждом сказано вслух:                                                   │
// │   1. раздача снимается ТЕМ ЖЕ рецептом и ТЕМИ ЖЕ значениями, какими поднята;          │
// │   2. наша строка уходит из `~/.ssh/authorized_keys` машины — мир уходит совсем;       │
// │   3. контекст докера и ключ снимаются из связки контроллера.                          │
// │                                                                                      │
// │ ТОМ РАЗДАЧИ ПО УМОЛЧАНИЮ ОСТАЁТСЯ: снятие вещи не означает потерю личности (`1.9`).   │
// │ Стереть — отдельно и явно, `?with-state=1`, и оба исхода названы словами.              │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func (h *Handler) deleteScope(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	var body scopeDropBody
	if ref := decode(r, &body); ref != nil {
		return ref
	}
	addr, ref := scope.Parse(body.Scope.Addr)
	if ref != nil {
		return ref
	}
	// ПАРОЛЬ ОБЯЗАТЕЛЕН: им доказывают, что скоуп твой. Проверяет его сама раздача — мы лишь
	// спрашиваем её тем же паролем, и `401` приезжает отказом `bad-password`.
	if body.Scope.Password == "" {
		return noPassword()
	}
	m := body.Machine
	if m == nil {
		return refusal.New(http.StatusBadRequest, "no-machine",
			"снять раздачу можно только НА машине, а машина не названа — из скоупа её кред взять неоткуда, мир их туда и не клал",
			`назови её так же, как при заведении: machine = {"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"password","value":"…"}}`,
			"имя участка назови ТО ЖЕ, что при заведении: из него и порта скоупа собрано имя раздачи, и снимается она по нему")
	}
	if ref := resource.ValidName(m.Name); ref != nil {
		return ref
	}
	if _, _, ref := resource.CheckAddr(m.Addr); ref != nil {
		return ref
	}
	shareRecipe, ref := h.shareRecipe()
	if ref != nil {
		return ref
	}

	sc := scope.Open(addr, body.Scope.Password, h.opt.ScopeTimeout)
	presence, ref := sc.Look(r.Context())
	if ref != nil {
		return ref
	}
	if presence == scope.PresenceNone {
		// ПОВТОРНЫЙ ВЫЗОВ ПРИХОДИТ СЮДА ЖЕ, и это не поломка, а ответ: раздачи по адресу нет.
		return refusal.New(http.StatusNotFound, "no-share",
			fmt.Sprintf("по адресу %s раздачи нет — снимать нечего", addr),
			"проверь адрес и порт: снимают по тому же адресу, по которому заводили",
			"если её уже сняли — это тот же ответ во второй раз, а не ошибка",
			"она стоит, но не отвечает — сними её на той машине: ./deploy/remote.sh drop <имя участка> --recipe <рецепт раздачи>")
	}

	// КЛЮЧ САМОГО СКОУПА — тот, что мир завёл ему паролем (`ключ юзера`). Скоуп снимается,
	// значит и доступ, выданный ради него, обязан уйти. Читаем состояние ДО снятия: после
	// него спросить будет некого.
	var свой string
	var st *state.State
	нечитаемо := ""
	if presence == scope.PresenceState {
		прочитано, ref := sc.Read(r.Context())
		if ref != nil {
			// Состояние не читается — снятию это не мешает (раздача есть, и она наша по
			// паролю), но своих прежних ключей мы на этой машине не найдём. Молчать нельзя.
			нечитаемо = "состояние по адресу прочитать не вышло (" + ref.Code + "), поэтому прежний ключ скоупа на машине не искали"
		} else {
			st = прочитано
			if k, есть := st.Key(state.UserKeyName); есть {
				свой = k.Value
			}
		}
	}

	h.live.Start("scope-drop", m.Name)
	defer h.live.Done()

	ключ, строка, ref := h.ключРаздачи(r.Context(), sc, m.Addr, m.Creds)
	if ref != nil {
		return ref
	}
	user, host, port, ref := resource.SplitAddr(m.Addr)
	if ref != nil {
		return ref
	}
	машина := creds.Machine{User: user, Host: host, Port: port}
	// Строка, положенная ЭТИМ ходом, — временная: она нужна ровно на время снятия и уходит в
	// любом исходе, удачном и нет.
	снято := false
	if строка != "" {
		defer func() {
			if снято {
				return // ушли ниже, на удачном пути, и сказали об этом юзеру
			}
			if ref := creds.Remove(r.Context(), машина, m.Creds.Value, строка,
				filepath.Join(h.opt.KeysDir, "known_hosts"), h.opt.SSHTimeout); ref != nil {
				h.opt.Logf("control: снятие не состоялось, и временную строку с машины %s убрать не вышло: %s", m.Addr, ref.Why)
			}
		}()
	}

	// Ключ и КОНТЕКСТ кладутся до вызова: снятие берёт адрес машины из контекста, а не из
	// наших аргументов (`deploy/remote.sh`, `cmd_drop`).
	if ref := h.res.PutKey(m.Name, m.Addr, ключ); ref != nil {
		return ref
	}
	if ref := h.res.PutContext(r.Context(), m.Name, m.Addr); ref != nil {
		h.res.DropKey(m.Name)
		return ref
	}
	dropped, ref := h.res.Lower(r.Context(), resource.Drop{
		Name:       m.Name,
		RecipePath: shareRecipe,
		// СНИМАЕМ ТЕМИ ЖЕ ЗНАЧЕНИЯМИ, КАКИМИ ПОДНИМАЛИ: имя проекта, контейнера и тома
		// рецепт собирает из `SHARE_NAME`, а его — из имени участка и порта скоупа. Позови
		// мы снятие без них, компоуз снял бы раздачу ПО УМОЛЧАНИЮ, то есть соседнюю.
		Env:       shareVars(m.Name, addr, body.Scope.Password),
		WithState: flag(r.URL.Query().Get("with-state")),
		WithImage: flag(r.URL.Query().Get("with-image")),
		Ручка:     "DELETE /api/scope",
	})
	if ref != nil {
		h.res.DropKey(m.Name)
		h.res.DropContext(r.Context(), m.Name)
		return ref
	}

	// ┌─────────────────────────────────────────────────────────────────────────────────────┐
	// │ СНЯЛИ ЛИ МЫ ТО, ЧТО ПРОСИЛИ, — ИЗМЕРЯЕТСЯ (`WORLD2` 4.2 п. 5). Живой прогон           │
	// │ 2026-08-20, находка ревью `WORLD2-152`.                                               │
	// │                                                                                      │
	// │ Имя раздачи собрано из ИМЕНИ УЧАСТКА и порта, а имя участка называет юзер — и назвать │
	// │ он может ДРУГОЕ. Тогда компоуз честно снимает проект, которого на машине нет: это     │
	// │ для него не ошибка, и подъём выходит нулём. Мир отвечал «снято», а раздача жила       │
	// │ дальше — успех, выведенный из кода возврата, вместо измеренного.                       │
	// │                                                                                      │
	// │ Мерим ТЕМ, ЧТО НАЗВАЛ ЮЗЕР, — адресом: единица личности это адрес, а не имя и не      │
	// │ машина (`3.4`, «Копий нет — есть адреса»). Отвечает — значит по нему стоит раздача, и │
	// │ снятое ею не было.                                                                     │
	// │                                                                                      │
	// │ Имя вещи в отказе — СЛОВО СОСЕДА (`REMOTE-THING`), а не наш пересказ: он рецепт и     │
	// │ читал. Сверять его с нашим ожиданием бесполезно — рецепт собирает имя проекта из      │
	// │ того же `SHARE_NAME`, который мы ему и назвали, и сошлось бы оно всегда.               │
	// └─────────────────────────────────────────────────────────────────────────────────────┘
	// Непроверенное называется непроверенным: не смогли измерить — говорим об этом, а не
	// выдаём неизмеренное за снятое.
	неизмерено := ""
	if живо, ref := sc.Look(r.Context()); ref != nil {
		неизмерено = "снялось ли по адресу, проверить не вышло (" + ref.Code + ") — посмотри сам: " + addr.String()
	} else if живо != scope.PresenceNone {
		h.res.DropKey(m.Name)
		h.res.DropContext(r.Context(), m.Name)
		чтоСнимали := "имя раздачи"
		if dropped.Thing != "" {
			чтоСнимали = "«" + dropped.Thing + "»"
		}
		return refusal.New(http.StatusConflict, "share-alive",
			fmt.Sprintf("снятие прошло, а по адресу %s раздача ПО-ПРЕЖНЕМУ отвечает: на машине снималось %s — то есть не то, что стоит по этому адресу",
				addr, чтоСнимали),
			"имя участка назови ТО ЖЕ, что при заведении: из него и порта скоупа собрано имя раздачи, и снимается она по нему",
			"имя забыто — сними её на той машине: ./deploy/remote.sh drop <имя участка> --recipe <рецепт раздачи>",
			"проверь и адрес: снимают по тому же адресу, по которому заводили")
	}
	снято = true

	// ── мир уходит с машины ──────────────────────────────────────────────────
	if строка != "" {
		if осталось := creds.Remove(r.Context(), машина, m.Creds.Value, строка,
			filepath.Join(h.opt.KeysDir, "known_hosts"), h.opt.SSHTimeout); осталось != nil {
			h.opt.Logf("control: раздача снята, а временную строку с машины %s убрать не вышло: %s", m.Addr, осталось.Why)
			dropped.Left = append(dropped.Left, осталось.Why)
			dropped.Ways = append(dropped.Ways, осталось.Ways...)
		} else {
			dropped.Removed = append(dropped.Removed, "наша временная строка в ~/.ssh/authorized_keys той машины")
		}
	}
	// ┌─────────────────────────────────────────────────────────────────────────────────────┐
	// │ КЛЮЧ САМОГО СКОУПА УХОДИТ БЕЗ ОГОВОРОК — и здесь это не то же, что при снятии       │
	// │ участка.                                                                             │
	// │                                                                                      │
	// │ Ключ у скоупа ОДИН на все машины, поэтому при снятии УЧАСТКА его строку приходится   │
	// │ беречь: скоуп жив, и на ту же машину может смотреть его второй участок. Здесь        │
	// │ снимается САМ СКОУП — беречь строку не для кого: его участки уходят вместе с ним.    │
	// │ Оставленная строка была бы ровно тем, из-за чего эта задача и заведена: доступ,      │
	// │ убрать который через мир больше нечем.                                                │
	// │                                                                                      │
	// │ Участок на этой же машине от этого не молчит: он назван в ответе — юзер узнаёт, что  │
	// │ вернуть скоуп на уцелевший том мало, участок надо будет завести заново.               │
	// └─────────────────────────────────────────────────────────────────────────────────────┘
	if свой != "" {
		if осталось := h.уйтиСМашины(r.Context(), sc, свой, m.Addr); осталось != nil {
			h.opt.Logf("control: раздача снята, а строку ключа скоупа с машины %s убрать не вышло: %s", m.Addr, осталось.Why)
			dropped.Left = append(dropped.Left, осталось.Why)
			dropped.Ways = append(dropped.Ways, осталось.Ways...)
		} else {
			dropped.Removed = append(dropped.Removed, "строка ключа скоупа в ~/.ssh/authorized_keys той машины")
		}
	}
	if ещёХодим(st, "", m.Addr) {
		if dropped.Note != "" {
			dropped.Note += "; " // сказанное подъёмом не затирается нашим: две разные правды
		}
		dropped.Note += "на этой машине стоял и участок снятого скоупа — его дорога ушла вместе с ключом скоупа; " +
			"вернёшь скоуп на уцелевший том — заведи участок заново: POST /api/resources"
	}

	// Времянки контроллера: ключ, блок в `config` и контекст. Контекст снимает и сам подъём,
	// но обещание «на этой машине следов не осталось» держим мы, а не его умолчание.
	h.res.DropKey(m.Name)
	h.res.DropContext(r.Context(), m.Name)

	// Сессия, которая вела В ЭТОТ скоуп, больше никуда не ведёт: раздачи по адресу нет.
	h.mu.Lock()
	if h.sess != nil && h.sess.sc.Addr.Raw == sc.Addr.Raw {
		h.sess = nil
	}
	h.mu.Unlock()

	// ОБА ИСХОДА ПРО ТОМ НАЗЫВАЮТСЯ ВСЛУХ, и словами про личность, а не про «состояние
	// вещи»: у раздачи скоупа в томе лежит ЛИЧНОСТЬ, и человек имеет право знать, стёрлась
	// она или ждёт на месте.
	//
	// КОМАНДА СТИРАНИЯ ЗДЕСЬ НЕ ПОВТОРЯЕТСЯ: её называют выходы (`dropped.ways`), и второй
	// раз она сказана быть не должна. Два текста об одном разъезжаются молча, а сторож на
	// присутствие проходит зелёным, пока жива хоть одна копия (своя грабля `WORLD2-150`).
	note := "том раздачи остался, и в нём лежит личность: снятие вещи не означает её потерю (`WORLD2` 1.9) — " +
		"подними раздачу на том же томе, и она снова отдаст её"
	if flag(r.URL.Query().Get("with-state")) {
		note = "том раздачи стёрт вместе с раздачей — личность по этому адресу стёрта, и вернуть её мир не может"
	}
	if неизмерено != "" {
		note += "; " + неизмерено
	}
	if нечитаемо != "" {
		note += "; " + нечитаемо
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dropped": dropped,
		"scope":   scopeView(addr),
		"note":    note,
	})
	return nil
}

// уйтиСМашины — убрать свою строку из `~/.ssh/authorized_keys` названной машины, зайдя ПО
// ТОМУ САМОМУ ключу: пароля у нас нет и быть не должно, а ключ лежит в скоупе.
//
// Возвращает не отказ ручки, а СЛОВА: действие, ради которого мы сюда пришли, уже
// состоялось, и подменять его причину уборкой нельзя. Молчать — тоже: строка осталась на
// машине юзера, и убрать её теперь может только он.
func (h *Handler) уйтиСМашины(ctx context.Context, sc *scope.Scope, ключ, addr string) *refusal.Refusal {
	if strings.TrimSpace(ключ) == "" {
		return nil
	}
	authorized, ref := creds.Authorized(ключ, creds.Sign(sc.Addr.String()))
	if ref != nil {
		return ref
	}
	user, host, port, ref := resource.SplitAddr(addr)
	if ref != nil {
		return ref
	}
	return creds.RemoveByKey(ctx, creds.Machine{User: user, Host: host, Port: port},
		ключ, authorized, filepath.Join(h.opt.KeysDir, "known_hosts"), h.opt.SSHTimeout)
}

// ещёХодим — на ЭТУ ЖЕ машину смотрит другой участок того же скоупа.
//
// Ключ у скоупа один на все машины (`state.UserKeyName`), поэтому строка в чужом
// `authorized_keys` принадлежит не участку, а СКОУПУ. Убрав её, пока на машину смотрит
// второй участок, мир оборвал бы живую вещь — и обнаружилось бы это при следующем подъёме,
// а не сейчас. Машины сличаются по хосту: имя участка — слово юзера, а не адрес.
func ещёХодим(st *state.State, кроме, addr string) bool {
	if st == nil {
		return false
	}
	host, _, ref := resource.CheckAddr(addr)
	if ref != nil {
		return false
	}
	for _, t := range st.Territories {
		if t.Name == кроме {
			continue
		}
		if h, _, ref := resource.CheckAddr(t.Addr); ref == nil && h == host {
			return true
		}
	}
	return false
}

// ── вход и выход ─────────────────────────────────────────────────────────────

// sessionBody — вход. АДРЕС И ПАРОЛЬ, и больше ничего: ни `create`, ни имени, ни бренда.
// Разница между «состояние есть» и «состояния нет» — только в исходе, а спрашивается одно
// и то же (`WORLD2` 3.7). Поля «завести здесь» тут нет и не появится.
type sessionBody struct {
	Addr     string `json:"addr"`
	Password string `json:"password"`
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
	if body.Password == "" {
		return noPassword()
	}

	sc := scope.Open(addr, body.Password, h.opt.ScopeTimeout)
	st, ref := sc.Read(r.Context())
	if ref != nil {
		return ref
	}
	// Времянки контроллера поднимаются ИЗ СКОУПА и только из него: сначала снимается всё
	// своё, потом раскладывается то, что лежит в состоянии. Вошёл под другой личностью —
	// видишь её территории, а не чужие.
	if ref := h.res.Bind(r.Context(), st); ref != nil {
		return ref
	}

	token, ref := h.remember(w, sc)
	if ref != nil {
		return ref
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    st.Identity.Name,
		"brand":   st.Identity.Brand,
		"scope":   scopeView(addr),
		"created": false,
		"token":   token,
	})
	return nil
}

// deleteSession — выход. Своего состояния он не трогает: скоуп лежит там, где лежал.
// Снимаются только ВРЕМЯНКИ контроллера — контексты, ключи и блоки в `config`. Без этого
// следующий вошедший увидел бы чужие территории, и «личность» ничего бы не значила.
func (h *Handler) deleteSession(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	if _, ref := h.current(r); ref != nil {
		return ref
	}
	if ref := h.res.Unbind(r.Context()); ref != nil {
		return ref
	}
	h.mu.Lock()
	h.sess = nil
	h.mu.Unlock()

	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, http.StatusOK, map[string]any{
		"out": true,
		"note": "вышли: контексты докера, ключи и блоки в config сняты. Скоуп не тронут — " +
			"он лежит там, где лежал, и открывается тем же адресом и паролем",
	})
	return nil
}

func (h *Handler) getMe(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	sess, ref := h.current(r)
	if ref != nil {
		return ref
	}
	// Личность перечитывается из скоупа, а не отдаётся из памяти: состояние могло
	// измениться с другой машины, а мы обещали связь, а не снимок.
	st, ref := sess.sc.Read(r.Context())
	if ref != nil {
		return ref
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":  st.Identity.Name,
		"brand": st.Identity.Brand,
		"scope": scopeView(sess.sc.Addr),
		"since": sess.since.UTC().Format(time.RFC3339),
	})
	return nil
}

// ── каким путём идёт начатое ─────────────────────────────────────────────────

// getProgress — ход действия, которое идёт ПРЯМО СЕЙЧАС.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ЧИТАЕТСЯ БЕЗ ВХОДА, И ЭТО НЕ ОГОВОРКА (`WORLD2-150` B2).                             │
// │                                                                                      │
// │ Единственный момент, ради которого ручка заведена, — тот, когда сессии ЕЩЁ НЕТ:       │
// │ `POST /api/scope` метку не выдал, а раздача на чужой машине уже поднимается, и        │
// │ копия образа по ssh идёт минуты. Потребуй мы здесь входа — ждущий не прочёл бы ход     │
// │ ровно тогда, когда ждёт, и ручка стала бы украшением.                                 │
// │                                                                                      │
// │ Цена названа вслух и она настоящая: пульт публикуется наружу умолчанием (`B4`),       │
// │ значит ход читает любой, кто знает его адрес. Поэтому наружу отсюда уезжает только    │
// │ род действия, метка пути и ИМЯ УЧАСТКА — слово, которое юзер придумал сам. Ни адреса   │
// │ машины, ни адреса скоупа, ни кред здесь нет и не появится: они названы в теле запроса │
// │ и оттуда никуда не едут.                                                              │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func (h *Handler) getProgress(w http.ResponseWriter, _ *http.Request) *refusal.Refusal {
	writeJSON(w, http.StatusOK, h.live.Snapshot())
	return nil
}

// remember — выдать метку сессии. Метка приезжает печеньем И телом: пульт берёт печенье,
// человек курлом — заголовок `Authorization: Bearer`.
func (h *Handler) remember(w http.ResponseWriter, sc *scope.Scope) (string, *refusal.Refusal) {
	token, err := h.opt.NewToken()
	if err != nil {
		return "", refusal.New(http.StatusInternalServerError, "no-token",
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
	return token, nil
}

// ── территории ───────────────────────────────────────────────────────────────

func (h *Handler) getResources(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	sess, ref := h.current(r)
	if ref != nil {
		return ref
	}
	st, ref := sess.sc.Read(r.Context())
	if ref != nil {
		return ref
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": h.res.List(r.Context(), st.Territories)})
	return nil
}

type resourceBody struct {
	// Name — ИМЯ УЧАСТКА, и его называет юзер (`WORLD2` 2.5 п. 11). Мир его не выдумывает
	// и из адреса машины не выводит: на имени стоит адрес локации.
	Name  string    `json:"name"`
	Addr  string    `json:"addr"`
	Creds credsBody `json:"creds"`
	// Recipe — ЧТО поднять на этой территории: имя рецепта из каталога либо путь (в
	// названии с косой чертой контроллер видит путь). Не назван — дверь.
	Recipe string `json:"recipe"`
}

func (h *Handler) postResource(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	sess, ref := h.current(r)
	if ref != nil {
		return ref
	}
	var body resourceBody
	if ref := decode(r, &body); ref != nil {
		return ref
	}
	if ref := resource.ValidName(body.Name); ref != nil {
		return ref
	}
	if _, _, ref := resource.CheckAddr(body.Addr); ref != nil {
		return ref
	}
	st, ref := sess.sc.Read(r.Context())
	if ref != nil {
		return ref
	}
	// ИМЯ ЗАНЯТО — ЭТО ОТКАЗ МЕХАНИКИ, а не ответ ресурса (`WORLD2` 2.3, три рода «нет»):
	// проверяем мы, по содержимому скоупа, ДО всякого докера. Молчаливая перезапись строки
	// в файле означала бы потерянный участок и столкнувшиеся адреса локаций.
	if _, busy := st.Territory(body.Name); busy {
		return refusal.New(http.StatusConflict, "name-taken",
			fmt.Sprintf("участок с именем «%s» в твоём скоупе уже есть, а на имени стоит адрес локации", body.Name),
			"назови другое имя",
			"посмотри, какие уже есть: GET /api/resources",
			"это тот же участок и ты хочешь его пересобрать — сними его сначала: DELETE /api/resources/"+body.Name)
	}
	// Рецепт находим ДО того, как тронули связку и машину: неизвестное имя обязано
	// отказать, не оставив за собой ни ключа, ни контекста (`WORLD2` 2.3).
	recipePath, ref := h.recipes.Find(body.Recipe)
	if ref != nil {
		return ref
	}

	// Ход начинается до первого касания чужой машины — по той же причине, что у заведения
	// скоупа: доставка образа на ту машину идёт минутами, и ждущий вправе знать, чем.
	h.live.Start("resource-add", body.Name)
	defer h.live.Done()

	// Креды двух видов, и вид назван явно. Паролем контроллер заходит ОДИН раз — и
	// говорит об этом вслух до того, как тронет чужую машину.
	// Раздача здесь заведомо отвечает: без неё не состоялся бы вход, а состояние этой
	// сессии только что из неё и прочитано. Значит ключ ложится в скоуп ДО машины — как и
	// задумано, и обратный порядок тут не нужен.
	key, цена, ref := h.machineKey(r.Context(), sess.sc, st, body.Name, body.Addr, body.Creds)
	if ref != nil {
		return ref
	}
	if ref := h.res.PutKey(body.Name, body.Addr, key.Value); ref != nil {
		return ref
	}
	вещь, ref := h.res.Raise(r.Context(), body.Name, body.Addr, recipePath, nil)
	if ref != nil {
		// Подъём не удался — свой след убираем за собой. Оставленный ключ означал бы, что
		// вторая попытка пойдёт кредами, которых юзер уже не называет.
		h.res.DropKey(body.Name)
		return ref
	}

	if ref := st.AddTerritory(state.Territory{Name: body.Name, Addr: body.Addr, Things: вещи(вещь)}, key); ref != nil {
		return ref
	}
	if ref := sess.sc.Write(r.Context(), st); ref != nil {
		// Вещь по ЧУЖОМУ рецепту снимается без наших значений: что ей нужно, знает она, а
		// не мы, и подсовывать ей `SHARE_*` значило бы толковать чужой рецепт.
		if _, low := h.res.Lower(r.Context(), resource.Drop{
			Name: body.Name, RecipePath: recipePath, RecipeName: body.Recipe,
		}); low != nil {
			h.opt.Logf("control: вещь %s снять за собой не вышло: %s", body.Name, low.Why)
		}
		h.res.DropKey(body.Name)
		return ref
	}

	added := resource.Resource{Name: body.Name, Addr: body.Addr}
	list := h.res.List(r.Context(), st.Territories)
	for _, cur := range list {
		if cur.Name == added.Name {
			added = cur
		}
	}
	// Список отдаётся тем же ответом: главное, что должен увидеть человек, — что
	// территорий стало две (`WORLD2-80`), и ради этого не надо спрашивать второй раз.
	ответ := map[string]any{"resource": added, "resources": list}
	if цена != "" {
		// Что изменено на ЧУЖОЙ машине — говорится и человеку, а не только в журнал.
		ответ["note"] = цена
	}
	writeJSON(w, http.StatusCreated, ответ)
	return nil
}

func (h *Handler) deleteResource(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	sess, ref := h.current(r)
	if ref != nil {
		return ref
	}
	name := r.PathValue("name")
	if ref := resource.ValidName(name); ref != nil {
		return ref
	}
	st, ref := sess.sc.Read(r.Context())
	if ref != nil {
		return ref
	}
	t, found := st.Territory(name)
	if !found {
		return refusal.New(http.StatusNotFound, "no-such-resource",
			fmt.Sprintf("участка «%s» в твоём скоупе нет — снимать нечего", name),
			"посмотри, какие есть: GET /api/resources")
	}
	// ┌─────────────────────────────────────────────────────────────────────────────────────┐
	// │ ЗАПРЕТА «НА ЭТОМ УЧАСТКЕ СТОИТ РАЗДАЧА ТВОЕГО СКОУПА» ЗДЕСЬ БОЛЬШЕ НЕТ.              │
	// │                                                                                      │
	// │ Он был следствием ошибки, а не правилом: пока заведение само записывало машину       │
	// │ раздачи территорией, у скоупа появлялся «дом», и снятие такого участка стирало бы    │
	// │ файл, в котором записано, что у юзера есть машины. Территория больше не заводится    │
	// │ сама (`WORLD2-152`); завёл её юзер руками — это ЕГО участок, и снимать его он вправе.│
	// │ Что стоит на машине — вопрос его решения, а не нашего разрешения (`WORLD2` 0.1).     │
	// │                                                                                      │
	// │ Раздачу скоупа по адресу снимает своя ручка — `DELETE /api/scope`.                    │
	// └─────────────────────────────────────────────────────────────────────────────────────┘
	q := r.URL.Query()
	// Рецепт называется и при снятии: снимаем ТО ЖЕ, что ставили, а своего реестра вещей
	// зона не заводит — второй список того же самого разъехался бы со скоупом.
	recipeName := q.Get("recipe")
	recipePath, ref := h.recipes.Find(recipeName)
	if ref != nil {
		return ref
	}

	dropped, ref := h.res.Lower(r.Context(), resource.Drop{
		Name:       name,
		RecipePath: recipePath,
		RecipeName: recipeName,
		WithState:  flag(q.Get("with-state")),
		WithImage:  flag(q.Get("with-image")),
		Ручка:      "DELETE /api/resources/" + name,
	})
	if ref != nil {
		return ref
	}
	// СНЯЛИ УЧАСТОК — УХОДИМ С МАШИНЫ СОВСЕМ. Ключ юзера, положенный туда при заведении,
	// снимаем ЗАХОДОМ ПО НЕМУ ЖЕ: пароля у нас нет и не должно быть, а ключ есть — он в
	// скоупе. Иначе строки копятся, и обещание «убрать доступ можно, удалив эту строку»
	// становится неисполнимым: подписаны они одинаково, и какая чья, юзер не знает
	// (живой прогон 2026-08-20 — восемь строк на одной машине).
	//
	// Делается ДО того, как участок исчезнет из состояния: после него мы уже не будем
	// знать ни адреса, ни ключа.
	ключ, есть := st.Key(t.Key)
	switch {
	case !есть:
	case ещёХодим(st, name, t.Addr):
		// НА ЭТОЙ МАШИНЕ СТОИТ ЕЩЁ ОДИН ТВОЙ УЧАСТОК, а ключ у скоупа один на все машины:
		// убрав строку сейчас, мир оборвал бы себе дорогу к живой вещи. Говорим вслух, что
		// строка осталась и почему, — иначе «ушли совсем» было бы неправдой молча.
		h.opt.Logf("control: участок %s снят, строку на машине %s оставил — на неё смотрит ещё один участок скоупа", name, t.Addr)
		dropped.Left = append(dropped.Left,
			"наша строка в ~/.ssh/authorized_keys той машины: на ней стоит ещё один твой участок, а ключ у скоупа один на все машины")
		dropped.Ways = append(dropped.Ways, "сними и его — уйдёт и она: GET /api/resources покажет, какие остались")
	default:
		if осталось := h.уйтиСМашины(r.Context(), sess.sc, ключ.Value, t.Addr); осталось != nil {
			// НЕ ВЫШЛО — ЭТО НЕ МОЛЧАНИЕ. Вещь снята, и отказывать поверх снятого нельзя; но
			// строка осталась на МАШИНЕ ЮЗЕРА, и убрать её теперь может только он.
			h.opt.Logf("control: участок %s снят, а строку с машины %s убрать не вышло: %s", name, t.Addr, осталось.Why)
			dropped.Left = append(dropped.Left, осталось.Why)
			dropped.Ways = append(dropped.Ways, осталось.Ways...)
		} else {
			h.opt.Logf("control: участок %s снят — убрал свою строку из ~/.ssh/authorized_keys машины %s", name, t.Addr)
			dropped.Removed = append(dropped.Removed, "наша строка в ~/.ssh/authorized_keys той машины")
		}
	}

	if ref := st.DropTerritory(name); ref != nil {
		return ref
	}
	if ref := sess.sc.Write(r.Context(), st); ref != nil {
		return ref
	}
	h.res.DropKey(name)

	writeJSON(w, http.StatusOK, map[string]any{
		"dropped":   dropped,
		"resources": h.res.List(r.Context(), st.Territories),
	})
	return nil
}

// ── рецепты ──────────────────────────────────────────────────────────────────

// getRecipes — что контроллер умеет поднимать прямо сейчас. Список ЧИТАЕТСЯ из каталога,
// а не перечисляется в коде: список вещей мира механикой не является (`WORLD2` 3.7).
func (h *Handler) getRecipes(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	if _, ref := h.current(r); ref != nil {
		return ref
	}
	list, ref := h.recipes.List()
	if ref != nil {
		return ref
	}
	if list == nil {
		list = []recipe.Recipe{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"recipes": list})
	return nil
}

// ── поля ─────────────────────────────────────────────────────────────────────

func (h *Handler) getFields(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	sess, ref := h.current(r)
	if ref != nil {
		return ref
	}
	st, ref := sess.sc.Read(r.Context())
	if ref != nil {
		return ref
	}
	writeJSON(w, http.StatusOK, map[string]any{"fields": fieldsView(st.Fields)})
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
	st, ref := sess.sc.Read(r.Context())
	if ref != nil {
		return ref
	}
	if ref := st.AddField(body.Name); ref != nil {
		return ref
	}
	if ref := sess.sc.Write(r.Context(), st); ref != nil {
		return ref
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"field":  map[string]any{"name": strings.TrimSpace(body.Name)},
		"fields": fieldsView(st.Fields),
		// Говорим вслух, чего НЕ произошло: поле записано в скоуп, но нигде не поднято.
		// Человек, не увидевший этой строки, ждал бы поднятого поля и не получил бы ни
		// его, ни отказа — а это молчание, которое мир себе не позволяет.
		"note": "поле записано в твой скоуп; само поле пока не поднимается — это следующая итерация",
	})
	return nil
}

// fieldsView — поля наружу. Внутри скоупа они лежат формой мира (`имя` · `адрес` ·
// `состояние`), а ручки говорят с пультом по-своему: формат состояния принадлежит канону,
// и подгонять его под ручки нельзя, как и наоборот.
func fieldsView(fields []state.Field) []map[string]any {
	out := make([]map[string]any, 0, len(fields))
	for _, f := range fields {
		out = append(out, map[string]any{"name": f.Name, "addr": f.Addr, "state": f.State})
	}
	return out
}

// ── общее ────────────────────────────────────────────────────────────────────

// current — активная сессия. Единственное место, где решается «вошёл или нет».
func (h *Handler) current(r *http.Request) (*session, *refusal.Refusal) {
	h.mu.Lock()
	sess := h.sess
	h.mu.Unlock()

	notSignedIn := refusal.New(http.StatusUnauthorized, "not-signed-in",
		"в скоуп ещё не входили — до входа ни территорий, ни полей не существует",
		"войди: POST /api/session с адресом скоупа и паролем",
		"скоупа нет вовсе — заведи его ТАМ, где он будет лежать: POST /api/scope")

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

func noPassword() *refusal.Refusal {
	return refusal.New(http.StatusBadRequest, "no-password",
		"пароль скоупа не назван, а без него раздача не отдаст состояние: доступ к личности доказывается кредами (`WORLD2` 3.4)",
		"пришли его полем password",
		"пароль называют при подъёме раздачи, и внутри файла состояния его нет")
}

func noCreds(kind creds.Kind) *refusal.Refusal {
	if kind == creds.Password {
		return refusal.New(http.StatusBadRequest, "no-creds",
			"вид кред назван паролем, а самого пароля нет — заходить нечем",
			`пришли его значением: creds = {"kind":"password","value":"<пароль машины>"}`,
			"паролем контроллер зайдёт ОДИН раз и положит на машину публичный ключ юзера — дальше только по ключу")
	}
	return refusal.New(http.StatusBadRequest, "no-creds",
		"креды машины не названы, а без них до неё не дотянуться — мир кред не заводит и не выдаёт",
		`пришли их с явным видом: creds = {"kind":"key","value":"<приватный ключ>"}`,
		`если ключа нет, а есть пароль: creds = {"kind":"password","value":"<пароль>"} — контроллер заведёт ключ сам`,
		"креды машины юзер даёт руками — из скоупа их взять неоткуда, пока скоупа нет (`WORLD2` 3.4)")
}

func scopeView(a scope.Address) map[string]any {
	return map[string]any{"addr": a.Raw, "host": a.Host}
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
	// принять и промолчать: молча проглоченное поле выглядит как сработавшее. Ход
	// «завести здесь» (`create`) отсюда и краснеет: он не игнорируется, а отказывает.
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return refusal.New(http.StatusBadRequest, "bad-body",
			"тело запроса разобрать не вышло: "+err.Error(),
			"пришли объект JSON — состав полей в control/README.md",
			"хода «завести здесь» у контроллера нет: вход это адрес и пароль, а завести скоуп — POST /api/scope (`WORLD2` 3.7)")
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
		"их семь путей: /api/scope, /api/session, /api/me, /api/progress, /api/resources, /api/recipes, /api/fields")
}

// writeRefusal печатает отказ ОДИН И ТОТ ЖЕ, но подаёт его двумя способами: машине —
// JSON, человеку в браузере — читаемый текст. Источник у обоих один (`*refusal.Refusal`),
// поэтому разъехаться им нечем: второго текста отказа здесь не пишется.
//
// HTML здесь НЕ рисуется намеренно: лицо для человека — зона `web`, и рисовать своё
// «красивое» рядом с ним значит завести второе лицо мира. Текст отказа — наше дело,
// оформление — нет.
func writeRefusal(w http.ResponseWriter, r *http.Request, ref *refusal.Refusal) {
	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		writeJSON(w, ref.Status, ref)
		return
	}

	var text strings.Builder
	fmt.Fprintf(&text, "отказ: %s\n\n%s\n", ref.Code, ref.Why)
	if ref.From != "" {
		fmt.Fprintf(&text, "\nэто отказ инструмента %s — код его, не наш\n", ref.From)
	}
	if len(ref.Ways) > 0 {
		text.WriteString("\nвыходы:\n")
		for _, way := range ref.Ways {
			fmt.Fprintf(&text, "  · %s\n", way)
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// Код отказа уезжает и заголовком: браузер показывает текст, а тот, кто смотрит
	// глазами в инструменты разработчика, видит машинное имя, по которому спросить.
	w.Header().Set("X-Control-Refusal", ref.Code)
	w.WriteHeader(ref.Status)
	if _, err := io.WriteString(w, text.String()); err != nil {
		log.Printf("control: отказ не записан: %v", err)
	}
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
