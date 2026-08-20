package resource

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omnifield/world/control/internal/recipe"
	"github.com/omnifield/world/control/internal/run"
	"github.com/omnifield/world/control/internal/state"
)

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ГЛАВНОЕ, ЧТО СТЕРЕЖЁТСЯ ЗДЕСЬ: список территорий берётся ИЗ СКОУПА, а контексты      │
// │ докера, ключи и блоки в `config` — производные от него (`WORLD2-124`).               │
// │                                                                                      │
// │ Пока список был контекстами докера, он был состоянием МАШИНЫ: вошёл под другой        │
// │ личностью — увидел чужое. Поэтому проба ниже краснеет, если список опять поехал из    │
// │ докера, и если выход оставил после себя хоть один след.                               │
// └─────────────────────────────────────────────────────────────────────────────────────┘

const рецептДвери = "/opt/world/deploy/compose.yaml"

func стенд(t *testing.T, answer func(run.Command) (run.Result, error)) (*Manager, *run.Fake, string) {
	t.Helper()
	fake := &run.Fake{Answer: answer}
	keys := filepath.Join(t.TempDir(), "keys")
	m := &Manager{
		Runner:   fake,
		RemoteSh: "/opt/world/deploy/remote.sh",
		Recipes:  &recipe.Catalog{Door: рецептДвери},
		Docker:   "docker",
		KeysDir:  keys,
		Port:     8080,
	}
	return m, fake, keys
}

// докерОтвечает — подставной докер: на территории стоит одна здоровая вещь. Он не
// притворяется докером, а отвечает ровно те поля, которые контроллер спросил.
func докерОтвечает(c run.Command) (run.Result, error) {
	switch {
	// Контекста такого нет — так отвечает докер на чистой машине. Разные ответы здесь
	// ведут к разным командам (`create` против `update`), и путать их нельзя.
	case есть(c.Args, "context") && есть(c.Args, "inspect"):
		return run.Result{Code: 1, Err: "context not found"}, nil
	case есть(c.Args, "ps"):
		return run.Result{Out: "aaa111\n"}, nil
	case есть(c.Args, "inspect") && есть(c.Args, "--format"):
		return run.Result{Out: "world\thealthy\trunning\n"}, nil
	case есть(c.Args, "ls"):
		return run.Result{Out: "default\nworld-старое\n"}, nil
	}
	return run.Result{}, nil
}

