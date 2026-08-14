package scope

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omnifield/world/control/internal/run"
)

func fixedNow() time.Time { return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC) }

func local(t *testing.T, dir string) *Scope {
	t.Helper()
	addr, ref := Parse(dir)
	if ref != nil {
		t.Fatalf("адрес %s не разобрался: %v", dir, ref)
	}
	return Open(addr, &run.Fake{}, "", 5, fixedNow)
}

// ── адрес ────────────────────────────────────────────────────────────────────

func TestParseРазбираетОбеФормы(t *testing.T) {
	cases := []struct {
		raw  string
		here bool
		user string
		port int
		path string
	}{
		{"/scope/егор", true, "", 0, "/scope/егор"},
		{"/scope/../scope/егор/", true, "", 0, "/scope/егор"},
		{"world@10.8.0.5:/srv/scope", false, "world@10.8.0.5", 22, "/srv/scope"},
		{"world@10.8.0.5:2222:/srv/scope", false, "world@10.8.0.5", 2222, "/srv/scope"},
	}
	for _, c := range cases {
		addr, ref := Parse(c.raw)
		if ref != nil {
			t.Fatalf("%s: неожиданный отказ %s", c.raw, ref.Code)
		}
		if addr.Here != c.here || addr.UserAt != c.user || addr.Path != c.path {
			t.Fatalf("%s разобрался как here=%v user=%q path=%q", c.raw, addr.Here, addr.UserAt, addr.Path)
		}
		if !c.here && addr.Port != c.port {
			t.Fatalf("%s: порт %d вместо %d", c.raw, addr.Port, c.port)
		}
	}
}

func TestParseОтказываетКодом(t *testing.T) {
	cases := map[string]string{
		"":                       "no-address",
		"   ":                    "no-address",
		"просто-слово":           "bad-address",
		"@10.8.0.5:/srv/scope":   "bad-address", // юзер не назван — молча брать текущего нельзя
		"world@10.8.0.5":         "bad-address", // пути нет
		"world@:/srv/scope":      "bad-address", // машины нет
		"world@10.8.0.5:xx:/srv": "bad-address", // порт не число
		"world@10.8.0.5:srv":     "bad-address", // путь не абсолютный
	}
	for raw, want := range cases {
		_, ref := Parse(raw)
		if ref == nil {
			t.Fatalf("%q: отказа не было, а обязан", raw)
		}
		if ref.Code != want {
			t.Fatalf("%q: код %s вместо %s", raw, ref.Code, want)
		}
		if len(ref.Ways) == 0 {
			t.Fatalf("%q: отказ без выхода — тупик (WORLD2 2.3)", raw)
		}
	}
}

// ── скоуп здесь ──────────────────────────────────────────────────────────────

func TestЗавестиИВойти(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "scope")
	sc := local(t, dir)

	id, ref := sc.Create(context.Background(), "егор", "омнифилд")
	if ref != nil {
		t.Fatalf("личность не завелась: %v", ref)
	}
	if id.Name != "егор" || id.Created != "2026-08-14T12:00:00Z" {
		t.Fatalf("личность собралась не так: %+v", id)
	}

	again, ref := sc.Enter(context.Background())
	if ref != nil {
		t.Fatalf("вход не состоялся: %v", ref)
	}
	if again.Name != "егор" || again.Brand != "омнифилд" {
		t.Fatalf("вошли не в ту личность: %+v", again)
	}

	// Список полей заведён сразу пустым — «полей нет» и «файла нет» не должны быть
	// одним и тем же состоянием.
	fields, ref := sc.Fields(context.Background())
	if ref != nil || len(fields) != 0 {
		t.Fatalf("список полей: %v %v", fields, ref)
	}
}

func TestВходБезСкоупаЗовётЗавести(t *testing.T) {
	sc := local(t, filepath.Join(t.TempDir(), "пусто"))
	_, ref := sc.Enter(context.Background())
	if ref == nil || ref.Code != "no-scope" {
		t.Fatalf("ждали no-scope, получили %v", ref)
	}
	if !strings.Contains(strings.Join(ref.Ways, " "), "create") {
		t.Fatalf("отказ не назвал выход «завести здесь»: %v", ref.Ways)
	}
}

