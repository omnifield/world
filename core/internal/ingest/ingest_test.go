package ingest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// журнал — собиратель строк трассировки. С мьютексом, потому что часть тестов
// шлёт запросы параллельно, и гонка в самом тесте маскировала бы гонку в коде.
type журнал struct {
	mu     sync.Mutex
	строки []string
}

func (ж *журнал) печать(format string, args ...any) {
	ж.mu.Lock()
	defer ж.mu.Unlock()
	ж.строки = append(ж.строки, fmt.Sprintf(format, args...))
}

func (ж *журнал) всё() []string {
	ж.mu.Lock()
	defer ж.mu.Unlock()
	return append([]string(nil), ж.строки...)
}

// поднять даёт приём поверх свежего каталога прогонов.
func поднять(t *testing.T) (*Handler, string, *журнал) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "stand")
	store, err := New(dir)
	if err != nil {
		t.Fatalf("каталог прогонов не поднялся: %v", err)
	}
	ж := &журнал{}
	return NewHandler(store, ж.печать, time.Now), store.Dir(), ж
}

func запрос(t *testing.T, h *Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// отказ разбирает тело ошибки: код и поле обязаны быть названы.
func отказ(t *testing.T, rec *httptest.ResponseRecorder) apiError {
	t.Helper()
	var e apiError
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("тело отказа не разбирается: %v (тело: %s)", err, rec.Body.String())
	}
	return e
}

const событие = `{"session":"run-1","ts":"2026-08-07T18:23:34Z","seq":%d,"attempt":"place baser at 0,0","allowed":true}`

// ── запись и чтение ──────────────────────────────────────────────────────────

func TestДописалПрочиталОбратно(t *testing.T) {
	h, dir, _ := поднять(t)

	for i := 1; i <= 3; i++ {
		if rec := запрос(t, h, http.MethodPost, Prefix+"events", fmt.Sprintf(событие, i)); rec.Code != http.StatusOK {
			t.Fatalf("событие %d: код %d, тело %s", i, rec.Code, rec.Body.String())
		}
	}

	// На диске — ровно три строки в порядке отправки. Проверяем файлом, а не
	// только ручкой: журнал читают и `cat`'ом, когда сервис лежит.
	raw, err := os.ReadFile(filepath.Join(dir, "run-1", eventsFile))
	if err != nil {
		t.Fatalf("журнал не читается: %v", err)
	}
	строки := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(строки) != 3 {
		t.Fatalf("строк в журнале: получено %d, ожидалось 3 (файл: %q)", len(строки), raw)
	}
	for i, s := range строки {
		var d map[string]any
		if err := json.Unmarshal([]byte(s), &d); err != nil {
			t.Fatalf("строка %d не разбирается: %v (%q)", i+1, err, s)
		}
		if d["seq"] != float64(i+1) {
			t.Errorf("порядок нарушен: строка %d несёт seq=%v", i+1, d["seq"])
		}
	}

	rec := запрос(t, h, http.MethodGet, Prefix+"events?session=run-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("чтение: код %d, тело %s", rec.Code, rec.Body.String())
	}
	var ответ struct {
		Session string            `json:"session"`
		Count   int               `json:"count"`
		Events  []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ответ); err != nil {
		t.Fatalf("ответ не разбирается: %v (%s)", err, rec.Body.String())
	}
	if ответ.Count != 3 || len(ответ.Events) != 3 || ответ.Session != "run-1" {
		t.Errorf("прочитано не то: %+v", ответ)
	}
}

func TestПачкаСобытийЛожитсяЦеликомИВПорядке(t *testing.T) {
	h, dir, _ := поднять(t)

	пачка := "[" + fmt.Sprintf(событие, 1) + "," + fmt.Sprintf(событие, 2) + "]"
	rec := запрос(t, h, http.MethodPost, Prefix+"events", пачка)
	if rec.Code != http.StatusOK {
		t.Fatalf("пачка: код %d, тело %s", rec.Code, rec.Body.String())
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "run-1", eventsFile))
	if got := strings.Count(strings.TrimSpace(string(raw)), "\n") + 1; got != 2 {
		t.Errorf("строк в журнале: получено %d, ожидалось 2 (файл: %q)", got, raw)
	}
}

