// Пакет scope — скоуп юзера: его личность и его поля, лежащие ТАМ, где он их положил.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ГЛАВНОЕ ПРАВИЛО ЭТОГО ПАКЕТА: скоуп берётся СВЯЗЬЮ, а не копией (`WORLD2` 1.6, 3.0). │
// │ Мы не тянем его к себе, не кэшируем и не «синхронизируем» — каждое чтение и каждая   │
// │ запись идут туда, где скоуп лежит. Копия завела бы вторую истину о личности юзера:   │
// │ она разъезжается молча, и заметно это становится ровно тогда, когда человек вошёл с  │
// │ другой машины и увидел себя прежним.                                                 │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// Скоуп юзера ОДИН и общий для всех полей — это его личность, и она едет с ним
// (`WORLD2` 3.4). Поэтому здесь только личность и список полей; локации, застройка и
// рейтинг живут в поле и сюда не попадают.
//
// Адрес скоупа — две формы, и обе называет человек:
//
//	/srv/scope                 путь на ресурсе, ГДЕ СТОИТ КОНТРОЛЛЕР
//	user@10.8.0.5:/srv/scope   на другом ресурсе — берём связью, по ssh
//	user@10.8.0.5:2222:/srv/scope   то же, если ssh не на 22
//
// Отдельной пары «личность плюс пароль» здесь нет и не заводится: дотянулся — значит твой
// (`WORLD2-77`, решение user). Креды — это ключ к АДРЕСУ, а не к личности.
package scope

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/omnifield/world/control/internal/refusal"
	"github.com/omnifield/world/control/internal/run"
)

// Имена файлов внутри скоупа. Их два, и это весь состав: личность и список полей.
const (
	identityFile = "identity.json"
	fieldsFile   = "fields.json"
)

// missingMarker — код выхода, которым удалённая сторона говорит «файла нет». Свой код
// нужен потому, что `cat` на отсутствующий файл и `cat` на непрочитанный отвечают
// одинаково (1), а отказы из них получаются разные: «скоупа нет» — это приглашение
// завести, «не прочитали» — поломка.
const missingMarker = 66

// Address — разобранный адрес скоупа.
type Address struct {
	Raw string
	// Here — скоуп лежит на том же ресурсе, где стоит контроллер. Тогда ни ssh, ни
	// кред не нужно: контроллер дотягивается до него собственными правами.
	Here   bool
	UserAt string // `user@host` целиком — ровно в том виде, в каком его берёт ssh
	Host   string
	Port   int
	Path   string
}

func (a Address) String() string { return a.Raw }

// Parse разбирает адрес ОДИН раз и в одном месте. Разъедутся два разбора — вход пойдёт по
// одному пути, а запись поля по другому, и юзер обнаружит два скоупа вместо одного.
func Parse(raw string) (Address, *refusal.Refusal) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Address{}, refusal.New(http.StatusBadRequest, "no-address",
			"адрес скоупа не назван, а угадать его нельзя",
			"назови путь на этом ресурсе: /scope/егор",
			"или адрес на другом: user@10.8.0.5:/srv/scope")
	}

	if strings.HasPrefix(raw, "/") {
		return Address{Raw: raw, Here: true, Path: path.Clean(raw)}, nil
	}

	at := strings.Index(raw, "@")
	if at < 0 {
		return Address{}, refusal.New(http.StatusBadRequest, "bad-address",
			fmt.Sprintf("адрес %q не похож ни на путь, ни на «юзер@машина:путь»", raw),
			"путь на этом ресурсе начинается со слэша: /scope/егор",
			"адрес на другом ресурсе: user@10.8.0.5:/srv/scope")
	}
	// Пустой юзер молча проглатывать нельзя: ssh взял бы текущего, и человек получил бы
	// НЕ ТОГО, кого назвал. Грабля соседней зоны (`WORLD2-96`, пункт 4) — здесь закрыта
	// отказом, а не молчанием.
	if at == 0 {
		return Address{}, refusal.New(http.StatusBadRequest, "bad-address",
			fmt.Sprintf("в адресе %q не назван юзер — до собачки пусто", raw),
			"назови его целиком: user@10.8.0.5:/srv/scope")
	}

	rest := raw[at+1:]
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) < 2 {
		return Address{}, refusal.New(http.StatusBadRequest, "bad-address",
			fmt.Sprintf("в адресе %q нет пути — назван только ресурс", raw),
			"допиши путь через двоеточие: "+raw+":/srv/scope")
	}

	addr := Address{Raw: raw, Host: parts[0], Port: 22}
	switch len(parts) {
	case 2:
		addr.Path = parts[1]
	default:
		port, err := strconv.Atoi(parts[1])
		if err != nil || port <= 0 || port > 65535 {
			return Address{}, refusal.New(http.StatusBadRequest, "bad-address",
				fmt.Sprintf("между машиной и путём стоит %q — это не порт", parts[1]),
				"порт — число: user@10.8.0.5:2222:/srv/scope",
				"без порта — просто: user@10.8.0.5:/srv/scope")
		}
		addr.Port = port
		addr.Path = parts[2]
	}

	if addr.Host == "" {
		return Address{}, refusal.New(http.StatusBadRequest, "bad-address",
			fmt.Sprintf("в адресе %q не названа машина", raw),
			"назови её: user@10.8.0.5:/srv/scope")
	}
	if !strings.HasPrefix(addr.Path, "/") {
		return Address{}, refusal.New(http.StatusBadRequest, "bad-address",
			fmt.Sprintf("путь %q не абсолютный, а относительный путь на чужой машине означает «где-то у кого-то дома»", addr.Path),
			"назови путь от корня: "+raw[:at+1]+addr.Host+":/srv/scope")
	}
	addr.Path = path.Clean(addr.Path)
	addr.UserAt = raw[:at] + "@" + addr.Host
	return addr, nil
}

