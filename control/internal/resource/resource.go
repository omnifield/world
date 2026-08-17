// Пакет resource — источники ресурса: территории юзера, до которых он дотянулся.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ТЕРРИТОРИИ ЖИВУТ В СКОУПЕ, А НЕ ЗДЕСЬ (`WORLD2` 3.4 п. 2, `1.9`, `WORLD2-124`).      │
// │                                                                                      │
// │ Раньше списком ресурсов были контексты докера на машине контроллера — то есть         │
// │ состояние МАШИНЫ: зашёл под другой личностью и видел чужое. Теперь список — это       │
// │ раздел `территории` скоупа, а контексты докера, ключи в связке и блоки в `config`     │
// │ стали ПРОИЗВОДНЫМИ: поднялись из скоупа при входе, ушли при выходе.                   │
// │                                                                                      │
// │ Отсюда главное свойство зоны: контроллер ПУСТ. Снёс и поднял на другой машине —       │
// │ вошёл по тому же адресу, и всё на месте (`1.9`: контроллеру положено быть времянкой). │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ЭТОТ ПАКЕТ НЕ ПОДНИМАЕТ ВЕЩЬ. Подъём вещи на названном ресурсе уже написан и лежит   │
// │ в `deploy/remote.sh` (контекст докера по ssh, `WORLD2-99`). Контроллер его ЗОВЁТ.    │
// │ Второй подъём того же самого разъехался бы с первым на первой правке соседа — и      │
// │ человек получил бы две двери, которые ставятся по-разному.                            │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// РЕСУРС — ЭТО МАШИНА, А НЕ «МАШИНА С ДВЕРЬЮ» (`WORLD2` 3.7, `WORLD2-131`). Что на ней
// стоит — отдельный вопрос и отдельное поле: СПИСОК вещей. Имён контейнеров, образов и
// портов у зоны нет вовсе: они принадлежат рецепту, и знать их контроллеру неоткуда.
//
// У ТЕРРИТОРИИ ЕСТЬ ИМЯ, И ЕГО ДАЁТ ЮЗЕР (`2.5` п. 11). Мир его не выдумывает и из адреса
// машины не выводит; неповторимость имени в скоупе стережёт контроллер (`internal/state`),
// потому что раздача файл не разбирает вовсе.
package resource

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/omnifield/world/control/internal/recipe"
	"github.com/omnifield/world/control/internal/refusal"
	"github.com/omnifield/world/control/internal/run"
	"github.com/omnifield/world/control/internal/state"
)

const (
	// ContextPrefix — приставка имени контекста докера (`PREFIX` в deploy/remote.sh).
	// Значение ПРИНАДЛЕЖИТ зоне `deploy` и здесь повторено: по нему мы узнаём свои
	// контексты среди чужих. Повтор — это шов, и он стережётся пробой (`probe-control.sh`
	// читает `deploy/remote.sh` и краснеет, если там стало другое).
	ContextPrefix = "world-"
	// projectLabel — метка компоуза, по которой видно, ЧЬЁ это хозяйство. Ставит её сам
	// компоуз на всё, что заводит, и читает её же сосед (`deploy/remote.sh`).
	projectLabel = "com.docker.compose.project"
)

// nameRe — имя территории. Правило то же, что у соседа (`bad-name` в deploy/remote.sh):
// имя идёт в имя контекста докера и в имя файла ключа в связке.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// Thing — вещь, поднятая на территории. Имя — имя проекта компоуза: им помечено всё, что
// вещь на той машине завела, и его же называет рецепт. Своего перечня вещей у зоны нет.
type Thing struct {
	Name string `json:"name"`
	// State — что видно про вещь словами. Измеренное, а не выведенное: состояние,
	// выведенное вместо измеренного, врёт ровно тогда, когда на него смотрят (`4.2`).
	State string `json:"state"`
	// Alive — вещь ОТВЕЧАЕТ, а не «запущена». У вещи без HEALTHCHECK-а ответ не спросить
	// вовсе, и тогда здесь `false` — не «мертва», а «не подтверждено».
	Alive bool `json:"alive"`
}

// Resource — территория глазами юзера. Ни памяти, ни ядер здесь нет: ресурс в цифрах даёт
// отдельный инструмент осмотра, и он сознательно отложен (`WORLD2` 2.5 п. 10).
type Resource struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
	// Reach — отвечает ли САМА машина: «отвечает» либо «молчит». Вопрос про машину, а не
	// про вещь на ней, и ответ на него не выводится из другого поля.
	Reach string `json:"reach"`
	// Things — что на территории поднято. `null` означает «не спросили» (машина молчит), а
	// пустой список — «спросили, и там ничего нет» (`WORLD2` 4.2).
	Things []Thing `json:"things"`
}

