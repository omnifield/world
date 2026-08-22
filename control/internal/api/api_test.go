package api

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omnifield/world/control/internal/creds/sshtest"
	"github.com/omnifield/world/control/internal/run"
	"github.com/omnifield/world/control/internal/state"
)

// Ручки проверяются ЦЕЛИКОМ, вместе с формой ответа: пульт (`web`) делается по этой же
// таблице, и разъехавшаяся форма — это не косметика, а неработающий вход.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ГЛАВНОЕ, ЧТО СТЕРЕЖЁТСЯ ЗДЕСЬ (`WORLD2-132`, ступень 2):                             │
// │                                                                                      │
// │   1. вход — это АДРЕС и ПАРОЛЬ, и больше ничего; хода «завести здесь» не существует;  │
// │   2. личность лежит В РАЗДАЧЕ по адресу, а не на контроллере;                         │
// │   3. территории и ключи живут в скоупе, а контексты докера — производные от него;     │
// │   4. выход снимает времянки, и вошедший другой личностью видит СВОИ территории.       │
// └─────────────────────────────────────────────────────────────────────────────────────┘

// ── подставная раздача: чужая вилка, а не наша ───────────────────────────────

// раздача — подставная вилка скоупа (`WORLD2` 3.4, `0.3`). Две ручки, пароль, и больше
// ничего: ни одного своего знака, по которому её можно узнать.
type раздача struct {
	адрес  string
	URL    string
	пароль string

	mu        sync.Mutex
	сломана   bool
	состояние []byte
	принято   []byte
	сервер    *httptest.Server
}

// новаяРаздача занимает адрес, но НЕ поднимается: так выглядит машина, на которой раздачи
// ещё нет, — а именно с неё начинается заведение скоупа.
func новаяРаздача(t *testing.T, пароль string) *раздача {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	адрес := l.Addr().String()
	_ = l.Close()
	ш := &раздача{адрес: адрес, URL: "http://" + адрес + "/", пароль: пароль}
	t.Cleanup(func() {
		ш.mu.Lock()
		defer ш.mu.Unlock()
		if ш.сервер != nil {
			ш.сервер.Close()
		}
	})
	return ш
}

// поднять — раздача встала на своём адресе. Ровно это и делает подъём рецептом на машине
// юзера; здесь оно делается руками теста в тот же момент.
func (ш *раздача) поднять(t *testing.T, состояние []byte) {
	t.Helper()
	ш.mu.Lock()
	defer ш.mu.Unlock()
	if ш.сервер != nil {
		ш.состояние = состояние
		return
	}
	ш.состояние = состояние
	l, err := net.Listen("tcp", ш.адрес)
	if err != nil {
		t.Fatalf("раздача не встала на %s: %v", ш.адрес, err)
	}
	ш.сервер = &httptest.Server{Listener: l, Config: &http.Server{Handler: http.HandlerFunc(ш.ручки)}}
	ш.сервер.Start()
}

// снять — раздача ушла с машины: по адресу больше никто не отвечает. Ровно это делает
// `remote.sh drop`, и ровно так это видит контроллер.
//
// СОСТОЯНИЕ ПРИ ЭТОМ ОСТАЁТСЯ В ПАМЯТИ — это и есть ТОМ, переживающий снятие: поднятая
// заново раздача снова отдаёт ту же личность. Стереть его — отдельное действие (`стеретьТом`),
// как `--with-state` у настоящего рецепта.
func (ш *раздача) снять() {
	ш.mu.Lock()
	defer ш.mu.Unlock()
	if ш.сервер != nil {
		ш.сервер.Close()
		ш.сервер = nil
	}
}

// поднятьСТомом — раздача встаёт заново НА СВОЁМ ТОМЕ: что в нём лежало, там и лежит.
// Так ведёт себя настоящий рецепт, и на этом держится обещание «личность переживает
// снятие»: поднять раздачу заново и увидеть пустоту — значит потерять личность молча.
func (ш *раздача) поднятьСТомом(t *testing.T) {
	t.Helper()
	ш.mu.Lock()
	том := ш.состояние
	ш.mu.Unlock()
	ш.поднять(t, том)
}

func (ш *раздача) стеретьТом() {
	ш.mu.Lock()
	defer ш.mu.Unlock()
	ш.состояние = nil
}

// стоит — отвечает ли раздача по своему адресу прямо сейчас.
func (ш *раздача) стоит() bool {
	ш.mu.Lock()
	defer ш.mu.Unlock()
	return ш.сервер != nil
}

// естьЛичность — лежит ли в томе состояние. Смотрим в саму раздачу, а не в ответ ручки:
// «том остался» и «мы про это сказали» — разные вещи, и первая проверяется только здесь.
func (ш *раздача) естьЛичность() bool {
	ш.mu.Lock()
	defer ш.mu.Unlock()
	return len(ш.состояние) > 0
}

// ломать — раздача жива, но отвечает не тем: пятисотка на любой вопрос. Так выглядит
// адрес, по которому измерить снятое НЕЛЬЗЯ — ни «сняли», ни «стоит». Третий исход, и
// молчать о нём мир не вправе (`WORLD2` 4.2).
func (ш *раздача) ломать() {
	ш.mu.Lock()
	defer ш.mu.Unlock()
	ш.сломана = true
}

