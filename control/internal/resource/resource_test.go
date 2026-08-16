package resource

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omnifield/world/control/internal/recipe"
	"github.com/omnifield/world/control/internal/run"
)

// Отказ `deploy/remote.sh` — ровно в том виде, в каком он его печатает: машинный код в
// stdout, причина и выходы в stderr, с цветом. Списан с живого вывода соседа.
const remoteRefusal = "REMOTE-REFUSAL: no-docker\n"
const remoteRefusalHuman = "\n\x1b[1;31m✗ отказ:\x1b[0m на ресурсе world@10.8.0.5 нет докера\n" +
	"  выход: поставь докер на той машине\n" +
	"  выход: проверь, что ssh ходит тем же юзером\n"

// Рецепт двери — тот же путь, что складывает подъём контроллера: файл запуска лежит рядом
// с подъёмом. Существования файла здесь не требуется намеренно — годность рецепта
// проверяет сосед и отвечает тройкой отказов, а вторая такая же проверка разъехалась бы с
// его проверкой на первой правке.
const doorRecipe = "/opt/world/deploy/compose.yaml"

func manager(t *testing.T, fake *run.Fake) *Manager {
	t.Helper()
	return &Manager{
		Runner:   fake,
		RemoteSh: "/opt/world/deploy/remote.sh",
		Recipes:  &recipe.Catalog{Dir: t.TempDir(), Door: doorRecipe},
		Docker:   "docker",
		KeysDir:  t.TempDir(),
		Port:     8080,
	}
}

// докерОтвечает — подставной докер, у которого на ресурсе стоит одна здоровая вещь.
// Он НЕ притворяется докером: отвечает ровно теми полями, которые контроллер спросил.
func докерОтвечает(c run.Command) (run.Result, error) {
	switch {
	case has(c.Args, "context"):
		return run.Result{Out: "default\tunix:///var/run/docker.sock\n" +
			"world-vps\tssh://world@10.8.0.5\n"}, nil
	case has(c.Args, "ps"):
		return run.Result{Out: "aaa111\n"}, nil
	case has(c.Args, "inspect"):
		return run.Result{Out: "world\thealthy\trunning\n"}, nil
	}
	return run.Result{}, nil
}

func has(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// ── список ───────────────────────────────────────────────────────────────────

func TestСписокБерётсяИзКонтекстовДокера(t *testing.T) {
	fake := &run.Fake{Answer: func(c run.Command) (run.Result, error) {
		switch {
		case has(c.Args, "context"):
			return run.Result{Out: "default\tunix:///var/run/docker.sock\n" +
				"world-vps\tssh://world@10.8.0.5\n" +
				"чужой-контекст\tssh://кто-то@1.2.3.4\n"}, nil
		case has(c.Args, "ps"):
			return run.Result{Out: "aaa111\n"}, nil
		case has(c.Args, "inspect"):
			return run.Result{Out: "world\thealthy\trunning\n"}, nil
		}
		return run.Result{}, nil
	}}

	list, ref := manager(t, fake).List(context.Background())
	if ref != nil {
		t.Fatalf("список не собрался: %v", ref)
	}
	if len(list) != 2 {
		t.Fatalf("в списке %d источник(ов), а ждали два — «здесь» и vps: %+v", len(list), list)
	}
	if !list[0].Here || list[0].Name != HereName {
		t.Fatalf("первым обязан идти ресурс, где стоит контроллер: %+v", list[0])
	}
	if list[1].Name != "vps" || list[1].Addr != "world@10.8.0.5" || list[1].Reach != "отвечает" {
		t.Fatalf("ресурс разобрался не так: %+v", list[1])
	}
	// Ресурс — машина, а на ней СПИСОК вещей. Одного поля про дверь здесь больше нет:
	// вторая вещь не должна требовать правки кода (`WORLD2-131`).
	if len(list[1].Things) != 1 || list[1].Things[0].Name != "world" || !list[1].Things[0].Alive {
		t.Fatalf("вещи на ресурсе разобрались не так: %+v", list[1].Things)
	}
	// Чужие контексты — не наши ресурсы: список мира не должен подбирать всё, что
	// человек завёл в докере своими руками.
	for _, r := range list {
		if strings.Contains(r.Name, "чужой") {
			t.Fatalf("в список попал чужой контекст: %+v", r)
		}
	}
}

func TestСвоегоРеестраЗонаНеЗаводит(t *testing.T) {
	dir := t.TempDir()
	fake := &run.Fake{Answer: докерОтвечает}
	m := &Manager{Runner: fake, RemoteSh: "/opt/world/deploy/remote.sh",
		Recipes: &recipe.Catalog{Door: doorRecipe}, Docker: "docker", KeysDir: dir}
	if _, ref := m.List(context.Background()); ref != nil {
		t.Fatalf("список не собрался: %v", ref)
	}

	// В связке — только то, что мы туда клали (ничего). Файла-реестра ресурсов быть не
	// должно: два списка одного и того же однажды разъедутся.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("зона завела своё состояние: %v", entries)
	}
}