// Manager — работа с территориями юзера.
type Manager struct {
	Runner run.Runner
	// RemoteSh — путь к готовому подъёму вещи. Значение, а не константа: в образе он
	// лежит по своему пути, в девбоксе — по своему, а проба подменяет его заглушкой.
	RemoteSh string
	// Recipes — где контроллер берёт рецепты (`internal/recipe`).
	Recipes *recipe.Catalog
	// Docker — имя докер-клиента. Подменяется пробой по той же причине.
	Docker string
	// KeysDir — связка контроллера (`~/.ssh` внутри его образа). ВРЕМЯНКА: ключи в неё
	// кладутся из скоупа при входе и снимаются при выходе.
	KeysDir string
	// Port — хост-порт двери на той машине; едет `deploy/remote.sh` тем же именем, каким
	// тот его ждёт. Рецепт, который его не читает, о нём и не узнает.
	Port int
	// Version — СВОЯ версия сборки: штамп `WORLD_VERSION`, который выпуск вписал в образ
	// (`WORLD2-130`). Ею контроллер пинит вещи, которые ставит. Пусто — версии нет, и это
	// законное состояние: собран не выпуском, а на месте.
	Version string
	// Named — подстановки имени образа, названные ХОЗЯИНОМ снаружи (`WORLD_IMAGE=…` в
	// команде подъёма). Явное слово юзера старше нашего пина (`WORLD2` 0.1), поэтому мы их
	// не переписываем, а говорим о них вслух.
	Named map[string]string
	// Logf — журнал. Подъём обязан называть, ЧЕМ поднято, и называть это КАЖДЫЙ раз, а не
	// один раз на старте: тег `latest` подвижен, и «поднял старое и не заметил» — ровно тот
	// ложный результат, ради которого узел и заведён.
	Logf func(string, ...any)
}

// say — строка в журнал. Молчащий подъём это `WORLD2-130` целиком, поэтому канал сюда
// заведён полем, а не выведен из глобального журнала: подменяемость — то, чем проба
// проверяет, что подъём и правда говорит.
func (m *Manager) say(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}

// ── список ───────────────────────────────────────────────────────────────────

// List — территории юзера с тем, что на них сейчас стоит. Список берётся ИЗ СКОУПА, а
// живое состояние спрашивается у машин: список — знание юзера, состояние — знание машины,
// и выводить одно из другого нельзя.
func (m *Manager) List(ctx context.Context, territories []state.Territory) []Resource {
	out := make([]Resource, 0, len(territories))
	for _, t := range territories {
		r := Resource{Name: t.Name, Addr: t.Addr}
		r.Reach, r.Things = m.things(ctx, ContextPrefix+t.Name)
		out = append(out, r)
	}
	return out
}

// things — что стоит на территории. Спрашиваем ДЕМОН той машины, а не свой список: какие
// вещи там подняты — знание той стороны. Вопросов два, и они разные: «отвечает ли машина»
// и «что на ней стоит». Вывести второй из первого нельзя — молчащая машина это не пустая
// машина (`WORLD2` 4.2).
func (m *Manager) things(ctx context.Context, dockerContext string) (string, []Thing) {
	res, err := m.Runner.Run(ctx, run.Command{Name: m.Docker, Args: m.args(dockerContext, "ps", "-a", "--format", "{{.ID}}")})
	if err != nil || res.Code != 0 {
		return "молчит", nil
	}
	ids := strings.Fields(res.Out)
	if len(ids) == 0 {
		return "отвечает", []Thing{}
	}

	// Один вопрос на все контейнеры сразу: `inspect` принимает их списком. Спрашиваем
	// ПОЛЯ, а не разбираем строку статуса («Up 2 minutes (healthy)»): разбор чужой
	// формулировки разъехался бы с ней на первой правке докера.
	args := m.args(dockerContext, "inspect", "--format",
		"{{index .Config.Labels \""+projectLabel+"\"}}\t"+
			"{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}\t{{.State.Status}}")
	res, err = m.Runner.Run(ctx, run.Command{Name: m.Docker, Args: append(args, ids...)})
	if err != nil || res.Code != 0 {
		// Контейнеры есть, а спросить их не вышло. Врать «ничего не стоит» нельзя, и
		// назвать вещи нечем — значит машина для нас молчит.
		return "молчит", nil
	}

	order := []string{}
	seen := map[string]*tally{}
	for _, line := range strings.Split(res.Out, "\n") {
		// Обрезаем СПРАВА, а не с обеих сторон: у контейнера без метки поле проекта пусто,
		// и снятая слева табуляция превратила бы его состояние в имя вещи.
		project, rest, ok := strings.Cut(strings.TrimRight(line, " \t\r"), "\t")
		if !ok || project == "" {
			// Контейнер, поднятый не компоузом, вещью мира не является: у него нет
			// рецепта, и назвать его нам нечем.
			continue
		}
		health, st, _ := strings.Cut(rest, "\t")
		t, known := seen[project]
		if !known {
			t = &tally{}
			seen[project] = t
			order = append(order, project)
		}
		t.add(health, st)
	}

	out := make([]Thing, 0, len(order))
	for _, name := range order {
		st, alive := seen[name].verdict()
		out = append(out, Thing{Name: name, State: st, Alive: alive})
	}
	return "отвечает", out
}

