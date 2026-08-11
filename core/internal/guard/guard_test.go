package guard

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omnifield/world/core/internal/door"
)

func fixedNow() time.Time { return time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC) }

func сторож(t *testing.T) *Guard {
	t.Helper()
	return New("probe-loc", "проба присутствия", func(string, ...any) {}, fixedNow)
}

func ответ(t *testing.T, g *Guard, method, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	var тело map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &тело); err != nil {
		t.Fatalf("%s %s: тело не разбирается как JSON: %v (тело: %s)", method, path, err, rec.Body.String())
	}
	return rec.Code, тело
}

// Главное, что сторож обязан уметь: сказать «я здесь». Пустой ответ на маршруте
// локации читается как «сломано», хотя место существует (`kb:WORLD-54`).
func TestКореньГоворитЧтоМестоСуществует(t *testing.T) {
	code, тело := ответ(t, сторож(t), http.MethodGet, "/")

	if code != http.StatusOK {
		t.Fatalf("код ответа: получено %d, ожидалось %d", code, http.StatusOK)
	}
	for поле, ожидалось := range map[string]string{
		"service": "world",
		"mode":    "guard",
		"name":    "probe-loc",
		"gives":   "проба присутствия",
		"status":  "ok",
		"time":    "2026-08-11T12:00:00Z",
	} {
		if got, _ := тело[поле].(string); got != ожидалось {
			t.Errorf("поле %q: получено %q, ожидалось %q", поле, got, ожидалось)
		}
	}
	if msg, _ := тело["message"].(string); !strings.Contains(msg, "я здесь") {
		t.Errorf("сторож не сказал «я здесь»: %q", msg)
	}
}

// Проба жизни у всех служб контура одинаковая — на неё смотрит HEALTHCHECK
// образа локации.
func TestПробаЖизниОтвечаетКакОстальнойКонтур(t *testing.T) {
	code, тело := ответ(t, сторож(t), http.MethodGet, "/healthz")

	if code != http.StatusOK {
		t.Fatalf("код ответа: получено %d, ожидалось %d", code, http.StatusOK)
	}
	if тело["service"] != "world" || тело["status"] != "ok" {
		t.Errorf("healthz: получено %+v, ожидалось service=world status=ok", тело)
	}
	// mode отличает сторожа от двери: в журнале и в ответе видно, какой из двух
	// режимов одного бинаря поднят, — иначе перепутанный образ ищут глазами.
	if тело["mode"] != "guard" {
		t.Errorf("режим в ответе: получено %v, ожидалось guard", тело["mode"])
	}
}

// HEAD — то, чем службу проверяют пробы, и отдельной ручки под него нет.
// Сломай шаблон «GET /healthz» на «POST …» — и проба перестанет отвечать.
func TestПробаЖизниОтвечаетИНаHEAD(t *testing.T) {
	rec := httptest.NewRecorder()
	сторож(t).ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD /healthz: получено %d, ожидалось %d", rec.Code, http.StatusOK)
	}
}

// Граница режима: сторож НЕ ведёт реестр. Реестр один на поле и живёт за
// дверью; ответь сторож на регистрацию — и у поля стало бы два списка.
func TestСторожНеВедётРеестр(t *testing.T) {
	g := сторож(t)

	t.Run("регистрация в сторожа не проходит", func(t *testing.T) {
		rec := httptest.NewRecorder()
		тело := strings.NewReader(`{"name":"x","addr":"x:1","gives":"y"}`)
		g.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/locations", тело))

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("код ответа: получено %d (%s), ожидалось %d", rec.Code, rec.Body.String(), http.StatusMethodNotAllowed)
		}
		if !strings.Contains(rec.Body.String(), "guard-accepts-nothing") {
			t.Errorf("отказ не назван: %s", rec.Body.String())
		}
		// Причина без выхода — диагноз, по которому непонятно, что делать.
		if !strings.Contains(rec.Body.String(), "/api/locations") {
			t.Errorf("отказ не подсказывает, где реестр на самом деле: %s", rec.Body.String())
		}
	})

	t.Run("списка поля у сторожа нет", func(t *testing.T) {
		code, тело := ответ(t, g, http.MethodGet, "/api/locations")
		if code != http.StatusNotFound {
			t.Fatalf("код ответа: получено %d, ожидалось %d", code, http.StatusNotFound)
		}
		if тело["error"] != "nothing-here" {
			t.Errorf("отказ: получено %v, ожидалось nothing-here", тело["error"])
		}
	})
}

func TestНеизвестныйПутьНазываетПричинуИВыход(t *testing.T) {
	code, тело := ответ(t, сторож(t), http.MethodGet, "/что-то/внутри")

	if code != http.StatusNotFound {
		t.Fatalf("код ответа: получено %d, ожидалось %d", code, http.StatusNotFound)
	}
	detail, _ := тело["detail"].(string)
	for _, кусок := range []string{"только сторож", "GET /healthz"} {
		if !strings.Contains(detail, кусок) {
			t.Errorf("в отказе нет %q: %q", кусок, detail)
		}
	}
}

// Трассировка — не украшение: локация стоит на другой машине, и без строки
// непонятно, дошёл ли до неё стук двери вообще.
func TestКаждыйЗапросОставляетСтроку(t *testing.T) {
	var строки []string
	g := New("probe-loc", "", func(f string, a ...any) {
		строки = append(строки, fmt.Sprintf(f, a...))
	}, fixedNow)

	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if len(строки) != 1 {
		t.Fatalf("строк трассировки: получено %d, ожидалась 1", len(строки))
	}
	for _, кусок := range []string{"guard:", "name=", "code=", "dur="} {
		if !strings.Contains(строки[0], кусок) {
			t.Errorf("в строке трассировки нет %q: %q", кусок, строки[0])
		}
	}
}

// САМАЯ ВАЖНАЯ проба сторожа: присутствие в поле проверяется достижимостью
// (`kb:WORLD-53`), и проверяет её ДВЕРЬ — той самой пробой, которой она стучится
// в любого соседа. Поэтому здесь живой сокет, а не httptest-обработчик: вопрос
// ровно в том, есть ли по адресу кто-нибудь.
func TestДверьСчитаетСторожаДостижимым(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("слушатель не поднялся: %v", err)
	}
	g := сторож(t)
	go func() { _ = Serve(ln, g) }() // служба живёт до конца теста
	t.Cleanup(func() { ln.Close() })

	if err := door.DialProbe(2 * time.Second)(ln.Addr().String()); err != nil {
		t.Fatalf("дверь не достучалась до сторожа по %s: %v — с таким адресом локация в поле не войдёт", ln.Addr(), err)
	}

	// И то же самое живьём поверх HTTP: сокет открыт — этого двери довольно,
	// но человеку, пришедшему по маршруту, отвечает уже сторож.
	resp, err := http.Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("проба жизни по сети не прошла: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("проба жизни: получено %d, ожидалось %d", resp.StatusCode, http.StatusOK)
	}
}

// Порт занят — сторож не встаёт, и отказ называет причину и выход. Молчаливое
// «процесс кончился» здесь читается как «образ сломан».
func TestЗанятыйПортНазываетПричинуИВыход(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("слушатель не поднялся: %v", err)
	}
	defer ln.Close()

	err = Run(ln.Addr().String(), сторож(t))
	if err == nil {
		t.Fatal("сторож встал на занятый порт — этого не может быть")
	}
	for _, кусок := range []string{"сторож не встал", "WORLD_ADDR"} {
		if !strings.Contains(err.Error(), кусок) {
			t.Errorf("в отказе нет %q: %v", кусок, err)
		}
	}
}
