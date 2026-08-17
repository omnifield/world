package resource

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/omnifield/world/control/internal/run"
)

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ЧТО СТЕРЕЖЁТСЯ ЗДЕСЬ (`WORLD2-130`): контроллер поднимает вещи СВОЕЙ сборкой, и в     │
// │ журнале при КАЖДОМ подъёме сказано, чем поднято.                                     │
// │                                                                                      │
// │ Выпуск возит тройку одной сборки под одним `sha-<7>`; подъём эту гарантию выбрасывал  │
// │ — рецепты по умолчанию говорят `latest`, и контроллер одной сборки ставил юзеру дверь │
// │ другой. Молча.                                                                        │
// └─────────────────────────────────────────────────────────────────────────────────────┘

// рецептОтвечает — подставной компоуз: рецепт называет один образ, и он МЕНЯЕТСЯ, если
// подстановка названа. Это и есть то, что контроллер измеряет вместо догадки.
func рецептОтвечает(умолчание string) func(run.Command) (run.Result, error) {
	return func(c run.Command) (run.Result, error) {
		if !есть(c.Args, "compose") {
			return докерОтвечает(c)
		}
		for _, e := range c.Env {
			for _, name := range ImageVars {
				if strings.HasPrefix(e, name+"=") {
					return run.Result{Out: strings.TrimPrefix(e, name+"=") + "\n"}, nil
				}
			}
		}
		return run.Result{Out: умолчание + "\n"}, nil
	}
}

// журнал — куда подъём говорит, чем поднято. Подменяем его, потому что проверяем ИМЕННО
// эти строки: молчащий подъём и есть дефект, ради которого узел заведён.
func журнал(m *Manager) *[]string {
	var строки []string
	m.Logf = func(f string, a ...any) { строки = append(строки, fmt.Sprintf(f, a...)) }
	return &строки
}

func TestКонтроллерСтавитВещиСвоейСборкой(t *testing.T) {
	m, fake, _ := стенд(t, рецептОтвечает("ghcr.io/omnifield/world:latest"))
	m.Version = "sha-abc1234"
	строки := журнал(m)

	if ref := m.Raise(context.Background(), "vps", "world@10.8.0.5", рецептДвери, nil); ref != nil {
		t.Fatal(ref.Why)
	}

	// Пин уехал подъёму подстановкой рецепта — той самой, что у соседа уже есть.
	var env []string
	for i := range fake.Calls() {
		if strings.Contains(fake.Line(i), "remote.sh add") {
			env = fake.Calls()[i].Env
		}
	}
	if !есть(env, "WORLD_IMAGE=ghcr.io/omnifield/world:sha-abc1234") {
		t.Fatalf("вещь поднимается не своей сборкой: %v", env)
	}
	// Строка в журнале обязана быть и обязана называть ИМЕННО ТО, что поедет.
	if len(*строки) == 0 || !strings.Contains((*строки)[0], "ghcr.io/omnifield/world:sha-abc1234") {
		t.Fatalf("подъём промолчал о том, чем поднято: %v", *строки)
	}
}

// Значение штампа — ГОТОВЫЙ ТЕГ. Собирать его второй раз нельзя: приставка разъехалась бы
// с той, что ставит выпуск, и пин указал бы в пустоту.
func TestТегБерётсяКакЕстьИНеСобираетсяЗаново(t *testing.T) {
	m, _, _ := стенд(t, рецептОтвечает("ghcr.io/omnifield/world:latest"))
	m.Version = "sha-abc1234"
	_ = журнал(m)

	p := m.pinFor(context.Background(), рецептДвери, nil)
	for _, e := range p.env {
		if strings.Contains(e, "sha-sha-") || strings.Contains(e, ":sha-abc1234-") {
			t.Fatalf("тег собран второй раз: %q", e)
		}
	}
	if !есть(p.env, "WORLD_IMAGE=ghcr.io/omnifield/world:sha-abc1234") {
		t.Fatalf("пин собрался не так: %v", p.env)
	}
}

// Версии нет — это ЗАКОННОЕ состояние (собран на месте), и тогда вещь идёт тем, что назовёт
// рецепт. Молчать об этом нельзя: для разработчика зоны норма, для юзера — нет.
func TestВерсииНетГоворитсяВслухИБерётсяРецептовоеУмолчание(t *testing.T) {
	m, fake, _ := стенд(t, рецептОтвечает("ghcr.io/omnifield/world:latest"))
	m.Version = ""
	строки := журнал(m)

	if ref := m.Raise(context.Background(), "vps", "world@10.8.0.5", рецептДвери, nil); ref != nil {
		t.Fatal(ref.Why)
	}
	for i := range fake.Calls() {
		for _, e := range fake.Calls()[i].Env {
			if strings.HasPrefix(e, "WORLD_IMAGE=") {
				t.Fatalf("без версии контроллер всё равно что-то пинит: %q", e)
			}
		}
	}
	сказано := strings.Join(*строки, " ")
	if !strings.Contains(сказано, "версии не знаю") || !strings.Contains(сказано, "latest") {
		t.Fatalf("про отсутствие версии не сказано вслух: %v", *строки)
	}
}