func (ш *раздача) ручки(w http.ResponseWriter, r *http.Request) {
	ш.mu.Lock()
	сломана := ш.сломана
	ш.mu.Unlock()
	if сломана {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, пароль, есть := r.BasicAuth()
	if !есть || пароль != ш.пароль {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	ш.mu.Lock()
	defer ш.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		if ш.состояние == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(ш.состояние)
	case http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		ш.принято = data
		ш.состояние = data
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// файл — что сейчас лежит в раздаче. Смотрим НА НЕЁ, а не на ответ контроллера: скоуп
// обязан лежать там, куда его положили, а не в памяти процесса.
func (ш *раздача) файл(t *testing.T) *state.State {
	t.Helper()
	ш.mu.Lock()
	data := append([]byte(nil), ш.состояние...)
	ш.mu.Unlock()
	st, ref := state.Parse(data)
	if ref != nil {
		t.Fatalf("в раздаче лежит не состояние: %s — %s\n%s", ref.Code, ref.Why, data)
	}
	return st
}

// ── стенд ────────────────────────────────────────────────────────────────────

type стенд struct {
	*httptest.Server
	fake        *run.Fake
	keys        string
	pult        string
	recipes     string
	shareRecipe string
	log         []string
}

func поднять(t *testing.T, answer func(run.Command) (run.Result, error)) *стенд {
	t.Helper()
	dir := t.TempDir()
	st := &стенд{
		fake:        &run.Fake{Answer: answer},
		keys:        filepath.Join(dir, "keys"),
		pult:        filepath.Join(dir, "pult"),
		recipes:     filepath.Join(dir, "recipes"),
		shareRecipe: filepath.Join(dir, "share-compose.yaml"),
	}
	if err := os.MkdirAll(st.recipes, 0o755); err != nil {
		t.Fatal(err)
	}
	// Рецепт раздачи — обычный файл рядом с подъёмом. Содержимое здесь неважно: читает его
	// подъём, а не контроллер (`WORLD2` 3.7, «вещь описывается своим рецептом»).
	if err := os.WriteFile(st.shareRecipe, []byte("name: world-share\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := New(Options{
		Runner:       st.fake,
		RemoteSh:     "/opt/world/deploy/remote.sh",
		RecipesDir:   st.recipes,
		DoorRecipe:   дверьРецепт,
		ShareRecipe:  st.shareRecipe,
		Docker:       "docker",
		KeysDir:      st.keys,
		PultDir:      st.pult,
		DoorPort:     8080,
		ScopeTimeout: 2,
		Now:          func() time.Time { return time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) },
		NewToken:     func() (string, error) { return "metka", nil },
		Logf:         func(f string, a ...any) { st.log = append(st.log, f) },
	})
	st.Server = httptest.NewServer(h)
	t.Cleanup(st.Close)
	return st
}

// положитьПульт кладёт то, что приезжает из зоны `web` собранным. Содержимое неважно:
// зона `control` пульт не рисует, она его отдаёт.
func (s *стенд) положитьПульт(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(s.pult, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.pult, "index.html"), []byte("<!doctype html>пульт"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// зовБраузером — тот же запрос, но так, как его шлёт адресная строка. Пульт ходит сюда
// `fetch`, а тот шлёт `Accept: */*` — и обязан получать JSON, как и раньше.
func (s *стенд) зовБраузером(t *testing.T, path string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequest("GET", s.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header.Get("X-Control-Refusal")
}

func (s *стенд) зов(t *testing.T, method, path, body, token string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, s.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("%s %s: ответ не разобрался: %v", method, path, err)
	}
	return resp.StatusCode, out
}

// войти — вход в готовый скоуп: АДРЕС и ПАРОЛЬ, и больше ничего.
func (s *стенд) войти(t *testing.T, ш *раздача) map[string]any {
	t.Helper()
	status, body := s.зов(t, "POST", "/api/session",
		`{"addr":"`+ш.URL+`","password":"`+ш.пароль+`"}`, "")
	if status != http.StatusOK {
		t.Fatalf("вход отдал %d: %v", status, body)
	}
	return body
}

// личность — файл состояния с названными территориями. Тем же файлом отвечала бы чужая
// вилка: форма мира одна на всех.
func личность(t *testing.T, имя, бренд string, участки ...state.Territory) []byte {
	t.Helper()
	st := state.New(имя, бренд)
	for _, у := range участки {
		if ref := st.AddTerritory(у, state.Key{Name: у.Name, Kind: state.KindSSH, Value: "-----ключ " + у.Name + "-----"}); ref != nil {
			t.Fatal(ref.Why)
		}
	}
	data, err := st.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// отказ проверяет, что «нет» сказано ТРОЙКОЙ. Пустое «не получилось» — провал мира.
func отказ(t *testing.T, body map[string]any, want string) {
	t.Helper()
	code, _ := body["code"].(string)
	why, _ := body["why"].(string)
	ways, _ := body["ways"].([]any)
	if code != want {
		t.Fatalf("код отказа %q вместо %q (%v)", code, want, body)
	}
	if strings.TrimSpace(why) == "" {
		t.Fatalf("отказ %s без причины — человеку нечего чинить", want)
	}
	if len(ways) == 0 {
		t.Fatalf("отказ %s без выхода — тупик (WORLD2 2.3)", want)
	}
}

// Рецепт двери — там же, где его складывает подъём контроллера: рядом с готовым подъёмом.
const дверьРецепт = "/opt/world/deploy/compose.yaml"

// докерОтвечает — подставной докер, у которого на территории стоит одна здоровая вещь.
func докерОтвечает(c run.Command) (run.Result, error) {
	switch {
	case strings.HasSuffix(c.Name, "remote.sh"):
		return run.Result{}, nil
	case содержит(c.Args, "context") && содержит(c.Args, "inspect"):
		return run.Result{Code: 1, Err: "context not found"}, nil
	case содержит(c.Args, "context") && содержит(c.Args, "ls"):
		return run.Result{Out: "default\n"}, nil
	case содержит(c.Args, "ps"):
		return run.Result{Out: "aaa111\n"}, nil
	case содержит(c.Args, "inspect"):
		return run.Result{Out: "world\thealthy\trunning\n"}, nil
	}
	return run.Result{}, nil
}

func содержит(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// ── вход: адрес и пароль, и больше ничего ────────────────────────────────────

func TestВходЭтоАдресИПароль(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", "омнифилд"))

	body := st.войти(t, ш)
	if body["name"] != "егор" || body["brand"] != "омнифилд" || body["created"] != false {
		t.Fatalf("ответ входа собрался не так: %v", body)
	}
	if body["token"] != "metka" {
		t.Fatalf("метка сессии не отдана — курлом в контроллер не походишь: %v", body)
	}

	status, body := st.зов(t, "GET", "/api/me", "", "metka")
	if status != http.StatusOK || body["name"] != "егор" {
		t.Fatalf("«кто я» отдал %d: %v", status, body)
	}
	scope, _ := body["scope"].(map[string]any)
	if scope["addr"] != ш.URL {
		t.Fatalf("скоуп назвался не тем адресом: %v", scope)
	}
}

// ХОД «ЗАВЕСТИ ЗДЕСЬ» ОБЯЗАН КРАСНЕТЬ (`WORLD2` 3.7, `WORLD2-129`). Он не игнорируется
// молча — молча проглоченное поле выглядит как сработавшее, — а отказывает.
func TestХодаЗавестиЗдесьНеСуществует(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", ""))

	status, body := st.зов(t, "POST", "/api/session",
		`{"addr":"`+ш.URL+`","create":true,"name":"я"}`, "")
	if status != http.StatusBadRequest {
		t.Fatalf("«завести здесь» прошло как %d: %v", status, body)
	}
	отказ(t, body, "bad-body")
	if !strings.Contains(strings.Join(waysOf(t, body), " "), "/api/scope") {
		t.Fatalf("отказ не сказал, где скоуп заводится на самом деле: %v", body)
	}
}

func TestВходБезПароляОтказывает(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", ""))

	_, body := st.зов(t, "POST", "/api/session", `{"addr":"`+ш.URL+`"}`, "")
	отказ(t, body, "no-password")
}

func TestНеверныйПарольЭтоОтказАНеПустота(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", ""))

	_, body := st.зов(t, "POST", "/api/session", `{"addr":"`+ш.URL+`","password":"не-та"}`, "")
	отказ(t, body, "bad-password")
}

func TestБезВходаНиТерриторийНиПолей(t *testing.T) {
	st := поднять(t, докерОтвечает)
	for _, path := range []string{"/api/me", "/api/resources", "/api/fields", "/api/recipes"} {
		status, body := st.зов(t, "GET", path, "", "")
		if status != http.StatusUnauthorized {
			t.Fatalf("%s без входа отдал %d: %v", path, status, body)
		}
		отказ(t, body, "not-signed-in")
	}
	if len(st.fake.Calls()) != 0 {
		t.Fatalf("контроллер пошёл к докеру, не спросив, кто пришёл: %s", st.fake.Line(0))
	}
}

func TestЧужаяМеткаНеПускает(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", ""))
	st.войти(t, ш)

	status, body := st.зов(t, "GET", "/api/me", "", "не-та-метка")
	if status != http.StatusUnauthorized {
		t.Fatalf("чужая метка пустила: %d %v", status, body)
	}
	отказ(t, body, "not-signed-in")
}

// ── заведение скоупа: две пары, и путать их дорого ───────────────────────────

func TestЗаведениеПоднимаетРаздачуНаНазваннойМашине(t *testing.T) {
	ш := новаяРаздача(t, "тайна")
	var st *стенд
	st = поднять(t, func(c run.Command) (run.Result, error) {
		// Подъём рецептом — это и есть появление раздачи на той машине. Подставной подъём
		// делает ровно то же: раздача встаёт по названному адресу.
		if strings.HasSuffix(c.Name, "remote.sh") && содержит(c.Args, "add") {
			ш.поднять(t, nil)
			return run.Result{}, nil
		}
		return докерОтвечает(c)
	})

	status, body := st.зов(t, "POST", "/api/scope", `{
		"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"identity":{"name":"егор","brand":""},
		"machine":{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"-----ключ-----"}}}`, "")
	if status != http.StatusCreated {
		t.Fatalf("скоуп не завёлся: %d %v", status, body)
	}
	if body["name"] != "егор" || body["created"] != true {
		t.Fatalf("ответ собрался не так: %v", body)
	}

	// Раздача поднята РЕЦЕПТОМ, а не кодом контроллера, и пароль скоупа уехал ей её же
	// именем: им закрыта раздача, и внутри файла состояния его нет.
	if !st.fake.Called("remote.sh", "add", "vps", "--addr", "world@10.8.0.5", "--recipe", st.shareRecipe) {
		t.Fatalf("раздача поднята не рецептом: %s", st.fake.Line(0))
	}
	var env []string
	for i := range st.fake.Calls() {
		if strings.Contains(st.fake.Line(i), "remote.sh add") {
			env = st.fake.Calls()[i].Env
		}
	}
	if !содержит(env, "SHARE_PASSWORD=тайна") {
		t.Fatalf("пароль скоупа не доехал до подъёма раздачи: %v", env)
	}

	// ┌─────────────────────────────────────────────────────────────────────────────────┐
	// │ СВЕЖИЙ СКОУП — ЭТО ЛИЧНОСТЬ И ПУСТОТА (`WORLD2` 3.4, `WORLD2-152`).              │
	// │                                                                                  │
	// │ Ни территории, ни ключа: мир поднял раздачу и ушёл. Пока машина раздачи писалась  │
	// │ сюда участком, у скоупа появлялся «дом» — а с ним запрет на его снятие и ключ     │
	// │ мира, который нельзя было убрать через мир по устройству.                          │
	// └─────────────────────────────────────────────────────────────────────────────────┘
	файл := ш.файл(t)
	if файл.Identity.Name != "егор" || файл.Format != state.Version {
		t.Fatalf("в раздаче лежит не та личность: %+v", файл)
	}
	if len(файл.Territories) != 0 {
		t.Fatalf("заведение записало машину территорией — свежий скоуп не пуст: %+v", файл.Territories)
	}
	if len(файл.Keys) != 0 {
		t.Fatalf("в свежем скоупе лежат ключи, а мир на эту машину больше не ходит: %+v", файл.Keys)
	}
	// Пустой бренд — законное состояние (`WORLD2-135`), и заведение на нём не спотыкается.
	if файл.Identity.Brand != "" {
		t.Fatalf("бренд выдумался сам: %q", файл.Identity.Brand)
	}
	// Пустые списки заводятся сразу: отсутствующий раздел и пустой читались бы одинаково,
	// а чинятся по-разному (`WORLD2` 4.2).
	состояние, _ := файл.Bytes()
	for _, раздел := range []string{`"ключи": []`, `"территории": []`, `"поля": []`} {
		if !strings.Contains(string(состояние), раздел) {
			t.Fatalf("в свежем скоупе нет пустого раздела %s:\n%s", раздел, состояние)
		}
	}
}

// ТЕРРИТОРИЮ ЗАВОДИТ ЮЗЕР, И ТА ЖЕ МАШИНА ТАМ ЗАКОННА. Заведение скоупа её не записывает,
// но это не запрет на машину, а отказ от побочного следа: юзер вправе сделать её участком
// отдельным решением — и тогда мир туда ходит, и ключ лежит в скоупе.
func TestТаЖеМашинаЗаконнаУчасткомПоРешениюЮзера(t *testing.T) {
	ш := новаяРаздача(t, "тайна")
	var st *стенд
	st = поднять(t, func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") && содержит(c.Args, "add") {
			ш.поднять(t, nil)
			return run.Result{}, nil
		}
		return докерОтвечает(c)
	})

	status, body := st.зов(t, "POST", "/api/scope", `{
		"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"identity":{"name":"егор","brand":""},
		"machine":{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"-----ключ-----"}}}`, "")
	if status != http.StatusCreated {
		t.Fatalf("скоуп не завёлся: %d %v", status, body)
	}

	status, body = st.зов(t, "POST", "/api/resources",
		`{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"-----ключ-----"}}`, "metka")
	if status != http.StatusCreated {
		t.Fatalf("та же машина не завелась участком: %d %v", status, body)
	}
	файл := ш.файл(t)
	if len(файл.Territories) != 1 || файл.Territories[0].Addr != "world@10.8.0.5" {
		t.Fatalf("участок не записался в скоуп: %+v", файл.Territories)
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ГЛАВНЫЙ ПУТЬ ПРОДУКТА — И ДО 2026-08-20 ЕГО НЕ ПРОВЕРЯЛА НИ ОДНА ПРОБА.              │
// │                                                                                      │
// │ Юзер заводит скоуп на машине, где раздачи ЕЩЁ НЕТ, и даёт ПАРОЛЬ машины, а не ключ.  │
// │ Ровно это предлагает пульт первым делом, и ровно это падало живьём: `no-answer` за    │
// │ 90 мс, до первого касания машины.                                                     │
// │                                                                                      │
// │ Обе оси были покрыты ПОРОЗНЬ, и в этом всё дело: заведение проверялось только         │
// │ ключом (ранний выход мимо записи), пароль — только на `/api/resources`, где раздача    │
// │ уже стоит и запись проходит. На пересечении жил дефект, и он жил там месяц.           │
// │                                                                                      │
// │ Проба поэтому стережёт ПЕРЕСЕЧЕНИЕ, а не ещё один частный случай.                     │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestЗаведениеПоПаролюТамГдеРаздачиЕщёНет(t *testing.T) {
	пароль := "пароль-машины"
	ш := новаяРаздача(t, "тайна")
	var st *стенд
	st = поднять(t, func(c run.Command) (run.Result, error) {
		// Раздача появляется ровно тогда, когда её поднял рецепт, — не раньше.
		if strings.HasSuffix(c.Name, "remote.sh") && содержит(c.Args, "add") {
			ш.поднять(t, nil)
			return run.Result{}, nil
		}
		return докерОтвечает(c)
	})
	машина := машинаСПаролем(t, пароль)

	status, body := st.зов(t, "POST", "/api/scope", `{
		"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"identity":{"name":"егор","brand":""},
		"machine":{"name":"vps","addr":"world@`+машина.Addr+`","creds":{"kind":"password","value":"`+пароль+`"}}}`, "")
	if status != http.StatusCreated {
		t.Fatalf("скоуп по паролю на чистой машине не завёлся: %d %v", status, body)
	}

	// ┌─────────────────────────────────────────────────────────────────────────────────┐
	// │ МИР ПОДНЯЛ РАЗДАЧУ И УШЁЛ С МАШИНЫ (`WORLD2-152`).                               │
	// │                                                                                  │
	// │ Ключ, которым он туда ходил, снят с неё сразу после подъёма, и в скоуп он не      │
	// │ уехал: ходить туда мир больше не будет, а оставленный ключ — доступ, о котором     │
	// │ юзер не просил. Смотрим В ФАЙЛ МАШИНЫ: ответ ручки сказал бы что угодно.          │
	// └─────────────────────────────────────────────────────────────────────────────────┘
	if лежит := strings.TrimSpace(машина.Authorized()); лежит != "" {
		t.Fatalf("мир ушёл, а строка в authorized_keys машины осталась:\n%s", лежит)
	}
	файл := ш.файл(t)
	if len(файл.Keys) != 0 || len(файл.Territories) != 0 {
		t.Fatalf("свежий скоуп не пуст: ключи %+v, территории %+v", файл.Keys, файл.Territories)
	}

	// Цена названа: контроллер написал в чужую машину, и он же говорит, чем это кончилось.
	note, _ := body["note"].(string)
	if !strings.Contains(note, "authorized_keys") {
		t.Fatalf("не сказано, что изменено на чужой машине: %v", body)
	}
	if !strings.Contains(note, "убрали") {
		t.Fatalf("не сказано, что строку убрали, — юзер будет искать её у себя: %q", note)
	}

	// Пароль не утёк ни в скоуп, ни в журнал, ни в ответ.
	состояние, _ := файл.Bytes()
	if strings.Contains(string(состояние), пароль) {
		t.Fatal("пароль утёк в скоуп")
	}
	if strings.Contains(strings.Join(st.log, " "), пароль) {
		t.Fatalf("пароль утёк в журнал: %v", st.log)
	}
	if строка, _ := json.Marshal(body); strings.Contains(string(строка), пароль) {
		t.Fatal("пароль утёк в ответ ручки")
	}
}

func TestЗаведениеПоверхЧужойЛичностиОтказывает(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "чужой", ""))

	status, body := st.зов(t, "POST", "/api/scope", `{
		"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"identity":{"name":"егор","brand":""},
		"machine":{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"к"}}}`, "")
	if status != http.StatusConflict {
		t.Fatalf("заведение поверх личности отдало %d: %v", status, body)
	}
	отказ(t, body, "scope-exists")
	if st.fake.Called("remote.sh") {
		t.Fatalf("отказ уже тронул чужую машину: %s", st.fake.Line(0))
	}
	if ш.файл(t).Identity.Name != "чужой" {
		t.Fatal("чужая личность перезаписана")
	}
}

func TestЗаведениеБезМашиныТамГдеРаздачиНет(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна") // адрес занят, но никто не отвечает

	status, body := st.зов(t, "POST", "/api/scope", `{
		"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"identity":{"name":"егор","brand":""}}`, "")
	if status != http.StatusBadGateway {
		t.Fatalf("ждали «раздачи нет», получили %d: %v", status, body)
	}
	отказ(t, body, "no-share")
}

// Раздачу юзер вправе поднять сам — это его вилка, и мир в неё не смотрит (`0.3`). Тогда
// заведение это просто запись состояния по адресу, и машину называть не надо.
func TestВСвоюРаздачуЛичностьПишетсяБезМашины(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, nil)

	status, body := st.зов(t, "POST", "/api/scope", `{
		"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"identity":{"name":"егор","brand":"омнифилд"}}`, "")
	if status != http.StatusCreated {
		t.Fatalf("личность не легла в готовую раздачу: %d %v", status, body)
	}
	if st.fake.Called("remote.sh") {
		t.Fatalf("контроллер полез поднимать раздачу, которая уже стоит: %s", st.fake.Line(0))
	}
	файл := ш.файл(t)
	if файл.Identity.Name != "егор" || len(файл.Territories) != 0 {
		t.Fatalf("свежий скоуп собрался не как имя и пустота: %+v", файл)
	}
}

func TestЗаведениеБезПароляИБезИмени(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, nil)

	_, body := st.зов(t, "POST", "/api/scope",
		`{"scope":{"addr":"`+ш.URL+`","password":""},"identity":{"name":"егор"}}`, "")
	отказ(t, body, "no-password")

	_, body = st.зов(t, "POST", "/api/scope",
		`{"scope":{"addr":"`+ш.URL+`","password":"тайна"},"identity":{"name":""}}`, "")
	отказ(t, body, "no-name")
}

// ── территории живут в скоупе ────────────────────────────────────────────────

func TestТерриторияЗаписываетсяВСкоуп(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", ""))
	st.войти(t, ш)

	status, body := st.зов(t, "POST", "/api/resources",
		`{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"-----ключ-----"}}`, "metka")
	if status != http.StatusCreated {
		t.Fatalf("территория не завелась: %d %v", status, body)
	}
	if !st.fake.Called("remote.sh", "add", "vps", "--recipe", дверьРецепт) {
		t.Fatalf("подъём позван не готовый или без рецепта: %s", st.fake.Line(0))
	}

	файл := ш.файл(t)
	if len(файл.Territories) != 1 || файл.Territories[0].Name != "vps" {
		t.Fatalf("участок не уехал в скоуп: %+v", файл.Territories)
	}
	if ключ, есть := файл.Key("vps"); !есть || ключ.Value != "-----ключ-----" {
		t.Fatalf("креды не легли в связку скоупа: %+v", файл.Keys)
	}
	// Ключ ложится и в связку контроллера — но это ВРЕМЯНКА, производная от скоупа.
	if _, err := os.Stat(filepath.Join(st.keys, "world-vps")); err != nil {
		t.Fatalf("ключ не лёг в связку контроллера — ssh его не возьмёт: %v", err)
	}

	status, body = st.зов(t, "GET", "/api/resources", "", "metka")
	list, _ := body["resources"].([]any)
	if status != http.StatusOK || len(list) != 1 {
		t.Fatalf("список территорий: %d %v", status, body)
	}
	первая, _ := list[0].(map[string]any)
	if первая["name"] != "vps" || первая["reach"] != "отвечает" {
		t.Fatalf("территория описана не так: %v", первая)
	}
}

// ВТОРОЙ УЧАСТОК С ЗАНЯТЫМ ИМЕНЕМ — отказ МЕХАНИКИ (`WORLD2` 2.3), а не ответ ресурса:
// проверяем мы, по содержимому скоупа, ДО всякого докера.
func TestВторойУчастокСЗанятымИменемОтказывает(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", "", state.Territory{Name: "vps", Addr: "world@10.8.0.5"}))
	st.войти(t, ш)

	status, body := st.зов(t, "POST", "/api/resources",
		`{"name":"vps","addr":"world@10.8.0.9","creds":{"kind":"key","value":"другой"}}`, "metka")
	if status != http.StatusConflict {
		t.Fatalf("занятое имя принято как %d: %v", status, body)
	}
	отказ(t, body, "name-taken")
	if st.fake.Called("remote.sh") {
		t.Fatalf("до отказа успели тронуть машину: %s", st.fake.Line(0))
	}
	файл := ш.файл(t)
	if len(файл.Territories) != 1 || файл.Territories[0].Addr != "world@10.8.0.5" {
		t.Fatalf("строка в файле молча перезаписана: %+v", файл.Territories)
	}
}

func TestСнятиеУбираетУчастокИзСкоупа(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", "",
		state.Territory{Name: "vps", Addr: "world@10.8.0.5"},
		state.Territory{Name: "home", Addr: "world@10.8.0.6"}))
	st.войти(t, ш)

	status, body := st.зов(t, "DELETE", "/api/resources/vps", "", "metka")
	if status != http.StatusOK {
		t.Fatalf("снятие отдало %d: %v", status, body)
	}
	dropped, _ := body["dropped"].(map[string]any)
	if left, _ := dropped["left"].([]any); len(left) == 0 {
		t.Fatalf("сказали «сняли», не назвав оставленного: %v", dropped)
	}
	файл := ш.файл(t)
	if len(файл.Territories) != 1 || файл.Territories[0].Name != "home" {
		t.Fatalf("участок не убрался из скоупа: %+v", файл.Territories)
	}
	if _, есть := файл.Key("vps"); есть {
		t.Fatal("ключ пережил свой участок — это след, переживший вещь")
	}
	if _, err := os.Stat(filepath.Join(st.keys, "world-vps")); !os.IsNotExist(err) {
		t.Fatal("ключ снятого участка остался в связке контроллера")
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ УЧАСТОК НА ТОЙ ЖЕ МАШИНЕ, ГДЕ СТОИТ РАЗДАЧА СКОУПА, СНИМАЕТСЯ — И ЭТО ПРАВКА,        │
// │ А НЕ ПРОПУСК (`WORLD2-152`).                                                          │
// │                                                                                      │
// │ Прежде здесь стоял отказ `drop-scope-home`: снятие такого участка стёрло бы файл, в   │
// │ котором записано, что у юзера есть машины. Но участком эта машина становилась не по   │
// │ решению юзера, а сама — побочным следом заведения. След убран; завёл участок юзер     │
// │ руками — это его участок, и снимать его он вправе. Кода отказа больше нет вовсе.      │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestУчастокНаМашинеСвоейРаздачиСнимается(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	host, _, _ := net.SplitHostPort(ш.адрес)
	ш.поднять(t, личность(t, "егор", "", state.Territory{Name: "home", Addr: "world@" + host}))
	st.войти(t, ш)

	status, body := st.зов(t, "DELETE", "/api/resources/home", "", "metka")
	if status != http.StatusOK {
		t.Fatalf("свой участок не снялся: %d %v", status, body)
	}
	if code, _ := body["code"].(string); code == "drop-scope-home" {
		t.Fatal("код drop-scope-home вернулся — он снят вместе с причиной, которая его породила")
	}
	if !st.fake.Called("remote.sh", "drop", "home") {
		t.Fatalf("вещь на участке не снимали: %s", st.fake.Line(0))
	}
	if len(ш.файл(t).Territories) != 0 {
		t.Fatalf("участок остался в скоупе: %+v", ш.файл(t).Territories)
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ДВА УЧАСТКА НА ОДНОЙ МАШИНЕ: СНЯЛИ ОДИН — СТРОКА ОСТАЁТСЯ, И ЭТО СКАЗАНО ВСЛУХ.      │
// │                                                                                      │
// │ Ключ у скоупа ОДИН на все машины (`state.UserKeyName`), поэтому строка в чужом        │
// │ `authorized_keys` принадлежит скоупу, а не участку. Убери её мир вместе с первым      │
// │ участком — второй остался бы в скоупе живым на вид и недостижимым на деле.            │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestСнятиеОдногоУчасткаНеОбрываетВторойНаТойЖеМашине(t *testing.T) {
	пароль := "пароль-двух-участков"
	st := поднять(t, докерОтвечает)
	машина := машинаСПаролем(t, пароль)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", ""))
	st.войти(t, ш)

	for _, имя := range []string{"vps-a", "vps-b"} {
		status, body := st.зов(t, "POST", "/api/resources",
			`{"name":"`+имя+`","addr":"world@`+машина.Addr+`","creds":{"kind":"password","value":"`+пароль+`"}}`, "metka")
		if status != http.StatusCreated {
			t.Fatalf("участок %s не завёлся: %d %v", имя, status, body)
		}
	}

	status, body := st.зов(t, "DELETE", "/api/resources/vps-a", "", "metka")
	if status != http.StatusOK {
		t.Fatalf("снятие отдало %d: %v", status, body)
	}
	if strings.TrimSpace(машина.Authorized()) == "" {
		t.Fatal("строка убрана, пока на машине стоит второй участок — он остался недостижимым")
	}
	dropped, _ := body["dropped"].(map[string]any)
	left, _ := dropped["left"].([]any)
	сказано := false
	for _, l := range left {
		if s, _ := l.(string); strings.Contains(s, "authorized_keys") {
			сказано = true
		}
	}
	if !сказано {
		t.Fatalf("строку оставили молча — «ушли совсем» стало неправдой: %v", dropped)
	}
}

// ── выход и другая личность ──────────────────────────────────────────────────

func TestВыходСнимаетВремянкиИДругаяЛичностьВидитСвоё(t *testing.T) {
	st := поднять(t, докерОтвечает)
	первый := новаяРаздача(t, "п1")
	первый.поднять(t, личность(t, "егор", "", state.Territory{Name: "vps", Addr: "world@10.8.0.5"}))
	второй := новаяРаздача(t, "п2")
	второй.поднять(t, личность(t, "маша", "", state.Territory{Name: "home", Addr: "world@10.8.0.6"}))

	st.войти(t, первый)
	if _, err := os.Stat(filepath.Join(st.keys, "world-vps")); err != nil {
		t.Fatalf("ключ первой личности не разложился: %v", err)
	}

	status, body := st.зов(t, "DELETE", "/api/session", "", "metka")
	if status != http.StatusOK {
		t.Fatalf("выход отдал %d: %v", status, body)
	}
	if _, err := os.Stat(filepath.Join(st.keys, "world-vps")); !os.IsNotExist(err) {
		t.Fatal("ключ прежней личности пережил выход")
	}
	// Выход НЕ трогает своего состояния: скоуп лежит там, где лежал.
	if первый.файл(t).Identity.Name != "егор" {
		t.Fatal("выход тронул скоуп, а не должен был")
	}
	if status, body := st.зов(t, "GET", "/api/me", "", "metka"); status != http.StatusUnauthorized {
		t.Fatalf("после выхода метка ещё пускает: %d %v", status, body)
	}

	// Вход под ДРУГОЙ личностью — её территории, а не чужие. Это то, ради чего ступень и
	// делалась: если видно чужое, значит состояние осело в контроллере.
	st.войти(t, второй)
	_, body = st.зов(t, "GET", "/api/resources", "", "metka")
	list, _ := body["resources"].([]any)
	if len(list) != 1 {
		t.Fatalf("вошедший увидел не свой список: %v", body)
	}
	если, _ := list[0].(map[string]any)
	if если["name"] != "home" {
		t.Fatalf("вошедший увидел ЧУЖУЮ территорию: %v", если)
	}
	if _, err := os.Stat(filepath.Join(st.keys, "world-home")); err != nil {
		t.Fatalf("ключ своей личности не разложился: %v", err)
	}
}

// ── креды: ключ или пароль ───────────────────────────────────────────────────
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ПАРОЛЬ — СПОСОБ ПОЛУЧИТЬ КЛЮЧ, А НЕ ТРАНСПОРТ (`WORLD2-141`). Контроллер заходит им  │
// │ ОДИН раз, кладёт публичный ключ юзера на машину, приватный отдаёт в скоуп. Дальше    │
// │ всё по ключу, а пароль не живёт нигде.                                               │
// └─────────────────────────────────────────────────────────────────────────────────────┘

// машинаСПаролем — подставная машина, куда контроллер зайдёт паролем. Настоящий ssh, а не
// пересказ: смотреть потом будем В ЕЁ ФАЙЛ.
func машинаСПаролем(t *testing.T, пароль string) *sshtest.Machine {
	t.Helper()
	m, err := sshtest.Start("world", пароль, filepath.Join(t.TempDir(), "дом"))
	if err != nil {
		t.Fatalf("подставная машина не поднялась: %v", err)
	}
	t.Cleanup(m.Close)
	return m
}

func TestПарольМеняетсяНаКлючИНеЖивётНигде(t *testing.T) {
	пароль := "рут-пароль-от-впс-9876"
	st := поднять(t, докерОтвечает)
	машина := машинаСПаролем(t, пароль)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", ""))
	st.войти(t, ш)

	status, body := st.зов(t, "POST", "/api/resources",
		`{"name":"vps","addr":"world@`+машина.Addr+`","creds":{"kind":"password","value":"`+пароль+`"}}`, "metka")
	if status != http.StatusCreated {
		t.Fatalf("территория с паролем не завелась: %d %v", status, body)
	}

	// На машине лежит ровно одна строка — публичный ключ юзера с подписью.
	лежит := strings.TrimSpace(машина.Authorized())
	if лежит == "" || strings.Count(лежит, "\n") != 0 {
		t.Fatalf("в authorized_keys машины не одна строка:\n%s", машина.Authorized())
	}
	if !strings.Contains(лежит, "world-control") {
		t.Fatalf("строка не подписана — человек не поймёт, откуда она: %s", лежит)
	}

	// В скоупе лежит КЛЮЧ, и территория ссылается именно на него.
	файл := ш.файл(t)
	ключ, есть := файл.Key(state.UserKeyName)
	if !есть || !strings.Contains(ключ.Value, "PRIVATE KEY") {
		t.Fatalf("ключ юзера не появился в скоупе: %+v", файл.Keys)
	}
	if файл.Territories[0].Key != state.UserKeyName {
		t.Fatalf("территория ссылается не на ключ юзера: %+v", файл.Territories[0])
	}

	// ПАРОЛЬ НЕ УТЁК НИКУДА: ни в скоуп, ни в связку, ни в журнал, ни в ответ.
	состояние, _ := ш.файл(t).Bytes()
	if strings.Contains(string(состояние), пароль) {
		t.Fatal("пароль утёк в скоуп")
	}
	if strings.Contains(strings.Join(st.log, " "), пароль) {
		t.Fatalf("пароль утёк в журнал: %v", st.log)
	}
	if data, err := os.ReadFile(filepath.Join(st.keys, "world-vps")); err == nil && strings.Contains(string(data), пароль) {
		t.Fatal("пароль утёк в связку контроллера")
	}
	if строка, _ := json.Marshal(body); strings.Contains(string(строка), пароль) {
		t.Fatal("пароль утёк в ответ ручки")
	}

	// ЦЕНА НАЗВАНА: контроллер написал в чужую машину, и человек об этом узнаёт.
	if note, _ := body["note"].(string); !strings.Contains(note, "authorized_keys") {
		t.Fatalf("не сказано, что изменено на чужой машине: %v", body)
	}
	if !strings.Contains(strings.Join(st.log, " "), "authorized_keys") {
		t.Fatalf("в журнале нет строки про запись в чужую машину: %v", st.log)
	}
}

// Ключ юзера ОДИН на скоуп: вторая машина берёт тот же ключ, и повторный заход на первую
// строк не плодит.
func TestКлючЮзераОдинНаСкоуп(t *testing.T) {
	пароль := "пароль-два"
	st := поднять(t, докерОтвечает)
	первая := машинаСПаролем(t, пароль)
	вторая := машинаСПаролем(t, пароль)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", ""))
	st.войти(t, ш)

	for имя, машина := range map[string]*sshtest.Machine{"vps": первая, "home": вторая} {
		status, body := st.зов(t, "POST", "/api/resources",
			`{"name":"`+имя+`","addr":"world@`+машина.Addr+`","creds":{"kind":"password","value":"`+пароль+`"}}`, "metka")
		if status != http.StatusCreated {
			t.Fatalf("территория %s не завелась: %d %v", имя, status, body)
		}
	}
	файл := ш.файл(t)
	if len(файл.Keys) != 1 || файл.Keys[0].Name != state.UserKeyName {
		t.Fatalf("ключей в скоупе больше одного — повторный заход начнёт плодить строки: %+v", файл.Keys)
	}
	if strings.TrimSpace(первая.Authorized()) == strings.TrimSpace(вторая.Authorized()) &&
		strings.TrimSpace(первая.Authorized()) == "" {
		t.Fatal("ни на одной машине ключа нет")
	}
}

// ВИД КРЕД НАЗЫВАЕТСЯ ЯВНО. Не назван — отказ, а не догадка по виду строки: угаданный вид
// однажды примет ключ за пароль.
func TestВидКредНеУгадывается(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", ""))
	st.войти(t, ш)

	_, body := st.зов(t, "POST", "/api/resources",
		`{"name":"vps","addr":"world@10.8.0.5","creds":{"value":"-----ключ-----"}}`, "metka")
	отказ(t, body, "no-creds-kind")

	_, body = st.зов(t, "POST", "/api/resources",
		`{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"колдовство","value":"х"}}`, "metka")
	отказ(t, body, "bad-creds-kind")

	// Пароль, названный ключом, паролем НЕ становится: до машины никто не идёт, ключ
	// кладётся как есть — а дальше откажет ssh, и это честнее нашей догадки.
	status, _ := st.зов(t, "POST", "/api/resources",
		`{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"похоже-на-пароль"}}`, "metka")
	if status != http.StatusCreated {
		t.Fatalf("прежний путь по ключу сломан: %d", status)
	}
	файл := ш.файл(t)
	if ключ, есть := файл.Key("vps"); !есть || ключ.Value != "похоже-на-пароль" {
		t.Fatalf("ключ юзера подменён догадкой: %+v", файл.Keys)
	}
}

// Пароль не подошёл — отказ с причиной и выходом, и на машине ничего не появилось.
func TestНеверныйПарольМашиныОтказывает(t *testing.T) {
	st := поднять(t, докерОтвечает)
	машина := машинаСПаролем(t, "настоящий")
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", ""))
	st.войти(t, ш)

	status, body := st.зов(t, "POST", "/api/resources",
		`{"name":"vps","addr":"world@`+машина.Addr+`","creds":{"kind":"password","value":"не-тот"}}`, "metka")
	if status != http.StatusForbidden {
		t.Fatalf("неверный пароль принят как %d: %v", status, body)
	}
	отказ(t, body, "access-denied")
	if машина.Authorized() != "" {
		t.Fatal("на машине появилась строка при неверном пароле")
	}
	if len(ш.файл(t).Territories) != 0 {
		t.Fatal("территория записалась, хотя зайти не вышло")
	}
}

// ── рецепты ──────────────────────────────────────────────────────────────────

func TestРецептДоезжаетДоПодъёма(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", ""))
	st.войти(t, ш)

	весы := filepath.Join(st.recipes, "весы.yaml")
	if err := os.WriteFile(весы, []byte("name: весы\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	status, body := st.зов(t, "POST", "/api/resources",
		`{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"к"},"recipe":"весы"}`, "metka")
	if status != http.StatusCreated {
		t.Fatalf("вторая вещь не поднялась: %d %v", status, body)
	}
	if !st.fake.Called("remote.sh", "add", "vps", "--recipe", весы) {
		t.Fatalf("подъём позван не тем рецептом: %s", st.fake.Line(0))
	}

	if status, body = st.зов(t, "DELETE", "/api/resources/vps?recipe=весы", "", "metka"); status != http.StatusOK {
		t.Fatalf("снятие отдало %d: %v", status, body)
	}
	if !st.fake.Called("remote.sh", "drop", "vps", "--recipe", весы) {
		t.Fatalf("снятие пошло не тем рецептом: %s", st.fake.Line(len(st.fake.Calls())-1))
	}
}

func TestСписокРецептовЧитаетсяИзКаталога(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", ""))
	st.войти(t, ш)

	status, body := st.зов(t, "GET", "/api/recipes", "", "metka")
	list, _ := body["recipes"].([]any)
	if status != http.StatusOK || len(list) != 1 {
		t.Fatalf("в пустом ландшафте обязана быть одна дверь: %d %v", status, body)
	}
	дверь, _ := list[0].(map[string]any)
	if дверь["name"] != "door" || дверь["path"] != дверьРецепт {
		t.Fatalf("дверь описана не так: %v", дверь)
	}

	if err := os.WriteFile(filepath.Join(st.recipes, "весы.yaml"), []byte("name: весы\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, body = st.зов(t, "GET", "/api/recipes", "", "metka")
	list, _ = body["recipes"].([]any)
	if len(list) != 2 {
		t.Fatalf("положенный рецепт не появился, а обязан был: %v", body)
	}
}

func TestНеизвестныйРецептОтказываетСвоимКодом(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", ""))
	st.войти(t, ш)

	status, body := st.зов(t, "POST", "/api/resources",
		`{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"к"},"recipe":"часы"}`, "metka")
	if status != http.StatusNotFound {
		t.Fatalf("неизвестный рецепт отдан как %d: %v", status, body)
	}
	отказ(t, body, "no-such-recipe")
	if st.fake.Called("remote.sh") {
		t.Fatalf("подъём позвали на рецепте, которого нет: %s", st.fake.Line(0))
	}
}

func TestОтказПодъёмаДоезжаетДоПульта(t *testing.T) {
	st := поднять(t, func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") {
			return run.Result{
				Out:  "REMOTE-REFUSAL: access-denied\n",
				Err:  "✗ отказ: ресурс world@10.8.0.5 не принял ключ\n  выход: положи ключ юзеру на той машине\n",
				Code: 1,
			}, nil
		}
		return докерОтвечает(c)
	})
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", ""))
	st.войти(t, ш)

	status, body := st.зов(t, "POST", "/api/resources", `{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"к"}}`, "metka")
	if status != http.StatusBadGateway {
		t.Fatalf("отказ подъёма отдан как %d: %v", status, body)
	}
	отказ(t, body, "access-denied")
	if body["from"] != "deploy/remote.sh" {
		t.Fatalf("не названо, чей отказ: %v", body)
	}
	// Неудачный подъём не оставляет следов: ни ключа в связке, ни записи в скоупе.
	if _, err := os.Stat(filepath.Join(st.keys, "world-vps")); !os.IsNotExist(err) {
		t.Fatal("ключ пережил неудачный подъём — вторая попытка пошла бы ключом-призраком")
	}
	if len(ш.файл(t).Territories) != 0 {
		t.Fatalf("в скоуп записался участок, которого нет: %+v", ш.файл(t).Territories)
	}
}

// ── поля ─────────────────────────────────────────────────────────────────────

func TestПоляЛожатсяВСкоуп(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", ""))
	st.войти(t, ш)

	status, body := st.зов(t, "GET", "/api/fields", "", "metka")
	fields, _ := body["fields"].([]any)
	if status != http.StatusOK || len(fields) != 0 {
		t.Fatalf("список полей: %d %v", status, body)
	}

	status, body = st.зов(t, "POST", "/api/fields", `{"name":"дом"}`, "metka")
	if status != http.StatusCreated {
		t.Fatalf("поле не завелось: %d %v", status, body)
	}
	if note, _ := body["note"].(string); !strings.Contains(note, "не поднимается") {
		t.Fatalf("не сказано, чего не произошло: %v", body)
	}
	файл := ш.файл(t)
	if len(файл.Fields) != 1 || файл.Fields[0].Name != "дом" {
		t.Fatalf("поле не уехало в скоуп: %+v", файл.Fields)
	}
}

// ── комьюнити участка, адрес локации, заявленный ресурс ──────────────────────

// адресЛокации — что контроллер отвечает на вопрос «какой у неё адрес».
func (s *стенд) адресЛокации(t *testing.T, участок, локация string) string {
	t.Helper()
	status, body := s.зов(t, "GET", "/api/resources/"+участок+"/address?location="+локация, "", "metka")
	if status != http.StatusOK {
		t.Fatalf("адрес локации не отдан: %d %v", status, body)
	}
	адрес, _ := body["address"].(string)
	if адрес == "" {
		t.Fatalf("адрес пуст: %v", body)
	}
	return адрес
}

// ГЛАВНОЕ, ЧТО СТЕРЕЖЁТ ЭТА ПРОВЕРКА (`WORLD2` 2.1 п. 5, `2.2` п. 4): участок присоединился
// к двум комьюнити и ушёл из них — АДРЕС ЛОКАЦИИ НЕ ДРОГНУЛ ни разу.
func TestКомьюнитиУчасткаНеДвигаютАдресЛокации(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", "", state.Territory{Name: "vps", Addr: "world@10.8.0.5"}))
	st.войти(t, ш)

	до := st.адресЛокации(t, "vps", "baser")
	if до != "егор/vps/baser" {
		t.Fatalf("адрес собрался не тремя ярусами: %s", до)
	}

	for _, имя := range []string{"дом", "работа"} {
		if status, body := st.зов(t, "POST", "/api/fields", `{"name":"`+имя+`"}`, "metka"); status != http.StatusCreated {
			t.Fatalf("поле не завелось: %d %v", status, body)
		}
		status, body := st.зов(t, "POST", "/api/resources/vps/fields", `{"field":"`+имя+`"}`, "metka")
		if status != http.StatusOK {
			t.Fatalf("присоединение не прошло: %d %v", status, body)
		}
		// ЧЕГО НЕ ПРОИЗОШЛО — СКАЗАНО ВСЛУХ: вторая сторона доступа у службы поля, и не
		// увидевший этой строки счёл бы себя присоединённым.
		if note, _ := body["note"].(string); !strings.Contains(note, "служба поля") {
			t.Fatalf("не сказано, чего не произошло: %v", body)
		}
		if стало := st.адресЛокации(t, "vps", "baser"); стало != до {
			t.Fatalf("адрес сдвинулся от присоединения к «%s»: %s → %s", имя, до, стало)
		}
	}

	// Участок состоит в ДВУХ комьюнити разом, и это видно списком (`2.5` п. 3).
	файл := ш.файл(t)
	if len(файл.Territories[0].Fields) != 2 {
		t.Fatalf("в скоуп уехало не два комьюнити: %+v", файл.Territories[0].Fields)
	}
	if файл.Format != 2 {
		t.Fatalf("формат состояния не поднялся до 2: %d", файл.Format)
	}

	status, body := st.зов(t, "DELETE", "/api/resources/vps/fields/дом", "", "metka")
	if status != http.StatusOK {
		t.Fatalf("отвязка не прошла: %d %v", status, body)
	}
	// ОТВЯЗКА ОБЪЯВЛЯЕТСЯ (`2.2` п. 5), а уведомлять сегодня некому — и это сказано, а не
	// замолчано.
	if note, _ := body["note"].(string); !strings.Contains(note, "НЕ УВЕДОМЛЕНО") {
		t.Fatalf("отвязка промолчала о том, что поле не уведомлено: %v", body)
	}
	if стало := st.адресЛокации(t, "vps", "baser"); стало != до {
		t.Fatalf("адрес сдвинулся от отвязки: %s → %s", до, стало)
	}
	if остались := ш.файл(t).Territories[0].Fields; len(остались) != 1 || остались[0] != "работа" {
		t.Fatalf("отвязка убрала не то: %+v", остались)
	}
}

func TestОтказыКомьюнитиИАдресаПриходятТройкой(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", "", state.Territory{Name: "vps", Addr: "world@10.8.0.5"}))
	st.войти(t, ш)

	tests := []struct {
		имя, метод, путь, тело, код string
		статус                      int
	}{
		{"участка нет", "POST", "/api/resources/net/fields", `{"field":"дом"}`, "no-such-resource", http.StatusNotFound},
		{"комьюнити не записано", "POST", "/api/resources/vps/fields", `{"field":"дом"}`, "no-such-field", http.StatusNotFound},
		{"комьюнити не названо", "POST", "/api/resources/vps/fields", `{"field":""}`, "no-name", http.StatusBadRequest},
		{"не состоит вовсе", "DELETE", "/api/resources/vps/fields/дом", "", "not-joined", http.StatusNotFound},
		{"локация не названа", "GET", "/api/resources/vps/address", "", "no-name", http.StatusBadRequest},
		{"адрес чужого участка", "GET", "/api/resources/net/address?location=baser", "", "no-such-resource", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.имя, func(t *testing.T) {
			status, body := st.зов(t, tt.метод, tt.путь, tt.тело, "metka")
			if status != tt.статус {
				t.Fatalf("отдано %d вместо %d: %v", status, tt.статус, body)
			}
			отказ(t, body, tt.код)
		})
	}

	// До входа ни комьюнити, ни адреса не существует — как и территорий.
	for _, путь := range []string{"/api/resources/vps/fields", "/api/resources/vps/address"} {
		метод := "GET"
		if strings.HasSuffix(путь, "fields") {
			метод = "POST"
		}
		_, body := st.зов(t, метод, путь, `{"field":"дом"}`, "")
		отказ(t, body, "not-signed-in")
	}
}

// РЕСУРС УЧАСТКА ЗАЯВЛЯЕТСЯ, А НЕ ИЗМЕРЯЕТСЯ (`2.5` пп. 2, 6, 7). Проверок «хватит ли» нет
// ни одной, и пустое значение законно (`0.2`).
func TestРесурсУчасткаЗаявляется(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	ш.поднять(t, личность(t, "егор", "", state.Territory{Name: "vps", Addr: "world@10.8.0.5"}))
	st.войти(t, ш)

	status, body := st.зов(t, "GET", "/api/resources", "", "metka")
	список, _ := body["resources"].([]any)
	первый, _ := список[0].(map[string]any)
	if status != http.StatusOK || первый["resource"] != "" {
		t.Fatalf("незаявленный ресурс виден не пустым: %d %v", status, body)
	}
	// Пустой список комьюнити отдаётся списком, а не `null`: спрашивать тут некого —
	// принадлежность лежит в скоупе (`2.5` п. 4).
	if поля, есть := первый["fields"].([]any); !есть || len(поля) != 0 {
		t.Fatalf("комьюнити участка отданы не пустым списком: %v", первый["fields"])
	}

	status, body = st.зов(t, "PUT", "/api/resources/vps/resource", `{"resource":"2 ядра, 4 ГБ"}`, "metka")
	if status != http.StatusOK {
		t.Fatalf("заявление не прошло: %d %v", status, body)
	}
	if ш.файл(t).Territories[0].Resource != "2 ядра, 4 ГБ" {
		t.Fatalf("заявленное не уехало в скоуп: %+v", ш.файл(t).Territories[0])
	}
	// Снять заявление — то же действие пустым значением. Отказа здесь нет и быть не может.
	if status, body := st.зов(t, "PUT", "/api/resources/vps/resource", `{"resource":""}`, "metka"); status != http.StatusOK {
		t.Fatalf("снятие заявления отказало: %d %v", status, body)
	}
	if ш.файл(t).Territories[0].Resource != "" {
		t.Fatal("заявление не снялось пустым значением")
	}
}

// ── форма ────────────────────────────────────────────────────────────────────

func TestНеТаРучкаИНеТотГлагол(t *testing.T) {
	st := поднять(t, докерОтвечает)

	status, body := st.зов(t, "GET", "/api/такой-нет", "", "")
	if status != http.StatusNotFound {
		t.Fatalf("неизвестная ручка отдала %d", status)
	}
	отказ(t, body, "unknown-endpoint")

	status, body = st.зов(t, "PUT", "/api/session", "{}", "")
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("не тот глагол отдал %d: %v", status, body)
	}
	отказ(t, body, "wrong-method")
}

func TestКривоеТелоНазываетсяОтдельно(t *testing.T) {
	st := поднять(t, докерОтвечает)

	_, body := st.зов(t, "POST", "/api/session", "", "")
	отказ(t, body, "no-body")

	_, body = st.зов(t, "POST", "/api/session", "{это не json", "")
	отказ(t, body, "bad-body")

	// Лишнее поле — опечатка либо чужой контракт. Принять и промолчать нельзя: молча
	// проглоченное поле выглядит как сработавшее.
	_, body = st.зов(t, "POST", "/api/session", `{"addr":"http://x:8070/","адрес":"y"}`, "")
	отказ(t, body, "bad-body")
}

func TestТрассаПишетсяВсегда(t *testing.T) {
	st := поднять(t, докерОтвечает)
	st.зов(t, "GET", "/api/me", "", "")
	if len(st.log) != 1 {
		t.Fatalf("строк трассы %d вместо одной: %v", len(st.log), st.log)
	}
	for _, want := range []string{"name=", "http=", "refusal=", "dur="} {
		if !strings.Contains(st.log[0], want) {
			t.Fatalf("в трассе нет %q: %s", want, st.log[0])
		}
	}
}

// ── пульт и ручки одним адресом ──────────────────────────────────────────────

func TestПультРаздаётсяТемЖеАдресом(t *testing.T) {
	st := поднять(t, докерОтвечает)
	st.положитьПульт(t)

	status, body, _ := st.зовБраузером(t, "/")
	if status != http.StatusOK || !strings.Contains(body, "пульт") {
		t.Fatalf("корень отдал %d: %q", status, body)
	}
}

// Граница между ручками и пультом — то, ради чего в маршрутах есть отдельный `/api/`.
// Перехватит пульт ручку — машина получит страницу вместо JSON и не разберёт её; перехватит
// ручка пульт — человек увидит JSON вместо лица.
func TestРучкиИПультНеПерехватываютДругДруга(t *testing.T) {
	st := поднять(t, докерОтвечает)
	st.положитьПульт(t)

	status, body := st.зов(t, "GET", "/api/me", "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("ручка перехвачена пультом: %d %v", status, body)
	}
	отказ(t, body, "not-signed-in")

	status, body = st.зов(t, "GET", "/api/мее", "", "")
	if status != http.StatusNotFound {
		t.Fatalf("неизвестная ручка отдала %d: %v", status, body)
	}
	отказ(t, body, "unknown-endpoint")

	status, body = st.зов(t, "GET", "/api", "", "")
	if status != http.StatusNotFound {
		t.Fatalf("/api отдал %d (перенаправление вместо отказа?): %v", status, body)
	}
	отказ(t, body, "unknown-endpoint")

	if status, page, _ := st.зовБраузером(t, "/"); status != http.StatusOK || !strings.Contains(page, "пульт") {
		t.Fatalf("корень перестал быть пультом: %d %q", status, page)
	}
}

func TestПультаНетГоворитсяВслух(t *testing.T) {
	st := поднять(t, докерОтвечает) // пульт НЕ положен

	status, body := st.зов(t, "GET", "/", "", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("пустой пульт отдал %d: %v", status, body)
	}
	отказ(t, body, "no-pult")

	if !strings.Contains(strings.Join(waysOf(t, body), " "), "/api/me") {
		t.Fatalf("отказ не сказал, что ручки живы: %v", body)
	}
}

// Тот же отказ, две подачи: машине JSON, человеку в браузере — читаемый текст. Источник
// один, поэтому разъехаться им нечем.
func TestОтказЧеловекуЧитаемыйАМашинеJSON(t *testing.T) {
	st := поднять(t, докерОтвечает)

	status, page, code := st.зовБраузером(t, "/")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("браузеру отдано %d", status)
	}
	if strings.HasPrefix(strings.TrimSpace(page), "{") {
		t.Fatalf("человеку в браузере приехал JSON: %q", page)
	}
	for _, want := range []string{"no-pult", "выходы:", "build.sh"} {
		if !strings.Contains(page, want) {
			t.Fatalf("в тексте отказа нет %q:\n%s", want, page)
		}
	}
	if code != "no-pult" {
		t.Fatalf("машинный код не уехал заголовком: %q", code)
	}

	_, body := st.зов(t, "GET", "/", "", "")
	отказ(t, body, "no-pult")
}

