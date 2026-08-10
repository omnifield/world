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

	"github.com/omnifield/world/core/internal/door"
	"github.com/omnifield/world/core/internal/ingest"
)

// fixedNow — чтобы поле time проверялось значением, а не «похоже на дату».
func fixedNow() time.Time {
	return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
}

// роутер поднимает дерево маршрутов ЦЕЛИКОМ — со смонтированным приёмом
// стенда и дверью. Проверять старые ручки на урезанном дереве бессмысленно:
// вопрос ровно в том, живы ли они рядом с новыми префиксами.
func роутер(t *testing.T, webDir string) http.Handler {
	t.Helper()
	store, err := ingest.New(filepath.Join(t.TempDir(), "stand"))
	if err != nil {
		t.Fatalf("каталог прогонов не поднялся: %v", err)
	}
	registry, err := door.Open(filepath.Join(t.TempDir(), "field", "locations.json"))
	if err != nil {
		t.Fatalf("реестр локаций не поднялся: %v", err)
	}
	молча := func(string, ...any) {}
	// Проба адреса в тестах роутера всегда согласна: здесь проверяется дерево
	// маршрутов, а отказы регистрации — в пакете door, каждый своей пробой.
	доступен := func(string) error { return nil }
	return newRouter(webDir,
		ingest.NewHandler(store, молча, fixedNow),
		door.NewHandler(registry, молча, fixedNow, доступен),
		fixedNow)
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

// Тот же вопрос совместимости, что и у приёма стенда, но теперь дверь стоит
// на КОРНЕ: она смотрит на каждый запрос первой. Если бы она перехватывала
// лишнее, ломались бы ровно старые ручки.
func TestДверьНеЛомаетСтарыеРучки(t *testing.T) {
	dir := t.TempDir()
	const markup = "<!doctype html><title>мир</title>"
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(markup), 0o644); err != nil {
		t.Fatalf("подготовка статики не удалась: %v", err)
	}
	srv := роутер(t, dir)

	локация := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer локация.Close()
	тело := `{"name":"baser","addr":"` + strings.TrimPrefix(локация.URL, "http://") + `","gives":"консоль сборки"}`
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/locations", strings.NewReader(тело)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("регистрация: получено %d (%s), ожидалось %d", rec.Code, rec.Body.String(), http.StatusCreated)
	}

	tests := []struct {
		name, path, вТеле string
	}{
		{"статика", "/", markup},
		{"healthz", "/healthz", `"status":"ok"`},
		{"hello", "/api/hello", `"product":"world"`},
		{"события стенда", "/api/stand/events?session=нет-такой", `"error"`},
		{"список локаций", "/api/locations", `"baser"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if !strings.Contains(rec.Body.String(), tt.вТеле) {
				t.Errorf("тело: получено %q, ожидалась подстрока %q (код %d)", rec.Body.String(), tt.вТеле, rec.Code)
			}
		})
	}
}

// Маршрут выводится из РЕГИСТРАЦИИ (kb:WORLD-53), а не из того, что лежит на
// диске. Проверяется столкновением: файл с именем локации и локация с тем же
// именем — побеждает локация, иначе список за дверью снова разойдётся с полем.
func TestМаршрутЛокацииСильнееФайлаСТемЖеИменем(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "baser"), 0o755); err != nil {
		t.Fatalf("подготовка статики не удалась: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "baser", "index.html"), []byte("это файл, а не локация"), 0o644); err != nil {
		t.Fatalf("подготовка статики не удалась: %v", err)
	}
	srv := роутер(t, dir)

	локация := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("это локация"))
	}))
	defer локация.Close()
	тело := `{"name":"baser","addr":"` + strings.TrimPrefix(локация.URL, "http://") + `","gives":"консоль сборки"}`
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/locations", strings.NewReader(тело)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("регистрация: получено %d (%s)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/baser/", nil))
	if rec.Body.String() != "это локация" {
		t.Errorf("за дверью ответило %q, ожидалось %q", rec.Body.String(), "это локация")
	}
}

// Имена, которые дверь держит за собой, обязаны БЫТЬ её маршрутами. Список
// door.ReservedNames — не список запретов ради запретов: вычеркни оттуда имя,
// и локация с ним зарегистрируется и окажется недостижимой.
func TestИменаЗарезервированныеДверьюЕйЖеИПринадлежат(t *testing.T) {
	srv := роутер(t, t.TempDir())

	чейМаршрут := map[string]string{
		"api":     "/api/hello",
		"healthz": "/healthz",
	}
	for _, имя := range door.ReservedNames {
		путь, ok := чейМаршрут[имя]
		if !ok {
			t.Errorf("имя %q зарезервировано дверью, но ни одной её ручки под ним нет — либо ручка потерялась, либо резерв лишний", имя)
			continue
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, путь, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("ручка %q зарезервированного имени %q отвечает %d, ожидалось %d", путь, имя, rec.Code, http.StatusOK)
		}
	}
}

func TestСтатикаОтвечаетТолькоНаЧтение(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("страница"), 0o644); err != nil {
		t.Fatalf("подготовка статики не удалась: %v", err)
	}
	srv := роутер(t, dir)

	// POST в статику — это промах мимо ручки, а не запрос содержимого.
	// FileServer отдал бы файл и на POST, и промах остался бы неназванным.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("код ответа: получено %d (%s), ожидалось %d", rec.Code, rec.Body.String(), http.StatusMethodNotAllowed)
	}
	if !strings.Contains(rec.Body.String(), "/api/locations") {
		t.Errorf("отказ не подсказывает, где смотреть поле: %s", rec.Body.String())
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