// Состояние вещи ИЗМЕРЯЕТСЯ, а не выводится: у вещи несколько контейнеров, и целое не
// бывает здоровее худшей своей части. `none` (у образа нет HEALTHCHECK-а) — это НЕ
// «здорова»: приблизительная запись хуже отсутствующей (`WORLD2` 4.2).
func TestСостояниеВещиИзмеряется(t *testing.T) {
	cases := []struct {
		имя     string
		inspect string
		state   string
		alive   bool
	}{
		{имя: "одна здоровая", inspect: "world\thealthy\trunning", state: "здорова", alive: true},
		{имя: "две здоровые", inspect: "world\thealthy\trunning\nworld\thealthy\trunning", state: "здорова", alive: true},
		{имя: "поднимается", inspect: "world\tstarting\trunning", state: "поднимается"},
		{имя: "нездорова", inspect: "world\tunhealthy\trunning", state: "нездорова"},
		{имя: "часть встала", inspect: "world\thealthy\trunning\nworld\tnone\texited", state: "запущена не вся"},
		{имя: "здоровья нет вовсе", inspect: "world\tnone\trunning", state: "запущена, здоровья не спросить"},
		// Здоровый рядом с непроверяемым — то же правило, что у соседа: ждём тех, у кого
		// есть HEALTHCHECK, и не выдаём отсутствие проверки за поломку.
		{имя: "здоровый и непроверяемый", inspect: "world\thealthy\trunning\nworld\tnone\trunning", state: "здорова", alive: true},
	}
	for _, c := range cases {
		fake := &run.Fake{Answer: func(cmd run.Command) (run.Result, error) {
			if has(cmd.Args, "ps") {
				return run.Result{Out: "aaa111\nbbb222\n"}, nil
			}
			return run.Result{Out: c.inspect}, nil
		}}
		reach, things := manager(t, fake).things(context.Background(), "world-vps")
		if reach != "отвечает" || len(things) != 1 {
			t.Fatalf("%s: ресурс ответил %q, вещей %d: %+v", c.имя, reach, len(things), things)
		}
		if things[0].State != c.state || things[0].Alive != c.alive {
			t.Fatalf("%s: %q/%v, а ждали %q/%v", c.имя, things[0].State, things[0].Alive, c.state, c.alive)
		}
	}
}

// Молчащий ресурс и ресурс без вещей — РАЗНЫЕ ответы. Пустой список вместо «не спросили»
// читался бы как знание, которого у нас нет.
func TestМолчащийРесурсНеВыдаётсяЗаПустой(t *testing.T) {
	молчит := &run.Fake{Answer: func(run.Command) (run.Result, error) {
		return run.Result{Err: "cannot connect to the Docker daemon", Code: 1}, nil
	}}
	reach, things := manager(t, молчит).things(context.Background(), "world-vps")
	if reach != "молчит" || things != nil {
		t.Fatalf("молчащий ресурс отдал %q/%+v", reach, things)
	}

	пустой := &run.Fake{Answer: func(run.Command) (run.Result, error) { return run.Result{}, nil }}
	reach, things = manager(t, пустой).things(context.Background(), "world-vps")
	if reach != "отвечает" || things == nil || len(things) != 0 {
		t.Fatalf("ресурс без вещей отдал %q/%+v", reach, things)
	}
}

// Контейнер, поднятый не компоузом, вещью мира не является: рецепта у него нет, и назвать
// его нам нечем. Чужое хозяйство хозяина машины в список мира не попадает.
func TestЧужиеКонтейнерыВещамиНеСчитаются(t *testing.T) {
	fake := &run.Fake{Answer: func(c run.Command) (run.Result, error) {
		if has(c.Args, "ps") {
			return run.Result{Out: "aaa111\nbbb222\n"}, nil
		}
		return run.Result{Out: "\tnone\trunning\nworld\thealthy\trunning\n"}, nil
	}}
	_, things := manager(t, fake).things(context.Background(), "world-vps")
	if len(things) != 1 || things[0].Name != "world" {
		t.Fatalf("в список вещей попало чужое: %+v", things)
	}
}

// ── добавление ───────────────────────────────────────────────────────────────

