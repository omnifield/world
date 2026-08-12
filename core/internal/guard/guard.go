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
// Чего сторож НЕ делает — и это граница, а не недоделка:
//   - не раздаёт статику: витрина локации — стройка, а стройки может не быть.
//     Поставленную постройку он тоже НЕ раздаёт и не запускает: клон лежит на
//     диске места, а кто его поднимает — вопрос следующей ступени;
//   - не ведёт реестр: реестр один на поле, и он за дверью;
//   - не проксирует: сторож — конец маршрута, а не его середина;
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
	logf  func(string, ...any)
	now   func() time.Time
	mux   *http.ServeMux
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
// этом месте не настроена вовсе, и ручки постройки скажут это словами); logf —
// куда идёт строка трассировки (nil даёт log.Printf); now подменяется в тестах,
// чтобы длительность не плавала.
func New(name, gives string, site *build.Site, logf func(string, ...any), now func() time.Time) *Guard {
	if logf == nil {
		logf = log.Printf
	}
	if now == nil {
		now = time.Now
	}
	g := &Guard{name: name, gives: gives, site: site, logf: logf, now: now, mux: http.NewServeMux()}

	// healthz — по образцу двери и остального контура: одинаковая проба жизни у
	// всех служб. На неё смотрит HEALTHCHECK образа локации.
	g.mux.HandleFunc("GET /healthz", g.wrap(g.health))
	// Постройка — ЕДИНСТВЕННОЕ, что сторож принимает, и единственное, о чём он
	// отчитывается сверх собственного присутствия. Шаблоны с методом стоят выше
	// «GET /» и «/» по точности пути, а не по порядку строк.
	g.mux.HandleFunc("GET "+BuildPath, g.wrap(g.getBuild))
	g.mux.HandleFunc("POST "+BuildPath, g.wrap(g.postBuild))
	// «GET /» — все чтения; корень отвечает присутствием, остальные пути
	// названным отказом (см. root). Шаблон с методом матчит и HEAD, поэтому
	// проба HEAD'ом работает без отдельной ручки.
	g.mux.HandleFunc("GET /", g.wrap(g.root))
	// Всё, что не чтение, — промах мимо сторожа, и назвать его надо промахом:
	// сторож ничего не принимает, ему нечего записывать.
	g.mux.HandleFunc("/", g.wrap(g.notAccepted))

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
		g.logf("guard: %s %s name=%s code=%d dur=%s",
			r.Method, r.URL.Path, имя, rec.code, g.now().Sub(started).Round(time.Microsecond))
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

// root — «я здесь» человеку, который открыл маршрут локации за дверью. Пустая
// локация обязана отвечать ЧЕМ-ТО осмысленным: пустой ответ на маршруте читается
// как «сломано», хотя место существует и стоит ровно так, как задумано.
func (g *Guard) root(w http.ResponseWriter, r *http.Request) {
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

// notAccepted — граница режима: сторож не ведёт реестр, не раздаёт статику и не
// проксирует. Принимает он РОВНО ОДНО — постройку: мир даёт доступ внутрь места
// (`kb:WORLD-56`), и вот эта ручка им и является.
func (g *Guard) notAccepted(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
		"error": "guard-accepts-nothing",
		"detail": "сторож локации принимает ровно одно — постройку: POST " + BuildPath + ` {"schema":"<адрес репозитория>"}. ` +
			"А " + r.Method + " " + r.URL.Path + " ему нести некуда: он не ведёт реестр, не раздаёт статику и не проксирует. " +
			"Реестр поля — за дверью мира: POST/GET /api/locations",
	})
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
	if g.site == nil {
		return p
	}
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
	return p
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
type recorder struct {
	http.ResponseWriter
	code int
}

func (r *recorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap отдаёт исходный ResponseWriter — по нему net/http находит Flush и
// Hijack (та же причина, что у двери: обёртка не должна скрывать интерфейсы).
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

var _ http.Handler = (*Guard)(nil)