func waysOf(t *testing.T, body map[string]any) []string {
	t.Helper()
	var out []string
	ways, _ := body["ways"].([]any)
	for _, w := range ways {
		if s, ok := w.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ── B3 · скоупов на одной машине сколько угодно ──────────────────────────────
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ МАШИНА НЕ ЕДИНИЦА ЛИЧНОСТИ, ЕДИНИЦА — АДРЕС (`WORLD2` 3.4, «Копий нет — есть          │
// │ адреса»). Две раздачи это два скоупа, и обе вправе стоять на одной машине.            │
// │                                                                                      │
// │ Проверяется НЕ «есть ли в вызове слово SHARE_NAME», а то, ради чего оно там: два      │
// │ заведения на ОДНОЙ машине получают РАЗНЫЕ имена и КАЖДОЕ свой порт. Проверка на       │
// │ присутствие прошла бы молча, отдай контроллер обоим одно и то же имя.                 │
// └─────────────────────────────────────────────────────────────────────────────────────┘

// значенияПодъёма — окружение последнего вызова подъёма с этим глаголом.
func значенияПодъёма(t *testing.T, s *стенд, глагол string) []string {
	t.Helper()
	var env []string
	for i := range s.fake.Calls() {
		if strings.Contains(s.fake.Line(i), "remote.sh "+глагол) {
			env = s.fake.Calls()[i].Env
		}
	}
	if env == nil {
		t.Fatalf("подъёма «%s» не было вовсе: %s", глагол, s.fake.Line(0))
	}
	return env
}

// значение — что стоит после `ИМЯ=` в окружении вызова. Пусто означает «не назвали».
func значение(env []string, имя string) string {
	for _, v := range env {
		if strings.HasPrefix(v, имя+"=") {
			return strings.TrimPrefix(v, имя+"=")
		}
	}
	return ""
}

func TestДваСкоупаНаОднойМашинеРазведеныИменемИПортом(t *testing.T) {
	ш1 := новаяРаздача(t, "тайна-1")
	ш2 := новаяРаздача(t, "тайна-2")

	// Один и тот же стенд, одна и та же машина, ОДНО И ТО ЖЕ имя участка — разные у них
	// только адреса скоупов. Имя участка живёт в скоупе, и у двух разных личностей оно
	// законно совпадает: разводить их обязан не юзер, а тот, кто поднимает.
	завести := func(t *testing.T, ш *раздача, пароль string) *стенд {
		t.Helper()
		var s *стенд
		s = поднять(t, func(c run.Command) (run.Result, error) {
			if strings.HasSuffix(c.Name, "remote.sh") && содержит(c.Args, "add") {
				ш.поднять(t, nil)
				return run.Result{}, nil
			}
			return докерОтвечает(c)
		})
		status, body := s.зов(t, "POST", "/api/scope", `{
			"scope":{"addr":"`+ш.URL+`","password":"`+пароль+`"},
			"identity":{"name":"егор","brand":""},
			"machine":{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"-----ключ-----"}}}`, "")
		if status != http.StatusCreated {
			t.Fatalf("скоуп не завёлся: %d %v", status, body)
		}
		return s
	}

	env1 := значенияПодъёма(t, завести(t, ш1, "тайна-1"), "add")
	env2 := значенияПодъёма(t, завести(t, ш2, "тайна-2"), "add")

	имя1, имя2 := значение(env1, "SHARE_NAME"), значение(env2, "SHARE_NAME")
	порт1, порт2 := значение(env1, "SHARE_PORT"), значение(env2, "SHARE_PORT")

	if имя1 == "" || имя2 == "" {
		t.Fatalf("имя раздачи не названо вовсе — вторая сядет поверх первой:\n%v\n%v", env1, env2)
	}
	if имя1 == имя2 {
		t.Fatalf("двум скоупам на одной машине дано ОДНО имя %q — тот же контейнер и тот же том", имя1)
	}
	// Порт — не наш выбор, а то, что юзер уже сказал в адресе скоупа.
	if порт1 != порт(ш1.адрес) || порт2 != порт(ш2.адрес) {
		t.Fatalf("порт раздачи выдуман, а не взят из адреса скоупа: %q/%q при адресах %s и %s",
			порт1, порт2, ш1.адрес, ш2.адрес)
	}
	// Пароль по-прежнему свой у каждой: он закрывает раздачу, а не машину.
	if значение(env1, "SHARE_PASSWORD") != "тайна-1" || значение(env2, "SHARE_PASSWORD") != "тайна-2" {
		t.Fatalf("пароли скоупов перепутаны или не доехали: %v / %v", env1, env2)
	}
}

func порт(адрес string) string {
	_, p, _ := strings.Cut(адрес, ":")
	return p
}

// Снятие за собой идёт ТЕМ ЖЕ именем, каким ставили. Без этого компоуз взял бы имя проекта
// по умолчанию и снял бы раздачу СОСЕДНЕГО скоупа на той же машине, а нашу оставил бы жить.
func TestСнятиеЗаСобойНазываетТуЖеРаздачу(t *testing.T) {
	// Раздача НЕ встаёт: подъём отвечает успехом, а по адресу никого нет. Значит запись
	// состояния упрётся в отказ, и контроллер обязан убрать за собой то, что поднял.
	ш := новаяРаздача(t, "тайна")
	st := поднять(t, докерОтвечает)

	status, body := st.зов(t, "POST", "/api/scope", `{
		"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"identity":{"name":"егор","brand":""},
		"machine":{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"-----ключ-----"}}}`, "")
	if status < 400 {
		t.Fatalf("запись в несуществующую раздачу прошла: %d %v", status, body)
	}
	if !st.fake.Called("remote.sh", "drop", "vps") {
		t.Fatalf("контроллер оставил поднятую раздачу на чужой машине: %s", st.fake.Line(0))
	}

	подъём := значенияПодъёма(t, st, "add")
	снятие := значенияПодъёма(t, st, "drop")
	if значение(снятие, "SHARE_NAME") != значение(подъём, "SHARE_NAME") {
		t.Fatalf("снимаем не то, что ставили: подняли %q, снимаем %q",
			значение(подъём, "SHARE_NAME"), значение(снятие, "SHARE_NAME"))
	}
	// Пароль нужен и на снятии: рецепт объявляет его обязательным, и без него компоуз не
	// разбирает файл ЦЕЛИКОМ — снятие отказало бы про рецепт вместо снятия (`WORLD2-145`).
	if значение(снятие, "SHARE_PASSWORD") == "" {
		t.Fatal("снятие пошло без пароля — рецепт раздачи на таком не читается вовсе")
	}
}

// ── B2 · каким путём пошли, говорит КОНТРОЛЛЕР ───────────────────────────────
//
// Метка соседа обязана доехать до пульта ПОКА ШАГ ИДЁТ. Здесь подставной подъём смотрит
// ход СВОИМИ ГЛАЗАМИ, не выйдя из вызова: если бы контроллер разбирал вывод после выхода
// процесса, ход в этот момент был бы пуст, и проверка покраснела бы.
func TestМеткаПутиВидналДоТогоКакШагКончился(t *testing.T) {
	ш := новаяРаздача(t, "тайна")
	var st *стенд
	var виденВнутри map[string]any
	st = поднять(t, func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") && содержит(c.Args, "add") {
			if c.OnLine == nil {
				t.Error("подъём позван без чтения живого потока — метку пути читать нечем")
				return run.Result{}, nil
			}
			// Сосед печатает метку ДО пути. Дальше он ещё работает — мы всё ещё внутри.
			c.OnLine("REMOTE-PATH: image-copy")
			_, виденВнутри = st.зов(t, "GET", "/api/progress", "", "")
			ш.поднять(t, nil)
			return run.Result{Out: "REMOTE-PATH: image-copy\n"}, nil
		}
		return докерОтвечает(c)
	})

	status, body := st.зов(t, "POST", "/api/scope", `{
		"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"identity":{"name":"егор","brand":""},
		"machine":{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"-----ключ-----"}}}`, "")
	if status != http.StatusCreated {
		t.Fatalf("скоуп не завёлся: %d %v", status, body)
	}
	if виденВнутри["path"] != "image-copy" {
		t.Fatalf("пока шаг ШЁЛ, ход не назвал пути: %v", виденВнутри)
	}
	if виденВнутри["busy"] != true {
		t.Fatalf("ход не сказал, что действие идёт: %v", виденВнутри)
	}
	if виденВнутри["name"] != "vps" {
		t.Fatalf("ход не назвал участка: %v", виденВнутри)
	}
	// Адреса машины в ходе нет и не будет: ход читается без входа.
	if strings.Contains(strings.Join(жсон(t, виденВнутри), " "), "10.8.0.5") {
		t.Fatalf("в ход уехал адрес машины: %v", виденВнутри)
	}

	// Кончилось — «идёт» гаснет, а шаги остаются: спрашивают ход не непрерывно.
	_, после := st.зов(t, "GET", "/api/progress", "", "")
	if после["busy"] != false {
		t.Fatalf("действие кончилось, а ход всё ещё «идёт»: %v", после)
	}
	if после["path"] != "image-copy" {
		t.Fatalf("шаги стёрлись вместе с действием: %v", после)
	}
}

// жсон — значения ответа строками, чтобы искать в них то, чего там быть не должно.
func жсон(t *testing.T, body map[string]any) []string {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return []string{string(data)}
}

// Ход читается БЕЗ входа — иначе его нельзя прочесть в единственный момент, ради которого
// он заведён: `POST /api/scope` метки сессии ещё не выдал.
func TestХодЧитаетсяБезВхода(t *testing.T) {
	st := поднять(t, докерОтвечает)
	status, body := st.зов(t, "GET", "/api/progress", "", "")
	if status != http.StatusOK {
		t.Fatalf("ход потребовал входа: %d %v", status, body)
	}
	if body["busy"] != false {
		t.Fatalf("на пустом контроллере ход сказал «идёт»: %v", body)
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ УПАВШЕЕ ЗАВЕДЕНИЕ НЕ ОСТАВЛЯЕТ КЛЮЧ НА ЧУЖОЙ МАШИНЕ.                                 │
// │                                                                                      │
// │ Живой прогон 2026-08-20: заведение падало ПОСЛЕ того, как ключ уже лёг. Скоуп при     │
// │ этом не записан — значит приватной половины у юзера нет, опознать строку в своём      │
// │ `authorized_keys` он не может, и убрать её ему нечем. За несколько попыток их         │
// │ накопилось шесть.                                                                     │
// │                                                                                      │
// │ Отказ не вправе ничего оставлять за собой (`WORLD2` 2.3 п. 5) — на чужой машине тем   │
// │ более.                                                                                │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestУпавшееЗаведениеУбираетКлючСМашины(t *testing.T) {
	пароль := "пароль-машины"
	ш := новаяРаздача(t, "тайна")
	st := поднять(t, func(c run.Command) (run.Result, error) {
		// Подъём раздачи не удался — самая частая беда живого прогона.
		if strings.HasSuffix(c.Name, "remote.sh") && содержит(c.Args, "add") {
			return run.Result{Code: 1, Out: "REMOTE-REFUSAL: no-remote-docker\n"}, nil
		}
		return докерОтвечает(c)
	})
	машина := машинаСПаролем(t, пароль)

	status, _ := st.зов(t, "POST", "/api/scope", `{
		"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"identity":{"name":"егор","brand":""},
		"machine":{"name":"vps","addr":"world@`+машина.Addr+`","creds":{"kind":"password","value":"`+пароль+`"}}}`, "")
	if status == http.StatusCreated {
		t.Fatal("заведение прошло, хотя подъём отказал — проба проверяет не то")
	}

	if лежит := strings.TrimSpace(машина.Authorized()); лежит != "" {
		t.Fatalf("на чужой машине остался наш ключ, которого у юзера нет:\n%s", лежит)
	}
}

// СКОУП ПОМНИТ, ЧТО КОНТРОЛЛЕР НА ТЕРРИТОРИИ ПОДНЯЛ. Имя приходит от соседа меткой
// `REMOTE-THING:` — он рецепт и читал. Без этой памяти список вещей показывал бы юзеру
// раздачи чужих скоупов, стоящие на той же машине (живой прогон 2026-08-20).
//
// Спрашивается это у ЗАВЕДЕНИЯ УЧАСТКА, а не у заведения скоупа: у свежего скоупа участка
// нет вовсе, и помнить имя вещи там некому и незачем (`WORLD2-152`).
func TestЗаведениеУчасткаЗапоминаетИмяПоднятойВещи(t *testing.T) {
	ш := новаяРаздача(t, "тайна")
	st := поднять(t, func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") && содержит(c.Args, "add") {
			// Сосед называет имя вещи — тем же путём, что путь доставки и отказ.
			return run.Result{Out: "REMOTE-PATH: image-pull\nREMOTE-THING: world\n"}, nil
		}
		return докерОтвечает(c)
	})
	ш.поднять(t, личность(t, "егор", ""))
	st.войти(t, ш)

	status, body := st.зов(t, "POST", "/api/resources",
		`{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"-----ключ-----"}}`, "metka")
	if status != http.StatusCreated {
		t.Fatalf("участок не завёлся: %d %v", status, body)
	}

	файл := ш.файл(t)
	if len(файл.Territories) != 1 || len(файл.Territories[0].Things) != 1 ||
		файл.Territories[0].Things[0] != "world" {
		t.Fatalf("скоуп не запомнил поднятую вещь: %+v", файл.Territories)
	}
}

