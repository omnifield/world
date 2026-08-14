// Сервис мира: ДВЕРЬ в поле (`:8080`) плюс то, что за ней стоит, — реестр
// локаций и приём журнала стенда.
//
// Лица для человека здесь НЕТ и не будет: дверь — вход и выход, она ничего не
// показывает (`WORLD2` 5.1 п.4, 3.7). Пульт раздаёт контроллер (зона control):
// власть над машиной у входной двери означала бы дверь с ключами от всего дома.
//
// Наружу машины торчит ровно один порт (`kb:FUND-5`), и он принадлежит МИРУ,
// а не продукту (`kb:WORLD-53`): дверь умирает вместе с полем. Все маршруты
// к локациям выводятся из регистрации — см. internal/door.
//
// Чего здесь по-прежнему НЕТ: базовых правил мира (почва · гравитация ·
// атмосфера, tasker:PILOT-13). Маршрут требует сначала разложить их, а потом
// строить, и дверь их появление не приближает и не подменяет.
package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/omnifield/world/core/internal/door"
	"github.com/omnifield/world/core/internal/ingest"
)

const (
	defaultAddr     = ":8080"
	defaultStandDir = "../stand"
	// Реестр локаций — состояние ПОЛЯ, а не прогонов стенда, поэтому лежит
	// отдельно от них. Каталог рантаймовый, в репозиторий не едет.
	defaultDoorFile = "../field/locations.json"
	// Каталог стройки — сторона ЛОКАЦИИ, а не мира: в нём стоит постройка,
	// поставленная через мир. Умолчание относительное, как и остальные, и верно
	// для запуска из `core/`; образ локации задаёт его явно (зона `deploy`).
	defaultBuildDir = "../build"
)

// helloPayload — ответ смоук-эндпоинта. Поля названы так, чтобы ответ читался
// без документации: кто ответил, жив ли, и когда именно.
type helloPayload struct {
	Product string `json:"product"`
	Zone    string `json:"zone"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Time    string `json:"time"`
}

// newRouter собирает маршруты двери и приёма стенда. Вынесен из main отдельно,
// чтобы тест поднимал ровно то же дерево маршрутов, что и живой процесс.
//
// Дверь обязательна: без реестра локаций сервис перестаёт быть входом в поле.
// Приём стенда — необязателен (nil выключает).
func newRouter(standIngest http.Handler, doorH *door.Handler, now func() time.Time) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/hello", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, helloPayload{
			Product: "world",
			Zone:    "core",
			Status:  "ok",
			Message: "мир отвечает",
			Time:    now().UTC().Format(time.RFC3339),
		})
	})

	// healthz — по образцу tasker/knowledger: одинаковая дверь у всех сервисов контура.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"service": "world", "status": "ok"})
	})

	// Приём журнала испытательного стенда — целиком под своим префиксом,
	// вместе с трассировкой и CORS (см. core/internal/ingest). Методы названы
	// поимённо не для красоты: без метода этот шаблон конфликтует с «GET /»
	// корневого маршрута двери, и роутер падает на старте.
	if standIngest != nil {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodOptions} {
			mux.Handle(method+" "+ingest.Prefix, standIngest)
		}
	}

	// Реестр локаций — свои ручки под своим префиксом. Стоит ВЫШЕ корня, и это
	// не порядок строк, а правило маршрута: имена ручек двери зарезервированы
	// (door.ReservedNames), и локация с таким именем отвергается на регистрации.
	mux.Handle(door.Prefix, doorH)
	mux.Handle(door.Prefix+"/", doorH)

	// Корень — дверь и только дверь: имя в реестре уводит запрос к локации,
	// всё прочее упирается в названный отказ. Смотреть за дверью больше нечего
	// (лица здесь нет), поэтому за реестром стоит не раздача, а объяснение.
	mux.Handle("/", doorH.Route(noFace()))

	return mux
}

// noFace — то, что стоит за реестром на корне: дверь ничего не показывает и
// говорит об этом вслух. Отдавать здесь пустоту нельзя — пришедший за пультом
// увидел бы «сервис сломан» вместо «смотреть надо в другом месте»
// (`kb:WORLD-31`: мир называет причину и выход, а не притворяется).
//
// Ответа два, потому что и промаха два, и путать их дорого:
//   - КОРЕНЬ — здесь пульт когда-то жил и уехал насовсем. 410 Gone: ресурс был
//     и снят навсегда (RFC 9110 §15.5.11, сверено 2026-08-14). 404 отправил бы
//     искать опечатку в пути, хотя путь верный, а адрес — чужой.
//   - ЛЮБОЙ ДРУГОЙ ПУТЬ — первый сегмент не совпал ни с одной локацией в поле.
//     404, и в теле сказано, где посмотреть, кто в поле.
func noFace() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			writeJSON(w, http.StatusGone, map[string]string{
				"error": "face-elsewhere",
				"detail": "дверь — вход и выход, она ничего не показывает: пульт раздаёт КОНТРОЛЛЕР " +
					"(зона control, умолчание http://127.0.0.1:8090). " +
					"Кто сейчас в поле, видно в GET " + door.Prefix,
			})
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "not-in-field",
			"detail": "за дверью такого маршрута нет: путь ведёт к локации, а её нет в поле — " +
				"кто в поле, видно в GET " + door.Prefix + ". " +
				"Если это запрос к пульту — лицо живёт при контроллере, дверь ничего не показывает",
		})
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("ответ не записан: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// main разводит два режима одного бинаря (`kb:WORLD-53`): в мире он держит
// дверь, в локации стоит сторожем. Новой сущности мир не заводит.
//
// Без подкоманды — ДВЕРЬ, как было: образ мира зовёт бинарь без аргументов
// (`ENTRYPOINT ["/app/world"]`), и это требование совместимости, а не умолчание
// на всякий случай. Подкоманды — в commands.go.
func main() {
	if len(os.Args) > 1 {
		os.Exit(runCommand(os.Args[1:], streams{out: os.Stdout, err: os.Stderr}))
	}
	runDoor()
}

// runDoor — режим мира: дверь, реестр локаций и приём стенда. Пульта здесь
// нет: лицо для человека раздаёт контроллер.
func runDoor() {
	addr := envOr("WORLD_ADDR", defaultAddr)

	store, err := ingest.New(envOr("WORLD_STAND_DIR", defaultStandDir))
	if err != nil {
		log.Fatalf("world: %v", err)
	}

	registry, err := door.Open(envOr("WORLD_DOOR_FILE", defaultDoorFile))
	if err != nil {
		log.Fatalf("world: %v", err)
	}
	doorH := door.NewHandler(registry, log.Printf, time.Now, nil)

	srv := &http.Server{
		Addr:              addr,
		Handler:           newRouter(ingest.NewHandler(store, log.Printf, time.Now), doorH, time.Now),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Поле в стартовой строке — не украшение: «реестр переживает рестарт»
	// проверяется этой цифрой сразу после подъёма, а не догадкой.
	log.Printf("world: слушаю %s, прогоны стенда в %s, реестр локаций %s (в поле: %d)",
		addr, store.Dir(), registry.File(), len(registry.List()))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("world: сервер остановлен: %v", err)
	}
}
