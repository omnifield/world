package build

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omnifield/world/core/internal/schematest"
)

func fixedNow() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) }

func молча(string, ...any) {}

// место — пустое место под стройку: каталог, в который ещё ничего не приехало.
func место(t *testing.T) *Site {
	t.Helper()
	return Open(filepath.Join(t.TempDir(), "стройка"), молча, fixedNow)
}

// поставить — стройка, которая ОБЯЗАНА пройти. Отказ здесь валит пробу с его
// собственными словами: они точнее любого «не получилось».
func поставить(t *testing.T, s *Site, схема string, снос bool) (*Build, Outcome) {
	t.Helper()
	b, исход, отказ := s.Raise(схема, снос)
	if отказ != nil {
		t.Fatalf("стройка отказала: %s — %s", отказ.Code, отказ.Detail)
	}
	return b, исход
}

func отказано(t *testing.T, r *Refusal, код string, статус int, куски ...string) {
	t.Helper()
	if r == nil {
		t.Fatalf("отказа не было, а его вызывали нарочно (ожидался %s)", код)
	}
	if r.Code != код {
		t.Fatalf("код отказа: получено %q, ожидалось %q (%s)", r.Code, код, r.Detail)
	}
	if r.Status != статус {
		t.Errorf("код HTTP у отказа %s: получено %d, ожидалось %d", код, r.Status, статус)
	}
	// Отказ без выхода — диагноз, по которому непонятно, что делать
	// (`kb:WORLD-31`). Поэтому в каждой пробе проверяется не только причина.
	for _, кусок := range куски {
		if !strings.Contains(r.Detail, кусок) {
			t.Errorf("в отказе %s нет %q: %s", код, кусок, r.Detail)
		}
	}
}

// ГЛАВНОЕ, ради чего пакет есть: схема приезжает в место и становится
// постройкой. Проверяется не «ответ ок», а земля: клон на диске, файл схемы в
// нём, коммит в паспорте — тот самый, что стоит в схеме.
func TestСхемаПриезжаетИСтановитсяПостройкой(t *testing.T) {
	схема := schematest.Схема(t, map[string]string{
		"README.md":      "постройка пробы",
		"cmd/main.go":    "package main\n",
		"вложенный/файл": "и он тоже приехал",
	})
	s := место(t)

	b, исход := поставить(t, s, схема, false)

	if исход != OutcomeBuilt {
		t.Errorf("исход: получено %q, ожидалось %q", исход, OutcomeBuilt)
	}
	if b.Schema != схема {
		t.Errorf("схема в паспорте: получено %q, ожидалось %q", b.Schema, схема)
	}
	if b.Commit != schematest.Коммит(t, схема) {
		t.Errorf("коммит в паспорте: получено %q, ожидалось %q — «постройка встала» подтверждается именно им",
			b.Commit, schematest.Коммит(t, схема))
	}
	if b.Since != "2026-08-12T12:00:00Z" {
		t.Errorf("время стройки: получено %q", b.Since)
	}

	// Куда встало — не запись в ответе, а каталог на диске: содержимое схемы
	// обязано быть в нём целиком, включая вложенное.
	for _, файл := range []string{"README.md", "cmd/main.go", "вложенный/файл"} {
		if _, err := os.Stat(filepath.Join(b.Tree, файл)); err != nil {
			t.Errorf("файла %q нет в постройке: %v", файл, err)
		}
	}
	// Клон — это репозиторий, а не выгрузка файлов: схема остаётся схемой, и по
	// ней видно, откуда постройка приехала.
	if _, err := os.Stat(filepath.Join(b.Tree, ".git")); err != nil {
		t.Errorf("постройка не репозиторий: %v — форма доставки задаёт ступень (kb:WORLD-27)", err)
	}
}

// Место говорит, что на нём стоит, — и говорит это ПОСЛЕ перезапуска: паспорт
// лежит на диске места, а не в памяти процесса.
func TestМестоГоворитЧтоНаНёмСтоитИПослеПерезапуска(t *testing.T) {
	схема := schematest.Схема(t, map[string]string{"README.md": "x"})
	каталог := filepath.Join(t.TempDir(), "стройка")

	s := Open(каталог, молча, fixedNow)
	пусто, отказ := s.Standing()
	if отказ != nil {
		t.Fatalf("пустое место отказало: %s", отказ.Detail)
	}
	if пусто != nil {
		t.Fatalf("на пустом месте что-то стоит: %+v", пусто)
	}

	b, _ := поставить(t, s, схема, false)

	// Другой процесс, тот же каталог — постройка на месте.
	другой := Open(каталог, молча, fixedNow)
	стоит, отказ := другой.Standing()
	if отказ != nil {
		t.Fatalf("после перезапуска место отказало: %s", отказ.Detail)
	}
	if стоит == nil {
		t.Fatal("после перезапуска место говорит «пусто», хотя постройка на диске лежит")
	}
	if *стоит != *b {
		t.Errorf("паспорт разошёлся: получено %+v, ожидалось %+v", *стоит, *b)
	}
}