// А ЕСЛИ СОСЕД ПРОМОЛЧАЛ — не выдумываем: имя принадлежит рецепту, и пустая память честнее
// придуманной. Список вещей у такой территории показывается целиком.
func TestМолчаниеСоседаНеПревращаетсяВВыдуманноеИмя(t *testing.T) {
	ш := новаяРаздача(t, "тайна")
	st := поднять(t, func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") && содержит(c.Args, "add") {
			return run.Result{}, nil // ни метки, ни имени
		}
		return докерОтвечает(c)
	})
	ш.поднять(t, личность(t, "егор", ""))
	st.войти(t, ш)

	st.зов(t, "POST", "/api/resources",
		`{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"-----ключ-----"}}`, "metka")

	файл := ш.файл(t)
	if len(файл.Territories) != 1 || len(файл.Territories[0].Things) != 0 {
		t.Fatalf("имя вещи выдумано на пустом месте: %+v", файл.Territories)
	}
}

// ── снять скоуп по адресу ────────────────────────────────────────────────────
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ЧТО МИР УМЕЕТ ЗАВЕСТИ, ТО УМЕЕТ И УБРАТЬ (решение user 2026-08-20, `WORLD2-152`).    │
// │                                                                                      │
// │ Проверяется ПАРА: завели той же ручкой, сняли этой — и смотрим не в ответ, а в саму   │
// │ подставную раздачу: стоит ли она и лежит ли в её томе личность. Ответ можно собрать   │
// │ из памяти, поднятую вещь — нельзя.                                                    │
// └─────────────────────────────────────────────────────────────────────────────────────┘

