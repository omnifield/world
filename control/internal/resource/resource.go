// Пакет resource — источники ресурса: машины, до которых юзер дотянулся.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ЭТОТ ПАКЕТ НЕ ПОДНИМАЕТ ВЕЩЬ. Подъём вещи на названном ресурсе уже написан и лежит   │
// │ в `deploy/remote.sh` (контекст докера по ssh, `WORLD2-99`). Контроллер его ЗОВЁТ.    │
// │ Второй подъём того же самого разъехался бы с первым на первой правке соседа — и      │
// │ человек получил бы две двери, которые ставятся по-разному.                            │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ РЕСУРС — ЭТО МАШИНА, А НЕ «МАШИНА С ДВЕРЬЮ» (`WORLD2` 3.7, `WORLD2-131`).            │
// │                                                                                      │
// │ Что на ней стоит — отдельный вопрос и отдельный ответ: СПИСОК поднятых вещей, а не    │
// │ одно поле про дверь. Пока здесь жило `Door string`, зона знала ровно одну вещь мира,  │
// │ и вторая потребовала бы правки кода — значит вещь была зашита, а не описана.          │
// │                                                                                      │
// │ Имён контейнеров, образов и портов у зоны поэтому нет вовсе: они принадлежат рецепту, │
// │ и знать их контроллеру неоткуда. Чем поднимать — говорит рецепт (`internal/recipe`).  │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// Своего реестра ресурсов зона тоже НЕ заводит. Список — это контексты докера, ровно те
// же, что читает `deploy/remote.sh list`: один источник истины, а не два списка одного и
// того же (довод целиком — в шапке `deploy/remote.sh`). Мы читаем ту же истину, а не
// копируем её к себе. Список ВЕЩЕЙ на ресурсе читается там же, где его читает сосед, — по
// метке проекта, которую компоуз ставит на всё, что заводит сам.
//
// Что здесь ЕСТЬ своего и чего нет у соседа:
//
//   - креды. `docker context` поля для ключа не имеет вовсе, а юзер называет ключ, когда
//     добавляет ресурс. Контроллер кладёт ключ в свою связку и приписывает его к машине в
//     `~/.ssh/config` — то самое место, откуда ssh (а значит и докер) его берёт;
//   - состояние одной строкой на весь список: пульту нужен список с состоянием, а не
//     четырнадцать строк вывода `status` на каждый ресурс;
//   - ресурс «здесь» — тот, на котором стоит сам контроллер. У соседа его нет и быть не
//     может: он ведёт список ЧУЖИХ ресурсов, а свой контроллер знает про себя сам.
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
)

const (
	// ContextPrefix — приставка имени контекста докера (`PREFIX` в deploy/remote.sh).
	// Значение ПРИНАДЛЕЖИТ зоне `deploy` и здесь повторено: по нему мы узнаём свои
	// контексты среди чужих. Повтор — это шов, и он стережётся пробой (`probe-control.sh`
	// читает `deploy/remote.sh` и краснеет, если там стало другое). Молчаливый повтор
	// чужой константы — то, что разъезжается тише всего.
	//
	// Больше повторов у зоны нет: имя контейнера, образ и порт принадлежат РЕЦЕПТУ, а не
	// соседу и не нам (`WORLD2-131`).
	ContextPrefix = "world-"
	// HereName — имя ресурса, на котором стоит сам контроллер. Занятое имя: завести
	// второй «здесь» нельзя, иначе список начнёт врать про то, где мы стоим.
	HereName = "here"
	// projectLabel — метка компоуза, по которой видно, ЧЬЁ это хозяйство. Ставит её сам
	// компоуз на всё, что заводит (контейнеры, тома, сети), и читает её же сосед
	// (`deploy/remote.sh`, `thing_volumes` и `status`). Это правило докера, а не наше
	// знание о вещах: имя вещи здесь читается, а не перечисляется.
	projectLabel = "com.docker.compose.project"
)