func TestЗавестиПоверхЛичностиНельзя(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "scope")
	sc := local(t, dir)
	if _, ref := sc.Create(context.Background(), "егор", ""); ref != nil {
		t.Fatalf("первая личность не завелась: %v", ref)
	}
	_, ref := sc.Create(context.Background(), "другой", "")
	if ref == nil || ref.Code != "scope-exists" {
		t.Fatalf("ждали scope-exists, получили %v", ref)
	}
	// И главное: та, что была, осталась цела.
	id, _ := sc.Enter(context.Background())
	if id.Name != "егор" {
		t.Fatalf("личность затёрта: %+v", id)
	}
}

func TestЗавестиНаЧужомРесурсеОтказ(t *testing.T) {
	addr, _ := Parse("world@10.8.0.5:/srv/scope")
	sc := Open(addr, &run.Fake{}, "", 5, fixedNow)
	_, ref := sc.Create(context.Background(), "егор", "")
	if ref == nil || ref.Code != "create-elsewhere" {
		t.Fatalf("ждали create-elsewhere, получили %v", ref)
	}
}

func TestПоляЗаводятсяИНеДублируются(t *testing.T) {
	sc := local(t, filepath.Join(t.TempDir(), "scope"))
	if _, ref := sc.Create(context.Background(), "егор", ""); ref != nil {
		t.Fatalf("личность не завелась: %v", ref)
	}

	field, fields, ref := sc.AddField(context.Background(), "дом")
	if ref != nil || field.Name != "дом" || len(fields) != 1 {
		t.Fatalf("поле не завелось: %v %v %v", field, fields, ref)
	}
	if _, _, ref := sc.AddField(context.Background(), "дом"); ref == nil || ref.Code != "field-exists" {
		t.Fatalf("ждали field-exists, получили %v", ref)
	}
	if _, _, ref := sc.AddField(context.Background(), "  "); ref == nil || ref.Code != "no-name" {
		t.Fatalf("ждали no-name, получили %v", ref)
	}

	// Поля лежат в СКОУПЕ, а не в памяти процесса: новый доступ по тому же адресу
	// обязан их видеть — иначе это была бы копия, а обещана связь.
	fresh := local(t, sc.Addr.Path)
	got, _ := fresh.Fields(context.Background())
	if len(got) != 1 || got[0].Name != "дом" {
		t.Fatalf("поля не доехали до места: %v", got)
	}
}

func TestПокалеченнаяЛичностьНеПритворяетсяВходом(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, identityFile), []byte("{это не json"), 0o600); err != nil {
		t.Fatal(err)
	}
	sc := local(t, dir)
	_, ref := sc.Enter(context.Background())
	if ref == nil || ref.Code != "scope-broken" {
		t.Fatalf("ждали scope-broken, получили %v", ref)
	}
}

// ── скоуп по связи ───────────────────────────────────────────────────────────

func TestУдалённыйСкоупЧитаетсяСвязью(t *testing.T) {
	id := Identity{Name: "егор", Brand: "омнифилд"}
	data, _ := json.Marshal(id)

	fake := &run.Fake{Answer: func(c run.Command) (run.Result, error) {
		return run.Result{Out: string(data)}, nil
	}}
	addr, _ := Parse("world@10.8.0.5:2222:/srv/scope")
	sc := Open(addr, fake, "/keys/scope-key", 7, fixedNow)

	got, ref := sc.Enter(context.Background())
	if ref != nil {
		t.Fatalf("вход по связи не состоялся: %v", ref)
	}
	if got.Name != "егор" {
		t.Fatalf("прочитали не ту личность: %+v", got)
	}

	line := fake.Line(0)
	for _, want := range []string{
		"ssh", "BatchMode=yes", "ConnectTimeout=7", "-p 2222",
		"-i /keys/scope-key", "IdentitiesOnly=yes", "world@10.8.0.5",
		"'/srv/scope/identity.json'",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("в вызове ssh нет %q:\n%s", want, line)
		}
	}
}

// Скоуп никуда не копируется — ни в файл, ни в память между вызовами: каждое чтение
// уходит туда, где скоуп лежит (`WORLD2` 1.6).
func TestУдалённыйСкоупНеКэшируется(t *testing.T) {
	calls := 0
	fake := &run.Fake{Answer: func(c run.Command) (run.Result, error) {
		calls++
		return run.Result{Out: `{"name":"егор"}`}, nil
	}}
	addr, _ := Parse("world@10.8.0.5:/srv/scope")
	sc := Open(addr, fake, "", 5, fixedNow)

	_, _ = sc.Enter(context.Background())
	_, _ = sc.Enter(context.Background())
	if calls != 2 {
		t.Fatalf("вход сходил до места %d раз(а) вместо 2 — где-то завёлся кэш", calls)
	}
}