// ПОВТОР ТОЙ ЖЕ СХЕМЫ НАЗВАН СВОИМ ИСХОДОМ. Не тихая перезапись: место не
// трогает то, что уже стоит, — и это проверяется меткой, оставленной в клоне.
func TestПовторТойЖеСхемыПодтверждаетИНеТрогаетСтоящее(t *testing.T) {
	схема := schematest.Схема(t, map[string]string{"README.md": "x"})
	s := место(t)

	первая, _ := поставить(t, s, схема, false)

	метка := filepath.Join(первая.Tree, "наработано-внутри.txt")
	if err := os.WriteFile(метка, []byte("это сделал юзер в постройке"), 0o644); err != nil {
		t.Fatalf("метка не записана: %v", err)
	}

	вторая, исход := поставить(t, s, схема, false)

	if исход != OutcomeConfirmed {
		t.Errorf("исход повтора: получено %q, ожидалось %q", исход, OutcomeConfirmed)
	}
	if вторая.Since != первая.Since {
		t.Errorf("подтверждение сдвинуло время стройки: было %q, стало %q — это не новая стройка", первая.Since, вторая.Since)
	}
	if _, err := os.Stat(метка); err != nil {
		t.Errorf("повтор затёр наработанное в постройке: %v — тихой перезаписи здесь быть не должно", err)
	}
}

// ДРУГАЯ СХЕМА БЕЗ ЯВНОГО СНОСА — отказ с причиной и выходом. Стоящая при этом
// остаётся: отказ не вправе ничего потерять по дороге.
func TestДругаяСхемаБезСносаОтказываетИНичегоНеТеряет(t *testing.T) {
	перваяСхема := schematest.Схема(t, map[string]string{"первая.txt": "1"})
	втораяСхема := schematest.Схема(t, map[string]string{"вторая.txt": "2"})
	s := место(t)

	стояла, _ := поставить(t, s, перваяСхема, false)

	_, _, отказ := s.Raise(втораяСхема, false)
	отказано(t, отказ, "build-present", http.StatusConflict, "-replace", "tasker:WORLD-43", перваяСхема)

	if _, err := os.Stat(filepath.Join(стояла.Tree, "первая.txt")); err != nil {
		t.Errorf("после отказа прежняя постройка пострадала: %v", err)
	}
	стоит, _ := s.Standing()
	if стоит == nil || стоит.Schema != перваяСхема {
		t.Errorf("после отказа на месте стоит не прежняя постройка: %+v", стоит)
	}
}

// Снос по ЯВНОЙ просьбе — третий исход. Прежняя уходит целиком, и это сказано
// словом «replaced», а не выдано за обычную стройку.
func TestЯвныйСносЗаменяетПостройкуИНазываетЭто(t *testing.T) {
	перваяСхема := schematest.Схема(t, map[string]string{"первая.txt": "1"})
	втораяСхема := schematest.Схема(t, map[string]string{"вторая.txt": "2"})
	s := место(t)

	стояла, _ := поставить(t, s, перваяСхема, false)
	встала, исход := поставить(t, s, втораяСхема, true)

	if исход != OutcomeReplaced {
		t.Errorf("исход замены: получено %q, ожидалось %q", исход, OutcomeReplaced)
	}
	if встала.Schema != втораяСхема {
		t.Errorf("схема после замены: получено %q, ожидалось %q", встала.Schema, втораяСхема)
	}
	if _, err := os.Stat(filepath.Join(стояла.Tree, "первая.txt")); err == nil {
		t.Error("прежняя постройка осталась на месте после замены — снос обязан быть сносом")
	}
	if _, err := os.Stat(filepath.Join(встала.Tree, "вторая.txt")); err != nil {
		t.Errorf("новая постройка не встала: %v", err)
	}
	// Ничего постороннего в каталоге места не остаётся: времянка клона и
	// снесённое убираются, иначе диск места забьётся молча.
	осталось, err := os.ReadDir(filepath.Dir(встала.Tree))
	if err != nil {
		t.Fatalf("каталог стройки не читается: %v", err)
	}
	for _, e := range осталось {
		if e.Name() != деревоИмя && e.Name() != паспортИмя {
			t.Errorf("в каталоге стройки остался мусор: %q", e.Name())
		}
	}
}