// nameRe — имя ресурса. Правило то же, что у соседа (`bad-name` в deploy/remote.sh), и
// проверяем мы его ДО вызова не ради второй проверки, а потому что имя идёт в ИМЯ ФАЙЛА
// ключа в нашей связке: имя с косой чертой увело бы запись из связки в чужой каталог.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// Thing — вещь, поднятая на ресурсе. Имя — имя проекта компоуза: им помечено всё, что
// вещь на той машине завела, и его же называет рецепт. Своего перечня вещей у зоны нет.
type Thing struct {
	Name string `json:"name"`
	// State — что видно про вещь словами: «здорова», «поднимается», «нездорова»,
	// «запущена не вся», «запущена, здоровья не спросить». Измеренное, а не выведенное:
	// состояние, выведенное вместо измеренного, врёт ровно тогда, когда на него смотрят
	// (`WORLD2` 4.2).
	State string `json:"state"`
	// Alive — вещь ОТВЕЧАЕТ, а не «запущена». Разные вещи: запуск подтверждает докер,
	// ответ — HEALTHCHECK изнутри контейнера. У вещи без HEALTHCHECK-а ответ не спросить
	// вовсе, и тогда здесь `false` — не «мертва», а «не подтверждено», и это сказано
	// словами в `State`.
	Alive bool `json:"alive"`
}

// Resource — источник ресурса глазами юзера: машина, до которой дотянулись. Ни памяти, ни
// ядер здесь нет: ресурс в цифрах даёт отдельный инструмент осмотра, и он сознательно
// отложен (`WORLD2` 2.5).
type Resource struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
	// Here — на этом ресурсе стоит контроллер.
	Here bool `json:"here"`
	// Reach — отвечает ли САМ ресурс: «отвечает» либо «молчит». Вопрос про машину, а не
	// про вещь на ней, и ответ на него не выводится из другого поля.
	Reach string `json:"reach"`
	// Things — что на ресурсе поднято. `null` означает «не спросили» (ресурс молчит), а
	// пустой список — «спросили, и там ничего нет». Разные ответы: пустой список вместо
	// «не спросили» читался бы как знание, которого у нас нет (`WORLD2` 4.2).
	Things []Thing `json:"things"`
}

// Manager — работа с источниками ресурса.
type Manager struct {
	Runner run.Runner
	// RemoteSh — путь к готовому подъёму вещи. Значение, а не константа: в образе
	// контроллера он лежит по своему пути, в девбоксе — по своему, а проба подменяет его
	// заглушкой, чтобы проверить ПОВЕДЕНИЕ там, где докера нет.
	RemoteSh string
	// Recipes — где контроллер берёт рецепты. Чем поднимать вещь, знает рецепт, а какие
	// рецепты есть — каталог; в коде их перечня нет (`internal/recipe`).
	Recipes *recipe.Catalog
	// Docker — имя докер-клиента. Подменяется пробой по той же причине.
	Docker string
	// KeysDir — связка контроллера (`~/.ssh` внутри его образа): ключи ресурсов и
	// `config`, из которого их берёт ssh.
	KeysDir string
	// Port — хост-порт двери на том ресурсе; едет в `deploy/remote.sh` тем же именем,
	// каким тот его ждёт. Рецепт, который его не читает, о нём и не узнает.
	Port int
}

// List — все источники ресурса: «здесь» плюс заведённые.
func (m *Manager) List(ctx context.Context) ([]Resource, *refusal.Refusal) {
	out := []Resource{m.here(ctx)}

	res, err := m.Runner.Run(ctx, run.Command{
		Name: m.Docker,
		Args: []string{"context", "ls", "--format", "{{.Name}}\t{{.DockerEndpoint}}"},
	})
	if ref := m.dockerFailure(err); ref != nil {
		return nil, ref
	}
	if res.Code != 0 {
		return nil, refusal.New(http.StatusBadGateway, "no-daemon",
			"докер на этой машине не отвечает, а список ресурсов — это его контексты",
			"проверь, что сокет докера отдан контроллеру при подъёме: control/README.md",
			"подробность: "+tail(res.Err))
	}

	for _, line := range strings.Split(res.Out, "\n") {
		name, endpoint, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || !strings.HasPrefix(name, ContextPrefix) {
			continue
		}
		short := strings.TrimPrefix(name, ContextPrefix)
		r := Resource{Name: short, Addr: strings.TrimPrefix(endpoint, "ssh://")}
		r.Reach, r.Things = m.things(ctx, name)
		out = append(out, r)
	}
	return out, nil
}

