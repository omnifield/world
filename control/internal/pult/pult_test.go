package pult

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// собранный — каталог, похожий на результат сборки зоны `web`: страница и файл с хэшем
// в имени. Содержимое неважно: зона `control` пульт не рисует, она его отдаёт.
func собранный(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"),
		[]byte(`<!doctype html><script src="/assets/index-XY.js"></script>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "index-XY.js"), []byte("// пульт"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func зов(t *testing.T, h *Handler, method, path string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	ref := h.Serve(rec, httptest.NewRequest(method, path, nil))
	code := ""
	if ref != nil {
		code = ref.Code
		if strings.TrimSpace(ref.Why) == "" || len(ref.Ways) == 0 {
			t.Fatalf("отказ %s без причины или без выхода: %+v", code, ref)
		}
	}
	return rec, code
}

func TestПультОтдаётся(t *testing.T) {
	h := New(собранный(t))

	rec, code := зов(t, h, "GET", "/")
	if code != "" {
		t.Fatalf("корень отказал: %s", code)
	}
	if !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Fatalf("по корню приехала не страница: %q", rec.Body.String())
	}

	if rec, code = зов(t, h, "GET", "/assets/index-XY.js"); code != "" {
		t.Fatalf("файл сборки отказал: %s", code)
	}
	if rec.Body.String() != "// пульт" {
		t.Fatalf("файл сборки приехал не тот: %q", rec.Body.String())
	}
}

// Кэш назначается по имени: у файлов сборки хэш в имени и они вечны, а `index.html`
// меняется каждой сборкой — закэшированный, он показывал бы прошлый пульт при живом новом.
func TestКэшРазныйУСтраницыИФайловСборки(t *testing.T) {
	h := New(собранный(t))

	rec, _ := зов(t, h, "GET", "/")
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("страница кэшируется как %q — человек увидит прошлый пульт", got)
	}
	rec, _ = зов(t, h, "GET", "/assets/index-XY.js")
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("файл сборки не помечен вечным: %q", got)
	}
}

// Главное свойство этого пакета: пульта нет — контроллер ГОВОРИТ об этом, а не отдаёт
// пустую страницу и не молчит четырёхсотым.
func TestПультаНетСказаноКодомПричинойИВыходом(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "пусто"))

	rec, code := зов(t, h, "GET", "/")
	if code != "no-pult" {
		t.Fatalf("ждали no-pult, получили %q", code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("раздача сама что-то написала в ответ — печатать отказ не её дело: %q", rec.Body.String())
	}
	if !strings.Contains(h.State(), "ВЫКЛЮЧЕН") {
		t.Fatalf("стартовая строка молчит о том, что пульта нет: %q", h.State())
	}
}

func TestКаталогНеНазванЭтоТожеОтказ(t *testing.T) {
	if _, code := зов(t, New("  "), "GET", "/"); code != "no-pult" {
		t.Fatalf("ждали no-pult, получили %q", code)
	}
}

func TestПустаяСтраницаНеСчитаетсяПультом(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, code := зов(t, New(dir), "GET", "/"); code != "no-pult" {
		t.Fatalf("пустой index.html принят за пульт: %q", code)
	}
}

// Исходник вместо сборки — отдельный отказ: он открывается и НЕ ПОКАЗЫВАЕТ НИЧЕГО, и
// человек ищет поломку в мире, а не в раскладке.
func TestИсходникВместоСборкиНазванОтдельно(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"),
		[]byte(`<script type="module" src="/src/main.tsx"></script>`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, code := зов(t, New(dir), "GET", "/")
	if code != "pult-not-built" {
		t.Fatalf("ждали pult-not-built, получили %q", code)
	}
}

func TestКаталогНеЛистается(t *testing.T) {
	h := New(собранный(t))
	rec, code := зов(t, h, "GET", "/assets/")
	if code != "unknown-page" {
		t.Fatalf("каталог отдан вместо отказа: %q", code)
	}
	if strings.Contains(rec.Body.String(), "index-XY.js") {
		t.Fatalf("список файлов образа уехал наружу: %q", rec.Body.String())
	}
}

// Путь из запроса — единственное, что приходит снаружи в файловую систему, а прав у
// контроллера много: он держит докер-сокет и связку ключей.
func TestОбходПутиНеРаботает(t *testing.T) {
	dir := собранный(t)
	secret := filepath.Join(filepath.Dir(dir), "секрет")
	if err := os.WriteFile(secret, []byte("ключ юзера"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := New(dir)

	for _, path := range []string{"/../секрет", "/../../etc/passwd", "/assets/../../секрет"} {
		rec, code := зов(t, h, "GET", path)
		if strings.Contains(rec.Body.String(), "ключ юзера") {
			t.Fatalf("%s увёл чтение за каталог пульта", path)
		}
		if code == "" && rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "root:") {
			t.Fatalf("%s отдал системный файл", path)
		}
	}
}

func TestНеТотГлаголНазванПромахом(t *testing.T) {
	if _, code := зов(t, New(собранный(t)), "POST", "/"); code != "wrong-method" {
		t.Fatalf("ждали wrong-method, получили %q", code)
	}
}

// Состояние проверяется на КАЖДОМ запросе, а не только на старте: в разработке каталог
// пересобирается под работающим процессом, и «проверили один раз» означало бы врать до
// перезапуска.
func TestСостояниеПеречитываетсяНаКаждомЗапросе(t *testing.T) {
	dir := t.TempDir()
	h := New(dir)
	if _, code := зов(t, h, "GET", "/"); code != "no-pult" {
		t.Fatalf("ждали no-pult, получили %q", code)
	}

	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html>приехал"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, code := зов(t, h, "GET", "/")
	if code != "" || !strings.Contains(rec.Body.String(), "приехал") {
		t.Fatalf("появившийся пульт не подхвачен: code=%q body=%q", code, rec.Body.String())
	}
}
