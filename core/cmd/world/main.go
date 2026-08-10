// Сервис мира: ДВЕРЬ в поле (`:8080`) плюс то, что за ней стоит, — реестр
// локаций, приём журнала стенда и раздача статики зоны web.
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
	defaultAddr     = ":8080"
	defaultWebDir   = "../web"
	defaultStandDir = "../stand"
	// Реестр локаций — состояние ПОЛЯ, а не прогонов стенда, поэтому лежит
	// отдельно от них. Каталог рантаймовый, в репозиторий не едет.
	defaultDoorFile = "../field/locations.json"
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

// newRouter собирает маршруты поверх каталога статики, приёма стенда и двери.
// Вынесен из main отдельно, чтобы тест поднимал ровно то же дерево маршрутов,
// что и живой процесс.
//
// Дверь обязательна: без реестра локаций сервис перестаёт быть входом в поле
// и становится раздачей статики. Приём стенда — необязателен (nil выключает).
func newRouter(webDir string, standIngest http.Handler, doorH *door.Handler, now func() time.Time) http.Handler {
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
	// Граница зон соблюдена: core ЧИТАЕТ web на рантайме и никогда её не
	// правит — владелец файлов остаётся owner-web.
	mux.Handle("/", doorH.Route(staticHandler(webDir)))

	return mux
}

// staticHandler — раздача статики зоны web. Метод проверяем сами: FileServer
// отдаёт файл на любой метод, а «POST в статику» — это не запрос содержимого,
// а промах мимо ручки, и назвать его надо промахом.
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

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("ответ не записан: %v", err)
	}
}

// resolveWebDir проверяет каталог статики ДО старта и падает с адресом на руках.
// Молча подняться без статики — худший исход: смоук отчитается «работает»,
// а браузер покажет 404, и причина будет не названа.
func resolveWebDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("путь статики не разрешается: %w", err)
	}
	info, err := os.Stat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("каталога статики нет: %s (запускать из core/ либо задать WORLD_WEB_DIR)", abs)
	}
	if err != nil {
		return "", fmt.Errorf("каталог статики не читается: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("статика должна быть каталогом, а это файл: %s", abs)
	}
	return abs, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	addr := envOr("WORLD_ADDR", defaultAddr)

	webDir, err := resolveWebDir(envOr("WORLD_WEB_DIR", defaultWebDir))
	if err != nil {
		log.Fatalf("world: %v", err)
	}

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
		Handler:           newRouter(webDir, ingest.NewHandler(store, log.Printf, time.Now), doorH, time.Now),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Поле в стартовой строке — не украшение: «реестр переживает рестарт»
	// проверяется этой цифрой сразу после подъёма, а не догадкой.
	log.Printf("world: слушаю %s, статика из %s, прогоны стенда в %s, реестр локаций %s (в поле: %d)",
		addr, webDir, store.Dir(), registry.File(), len(registry.List()))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("world: сервер остановлен: %v", err)
	}
}