func есть(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// ── список ───────────────────────────────────────────────────────────────────

func TestСписокБерётсяИзСкоупаАНеИзДокера(t *testing.T) {
	m, fake, _ := стенд(t, докерОтвечает)

	list := m.List(context.Background(), []state.Territory{{Name: "vps", Addr: "world@10.8.0.5"}})
	if len(list) != 1 || list[0].Name != "vps" || list[0].Addr != "world@10.8.0.5" {
		t.Fatalf("список собрался не по скоупу: %+v", list)
	}
	// Контексты докера СПИСКОМ БОЛЬШЕ НЕ ЯВЛЯЮТСЯ: спрашивать `context ls`, чтобы узнать,
	// какие территории есть у юзера, значит снова держать его личность на машине.
	if fake.Called("context ls") {
		t.Fatal("список территорий поехал из контекстов докера — это состояние машины, а не личности")
	}
	// А вот СОСТОЯНИЕ спрашивается у той машины, и спрашивается её контекстом.
	if !fake.Called("--context", "world-vps", "ps") {
		t.Fatalf("состояние территории не спросили у её машины: %s", fake.Line(0))
	}
}

func TestРесурсаЗдесьВСпискеБольшеНет(t *testing.T) {
	m, _, _ := стенд(t, докерОтвечает)
	list := m.List(context.Background(), nil)
	// Машина контроллера территорией юзера не является: контроллер — времянка (`1.9`), и
	// стоящее на нём чужое хозяйство в список личности попадать не должно.
	if len(list) != 0 {
		t.Fatalf("в пустом скоупе нашлись территории: %+v", list)
	}
}

func TestМолчащаяМашинаЭтоНеПустаяМашина(t *testing.T) {
	m, _, _ := стенд(t, func(c run.Command) (run.Result, error) {
		return run.Result{Code: 1, Err: "cannot connect"}, nil
	})
	list := m.List(context.Background(), []state.Territory{{Name: "vps", Addr: "world@10.8.0.5"}})
	if list[0].Reach != "молчит" {
		t.Fatalf("машина названа отвечающей: %+v", list[0])
	}
	if list[0].Things != nil {
		t.Fatalf("«не спросили» выдано за «ничего нет»: %+v", list[0].Things)
	}
}

// ── времянки: ключи, config, контексты ───────────────────────────────────────

func TestВходРаскладываетВремянкиИзСкоупа(t *testing.T) {
	m, fake, keys := стенд(t, докерОтвечает)
	st := state.New("егор", "")
	_ = st.AddTerritory(
		state.Territory{Name: "vps", Addr: "world@10.8.0.5"},
		state.Key{Name: "vps", Kind: state.KindSSH, Value: "-----ключ-----"},
	)

	if ref := m.Bind(context.Background(), st); ref != nil {
		t.Fatalf("вход не разложил времянки: %s — %s", ref.Code, ref.Why)
	}
	ключ := filepath.Join(keys, "world-vps")
	if data, err := os.ReadFile(ключ); err != nil || !strings.Contains(string(data), "-----ключ-----") {
		t.Fatalf("ключ из скоупа не лёг в связку: %v", err)
	}
	config, err := os.ReadFile(filepath.Join(keys, "config"))
	if err != nil || !strings.Contains(string(config), "IdentityFile "+ключ) {
		t.Fatalf("ключ не назван в config — ssh его не возьмёт: %v", err)
	}
	// Контекст докера ПРОИЗВОДЕН от скоупа и собирается той же формулой, что у соседа
	// (`docker_endpoint` в deploy/remote.sh): ssh://юзер@машина:порт.
	if !fake.Called("context", "create", "world-vps", "host=ssh://world@10.8.0.5:22") {
		t.Fatalf("контекст территории не заведён из скоупа: %s", fake.Line(0))
	}
}

func TestВходСнимаетСледыПрежнейЛичности(t *testing.T) {
	m, fake, keys := стенд(t, докерОтвечает)
	// Осталось от прошлого входа: контекст (его назовёт подставной `context ls`) и ключ.
	if err := os.MkdirAll(keys, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keys, "world-старое"), []byte("чужой ключ\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if ref := m.Bind(context.Background(), state.New("другой", "")); ref != nil {
		t.Fatal(ref.Why)
	}
	if !fake.Called("context", "rm", "-f", "world-старое") {
		t.Fatalf("контекст прежней личности пережил вход: %s", fake.Line(0))
	}
	if _, err := os.Stat(filepath.Join(keys, "world-старое")); !os.IsNotExist(err) {
		t.Fatal("ключ прежней личности пережил вход — вошедший увидел бы чужое")
	}
}

func TestВыходНеТрогаетЧужиеСтрокиВСвязке(t *testing.T) {
	m, _, keys := стенд(t, докерОтвечает)
	if err := os.MkdirAll(keys, 0o700); err != nil {
		t.Fatal(err)
	}
	чужое := "Host мой-сервер\n    HostName 10.0.0.1\n"
	if err := os.WriteFile(filepath.Join(keys, "config"), []byte(чужое), 0o600); err != nil {
		t.Fatal(err)
	}
	st := state.New("егор", "")
	_ = st.AddTerritory(state.Territory{Name: "vps", Addr: "world@10.8.0.5"}, state.Key{Name: "vps", Value: "к"})

	if ref := m.Bind(context.Background(), st); ref != nil {
		t.Fatal(ref.Why)
	}
	if ref := m.Unbind(context.Background()); ref != nil {
		t.Fatal(ref.Why)
	}
	data, err := os.ReadFile(filepath.Join(keys, "config"))
	if err != nil {
		t.Fatalf("связка исчезла вместе с чужими строками: %v", err)
	}
	if !strings.Contains(string(data), "Host мой-сервер") {
		t.Fatalf("чужие строки в связке не пережили выход — связка принадлежит юзеру:\n%s", data)
	}
	if strings.Contains(string(data), "world vps") {
		t.Fatalf("наш блок пережил выход:\n%s", data)
	}
}

func TestУчастокБезКлючаВСкоупеЭтоОтказСВыходом(t *testing.T) {
	m, _, _ := стенд(t, докерОтвечает)
	st := state.New("егор", "")
	st.Territories = append(st.Territories, state.Territory{Name: "vps", Addr: "world@10.8.0.5", Key: "потерянный"})

	ref := m.Bind(context.Background(), st)
	if ref == nil || ref.Code != "scope-broken" || len(ref.Ways) == 0 {
		t.Fatalf("ссылка на несуществующий ключ проглочена: %+v", ref)
	}
}

// ── подъём и снятие вещи ─────────────────────────────────────────────────────

func TestПодъёмЗовётГотовыйИНазываетРецептВсегда(t *testing.T) {
	m, fake, _ := стенд(t, докерОтвечает)
	if _, ref := m.Raise(context.Background(), "vps", "world@10.8.0.5", рецептДвери, []string{"SHARE_PASSWORD=тайна"}); ref != nil {
		t.Fatal(ref.Why)
	}
	if !fake.Called("remote.sh", "add", "vps", "--addr", "world@10.8.0.5", "--recipe", рецептДвери) {
		t.Fatalf("подъём позван не готовый или без рецепта: %s", fake.Line(0))
	}
	// Значение вещи уезжает подъёму как есть: что вещи нужно, знает она, а не контроллер.
	var env []string
	for i := range fake.Calls() {
		if strings.Contains(fake.Line(i), "remote.sh add") {
			env = fake.Calls()[i].Env
		}
	}
	if !есть(env, "SHARE_PASSWORD=тайна") || !есть(env, "WORLD_PORT=8080") {
		t.Fatalf("значения не доехали до подъёма: %v", env)
	}
}

func TestОтказСоседаДоезжаетСвоимКодом(t *testing.T) {
	m, _, _ := стенд(t, func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") {
			return run.Result{Code: 1, Out: "REMOTE-REFUSAL: docker-install-no-net\n", Err: "отказ: машина не достала до установщика докера\n  выход: подними своё зеркало: WORLD_DOCKER_INSTALL_URL=…\n"}, nil
		}
		return докерОтвечает(c)
	})
	_, ref := m.Raise(context.Background(), "vps", "world@10.8.0.5", рецептДвери, nil)
	if ref == nil || ref.Code != "docker-install-no-net" {
		t.Fatalf("код соседа не доехал своим: %+v", ref)
	}
	if ref.From != "deploy/remote.sh" || len(ref.Ways) == 0 {
		t.Fatalf("не сказано, чей это отказ, или потеряны его выходы: %+v", ref)
	}
}