// Пачка либо ложится целиком, либо не ложится совсем: половина пачки вместе
// с отказом — журнал, которому нельзя верить.
func TestДурноеСобытиеВПачкеНеОставляетПолпачки(t *testing.T) {
	h, dir, _ := поднять(t)

	плохое := `{"session":"run-1","ts":"2026-08-07T18:23:34Z","attempt":"без allowed"}`
	rec := запрос(t, h, http.MethodPost, Prefix+"events", "["+fmt.Sprintf(событие, 1)+","+плохое+"]")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("код: получено %d, ожидалось 400 (тело %s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "run-1", eventsFile)); !os.IsNotExist(err) {
		raw, _ := os.ReadFile(filepath.Join(dir, "run-1", eventsFile))
		t.Errorf("отказанная пачка оставила след в журнале: %q", raw)
	}
}

func TestСессииИзолированы(t *testing.T) {
	h, dir, _ := поднять(t)

	первое := `{"session":"run-a","ts":"t","attempt":"a","allowed":true}`
	второе := `{"session":"run-b","ts":"t","attempt":"b","allowed":false,"rule":"no-place","reason":"за пределами поля"}`
	for _, body := range []string{первое, второе, первое} {
		if rec := запрос(t, h, http.MethodPost, Prefix+"events", body); rec.Code != http.StatusOK {
			t.Fatalf("запись: код %d, тело %s", rec.Code, rec.Body.String())
		}
	}

	tests := []struct {
		session string
		строк   int
		вФайле  string
	}{
		{"run-a", 2, `"attempt":"a"`},
		{"run-b", 1, `"attempt":"b"`},
	}
	for _, tt := range tests {
		t.Run(tt.session, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, tt.session, eventsFile))
			if err != nil {
				t.Fatalf("журнал %s не читается: %v", tt.session, err)
			}
			if got := len(strings.Split(strings.TrimSpace(string(raw)), "\n")); got != tt.строк {
				t.Errorf("строк: получено %d, ожидалось %d (%q)", got, tt.строк, raw)
			}
			if !strings.Contains(string(raw), tt.вФайле) {
				t.Errorf("в журнал %s попало чужое: %q", tt.session, raw)
			}
		})
	}
}

// Стенд имеет право слать больше, чем мы описали. Если незнакомое поле
// пропадает — журнал врёт молча, и это худший вид потери.
func TestНезнакомыеПоляПереживаютЗаписьИЧтение(t *testing.T) {
	h, dir, _ := поднять(t)

	body := `{"session":"run-1","ts":"2026-08-07T18:23:34Z","seq":7,"attempt":"place baser at 0,0",
	          "allowed":false,"rule":"no-place","reason":"за пределами поля (клетка -24,-1).",
	          "камера":{"pos":[1,2,3],"fov":60},"нечто":null,"вложенное":{"глубоко":{"да":true}}}`
	if rec := запрос(t, h, http.MethodPost, Prefix+"events", body); rec.Code != http.StatusOK {
		t.Fatalf("запись: код %d, тело %s", rec.Code, rec.Body.String())
	}

	raw, err := os.ReadFile(filepath.Join(dir, "run-1", eventsFile))
	if err != nil {
		t.Fatalf("журнал не читается: %v", err)
	}
	if строк := len(strings.Split(strings.TrimSpace(string(raw)), "\n")); строк != 1 {
		t.Fatalf("многострочное тело обязано лечь ОДНОЙ строкой, получено %d (%q)", строк, raw)
	}

	var d map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &d); err != nil {
		t.Fatalf("строка не разбирается: %v", err)
	}
	for _, поле := range []string{"камера", "нечто", "вложенное", "rule", "reason"} {
		if _, ok := d[поле]; !ok {
			t.Errorf("поле %q не пережило запись (строка: %s)", поле, raw)
		}
	}
	камера, ok := d["камера"].(map[string]any)
	if !ok || камера["fov"] != float64(60) {
		t.Errorf("вложенное значение исказилось: %v", d["камера"])
	}

	rec := запрос(t, h, http.MethodGet, Prefix+"events?session=run-1", "")
	if !strings.Contains(rec.Body.String(), `"камера"`) {
		t.Errorf("чтение потеряло незнакомое поле: %s", rec.Body.String())
	}
}

func TestSinceОтдаётТолькоНовыеСобытия(t *testing.T) {
	h, _, _ := поднять(t)
	for i := 1; i <= 4; i++ {
		запрос(t, h, http.MethodPost, Prefix+"events", fmt.Sprintf(событие, i))
	}

	tests := []struct {
		since string
		count int
	}{
		{"", 4},
		{"0", 4},
		{"2", 2},
		{"4", 0},
		{"99", 0},
	}
	for _, tt := range tests {
		t.Run("since="+tt.since, func(t *testing.T) {
			rec := запрос(t, h, http.MethodGet, Prefix+"events?session=run-1&since="+tt.since, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("код %d, тело %s", rec.Code, rec.Body.String())
			}
			var ответ struct {
				Count int `json:"count"`
			}
			json.Unmarshal(rec.Body.Bytes(), &ответ)
			if ответ.Count != tt.count {
				t.Errorf("событий: получено %d, ожидалось %d", ответ.Count, tt.count)
			}
		})
	}
}

