// Package guard — СТОРОЖ ЛОКАЦИИ: минимальная служба «я здесь».
//
// Локация вправе стоять пустой (`kb:WORLD-54`): вход в пространство и стройка
// внутри него — разные вещи, и стройка идёт ПОСЛЕ входа. Но присутствие в поле
// проверяется достижимостью (`kb:WORLD-53`): запись о локации, до которой не
// достучаться, превращает реестр двери обратно в чью-то память. Отсюда сторож —
// он отвечает «место существует», как вахта на участке.
//
// Новой сущности мир не заводит: это ТОТ ЖЕ бинарь, что держит дверь, только в
// другом режиме (`kb:WORLD-53`). Один бинарь, два режима, два образа.
//
// # Через сторожа мир даёт доступ ВНУТРЬ места
//
// Локации находятся внутри мира, значит доступ до них у мира есть по
// построению, и отдавать его инструменту стройки нельзя (`kb:WORLD-56`). Форма
// доступа изнутри места — сторож: он уже стоит внутри, уже слушает и уже
// достижим через дверь по маршруту места. Поэтому «мир дотянулся внутрь» на
// коде выглядит как обращение к вахте по её маршруту, а установку исполняет она
// (`internal/build`).
//
// Границы это НЕ сдвигает: место по-прежнему ничего не решает, строит ЮЗЕР
// руками, которые дал мир (`kb:WORLD-61`), а сторож остаётся вахтой — он
// исполняет и отчитывается, а не заводит своё хозяйство.
//
// # Маршрут места ведёт к тому, что на месте стоит
//
// Локация — это адрес и свойства, а ЗАСТРОЙКА есть её состояние (канон
// `WORLD2`, узел `2.0`). Вид идёт по приближению (`1.8`): зайдя внутрь
// локации, юзер видит застройку, а не рассказ вахты о себе, — и пользуется ею
// там, где она стоит (`1.6`). Поэтому маршрут места уходит в застройку, если
// она заявлена, а сторож остаётся хозяином порта и доводит до неё (stands.go).
//
// Пусто — сторож отвечает как отвечал: место существует, внутри ничего не
// построено. Это законное состояние (`1.0`), а не отказ.
//
// Чего сторож НЕ делает — и это граница, а не недоделка:
//   - не разворачивает застройку: развернуть — это доставить И выполнить
//     цепочку (`2.4`), делает это юзер. Сторож только ПОКАЗЫВАЕТ то, что уже
//     поднято, и никогда не поднимает сам;
//   - не раздаёт содержимое места с диска: лежащий на складе клон — это
//     доставленное, а не поднятое, и выдавать одно за другое нельзя;
//   - не ведёт реестр: реестр один на поле, и он за дверью;
//   - не публикует порт локации наружу: наружу торчит одна дверь мира
//     (`kb:FUND-5`), и застройка достижима по маршруту места через неё;
//   - не объявляет, ЧТО локация даёт, сверх одной строки, полученной настройкой:
//     форма объявления участка открыта (`kb:WORLD-50`), а тождество места —
//     отдельная задача (`tasker:WORLD-81`). Опережать их здесь нельзя.
package guard

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/omnifield/world/core/internal/build"
)

// ReadHeaderTimeout — сторож стоит открытым в общей сети, и соединение, которое
// не прислало заголовки, не должно держать его вечно.
const ReadHeaderTimeout = 5 * time.Second

// BuildPath — ручка постройки внутри места. Имя ОДНО на обе стороны шва:
// путь живёт в `internal/build` и здесь только называется вслух, иначе сторож и
// тот, кто к нему ходит, разъехались бы молча.
const BuildPath = build.Path