func TestДобавлениеЗовётГотовыйПодъём(t *testing.T) {
	fake := &run.Fake{Answer: func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") {
			return run.Result{}, nil
		}
		return докерОтвечает(c)
	}}
	m := manager(t, fake)

	got, ref := m.Add(context.Background(), "vps", "world@10.8.0.5", "-----ключ-----", "")
	if ref != nil {
		t.Fatalf("ресурс не добавился: %v", ref)
	}
	if got.Name != "vps" || got.Reach != "отвечает" || len(got.Things) != 1 {
		t.Fatalf("ответ собрался не так: %+v", got)
	}
	if !fake.Called("remote.sh", "add", "vps", "--addr", "world@10.8.0.5") {
		t.Fatalf("готовый подъём не позван — значит зона написала свой:\n%s", fake.Line(0))
	}
	// Рецепт называется ВСЕГДА, даже когда он тот же, что у соседа по умолчанию: умолчание
	// принадлежит его команде, и молчаливая опора на него однажды подняла бы не ту вещь.
	if !fake.Called("remote.sh", "--recipe", doorRecipe) {
		t.Fatalf("подъём позван без рецепта — вещь взялась из умолчания соседа:\n%s", fake.Line(0))
	}

	// Ключ лёг в связку, и `config` показывает ssh, каким ключом ходить на эту машину:
	// у контекста докера поля под ключ нет вовсе.
	key := filepath.Join(m.KeysDir, "world-vps")
	if info, err := os.Stat(key); err != nil {
		t.Fatalf("ключ не лёг в связку: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("права на ключ %v — ssh такой ключ не примет", info.Mode().Perm())
	}
	cfg, err := os.ReadFile(filepath.Join(m.KeysDir, "config"))
	if err != nil {
		t.Fatalf("config не завёлся: %v", err)
	}
	for _, want := range []string{"Host 10.8.0.5", "IdentityFile " + key, "IdentitiesOnly yes"} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("в config нет %q:\n%s", want, cfg)
		}
	}
}

// ГЛАВНАЯ проверка захода (`WORLD2-131`): вторая вещь поднимается тем же путём, и для
// этого не правится ни строки кода. Кладём второй рецепт в каталог — и зовём его по имени.
func TestВтораяВещьНеТребуетПравкиКода(t *testing.T) {
	fake := &run.Fake{Answer: func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") {
			return run.Result{}, nil
		}
		return докерОтвечает(c)
	}}
	m := manager(t, fake)
	весы := filepath.Join(m.Recipes.Dir, "весы.yaml")
	if err := os.WriteFile(весы, []byte("name: весы\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, ref := m.Add(context.Background(), "vps", "world@10.8.0.5", "", "весы"); ref != nil {
		t.Fatalf("вторая вещь не поднялась: %v", ref)
	}
	if !fake.Called("remote.sh", "add", "vps", "--recipe", весы) {
		t.Fatalf("подъём позван не тем рецептом:\n%s", fake.Line(0))
	}

	// И снимается она тем же рецептом: своего реестра вещей зона не заводит.
	if _, ref := m.Drop(context.Background(), "vps", false, false, "весы"); ref != nil {
		t.Fatalf("вторая вещь не снялась: %v", ref)
	}
	if !fake.Called("remote.sh", "drop", "vps", "--recipe", весы) {
		t.Fatalf("снятие пошло не тем рецептом:\n%s", fake.Line(len(fake.Calls())-1))
	}
}

// Рецепт, которого нет, обязан отказать НАШИМ кодом и до того, как тронут ресурс: имя
// рецепта — наше знание, и отвечать за него соседу не за что.
func TestНеизвестныйРецептОтказываетДоПодъёма(t *testing.T) {
	fake := &run.Fake{}
	m := manager(t, fake)

	_, ref := m.Add(context.Background(), "vps", "world@10.8.0.5", "ключ", "весы")
	if ref == nil || ref.Code != "no-such-recipe" {
		t.Fatalf("ждали no-such-recipe, получили %v", ref)
	}
	if len(fake.Calls()) != 0 {
		t.Fatalf("до подъёма дело дошло на неизвестном рецепте: %s", fake.Line(0))
	}
	if _, err := os.Stat(filepath.Join(m.KeysDir, "world-vps")); !os.IsNotExist(err) {
		t.Fatal("отказ оставил за собой ключ — а отказ не вправе оставлять следов")
	}
}

