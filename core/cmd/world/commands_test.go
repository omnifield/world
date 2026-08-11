package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omnifield/world/core/internal/door"
	"github.com/omnifield/world/core/internal/field"
	"github.com/omnifield/world/core/internal/guard"
)

// прогон — одна подкоманда с разведёнными потоками: результат отдельно,
// диагностика отдельно.
func прогон(args ...string) (код int, out, errOut string) {
	о, д := &bytes.Buffer{}, &bytes.Buffer{}
	код = runCommand(args, streams{out: о, err: д})
	return код, о.String(), д.String()
}

// дверьВПамяти поднимает настоящую дверь и отдаёт её адрес — «имя:порт», ровно
// в той форме, в какой его пишут в WORLD_DOOR.
func дверьВПамяти(t *testing.T) string {
	t.Helper()
	reg, err := door.Open(filepath.Join(t.TempDir(), "field", "locations.json"))
	if err != nil {
		t.Fatalf("реестр локаций не поднялся: %v", err)
	}
	молча := func(string, ...any) {}
	h := door.NewHandler(reg, молча, nil, nil)
	mux := http.NewServeMux()
	mux.Handle(door.Prefix, h)
	mux.Handle(door.Prefix+"/", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// сторожВПамяти — живая локация: тот же сторож, что поднимает `world guard`.
func сторожВПамяти(t *testing.T, имя string) string {
	t.Helper()
	srv := httptest.NewServer(guard.New(имя, "проба присутствия", func(string, ...any) {}, nil))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func содержит(t *testing.T, где, что string, куски ...string) {
	t.Helper()
	for _, кусок := range куски {
		if !strings.Contains(что, кусок) {
			t.Errorf("в %s нет %q: %s", где, кусок, что)
		}
	}
}

// Флоу целиком через КОМАНДЫ, а не через клиента: поднял сторожа → join →
// локация в поле и маршрут выдан → leave → её нет (`tasker:WORLD-84`).
func TestКомандыВходаВыходаИСпискаРаботают(t *testing.T) {
	t.Setenv("WORLD_DOOR", дверьВПамяти(t))
	t.Setenv("WORLD_NAME", "probe-loc")
	t.Setenv("WORLD_GIVES", "проба входа в поле")
	t.Setenv("WORLD_SELF_ADDR", сторожВПамяти(t, "probe-loc"))

	код, out, errOut := прогон("join")
	if код != 0 {
		t.Fatalf("join вернул %d: %s", код, errOut)
	}
	содержит(t, "выводе join", out, "probe-loc", "вошла в поле", "/probe-loc/")

	// Повторный вход — подтверждение, а не отказ.
	код, out, errOut = прогон("join")
	if код != 0 {
		t.Fatalf("повторный join вернул %d: %s", код, errOut)
	}
	содержит(t, "выводе повторного join", out, "подтвердила присутствие")

	код, out, _ = прогон("who")
	if код != 0 {
		t.Fatalf("who вернул %d", код)
	}
	содержит(t, "списке поля", out, "в поле 1", "probe-loc", "/probe-loc/", "проба входа в поле")

	код, out, errOut = прогон("leave")
	if код != 0 {
		t.Fatalf("leave вернул %d: %s", код, errOut)
	}
	содержит(t, "выводе leave", out, "снята с поля", "/probe-loc/")

	// Пустое поле — рабочее состояние, а не сбой: дверь стоит до первой локации.
	код, out, _ = прогон("who")
	if код != 0 {
		t.Fatalf("who на пустом поле вернул %d", код)
	}
	содержит(t, "выводе who", out, "в поле пусто", "world join")
}

// Результат читают глазами И скриптом, трассировку — только глазами. Смешаешь
// потоки — испортишь оба: `world who | …` начнёт получать строки журнала.
func TestРезультатИДиагностикаРазведеныПоПотокам(t *testing.T) {
	t.Setenv("WORLD_DOOR", дверьВПамяти(t))

	_, out, errOut := прогон("who")

	if strings.Contains(out, "field:") {
		t.Errorf("трассировка уехала в результат: %s", out)
	}
	содержит(t, "диагностике", errOut, "field:", "code=200")
}

// ── отказы: каждый вызывается нарочно (`kb:WORLD-32`) ────────────────────────

// Обязательный отказ ТЗ: по моему адресу никто не отвечает. Сторожа нарочно не
// поднимаем — ровно так это выглядит у человека.
func TestВходБезПоднятогоСторожаНазываетПричинуИВыход(t *testing.T) {
	t.Setenv("WORLD_DOOR", дверьВПамяти(t))
	t.Setenv("WORLD_NAME", "probe-loc")
	t.Setenv("WORLD_GIVES", "проба")
	// Адрес выводится из имени и порта: probe-loc:8080 — имени такого в сети
	// нет, значит и сторожа по нему нет.
	t.Setenv("WORLD_SELF_ADDR", "")

	код, _, errOut := прогон("join")

	if код != 1 {
		t.Fatalf("код возврата: получено %d, ожидалась 1 — вход не состоялся", код)
	}
	содержит(t, "отказе", errOut, "self-unreachable", "world guard")
}

func TestВходБезНастройкиНазываетПеременнуюИФлаг(t *testing.T) {
	t.Setenv("WORLD_DOOR", дверьВПамяти(t))

	tests := []struct {
		name, имя, даёт string
		куски           []string
	}{
		{"нет имени", "", "проба", []string{"WORLD_NAME", "-name", "маршрут"}},
		{"нет описания", "probe-loc", "", []string{"WORLD_GIVES", "-gives", "безымянной"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("WORLD_NAME", tt.имя)
			t.Setenv("WORLD_GIVES", tt.даёт)

			код, _, errOut := прогон("join")

			if код != 1 {
				t.Fatalf("код возврата: получено %d, ожидалась 1", код)
			}
			содержит(t, "отказе", errOut, tt.куски...)
		})
	}
}

func TestСнятиеБезИмениНазываетПричинуИВыход(t *testing.T) {
	t.Setenv("WORLD_DOOR", дверьВПамяти(t))
	t.Setenv("WORLD_NAME", "")

	код, _, errOut := прогон("leave")

	if код != 1 {
		t.Fatalf("код возврата: получено %d, ожидалась 1", код)
	}
	содержит(t, "отказе", errOut, "WORLD_NAME", "world who")
}

// У двери флагов нет, и лишнее слово рядом с ней — не «ничего страшного»:
// проглоти его молча, и дверь поднимется не так, как просили.
func TestЛишнееСловоУДвериОтвергается(t *testing.T) {
	код, _, errOut := прогон("door", "-listen", ":9999")

	if код != 1 {
		t.Fatalf("код возврата: получено %d, ожидалась 1", код)
	}
	содержит(t, "отказе", errOut, "лишнее слово", "WORLD_ADDR")
}

func TestНеизвестнаяПодкомандаНазываетПричинуИВыход(t *testing.T) {
	код, _, errOut := прогон("присоединиться")

	if код != 1 {
		t.Fatalf("код возврата: получено %d, ожидалась 1", код)
	}
	содержит(t, "отказе", errOut, "join", "guard", "world help")
}

// Позиция значения не имеет: `world join probe-loc` иначе вошло бы в поле под
// именем из переменной среды, а человек назвал другое — и не узнал бы об этом.
func TestЛишнееСловоОтвергается(t *testing.T) {
	код, _, errOut := прогон("who", "probe-loc")

	if код != 1 {
		t.Fatalf("код возврата: получено %d, ожидалась 1", код)
	}
	содержит(t, "отказе", errOut, "лишнее слово", "флагами")
}

// ── настройка ────────────────────────────────────────────────────────────────

// Имя локации — это и её имя в общей сети (`kb:FUND-5`), поэтому свой адрес
// выводится, а не спрашивается лишний раз. Сломай вывод — и join начнёт
// объявлять локацию по адресу, которого у неё нет.
func TestСвойАдресВыводитсяИзИмениИПортаПрослушивания(t *testing.T) {
	tests := []struct{ имя, слушает, ожидалось string }{
		{"probe-loc", ":8080", "probe-loc:8080"},
		{"probe-loc", "0.0.0.0:3500", "probe-loc:3500"},
		{"probe-loc", "127.0.0.1:9", "probe-loc:9"},
	}
	for _, tt := range tests {
		got, err := selfAddr(tt.имя, tt.слушает)
		if err != nil {
			t.Fatalf("selfAddr(%q, %q): %v", tt.имя, tt.слушает, err)
		}
		if got != tt.ожидалось {
			t.Errorf("selfAddr(%q, %q): получено %q, ожидалось %q", tt.имя, tt.слушает, got, tt.ожидалось)
		}
	}
}

func TestАдресБезПортаНеВыводитсяАНазываетВыход(t *testing.T) {
	_, err := selfAddr("probe-loc", "просто-имя")

	if err == nil {
		t.Fatal("адрес без порта принят молча — локация объявилась бы неизвестно где")
	}
	содержит(t, "отказе", err.Error(), "WORLD_SELF_ADDR", "-addr", "имя:порт")
}

// Флаг перекрывает переменную среды — это и есть объявленная форма настройки.
func TestФлагСильнееПеременнойСреды(t *testing.T) {
	t.Setenv("WORLD_DOOR", "переменная:8080")

	_, _, errOut := прогон("who", "-door", "127.0.0.1:1")

	if strings.Contains(errOut, "переменная:8080") {
		t.Errorf("флаг не перекрыл переменную среды: %s", errOut)
	}
	содержит(t, "отказе", errOut, "127.0.0.1:1")
}

// Умолчание адреса двери — контрактное имя из файла запуска мира. Разойдись
// оно с `deploy/compose.yaml`, и локация будет входить в никуда.
func TestУмолчаниеАдресаДвериКонтрактное(t *testing.T) {
	if field.DefaultDoor != "door:8080" {
		t.Errorf("умолчание адреса двери: получено %q, ожидалось %q — это имя объявлено алиасом в deploy/compose.yaml",
			field.DefaultDoor, "door:8080")
	}
}

// `-h` у подкоманды — просьба ПРОЧИТАТЬ, а не сделать. Дверь при этом стоит на
// мёртвом адресе: выполнись команда — и в stderr уехал бы отказ, а код стал бы 1.
func TestПодсказкаПодкомандыНеВыполняетКоманду(t *testing.T) {
	t.Setenv("WORLD_DOOR", свободныйАдрес(t))

	код, _, errOut := прогон("who", "-h")

	if код != 0 {
		t.Fatalf("код возврата: получено %d, ожидался 0 — подсказку показали, отказа не было", код)
	}
	if strings.Contains(errOut, "door-unreachable") {
		t.Errorf("подсказка не остановила команду — она полезла к двери: %s", errOut)
	}
	содержит(t, "подсказке", errOut, "-door", "WORLD_DOOR")
}

// Подсказка обязана показывать ОБА режима одного бинаря: человек, увидевший
// только команды входа, не узнает, чем поднимается сторож.
func TestПодсказкаПоказываетОбаРежима(t *testing.T) {
	код, out, _ := прогон("help")

	if код != 0 {
		t.Fatalf("код возврата: получено %d, ожидался 0", код)
	}
	содержит(t, "подсказке", out,
		"без подкоманды", "world guard", "world join", "world leave", "world who",
		"WORLD_DOOR", "WORLD_SELF_ADDR", "core/README.md")
}