// Identity — личность юзера. Ровно то, что назвал канон: имя и бренд (`WORLD2` 3.4).
// Ключи личности сюда не кладутся: мир кред не заводит и не хранит (`WORLD2` 3.0).
type Identity struct {
	Name    string `json:"name"`
	Brand   string `json:"brand"`
	Created string `json:"created"`
}

// Field — поле юзера в его списке. Пока это ЗАПИСЬ о поле, а не поднятое поле: само поле
// разбирается следующей итерацией (`WORLD2-101`), и опережать её здесь нечем.
type Field struct {
	Name    string `json:"name"`
	Created string `json:"created"`
}

// Scope — открытый доступ к скоупу по адресу. Состояния внутри не держит: каждый вызов
// идёт до места. Это дороже одного кэша и честнее его.
type Scope struct {
	Addr Address

	runner run.Runner
	// key — файл ключа для ssh. Пусто — обычный порядок ssh (агент, `~/.ssh/config`):
	// креды принадлежат юзеру и его связке, мир своего хранилища не заводит.
	key     string
	timeout int
	now     func() time.Time
}

// Open готовит доступ. Связь при этом НЕ проверяется: проверка — отдельное действие
// (`Enter`), и сливать их значило бы получить «открыли, но не знаем, есть ли что».
func Open(addr Address, runner run.Runner, key string, timeout int, now func() time.Time) *Scope {
	if timeout <= 0 {
		timeout = 10
	}
	if now == nil {
		now = time.Now
	}
	return &Scope{Addr: addr, runner: runner, key: key, timeout: timeout, now: now}
}

// Enter — вход: дотянуться до скоупа и прочитать, кто это. Скоупа по адресу нет — это не
// поломка, а развилка: юзеру предлагается завести его здесь (`no-scope`).
func (s *Scope) Enter(ctx context.Context) (*Identity, *refusal.Refusal) {
	data, found, ref := s.read(ctx, identityFile)
	if ref != nil {
		return nil, ref
	}
	if !found {
		return nil, refusal.New(http.StatusNotFound, "no-scope",
			fmt.Sprintf("по адресу %s скоупа нет — дотянулись, а личности там не лежит", s.Addr),
			"завести его здесь: POST /api/session с create=true, name и brand",
			"или назови адрес, где скоуп уже лежит")
	}
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, refusal.New(http.StatusBadGateway, "scope-broken",
			fmt.Sprintf("по адресу %s лежит %s, но прочитать его как личность не вышло: %v", s.Addr, identityFile, err),
			"посмотри файл глазами: он должен быть объектом с полями name и brand",
			"или назови другой адрес")
	}
	if id.Name == "" {
		return nil, refusal.New(http.StatusBadGateway, "scope-broken",
			fmt.Sprintf("в %s по адресу %s нет имени", identityFile, s.Addr),
			"поправь файл: имя — это то, чем юзер зовётся в мире")
	}
	return &id, nil
}