// args — общее начало команды докера. Пустой контекст означает «машина контроллера»: у
// своей машины контекст не спрашивается вовсе.
func (m *Manager) args(dockerContext string, rest ...string) []string {
	var args []string
	if dockerContext != "" {
		args = append(args, "--context", dockerContext)
	}
	return append(args, rest...)
}

// tally — контейнеры одной вещи, посчитанные по состояниям. Вещь не бывает целой по
// одному контейнеру: сколько их у неё — знает рецепт, и судить о ней по первому
// попавшемуся значило бы выдать часть за целое.
type tally struct{ stopped, sick, rising, healthy, unchecked int }

func (t *tally) add(health, state string) {
	if strings.TrimSpace(state) != "running" {
		t.stopped++
		return
	}
	switch strings.TrimSpace(health) {
	case "healthy":
		t.healthy++
	case "starting":
		t.rising++
	case "none", "":
		// У образа нет HEALTHCHECK-а. Это НЕ «здоров»: ждать нечего и спрашивать нечего —
		// приблизительная запись хуже отсутствующей. То же правило, что у соседа.
		t.unchecked++
	default:
		t.sick++
	}
}

// verdict — вещь целиком: словами и «отвечает ли». Порядок ступеней — от того, что чинят
// первым: не запущено · нездорово · поднимается · отвечает · отвечать нечему.
func (t *tally) verdict() (string, bool) {
	switch {
	case t.stopped > 0:
		return "запущена не вся", false
	case t.sick > 0:
		return "нездорова", false
	case t.rising > 0:
		return "поднимается", false
	case t.healthy > 0:
		return "здорова", true
	default:
		return "запущена, здоровья не спросить", false
	}
}

// ── подъём и снятие вещи ─────────────────────────────────────────────────────

// Raise — поднять НАЗВАННУЮ РЕЦЕПТОМ вещь на территории. Ключ к машине кладётся ДО вызова
// (`Bind`/`PutKey`): докер пойдёт по ssh сам и возьмёт его из связки — своего поля под
// ключ у контекста нет вовсе.
//
// `env` — значения, которые читает сам рецепт. Контроллер их не толкует: что вещи нужно,
// знает вещь, а не он.
func (m *Manager) Raise(ctx context.Context, name, addr, recipePath string, env []string) *refusal.Refusal {
	// ЧЕМ ПОДНИМАЕМ — решается здесь и НАЗЫВАЕТСЯ ВСЛУХ (`WORLD2-130`). Строка пишется при
	// каждом подъёме: и когда пин сработал, и когда версии нет, и когда имя названо
	// снаружи. Молчание тут и есть тот дефект, ради которого узел заведён.
	env = append(append([]string{}, env...), m.remoteEnv()...)
	p := m.pinFor(ctx, recipePath, env)
	m.say("control: вещь по рецепту %s на территории %s — %s", recipePath, name, p.say)
	env = append(env, p.env...)

	// Рецепт называется ВСЕГДА, даже когда он тот же самый, что у соседа по умолчанию.
	// Умолчание принадлежит ЕГО команде, а не нашему вызову: положись мы на него, смена
	// умолчания у соседа молча сменила бы вещь, которую поднимает контроллер.
	args := []string{"add", name, "--addr", addr, "--recipe", recipePath}
	res, err := m.Runner.Run(ctx, run.Command{
		Name: m.RemoteSh,
		Args: args,
		Env:  env,
	})
	if err != nil || res.Code != 0 {
		return m.toolFailure(err, res, "поставить вещь по рецепту "+recipePath+" на "+addr+" не вышло")
	}
	return nil
}