// СХЕМЫ НЕТ — отказ обязан назвать словами git'а, а не «не получилось». И место
// остаётся пустым: неудавшаяся стройка ничего на нём не оставляет.
func TestСхемыНетОтказНазываетПричинуИВыходАМестоОстаётсяПустым(t *testing.T) {
	schematest.Требуется(t)
	s := место(t)
	несуществующая := filepath.Join(t.TempDir(), "такой-схемы-нет")

	_, _, отказ := s.Raise(несуществующая, false)
	отказано(t, отказ, "schema-unreachable", http.StatusBadGateway, "git ls-remote", "осталось таким, каким было", несуществующая)

	стоит, о := s.Standing()
	if о != nil {
		t.Fatalf("после неудачи место отказало: %s", о.Detail)
	}
	if стоит != nil {
		t.Errorf("после неудавшейся стройки на месте что-то стоит: %+v", стоит)
	}
	осталось, _ := os.ReadDir(s.Dir())
	if len(осталось) != 0 {
		t.Errorf("после неудавшейся стройки в каталоге места остались файлы: %v", осталось)
	}
}

// Схема без единого коммита: клон проходит, а измерять нечего. Пишем пусто, а
// не правдоподобное (`kb:WORLD-32`).
func TestСхемаБезКоммитовВстаётНоКоммитНеВыдумывается(t *testing.T) {
	s := место(t)

	b, исход := поставить(t, s, schematest.Пустая(t), false)

	if исход != OutcomeBuilt {
		t.Errorf("исход: получено %q, ожидалось %q", исход, OutcomeBuilt)
	}
	if b.Commit != "" {
		t.Errorf("коммит у схемы без коммитов: получено %q, ожидалась пустота", b.Commit)
	}
}

// Форма адреса схемы. Каждый отказ называет, что берётся, — иначе человек
// правит адрес наугад.
func TestАдресСхемыПроверяетсяДоВсякогоКлона(t *testing.T) {
	s := место(t)

	t.Run("адреса нет вовсе", func(t *testing.T) {
		_, _, отказ := s.Raise("   ", false)
		отказано(t, отказ, "field-missing", http.StatusBadRequest, "WORLD_SCHEMA", "kb:WORLD-27")
	})

	t.Run("адрес начинается с дефиса — git прочёл бы его как опцию", func(t *testing.T) {
		_, _, отказ := s.Raise("--upload-pack=payload", false)
		отказано(t, отказ, "schema-invalid", http.StatusBadRequest, "опцию")
	})

	t.Run("относительный путь не берём", func(t *testing.T) {
		_, _, отказ := s.Raise("../соседний-каталог", false)
		отказано(t, отказ, "schema-invalid", http.StatusBadRequest, "ОТ МЕСТА")
	})

	t.Run("перевод строки в адресе", func(t *testing.T) {
		_, _, отказ := s.Raise("https://пример/схема\nи ещё строка", false)
		отказано(t, отказ, "schema-invalid", http.StatusBadRequest, "одной строкой")
	})

	t.Run("адрес длиннее потолка", func(t *testing.T) {
		_, _, отказ := s.Raise("https://"+strings.Repeat("а", MaxSchemaLen), false)
		отказано(t, отказ, "schema-invalid", http.StatusBadRequest, "адрес репозитория")
	})

	t.Run("живые формы адреса проходят проверку", func(t *testing.T) {
		for _, адрес := range []string{
			"https://github.com/omnifield/world.git",
			"http://пример/схема.git",
			"ssh://git@github.com/omnifield/world.git",
			"git://пример/схема.git",
			"file:///схемы/постройка",
			"git@github.com:omnifield/world.git",
			"/схемы/постройка",
		} {
			if r := validSchema(адрес); r != nil {
				t.Errorf("адрес %q отвергнут: %s", адрес, r.Detail)
			}
		}
	})
}

// Каталога стройки нет и быть не может — стройка отказывает, а МЕСТО СТОИТ.
// Скоуп не вправе зависеть от того, идёт ли в нём стройка (`kb:WORLD-54`).
func TestНепишущийсяКаталогОстанавливаетСтройкуАНеМесто(t *testing.T) {
	файл := filepath.Join(t.TempDir(), "не-каталог")
	if err := os.WriteFile(файл, []byte("я файл, а не каталог"), 0o644); err != nil {
		t.Fatalf("файл не записан: %v", err)
	}

	s := Open(файл, молча, fixedNow)

	_, _, отказ := s.Raise("https://пример/схема.git", false)
	отказано(t, отказ, "build-dir-unusable", http.StatusServiceUnavailable, "WORLD_BUILD_DIR", "kb:WORLD-54")
}