// here — ресурс, на котором стоит контроллер. Адреса у него нет намеренно: изнутри
// машины её собственный адрес неизвестен, а выдуманный («localhost») однажды уедет в
// пульт и станет ложью для того, кто смотрит снаружи.
func (m *Manager) here(ctx context.Context) Resource {
	r := Resource{Name: HereName, Here: true}
	r.Reach, r.Things = m.things(ctx, "")
	return r
}

// things — что стоит на ресурсе. Пустой контекст означает «здесь».
//
// Спрашиваем ДЕМОН той машины, а не свой список: какие вещи там подняты — знание той
// стороны, и у нас его взять неоткуда. Вопросов два, и они разные: «отвечает ли ресурс» и
// «что на нём стоит». Вывести второй из первого нельзя — молчащий ресурс это не пустая
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
		// назвать вещи нечем — значит ресурс для нас молчит.
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
			// рецепта, и назвать его нам нечем. Чужое хозяйство хозяина машины мы не
			// перечисляем — оно не наше и не про нас.
			continue
		}
		health, state, _ := strings.Cut(rest, "\t")
		t, known := seen[project]
		if !known {
			t = &tally{}
			seen[project] = t
			order = append(order, project)
		}
		t.add(health, state)
	}

	out := make([]Thing, 0, len(order))
	for _, name := range order {
		state, alive := seen[name].verdict()
		out = append(out, Thing{Name: name, State: state, Alive: alive})
	}
	return "отвечает", out
}

// args — общее начало команды докера. Пустой контекст означает «здесь»: у своей машины
// контекст не спрашивается вовсе.
func (m *Manager) args(dockerContext string, rest ...string) []string {
	var args []string
	if dockerContext != "" {
		args = append(args, "--context", dockerContext)
	}
	return append(args, rest...)
}

// tally — контейнеры одной вещи, посчитанные по состояниям. Вещь не бывает целой по
// одному контейнеру: сколько их у неё и какие — знает рецепт, и судить о ней по первому
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
		// приблизительная запись хуже отсутствующей. То же правило и та же развилка, что
		// у соседа в `deploy/remote.sh`.
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

// Add — добавить ресурс: креды в связку, дальше готовый подъём НАЗВАННОЙ РЕЦЕПТОМ вещи.
// Рецепт не назван — берётся дверь: прежний путь («поставь дверь на ресурс») остаётся
// запросом без единого лишнего поля.
func (m *Manager) Add(ctx context.Context, name, addr, creds, recipeName string) (Resource, *refusal.Refusal) {
	if ref := validName(name); ref != nil {
		return Resource{}, ref
	}
	host, ref := checkAddr(addr)
	if ref != nil {
		return Resource{}, ref
	}
	// Рецепт находим ДО того, как тронули связку и ресурс: неизвестное имя обязано
	// отказать, не оставив за собой ни ключа, ни контекста (`WORLD2` 2.3).
	recipePath, ref := m.recipe(recipeName)
	if ref != nil {
		return Resource{}, ref
	}

	// Ключ кладётся ДО подъёма: докер пойдёт по ssh сам и возьмёт его из связки — своего
	// поля под ключ у контекста нет вовсе (см. шапку deploy/remote.sh).
	installed := false
	if creds != "" {
		if ref := m.installKey(name, host, creds); ref != nil {
			return Resource{}, ref
		}
		installed = true
	}

	// Рецепт называется ВСЕГДА, даже когда он тот же самый, что у соседа по умолчанию.
	// Умолчание принадлежит ЕГО команде, а не нашему вызову: положись мы на него, смена
	// умолчания у соседа молча сменила бы вещь, которую поднимает контроллер.
	args := []string{"add", name, "--addr", addr, "--recipe", recipePath}
	res, err := m.Runner.Run(ctx, run.Command{
		Name: m.RemoteSh,
		Args: args,
		Env:  m.remoteEnv(),
	})
	if err != nil || res.Code != 0 {
		// Подъём не удался — свой след убираем за собой. Оставленный ключ означал бы,
		// что вторая попытка пойдёт кредами, которых юзер уже не называет, и отказ
		// «ключ не принят» приехал бы про ключ-призрак.
		if installed {
			_ = m.removeKey(name)
		}
		return Resource{}, m.toolFailure(err, res, "поставить вещь по рецепту "+recipePath+" на "+addr+" не вышло")
	}

	out := Resource{Name: name, Addr: addr}
	out.Reach, out.Things = m.things(ctx, ContextPrefix+name)
	return out, nil
}