// Create разворачивает личность. Только ЗДЕСЬ, на ресурсе контроллера: первый вход
// заводит скоуп там, где юзер вошёл, а перенести его он сможет потом (решение user,
// `WORLD2-101`). Заводить личность на чужом ресурсе одной ручкой мы не беремся — это
// уже не «завести», а «развернуть у соседа», и делается оно подъёмом.
func (s *Scope) Create(ctx context.Context, name, brand string) (*Identity, *refusal.Refusal) {
	if !s.Addr.Here {
		return nil, refusal.New(http.StatusConflict, "create-elsewhere",
			fmt.Sprintf("скоуп заводится на ресурсе, где стоит контроллер, а %s — другой ресурс", s.Addr),
			"назови путь здесь: /scope/"+safeName(name),
			"а на тот ресурс перенесёшь его, когда он появится")
	}
	if name == "" {
		return nil, refusal.New(http.StatusBadRequest, "no-name",
			"личность заводится с именем, а имя не названо",
			"назови его: name=«егор»")
	}

	if _, found, ref := s.read(ctx, identityFile); ref != nil {
		return nil, ref
	} else if found {
		return nil, refusal.New(http.StatusConflict, "scope-exists",
			fmt.Sprintf("по адресу %s личность уже лежит — заводить поверх неё значит стереть её", s.Addr),
			"войди в неё: POST /api/session без create",
			"или назови другой путь")
	}

	id := Identity{Name: name, Brand: brand, Created: s.stamp()}
	if ref := s.writeJSON(ctx, identityFile, id); ref != nil {
		return nil, ref
	}
	// Список полей заводится сразу пустым, а не «когда появится первое»: пустой список и
	// отсутствующий файл — разные вещи, и второе пришлось бы каждый раз разбирать как
	// «то ли поля не заводили, то ли скоуп покалечен».
	if ref := s.writeJSON(ctx, fieldsFile, []Field{}); ref != nil {
		return nil, ref
	}
	return &id, nil
}

// Fields — поля юзера. Списка нет вовсе (скоуп заведён руками, старый) — это пустой
// список, а не отказ.
func (s *Scope) Fields(ctx context.Context) ([]Field, *refusal.Refusal) {
	data, found, ref := s.read(ctx, fieldsFile)
	if ref != nil {
		return nil, ref
	}
	if !found {
		return []Field{}, nil
	}
	var fields []Field
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, refusal.New(http.StatusBadGateway, "scope-broken",
			fmt.Sprintf("список полей по адресу %s прочитать не вышло: %v", s.Addr, err),
			"посмотри "+fieldsFile+" глазами: он должен быть массивом объектов с полем name")
	}
	if fields == nil {
		fields = []Field{}
	}
	return fields, nil
}

// AddField заводит ЗАПИСЬ о поле. Поле при этом не поднимается — и говорить об этом надо
// вслух, иначе человек ждёт поднятого поля и не получает ни его, ни отказа.
func (s *Scope) AddField(ctx context.Context, name string) (Field, []Field, *refusal.Refusal) {
	if strings.TrimSpace(name) == "" {
		return Field{}, nil, refusal.New(http.StatusBadRequest, "no-name",
			"у поля должно быть имя, а оно не названо",
			"назови его: name=«дом»")
	}
	fields, ref := s.Fields(ctx)
	if ref != nil {
		return Field{}, nil, ref
	}
	for _, f := range fields {
		if f.Name == name {
			return Field{}, nil, refusal.New(http.StatusConflict, "field-exists",
				fmt.Sprintf("поле «%s» в твоём списке уже есть", name),
				"назови другое имя",
				"или посмотри список: GET /api/fields")
		}
	}
	field := Field{Name: name, Created: s.stamp()}
	fields = append(fields, field)
	if ref := s.writeJSON(ctx, fieldsFile, fields); ref != nil {
		return Field{}, nil, ref
	}
	return field, fields, nil
}

func (s *Scope) stamp() string { return s.now().UTC().Format(time.RFC3339) }

// ── доступ до места ──────────────────────────────────────────────────────────
//
// Ниже — единственные два действия, которыми контроллер трогает скоуп: прочитать файл и
// записать файл. Обе ветки (здесь / по связи) держатся рядом намеренно: разъедутся они —
// и локальный скоуп начнёт вести себя не так, как удалённый, а юзеру обещано, что это
// одно и то же место, просто лежащее в разных местах.