// «Такого ресурса подъём не знает» — не повод запереть участок в скоупе навсегда: вещи там
// уже нет, а убрать запись было бы нечем. Говорим вслух, что на машине ничего не трогали.
func TestСнятиеТогоЧегоПодъёмНеНашлоНеЗапираетСкоуп(t *testing.T) {
	m, _, _ := стенд(t, func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") {
			return run.Result{Code: 1, Out: "REMOTE-REFUSAL: no-such-resource\n", Err: "отказ: такого ресурса нет\n"}, nil
		}
		return докерОтвечает(c)
	})
	dropped, ref := m.Lower(context.Background(), Drop{Name: "vps", RecipePath: рецептДвери})
	if ref != nil {
		t.Fatalf("снятие уперлось в отказ и заперло участок: %+v", ref)
	}
	if dropped.Note == "" {
		t.Fatal("контроллер промолчал о том, что на машине ничего не трогал")
	}
}

func TestСнятиеНазываетЧтоОсталось(t *testing.T) {
	m, fake, _ := стенд(t, докерОтвечает)
	dropped, ref := m.Lower(context.Background(), Drop{Name: "vps", RecipePath: рецептДвери, RecipeName: "door", Ручка: "DELETE /api/resources/vps"})
	if ref != nil {
		t.Fatal(ref.Why)
	}
	if !fake.Called("remote.sh", "drop", "vps", "--recipe", рецептДвери) {
		t.Fatalf("снятие пошло без рецепта: %s", fake.Line(0))
	}
	if len(dropped.Left) == 0 || len(dropped.Ways) == 0 {
		t.Fatalf("снятие не назвало, что осталось на машине: %+v", dropped)
	}
	// Выход обязан быть КОМАНДОЙ, которую можно повторить как есть: потеряв рецепт, вторая
	// попытка сняла бы не ту вещь.
	if !strings.Contains(strings.Join(dropped.Ways, " "), "recipe=door") {
		t.Fatalf("в выходе потерялся рецепт: %v", dropped.Ways)
	}
}

