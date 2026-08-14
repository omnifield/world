// Пакет resource — источники ресурса: где у юзера стоят двери мира.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ЭТОТ ПАКЕТ НЕ ПОДНИМАЕТ ДВЕРЬ. Подъём двери на названном ресурсе уже написан и лежит │
// │ в `deploy/remote.sh` (контекст докера по ssh, `WORLD2-99`). Контроллер его ЗОВЁТ.    │
// │ Второй подъём того же самого разъехался бы с первым на первой правке соседа — и      │
// │ человек получил бы две двери, которые ставятся по-разному.                            │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// Своего реестра ресурсов зона тоже НЕ заводит. Список — это контексты докера, ровно те
// же, что читает `deploy/remote.sh list`: один источник истины, а не два списка одного и
// того же (довод целиком — в шапке `deploy/remote.sh`). Мы читаем ту же истину, а не
// копируем её к себе.
//
// Что здесь ЕСТЬ своего и чего нет у соседа:
//
//   - креды. `docker context` поля для ключа не имеет вовсе, а юзер называет ключ, когда
//     добавляет ресурс. Контроллер кладёт ключ в свою связку и приписывает его к машине в
//     `~/.ssh/config` — то самое место, откуда ssh (а значит и докер) его берёт;
//   - «жив ли» одной строкой на весь список: пульту нужен список с состоянием, а не
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

	"github.com/omnifield/world/control/internal/refusal"
	"github.com/omnifield/world/control/internal/run"
)

// Значения, которые ПРИНАДЛЕЖАТ зоне `deploy` и здесь только повторены. Повтор — это шов,
// и он стережётся пробой (`probe-control.sh` читает `deploy/remote.sh` и краснеет, если
// там стало другое). Молчаливый повтор чужой константы — то, что разъезжается тише всего.
const (
	// ContextPrefix — приставка имени контекста докера (`PREFIX` в deploy/remote.sh).
	ContextPrefix = "world-"
	// DoorContainer — имя контейнера двери (`DOOR` в deploy/remote.sh, оно же
	// `container_name` в deploy/compose.yaml).
	DoorContainer = "world-door"
	// HereName — имя ресурса, на котором стоит сам контроллер. Занятое имя: завести
	// второй «здесь» нельзя, иначе список начнёт врать про то, где мы стоим.
	HereName = "here"
)

// nameRe — имя ресурса. Правило то же, что у соседа (`bad-name` в deploy/remote.sh), и
// проверяем мы его ДО вызова не ради второй проверки, а потому что имя идёт в ИМЯ ФАЙЛА
// ключа в нашей связке: имя с косой чертой увело бы запись из связки в чужой каталог.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// Resource — источник ресурса глазами юзера. Ни памяти, ни ядер здесь нет: ресурс в
// цифрах даёт отдельный инструмент осмотра, и он сознательно отложен (`WORLD2` 2.5).
type Resource struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
	// Here — на этом ресурсе стоит контроллер.
	Here bool `json:"here"`
	// Alive — отвечает ли ДВЕРЬ. Источник считается по двери: вход и есть то, чем ресурс
	// включается в мир (`WORLD2` 3.5). «Докер отвечает» — это не «ресурс в мире».
	Alive bool `json:"alive"`
	// Door — что именно видно про дверь, словами: «здорова», «поднимается», «нет»,
	// «ресурс молчит». Выведенное состояние врёт ровно тогда, когда на него смотрят,
	// поэтому здесь то, что измерено, а не то, что следует из другого поля.
	Door string `json:"door"`
}

// Manager — работа с источниками ресурса.
type Manager struct {
	Runner run.Runner
	// RemoteSh — путь к готовому подъёму двери. Значение, а не константа: в образе
	// контроллера он лежит по своему пути, в девбоксе — по своему, а проба подменяет его
	// заглушкой, чтобы проверить ПОВЕДЕНИЕ там, где докера нет.
	RemoteSh string
	// Docker — имя докер-клиента. Подменяется пробой по той же причине.
	Docker string
	// KeysDir — связка контроллера (`~/.ssh` внутри его образа): ключи ресурсов и
	// `config`, из которого их берёт ssh.
	KeysDir string
	// Port — хост-порт двери на том ресурсе; едет в `deploy/remote.sh` тем же именем,
	// каким тот его ждёт.
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
		r.Door, r.Alive = m.door(ctx, name)
		out = append(out, r)
	}
	return out, nil
}