// Dropped — что осталось на той машине после снятия. Ответ обязан это называть: «снял» без
// перечня оставленного — это обещание чистоты, которого мы не давали.
type Dropped struct {
	Name    string   `json:"name"`
	Removed []string `json:"removed"`
	Left    []string `json:"left"`
	Ways    []string `json:"ways"`
	// Note — сказанное вслух про то, чего НЕ случилось. Пусто — значит сказать нечего.
	Note string `json:"note,omitempty"`
}

// Lower — снять вещь с территории. Состояние вещи и образ по умолчанию остаются: стереть
// их молча значило бы потерять то, что юзер клал не сюда и не сейчас.
//
// Рецепт называется и здесь: снимаем ТО ЖЕ, что ставили, а своего реестра вещей зона не
// заводит — «что мы там поднимали», помнит человек (тот же довод, что у соседа).
func (m *Manager) Lower(ctx context.Context, name, recipePath, recipeName string, withState, withImage bool) (Dropped, *refusal.Refusal) {
	args := []string{"drop", name, "--recipe", recipePath}
	if withState {
		args = append(args, "--with-state")
	}
	if withImage {
		args = append(args, "--with-image")
	}
	// Снимаем ТЕМ ЖЕ, чем ставили. Без пина `--with-image` унёс бы образ `latest` — не тот,
	// которым вещь поднята, — и человек получил бы «снял», не сняв. Строка в журнале здесь
	// по той же причине, что и при подъёме: чем действуем, говорится вслух.
	env := m.remoteEnv()
	p := m.pinFor(ctx, recipePath, env)
	m.say("control: снимаю вещь по рецепту %s с территории %s — %s", recipePath, name, p.say)
	env = append(env, p.env...)

	res, err := m.Runner.Run(ctx, run.Command{Name: m.RemoteSh, Args: args, Env: env})

	out := Dropped{
		Name:    name,
		Removed: []string{"контейнеры рецепта", "сеть мира, если в ней больше никого", "контекст докера", "ключ территории из связки контроллера"},
	}
	if err != nil || res.Code != 0 {
		ref := m.toolFailure(err, res, "снять вещь с территории «"+name+"» не вышло")
		// «Такого ресурса у меня нет» — это НЕ повод оставить участок в скоупе навсегда.
		// Иначе снятие, споткнувшееся один раз, запирало бы запись в личности насмерть:
		// вещи там уже нет, а убрать участок нечем. Говорим вслух, что вещь не трогали.
		if ref.Code == "no-such-resource" {
			out.Removed = nil
			out.Note = "вещи на этой территории подъём не нашёл — участок убран из скоупа, на машине ничего не тронуто"
			return out, nil
		}
		return Dropped{}, ref
	}

	query := "?recipe=" + recipeName
	if recipeName == "" {
		query = ""
	}
	if withState {
		out.Removed = append(out.Removed, "тома рецепта — состояние вещи стёрто")
	} else {
		out.Left = append(out.Left, "тома рецепта — состояние вещи, оно переживает снятие")
		out.Ways = append(out.Ways, "снять и его: DELETE /api/resources/"+name+with(query, "with-state=1"))
	}
	if withImage {
		out.Removed = append(out.Removed, "образ, названный рецептом")
	} else {
		out.Left = append(out.Left, "образ, названный рецептом")
		out.Ways = append(out.Ways, "снять и его: DELETE /api/resources/"+name+with(query, "with-image=1"))
	}
	if len(out.Left) == 0 {
		out.Ways = append(out.Ways, "на той машине нашего не осталось ничего, кроме докера и ssh — их ставил не мир")
	}
	return out, nil
}

// with — приписать значение к пути ручки, не потеряв уже названный рецепт. Выход обязан
// быть КОМАНДОЙ, которую можно повторить как есть: потеряй он рецепт, вторая попытка
// сняла бы не ту вещь.
func with(query, param string) string {
	if query == "" {
		return "?" + param
	}
	return query + "&" + param
}

func (m *Manager) remoteEnv() []string {
	if m.Port <= 0 {
		return nil
	}
	// Имя переменной — то, каким её ждёт сосед (`WORLD_PORT` в deploy/remote.sh).
	return []string{"WORLD_PORT=" + strconv.Itoa(m.Port)}
}