// подъёмИСнятие — подставной подъём, который и правда поднимает раздачу и правда её
// снимает. Том при этом переживает снятие, как у настоящего рецепта, и стирается только
// по `--with-state`.
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ЗАГЛУШКА СНИМАЕТ ТО, ЧТО НАЗВАНО ИМЕНЕМ, а не «раздачу вообще» — иначе она врала бы   │
// │ в самую важную сторону.                                                               │
// │                                                                                      │
// │ Компоуз честно снимает проект, которого на машине НЕТ: для него это не ошибка, и      │
// │ подъём выходит нулём. Раздача, стоящая под другим именем, от такого снятия не         │
// │ падает — ровно это и нашлось живым прогоном (`WORLD2-152`, ревью).                    │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func подъёмИСнятие(t *testing.T, ш *раздача) func(run.Command) (run.Result, error) {
	поднято := "" // под каким именем проекта раздача сейчас стоит
	return func(c run.Command) (run.Result, error) {
		// РАЗДАЧА ЛИ ЭТО — видно по `SHARE_NAME`: её значения называет только тот, кто
		// поднимает раздачу. Обычная вещь на территории идёт без них, и путать её с
		// раздачей нельзя — заглушка, снимающая «что угодно», врала бы в самую важную
		// сторону.
		if strings.HasSuffix(c.Name, "remote.sh") && содержит(c.Args, "add") {
			имя := значение(c.Env, "SHARE_NAME")
			if имя == "" {
				return run.Result{}, nil // вещь юзера, не раздача
			}
			поднято = имя
			ш.поднятьСТомом(t)
			// Имя вещи сосед называет меткой — он рецепт и читал.
			return run.Result{Out: "REMOTE-THING: " + поднято + "\n"}, nil
		}
		if strings.HasSuffix(c.Name, "remote.sh") && содержит(c.Args, "drop") {
			имя := значение(c.Env, "SHARE_NAME")
			if имя == "" {
				return run.Result{}, nil // снимают вещь юзера, а не раздачу
			}
			if имя == поднято {
				ш.снять()
				if содержит(c.Args, "--with-state") {
					ш.стеретьТом()
				}
			}
			return run.Result{Out: "REMOTE-THING: " + имя + "\n"}, nil
		}
		return докерОтвечает(c)
	}
}