// Guard — служба присутствия. Имя и «что даёт» приходят настройкой и нужны
// ТОЛЬКО чтобы человек, постучавшийся в локацию, видел, куда попал: сторож
// ничем их не подтверждает и подтверждать не может.
type Guard struct {
	name  string
	gives string
	site  *build.Site
	// stands — застройка места: то, что на нём стоит. Заявлена — маршрут места
	// ведёт к ней, и сторож только доводит (см. stands.go). nil значит «пусто»,
	// и это законное состояние, а не недонастройка.
	stands *Stands
	logf   func(string, ...any)
	now    func() time.Time
	mux    *http.ServeMux
}

// presence — ответ сторожа. Поля названы так, чтобы ответ читался без
// документации: кто ответил, в каком режиме, жив ли, что на месте стоит и
// когда именно.
type presence struct {
	Service string `json:"service"`
	Mode    string `json:"mode"`
	Name    string `json:"name,omitempty"`
	Gives   string `json:"gives,omitempty"`
	Status  string `json:"status"`
	Message string `json:"message"`
	// Build — что стоит на месте. Поля НЕТ вовсе, пока место пустое: пустой
	// объект читался бы как «постройка есть, но про неё ничего не известно», а
	// пустое место — законное состояние (`kb:WORLD-54`), а не отсутствие данных.
	Build *build.Build `json:"build,omitempty"`
	Time  string       `json:"time"`
}

// New собирает сторожа. site — место под стройку (nil значит, что стройка на
// этом месте не настроена вовсе, и ручки постройки скажут это словами); stands —
// заявленная застройка места (nil значит «пусто», и маршрут остаётся сторожу);
// logf — куда идёт строка трассировки (nil даёт log.Printf); now подменяется в
// тестах, чтобы длительность не плавала.
func New(name, gives string, site *build.Site, stands *Stands, logf func(string, ...any), now func() time.Time) *Guard {
	if logf == nil {
		logf = log.Printf
	}
	if now == nil {
		now = time.Now
	}
	g := &Guard{name: name, gives: gives, site: site, stands: stands, logf: logf, now: now, mux: http.NewServeMux()}

	// ДВА ПУТИ СТОРОЖ ДЕРЖИТ ЗА СОБОЙ ЦЕЛИКОМ — и это названная граница, а не
	// умолчание: `/healthz` (на неё смотрит HEALTHCHECK образа и весь контур) и
	// ручка постройки. Всё прочее принадлежит маршруту места и уезжает в
	// застройку, когда она заявлена. Затенены ровно два пути, и застройке,
	// которой они нужны, придётся знать об этом — сказано в core/README.md.
	g.mux.HandleFunc("GET /healthz", g.wrap(g.health))
	g.mux.HandleFunc("/healthz", g.wrap(g.notAccepted))
	g.mux.HandleFunc("GET "+BuildPath, g.wrap(g.getBuild))
	g.mux.HandleFunc("POST "+BuildPath, g.wrap(g.postBuild))
	g.mux.HandleFunc(BuildPath, g.wrap(g.notAccepted))
	// Всё остальное — МАРШРУТ МЕСТА: он ведёт к тому, что на месте стоит, а
	// пустое место отвечает присутствием, как и до застройки (см. место).
	g.mux.HandleFunc("/", g.wrap(g.место))

	return g
}

func (g *Guard) ServeHTTP(w http.ResponseWriter, r *http.Request) { g.mux.ServeHTTP(w, r) }

// wrap даёт каждой ручке строку трассировки. Локация стоит на другой машине, и
// без неё непонятно, дошёл ли до неё стук двери вообще (та же причина, что у
// трассировки двери и приёма стенда).
func (g *Guard) wrap(fn func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		started := g.now()
		rec := &recorder{ResponseWriter: w, code: http.StatusOK}
		fn(rec, r)
		имя := g.name
		if имя == "" {
			имя = "-"
		}
		// Куда ушёл запрос — часть строки, а не отдельный журнал: маршрут места
		// теперь ведёт к застройке, и «кто ответил» видно только по адресу.
		куда := ""
		if rec.куда != "" {
			куда = " → застройка " + rec.куда
		}
		g.logf("guard: %s %s name=%s code=%d dur=%s%s",
			r.Method, r.URL.Path, имя, rec.code, g.now().Sub(started).Round(time.Microsecond), куда)
	}
}