// ── манифест и снимок ────────────────────────────────────────────────────────

func TestМанифестИСписокПрогонов(t *testing.T) {
	h, dir, _ := поднять(t)

	манифест := `{"session":"run-2026-08-07-01","started":"2026-08-07T18:23:30Z","unity":"6000.4.10f1",` +
		`"build":"editor","scenario":"manual exploration","stubs":["roles — not modelled"]}`
	if rec := запрос(t, h, http.MethodPost, Prefix+"runs", манифест); rec.Code != http.StatusOK {
		t.Fatalf("манифест: код %d, тело %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "run-2026-08-07-01", runFile)); err != nil {
		t.Fatalf("манифест не лёг на диск: %v", err)
	}

	rec := запрос(t, h, http.MethodGet, Prefix+"runs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("список: код %d, тело %s", rec.Code, rec.Body.String())
	}
	var ответ struct {
		Count int `json:"count"`
		Runs  []struct {
			Session string   `json:"session"`
			Stubs   []string `json:"stubs"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ответ); err != nil {
		t.Fatalf("список не разбирается: %v (%s)", err, rec.Body.String())
	}
	if ответ.Count != 1 || ответ.Runs[0].Session != "run-2026-08-07-01" || len(ответ.Runs[0].Stubs) != 1 {
		t.Errorf("список прогонов: получено %+v", ответ)
	}
}

func TestСнимокПерезаписываетсяЦеликом(t *testing.T) {
	h, _, _ := поднять(t)

	for _, world := range []string{`{"tick":1}`, `{"tick":2,"buildings":[{"schema":"baser"}]}`} {
		if rec := запрос(t, h, http.MethodPost, Prefix+"state", `{"session":"run-1","world":`+world+`}`); rec.Code != http.StatusOK {
			t.Fatalf("снимок: код %d, тело %s", rec.Code, rec.Body.String())
		}
	}

	rec := запрос(t, h, http.MethodGet, Prefix+"state?session=run-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("чтение снимка: код %d, тело %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"tick":1`) || !strings.Contains(rec.Body.String(), `"tick":2`) {
		t.Errorf("побеждать должен последний снимок, получено %s", rec.Body.String())
	}
}

func TestНетСессииНаДискеЭто404(t *testing.T) {
	h, _, _ := поднять(t)

	tests := []string{Prefix + "events?session=нет-такой", Prefix + "state?session=run-1"}
	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			rec := запрос(t, h, http.MethodGet, path, "")
			if rec.Code == http.StatusNotFound {
				return
			}
			// имя с кириллицей отбивается раньше — тоже законный отказ, но 400
			if rec.Code != http.StatusBadRequest {
				t.Errorf("код: получено %d, ожидалось 404 либо 400 (тело %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// ── отказы формы ─────────────────────────────────────────────────────────────

// Имя сессии идёт в путь на диске. Проверка одна на все ручки, поэтому и
// таблица одна: пробитая защита здесь — запись куда угодно по файловой системе.
func TestДурноеИмяСессииОтбито(t *testing.T) {
	h, dir, _ := поднять(t)

	tests := []struct {
		name    string
		session string
		код     string
	}{
		{"вверх по дереву", "../evil", "session-invalid"},
		{"вверх в середине", "run/../../evil", "session-invalid"},
		{"абсолютный путь", "/etc/passwd", "session-invalid"},
		{"слэш внутри", "run-1/sub", "session-invalid"},
		{"обратный слэш", `run\..\evil`, "session-invalid"},
		{"верхний регистр", "RUN-1", "session-invalid"},
		{"кириллица", "прогон-1", "session-invalid"},
		{"пробел", "run 1", "session-invalid"},
		{"нулевой байт", "run\x001", "session-invalid"},
		{"начинается с точки", ".hidden", "session-invalid"},
		{"начинается с дефиса", "-run", "session-invalid"},
		{"пусто", "", "session-missing"},
		{"длиннее 64", strings.Repeat("a", 65), "session-invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{
				"session": tt.session, "ts": "t", "attempt": "a", "allowed": true,
			})
			rec := запрос(t, h, http.MethodPost, Prefix+"events", string(body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("код: получено %d, ожидалось 400 (тело %s)", rec.Code, rec.Body.String())
			}
			e := отказ(t, rec)
			if e.Code != tt.код {
				t.Errorf("код отказа: получено %q, ожидалось %q", e.Code, tt.код)
			}
			if !strings.Contains(e.Detail, "session") && !strings.Contains(e.Detail, "сесси") {
				t.Errorf("отказ не называет поле: %q", e.Detail)
			}
		})
	}

	// И контрольный вопрос ко всей таблице: за каталогом прогонов не появилось
	// ничего. Отказ, не пустивший запись, но создавший каталог, — не отказ.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("каталог прогонов не читается: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("отбитые имена оставили следы на диске: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil")); !os.IsNotExist(err) {
		t.Errorf("запись вышла за каталог прогонов: %v", err)
	}
}

func TestНетОбязательногоПоляОтказСПричиной(t *testing.T) {
	h, _, _ := поднять(t)

	tests := []struct {
		name    string
		path    string
		body    string
		код     string
		вДетали string
	}{
		{"событие без ts", Prefix + "events",
			`{"session":"run-1","attempt":"a","allowed":true}`, "field-missing", "ts"},
		{"событие без attempt", Prefix + "events",
			`{"session":"run-1","ts":"t","allowed":true}`, "field-missing", "attempt"},
		{"событие без allowed", Prefix + "events",
			`{"session":"run-1","ts":"t","attempt":"a"}`, "field-missing", "allowed"},
		{"allowed не булево", Prefix + "events",
			`{"session":"run-1","ts":"t","attempt":"a","allowed":"да"}`, "field-invalid", "allowed"},
		{"событие без session", Prefix + "events",
			`{"ts":"t","attempt":"a","allowed":true}`, "session-missing", "session"},
		{"манифест без session", Prefix + "runs",
			`{"started":"2026-08-07T18:23:30Z"}`, "session-missing", "session"},
		{"снимок без world", Prefix + "state",
			`{"session":"run-1"}`, "field-missing", "world"},
		{"тело не объект", Prefix + "runs", `"строка"`, "body-not-object", "объект"},
		{"тело не JSON", Prefix + "runs", `{сломано}`, "json-invalid", "JSON"},
		{"пустая пачка", Prefix + "events", `[]`, "events-empty", "пуст"},
		{"две сессии в пачке", Prefix + "events",
			`[{"session":"run-1","ts":"t","attempt":"a","allowed":true},` +
				`{"session":"run-2","ts":"t","attempt":"b","allowed":true}]`, "session-mixed", "run-2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := запрос(t, h, http.MethodPost, tt.path, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("код: получено %d, ожидалось 400 (тело %s)", rec.Code, rec.Body.String())
			}
			e := отказ(t, rec)
			if e.Code != tt.код {
				t.Errorf("код отказа: получено %q, ожидалось %q", e.Code, tt.код)
			}
			if !strings.Contains(e.Detail, tt.вДетали) {
				t.Errorf("отказ не называет причину: получено %q, ожидалась подстрока %q", e.Detail, tt.вДетали)
			}
		})
	}
}