// завестиСкоуп — тот самый ход, который потом снимаем. Отдельной функцией, потому что
// проверяем мы ПАРУ, а не одну ручку.
func завестиСкоуп(t *testing.T, st *стенд, ш *раздача) {
	t.Helper()
	status, body := st.зов(t, "POST", "/api/scope", `{
		"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"identity":{"name":"егор","brand":""},
		"machine":{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"-----ключ-----"}}}`, "")
	if status != http.StatusCreated {
		t.Fatalf("скоуп не завёлся: %d %v", status, body)
	}
}

// снятьСкоуп — DELETE тем же телом, что и заведение, минус личность.
func снятьСкоуп(t *testing.T, st *стенд, ш *раздача, ключи string) (int, map[string]any) {
	t.Helper()
	return st.зов(t, "DELETE", "/api/scope"+ключи, `{
		"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"machine":{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"-----ключ-----"}}}`, "")
}

func TestСкоупСнимаетсяПоАдресуСимметричноЗаведению(t *testing.T) {
	ш := новаяРаздача(t, "тайна")
	st := поднять(t, подъёмИСнятие(t, ш))
	завестиСкоуп(t, st, ш)

	status, body := снятьСкоуп(t, st, ш, "")
	if status != http.StatusOK {
		t.Fatalf("скоуп не снялся: %d %v", status, body)
	}
	if ш.стоит() {
		t.Fatal("ответили «снято», а раздача по адресу всё ещё отвечает")
	}
	if !st.fake.Called("remote.sh", "drop", "vps", "--recipe", st.shareRecipe) {
		t.Fatalf("раздача снята не тем же рецептом, каким поднята: %s", st.fake.Line(0))
	}

	// СНИМАЕМ ТУ ЖЕ РАЗДАЧУ, ЧТО ПОДНИМАЛИ. Имя проекта, контейнера и тома рецепт собирает
	// из `SHARE_NAME`; уйди оно из снятия — компоуз снял бы раздачу ПО УМОЛЧАНИЮ, то есть
	// соседний скоуп на той же машине, а наш оставил бы жить.
	подъём, снятие := значенияПодъёма(t, st, "add"), значенияПодъёма(t, st, "drop")
	if значение(снятие, "SHARE_NAME") == "" || значение(снятие, "SHARE_NAME") != значение(подъём, "SHARE_NAME") {
		t.Fatalf("снимаем не то, что поднимали: подняли %q, снимаем %q",
			значение(подъём, "SHARE_NAME"), значение(снятие, "SHARE_NAME"))
	}
	if значение(снятие, "SHARE_PASSWORD") == "" {
		t.Fatal("снятие пошло без пароля — рецепт раздачи на таком не читается вовсе (WORLD2-145)")
	}

	// АДРЕС МАШИНЫ СНЯТИЕ БЕРЁТ ИЗ КОНТЕКСТА (`deploy/remote.sh`, cmd_drop), а контекста у
	// снятого скоупа нет: территории в нём не заводится. Значит мы обязаны положить его сами
	// — и положить ДО вызова, иначе сосед откажет «такого ресурса не заводили».
	контекст, снятиеВызвано := -1, -1
	for i := range st.fake.Calls() {
		line := st.fake.Line(i)
		if strings.Contains(line, "context") && strings.Contains(line, "world-vps") && контекст < 0 {
			контекст = i
		}
		if strings.Contains(line, "remote.sh drop") {
			снятиеВызвано = i
		}
	}
	if контекст < 0 || снятиеВызвано < 0 || контекст > снятиеВызвано {
		t.Fatalf("контекст докера не заведён до снятия — сосед возьмёт адрес машины неоткуда: %s", st.fake.Line(0))
	}

	// Времянки контроллера ушли: ключ и блок в `config`.
	if _, err := os.Stat(filepath.Join(st.keys, "world-vps")); !os.IsNotExist(err) {
		t.Fatal("ключ остался в связке контроллера — мир ушёл с машины не совсем")
	}

	// ТОМ ПО УМОЛЧАНИЮ ОСТАЁТСЯ, и это сказано вслух: снятие вещи не означает потерю
	// личности (`WORLD2` 1.9).
	if !ш.естьЛичность() {
		t.Fatal("том стёрт без with-state — личность стёрта молча")
	}
	note, _ := body["note"].(string)
	if !strings.Contains(note, "остал") || !strings.Contains(note, "личност") {
		t.Fatalf("не сказано, что осталось: %q", note)
	}
	// ЧЕМ ЭТО СТЕРЕТЬ — говорят ВЫХОДЫ, и говорят ровно один раз. Ищем ключ именно в них:
	// проверка «есть ли где-нибудь в ответе» зеленела бы на любой из двух копий, а копий
	// быть не должно (своя грабля `WORLD2-150`).
	dropped, _ := body["dropped"].(map[string]any)
	if left, _ := dropped["left"].([]any); len(left) == 0 {
		t.Fatalf("сказали «сняли», не назвав оставленного: %v", dropped)
	}
	ways, _ := dropped["ways"].([]any)
	чемСтереть := ""
	for _, w := range ways {
		if s, _ := w.(string); strings.Contains(s, "with-state=1") {
			чемСтереть = s
		}
	}
	if чемСтереть == "" {
		t.Fatalf("не сказано, чем стереть оставшийся том: %v", dropped)
	}
	if !strings.Contains(чемСтереть, "/api/scope") {
		t.Fatalf("выход зовёт не в ту ручку — команда, которую нельзя повторить как есть: %q", чемСтереть)
	}
	if strings.Contains(note, "with-state=1") {
		t.Fatalf("команда стирания сказана дважды — две правды об одном разъедутся молча: %q", note)
	}

	// Сессия, которая вела в этот скоуп, больше никуда не ведёт: раздачи по адресу нет.
	if status, body := st.зов(t, "GET", "/api/me", "", "metka"); status != http.StatusUnauthorized {
		t.Fatalf("после снятия скоупа метка ещё пускает: %d %v", status, body)
	}
}