func TestПутьСКавычкойНеЛомаетКоманду(t *testing.T) {
	var got string
	fake := &run.Fake{Answer: func(c run.Command) (run.Result, error) {
		got = c.Args[len(c.Args)-1]
		return run.Result{Code: missingMarker}, nil
	}}
	addr, _ := Parse("world@10.8.0.5:/srv/it's")
	sc := Open(addr, fake, "", 5, fixedNow)
	_, _ = sc.Enter(context.Background())

	if !strings.Contains(got, `'/srv/it'\''s/identity.json'`) {
		t.Fatalf("кавычка в пути не экранирована — команда на той стороне разъедется:\n%s", got)
	}
}

func TestНетФайлаНаТойСторонеЭтоНеПоломка(t *testing.T) {
	fake := &run.Fake{Answer: func(run.Command) (run.Result, error) {
		return run.Result{Code: missingMarker}, nil
	}}
	addr, _ := Parse("world@10.8.0.5:/srv/scope")
	sc := Open(addr, fake, "", 5, fixedNow)

	_, ref := sc.Enter(context.Background())
	if ref == nil || ref.Code != "no-scope" {
		t.Fatalf("ждали no-scope (приглашение завести), получили %v", ref)
	}
}

// Ступени те же, что у шлюза: дорога · ответ · доступ. Чинят их разные люди, и общий
// отказ «не дотянулись» отправляет человека чинить наугад.
func TestСтупениСвязиРазличаются(t *testing.T) {
	cases := map[string]string{
		"world@10.8.0.5: Permission denied (publickey).":                       "access-denied",
		"ssh: connect to host 10.8.0.5 port 22: No route to host":              "no-route",
		"ssh: connect to host 10.8.0.5 port 22: Connection refused":            "no-answer",
		"ssh: Could not resolve hostname нет-такой: Name or service not known": "no-route",
		"что-то, чего мы не знаем":                                             "scope-unreachable",
	}
	for stderr, want := range cases {
		fake := &run.Fake{Answer: func(run.Command) (run.Result, error) {
			return run.Result{Code: 255, Err: stderr}, nil
		}}
		addr, _ := Parse("world@10.8.0.5:/srv/scope")
		sc := Open(addr, fake, "", 5, fixedNow)

		_, ref := sc.Enter(context.Background())
		if ref == nil || ref.Code != want {
			t.Fatalf("%q → %v, а ждали %s", stderr, ref, want)
		}
		if len(ref.Ways) == 0 {
			t.Fatalf("%q: отказ без выхода", stderr)
		}
	}
}

func TestЗаписьПоСвязиИдётЧерезВремянку(t *testing.T) {
	var remote, stdin string
	fake := &run.Fake{Answer: func(c run.Command) (run.Result, error) {
		remote, stdin = c.Args[len(c.Args)-1], c.Stdin
		return run.Result{}, nil
	}}
	addr, _ := Parse("world@10.8.0.5:/srv/scope")
	sc := Open(addr, fake, "", 5, fixedNow)

	if ref := sc.writeJSON(context.Background(), fieldsFile, []Field{{Name: "дом"}}); ref != nil {
		t.Fatalf("запись не прошла: %v", ref)
	}
	for _, want := range []string{"mkdir -p '/srv/scope'", "cat > '/srv/scope/.fields.json.tmp'", "mv '/srv/scope/.fields.json.tmp' '/srv/scope/fields.json'"} {
		if !strings.Contains(remote, want) {
			t.Fatalf("в команде записи нет %q:\n%s", want, remote)
		}
	}
	if !strings.Contains(stdin, `"дом"`) {
		t.Fatalf("тело не доехало: %s", stdin)
	}
}

func TestБезSshСкоупНаЧужомРесурсеОтказываетВнятно(t *testing.T) {
	fake := &run.Fake{Answer: func(run.Command) (run.Result, error) {
		return run.Result{}, run.ErrNoTool
	}}
	addr, _ := Parse("world@10.8.0.5:/srv/scope")
	sc := Open(addr, fake, "", 5, fixedNow)

	_, ref := sc.Enter(context.Background())
	if ref == nil || ref.Code != "no-ssh" {
		t.Fatalf("ждали no-ssh, получили %v", ref)
	}
}