// Внутри места нет git — отказ называет ПРИЧИНУ и того, кто её чинит. Это
// единственная проба, где инструмент стройки отбирают нарочно.
func TestБезGitВнутриМестаОтказНазываетЗонуDeploy(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // в этом PATH нет ничего, в том числе git
	s := место(t)

	_, _, отказ := s.Raise("https://пример/схема.git", false)
	отказано(t, отказ, "git-missing", http.StatusServiceUnavailable, "tasker:WORLD-95", "kb:WORLD-27")
}

// Паспорт есть, а клона нет — место НЕ говорит «пусто»: пустое место паспорта
// не имеет, и спрятать пропажу значит соврать. Выход, названный в отказе,
// обязан работать: стройка после него проходит.
func TestПропавшаяПостройкаНазванаАНеСпрятана(t *testing.T) {
	схема := schematest.Схема(t, map[string]string{"README.md": "x"})
	s := место(t)
	b, _ := поставить(t, s, схема, false)

	if err := os.RemoveAll(b.Tree); err != nil {
		t.Fatalf("клон не удалён: %v", err)
	}

	_, отказ := s.Standing()
	отказано(t, отказ, "build-lost", http.StatusInternalServerError, "world build", "тома под стройку")

	_, исход := поставить(t, s, схема, false)
	if исход != OutcomeBuilt {
		t.Errorf("исход стройки на месте пропавшей: получено %q, ожидалось %q", исход, OutcomeBuilt)
	}
}

// Битый паспорт — тоже отказ с выходом, а не молчаливое «пусто».
func TestБитыйПаспортНазванИНеЧитаетсяКакПустоеМесто(t *testing.T) {
	s := место(t)
	if _, _, отказ := s.Raise(schematest.Схема(t, map[string]string{"a": "b"}), false); отказ != nil {
		t.Fatalf("стройка отказала: %s", отказ.Detail)
	}
	if err := os.WriteFile(s.паспорт(), []byte("{это не json"), 0o644); err != nil {
		t.Fatalf("паспорт не испорчен: %v", err)
	}

	_, отказ := s.Standing()
	отказано(t, отказ, "passport-invalid", http.StatusInternalServerError, "world build")
}

// Стройка оставляет след в журнале: место стоит на другой машине, и без строки
// непонятно, что именно на него приехало и сколько это заняло.
func TestСтройкаОставляетСтрокуВЖурнале(t *testing.T) {
	схема := schematest.Схема(t, map[string]string{"README.md": "x"})
	другая := schematest.Схема(t, map[string]string{"иное.txt": "y"})

	var строки []string
	s := Open(filepath.Join(t.TempDir(), "стройка"), func(f string, a ...any) {
		строки = append(строки, fmt.Sprintf(f, a...))
	}, fixedNow)

	поставить(t, s, схема, false)
	поставить(t, s, другая, true)

	соединённые := strings.Join(строки, "\n")
	for _, кусок := range []string{"постройка built", "commit=", "постройка ЗАМЕНЕНА", "снесена схема " + схема} {
		if !strings.Contains(соединённые, кусок) {
			t.Errorf("в журнале нет %q:\n%s", кусок, соединённые)
		}
	}
}

// Остаток оборванной стройки не имеет права ломать следующую: `git clone` в
// непустой каталог отказывает, и место, однажды пережившее рестарт посреди
// клона, иначе перестало бы строиться совсем — молча и навсегда.
func TestОстатокПрошлойНеудачиНеЛомаетСледующуюСтройку(t *testing.T) {
	схема := schematest.Схема(t, map[string]string{"README.md": "x"})
	s := место(t)

	остаток := filepath.Join(s.Dir(), времянка)
	if err := os.MkdirAll(остаток, 0o755); err != nil {
		t.Fatalf("остаток не создан: %v", err)
	}
	if err := os.WriteFile(filepath.Join(остаток, "полклона"), []byte("оборвалось"), 0o644); err != nil {
		t.Fatalf("остаток не заполнен: %v", err)
	}

	b, исход := поставить(t, s, схема, false)

	if исход != OutcomeBuilt {
		t.Errorf("исход: получено %q, ожидалось %q", исход, OutcomeBuilt)
	}
	if _, err := os.Stat(filepath.Join(b.Tree, "README.md")); err != nil {
		t.Errorf("постройка не встала поверх остатка: %v", err)
	}
}
