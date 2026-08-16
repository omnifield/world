package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omnifield/world/control/internal/run"
)

// Ручки проверяются ЦЕЛИКОМ, вместе с формой ответа: пульт (`web`) делается по этой же
// таблице, и разъехавшаяся форма — это не косметика, а неработающий вход.

type стенд struct {
	*httptest.Server
	fake    *run.Fake
	keys    string
	scope   string
	pult    string
	recipes string
	log     []string
}

func поднять(t *testing.T, answer func(run.Command) (run.Result, error)) *стенд {
	t.Helper()
	dir := t.TempDir()
	st := &стенд{
		fake:    &run.Fake{Answer: answer},
		keys:    filepath.Join(dir, "keys"),
		scope:   filepath.Join(dir, "scope"),
		pult:    filepath.Join(dir, "pult"),
		recipes: filepath.Join(dir, "recipes"),
	}
	if err := os.MkdirAll(st.recipes, 0o755); err != nil {
		t.Fatal(err)
	}
	h := New(Options{
		Runner:     st.fake,
		RemoteSh:   "/opt/world/deploy/remote.sh",
		RecipesDir: st.recipes,
		DoorRecipe: дверьРецепт,
		Docker:     "docker",
		KeysDir:    st.keys,
		PultDir:    st.pult,
		DoorPort:   8080,
		SSHTimeout: 5,
		Now:        func() time.Time { return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC) },
		NewToken:   func() (string, error) { return "метка", nil },
		Logf:       func(f string, a ...any) { st.log = append(st.log, f) },
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
	var rd *strings.Reader = strings.NewReader(body)
	req, err := http.NewRequest(method, s.URL+path, rd)
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

// докерОтвечает — подставной докер, у которого на ресурсе стоит одна здоровая вещь. Он не
// притворяется докером: отвечает ровно те поля, которые контроллер спросил.
func докерОтвечает(c run.Command) (run.Result, error) {
	switch {
	case strings.HasSuffix(c.Name, "remote.sh"):
		return run.Result{}, nil
	case содержит(c.Args, "context"):
		return run.Result{Out: "world-vps\tssh://world@10.8.0.5\n"}, nil
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

// ── вход ─────────────────────────────────────────────────────────────────────

func TestПервыйВходЗаводитЛичностьЗдесь(t *testing.T) {
	st := поднять(t, докерОтвечает)

	status, body := st.зов(t, "POST", "/api/session",
		`{"addr":"`+st.scope+`","create":true,"name":"егор","brand":"омнифилд"}`, "")
	if status != http.StatusCreated {
		t.Fatalf("вход отдал %d: %v", status, body)
	}
	if body["name"] != "егор" || body["brand"] != "омнифилд" || body["created"] != true {
		t.Fatalf("ответ входа собрался не так: %v", body)
	}
	if body["token"] != "метка" {
		t.Fatalf("метка сессии не отдана — курлом в контроллер не походишь: %v", body)
	}

	status, body = st.зов(t, "GET", "/api/me", "", "метка")
	if status != http.StatusOK || body["name"] != "егор" {
		t.Fatalf("«кто я» отдал %d: %v", status, body)
	}
	scope, _ := body["scope"].(map[string]any)
	if scope["here"] != true {
		t.Fatalf("скоуп назвался не тем местом: %v", scope)
	}
}

func TestБезВходаНиРесурсовНиПолей(t *testing.T) {
	st := поднять(t, докерОтвечает)
	for _, path := range []string{"/api/me", "/api/resources", "/api/fields"} {
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
	st.зов(t, "POST", "/api/session", `{"addr":"`+st.scope+`","create":true,"name":"егор"}`, "")

	status, body := st.зов(t, "GET", "/api/me", "", "не-та-метка")
	if status != http.StatusUnauthorized {
		t.Fatalf("чужая метка пустила: %d %v", status, body)
	}
	отказ(t, body, "not-signed-in")
}

func TestВходБезСкоупаПредлагаетЗавестиЕго(t *testing.T) {
	st := поднять(t, докерОтвечает)
	status, body := st.зов(t, "POST", "/api/session", `{"addr":"`+st.scope+`"}`, "")
	if status != http.StatusNotFound {
		t.Fatalf("ждали «скоупа нет», получили %d: %v", status, body)
	}
	отказ(t, body, "no-scope")
}

func TestКредыКСкоупуЗдесьНеПроглатываютсяМолча(t *testing.T) {
	st := поднять(t, докерОтвечает)
	status, body := st.зов(t, "POST", "/api/session",
		`{"addr":"`+st.scope+`","creds":"ключ","create":true,"name":"егор"}`, "")
	if status != http.StatusBadRequest {
		t.Fatalf("креды к местному скоупу приняты молча: %d %v", status, body)
	}
	отказ(t, body, "creds-here")
}

func TestКредыКЧужомуСкоупуЛожатсяВСвязку(t *testing.T) {
	st := поднять(t, func(c run.Command) (run.Result, error) {
		if c.Name == "ssh" {
			return run.Result{Out: `{"name":"егор","brand":"омнифилд"}`}, nil
		}
		return докерОтвечает(c)
	})

	status, body := st.зов(t, "POST", "/api/session",
		`{"addr":"world@10.8.0.5:/srv/scope","creds":"-----ключ-----"}`, "")
	if status != http.StatusOK {
		t.Fatalf("вход по связи отдал %d: %v", status, body)
	}
	if !st.fake.Called("ssh", "-i "+filepath.Join(st.keys, "scope-key")) {
		t.Fatalf("ssh пошёл не тем ключом: %s", st.fake.Line(0))
	}
}

// ── источники ресурса ────────────────────────────────────────────────────────

func TestИсточниковСтановитсяДва(t *testing.T) {
	st := поднять(t, докерОтвечает)
	st.зов(t, "POST", "/api/session", `{"addr":"`+st.scope+`","create":true,"name":"егор"}`, "")

	status, body := st.зов(t, "POST", "/api/resources",
		`{"name":"vps","addr":"world@10.8.0.5","creds":"ключ"}`, "метка")
	if status != http.StatusCreated {
		t.Fatalf("ресурс не добавился: %d %v", status, body)
	}
	list, _ := body["resources"].([]any)
	if len(list) != 2 {
		t.Fatalf("источников %d, а человек обязан увидеть два: %v", len(list), body)
	}
	first, _ := list[0].(map[string]any)
	if first["here"] != true {
		t.Fatalf("первым обязан идти ресурс контроллера: %v", first)
	}
	if !st.fake.Called("remote.sh", "add", "vps") {
		t.Fatal("подъём вещи написан заново вместо готового")
	}
	// Ресурс — МАШИНА, а что на ней стоит — отдельное поле: список вещей, а не одна дверь
	// (`WORLD2-131`). Пульт делается по этой же таблице.
	второй, _ := list[1].(map[string]any)
	if второй["reach"] != "отвечает" {
		t.Fatalf("ресурс не сказал, отвечает ли он сам: %v", второй)
	}
	things, _ := второй["things"].([]any)
	if len(things) != 1 {
		t.Fatalf("список вещей на ресурсе не собрался: %v", второй)
	}
	вещь, _ := things[0].(map[string]any)
	if вещь["name"] != "world" || вещь["alive"] != true || вещь["state"] == "" {
		t.Fatalf("вещь описана не так: %v", вещь)
	}
}

// Рецепт — то, ЧТО поднимается. Он доезжает до подъёма ключом, а не подразумевается его
// умолчанием: умолчание принадлежит команде соседа, и опора на него однажды подняла бы
// не ту вещь.
func TestРецептДоезжаетДоПодъёма(t *testing.T) {
	st := поднять(t, докерОтвечает)
	st.зов(t, "POST", "/api/session", `{"addr":"`+st.scope+`","create":true,"name":"егор"}`, "")

	весы := filepath.Join(st.recipes, "весы.yaml")
	if err := os.WriteFile(весы, []byte("name: весы\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	status, body := st.зов(t, "POST", "/api/resources",
		`{"name":"vps","addr":"world@10.8.0.5","recipe":"весы"}`, "метка")
	if status != http.StatusCreated {
		t.Fatalf("вторая вещь не поднялась: %d %v", status, body)
	}
	if !st.fake.Called("remote.sh", "add", "vps", "--recipe", весы) {
		t.Fatalf("подъём позван не тем рецептом: %s", st.fake.Line(0))
	}

	// Снятие называет рецепт тем же способом — своего реестра вещей зона не заводит.
	if status, body = st.зов(t, "DELETE", "/api/resources/vps?recipe=весы", "", "метка"); status != http.StatusOK {
		t.Fatalf("снятие отдало %d: %v", status, body)
	}
	if !st.fake.Called("remote.sh", "drop", "vps", "--recipe", весы) {
		t.Fatalf("снятие пошло не тем рецептом: %s", st.fake.Line(len(st.fake.Calls())-1))
	}
}

// Список того, чем контроллер умеет поднимать, ЧИТАЕТСЯ из каталога: положили файл — вещь
// появилась, без правки кода и без пересборки образа (`WORLD2` 3.7).
func TestСписокРецептовЧитаетсяИзКаталога(t *testing.T) {
	st := поднять(t, докерОтвечает)

	status, body := st.зов(t, "GET", "/api/recipes", "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("до входа рецепты видны: %d %v", status, body)
	}
	st.зов(t, "POST", "/api/session", `{"addr":"`+st.scope+`","create":true,"name":"егор"}`, "")

	status, body = st.зов(t, "GET", "/api/recipes", "", "метка")
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
	_, body = st.зов(t, "GET", "/api/recipes", "", "метка")
	list, _ = body["recipes"].([]any)
	if len(list) != 2 {
		t.Fatalf("положенный рецепт не появился, а обязан был: %v", body)
	}
}

// Рецепт, которого нет, — наш отказ и наш код: имя рецепта это наше знание, и спрашивать
// за него соседа не за что. До ресурса при этом дело не доходит.
func TestНеизвестныйРецептОтказываетСвоимКодом(t *testing.T) {
	st := поднять(t, докерОтвечает)
	st.зов(t, "POST", "/api/session", `{"addr":"`+st.scope+`","create":true,"name":"егор"}`, "")

	status, body := st.зов(t, "POST", "/api/resources",
		`{"name":"vps","addr":"world@10.8.0.5","recipe":"часы"}`, "метка")
	if status != http.StatusNotFound {
		t.Fatalf("неизвестный рецепт отдан как %d: %v", status, body)
	}
	отказ(t, body, "no-such-recipe")
	if st.fake.Called("remote.sh") {
		t.Fatalf("подъём позвали на рецепте, которого нет: %s", st.fake.Line(0))
	}
}

func TestСнятиеГоворитЧтоОсталосьНаМашине(t *testing.T) {
	st := поднять(t, докерОтвечает)
	st.зов(t, "POST", "/api/session", `{"addr":"`+st.scope+`","create":true,"name":"егор"}`, "")

	status, body := st.зов(t, "DELETE", "/api/resources/vps", "", "метка")
	if status != http.StatusOK {
		t.Fatalf("снятие отдало %d: %v", status, body)
	}
	dropped, _ := body["dropped"].(map[string]any)
	left, _ := dropped["left"].([]any)
	if len(left) == 0 {
		t.Fatalf("сказали «сняли», не назвав оставленного: %v", dropped)
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
	st.зов(t, "POST", "/api/session", `{"addr":"`+st.scope+`","create":true,"name":"егор"}`, "")

	status, body := st.зов(t, "POST", "/api/resources", `{"name":"vps","addr":"world@10.8.0.5"}`, "метка")
	if status != http.StatusBadGateway {
		t.Fatalf("отказ подъёма отдан как %d: %v", status, body)
	}
	отказ(t, body, "access-denied")
	if body["from"] != "deploy/remote.sh" {
		t.Fatalf("не названо, чей отказ: %v", body)
	}
}

// ── поля ─────────────────────────────────────────────────────────────────────

func TestПоляПустыИЗаводятся(t *testing.T) {
	st := поднять(t, докерОтвечает)
	st.зов(t, "POST", "/api/session", `{"addr":"`+st.scope+`","create":true,"name":"егор"}`, "")

	status, body := st.зов(t, "GET", "/api/fields", "", "метка")
	fields, _ := body["fields"].([]any)
	if status != http.StatusOK || len(fields) != 0 {
		t.Fatalf("список полей: %d %v", status, body)
	}

	status, body = st.зов(t, "POST", "/api/fields", `{"name":"дом"}`, "метка")
	if status != http.StatusCreated {
		t.Fatalf("поле не завелось: %d %v", status, body)
	}
	// Говорим вслух, что поле не поднято: молчание тут выглядело бы как «подняли».
	if note, _ := body["note"].(string); !strings.Contains(note, "не поднимается") {
		t.Fatalf("не сказано, чего не произошло: %v", body)
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
	_, body = st.зов(t, "POST", "/api/session", `{"addr":"/x","адрес":"/y"}`, "")
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

	// ручка на месте, хотя пульт лежит рядом
	status, body := st.зов(t, "GET", "/api/me", "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("ручка перехвачена пультом: %d %v", status, body)
	}
	отказ(t, body, "not-signed-in")

	// опечатка в ИМЕНИ РУЧКИ остаётся промахом машины, а не «страницы нет»
	status, body = st.зов(t, "GET", "/api/мее", "", "")
	if status != http.StatusNotFound {
		t.Fatalf("неизвестная ручка отдала %d: %v", status, body)
	}
	отказ(t, body, "unknown-endpoint")

	// `/api` без косой черты — тоже отказ, а не перенаправление
	status, body = st.зов(t, "GET", "/api", "", "")
	if status != http.StatusNotFound {
		t.Fatalf("/api отдал %d (перенаправление вместо отказа?): %v", status, body)
	}
	отказ(t, body, "unknown-endpoint")

	// а вот путь мимо ручек — это уже пульт, и он отвечает страницей
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

	// Ручки при этом работают — и отказ обязан об этом говорить.
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

	// А `fetch` пульта (Accept: */*) обязан по-прежнему получать JSON.
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