func TestСлишкомБольшоеТелоОтбитоПоРазмеру(t *testing.T) {
	h, _, _ := поднять(t)

	body := `{"session":"run-1","ts":"t","allowed":true,"attempt":"` + strings.Repeat("ы", MaxBody) + `"}`
	rec := запрос(t, h, http.MethodPost, Prefix+"events", body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("код: получено %d, ожидалось %d (тело %s)", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	if e := отказ(t, rec); e.Code != "body-too-large" {
		t.Errorf("код отказа: получено %q, ожидалось body-too-large", e.Code)
	}
}

func TestНеизвестнаяРучкаОтвечаетТемЖеФорматом(t *testing.T) {
	h, _, _ := поднять(t)

	rec := запрос(t, h, http.MethodGet, Prefix+"нет-такого", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("код: получено %d, ожидалось 404", rec.Code)
	}
	if e := отказ(t, rec); e.Code != "unknown-endpoint" {
		t.Errorf("код отказа: получено %q, ожидалось unknown-endpoint", e.Code)
	}
}

// Правило мира НЕ наше дело: сервис пишет, судит проверялка. Событие с
// неизвестным правилом обязано лечь на диск как есть.
func TestПравилоНеПроверяетсяНаПринадлежностьСписку(t *testing.T) {
	h, dir, _ := поднять(t)

	body := `{"session":"run-1","ts":"t","attempt":"a","allowed":false,"rule":"такого-правила-нет","reason":"почему-то"}`
	if rec := запрос(t, h, http.MethodPost, Prefix+"events", body); rec.Code != http.StatusOK {
		t.Fatalf("сервис взялся судить правило: код %d, тело %s", rec.Code, rec.Body.String())
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "run-1", eventsFile))
	if !strings.Contains(string(raw), "такого-правила-нет") {
		t.Errorf("правило не доехало до диска: %q", raw)
	}
}

// ── одновременность ──────────────────────────────────────────────────────────

func TestПараллельнаяЗаписьНеРвётСтроки(t *testing.T) {
	h, dir, _ := поднять(t)

	const писателей, накаждого = 8, 25
	var wg sync.WaitGroup
	for w := 0; w < писателей; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < накаждого; i++ {
				body := fmt.Sprintf(
					`{"session":"run-1","ts":"t","seq":%d,"attempt":"писатель %d, попытка %d","allowed":true,"хвост":"%s"}`,
					w*накаждого+i+1, w, i, strings.Repeat("х", 200))
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, Prefix+"events", strings.NewReader(body)))
				if rec.Code != http.StatusOK {
					t.Errorf("писатель %d: код %d, тело %s", w, rec.Code, rec.Body.String())
				}
			}
		}(w)
	}
	wg.Wait()

	raw, err := os.ReadFile(filepath.Join(dir, "run-1", eventsFile))
	if err != nil {
		t.Fatalf("журнал не читается: %v", err)
	}
	строки := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(строки) != писателей*накаждого {
		t.Fatalf("строк: получено %d, ожидалось %d", len(строки), писателей*накаждого)
	}
	видели := map[float64]bool{}
	for i, s := range строки {
		var d map[string]any
		if err := json.Unmarshal([]byte(s), &d); err != nil {
			t.Fatalf("строка %d порвана или смешана с чужой: %v (%q)", i+1, err, s)
		}
		seq, _ := d["seq"].(float64)
		if видели[seq] {
			t.Errorf("seq %v встретился дважды — строка задвоилась", seq)
		}
		видели[seq] = true
	}
}

