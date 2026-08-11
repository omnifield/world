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
// Чего сторож НЕ делает — и это граница, а не недоделка:
//   - не раздаёт статику: витрина локации — стройка, а стройки может не быть;
//   - не ведёт реестр: реестр один на поле, и он за дверью;
//   - не проксирует: сторож — конец маршрута, а не его середина;
//   - не объявляет, ЧТО локация даёт, сверх одной строки, полученной настройкой:
//     форма объявления участка открыта (`kb:WORLD-50`), а тождество места —
//     отдельная задача (`tasker:WORLD-81`). Опережать их здесь нельзя.
package guard

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

// ReadHeaderTimeout — сторож стоит открытым в общей сети, и соединение, которое
// не прислало заголовки, не должно держать его вечно.
const ReadHeaderTimeout = 5 * time.Second

// Guard — служба присутствия. Имя и «что даёт» приходят настройкой и нужны
// ТОЛЬКО чтобы человек, постучавшийся в локацию, видел, куда попал: сторож
// ничем их не подтверждает и подтверждать не может.
type Guard struct {
	name  string
	gives string
	logf  func(string, ...any)
	now   func() time.Time
	mux   *http.ServeMux
}

// presence — ответ сторожа. Поля названы так, чтобы ответ читался без
// документации: кто ответил, в каком режиме, жив ли и когда именно.
type presence struct {
	Service string `json:"service"`
	Mode    string `json:"mode"`
	Name    string `json:"name,omitempty"`
	Gives   string `json:"gives,omitempty"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Time    string `json:"time"`
}

// New собирает сторожа. logf — куда идёт строка трассировки (nil даёт
// log.Printf); now подменяется в тестах, чтобы длительность не плавала.
func New(name, gives string, logf func(string, ...any), now func() time.Time) *Guard {
	if logf == nil {
		logf = log.Printf
	}
	if now == nil {
		now = time.Now
	}
	g := &Guard{name: name, gives: gives, logf: logf, now: now, mux: http.NewServeMux()}

	// healthz — по образцу двери и остального контура: одинаковая проба жизни у
	// всех служб. На неё смотрит HEALTHCHECK образа локации.
	g.mux.HandleFunc("GET /healthz", g.wrap(g.health))
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

// root — «я здесь» человеку, который открыл маршрут локации за дверью. Пустая
// локация обязана отвечать ЧЕМ-ТО осмысленным: пустой ответ на маршруте читается
// как «сломано», хотя место существует и стоит ровно так, как задумано.
func (g *Guard) root(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		// Сторож — конец маршрута, а не его середина: за ним ничего нет, и
		// сказать об этом надо прямо, иначе 404 читается как «локация сломана».
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "nothing-here",
			"detail": "в локации по пути " + r.URL.Path + " ничего нет: тут стоит только сторож — он говорит «место существует» и больше ничего. " +
				"Локация вправе стоять пустой (kb:WORLD-54), стройка идёт после входа в поле; проба жизни — GET /healthz",
		})
		return
	}
	writeJSON(w, http.StatusOK, g.presence("я здесь: место существует, внутри пока ничего не построено"))
}

// notAccepted — сторож ничего не принимает. Это не «метод не поддержан ещё», а
// граница режима: приём и реестр живут за дверью, а не в локации.
func (g *Guard) notAccepted(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
		"error": "guard-accepts-nothing",
		"detail": "сторож локации отвечает только на чтение (GET/HEAD), а " + r.Method + " ему нести некуда: он не ведёт реестр, не раздаёт статику и не проксирует. " +
			"Реестр поля — за дверью мира: POST/GET /api/locations",
	})
}

func (g *Guard) presence(message string) presence {
	return presence{
		Service: "world",
		Mode:    "guard",
		Name:    g.name,
		Gives:   g.gives,
		Status:  "ok",
		Message: message,
		Time:    g.now().UTC().Format(time.RFC3339),
	}
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