// recipe — путь рецепта по имени, названному человеком. Каталога рецептов может не быть
// вовсе (зона его не заводит — это ландшафт машины), и тогда остаётся дверь.
func (m *Manager) recipe(name string) (string, *refusal.Refusal) {
	if m.Recipes == nil {
		return "", refusal.New(http.StatusInternalServerError, "no-recipes",
			"контроллеру не назвали, где брать рецепты, — поднимать нечем",
			"это дефект подъёма контроллера: см. control/README.md, CONTROL_RECIPES")
	}
	return m.Recipes.Find(name)
}

// Dropped — что осталось на той машине после снятия. Ответ обязан это называть: «снял» без
// перечня оставленного — это обещание чистоты, которого мы не давали.
type Dropped struct {
	Name    string   `json:"name"`
	Removed []string `json:"removed"`
	Left    []string `json:"left"`
	Ways    []string `json:"ways"`
}

// Drop — снять ресурс. Состояние поля и образ по умолчанию остаются: стереть их молча
// значило бы потерять то, что юзер клал не сюда и не сейчас.
//
// Рецепт называется и здесь: снимаем ТО ЖЕ, что ставили, а своего реестра вещей зона не
// заводит — «что мы там поднимали», помнит человек (тот же довод, что у соседа).
func (m *Manager) Drop(ctx context.Context, name string, withState, withImage bool, recipeName string) (Dropped, *refusal.Refusal) {
	if ref := validName(name); ref != nil {
		return Dropped{}, ref
	}
	if name == HereName {
		return Dropped{}, refusal.New(http.StatusConflict, "drop-here",
			"«здесь» — это ресурс, на котором стоит сам контроллер; снять его контроллером нельзя",
			"вещи на этом ресурсе снимаются своим подъёмом: ./deploy/up.sh down",
			"сам контроллер снимается руками того, кто его ставил: ./control/up.sh down")
	}
	recipePath, ref := m.recipe(recipeName)
	if ref != nil {
		return Dropped{}, ref
	}

	args := []string{"drop", name, "--recipe", recipePath}
	if withState {
		args = append(args, "--with-state")
	}
	if withImage {
		args = append(args, "--with-image")
	}
	res, err := m.Runner.Run(ctx, run.Command{Name: m.RemoteSh, Args: args, Env: m.remoteEnv()})
	if err != nil || res.Code != 0 {
		return Dropped{}, m.toolFailure(err, res, "снять ресурс «"+name+"» не вышло")
	}

	// Ключ ресурса снимается вместе с ним: связка контроллера — это тоже след, и
	// оставленный ключ пережил бы ресурс, к которому он был.
	if ref := m.removeKey(name); ref != nil {
		return Dropped{}, ref
	}

	// Перечисляем то, что снято, БЕЗ имён: имена контейнеров, томов и образа принадлежат
	// рецепту, и знать их контроллеру неоткуда. Названное наугад имя выглядело бы знанием
	// и разъехалось бы с рецептом молча (`WORLD2` 4.2).
	out := Dropped{
		Name:    name,
		Removed: []string{"контейнеры рецепта", "сеть мира, если в ней больше никого", "контекст докера", "ключ ресурса из связки контроллера"},
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

// ── связка ключей контроллера ────────────────────────────────────────────────
//
// Здесь единственное место, где контроллер трогает креды. Он их НЕ ЗАВОДИТ и не выдаёт —
// он кладёт то, что назвал юзер, туда, откуда это возьмёт ssh (`WORLD2` 3.0: креды
// принадлежат юзеру и его связке). Блок в `config` помечен именем ресурса, чтобы снятие
// убирало ровно свой блок, а не «похожий».

func (m *Manager) keyPath(name string) string {
	return filepath.Join(m.KeysDir, ContextPrefix+name)
}

func (m *Manager) installKey(name, host, creds string) *refusal.Refusal {
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
			fmt.Sprintf("ключ ресурса не записался: %v", err),
			"проверь права на связку контроллера")
	}
	return m.writeConfigBlock(name, host)
}