// Путь, названный юзером, уезжает подъёму КАК ЕСТЬ: годность файла проверяет сосед и
// отвечает своей тройкой (`no-recipe` · `bad-recipe` · `recipe-no-image`), а вторая такая
// же проверка здесь разъехалась бы с ней на первой правке.
func TestПутьРецептаУезжаетПодъёмуКакЕсть(t *testing.T) {
	fake := &run.Fake{Answer: func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") {
			return run.Result{}, nil
		}
		return докерОтвечает(c)
	}}
	m := manager(t, fake)

	if _, ref := m.Add(context.Background(), "vps", "world@10.8.0.5", "", "/чужой/путь/весы.yaml"); ref != nil {
		t.Fatalf("названный путь не доехал: %v", ref)
	}
	if !fake.Called("remote.sh", "--recipe", "/чужой/путь/весы.yaml") {
		t.Fatalf("путь рецепта до подъёма не доехал:\n%s", fake.Line(0))
	}
}

func TestКлючДокладываетсяДоПодъёма(t *testing.T) {
	var order []string
	m := manager(t, nil)
	fake := &run.Fake{Answer: func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") {
			if _, err := os.Stat(filepath.Join(m.KeysDir, "world-vps")); err != nil {
				t.Fatalf("подъём позван РАНЬШЕ, чем лёг ключ: докер пойдёт по ssh без него")
			}
			order = append(order, "подъём")
		}
		return run.Result{Out: "healthy"}, nil
	}}
	m.Runner = fake

	if _, ref := m.Add(context.Background(), "vps", "world@10.8.0.5", "ключ", ""); ref != nil {
		t.Fatalf("ресурс не добавился: %v", ref)
	}
	if len(order) != 1 {
		t.Fatalf("подъём позван %d раз(а)", len(order))
	}
}

func TestОтказПодъёмаДоезжаетСвоимКодом(t *testing.T) {
	fake := &run.Fake{Answer: func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") {
			return run.Result{Out: remoteRefusal, Err: remoteRefusalHuman, Code: 1}, nil
		}
		return run.Result{}, nil
	}}
	m := manager(t, fake)

	_, ref := m.Add(context.Background(), "vps", "world@10.8.0.5", "ключ", "")
	if ref == nil {
		t.Fatal("подъём отказал, а контроллер промолчал")
	}
	if ref.Code != "no-docker" {
		t.Fatalf("код соседа переписан в %q — словарь отказов раздвоился", ref.Code)
	}
	if ref.From != "deploy/remote.sh" {
		t.Fatalf("не названо, чей это отказ: %+v", ref)
	}
	if !strings.Contains(ref.Why, "нет докера") {
		t.Fatalf("причина потеряна: %q", ref.Why)
	}
	if len(ref.Ways) != 2 {
		t.Fatalf("выходы соседа потеряны: %v", ref.Ways)
	}
	if strings.Contains(ref.Why+strings.Join(ref.Ways, ""), "\x1b[") {
		t.Fatalf("цвет терминала уехал в JSON: %+v", ref)
	}
}

// Неудачный подъём не должен оставлять за собой ключ: вторая попытка пошла бы кредами,
// которых юзер уже не называет, и отказ «ключ не принят» приехал бы про ключ-призрак.
func TestНеудачныйПодъёмУбираетКлючЗаСобой(t *testing.T) {
	fake := &run.Fake{Answer: func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") {
			return run.Result{Out: remoteRefusal, Err: remoteRefusalHuman, Code: 1}, nil
		}
		return run.Result{}, nil
	}}
	m := manager(t, fake)

	if _, ref := m.Add(context.Background(), "vps", "world@10.8.0.5", "ключ", ""); ref == nil {
		t.Fatal("ждали отказ")
	}
	if _, err := os.Stat(filepath.Join(m.KeysDir, "world-vps")); !os.IsNotExist(err) {
		t.Fatalf("ключ остался после неудачи: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(m.KeysDir, "config")); err == nil && strings.Contains(string(data), "10.8.0.5") {
		t.Fatalf("блок в config остался после неудачи:\n%s", data)
	}
}

func TestИменаИАдресаПроверяютсяДоВызова(t *testing.T) {
	fake := &run.Fake{}
	m := manager(t, fake)

	cases := []struct{ name, addr, code string }{
		{"", "world@10.8.0.5", "no-name"},
		{"../побег", "world@10.8.0.5", "bad-name"},
		{"ВПС", "world@10.8.0.5", "bad-name"},
		{"vps", "", "no-address"},
		{"vps", "10.8.0.5", "bad-address"},
		{"vps", "@10.8.0.5", "bad-address"},
		{"vps", "world@10.8.0.5:порт", "bad-address"},
	}
	for _, c := range cases {
		_, ref := m.Add(context.Background(), c.name, c.addr, "", "")
		if ref == nil || ref.Code != c.code {
			t.Fatalf("%q/%q → %v, а ждали %s", c.name, c.addr, ref, c.code)
		}
	}
	if len(fake.Calls()) != 0 {
		t.Fatalf("до подъёма дело дошло на кривых значениях: %s", fake.Line(0))
	}
}

