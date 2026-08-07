package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omnifield/world/core/internal/ingest"
)

// fixedNow — чтобы поле time проверялось значением, а не «похоже на дату».
func fixedNow() time.Time {
	return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
}

// роутер поднимает дерево маршрутов ЦЕЛИКОМ — со смонтированным приёмом
// стенда. Проверять старые ручки на урезанном дереве бессмысленно: вопрос
// ровно в том, живы ли они рядом с новым префиксом.
func роутер(t *testing.T, webDir string) http.Handler {
	t.Helper()
	store, err := ingest.New(filepath.Join(t.TempDir(), "stand"))
	if err != nil {
		t.Fatalf("каталог прогонов не поднялся: %v", err)
	}
	return newRouter(webDir, ingest.NewHandler(store, func(string, ...any) {}, fixedNow), fixedNow)
}

func TestHelloОтдаётПаспортМира(t *testing.T) {
	srv := роутер(t, t.TempDir())

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hello", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("код ответа: получено %d, ожидалось %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: получено %q, ожидался application/json", ct)
	}

	var got helloPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("тело не разбирается как JSON: %v (тело: %s)", err, rec.Body.String())
	}

	want := helloPayload{
		Product: "world",
		Zone:    "core",
		Status:  "ok",
		Message: "мир отвечает",
		Time:    "2026-08-06T12:00:00Z",
	}
	if got != want {
		t.Errorf("ответ: получено %+v, ожидалось %+v", got, want)
	}
}

func TestHealthzОтвечаетКакОстальнойКонтур(t *testing.T) {
	srv := роутер(t, t.TempDir())

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("код ответа: получено %d, ожидалось %d", rec.Code, http.StatusOK)
	}

	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("тело не разбирается как JSON: %v", err)
	}
	if got["service"] != "world" || got["status"] != "ok" {
		t.Errorf("healthz: получено %+v, ожидалось service=world status=ok", got)
	}
}

func TestКореньРаздаётСтатикуЗоныWeb(t *testing.T) {
	dir := t.TempDir()
	const markup = "<!doctype html><title>мир</title>"
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(markup), 0o644); err != nil {
		t.Fatalf("подготовка статики не удалась: %v", err)
	}

	srv := роутер(t, dir)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("код ответа: получено %d, ожидалось %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != markup {
		t.Errorf("отдан не тот файл: получено %q, ожидалось %q", body, markup)
	}
}

// Главный вопрос совместимости: новый префикс не съел ни статику, ни старые
// ручки. Проверяется на одном дереве и после живой записи в журнал — так
// ловится и перехват маршрута, и падение общего роутера на приёме.
func TestПриёмСтендаНеЛомаетСтаруюДверь(t *testing.T) {
	dir := t.TempDir()
	const markup = "<!doctype html><title>мир</title>"
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(markup), 0o644); err != nil {
		t.Fatalf("подготовка статики не удалась: %v", err)
	}
	srv := роутер(t, dir)

	событие := `{"session":"run-1","ts":"2026-08-07T18:23:34Z","seq":1,"attempt":"place baser at 0,0","allowed":true}`
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/stand/events", strings.NewReader(событие)))
	if rec.Code != http.StatusOK {
		t.Fatalf("приём события: получено %d (%s), ожидалось %d", rec.Code, rec.Body.String(), http.StatusOK)
	}

	tests := []struct {
		name, path, вТеле string
	}{
		{"статика", "/", markup},
		{"healthz", "/healthz", `"status":"ok"`},
		{"hello", "/api/hello", `"product":"world"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("код ответа: получено %d, ожидалось %d", rec.Code, http.StatusOK)
			}
			if !strings.Contains(rec.Body.String(), tt.вТеле) {
				t.Errorf("тело: получено %q, ожидалась подстрока %q", rec.Body.String(), tt.вТеле)
			}
		})
	}
}

func TestResolveWebDirНазываетПричинуОтказа(t *testing.T) {
	файл := filepath.Join(t.TempDir(), "не-каталог.txt")
	if err := os.WriteFile(файл, []byte("x"), 0o644); err != nil {
		t.Fatalf("подготовка не удалась: %v", err)
	}

	tests := []struct {
		name    string
		dir     string
		вОшибке string
	}{
		{"каталога нет", filepath.Join(t.TempDir(), "нет-такого"), "каталога статики нет"},
		{"путь ведёт в файл", файл, "должна быть каталогом"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveWebDir(tt.dir)
			if err == nil {
				t.Fatalf("ожидалась ошибка, получено nil")
			}
			if !strings.Contains(err.Error(), tt.вОшибке) {
				t.Errorf("ошибка не называет причину: получено %q, ожидалась подстрока %q", err, tt.вОшибке)
			}
		})
	}
}

func TestResolveWebDirПринимаетЖивойКаталог(t *testing.T) {
	dir := t.TempDir()

	got, err := resolveWebDir(dir)
	if err != nil {
		t.Fatalf("живой каталог отвергнут: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("путь должен быть абсолютным, получено %q", got)
	}
}