func (g *Guard) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, g.presence("сторож локации на месте"))
}

// getBuild — МЕСТО ГОВОРИТ, ЧТО НА НЁМ СТОИТ. До стройки пусто, и это законное
// состояние (`kb:WORLD-54`), а не отсутствие ответа; после — видно, что это и
// откуда приехало.
func (g *Guard) getBuild(w http.ResponseWriter, _ *http.Request) {
	if g.site == nil {
		writeJSON(w, http.StatusServiceUnavailable, стройкиНет())
		return
	}
	стоит, отказ := g.site.Standing()
	if отказ != nil {
		writeJSON(w, отказ.Status, отказ)
		return
	}
	if стоит == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"built": false,
			"detail": "место пустое: постройки на нём нет. Это законное состояние (kb:WORLD-54) — вход в пространство и стройка внутри него разные вещи. " +
				"Поставить: world build -schema <адрес схемы> (через мир, не заходя в контейнер)",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"built": true, "build": стоит})
}

// postBuild — стройка. Тело ровно одно поле-адрес плюс явная просьба сносить:
// содержимое постройки приезжает гитом по схеме, а не этим запросом.
type стройка struct {
	Schema string `json:"schema"`
	// Replace — снести стоящую постройку и поставить эту. Отдельным полем, а не
	// умолчанием: снос теряет то, что на месте наработано, и случаться молча он
	// не вправе.
	Replace bool `json:"replace"`
}

func (g *Guard) postBuild(w http.ResponseWriter, r *http.Request) {
	if g.site == nil {
		writeJSON(w, http.StatusServiceUnavailable, стройкиНет())
		return
	}

	// Потолок проверяется чтением лишнего байта, а не заголовком Content-Length:
	// заголовок присылает клиент, и верить ему в вопросе «сколько мы согласны
	// прочитать» нельзя (тот же приём, что у двери).
	body, err := io.ReadAll(io.LimitReader(r.Body, build.MaxBody+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, &build.Refusal{Code: "body-unreadable",
			Detail: "тело запроса не дочитано: " + err.Error() + "; повтори — на месте ничего не изменилось"})
		return
	}
	if len(body) > build.MaxBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, &build.Refusal{Code: "body-too-large",
			Detail: fmt.Sprintf("тело больше %d байт — стройка это АДРЕС схемы, а не сама схема: содержимое постройки приезжает гитом", build.MaxBody)})
		return
	}

	var тело стройка
	if отказ := разобрать(body, &тело); отказ != nil {
		writeJSON(w, отказ.Status, отказ)
		return
	}

	готово, исход, отказ := g.site.Raise(тело.Schema, тело.Replace)
	if отказ != nil {
		writeJSON(w, отказ.Status, отказ)
		return
	}

	// Исход — не украшение: повтор обязан называться повтором, а замена
	// заменой. Один код на все три ответил бы «ок» и на «встала», и на «ничего
	// не делали», и человек не отличил бы одно от другого.
	code := http.StatusOK
	if исход == build.OutcomeBuilt {
		code = http.StatusCreated
	}
	writeJSON(w, code, map[string]any{"outcome": исход, "build": готово})
}