// ЯВНОЕ ИМЯ ОТ ЮЗЕРА СТАРШЕ ПИНА (`WORLD2` 0.1). Мир не решает за юзера: назвал образ —
// поедет он, а наше дело сказать это вслух, а не переписать молча.
func TestЯвноеИмяЮзераСтаршеПина(t *testing.T) {
	m, fake, _ := стенд(t, рецептОтвечает("ghcr.io/omnifield/world:latest"))
	m.Version = "sha-abc1234"
	m.Named = map[string]string{"WORLD_IMAGE": "своя/дверь:мояверсия"}
	строки := журнал(m)

	if ref := m.Raise(context.Background(), "vps", "world@10.8.0.5", рецептДвери, nil); ref != nil {
		t.Fatal(ref.Why)
	}
	for i := range fake.Calls() {
		for _, e := range fake.Calls()[i].Env {
			if strings.HasPrefix(e, "WORLD_IMAGE=") && !strings.Contains(e, "своя/дверь") {
				t.Fatalf("наш пин переписал слово юзера: %q", e)
			}
		}
	}
	if !strings.Contains(strings.Join(*строки, " "), "старше") {
		t.Fatalf("не сказано, что имя названо снаружи: %v", *строки)
	}
}

// Рецепт может имя образа подстановкой НЕ БРАТЬ — тогда пин не сработал, и журнал обязан
// говорить правду, а не то, что мы хотели сделать (`WORLD2` 4.2 п. 5).
func TestПинНеСработалСказаноКакЕсть(t *testing.T) {
	m, _, _ := стенд(t, func(c run.Command) (run.Result, error) {
		if есть(c.Args, "compose") {
			// Рецепт называет своё имя всегда — подстановки он не читает.
			return run.Result{Out: "весы:1.2\n"}, nil
		}
		return докерОтвечает(c)
	})
	m.Version = "sha-abc1234"
	строки := журнал(m)

	if ref := m.Raise(context.Background(), "vps", "world@10.8.0.5", "/рецепты/весы.yaml", nil); ref != nil {
		t.Fatal(ref.Why)
	}
	сказано := strings.Join(*строки, " ")
	if strings.Contains(сказано, "sha-abc1234") && !strings.Contains(сказано, "не берёт") {
		t.Fatalf("журнал соврал: пин не сработал, а сказано обратное: %v", *строки)
	}
	if !strings.Contains(сказано, "весы:1.2") {
		t.Fatalf("не названо, чем поднято на самом деле: %v", *строки)
	}
}

func TestРецептСНесколькимиОбразамиНеПинится(t *testing.T) {
	m, _, _ := стенд(t, func(c run.Command) (run.Result, error) {
		if есть(c.Args, "compose") {
			return run.Result{Out: "первый:latest\nвторой:latest\n"}, nil
		}
		return докерОтвечает(c)
	})
	m.Version = "sha-abc1234"
	_ = журнал(m)

	p := m.pinFor(context.Background(), "/рецепты/пара.yaml", nil)
	if len(p.env) != 0 {
		t.Fatalf("рецепт с двумя образами пропинен одним именем: %v", p.env)
	}
	if !strings.Contains(p.say, "2") {
		t.Fatalf("не сказано, почему не пиним: %q", p.say)
	}
}

func TestИмяОбразаСПортомРеестраНеЛомается(t *testing.T) {
	tests := []struct {
		образ string
		ждём  string
		можно bool
	}{
		{"ghcr.io/omnifield/world:latest", "ghcr.io/omnifield/world:sha-abc1234", true},
		{"localhost:5000/вещь", "localhost:5000/вещь:sha-abc1234", true},
		{"localhost:5000/вещь:latest", "localhost:5000/вещь:sha-abc1234", true},
		{"вещь@sha256:0123456789", "", false},
	}
	for _, tt := range tests {
		got, ok := withTag(tt.образ, "sha-abc1234")
		if ok != tt.можно || got != tt.ждём {
			t.Fatalf("%q → %q,%v; ждали %q,%v", tt.образ, got, ok, tt.ждём, tt.можно)
		}
	}
}

// Снимаем ТЕМ ЖЕ, чем ставили: без пина `--with-image` унёс бы образ `latest`, а не тот,
// которым вещь поднята, — и человек получил бы «снял», не сняв.
func TestСнятиеИдётТемЖеПином(t *testing.T) {
	m, fake, _ := стенд(t, рецептОтвечает("ghcr.io/omnifield/world:latest"))
	m.Version = "sha-abc1234"
	_ = журнал(m)

	if _, ref := m.Lower(context.Background(), "vps", рецептДвери, "", false, true); ref != nil {
		t.Fatal(ref.Why)
	}
	var env []string
	for i := range fake.Calls() {
		if strings.Contains(fake.Line(i), "remote.sh drop") {
			env = fake.Calls()[i].Env
		}
	}
	if !есть(env, "WORLD_IMAGE=ghcr.io/omnifield/world:sha-abc1234") {
		t.Fatalf("снятие пошло не тем образом, которым ставили: %v", env)
	}
}