// ── времянки контроллера: ключи, config, контексты ───────────────────────────
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ВСЁ, ЧТО В ЭТОМ РАЗДЕЛЕ, — ПРОИЗВОДНОЕ ОТ СКОУПА, А НЕ СОСТОЯНИЕ (`WORLD2` 1.9).     │
// │                                                                                      │
// │ Ключи в связке, блоки в `config` и контексты докера поднимаются из скоупа при ВХОДЕ   │
// │ и снимаются при ВЫХОДЕ. Своего списка контроллер не держит: держал бы — вошедший под  │
// │ другой личностью увидел бы чужие территории, и «личность» перестала бы что-то значить.│
// └─────────────────────────────────────────────────────────────────────────────────────┘

// Bind — привести времянки контроллера в соответствие со скоупом. Делается при входе, и
// делается целиком: сначала снимаем всё своё, потом раскладываем то, что лежит в скоупе.
// Так вход под другой личностью не оставляет ни одного следа прежней.
func (m *Manager) Bind(ctx context.Context, st *state.State) *refusal.Refusal {
	if ref := m.Unbind(ctx); ref != nil {
		return ref
	}
	for _, t := range st.Territories {
		if ref := validName(t.Name); ref != nil {
			return ref
		}
		host, port, ref := checkAddr(t.Addr)
		if ref != nil {
			return ref
		}
		if t.Key != "" {
			key, found := st.Key(t.Key)
			if !found {
				return refusal.New(http.StatusBadGateway, "scope-broken",
					fmt.Sprintf("участок «%s» ссылается на ключ «%s», а такого ключа в связке скоупа нет", t.Name, t.Key),
					"поправь файл состояния: ключи лежат в разделе «ключи», а территории ссылаются на них по имени (`WORLD2` 3.4)",
					"или заведи участок заново: DELETE /api/resources/"+t.Name+", затем POST /api/resources")
			}
			if ref := m.putKey(t.Name, host, key.Value); ref != nil {
				return ref
			}
		}
		if ref := m.putContext(ctx, t.Name, t.Addr, host, port); ref != nil {
			return ref
		}
	}
	return nil
}

// Unbind — снять всё своё: контексты, ключи, блоки в `config`. Делается при выходе и перед
// каждым входом. Чужие контексты и чужие строки в `config` не трогаются: связка принадлежит
// юзеру, мы в ней гости, а машина — хозяину.
func (m *Manager) Unbind(ctx context.Context) *refusal.Refusal {
	for _, name := range m.ours(ctx) {
		res, err := m.Runner.Run(ctx, run.Command{Name: m.Docker, Args: []string{"context", "rm", "-f", name}})
		if err != nil || res.Code != 0 {
			return refusal.New(http.StatusBadGateway, "context-failed",
				fmt.Sprintf("контекст докера %s не снять: %s", name, tail(res.Err)),
				"посмотри, что мешает: docker context rm -f "+name,
				"контексты контроллера — времянка: они поднимаются из скоупа при входе")
		}
	}
	if m.KeysDir == "" {
		return nil
	}
	entries, err := os.ReadDir(m.KeysDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return refusal.New(http.StatusInternalServerError, "no-keyring",
			"связку контроллера не прочитать: "+err.Error(),
			"проверь том, смонтированный под связку: control/README.md")
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), ContextPrefix) {
			continue
		}
		if err := os.Remove(filepath.Join(m.KeysDir, e.Name())); err != nil && !os.IsNotExist(err) {
			return refusal.New(http.StatusInternalServerError, "no-keyring",
				fmt.Sprintf("ключ %s из связки не снялся: %v", e.Name(), err),
				"убери его руками: "+filepath.Join(m.KeysDir, e.Name()))
		}
		if ref := m.dropConfigBlock(strings.TrimPrefix(e.Name(), ContextPrefix)); ref != nil {
			return ref
		}
	}
	return nil
}

// ours — контексты докера, заведённые нами. Узнаём их по приставке — той же, что у соседа.
// Докера может не быть вовсе (девбокс, проба): тогда снимать нечего, и это не отказ.
func (m *Manager) ours(ctx context.Context) []string {
	res, err := m.Runner.Run(ctx, run.Command{Name: m.Docker, Args: []string{"context", "ls", "--format", "{{.Name}}"}})
	if err != nil || res.Code != 0 {
		return nil
	}
	var out []string
	for _, line := range strings.Split(res.Out, "\n") {
		name := strings.TrimSpace(line)
		if strings.HasPrefix(name, ContextPrefix) {
			out = append(out, name)
		}
	}
	return out
}