// blockMarks — границы нашего блока в `config`. По имени ресурса, а не по хосту: на одной
// машине может стоять один ресурс, но имя — это то, чем юзер его зовёт.
func blockMarks(name string) (string, string) {
	return "# >>> world " + name, "# <<< world " + name
}

func (m *Manager) writeConfigBlock(name, host string) *refusal.Refusal {
	open, end := blockMarks(name)
	block := fmt.Sprintf(`%s — поставил контроллер, снимается вместе с ресурсом
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
	body := rest + block
	if err := os.WriteFile(m.configPath(), []byte(body), 0o600); err != nil {
		return refusal.New(http.StatusInternalServerError, "no-keyring",
			fmt.Sprintf("связка контроллера не записалась: %v", err),
			"проверь права на связку контроллера")
	}
	return nil
}

func (m *Manager) configPath() string { return filepath.Join(m.KeysDir, "config") }

// configWithout — содержимое `config` без нашего блока про этот ресурс. Чужие строки в
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

func (m *Manager) removeKey(name string) *refusal.Refusal {
	if m.KeysDir == "" {
		return nil
	}
	if err := os.Remove(m.keyPath(name)); err != nil && !os.IsNotExist(err) {
		return refusal.New(http.StatusInternalServerError, "no-keyring",
			fmt.Sprintf("ключ ресурса не снялся: %v", err),
			"убери его руками из связки контроллера: "+m.keyPath(name))
	}
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

// ── отказы ───────────────────────────────────────────────────────────────────

func validName(name string) *refusal.Refusal {
	if name == "" {
		return refusal.New(http.StatusBadRequest, "no-name",
			"имя ресурса не названо, а по нему потом искать и снимать",
			"назови короткое своё: vps, дом-рядом → dom-ryadom")
	}
	if !nameRe.MatchString(name) {
		return refusal.New(http.StatusBadRequest, "bad-name",
			fmt.Sprintf("имя %q не подходит: имя ресурса идёт в имя контекста докера и в имя файла ключа", name),
			"возьми строчные латинские буквы, цифры и дефис: vps, home, box-2")
	}
	return nil
}

// checkAddr проверяет адрес ресурса ровно настолько, насколько его касается контроллер:
// юзер назван, машина названа. Разбирать его дальше не наше дело — адрес уезжает соседу
// как есть, и второй разбор того же адреса разъехался бы с первым.
func checkAddr(addr string) (string, *refusal.Refusal) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", refusal.New(http.StatusBadRequest, "no-address",
			"адрес ресурса не назван — угадать его нельзя",
			"назови: user@10.8.0.5 или user@10.8.0.5:2222")
	}
	user, hostPort, ok := strings.Cut(addr, "@")
	if !ok || user == "" {
		// Пустой юзер ssh проглатывает молча и идёт текущим — человек назвал одного, а
		// пошли бы другим (грабля `WORLD2-96`, пункт 4).
		return "", refusal.New(http.StatusBadRequest, "bad-address",
			fmt.Sprintf("в адресе %q не назван юзер", addr),
			"назови целиком: user@10.8.0.5")
	}
	host, port, hasPort := strings.Cut(hostPort, ":")
	if host == "" {
		return "", refusal.New(http.StatusBadRequest, "bad-address",
			fmt.Sprintf("в адресе %q не названа машина", addr),
			"назови целиком: user@10.8.0.5")
	}
	if hasPort {
		if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 {
			return "", refusal.New(http.StatusBadRequest, "bad-address",
				fmt.Sprintf("в адресе %q после двоеточия стоит %q — это не порт ssh", addr, port),
				"порт — число: user@10.8.0.5:2222")
		}
	}
	return host, nil
}

func (m *Manager) dockerFailure(err error) *refusal.Refusal {
	if err == nil {
		return nil
	}
	if errors.Is(err, run.ErrNoTool) {
		return refusal.New(http.StatusInternalServerError, "no-docker",
			"докера в контроллере нет, а ресурсы — это его контексты",
			"это дефект образа контроллера — заведи задачу зоне control")
	}
	return refusal.New(http.StatusGatewayTimeout, "no-daemon",
		fmt.Sprintf("докер не ответил: %v", err),
		"проверь, что сокет докера отдан контроллеру при подъёме: control/README.md")
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
