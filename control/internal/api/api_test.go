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

func (ш *раздача) ручки(w http.ResponseWriter, r *http.Request) {
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

	// Личность легла В РАЗДАЧУ, а не на контроллер, и креды машины уехали в скоуп — в
	// раздел `территории` и в связку ключей (`WORLD2` 3.4, «Два адреса»).
	файл := ш.файл(t)
	if файл.Identity.Name != "егор" || файл.Format != state.Version {
		t.Fatalf("в раздаче лежит не та личность: %+v", файл)
	}
	if len(файл.Territories) != 1 || файл.Territories[0].Name != "vps" || файл.Territories[0].Addr != "world@10.8.0.5" {
		t.Fatalf("машина не записалась участком: %+v", файл.Territories)
	}
	ключ, есть := файл.Key(файл.Territories[0].Key)
	if !есть || ключ.Value != "-----ключ-----" || ключ.Kind != state.KindSSH {
		t.Fatalf("креды машины не легли в связку скоупа: %+v", файл.Keys)
	}
	// Пустой бренд — законное состояние (`WORLD2-135`), и заведение на нём не спотыкается.
	if файл.Identity.Brand != "" {
		t.Fatalf("бренд выдумался сам: %q", файл.Identity.Brand)
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

	// Ключ, заведённый паролем, ДОЕХАЛ в скоуп — общей записью, после подъёма раздачи.
	// Без этого юзер остался бы с машиной, куда пущен ключ, которого у него нет.
	файл := ш.файл(t)
	ключ, есть := файл.Key(state.UserKeyName)
	if !есть || !strings.Contains(ключ.Value, "PRIVATE KEY") {
		t.Fatalf("ключ юзера не доехал в скоуп: %+v", файл.Keys)
	}
	if len(файл.Territories) != 1 || файл.Territories[0].Key != state.UserKeyName {
		t.Fatalf("территория ссылается не на ключ юзера: %+v", файл.Territories)
	}

	// На чужой машине — ровно одна подписанная строка, как и обещано юзеру.
	лежит := strings.TrimSpace(машина.Authorized())
	if лежит == "" || strings.Count(лежит, "\n") != 0 {
		t.Fatalf("в authorized_keys машины не одна строка:\n%s", машина.Authorized())
	}

	// Цена названа: контроллер написал в чужую машину и говорит об этом.
	if note, _ := body["note"].(string); !strings.Contains(note, "authorized_keys") {
		t.Fatalf("не сказано, что изменено на чужой машине: %v", body)
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

// На участке может стоять раздача СВОЕЙ ЖЕ личности: сняв её молча, контроллер оборвал бы
// юзеру доступ к себе, и обнаружилось бы это при следующем входе.
func TestУчастокСРаздачейСвоегоСкоупаНеСнимаетсяМолча(t *testing.T) {
	st := поднять(t, докерОтвечает)
	ш := новаяРаздача(t, "тайна")
	host, _, _ := net.SplitHostPort(ш.адрес)
	ш.поднять(t, личность(t, "егор", "", state.Territory{Name: "home", Addr: "world@" + host}))
	st.войти(t, ш)

	status, body := st.зов(t, "DELETE", "/api/resources/home", "", "metka")
	if status != http.StatusConflict {
		t.Fatalf("раздача своего скоупа снялась как %d: %v", status, body)
	}
	отказ(t, body, "drop-scope-home")
	if st.fake.Called("remote.sh", "drop") {
		t.Fatalf("до отказа успели снять вещь: %s", st.fake.Line(0))
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
func TestЗаведениеЗапоминаетИмяПоднятойВещи(t *testing.T) {
	ш := новаяРаздача(t, "тайна")
	var st *стенд
	st = поднять(t, func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") && содержит(c.Args, "add") {
			ш.поднять(t, nil)
			// Сосед называет имя вещи — тем же путём, что путь доставки и отказ.
			return run.Result{Out: "REMOTE-PATH: image-pull\nREMOTE-THING: vps-8070\n"}, nil
		}
		return докерОтвечает(c)
	})

	status, _ := st.зов(t, "POST", "/api/scope", `{
		"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"identity":{"name":"егор","brand":""},
		"machine":{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"-----ключ-----"}}}`, "")
	if status != http.StatusCreated {
		t.Fatalf("скоуп не завёлся: %d", status)
	}

	файл := ш.файл(t)
	if len(файл.Territories) != 1 || len(файл.Territories[0].Things) != 1 ||
		файл.Territories[0].Things[0] != "vps-8070" {
		t.Fatalf("скоуп не запомнил поднятую вещь: %+v", файл.Territories)
	}
}

// А ЕСЛИ СОСЕД ПРОМОЛЧАЛ — не выдумываем: имя принадлежит рецепту, и пустая память честнее
// придуманной. Список вещей у такой территории показывается целиком.
func TestМолчаниеСоседаНеПревращаетсяВВыдуманноеИмя(t *testing.T) {
	ш := новаяРаздача(t, "тайна")
	var st *стенд
	st = поднять(t, func(c run.Command) (run.Result, error) {
		if strings.HasSuffix(c.Name, "remote.sh") && содержит(c.Args, "add") {
			ш.поднять(t, nil)
			return run.Result{}, nil // ни метки, ни имени
		}
		return докерОтвечает(c)
	})

	st.зов(t, "POST", "/api/scope", `{
		"scope":{"addr":"`+ш.URL+`","password":"тайна"},
		"identity":{"name":"егор","brand":""},
		"machine":{"name":"vps","addr":"world@10.8.0.5","creds":{"kind":"key","value":"-----ключ-----"}}}`, "")

	файл := ш.файл(t)
	if len(файл.Territories) != 1 || len(файл.Territories[0].Things) != 0 {
		t.Fatalf("имя вещи выдумано на пустом месте: %+v", файл.Territories)
	}
}