func (s *Scope) read(ctx context.Context, name string) ([]byte, bool, *refusal.Refusal) {
	if s.Addr.Here {
		data, err := os.ReadFile(filepath.Join(s.Addr.Path, name))
		switch {
		case err == nil:
			return data, true, nil
		case os.IsNotExist(err):
			return nil, false, nil
		default:
			return nil, false, refusal.New(http.StatusInternalServerError, "scope-unreadable",
				fmt.Sprintf("%s по адресу %s не прочитался: %v", name, s.Addr, err),
				"проверь права на путь внутри контроллера",
				"путь монтируется томом при подъёме — см. control/README.md")
		}
	}

	// Удалённая сторона отвечает нашим кодом 66, если файла нет: `cat` своим кодом
	// «нет файла» и «не прочитал» не различает.
	remote := fmt.Sprintf("if [ -e %s ]; then cat -- %s; else exit %d; fi",
		quote(s.file(name)), quote(s.file(name)), missingMarker)
	res, err := s.runner.Run(ctx, run.Command{Name: "ssh", Args: append(s.sshArgs(), remote)})
	if ref := s.sshFailure(res, err); ref != nil {
		return nil, false, ref
	}
	if res.Code == missingMarker {
		return nil, false, nil
	}
	if res.Code != 0 {
		return nil, false, refusal.New(http.StatusBadGateway, "scope-unreadable",
			fmt.Sprintf("%s по адресу %s не прочитался: %s", name, s.Addr, tail(res.Err)),
			"проверь права на путь на том ресурсе",
			"путь и юзер в адресе — те же, что у ssh")
	}
	return []byte(res.Out), true, nil
}

func (s *Scope) writeJSON(ctx context.Context, name string, v any) *refusal.Refusal {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return refusal.New(http.StatusInternalServerError, "scope-unwritable",
			fmt.Sprintf("%s не собрался в JSON: %v", name, err),
			"это дефект контроллера — заведи задачу зоне control")
	}
	data = append(data, '\n')

	if s.Addr.Here {
		if err := os.MkdirAll(s.Addr.Path, 0o700); err != nil {
			return refusal.New(http.StatusInternalServerError, "scope-unwritable",
				fmt.Sprintf("каталог скоупа %s не завёлся: %v", s.Addr.Path, err),
				"проверь, что том смонтирован и права на нём есть")
		}
		// Через времянку и переименование: оборванная запись не должна оставлять
		// покалеченную личность. Личность у юзера одна, второй попытки прочитать её
		// «как было» у него нет.
		tmp := filepath.Join(s.Addr.Path, "."+name+".tmp")
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return refusal.New(http.StatusInternalServerError, "scope-unwritable",
				fmt.Sprintf("%s не записался: %v", name, err),
				"проверь права на путь внутри контроллера")
		}
		if err := os.Rename(tmp, filepath.Join(s.Addr.Path, name)); err != nil {
			return refusal.New(http.StatusInternalServerError, "scope-unwritable",
				fmt.Sprintf("%s не встал на место: %v", name, err),
				"проверь права на путь внутри контроллера")
		}
		return nil
	}

	tmp := s.file("." + name + ".tmp")
	remote := fmt.Sprintf("mkdir -p %s && cat > %s && mv %s %s",
		quote(s.Addr.Path), quote(tmp), quote(tmp), quote(s.file(name)))
	res, err := s.runner.Run(ctx, run.Command{Name: "ssh", Args: append(s.sshArgs(), remote), Stdin: string(data)})
	if ref := s.sshFailure(res, err); ref != nil {
		return ref
	}
	if res.Code != 0 {
		return refusal.New(http.StatusBadGateway, "scope-unwritable",
			fmt.Sprintf("%s по адресу %s не записался: %s", name, s.Addr, tail(res.Err)),
			"проверь права юзера из адреса на этот путь",
			"проверь, что на том ресурсе есть место")
	}
	return nil
}

func (s *Scope) file(name string) string { return path.Join(s.Addr.Path, name) }