// ТОМ СТИРАЕТСЯ ТОЛЬКО ЯВНО. Это порча 4 из приёмки: личность не стирается молча.
func TestТомСтираетсяТолькоЯвнымКлючом(t *testing.T) {
	ш := новаяРаздача(t, "тайна")
	st := поднять(t, подъёмИСнятие(t, ш))
	завестиСкоуп(t, st, ш)

	status, body := снятьСкоуп(t, st, ш, "?with-state=1")
	if status != http.StatusOK {
		t.Fatalf("скоуп не снялся: %d %v", status, body)
	}
	if !st.fake.Called("remote.sh", "drop", "vps", "--with-state") {
		t.Fatalf("ключ стирания не доехал до подъёма: %s", st.fake.Line(0))
	}
	if ш.естьЛичность() {
		t.Fatal("сказали «стёрли», а личность в томе осталась")
	}
	note, _ := body["note"].(string)
	if !strings.Contains(note, "стёрт") || !strings.Contains(note, "личност") {
		t.Fatalf("стирание личности не названо вслух: %q", note)
	}
}

// Повторный вызов по тому же адресу — ВНЯТНЫЙ ОТКАЗ, а не падение и не молчаливое «ок».
func TestПовторноеСнятиеОтказываетВнятно(t *testing.T) {
	ш := новаяРаздача(t, "тайна")
	st := поднять(t, подъёмИСнятие(t, ш))
	завестиСкоуп(t, st, ш)

	if status, body := снятьСкоуп(t, st, ш, ""); status != http.StatusOK {
		t.Fatalf("первое снятие отдало %d: %v", status, body)
	}
	status, body := снятьСкоуп(t, st, ш, "")
	if status != http.StatusNotFound {
		t.Fatalf("повторное снятие отдало %d: %v", status, body)
	}
	отказ(t, body, "no-share")
}