// putContext — контекст докера на территорию. Формула адреса — та же, что у соседа
// (`docker_endpoint` в deploy/remote.sh): один разбор, две сборки, разъехаться нечему.
// Это ШОВ, и он стережётся пробой.
func (m *Manager) putContext(ctx context.Context, name, addr, host string, port int) *refusal.Refusal {
	endpoint := "ssh://" + userAt(addr, host) + ":" + strconv.Itoa(port)
	full := ContextPrefix + name

	res, err := m.Runner.Run(ctx, run.Command{Name: m.Docker, Args: []string{"context", "inspect", full}})
	verb := "create"
	if err == nil && res.Code == 0 {
		verb = "update"
	}
	res, err = m.Runner.Run(ctx, run.Command{
		Name: m.Docker,
		Args: []string{"context", verb, full, "--description", "мир: территория " + name, "--docker", "host=" + endpoint},
	})
	if err != nil {
		if errors.Is(err, run.ErrNoTool) {
			return refusal.New(http.StatusInternalServerError, "no-docker",
				"докера в контроллере нет, а до территорий он ходит контекстами докера",
				"это дефект образа контроллера — заведи задачу зоне control")
		}
		return refusal.New(http.StatusGatewayTimeout, "no-daemon",
			fmt.Sprintf("докер не ответил, когда заводили контекст %s: %v", full, err),
			"проверь, что сокет докера отдан контроллеру при подъёме: control/README.md")
	}
	if res.Code != 0 {
		return refusal.New(http.StatusBadGateway, "context-failed",
			fmt.Sprintf("контекст докера %s на %s не завести: %s", full, endpoint, tail(res.Err)),
			"посмотри, что мешает: docker context "+verb+" "+full+" --docker host="+endpoint,
			"имя занято чем-то чужим — возьми другое имя территории")
	}
	return nil
}

// userAt — «юзер@машина» из адреса территории. Адрес разобран уже дважды (нами и соседом),
// и третьего разбора здесь нет: берём то, что до двоеточия, и подставляем разобранный хост.
func userAt(addr, host string) string {
	if user, _, ok := strings.Cut(addr, "@"); ok {
		return user + "@" + host
	}
	return host
}

// putKey кладёт ключ территории туда, откуда его возьмёт ssh (а через ssh — докер). Мир
// кред НЕ ЗАВОДИТ и не выдаёт: он кладёт то, что назвал юзер и что лежит в его скоупе
// (`WORLD2` 3.0).
func (m *Manager) putKey(name, host, creds string) *refusal.Refusal {
	if m.KeysDir == "" {
		return refusal.New(http.StatusInternalServerError, "no-keyring",
			"контроллеру некуда положить ключ: связка не названа",
			"это дефект подъёма контроллера — см. control/README.md, CONTROL_KEYS")
	}
	if err := os.MkdirAll(m.KeysDir, 0o700); err != nil {
		return refusal.New(http.StatusInternalServerError, "no-keyring",
			fmt.Sprintf("связка контроллера %s не завелась: %v", m.KeysDir, err),
			"проверь том, смонтированный под связку: control/README.md")
	}
	if !strings.HasSuffix(creds, "\n") {
		// Ключ без завершающего перевода строки ssh не принимает и говорит об этом
		// «invalid format» — отказ про формат вместо отказа про ключ.
		creds += "\n"
	}
	if err := os.WriteFile(m.keyPath(name), []byte(creds), 0o600); err != nil {
		return refusal.New(http.StatusInternalServerError, "no-keyring",
			fmt.Sprintf("ключ территории не записался: %v", err),
			"проверь права на связку контроллера")
	}
	return m.writeConfigBlock(name, host)
}

// PutKey — положить ключ до того, как скоуп о нём знает. Нужен ровно в одном месте: когда
// территорию ЗАВОДЯТ, ключ должен лежать в связке ещё до подъёма вещи — иначе ssh не
// пустит, и отказ приедет про несуществующий ключ.
func (m *Manager) PutKey(name, addr, creds string) *refusal.Refusal {
	host, _, ref := checkAddr(addr)
	if ref != nil {
		return ref
	}
	return m.putKey(name, host, creds)
}

// DropKey — снять ключ территории и её блок в `config`. Зовётся, когда подъём не удался:
// оставленный ключ означал бы, что вторая попытка пойдёт кредами, которых юзер уже не
// называет, и отказ «ключ не принят» приехал бы про ключ-призрак.
func (m *Manager) DropKey(name string) {
	if m.KeysDir == "" {
		return
	}
	_ = os.Remove(m.keyPath(name))
	_ = m.dropConfigBlock(name)
}

