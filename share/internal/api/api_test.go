package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omnifield/world/share/internal/state"
)

const пароль = "пароль-скоупа"

type журнал struct{ строки []string }

func (ж *журнал) писать(format string, v ...any) {
	ж.строки = append(ж.строки, strings.TrimSpace(fmt.Sprintf(format, v...)))
}

func поднять(t *testing.T) (http.Handler, *журнал) {
	t.Helper()
	ж := &журнал{}
	h := New(Options{
		Store:    state.New(filepath.Join(t.TempDir(), "scope.json"), 0),
		Password: пароль,
		Log:      ж.писать,
	})
	return h, ж
}

func запрос(t *testing.T, h http.Handler, метод, путь, пароль, тело string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if тело == "" {
		r = httptest.NewRequest(метод, путь, nil)
	} else {
		r = httptest.NewRequest(метод, путь, strings.NewReader(тело))
	}
	if пароль != "" {
		// Имя не называем намеренно: раздача его не смотрит — личность определяется
		// адресом, а не именем в заголовке.
		r.SetBasicAuth("", пароль)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func код(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var ref struct {
		Code string   `json:"code"`
		Why  string   `json:"why"`
		Ways []string `json:"ways"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ref); err != nil {
		t.Fatalf("отказ пришёл не тройкой, а %q", w.Body.String())
	}
	if ref.Why == "" || len(ref.Ways) == 0 {
		t.Fatalf("отказ %q без причины или без выхода: %s", ref.Code, w.Body.String())
	}
	return ref.Code
}

// ── две ручки, и обе работают файлом целиком ─────────────────────────────────

func TestДвеРучкиОтдаютИПринимаютЦеликом(t *testing.T) {
	h, _ := поднять(t)

	состояние := `{"формат":1,"личность":{"имя":"егор","бренд":""},"ключи":[],"территории":[],"поля":[]}`
	w := запрос(t, h, http.MethodPut, "/", пароль, состояние)
	if w.Code != http.StatusNoContent {
		t.Fatalf("PUT ответил %d: %s", w.Code, w.Body.String())
	}

	w = запрос(t, h, http.MethodGet, "/", пароль, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET ответил %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != состояние {
		t.Fatalf("отдано %q вместо того, что клали", w.Body.String())
	}
}

// Свежесозданный скоуп — это личность и пустота (`WORLD2` 3.4, п. 5). Раздача обязана
// принять его как есть: пустые списки — законное состояние, а не поломка. Ровно на этом
// уже споткнулась панель (`WORLD2-135`).
func TestПустыеРазделыИПустойБрендЗаконны(t *testing.T) {
	h, _ := поднять(t)
	свежий := `{"формат":1,"личность":{"имя":"я","бренд":"","описание":""},"ключи":[],"территории":[],"поля":[]}`
	if w := запрос(t, h, http.MethodPut, "/", пароль, свежий); w.Code != http.StatusNoContent {
		t.Fatalf("свежий скоуп не приняли: %d %s", w.Code, w.Body.String())
	}
	w := запрос(t, h, http.MethodGet, "/", пароль, "")
	if w.Body.String() != свежий {
		t.Fatalf("свежий скоуп вернулся другим: %q", w.Body.String())
	}
}

// Внутрь файла раздача не смотрит. Доказательство — то, что она берёт заведомо не-JSON:
// появись здесь разбор, эта проба покраснеет первой.
func TestФайлНеРазбираетсяВовсе(t *testing.T) {
	h, _ := поднять(t)
	мусор := "ни разу не JSON: {{{ \x01\x02"
	if w := запрос(t, h, http.MethodPut, "/", пароль, мусор); w.Code != http.StatusNoContent {
		t.Fatalf("раздача полезла внутрь файла: %d %s", w.Code, w.Body.String())
	}
	if w := запрос(t, h, http.MethodGet, "/", пароль, ""); w.Body.String() != мусор {
		t.Fatalf("отдано %q — раздача что-то поправила по дороге", w.Body.String())
	}
}

func TestСостоянияЕщёНетОтвечаетNoState(t *testing.T) {
	h, _ := поднять(t)
	w := запрос(t, h, http.MethodGet, "/", пароль, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("ответ %d вместо 404", w.Code)
	}
	if c := код(t, w); c != "no-state" {
		t.Fatalf("код %q вместо no-state", c)
	}
}

// ── пароль ───────────────────────────────────────────────────────────────────

func TestБезПароляНеОтдаётИНеПринимает(t *testing.T) {
	h, _ := поднять(t)
	for _, метод := range []string{http.MethodGet, http.MethodPut} {
		w := запрос(t, h, метод, "/", "", "что-нибудь")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s без пароля ответил %d", метод, w.Code)
		}
		if c := код(t, w); c != "no-creds" {
			t.Fatalf("%s без пароля: код %q вместо no-creds", метод, c)
		}
		if w.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("%s: 401 без заголовка вызова — это молчание с текстом", метод)
		}
	}
}

func TestНеверныйПарольОтказываетСПричинойИВыходом(t *testing.T) {
	h, _ := поднять(t)
	w := запрос(t, h, http.MethodGet, "/", "не тот", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("ответ %d вместо 401", w.Code)
	}
	if c := код(t, w); c != "bad-creds" {
		t.Fatalf("код %q вместо bad-creds", c)
	}
}

// Запись под неверным паролем не должна доехать до диска ВООБЩЕ: иначе «нет» пришло бы
// после того, как чужая личность уже легла на место.
func TestНеверныйПарольНеПишетНичего(t *testing.T) {
	h, _ := поднять(t)
	if w := запрос(t, h, http.MethodPut, "/", пароль, "моё"); w.Code != http.StatusNoContent {
		t.Fatalf("своя запись отказала: %d", w.Code)
	}
	if w := запрос(t, h, http.MethodPut, "/", "чужой", "чужое"); w.Code != http.StatusUnauthorized {
		t.Fatalf("чужая запись ответила %d", w.Code)
	}
	w := запрос(t, h, http.MethodGet, "/", пароль, "")
	if w.Body.String() != "моё" {
		t.Fatalf("после чужой записи лежит %q", w.Body.String())
	}
}

// Пароль проверяется ДО пути: иначе посторонний по разным отказам узнавал бы, что за этим
// адресом вообще что-то лежит.
func TestПарольПроверяетсяРаньшеПути(t *testing.T) {
	h, _ := поднять(t)
	w := запрос(t, h, http.MethodGet, "/чужой-путь", "", "")
	if c := код(t, w); c != "no-creds" {
		t.Fatalf("код %q вместо no-creds — путь разобрали раньше пароля", c)
	}
}

// ── границы: третьей ручки нет, разделов нет ─────────────────────────────────

func TestТретьейРучкиНет(t *testing.T) {
	h, _ := поднять(t)
	for _, метод := range []string{http.MethodPost, http.MethodDelete, http.MethodPatch, http.MethodHead} {
		w := запрос(t, h, метод, "/", пароль, "")
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s ответил %d вместо 405", метод, w.Code)
		}
		if allow := w.Header().Get("Allow"); allow != "GET, PUT" {
			t.Fatalf("%s: Allow=%q — клиенту неоткуда узнать, что тут можно", метод, allow)
		}
		if метод != http.MethodHead {
			if c := код(t, w); c != "bad-handle" {
				t.Fatalf("%s: код %q вместо bad-handle", метод, c)
			}
		}
	}
}

func TestРазделовУРаздачиНет(t *testing.T) {
	h, _ := поднять(t)
	for _, путь := range []string{"/ключи", "/территории", "/scope/егор"} {
		w := запрос(t, h, http.MethodGet, путь, пароль, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s ответил %d вместо 404", путь, w.Code)
		}
		if c := код(t, w); c != "no-such-address" {
			t.Fatalf("%s: код %q вместо no-such-address", путь, c)
		}
	}
}

// ── чужая раздача равноправна: своих знаков в протоколе нет ──────────────────
//
// Проба стережёт ГРАНИЦУ ЗОНЫ, а не удобство: появись у ответа свой заголовок — и клиент
// однажды начнёт по нему узнавать «свою» раздачу, а чужая («хоть на ардуино») перестанет
// быть равноправной (`WORLD2` 0.3, 3.4).
func TestНиОдногоСвоегоЗаголовкаВОтвете(t *testing.T) {
	h, _ := поднять(t)
	if w := запрос(t, h, http.MethodPut, "/", пароль, "состояние"); w.Code != http.StatusNoContent {
		t.Fatalf("запись отказала: %d", w.Code)
	}
	w := запрос(t, h, http.MethodGet, "/", пароль, "")

	разрешено := map[string]bool{
		"Content-Type": true, "Content-Length": true, "Cache-Control": true,
		"Www-Authenticate": true, "Allow": true, "Date": true,
	}
	for имя := range w.Header() {
		if !разрешено[имя] {
			t.Fatalf("в ответе заголовок %q — это опознавательный знак нашей раздачи", имя)
		}
	}
}

// ── журнал ───────────────────────────────────────────────────────────────────

func TestКаждыйЗапросОставляетСтрокуВЖурналеБезПароля(t *testing.T) {
	h, ж := поднять(t)
	запрос(t, h, http.MethodPut, "/", пароль, "состояние")
	запрос(t, h, http.MethodGet, "/", пароль, "")
	запрос(t, h, http.MethodGet, "/", "не тот", "")

	if len(ж.строки) != 3 {
		t.Fatalf("строк в журнале %d вместо 3: %v", len(ж.строки), ж.строки)
	}
	if !strings.Contains(ж.строки[0], "PUT /") || !strings.Contains(ж.строки[0], "204") {
		t.Fatalf("запись не видна в журнале: %q", ж.строки[0])
	}
	if !strings.Contains(ж.строки[2], "bad-creds") {
		t.Fatalf("отказ не назван в журнале своим кодом: %q", ж.строки[2])
	}
	for _, s := range ж.строки {
		if strings.Contains(s, пароль) {
			t.Fatalf("пароль утёк в журнал: %q", s)
		}
	}
}
