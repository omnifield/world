// Сервис мира: ДВЕРЬ в поле (`:8080`) плюс то, что за ней стоит, — реестр
// локаций, приём журнала стенда и раздача СОБРАННОЙ страницы зоны web.
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
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/omnifield/world/core/internal/door"
	"github.com/omnifield/world/core/internal/ingest"
)

const (
	defaultAddr = ":8080"
	// Наружу уезжает АРТЕФАКТ СБОРКИ зоны web, а не её исходник. Раньше в
	// `web/` лежала голая статика и дефолт `../web` был верен; теперь там фронт
	// на сборщике, и `web/index.html` — исходник со ссылкой на `/src/main.tsx`,
	// которого в собранном виде нет. Отдать его значит ответить 200 страницей,
	// которая ничего не показывает.
	defaultWebDir   = "../web/dist"
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

// newRouter собирает маршруты поверх раздачи статики, приёма стенда и двери.
// Вынесен из main отдельно, чтобы тест поднимал ровно то же дерево маршрутов,
// что и живой процесс.
//
// Дверь обязательна: без реестра локаций сервис перестаёт быть входом в поле
// и становится раздачей статики. Приём стенда — необязателен (nil выключает).
// Раздача приходит ГОТОВЫМ обработчиком, а не путём: собрана страница или нет,
// решается один раз на старте (resolveStatic), и роутер об этом уже не думает.
func newRouter(static http.Handler, standIngest http.Handler, doorH *door.Handler, now func() time.Time) http.Handler {
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
	// раздачи статики, и роутер падает на старте.
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

	// Корень — сначала дверь, потом статика зоны web. Маршрут выводится из
	// регистрации (kb:WORLD-53), поэтому реестр спрашивают раньше файловой
	// системы: иначе файл с именем локации молча перехватил бы её маршрут.
	//
	// Граница зон соблюдена: core ЧИТАЕТ собранную страницу web на рантайме и
	// никогда её не правит — владелец файлов остаётся owner-web. Собирает её
	// тоже он: сборка чужой зоны не дело двери.
	mux.Handle("/", doorH.Route(static))

	return mux
}

// resolveStatic решает на старте, чем будет раздача: собранной страницей зоны
// web либо названным отказом. Возвращает обработчик и то, что уйдёт в стартовую
// строку.
//
// Страница не собрана — НЕ повод не пускать в поле: дверь, реестр и ручки
// поднимаются в любом случае, поэтому здесь предупреждение, а не Fatal. Но и
// промолчать нельзя: отдать исходник вместо сборки — ошибка раскладки, а мир
// обязан называть её, а не притворяться (`kb:WORLD-31`). Молчание тут и есть
// дефект: снаружи это выглядит как рабочая страница, которая ничего не
// показывает, и искать причину идут не туда.
func resolveStatic(dir string) (http.Handler, string) {
	abs, изъян := checkWebDir(dir)
	if изъян != nil {
		log.Printf("world: ПРЕДУПРЕЖДЕНИЕ: %v", изъян)
		return staticOff(изъян.Error()), "ВЫКЛЮЧЕНА — " + изъян.Error()
	}
	return staticHandler(abs), "из " + abs
}

// staticHandler — раздача собранной страницы зоны web. Метод проверяем сами:
// FileServer отдаёт файл на любой метод, а «POST в статику» — это не запрос
// содержимого, а промах мимо ручки, и назвать его надо промахом.
func staticHandler(webDir string) http.Handler {
	files := http.FileServer(http.Dir(webDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			files.ServeHTTP(w, r)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
				"error": "method-not-allowed",
				"detail": "статика отдаётся по GET и HEAD; " +
					"если это запрос к локации — её нет в поле, кто в поле, видно в GET " + door.Prefix,
			})
		}
	})
}

// staticOff — раздача выключена, и каждый запрос к ней говорит об этом вслух.
// 503, а не 404: файла не «нет», его негде взять — витрина не собрана, и это
// состояние сервиса, а не адреса. 404 отправил бы искать опечатку в пути.
func staticOff(причина string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "web-not-built",
			"detail": причина + "; дверь и ручки при этом работают — " +
				"кто сейчас в поле, видно в GET " + door.Prefix,
		})
	})
}

// checkWebDir проверяет то, что дверь собирается отдавать наружу, ДО первого
// запроса. Каждый изъян называет причину и выход: без выхода это диагноз, по
// которому непонятно, что делать.
func checkWebDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("путь статики не разрешается: %w; раздача выключена", err)
	}
	info, err := os.Stat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf(
			"каталога собранной страницы нет: %s — страница зоны web не собрана: pnpm -C web build (либо задай WORLD_WEB_DIR)", abs)
	}
	if err != nil {
		return "", fmt.Errorf("каталог статики не читается: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("статика должна быть каталогом, а это файл: %s — задай WORLD_WEB_DIR каталогом сборки", abs)
	}
	// Без index.html раздача ответила бы 404 на корень — ответ, который читается
	// как «сервиса нет», хотя дверь работает. Проверка узкая и названа честно:
	// она ловит недособранный или не тот каталог, но НЕ отличает исходник от
	// сборки — у исходника зоны web свой index.html, и по нему их не развести.
	// От исходника защищает дефолт (../web/dist), а не эта проверка.
	if _, err := os.Stat(filepath.Join(abs, "index.html")); errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf(
			"в каталоге статики %s нет index.html — это не собранная страница: pnpm -C web build", abs)
	} else if err != nil {
		return "", fmt.Errorf("index.html в каталоге статики не читается: %w", err)
	}
	return abs, nil
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

// runDoor — режим мира: дверь, реестр локаций, приём стенда и раздача пульта.
func runDoor() {
	addr := envOr("WORLD_ADDR", defaultAddr)

	static, откудаСтатика := resolveStatic(envOr("WORLD_WEB_DIR", defaultWebDir))

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
		Handler:           newRouter(static, ingest.NewHandler(store, log.Printf, time.Now), doorH, time.Now),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Поле в стартовой строке — не украшение: «реестр переживает рестарт»
	// проверяется этой цифрой сразу после подъёма, а не догадкой. Статика — там
	// же и по той же причине: видно, отдаём мы сборку или не отдаём ничего.
	log.Printf("world: слушаю %s, статика %s, прогоны стенда в %s, реестр локаций %s (в поле: %d)",
		addr, откудаСтатика, store.Dir(), registry.File(), len(registry.List()))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("world: сервер остановлен: %v", err)
	}
}