func (m *Manager) keyPath(name string) string {
	return filepath.Join(m.KeysDir, ContextPrefix+name)
}

// blockMarks — границы нашего блока в `config`. По имени территории: имя — это то, чем
// юзер её зовёт, и снятие обязано убирать ровно свой блок, а не «похожий».
func blockMarks(name string) (string, string) {
	return "# >>> world " + name, "# <<< world " + name
}

func (m *Manager) writeConfigBlock(name, host string) *refusal.Refusal {
	open, end := blockMarks(name)
	block := fmt.Sprintf(`%s — поставил контроллер, снимается при выходе из скоупа
Host %s
    HostName %s
    IdentityFile %s
    IdentitiesOnly yes
    StrictHostKeyChecking accept-new
%s
`, open, host, host, m.keyPath(name), end)

	rest, ref := m.configWithout(name)
	if ref != nil {
		return ref
	}
	if err := os.WriteFile(m.configPath(), []byte(rest+block), 0o600); err != nil {
		return refusal.New(http.StatusInternalServerError, "no-keyring",
			fmt.Sprintf("связка контроллера не записалась: %v", err),
			"проверь права на связку контроллера")
	}
	return nil
}

func (m *Manager) dropConfigBlock(name string) *refusal.Refusal {
	rest, ref := m.configWithout(name)
	if ref != nil {
		return ref
	}
	if rest == "" {
		if err := os.Remove(m.configPath()); err != nil && !os.IsNotExist(err) {
			return refusal.New(http.StatusInternalServerError, "no-keyring",
				fmt.Sprintf("связка контроллера не почистилась: %v", err),
				"убери блок руками: "+m.configPath())
		}
		return nil
	}
	if err := os.WriteFile(m.configPath(), []byte(rest), 0o600); err != nil {
		return refusal.New(http.StatusInternalServerError, "no-keyring",
			fmt.Sprintf("связка контроллера не переписалась: %v", err),
			"убери блок руками: "+m.configPath())
	}
	return nil
}

func (m *Manager) configPath() string { return filepath.Join(m.KeysDir, "config") }

// configWithout — содержимое `config` без нашего блока про эту территорию. Чужие строки в
// файле остаются нетронутыми: связка принадлежит юзеру, мы в ней только гости.
func (m *Manager) configWithout(name string) (string, *refusal.Refusal) {
	data, err := os.ReadFile(m.configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", refusal.New(http.StatusInternalServerError, "no-keyring",
			fmt.Sprintf("связка контроллера не прочиталась: %v", err),
			"проверь права на связку контроллера")
	}

	open, end := blockMarks(name)
	var kept []string
	inside := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, open):
			inside = true
		case inside && strings.HasPrefix(line, end):
			inside = false
		case !inside:
			kept = append(kept, line)
		}
	}
	body := strings.TrimRight(strings.Join(kept, "\n"), "\n")
	if body != "" {
		body += "\n"
	}
	return body, nil
}

// ── отказы ───────────────────────────────────────────────────────────────────

// ValidName — имя территории, проверенное до всякого докера. Имя даёт юзер (`2.5` п. 11),
// а идёт оно в имя контекста и в имя файла ключа: имя с косой чертой увело бы запись из
// связки в чужой каталог.
func ValidName(name string) *refusal.Refusal { return validName(name) }

func validName(name string) *refusal.Refusal {
	if name == "" {
		return refusal.New(http.StatusBadRequest, "no-name",
			"имя территории не названо, а мир его не выдумывает и из адреса машины не выводит",
			"назови короткое своё: vps, дом-рядом → dom-ryadom",
			"на имени стоит адрес локации — оно часть адреса, а не подпись (`WORLD2` 2.5 п. 11)")
	}
	if !nameRe.MatchString(name) {
		return refusal.New(http.StatusBadRequest, "bad-name",
			fmt.Sprintf("имя %q не подходит: имя территории идёт в имя контекста докера и в имя файла ключа", name),
			"возьми строчные латинские буквы, цифры и дефис: vps, home, box-2")
	}
	return nil
}

// CheckAddr — адрес машины ровно настолько, насколько его касается контроллер: юзер назван,
// машина названа. Разбирать его дальше не наше дело — адрес уезжает соседу как есть.
func CheckAddr(addr string) (string, int, *refusal.Refusal) { return checkAddr(addr) }