// разобрать читает тело стройки. Форма тела названа в каждом отказе: у стройки
// одно обязательное поле, и промах по нему обязан быть виден сразу.
func разобрать(body []byte, v *стройка) *build.Refusal {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return &build.Refusal{Status: http.StatusBadRequest, Code: "body-empty",
			Detail: `тело запроса пустое; стройка — это {"schema":"<адрес репозитория>"} и, если сносим стоящую, {"replace":true}`}
	}
	if trimmed[0] != '{' {
		return &build.Refusal{Status: http.StatusBadRequest, Code: "body-not-object",
			Detail: fmt.Sprintf(`ожидался JSON-объект, тело начинается с %q; стройка — это {"schema":"<адрес репозитория>"}`, string(trimmed[0]))}
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	// Лишнее поле — не молчаливая опечатка: «shema» вместо «schema» иначе уехал
	// бы в отказ «поле schema обязательно», и автор искал бы не там.
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &build.Refusal{Status: http.StatusBadRequest, Code: "json-invalid",
			Detail: fmt.Sprintf(`тело не разбирается: %v; у стройки два поля — schema (обязательное) и replace`, err)}
	}
	return nil
}

// стройкиНет — места под стройку у сторожа нет вовсе. Отдельный отказ, а не
// «внутренняя ошибка»: причина тут в раскладке места, а не в запросе, и чинится
// она настройкой.
func стройкиНет() *build.Refusal {
	return &build.Refusal{Status: http.StatusServiceUnavailable, Code: "build-off",
		Detail: "стройка на этом месте не настроена: сторож поднят без каталога постройки. " +
			"Место при этом стоит и в поле остаётся (kb:WORLD-54). Выход: задай WORLD_BUILD_DIR (либо -build-dir) и подними сторожа заново"}
}

// место — МАРШРУТ МЕСТА, и ведёт он к тому, что на месте стоит.
//
// Порядок здесь и есть правило (канон `WORLD2`, узлы `1.8` и `1.6`): вид идёт
// по приближению, и, зайдя внутрь локации, юзер видит застройку, а не рассказ
// вахты о себе; пользуются стоящей вещью ТАМ, ГДЕ ОНА СТОИТ.
//
//	застройка заявлена — запрос уезжает в неё целиком, любым методом;
//	не заявлена        — прежний ответ сторожа, слово в слово: место
//	                     существует, внутри ничего не построено. Пустая
//	                     локация законна (`1.0`), и это не отказ;
//	заявлена, но мертва либо адрес дурной — названный отказ с причиной и
//	                     выходом (`2.3`), а не пустая страница и не голый 502.
func (g *Guard) место(w http.ResponseWriter, r *http.Request) {
	if g.stands != nil {
		// Куда ушёл запрос — в строку трассировки: без адреса «код 502» на
		// маршруте места не отличить от «сторож сам ответил отказом».
		if rec, ok := w.(*recorder); ok {
			rec.куда = g.stands.Addr()
		}
		g.stands.ServeHTTP(w, r)
		return
	}

	// Дальше — место без застройки, и отвечает оно ровно как отвечало.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		g.notAccepted(w, r)
		return
	}
	if r.URL.Path != "/" {
		// Сторож — конец маршрута, а не его середина: за ним ничего нет, и
		// сказать об этом надо прямо, иначе 404 читается как «локация сломана».
		// Поставленную постройку он тоже не раздаёт: клон лежит на диске места.
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "nothing-here",
			"detail": "в локации по пути " + r.URL.Path + " ничего нет: тут стоит только сторож — он говорит «место существует» и исполняет стройку. " +
				"Локация вправе стоять пустой (kb:WORLD-54), стройка идёт после входа в поле; что на месте стоит — GET " + BuildPath +
				", проба жизни — GET /healthz",
		})
		return
	}
	writeJSON(w, http.StatusOK, g.presence("я здесь: место существует"))
}

