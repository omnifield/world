package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
func роутер(t *testing.T) http.Handler {
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
	return newRouter(
		ingest.NewHandler(store, молча, fixedNow),
		door.NewHandler(registry, молча, fixedNow, доступен),
		fixedNow)
}

func TestHelloОтдаётПаспортМира(t *testing.T) {
	srv := роутер(t)

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
	srv := роутер(t)

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

// Дверь ничего не показывает (`WORLD2` 5.1 п.4): пришедший на корень за пультом
// обязан УЗНАТЬ, куда идти, а не получить пустоту. Проба держит именно это —
// код «было и снято» плюс адрес лица в теле.
func TestКореньГоворитЧтоЛицоЖивётПриКонтроллере(t *testing.T) {
	srv := роутер(t)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusGone {
		t.Fatalf("код ответа: получено %d (%s), ожидалось %d — пульт с двери снят насовсем",
			rec.Code, rec.Body.String(), http.StatusGone)
	}
	for _, кусок := range []string{"face-elsewhere", "КОНТРОЛЛЕР", door.Prefix} {
		if !strings.Contains(rec.Body.String(), кусок) {
			t.Errorf("в отказе нет %q: %s", кусок, rec.Body.String())
		}
	}
}

// Отказ не зависит от метода: промах мимо лица остаётся промахом и на POST.
// Раньше здесь стоял 405 от раздачи статики — раздачи больше нет, а молчать
// на POST в корень значит снова отдать пустоту.
func TestОтказНаКорнеНеЗависитОтМетода(t *testing.T) {
	srv := роутер(t)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))

	if rec.Code != http.StatusGone {
		t.Fatalf("код ответа: получено %d (%s), ожидалось %d", rec.Code, rec.Body.String(), http.StatusGone)
	}
	if !strings.Contains(rec.Body.String(), "face-elsewhere") {
		t.Errorf("отказ не назван: %s", rec.Body.String())
	}
}

// Второй промах — не за пультом, а мимо локации. Он обязан отличаться от
// первого: путь верный, а имени нет в поле, и ответ говорит, где смотреть поле.
func TestПутьМимоЛокацииОтвечаетЧтоЕёНетВПоле(t *testing.T) {
	srv := роутер(t)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/нет-такой/api/build", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("код ответа: получено %d (%s), ожидалось %d", rec.Code, rec.Body.String(), http.StatusNotFound)
	}
	for _, кусок := range []string{"not-in-field", door.Prefix} {
		if !strings.Contains(rec.Body.String(), кусок) {
			t.Errorf("в отказе нет %q: %s", кусок, rec.Body.String())
		}
	}
}

// Главный вопрос совместимости: новый префикс не съел старые ручки.
// Проверяется на одном дереве и после живой записи в журнал — так ловится и
// перехват маршрута, и падение общего роутера на приёме.
func TestПриёмСтендаНеЛомаетСтаруюДверь(t *testing.T) {
	srv := роутер(t)

	событие := `{"session":"run-1","ts":"2026-08-07T18:23:34Z","seq":1,"attempt":"place baser at 0,0","allowed":true}`
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/stand/events", strings.NewReader(событие)))
	if rec.Code != http.StatusOK {
		t.Fatalf("приём события: получено %d (%s), ожидалось %d", rec.Code, rec.Body.String(), http.StatusOK)
	}

	tests := []struct {
		name, path, вТеле string
	}{
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
	srv := роутер(t)

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

// Маршрут выводится из РЕГИСТРАЦИИ (kb:WORLD-53), и проверять это надо на
// ПОЛНОМ дереве: в пакете door маршрут живёт без соседних префиксов, а здесь
// рядом стоят приём стенда, ручки и отказ на корне — перехватить может любой.
func TestЗаРегистрациейМаршрутРаботаетНаПолномДереве(t *testing.T) {
	srv := роутер(t)

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
	srv := роутер(t)

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