// ── трассировка и CORS ───────────────────────────────────────────────────────

func TestТрассировкаНазываетСессиюИКод(t *testing.T) {
	h, _, ж := поднять(t)

	запрос(t, h, http.MethodPost, Prefix+"events", fmt.Sprintf(событие, 1))
	запрос(t, h, http.MethodGet, Prefix+"state?session=run-1", "")

	строки := ж.всё()
	if len(строки) != 2 {
		t.Fatalf("строк трассировки: получено %d, ожидалось 2 (%v)", len(строки), строки)
	}
	for _, кусок := range []string{"POST", Prefix + "events", "session=run-1", "code=200", "bytes=", "dur="} {
		if !strings.Contains(строки[0], кусок) {
			t.Errorf("в строке трассировки нет %q: %q", кусок, строки[0])
		}
	}
	if !strings.Contains(строки[1], "code=404") {
		t.Errorf("отказ не попал в трассировку: %q", строки[1])
	}
}

func TestCORSОтвечаетНаPreflight(t *testing.T) {
	h, _, _ := поднять(t)

	rec := запрос(t, h, http.MethodOptions, Prefix+"events", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight: получено %d, ожидалось %d", rec.Code, http.StatusNoContent)
	}
	tests := map[string]string{
		"Access-Control-Allow-Origin":  "*",
		"Access-Control-Allow-Methods": "GET, POST, OPTIONS",
	}
	for заголовок, want := range tests {
		if got := rec.Header().Get(заголовок); got != want {
			t.Errorf("%s: получено %q, ожидалось %q", заголовок, got, want)
		}
	}

	// И на рабочем ответе тоже — иначе браузер отбросит уже полученное тело.
	rec = запрос(t, h, http.MethodPost, Prefix+"events", fmt.Sprintf(событие, 1))
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("на рабочем ответе нет CORS: %q", got)
	}
}

// ── каталог прогонов ─────────────────────────────────────────────────────────

func TestNewНазываетПричинуОтказа(t *testing.T) {
	файл := filepath.Join(t.TempDir(), "не-каталог")
	if err := os.WriteFile(файл, []byte("x"), 0o644); err != nil {
		t.Fatalf("подготовка не удалась: %v", err)
	}

	if _, err := New(файл); err == nil {
		t.Fatalf("файл принят за каталог прогонов")
	} else if !strings.Contains(err.Error(), "каталог прогонов") {
		t.Errorf("ошибка не называет причину: %v", err)
	}
}