// ПАРОЛЬ ДОКАЗЫВАЕТ, ЧТО СКОУП ТВОЙ, а машина — то, где его снимать. Без них снятие не
// бывает, и отказы у них разные: первое чинится паролем, второе — названной машиной.
func TestСнятиеТребуетПароляМашиныИНеБеретЛичность(t *testing.T) {
	ш := новаяРаздача(t, "тайна")
	st := поднять(t, подъёмИСнятие(t, ш))
	завестиСкоуп(t, st, ш)

	_, body := st.зов(t, "DELETE", "/api/scope", `{"scope":{"addr":"`+ш.URL+`","password":""},
		"machine":{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"к"}}}`, "")
	отказ(t, body, "no-password")

	_, body = st.зов(t, "DELETE", "/api/scope", `{"scope":{"addr":"`+ш.URL+`","password":"не-та"},
		"machine":{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"к"}}}`, "")
	отказ(t, body, "bad-password")

	_, body = st.зов(t, "DELETE", "/api/scope", `{"scope":{"addr":"`+ш.URL+`","password":"тайна"}}`, "")
	отказ(t, body, "no-machine")

	// Личность в теле снятия — не поле, а недоразумение: снимают то, что лежит по адресу.
	// Молча проглоченное поле выглядит как сработавшее, поэтому оно краснеет.
	_, body = st.зов(t, "DELETE", "/api/scope", `{"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"identity":{"name":"егор"},
		"machine":{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"к"}}}`, "")
	отказ(t, body, "bad-body")

	// Ни один из отказов не тронул ни машину, ни раздачу.
	if st.fake.Called("remote.sh", "drop") {
		t.Fatalf("отказ успел снять раздачу: %s", st.fake.Line(0))
	}
	if !ш.стоит() {
		t.Fatal("раздача упала на отказе — отказ не вправе ничего менять")
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ СНЯЛИ СКОУП — МИР УХОДИТ С МАШИНЫ СОВСЕМ, включая строку в её authorized_keys.       │
// │                                                                                      │
// │ Проверяется на НАСТОЯЩЕМ ssh-обмене и смотрим В ФАЙЛ МАШИНЫ: ответ ручки сказал бы    │
// │ что угодно. Заходили паролем — значит строку положили мы, и убрать её обязаны мы же.  │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestСнятиеСкоупаУходитСМашины(t *testing.T) {
	пароль := "пароль-машины-снятия"
	ш := новаяРаздача(t, "тайна")
	st := поднять(t, подъёмИСнятие(t, ш))
	машина := машинаСПаролем(t, пароль)

	тело := func(что string) string {
		return `{"scope":{"addr":"` + ш.URL + `","password":"тайна"},` + что +
			`"machine":{"name":"vps","addr":"world@` + машина.Addr + `","creds":{"kind":"password","value":"` + пароль + `"}}}`
	}
	if status, body := st.зов(t, "POST", "/api/scope", тело(`"identity":{"name":"егор","brand":""},`), ""); status != http.StatusCreated {
		t.Fatalf("скоуп не завёлся: %d %v", status, body)
	}
	if лежит := strings.TrimSpace(машина.Authorized()); лежит != "" {
		t.Fatalf("после заведения на машине осталась строка:\n%s", лежит)
	}

	status, body := st.зов(t, "DELETE", "/api/scope", тело(""), "")
	if status != http.StatusOK {
		t.Fatalf("скоуп не снялся: %d %v", status, body)
	}
	if лежит := strings.TrimSpace(машина.Authorized()); лежит != "" {
		t.Fatalf("сняли раздачу, а свою строку на машине оставили:\n%s", лежит)
	}
	// Пароль не живёт нигде — ни на снятии тоже.
	if строка, _ := json.Marshal(body); strings.Contains(string(строка), пароль) {
		t.Fatal("пароль машины вернулся человеку в ответе снятия")
	}
	if strings.Contains(strings.Join(st.log, " "), пароль) {
		t.Fatal("пароль машины утёк в журнал на снятии")
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ТОМ ПЕРЕЖИЛ СНЯТИЕ — ЗНАЧИТ ЗАВЕДЕНИЕ НЕ ПИШЕТ ПОВЕРХ ТОГО, ЧТО В НЁМ ЛЕЖИТ.         │
// │                                                                                      │
// │ Обещание «личность переживает снятие» проверяется не словами в ответе, а следующим    │
// │ ходом: подняли раздачу на старом томе — и она отдала прежнюю личность. Пиши заведение │
// │ поверх, обещание было бы ложью через один вызов, и стёрлось бы молча.                 │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestЗаведениеНеПишетПоверхУцелевшейЛичности(t *testing.T) {
	ш := новаяРаздача(t, "тайна")
	st := поднять(t, подъёмИСнятие(t, ш))
	завестиСкоуп(t, st, ш)
	if status, body := снятьСкоуп(t, st, ш, ""); status != http.StatusOK {
		t.Fatalf("скоуп не снялся: %d %v", status, body)
	}

	status, body := st.зов(t, "POST", "/api/scope", `{
		"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"identity":{"name":"другой","brand":""},
		"machine":{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"-----ключ-----"}}}`, "")
	if status != http.StatusConflict {
		t.Fatalf("заведение легло поверх уцелевшей личности: %d %v", status, body)
	}
	отказ(t, body, "scope-exists")
	if ш.файл(t).Identity.Name != "егор" {
		t.Fatalf("прежняя личность перезаписана: %+v", ш.файл(t).Identity)
	}
	// Раздачу при этом не снимаем: она уже отдаёт ту личность, и снять её значило бы
	// отобрать у юзера единственный вход в неё. Отказ говорит, чем войти.
	if !ш.стоит() {
		t.Fatal("отказ снёс раздачу, в которой лежит уцелевшая личность")
	}
	if !strings.Contains(strings.Join(waysOf(t, body), " "), "/api/session") {
		t.Fatalf("отказ не сказал, как войти в найденную личность: %v", body)
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ СНЯЛИ СКОУП — С МАШИНЫ УХОДЯТ ОБЕ НАШИ СТРОКИ, а не только та, что положена сейчас.  │
// │                                                                                      │
// │ Их бывает две, и появились они по разным поводам: временная — этим самым снятием      │
// │ (паролем, чтобы дотянуться), и строка КЛЮЧА СКОУПА — тем ходом, которым юзер завёл    │
// │ на этой машине участок. Скоуп уходит целиком, значит и доступ, выданный ради него.    │
// │ Убери мир только свою временную — на машине осталась бы ровно та строка, из-за        │
// │ которой эта задача и заведена (живой прогон: одиннадцать штук на одной машине).       │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestСнятиеСкоупаЗабираетИКлючСамогоСкоупа(t *testing.T) {
	пароль := "пароль-обеих-строк"
	ш := новаяРаздача(t, "тайна")
	st := поднять(t, подъёмИСнятие(t, ш))
	машина := машинаСПаролем(t, пароль)
	где := `"machine":{"name":"vps","addr":"world@` + машина.Addr + `","creds":{"kind":"password","value":"` + пароль + `"}}}`

	if status, body := st.зов(t, "POST", "/api/scope", `{"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"identity":{"name":"егор","brand":""},`+где, ""); status != http.StatusCreated {
		t.Fatalf("скоуп не завёлся: %d %v", status, body)
	}
	// Участок на ТОЙ ЖЕ машине — отдельное решение юзера. Вот теперь ключ скоупа лежит и в
	// скоупе, и строкой на машине: мир туда ходит.
	if status, body := st.зов(t, "POST", "/api/resources",
		`{"name":"vps","addr":"world@`+машина.Addr+`","creds":{"kind":"password","value":"`+пароль+`"}}`, "metka"); status != http.StatusCreated {
		t.Fatalf("участок не завёлся: %d %v", status, body)
	}
	if strings.TrimSpace(машина.Authorized()) == "" {
		t.Fatal("после заведения участка на машине нет строки — проверять нечего")
	}

	status, body := st.зов(t, "DELETE", "/api/scope", `{"scope":{"addr":"`+ш.URL+`","password":"тайна"},`+где, "")
	if status != http.StatusOK {
		t.Fatalf("скоуп не снялся: %d %v", status, body)
	}
	if лежит := strings.TrimSpace(машина.Authorized()); лежит != "" {
		t.Fatalf("после снятия скоупа на машине остались наши строки:\n%s", лежит)
	}
	// И сказано вслух, что участок на этой машине ушёл вместе с ключом: вернуть скоуп на
	// уцелевший том мало — дорогу к участку придётся завести заново.
	dropped, _ := body["dropped"].(map[string]any)
	note, _ := dropped["note"].(string)
	if !strings.Contains(note, "участок") {
		t.Fatalf("про участок на этой машине не сказано ничего: %v", dropped)
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ «В ЛЮБОМ ИСХОДЕ» ЗНАЧИТ И В НЕУДАЧНОМ — иначе это «на удачном пути» (находка ревью   │
// │ `WORLD2-152`).                                                                       │
// │                                                                                      │
// │ Строку с чужой машины убирают ДВА разных куска кода: удачный путь и отложенная        │
// │ уборка. Пока стерёгся только первый, перевёрнутое условие во втором проходило молча:  │
// │ снятие отказывало, а доступ, выданный ради него, оставался у юзера навсегда — ровно    │
// │ та беда, ради которой заведена вся задача.                                            │
// │                                                                                      │
// │ Ломаем НАСТОЯЩИМ отказом снятия: контроллер уже зашёл паролем и уже положил строку,   │
// │ и только потом снятие не состоялось. Смотрим В ФАЙЛ МАШИНЫ.                           │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestУпавшееСнятиеУбираетКлючСМашины(t *testing.T) {
	пароль := "пароль-упавшего-снятия"
	ш := новаяРаздача(t, "тайна")
	снятиеПадает := false
	st := поднять(t, func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") && содержит(c.Args, "drop") && снятиеПадает {
			return run.Result{Code: 1, Out: "REMOTE-REFUSAL: down-failed\n"}, nil
		}
		return подъёмИСнятие(t, ш)(c)
	})
	машина := машинаСПаролем(t, пароль)
	где := `"machine":{"name":"vps","addr":"world@` + машина.Addr + `","creds":{"kind":"password","value":"` + пароль + `"}}}`

	if status, body := st.зов(t, "POST", "/api/scope", `{"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"identity":{"name":"егор","brand":""},`+где, ""); status != http.StatusCreated {
		t.Fatalf("скоуп не завёлся: %d %v", status, body)
	}

	снятиеПадает = true
	status, body := st.зов(t, "DELETE", "/api/scope", `{"scope":{"addr":"`+ш.URL+`","password":"тайна"},`+где, "")
	if status < 400 {
		t.Fatalf("снятие прошло, хотя подъём отказал — проверяется не то: %d %v", status, body)
	}
	отказ(t, body, "down-failed")
	if лежит := strings.TrimSpace(машина.Authorized()); лежит != "" {
		t.Fatalf("снятие отказало, а наша строка осталась на чужой машине:\n%s", лежит)
	}
	// И раздача не тронута: отказ не снимает того, что снять не смог.
	if !ш.стоит() {
		t.Fatal("снятие отказало, а раздача упала")
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ СНЯЛИ ЛИ ТУ ВЕЩЬ — ИЗМЕРЯЕТСЯ. Находка живого прогона (`WORLD2-152`, ревью).         │
// │                                                                                      │
// │ Имя раздачи собрано из ИМЕНИ УЧАСТКА, а его называет юзер — и назвать может ДРУГОЕ.  │
// │ Компоуз честно снимает проект, которого на машине нет, и выходит нулём: мир отвечал  │
// │ «снято», а раздача жила дальше. Успех, выведенный из кода возврата, вместо            │
// │ измеренного (`WORLD2` 4.2 п. 5).                                                      │
// │                                                                                      │
// │ Мерим тем, что назвал юзер, — АДРЕСОМ: отвечает, значит снятое им не было.            │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestСнятиеЧужимИменемУчасткаОтказываетАНеОтчитываетсяУспехом(t *testing.T) {
	ш := новаяРаздача(t, "тайна")
	st := поднять(t, подъёмИСнятие(t, ш))
	завестиСкоуп(t, st, ш) // участок назван «vps»

	status, body := st.зов(t, "DELETE", "/api/scope", `{
		"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"machine":{"name":"drugoe","addr":"world@10.8.0.5","creds":{"kind":"key","value":"-----ключ-----"}}}`, "")
	if status != http.StatusConflict {
		t.Fatalf("снятие чужим именем участка отдало %d: %v", status, body)
	}
	отказ(t, body, "share-alive")
	// Раздача жива — и это главное: ответ «снято» на живой раздаче и был бедой.
	if !ш.стоит() {
		t.Fatal("раздача упала — тогда проверяется не то, что нашли живьём")
	}
	// В отказе названо, ЧТО снималось на самом деле, — и названо словом СОСЕДА.
	if !strings.Contains(жсонСтрокой(t, body), "drugoe-") {
		t.Fatalf("не сказано, какую вещь снимали на машине: %v", body)
	}
	// И выход зовёт назвать то же имя участка, что при заведении.
	if !strings.Contains(strings.Join(waysOf(t, body), " "), "имя участка") {
		t.Fatalf("отказ не сказал, что чинить: %v", body)
	}

	// А тем же именем, каким заводили, — снимается.
	status, body = снятьСкоуп(t, st, ш, "")
	if status != http.StatusOK {
		t.Fatalf("снятие своим именем отдало %d: %v", status, body)
	}
	if ш.стоит() {
		t.Fatal("ответили «снято», а раздача отвечает")
	}
}

// жсонСтрокой — ответ целиком одной строкой, чтобы искать в нём то, что обязано быть.
func жсонСтрокой(t *testing.T, body map[string]any) string {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ЖИВАЯ ПУСТАЯ РАЗДАЧА — ТОЖЕ НЕ СНЯТАЯ. Находка ревью, третий круг.                   │
// │                                                                                      │
// │ `Look` отвечает тремя значениями, и «отвечает пустотой» (`PresenceEmpty`) — это стоящая│
// │ раздача, у которой нет состояния: юзер стёр его руками, либо том пережил снятие, а    │
// │ контейнер поднялся заново. Сторож, стоящий на одном `PresenceState`, пропускает этот   │
// │ случай молча — и мир снова отвечает «снято» живой раздаче.                             │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestЖиваяПустаяРаздачаТожеНеСнята(t *testing.T) {
	ш := новаяРаздача(t, "тайна")
	st := поднять(t, подъёмИСнятие(t, ш))
	завестиСкоуп(t, st, ш) // участок назван «vps»

	// Состояния в раздаче больше нет, а сама она стоит: `GET` отвечает «404 пусто».
	ш.стеретьТом()
	if !ш.стоит() {
		t.Fatal("раздача упала — проверяется тогда не тот случай")
	}

	status, body := st.зов(t, "DELETE", "/api/scope", `{
		"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"machine":{"name":"drugoe","addr":"world@10.8.0.5","creds":{"kind":"key","value":"-----ключ-----"}}}`, "")
	if status != http.StatusConflict {
		t.Fatalf("живая ПУСТАЯ раздача сочтена снятой: %d %v", status, body)
	}
	отказ(t, body, "share-alive")
	if !ш.стоит() {
		t.Fatal("раздача упала на отказе")
	}
	// Отказ различает два живых исхода словами: «пустая» и «с личностью» чинятся по-разному.
	if why, _ := body["why"].(string); !strings.Contains(why, "состояния в ней нет") {
		t.Fatalf("отказ не сказал, ЧТО именно стоит по адресу: %q", why)
	}

	// А своим именем участка снимается и пустая.
	status, body = снятьСкоуп(t, st, ш, "")
	if status != http.StatusOK {
		t.Fatalf("пустая раздача своим именем не снялась: %d %v", status, body)
	}
	if ш.стоит() {
		t.Fatal("ответили «снято», а раздача отвечает")
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ НЕИЗМЕРЕННОЕ НАЗЫВАЕТСЯ НЕИЗМЕРЕННЫМ (`WORLD2` 4.2). Находка ревью, третий круг.     │
// │                                                                                      │
// │ Четвёртый исход вопроса — не значение, а ОТКАЗ: по адресу отвечают, но не тем, и      │
// │ сказать «сняли» нельзя, как нельзя и отказать — снятие-то прошло. Оговорка была, но её │
// │ не стерёг никто: сотри её — и ответ молча становится обещанием.                        │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestНеизмеренноеСнятиеНазываетсяВслух(t *testing.T) {
	ш := новаяРаздача(t, "тайна")
	var st *стенд
	st = поднять(t, func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") && содержит(c.Args, "drop") &&
			значение(c.Env, "SHARE_NAME") != "" {
			// Снятие прошло, а по адресу теперь отвечает не раздача: измерить нечем.
			ш.ломать()
			return run.Result{Out: "REMOTE-THING: " + значение(c.Env, "SHARE_NAME") + "\n"}, nil
		}
		return подъёмИСнятие(t, ш)(c)
	})
	завестиСкоуп(t, st, ш)

	status, body := снятьСкоуп(t, st, ш, "")
	if status != http.StatusOK {
		t.Fatalf("снятие отдало %d: %v", status, body)
	}
	note, _ := body["note"].(string)
	if !strings.Contains(note, "проверить не вышло") {
		t.Fatalf("неизмеренное выдано за снятое — оговорки в ответе нет: %q", note)
	}
	// И адрес назван: человеку идти смотреть самому, а идти он будет по нему.
	if !strings.Contains(note, ш.URL) {
		t.Fatalf("не сказано, что смотреть: %q", note)
	}
}
