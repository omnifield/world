package guard

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omnifield/world/core/internal/build"
	"github.com/omnifield/world/core/internal/door"
	"github.com/omnifield/world/core/internal/schematest"
)

func fixedNow() time.Time { return time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC) }

// сторож — место БЕЗ настроенной стройки: так поднимался сторож до того, как
// мир начал давать доступ внутрь места, и так он поднимется, если каталог
// стройки не задан. Пробы присутствия обязаны работать и в этом случае.
func сторож(t *testing.T) *Guard {
	t.Helper()
	return New("probe-loc", "проба присутствия", nil, nil, func(string, ...any) {}, fixedNow)
}

// сторожСМестом — сторож с настоящим местом под стройку: пустой каталог, в
// который ещё ничего не приехало.
func сторожСМестом(t *testing.T) (*Guard, *build.Site) {
	t.Helper()
	site := build.Open(filepath.Join(t.TempDir(), "стройка"), func(string, ...any) {}, fixedNow)
	return New("probe-loc", "проба присутствия", site, nil, func(string, ...any) {}, fixedNow), site
}

// послать — запрос с телом: стройка это POST, и на httptest.NewRequest без тела
// её не проверить.
func послать(t *testing.T, g *Guard, method, path, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	var тело map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &тело); err != nil {
		t.Fatalf("%s %s: тело не разбирается как JSON: %v (тело: %s)", method, path, err, rec.Body.String())
	}
	return rec.Code, тело
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
	g := New("probe-loc", "", nil, nil, func(f string, a ...any) {
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

// ── доступ внутрь места: стройка ─────────────────────────────────────────────

// ГЛАВНОЕ, ради чего сторож перестал быть только вахтой: через него мир даёт
// доступ ВНУТРЬ места (`kb:WORLD-56`). Постройка приезжает схемой, встаёт, и
// место после этого говорит, что на нём стоит.
func TestСторожСтавитПостройкуИМестоГоворитЧтоНаНёмСтоит(t *testing.T) {
	схема := schematest.Схема(t, map[string]string{"README.md": "постройка пробы"})
	g, _ := сторожСМестом(t)

	код, тело := послать(t, g, http.MethodPost, BuildPath, `{"schema":"`+схема+`"}`)

	if код != http.StatusCreated {
		t.Fatalf("код стройки: получено %d (%v), ожидалось %d", код, тело, http.StatusCreated)
	}
	if тело["outcome"] != string(build.OutcomeBuilt) {
		t.Errorf("исход: получено %v, ожидалось %q", тело["outcome"], build.OutcomeBuilt)
	}
	встала, _ := тело["build"].(map[string]any)
	if встала["schema"] != схема {
		t.Errorf("схема в ответе: получено %v, ожидалось %q", встала["schema"], схема)
	}
	if встала["commit"] != schematest.Коммит(t, схема) {
		t.Errorf("коммит в ответе: получено %v — «встала» подтверждается им", встала["commit"])
	}

	// Место говорит, что на нём стоит: и своей ручкой, и присутствием на корне.
	код, тело = ответ(t, g, http.MethodGet, BuildPath)
	if код != http.StatusOK || тело["built"] != true {
		t.Fatalf("место не говорит о постройке: %d %v", код, тело)
	}
	if стоит, _ := тело["build"].(map[string]any); стоит["schema"] != схема {
		t.Errorf("на месте стоит не та схема: %v", тело["build"])
	}

	_, корень := ответ(t, g, http.MethodGet, "/")
	if корень["build"] == nil {
		t.Error("на корне не видно постройки — «я здесь» обязано говорить и о том, что стоит")
	}
	if msg, _ := корень["message"].(string); !strings.Contains(msg, схема) {
		t.Errorf("в присутствии не названа схема постройки: %q", msg)
	}
}

// До стройки место ПУСТОЕ, и это законное состояние (`kb:WORLD-54`), а не отказ.
func TestПустоеМестоОтвечаетЧестноИЭтоНеОтказ(t *testing.T) {
	g, _ := сторожСМестом(t)

	код, тело := ответ(t, g, http.MethodGet, BuildPath)

	if код != http.StatusOK {
		t.Fatalf("код ответа пустого места: получено %d, ожидалось %d — пусто это не отказ", код, http.StatusOK)
	}
	if тело["built"] != false {
		t.Errorf("пустое место сказало, что на нём что-то стоит: %v", тело)
	}
	detail, _ := тело["detail"].(string)
	for _, кусок := range []string{"kb:WORLD-54", "world build"} {
		if !strings.Contains(detail, кусок) {
			t.Errorf("в ответе пустого места нет %q: %q", кусок, detail)
		}
	}
	// И присутствие на корне говорит то же самое, а не молчит.
	_, корень := ответ(t, g, http.MethodGet, "/")
	if msg, _ := корень["message"].(string); !strings.Contains(msg, "ничего не построено") {
		t.Errorf("присутствие не говорит, что место пустое: %q", msg)
	}
}

// Повтор и замена — исходы, а не «ок» на всё подряд.
func TestИсходыСтройкиНазваныПоИмени(t *testing.T) {
	перваяСхема := schematest.Схема(t, map[string]string{"первая.txt": "1"})
	втораяСхема := schematest.Схема(t, map[string]string{"вторая.txt": "2"})
	g, _ := сторожСМестом(t)

	if код, тело := послать(t, g, http.MethodPost, BuildPath, `{"schema":"`+перваяСхема+`"}`); код != http.StatusCreated {
		t.Fatalf("первая стройка: %d %v", код, тело)
	}

	t.Run("повтор той же схемы — confirmed и 200, а не 201", func(t *testing.T) {
		код, тело := послать(t, g, http.MethodPost, BuildPath, `{"schema":"`+перваяСхема+`"}`)
		if код != http.StatusOK || тело["outcome"] != string(build.OutcomeConfirmed) {
			t.Errorf("повтор: получено %d %v, ожидалось 200 confirmed", код, тело)
		}
	})

	t.Run("другая схема без сноса — отказ с выходом", func(t *testing.T) {
		код, тело := послать(t, g, http.MethodPost, BuildPath, `{"schema":"`+втораяСхема+`"}`)
		if код != http.StatusConflict || тело["error"] != "build-present" {
			t.Fatalf("другая схема: получено %d %v, ожидалось 409 build-present", код, тело)
		}
		if detail, _ := тело["detail"].(string); !strings.Contains(detail, "-replace") {
			t.Errorf("в отказе нет выхода: %q", detail)
		}
	})

	t.Run("другая схема со сносом — replaced", func(t *testing.T) {
		код, тело := послать(t, g, http.MethodPost, BuildPath, `{"schema":"`+втораяСхема+`","replace":true}`)
		if код != http.StatusOK || тело["outcome"] != string(build.OutcomeReplaced) {
			t.Errorf("замена: получено %d %v, ожидалось 200 replaced", код, тело)
		}
	})
}

// Форма тела стройки. Каждый промах назван своим словом: «schema обязательно»
// в ответ на опечатку «shema» отправило бы автора искать не там.
func TestФормаТелаСтройкиНазываетПромахСвоимИменем(t *testing.T) {
	g, _ := сторожСМестом(t)

	for _, случай := range []struct {
		имя, тело, код string
		статус         int
	}{
		{"пустое тело", "", "body-empty", http.StatusBadRequest},
		{"не объект", `["схема"]`, "body-not-object", http.StatusBadRequest},
		{"лишнее поле", `{"schema":"/x","shema":"/y"}`, "json-invalid", http.StatusBadRequest},
		{"нет схемы", `{}`, "field-missing", http.StatusBadRequest},
		{"тело больше потолка", `{"schema":"` + strings.Repeat("a", build.MaxBody) + `"}`, "body-too-large", http.StatusRequestEntityTooLarge},
	} {
		t.Run(случай.имя, func(t *testing.T) {
			код, тело := послать(t, g, http.MethodPost, BuildPath, случай.тело)
			if код != случай.статус || тело["error"] != случай.код {
				t.Errorf("получено %d %v, ожидалось %d %s", код, тело, случай.статус, случай.код)
			}
		})
	}
}

// Стройка не настроена — место всё равно СТОИТ, а отказ называет, чем это
// чинится. Скоуп не зависит от стройки (`kb:WORLD-54`).
func TestБезКаталогаСтройкиМестоСтоитАСтройкаНазываетВыход(t *testing.T) {
	g := сторож(t) // site == nil

	if код, _ := ответ(t, g, http.MethodGet, "/"); код != http.StatusOK {
		t.Fatalf("место без стройки не отвечает «я здесь»: %d", код)
	}

	код, тело := послать(t, g, http.MethodPost, BuildPath, `{"schema":"/схема"}`)
	if код != http.StatusServiceUnavailable || тело["error"] != "build-off" {
		t.Fatalf("получено %d %v, ожидалось 503 build-off", код, тело)
	}
	if detail, _ := тело["detail"].(string); !strings.Contains(detail, "WORLD_BUILD_DIR") {
		t.Errorf("в отказе нет выхода: %q", detail)
	}
}

// Граница режима не сдвинулась: сторож принимает РОВНО ОДНО — постройку.
// Всё остальное по-прежнему промах, и отказ теперь называет, что именно он берёт.
func TestСторожПринимаетТолькоПостройку(t *testing.T) {
	g, _ := сторожСМестом(t)

	код, тело := послать(t, g, http.MethodPost, "/api/locations", `{"name":"x","addr":"x:1","gives":"y"}`)

	if код != http.StatusMethodNotAllowed || тело["error"] != "guard-accepts-nothing" {
		t.Fatalf("получено %d %v, ожидалось 405 guard-accepts-nothing", код, тело)
	}
	detail, _ := тело["detail"].(string)
	for _, кусок := range []string{BuildPath, "/api/locations"} {
		if !strings.Contains(detail, кусок) {
			t.Errorf("в отказе нет %q: %q", кусок, detail)
		}
	}
}

// Постройка стоит — сторож её НЕ раздаёт. Это граница, а не недоделка: клон
// лежит на диске места, а кто его поднимает — вопрос следующей ступени.
func TestПоставленнуюПостройкуСторожНеРаздаёт(t *testing.T) {
	схема := schematest.Схема(t, map[string]string{"README.md": "постройка пробы"})
	g, _ := сторожСМестом(t)
	if код, тело := послать(t, g, http.MethodPost, BuildPath, `{"schema":"`+схема+`"}`); код != http.StatusCreated {
		t.Fatalf("стройка: %d %v", код, тело)
	}

	код, тело := ответ(t, g, http.MethodGet, "/README.md")

	if код != http.StatusNotFound || тело["error"] != "nothing-here" {
		t.Errorf("сторож раздал содержимое постройки: %d %v", код, тело)
	}
}