// here — ресурс, на котором стоит контроллер. Адреса у него нет намеренно: изнутри
// машины её собственный адрес неизвестен, а выдуманный («localhost») однажды уедет в
// пульт и станет ложью для того, кто смотрит снаружи.
func (m *Manager) here(ctx context.Context) Resource {
	r := Resource{Name: HereName, Here: true}
	r.Door, r.Alive = m.door(ctx, "")
	return r
}

// door — измеряем дверь на ресурсе. Пустой контекст означает «здесь».
func (m *Manager) door(ctx context.Context, dockerContext string) (string, bool) {
	args := []string{}
	if dockerContext != "" {
		args = append(args, "--context", dockerContext)
	}
	args = append(args, "inspect", "--format",
		"{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}", DoorContainer)

	res, err := m.Runner.Run(ctx, run.Command{Name: m.Docker, Args: args})
	if err != nil {
		return "ресурс молчит", false
	}
	if res.Code != 0 {
		// Демон мог не ответить вовсе, а мог ответить «такого контейнера нет». Разные
		// вещи: первое чинит хозяин ресурса, второе — подъём двери.
		if strings.Contains(res.Err, "No such object") || strings.Contains(res.Err, "no such container") {
			return "двери нет", false
		}
		return "ресурс молчит", false
	}
	switch state := strings.TrimSpace(res.Out); state {
	case "healthy", "running":
		return "здорова", true
	case "starting":
		return "поднимается", false
	case "":
		return "неизвестно", false
	default:
		return state, false
	}
}

// Add — добавить ресурс: креды в связку, дальше готовый подъём двери.
func (m *Manager) Add(ctx context.Context, name, addr, creds string) (Resource, *refusal.Refusal) {
	if ref := validName(name); ref != nil {
		return Resource{}, ref
	}
	host, ref := checkAddr(addr)
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

	args := []string{"add", name, "--addr", addr}
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
		return Resource{}, m.toolFailure(err, res, "поставить дверь на "+addr+" не вышло")
	}

	out := Resource{Name: name, Addr: addr}
	out.Door, out.Alive = m.door(ctx, ContextPrefix+name)
	return out, nil
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
func (m *Manager) Drop(ctx context.Context, name string, withState, withImage bool) (Dropped, *refusal.Refusal) {
	if ref := validName(name); ref != nil {
		return Dropped{}, ref
	}
	if name == HereName {
		return Dropped{}, refusal.New(http.StatusConflict, "drop-here",
			"«здесь» — это ресурс, на котором стоит сам контроллер; снять его контроллером нельзя",
			"дверь на этом ресурсе снимается своим подъёмом: ./deploy/up.sh down",
			"сам контроллер снимается руками того, кто его ставил: ./control/up.sh down")
	}

	args := []string{"drop", name}
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

	out := Dropped{
		Name:    name,
		Removed: []string{"дверь (контейнер)", "сеть мира, если в ней больше никого", "контекст докера", "ключ ресурса из связки контроллера"},
	}
	if withState {
		out.Removed = append(out.Removed, "состояние поля (тома)")
	} else {
		out.Left = append(out.Left, "состояние поля (тома world-field, world-stand)")
		out.Ways = append(out.Ways, "снять и его: DELETE /api/resources/"+name+"?with-state=1")
	}
	if withImage {
		out.Removed = append(out.Removed, "образ мира")
	} else {
		out.Left = append(out.Left, "образ мира")
		out.Ways = append(out.Ways, "снять и его: DELETE /api/resources/"+name+"?with-image=1")
	}
	if len(out.Left) == 0 {
		out.Ways = append(out.Ways, "на той машине нашего не осталось ничего, кроме докера и ssh — их ставил не мир")
	}
	return out, nil
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
			"подъём двери (deploy/remote.sh) до контроллера не доехал — звать нечего",
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