// sshArgs — как мы ходим по связи. `BatchMode` обязателен: без него ssh на непринятом
// ключе уходит спрашивать пароль в пустой терминал и висит до предела времени, а человеку
// вместо «ключ не принят» приезжает молчание.
func (s *Scope) sshArgs() []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=" + strconv.Itoa(s.timeout),
	}
	if s.Addr.Port != 22 {
		args = append(args, "-p", strconv.Itoa(s.Addr.Port))
	}
	if s.key != "" {
		// IdentitiesOnly — чтобы агент не подсунул первый попавшийся ключ: юзер назвал
		// креды к ЭТОМУ адресу, и ходить надо ими, иначе «ключ не принят» приедет про
		// чужой ключ.
		args = append(args, "-i", s.key, "-o", "IdentitiesOnly=yes")
	}
	return append(args, s.Addr.UserAt)
}

// sshFailure разбирает, ЧТО именно не получилось. Ступени взяты те же, что у шлюза
// (`gate/README.md`): дорога · ответ · доступ. Чинят их разные люди, и общий отказ
// «не дотянулись» отправляет человека чинить наугад.
func (s *Scope) sshFailure(res run.Result, err error) *refusal.Refusal {
	if err != nil {
		if errors.Is(err, run.ErrNoTool) {
			return refusal.New(http.StatusInternalServerError, "no-ssh",
				"скоуп лежит на другом ресурсе, а ssh в контроллере нет",
				"это дефект образа контроллера — заведи задачу зоне control",
				"пока: положи скоуп на ресурс контроллера — путь /scope/…")
		}
		return refusal.New(http.StatusGatewayTimeout, "scope-silent",
			fmt.Sprintf("ресурс %s не ответил за отведённое время", s.Addr.Host),
			"разобрать по ступеням: ./gate/gate.sh check (GATE_ADDR="+s.Addr.UserAt+")",
			"дай больше времени: CONTROL_SSH_TIMEOUT=30")
	}
	if res.Code != 255 {
		return nil // 255 у ssh — «связь не состоялась»; всё прочее сказала уже команда
	}

	text := res.Err
	switch {
	case containsAny(text, "Permission denied", "Too many authentication failures", "no such identity", "Host key verification failed"):
		return refusal.New(http.StatusForbidden, "access-denied",
			fmt.Sprintf("ресурс %s ответил, но креды не принял", s.Addr.Host),
			"проверь ключ: он приезжает полем creds при входе",
			"проверь юзера в адресе: ходим именно им",
			"подробность: "+tail(text))
	case containsAny(text, "No route to host", "Network is unreachable", "Name or service not known", "could not resolve", "Could not resolve", "nodename nor servname"):
		return refusal.New(http.StatusBadGateway, "no-route",
			fmt.Sprintf("до ресурса %s нет дороги — адрес не разрешается или не маршрутизируется", s.Addr.Host),
			"проверь адрес",
			"если ресурс за туннелем — подними его: это хозяйство машины, не мира",
			"подробность: "+tail(text))
	case containsAny(text, "Connection refused", "Connection timed out", "Connection closed", "Operation timed out"):
		return refusal.New(http.StatusBadGateway, "no-answer",
			fmt.Sprintf("дорога до %s есть, а ssh на порту %d не отвечает", s.Addr.Host, s.Addr.Port),
			"проверь, что ssh на том ресурсе поднят",
			"если порт не 22 — назови его: user@host:2222:/srv/scope",
			"подробность: "+tail(text))
	default:
		return refusal.New(http.StatusBadGateway, "scope-unreachable",
			fmt.Sprintf("до скоупа %s дотянуться не вышло", s.Addr),
			"разобрать по ступеням: ./gate/gate.sh check (GATE_ADDR="+s.Addr.UserAt+")",
			"подробность: "+tail(text))
	}
}

// ── мелочи, у которых есть причина ───────────────────────────────────────────

// quote — единственный способ, которым значение попадает в команду на той стороне.
// Путь называет человек, и одинарная кавычка в нём ломала бы команду. Соседняя зона на
// этом уже поймана ревью (`WORLD2-96`, пункт 2) — повторять её грабли незачем.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func containsAny(text string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(text, n) {
			return true
		}
	}
	return false
}

// tail — последняя внятная строка чужого вывода. Целиком его в отказ не кладём: человеку
// нужна причина, а не журнал ssh.
func tail(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return "(инструмент промолчал)"
}

// safeName — имя в подсказке пути. Только для текста выхода: в путь оно не идёт.
func safeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "мой-скоуп"
	}
	return strings.ReplaceAll(s, " ", "-")
}