// ── имена и адреса ───────────────────────────────────────────────────────────

func TestИмяУчасткаНеВыдумывается(t *testing.T) {
	if ref := ValidName(""); ref == nil || ref.Code != "no-name" {
		t.Fatalf("безымянный участок принят: %+v", ref)
	}
	if ref := ValidName("../чужое"); ref == nil || ref.Code != "bad-name" {
		t.Fatalf("имя, уводящее запись из связки, принято: %+v", ref)
	}
}

func TestАдресМашиныПроверяетсяРовноНастолькоНасколькоНаш(t *testing.T) {
	if _, _, ref := CheckAddr("10.8.0.5"); ref == nil || ref.Code != "bad-address" {
		t.Fatalf("адрес без юзера принят — ssh пошёл бы текущим: %+v", ref)
	}
	host, port, ref := CheckAddr("world@10.8.0.5:2222")
	if ref != nil || host != "10.8.0.5" || port != 2222 {
		t.Fatalf("адрес с портом разобран не так: %s %d %+v", host, port, ref)
	}
}

// ── метка пути: слово соседа, довезённое дословно ────────────────────────────

// Контроллер НЕ ТОЛКУЕТ путь и своего словаря не заводит: перечень путей принадлежит
// соседу. Проверяется поэтому не «узнал ли он `image-copy`», а то, что мимо метки не
// проходит ничего и что через метку проходит ЛЮБОЕ слово, включая незнакомое.
func TestМеткаПутиДовозитсяДословноИТолькоОна(t *testing.T) {
	m, _, _ := стенд(t, func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") && c.OnLine != nil {
			c.OnLine("REMOTE-PATH: image-copy")
			c.OnLine("REMOTE-REFUSAL: no-daemon") // не путь — это код отказа
			c.OnLine("  ✓ образ поехал")          // не путь — это слова человеку
			c.OnLine("REMOTE-PATH: путь-которого-мы-не-знаем")
			c.OnLine("REMOTE-PATH:   ") // пустое имя пути — не путь
			return run.Result{}, nil
		}
		return докерОтвечает(c)
	})
	var пути []string
	m.OnPath = func(_, path string) { пути = append(пути, path) }

	if _, ref := m.Raise(context.Background(), "vps", "world@10.8.0.5", рецептДвери, nil); ref != nil {
		t.Fatal(ref.Why)
	}
	if len(пути) != 2 || пути[0] != "image-copy" || пути[1] != "путь-которого-мы-не-знаем" {
		t.Fatalf("метка разобрана не дословно либо мимо неё прошло лишнее: %q", пути)
	}
}