// notAccepted — граница режима: сторож не ведёт реестр и не раздаёт содержимое
// места с диска. Принимает он РОВНО ОДНО — постройку: мир даёт доступ внутрь
// места (`kb:WORLD-56`), и вот эта ручка им и является.
//
// Стоит он в двух случаях: на пустом месте — на любом не-чтении, и на двух
// путях, которые сторож держит за собой (`/healthz`, ручка постройки), — там
// он отвечает и при заявленной застройке. Второе названо в отказе вслух: иначе
// автор застройки видел бы «405» на своём же пути и не понимал, кто ответил.
func (g *Guard) notAccepted(w http.ResponseWriter, r *http.Request) {
	detail := "сторож локации принимает ровно одно — постройку: POST " + BuildPath + ` {"schema":"<адрес репозитория>"}. ` +
		"А " + r.Method + " " + r.URL.Path + " ему нести некуда: он не ведёт реестр и не раздаёт содержимое места с диска. " +
		"Реестр поля — за дверью мира: POST/GET /api/locations"
	if g.stands != nil {
		detail += ". Маршрут места ведёт в застройку (" + g.stands.Addr() + "), но этот путь сторож держит за собой — " +
			"застройке он не достаётся"
	}
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "guard-accepts-nothing", "detail": detail})
}

// presence — «я здесь» плюс то, что на месте стоит. Постройка читается ЛУЧШИМ
// усилием: беда с ней не повод отвечать «место сломано» — место стоит, а
// причину назовёт своими словами GET /api/build.
func (g *Guard) presence(message string) presence {
	p := presence{
		Service: "world",
		Mode:    "guard",
		Name:    g.name,
		Gives:   g.gives,
		Status:  "ok",
		Message: message,
		Time:    g.now().UTC().Format(time.RFC3339),
	}
	if g.site != nil {
		стоит, отказ := g.site.Standing()
		switch {
		case отказ != nil:
			p.Message = message + ", но с постройкой беда: " + отказ.Code + " — причину и выход называет GET " + BuildPath
		case стоит == nil:
			p.Message = message + ", внутри пока ничего не построено"
		default:
			p.Build = стоит
			p.Message = message + ", на нём стоит постройка по схеме " + стоит.Schema
		}
	}
	p.Message += g.кудаВедётМаршрут()
	return p
}

// кудаВедётМаршрут — хвост присутствия про застройку. Нужен именно здесь: при
// заявленной застройке сам маршрут места отвечает уже НЕ сторожем, и увидеть,
// куда он ведёт, можно только на пробе жизни — она за сторожем остаётся.
func (g *Guard) кудаВедётМаршрут() string {
	if g.stands == nil {
		return ""
	}
	if изъян := g.stands.Изъян(); изъян != nil {
		return "; маршрут места заявлен в застройку (" + g.stands.Addr() + "), но адрес не годится — " + изъян.Error()
	}
	return "; маршрут места ведёт в застройку " + g.stands.Addr()
}

// Run поднимает сторожа на адресе и не возвращается, пока служба жива.
func Run(addr string, g *Guard) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf(
			"сторож не встал на %s: %w; порт занят либо адрес не тот — что слушать, задаётся WORLD_ADDR либо -listen (умолчание :8080)",
			addr, err)
	}
	return Serve(ln, g)
}

// Serve — то же самое на готовом слушателе. Разделено не ради слоёв: «дверь
// считает сторожа достижимым» проверяется ТОЛЬКО живым сокетом, и проба должна
// брать его без гонки за свободный порт (см. guard_test.go).
func Serve(ln net.Listener, g *Guard) error {
	srv := &http.Server{
		Handler:           g,
		ReadHeaderTimeout: ReadHeaderTimeout,
	}
	return srv.Serve(ln)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("guard: ответ не записан: %v", err)
	}
}

// recorder — счётчик кода ответа: без него в трассировке нечего писать.
// «куда» проставляется, когда запрос ушёл в застройку, — и уезжает в ту же
// строку трассировки.
type recorder struct {
	http.ResponseWriter
	code int
	куда string
}

func (r *recorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap отдаёт исходный ResponseWriter — по нему net/http находит Flush и
// Hijack (та же причина, что у двери: обёртка не должна скрывать интерфейсы).
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

var _ http.Handler = (*Guard)(nil)