// SplitAddr — то же самое, но с юзером. Нужен там, где до машины дотягиваются НЕ докером:
// заход паролем идёт своей библиотекой, и юзера ей надо назвать отдельно. Разбор при этом
// один и тот же — второй разъехался бы с первым.
func SplitAddr(addr string) (string, string, int, *refusal.Refusal) {
	host, port, ref := checkAddr(addr)
	if ref != nil {
		return "", "", 0, ref
	}
	user, _, _ := strings.Cut(strings.TrimSpace(addr), "@")
	return user, host, port, nil
}

func checkAddr(addr string) (string, int, *refusal.Refusal) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", 0, refusal.New(http.StatusBadRequest, "no-address",
			"адрес машины не назван — угадать его нельзя",
			"назови: user@10.8.0.5 или user@10.8.0.5:2222")
	}
	user, hostPort, ok := strings.Cut(addr, "@")
	if !ok || user == "" {
		// Пустой юзер ssh проглатывает молча и идёт текущим — человек назвал одного, а
		// пошли бы другим (грабля `WORLD2-96`, пункт 4).
		return "", 0, refusal.New(http.StatusBadRequest, "bad-address",
			fmt.Sprintf("в адресе %q не назван юзер", addr),
			"назови целиком: user@10.8.0.5")
	}
	host, port, hasPort := strings.Cut(hostPort, ":")
	if host == "" {
		return "", 0, refusal.New(http.StatusBadRequest, "bad-address",
			fmt.Sprintf("в адресе %q не названа машина", addr),
			"назови целиком: user@10.8.0.5")
	}
	sshPort := 22
	if hasPort {
		n, err := strconv.Atoi(port)
		if err != nil || n <= 0 || n > 65535 {
			return "", 0, refusal.New(http.StatusBadRequest, "bad-address",
				fmt.Sprintf("в адресе %q после двоеточия стоит %q — это не порт ssh", addr, port),
				"порт — число: user@10.8.0.5:2222")
		}
		sshPort = n
	}
	return host, sshPort, nil
}

// toolFailure — отказ соседнего инструмента, довезённый до человека БЕЗ перевода.
// `deploy/remote.sh` печатает машинный код в stdout (`REMOTE-REFUSAL: no-docker`), а
// причину и выходы — в stderr. Мы забираем ровно это: свой словарь тех же отказов
// разъехался бы с его словарём на первой правке.
func (m *Manager) toolFailure(err error, res run.Result, what string) *refusal.Refusal {
	if errors.Is(err, run.ErrNoTool) {
		return refusal.New(http.StatusInternalServerError, "no-remote-tool",
			"подъём вещи (deploy/remote.sh) до контроллера не доехал — звать нечего",
			"это дефект образа контроллера: путь называется CONTROL_REMOTE_SH",
			"см. control/README.md, раздел «что лежит в образе»")
	}
	if err != nil {
		return refusal.New(http.StatusGatewayTimeout, "remote-silent",
			what+": подъём не ответил за отведённое время",
			"дай больше времени: CONTROL_TOOL_TIMEOUT=300",
			"или прогони его руками: "+m.RemoteSh+" list")
	}

	code := firstMatch(res.Out, "REMOTE-REFUSAL: ")
	why := strings.TrimSpace(strip(firstMatch(res.Err, "отказ:")))
	ways := waysFrom(res.Err)
	if code == "" {
		code = "remote-failed"
	}
	if why == "" {
		why = what + "; подъём вышел с кодом " + strconv.Itoa(res.Code) + ": " + tail(res.Err)
	}
	return refusal.FromTool(http.StatusBadGateway, "deploy/remote.sh", code, why, ways...)
}

// firstMatch — то, что стоит после метки в первой строке, где она встретилась.
func firstMatch(text, mark string) string {
	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, mark); i >= 0 {
			return strings.TrimSpace(strip(line[i+len(mark):]))
		}
	}
	return ""
}

// waysFrom — выходы, названные соседом. Он печатает их строками «  выход: …», и это его
// слова про его же отказ: точнее наших они по определению.
func waysFrom(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, "выход: "); i >= 0 {
			if way := strings.TrimSpace(strip(line[i+len("выход: "):])); way != "" {
				out = append(out, way)
			}
		}
	}
	return out
}

// strip убирает цветовые последовательности: сосед печатает человеку в терминал, а мы
// везём его слова в JSON, где escape-последовательности — мусор.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func strip(s string) string { return ansiRe.ReplaceAllString(s, "") }

func tail(s string) string {
	lines := strings.Split(strings.TrimSpace(strip(s)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return "(инструмент промолчал)"
}