// Снятие несёт ТЕ ЖЕ значения, что подъём: рецепт собирает из них имя проекта, контейнера
// и тома. Снять без них значит снять вещь ПО УМОЛЧАНИЮ — то есть соседнюю.
func TestСнятиеВезётЗначенияРецепта(t *testing.T) {
	m, fake, _ := стенд(t, докерОтвечает)
	if _, ref := m.Lower(context.Background(), Drop{Name: "vps", RecipePath: рецептДвери, Env: []string{"SHARE_NAME=vps-8071"}}); ref != nil {
		t.Fatal(ref.Why)
	}
	var env []string
	for i := range fake.Calls() {
		if strings.Contains(fake.Line(i), "remote.sh drop") {
			env = fake.Calls()[i].Env
		}
	}
	if !есть(env, "SHARE_NAME=vps-8071") || !есть(env, "WORLD_PORT=8080") {
		t.Fatalf("значения не доехали до снятия: %v", env)
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ЮЗЕР ВИДИТ ТОЛЬКО СВОИ ВЕЩИ (решение user 2026-08-20, найдено живым прогоном).       │
// │                                                                                      │
// │ На одной машине законно стоят раздачи РАЗНЫХ юзеров. Список показывал их все — с      │
// │ именами и портами, — а пульт открыт по адресу: значит чужое видел бы каждый, кто до   │
// │ него дошёл.                                                                           │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestЧужиеВещиНаТойЖеМашинеНеПоказываются(t *testing.T) {
	m, _, _ := стенд(t, func(c run.Command) (run.Result, error) {
		switch {
		case есть(c.Args, "context") && есть(c.Args, "inspect"):
			return run.Result{Code: 1, Err: "context not found"}, nil
		case есть(c.Args, "ps"):
			return run.Result{Out: "aaa\nbbb\nccc\n"}, nil
		case есть(c.Args, "inspect") && есть(c.Args, "--format"):
			// Наша дверь, наша раздача — и раздача ЧУЖОГО скоупа на той же машине.
			return run.Result{Out: "world\thealthy\trunning\nvps-8071\thealthy\trunning\nчужой-8072\thealthy\trunning\n"}, nil
		}
		return run.Result{}, nil
	})

	list := m.List(context.Background(), []state.Territory{
		{Name: "vps", Addr: "world@10.8.0.5", Things: []string{"world", "vps-8071"}},
	})
	var имена []string
	for _, thing := range list[0].Things {
		имена = append(имена, thing.Name)
		if thing.Name == "чужой-8072" {
			t.Fatalf("чужая раздача попала в список юзера: %v", имена)
		}
	}
	if len(имена) != 2 {
		t.Fatalf("своих вещей должно быть две, а показано: %v", имена)
	}
}

// ПУСТОЙ СПИСОК — ЭТО «НЕ ЗНАЕМ», А НЕ «НИЧЕГО СВОЕГО НЕТ». Так выглядят скоупы, заведённые
// до того, как контроллер начал помнить поднятое: спрятать у них всё значило бы стереть
// юзеру его вещи с экрана.
func TestСкоупБезПамятиОВещахВидитВсё(t *testing.T) {
	m, _, _ := стенд(t, докерОтвечает)
	list := m.List(context.Background(), []state.Territory{{Name: "vps", Addr: "world@10.8.0.5"}})
	if len(list[0].Things) == 0 {
		t.Fatal("старому скоупу спрятали его собственные вещи")
	}
}

// А МОЛЧАЩАЯ машина остаётся молчащей: `null` и пустой список — разные ответы (`4.2`).
func TestОтсевНеПревращаетМолчаниеВПустоту(t *testing.T) {
	m, _, _ := стенд(t, func(c run.Command) (run.Result, error) {
		return run.Result{Code: 1, Err: "cannot connect"}, nil
	})
	list := m.List(context.Background(), []state.Territory{
		{Name: "vps", Addr: "world@10.8.0.5", Things: []string{"world"}},
	})
	if list[0].Things != nil {
		t.Fatalf("молчание превратилось в пустой список: %+v", list[0])
	}
}
