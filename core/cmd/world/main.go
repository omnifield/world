// Смоук-каркас мира: Go отдаёт JSON и раздаёт статику зоны web.
//
// Это ПРОВЕРКА ТУЛЧЕЙНА, а не движок мира. Базовые правила (почва · гравитация ·
// атмосфера, tasker:PILOT-13) сюда не заложены и закладываться здесь не должны:
// маршрут требует сначала разложить их, а потом строить. Всё, что этот файл
// доказывает, — что в девбоксе едет Go, едет веб и едет связка между ними.
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
)

const (
	defaultAddr   = ":8080"
	defaultWebDir = "../web"
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

// newRouter собирает маршруты поверх каталога статики. Вынесен из main отдельно,
// чтобы тест поднимал ровно то же дерево маршрутов, что и живой процесс.
func newRouter(webDir string, now func() time.Time) http.Handler {
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

	// Статика зоны web. Граница зон соблюдена: core ЧИТАЕТ web на рантайме и
	// никогда её не правит — владелец файлов остаётся owner-web.
	mux.Handle("GET /", http.FileServer(http.Dir(webDir)))

	return mux
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

	srv := &http.Server{
		Addr:              addr,
		Handler:           newRouter(webDir, time.Now),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("world: слушаю %s, статика из %s", addr, webDir)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("world: сервер остановлен: %v", err)
	}
}