// ── снятие ───────────────────────────────────────────────────────────────────

func TestСнятиеНазываетЧтоОсталось(t *testing.T) {
	fake := &run.Fake{Answer: func(c run.Command) (run.Result, error) { return run.Result{}, nil }}
	m := manager(t, fake)
	if _, ref := m.Add(context.Background(), "vps", "world@10.8.0.5", "ключ", ""); ref != nil {
		t.Fatalf("ресурс не добавился: %v", ref)
	}

	got, ref := m.Drop(context.Background(), "vps", false, false, "")
	if ref != nil {
		t.Fatalf("ресурс не снялся: %v", ref)
	}
	if len(got.Left) != 2 || len(got.Ways) != 2 {
		t.Fatalf("оставленное не названо: %+v", got)
	}
	if !fake.Called("remote.sh", "drop", "vps") {
		t.Fatal("снятие пошло не через готовый подъём")
	}
	// След в связке снимается вместе с ресурсом.
	if _, err := os.Stat(filepath.Join(m.KeysDir, "world-vps")); !os.IsNotExist(err) {
		t.Fatalf("ключ пережил ресурс: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(m.KeysDir, "config")); err == nil && strings.Contains(string(data), "10.8.0.5") {
		t.Fatalf("блок в config пережил ресурс:\n%s", data)
	}
}

func TestСнятиеСоСостояниемГоворитОбЭтомСоседу(t *testing.T) {
	fake := &run.Fake{}
	m := manager(t, fake)
	got, ref := m.Drop(context.Background(), "vps", true, true, "")
	if ref != nil {
		t.Fatalf("ресурс не снялся: %v", ref)
	}
	if !fake.Called("drop", "vps", "--with-state", "--with-image") {
		t.Fatalf("ключи не доехали до подъёма: %s", fake.Line(0))
	}
	if len(got.Left) != 0 {
		t.Fatalf("сказали «сняли всё», а перечислили оставленное: %+v", got)
	}
}

func TestРесурсКонтроллераСнятьНельзя(t *testing.T) {
	fake := &run.Fake{}
	_, ref := manager(t, fake).Drop(context.Background(), HereName, false, false, "")
	if ref == nil || ref.Code != "drop-here" {
		t.Fatalf("ждали drop-here, получили %v", ref)
	}
	if len(fake.Calls()) != 0 {
		t.Fatal("контроллер пошёл снимать машину, на которой стоит сам")
	}
}

// ── чужая связка ─────────────────────────────────────────────────────────────

// Связка принадлежит юзеру, мы в ней гости: чужие строки обязаны пережить и добавление,
// и снятие ресурса.
func TestЧужиеСтрокиВСвязкеНеТрогаем(t *testing.T) {
	fake := &run.Fake{}
	m := manager(t, fake)
	mine := "Host мой-личный\n    User егор\n"
	if err := os.WriteFile(filepath.Join(m.KeysDir, "config"), []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ref := m.Add(context.Background(), "vps", "world@10.8.0.5", "ключ", ""); ref != nil {
		t.Fatalf("ресурс не добавился: %v", ref)
	}
	if _, ref := m.Drop(context.Background(), "vps", false, false, ""); ref != nil {
		t.Fatalf("ресурс не снялся: %v", ref)
	}

	data, err := os.ReadFile(filepath.Join(m.KeysDir, "config"))
	if err != nil {
		t.Fatalf("чужой config снесён целиком: %v", err)
	}
	if !strings.Contains(string(data), "Host мой-личный") {
		t.Fatalf("чужие строки потеряны:\n%s", data)
	}
}

func TestПовторноеДобавлениеНеПлодитБлоки(t *testing.T) {
	fake := &run.Fake{}
	m := manager(t, fake)
	for range 3 {
		if _, ref := m.Add(context.Background(), "vps", "world@10.8.0.5", "ключ", ""); ref != nil {
			t.Fatalf("ресурс не добавился: %v", ref)
		}
	}
	data, _ := os.ReadFile(filepath.Join(m.KeysDir, "config"))
	if n := strings.Count(string(data), "Host 10.8.0.5"); n != 1 {
		t.Fatalf("блок про машину лежит %d раз(а) — ssh возьмёт первый и молча:\n%s", n, data)
	}
}
