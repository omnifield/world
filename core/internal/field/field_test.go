package field

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omnifield/world/core/internal/build"
	"github.com/omnifield/world/core/internal/door"
	"github.com/omnifield/world/core/internal/guard"
	"github.com/omnifield/world/core/internal/schematest"
)

func fixedNow() time.Time { return time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC) }

// идущиеЧасы — время, которое ИДЁТ: на застывших часах «в поле с» и «вернулась»
// совпадают, и проба тождества места не отличила бы сохранённую историю от
// переписанной заново.
func идущиеЧасы() func() time.Time {
	var mu sync.Mutex
	т := fixedNow()
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		т = т.Add(time.Minute)
		return т
	}
}

func молча(string, ...any) {}

// дверь поднимает НАСТОЯЩУЮ дверь с настоящей пробой адреса. Заглушка здесь
// была бы подделкой проверки: половина отказов клиента — это отказы двери,
// и проверять их на выдуманном ответе значит проверять свою же выдумку.
func дверь(t *testing.T) string {
	return дверьСЧасами(t, fixedNow)
}

// дверьСЧасами — та же дверь с идущими часами: «в поле с» и «вернулась» на
// застывшем времени неразличимы, и проба тождества позеленела бы впустую.
func дверьСЧасами(t *testing.T, now func() time.Time) string {
	t.Helper()
	reg, err := door.Open(filepath.Join(t.TempDir(), "field", "locations.json"))
	if err != nil {
		t.Fatalf("реестр локаций не поднялся: %v", err)
	}
	h := door.NewHandler(reg, молча, now, door.DialProbe(2*time.Second))

	mux := http.NewServeMux()
	mux.Handle(door.Prefix, h)
	mux.Handle(door.Prefix+"/", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// локация — живой сторож, каким его увидит дверь.
func локация(t *testing.T, имя string) string {
	адрес, _ := локацияСоСносом(t, имя)
	return адрес
}

// локацияСоСносом — та же живая локация, но её можно СНЕСТИ посреди прогона:
// пересоздание контейнера иначе не изобразить, а тождество места проверяется
// именно им (`tasker:WORLD-81`).
func локацияСоСносом(t *testing.T, имя string) (адрес string, снести func()) {
	t.Helper()
	srv := httptest.NewServer(guard.New(имя, "проба присутствия", nil, молча, fixedNow))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), srv.Close
}

// мёртвыйАдрес — адрес, по которому ГАРАНТИРОВАННО никого нет: слушатель поднят
// и тут же закрыт, поэтому порт свободен, а не занят кем-то посторонним.
func мёртвыйАдрес(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("слушатель не поднялся: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func клиент(doorAddr string) *Client { return New(doorAddr, nil, молча, fixedNow) }

// отказ достаёт названный отказ из ошибки. Ошибка без кода — провал сама по
// себе: «что-то пошло не так» не называет ни причины, ни выхода.
func отказ(t *testing.T, err error) *Refusal {
	t.Helper()
	if err == nil {
		t.Fatal("отказа не было — а его вызывали нарочно")
	}
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("ошибка не названа отказом: %v", err)
	}
	return r
}

func должноБыть(t *testing.T, r *Refusal, код string, куски ...string) {
	t.Helper()
	if r.Code != код {
		t.Errorf("код отказа: получено %q, ожидалось %q (%s)", r.Code, код, r.Detail)
	}
	for _, кусок := range куски {
		if !strings.Contains(r.Detail, кусок) {
			t.Errorf("в отказе %q нет выхода %q: %s", r.Code, кусок, r.Detail)
		}
	}
}

// Флоу целиком (`kb:WORLD-53`): поднял сторожа → вошёл → локация в поле и
// маршрут выдан → снялся → её нет.
func TestВходВПолеВыходИСписок(t *testing.T) {
	c := клиент(дверь(t))
	свой := локация(t, "probe-loc")

	вошли, err := c.Join(Announce{Name: "probe-loc", Addr: свой, Gives: "проба входа в поле"})
	if err != nil {
		t.Fatalf("вход в поле не прошёл: %v", err)
	}
	if !вошли.Created {
		t.Errorf("первый вход обязан быть регистрацией, а не подтверждением")
	}
	if вошли.Route != "/probe-loc/" {
		t.Errorf("маршрут: получено %q, ожидалось %q", вошли.Route, "/probe-loc/")
	}
	if вошли.Location.Addr != свой {
		t.Errorf("адрес в реестре: получено %q, ожидалось %q", вошли.Location.Addr, свой)
	}

	вПоле, err := c.Who()
	if err != nil {
		t.Fatalf("список поля не прочитан: %v", err)
	}
	if len(вПоле) != 1 || вПоле[0].Name != "probe-loc" {
		t.Fatalf("в поле: получено %+v, ожидалась одна локация probe-loc", вПоле)
	}

	if err := c.Leave("probe-loc"); err != nil {
		t.Fatalf("снятие с поля не прошло: %v", err)
	}
	после, err := c.Who()
	if err != nil {
		t.Fatalf("список поля не прочитан: %v", err)
	}
	if len(после) != 0 {
		t.Errorf("после снятия в поле осталось %+v — маршрут обязан уходить вместе с локацией", после)
	}
}

// Повторный вход тем же адресом — подтверждение присутствия, а не отказ:
// локация переобъявляется на каждом подъёме (`tasker:WORLD-84`).
func TestПовторныйВходТемЖеАдресомПодтверждает(t *testing.T) {
	c := клиент(дверь(t))
	свой := локация(t, "probe-loc")
	объявление := Announce{Name: "probe-loc", Addr: свой, Gives: "проба входа в поле"}

	if _, err := c.Join(объявление); err != nil {
		t.Fatalf("первый вход: %v", err)
	}
	второй, err := c.Join(объявление)
	if err != nil {
		t.Fatalf("повторный вход отвергнут, а он обязан подтверждать: %v", err)
	}
	if второй.Created {
		t.Errorf("повторный вход выдан за регистрацию — по этому полю печатается «вошла», а она уже была в поле")
	}
	if второй.Outcome != door.OutcomeConfirmed {
		t.Errorf("исход: получено %q, ожидалось %q", второй.Outcome, door.OutcomeConfirmed)
	}
}

// Пересоздали контейнер — адрес другой, место ТО ЖЕ (`tasker:WORLD-81`). Клиент
// обязан донести исход до печати: «вошла» вместо «вернулась» это ровно та ложь,
// ради которой задача и делалась.
func TestВозвращениеПоНовомуАдресуДоезжаетДоКлиента(t *testing.T) {
	идут := идущиеЧасы()
	c := клиент(дверьСЧасами(t, идут))
	прежний, снести := локацияСоСносом(t, "probe-loc")

	первый, err := c.Join(Announce{Name: "probe-loc", Addr: прежний, Gives: "проба входа в поле"})
	if err != nil {
		t.Fatalf("первый вход: %v", err)
	}

	// Пересоздание: прежний контейнер снесён, новый встал по другому адресу.
	снести()
	новый := локация(t, "probe-loc")
	вернулись, err := c.Join(Announce{Name: "probe-loc", Addr: новый, Gives: "проба входа в поле"})
	if err != nil {
		t.Fatalf("возвращение отвергнуто, а это то же место: %v", err)
	}

	if вернулись.Outcome != door.OutcomeReturned {
		t.Fatalf("исход: получено %q, ожидалось %q", вернулись.Outcome, door.OutcomeReturned)
	}
	if вернулись.Location.Since != первый.Location.Since {
		t.Errorf("время первого входа: получено %q, ожидалось %q", вернулись.Location.Since, первый.Location.Since)
	}
	if вернулись.Location.Returned == "" {
		t.Error("метка возвращения пуста — по ней печатается «вернулась», и без неё это снова просто вход")
	}
	if вернулись.Location.Addr != новый {
		t.Errorf("адрес: получено %q, ожидалось %q", вернулись.Location.Addr, новый)
	}

	// В поле по-прежнему ОДНА локация: вторая записью рядом — это ровно то, чего
	// не должно случиться.
	поле, err := c.Who()
	if err != nil {
		t.Fatalf("список поля не прочитан: %v", err)
	}
	if len(поле) != 1 || поле[0].Addr != новый {
		t.Errorf("в поле: получено %+v, ожидалась одна probe-loc по новому адресу", поле)
	}
}

// Дверь старше этой редакции исхода не называет: локация и дверь — разные
// образы с разной судьбой, и новый образ законно приезжает к старой двери.
// Молчание тогда печаталось бы как «вошла».
func TestОтветБезИсходаВыводитсяИзПрежнегоПоля(t *testing.T) {
	tests := []struct {
		name    string
		тело    string
		ожидали door.Outcome
	}{
		{"создание", `{"created":true,"route":"/probe-loc/","location":{"name":"probe-loc"}}`, door.OutcomeCreated},
		{"подтверждение", `{"created":false,"route":"/probe-loc/","location":{"name":"probe-loc"}}`, door.OutcomeConfirmed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			старая := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tt.тело)
			}))
			t.Cleanup(старая.Close)

			c := клиент(strings.TrimPrefix(старая.URL, "http://"))
			вошли, err := c.Join(Announce{Name: "probe-loc", Addr: локация(t, "probe-loc"), Gives: "проба"})
			if err != nil {
				t.Fatalf("вход: %v", err)
			}
			if вошли.Outcome != tt.ожидали {
				t.Errorf("исход: получено %q, ожидалось %q", вошли.Outcome, tt.ожидали)
			}
		})
	}
}

// ── отказы: каждый вызывается нарочно (`kb:WORLD-32`) ────────────────────────

// Первый из трёх обязательных (`tasker:WORLD-84`): по моему адресу никто не
// отвечает. Отказ обязан говорить «подними сторожа РАНЬШЕ, чем входишь».
func TestОтказЯСамНедостижим(t *testing.T) {
	c := клиент(дверь(t))

	_, err := c.Join(Announce{Name: "probe-loc", Addr: мёртвыйАдрес(t), Gives: "проба"})

	r := отказ(t, err)
	должноБыть(t, r, "self-unreachable", "world guard", "WORLD_SELF_ADDR")
	if r.Status != 0 {
		t.Errorf("отказ приписан двери (%d), а он наш: до двери дело не дошло", r.Status)
	}
}

// Второй обязательный: имя занято ДРУГОЙ локацией. Причина и выход берутся
// словами двери — она про своё правило знает больше клиента.
func TestОтказИмяЗанятоДругойЛокацией(t *testing.T) {
	c := клиент(дверь(t))
	перваяЛокация := локация(t, "probe-loc")
	другая := локация(t, "другая")

	if _, err := c.Join(Announce{Name: "probe-loc", Addr: перваяЛокация, Gives: "первая"}); err != nil {
		t.Fatalf("первый вход: %v", err)
	}
	_, err := c.Join(Announce{Name: "probe-loc", Addr: другая, Gives: "вторая"})

	r := отказ(t, err)
	должноБыть(t, r, "name-taken", "DELETE")
	if r.Status != http.StatusConflict {
		t.Errorf("код двери: получено %d, ожидалось %d", r.Status, http.StatusConflict)
	}
}

// Третий обязательный: двери не видно. Выход — проверить, что мир поднят и что
// ты в общей сети: снаружи это самая частая причина, и гадать по «connection
// refused» человек не должен.
func TestОтказДвериНеВидно(t *testing.T) {
	c := клиент(мёртвыйАдрес(t))
	свой := локация(t, "probe-loc")

	_, err := c.Join(Announce{Name: "probe-loc", Addr: свой, Gives: "проба"})

	должноБыть(t, отказ(t, err), "door-unreachable", "мир поднят", "общей сети", DefaultDoor)
}

// Тот же отказ обязан звучать и у остальных команд: «двери нет» — состояние
// мира, а не особенность входа.
func TestДвериНеВидноНаЛюбойКоманде(t *testing.T) {
	c := клиент(мёртвыйАдрес(t))

	if _, err := c.Who(); отказ(t, err).Code != "door-unreachable" {
		t.Errorf("who: %v", err)
	}
	if err := c.Leave("probe-loc"); отказ(t, err).Code != "door-unreachable" {
		t.Errorf("leave: %v", err)
	}
}

// Дверь не достучалась до меня, хотя я сам до себя дошёл. Живой случай — локация
// и дверь в разных сетях: с точки зрения локации всё в порядке. Проба нарочно
// согласна, чтобы вопрос дошёл до двери.
func TestОтказДверьМеняНеВидит(t *testing.T) {
	согласнаВсегда := func(string) error { return nil }
	c := New(дверь(t), согласнаВсегда, молча, fixedNow)

	_, err := c.Join(Announce{Name: "probe-loc", Addr: мёртвыйАдрес(t), Gives: "проба"})

	r := отказ(t, err)
	должноБыть(t, r, "addr-unreachable", "подними локацию")
	if r.Status != http.StatusBadGateway {
		t.Errorf("код двери: получено %d, ожидалось %d", r.Status, http.StatusBadGateway)
	}
}

// Правило имени живёт в двери и здесь не дублируется — значит отказ обязан
// доехать целиком, вместе с алфавитом имени.
func TestОтказНеверноеИмяЕдетСловамиДвери(t *testing.T) {
	c := клиент(дверь(t))
	свой := локация(t, "probe-loc")

	_, err := c.Join(Announce{Name: "ПробаЛокация", Addr: свой, Gives: "проба"})

	должноБыть(t, отказ(t, err), "name-invalid", "путь за дверью")
}

func TestОтказСнимаемТого_КогоВПолеНет(t *testing.T) {
	c := клиент(дверь(t))

	err := c.Leave("no-such-loc")

	r := отказ(t, err)
	должноБыть(t, r, "location-unknown", door.Prefix)
	if r.Status != http.StatusNotFound {
		t.Errorf("код двери: получено %d, ожидалось %d", r.Status, http.StatusNotFound)
	}
}

func TestОтказСнимаемБезИмени(t *testing.T) {
	должноБыть(t, отказ(t, клиент(дверь(t)).Leave("   ")), "name-missing", "WORLD_NAME", "world who")
}

// По адресу двери стоит кто-то другой — самая частая порча настройки: в
// WORLD_DOOR уехал адрес локации либо хост-порт. Назвать это «неверным JSON»
// значит отправить человека искать ошибку не там.
func TestОтказПоАдресуДвериОтвечаетНеДверь(t *testing.T) {
	чужой := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>это не дверь</html>"))
	}))
	defer чужой.Close()
	c := клиент(strings.TrimPrefix(чужой.URL, "http://"))
	свой := локация(t, "probe-loc")

	t.Run("на входе", func(t *testing.T) {
		_, err := c.Join(Announce{Name: "probe-loc", Addr: свой, Gives: "проба"})
		должноБыть(t, отказ(t, err), "not-a-door", "WORLD_DOOR", DefaultDoor)
	})
	t.Run("на списке поля", func(t *testing.T) {
		_, err := c.Who()
		должноБыть(t, отказ(t, err), "not-a-door", "WORLD_DOOR")
	})
	t.Run("на отказе не в форме двери", func(t *testing.T) {
		отказНеДвери := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "нет такой страницы", http.StatusNotFound)
		}))
		defer отказНеДвери.Close()
		err := клиент(strings.TrimPrefix(отказНеДвери.URL, "http://")).Leave("probe-loc")
		должноБыть(t, отказ(t, err), "not-a-door", "WORLD_DOOR")
	})
}

// Адрес двери со схемой либо с мусором внутри — запрос из него не собирается.
// Отказ называет форму адреса: «имя:порт» без схемы, как везде у двери.
func TestОтказАдресДвериНеСкладываетсяВЗапрос(t *testing.T) {
	_, err := клиент("дверь\nс переводом строки:8080").Who()

	должноБыть(t, отказ(t, err), "door-addr-invalid", "имя:порт", "WORLD_DOOR")
}

// Дверь оборвала ответ на середине. Отказ обязан сказать, что поле не
// изменилось: иначе человек не знает, повторять ему или чинить.
func TestОтказОтветДвериОборван(t *testing.T) {
	обрыв := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("перехват соединения не удался: %v", err)
			return
		}
		// Обещаем 100 байт и обрываем на десятом: ровно то, как выглядит
		// умерший на полуслове собеседник.
		fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"count\":0")
		conn.Close()
	}))
	defer обрыв.Close()

	_, err := клиент(strings.TrimPrefix(обрыв.URL, "http://")).Who()

	должноБыть(t, отказ(t, err), "door-answer-unreadable", "поле от неудавшегося чтения не изменилось")
}

// Трассировка на каждый запрос — по той же причине, что и у двери: собеседник
// на другой машине, и без строки непонятно, дошли ли мы до него вообще.
func TestКаждыйРазговорСДверьюОставляетСтроку(t *testing.T) {
	var строки []string
	c := New(дверь(t), nil, func(f string, a ...any) { строки = append(строки, fmt.Sprintf(f, a...)) }, fixedNow)

	if _, err := c.Who(); err != nil {
		t.Fatalf("список поля: %v", err)
	}
	if len(строки) != 1 {
		t.Fatalf("строк трассировки: получено %d, ожидалась 1", len(строки))
	}
	for _, кусок := range []string{"field:", "GET", door.Prefix, "code=200", "dur="} {
		if !strings.Contains(строки[0], кусок) {
			t.Errorf("в строке трассировки нет %q: %q", кусок, строки[0])
		}
	}
}

// И то же самое, когда дверь не ответила: строка обязана быть — по ней видно,
// что мы вообще пытались и сколько ждали.
func TestНеудавшийсяРазговорТожеОставляетСтроку(t *testing.T) {
	var строки []string
	c := New(мёртвыйАдрес(t), nil, func(f string, a ...any) { строки = append(строки, fmt.Sprintf(f, a...)) }, fixedNow)

	if _, err := c.Who(); err == nil {
		t.Fatal("мёртвая дверь ответила — этого не может быть")
	}
	if len(строки) != 1 || !strings.Contains(строки[0], "дверь не ответила") {
		t.Errorf("строки трассировки: %+v", строки)
	}
}

// ── доступ внутрь места: стройка через дверь ─────────────────────────────────

// дверьСМаршрутами — дверь, которая не только ведёт реестр, но и ВОДИТ к
// местам. Доступ внутрь места идёт по маршруту (`kb:WORLD-56`), и проверять его
// на двери без маршрутизации значит проверять половину дороги.
func дверьСМаршрутами(t *testing.T) string {
	t.Helper()
	reg, err := door.Open(filepath.Join(t.TempDir(), "field", "locations.json"))
	if err != nil {
		t.Fatalf("реестр локаций не поднялся: %v", err)
	}
	h := door.NewHandler(reg, молча, fixedNow, door.DialProbe(2*time.Second))

	mux := http.NewServeMux()
	mux.Handle(door.Prefix, h)
	mux.Handle(door.Prefix+"/", h)
	// Имени нет в поле — запрос уходит в раздачу, ровно как у живой двери.
	mux.Handle("/", h.Route(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "витрина зоны web", http.StatusNotFound)
	})))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// местоСоСтройкой — живая локация, в которую МОЖНО строить: тот же сторож, но с
// каталогом под постройку.
func местоСоСтройкой(t *testing.T, имя string) string {
	t.Helper()
	site := build.Open(filepath.Join(t.TempDir(), "стройка"), молча, fixedNow)
	srv := httptest.NewServer(guard.New(имя, "проба присутствия", site, молча, fixedNow))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// вПоле вводит место в поле и отдаёт клиента двери.
func вПоле(t *testing.T, doorAddr, имя, адрес string) *Client {
	t.Helper()
	c := клиент(doorAddr)
	if _, err := c.Join(Announce{Name: имя, Addr: адрес, Gives: "проба доступа внутрь места"}); err != nil {
		t.Fatalf("место не вошло в поле: %v", err)
	}
	return c
}

// ПРИЁМКА со стороны клиента: доступ внутрь места идёт ЧЕРЕЗ МИР — дверь
// доводит до места по маршруту из реестра, место исполняет. Докера здесь нет
// вовсе, и в этом вся задача (`kb:WORLD-56`).
func TestПостройкаВстаётЧерезДверьАНеМимоНеё(t *testing.T) {
	схема := schematest.Схема(t, map[string]string{"README.md": "постройка пробы"})
	doorAddr := дверьСМаршрутами(t)
	c := вПоле(t, doorAddr, "probe-loc", местоСоСтройкой(t, "probe-loc"))

	// До стройки место пустое, и это законный ответ.
	пусто, err := c.Standing("probe-loc")
	if err != nil {
		t.Fatalf("что стоит на месте: %v", err)
	}
	if пусто.Built {
		t.Fatalf("на свежем месте что-то стоит: %+v", пусто)
	}

	готово, err := c.Raise("probe-loc", схема, false)
	if err != nil {
		t.Fatalf("стройка через дверь: %v", err)
	}
	if готово.Outcome != build.OutcomeBuilt {
		t.Errorf("исход: получено %q, ожидалось %q", готово.Outcome, build.OutcomeBuilt)
	}
	if готово.Build.Commit != schematest.Коммит(t, схема) {
		t.Errorf("коммит: получено %q, ожидалось %q", готово.Build.Commit, schematest.Коммит(t, схема))
	}

	стоит, err := c.Standing("probe-loc")
	if err != nil {
		t.Fatalf("что стоит на месте: %v", err)
	}
	if !стоит.Built || стоит.Build.Schema != схема {
		t.Errorf("место говорит не то, что на нём стоит: %+v", стоит)
	}
}

// МЕСТА НЕТ В ПОЛЕ — отказ до всякой стройки, с причиной и выходом. Маршрут
// берётся из регистрации, а не собирается из имени: иначе стройка ушла бы в
// витрину и вернулась непонятным ответом.
func TestДоМестаКоторогоНетВПолеДотянутьсяНельзя(t *testing.T) {
	c := клиент(дверьСМаршрутами(t))

	_, err := c.Raise("такого-места-нет", "/схема", false)

	должноБыть(t, отказ(t, err), "location-unknown", "world who", "world join")

	_, err = c.Standing("такого-места-нет")
	должноБыть(t, отказ(t, err), "location-unknown", "world who")
}

// Имени не сказали вовсе — тянуться некуда, и это отдельный отказ: без него
// пустое имя ушло бы в дверь и вернулось «места нет».
func TestБезИмениМестаТянутьсяНекуда(t *testing.T) {
	c := клиент(дверьСМаршрутами(t))

	_, err := c.Raise("  ", "/схема", false)

	должноБыть(t, отказ(t, err), "name-missing", "WORLD_NAME")
}

// Отказ МЕСТА доезжает до человека целиком — его словами, вместе с выходом.
// Своя формулировка здесь потеряла бы выход, который место уже назвало.
func TestОтказМестаДоезжаетЧерезДверьЦеликом(t *testing.T) {
	schematest.Требуется(t)
	doorAddr := дверьСМаршрутами(t)
	c := вПоле(t, doorAddr, "probe-loc", местоСоСтройкой(t, "probe-loc"))

	_, err := c.Raise("probe-loc", filepath.Join(t.TempDir(), "такой-схемы-нет"), false)

	должноБыть(t, отказ(t, err), "schema-unreachable", "git ls-remote")
}

// За именем в поле стоит НЕ сторож мира — отказ называет это причиной, а не
// «неверным JSON»: чинится оно образом локации, а не разбором ответа.
func TestЗаМестомОтвечаетНеСторожМира(t *testing.T) {
	чужой := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("я не сторож мира, я чужой контейнер"))
	}))
	t.Cleanup(чужой.Close)

	doorAddr := дверьСМаршрутами(t)
	c := вПоле(t, doorAddr, "probe-loc", strings.TrimPrefix(чужой.URL, "http://"))

	_, err := c.Standing("probe-loc")

	должноБыть(t, отказ(t, err), "not-a-place", "kb:WORLD-53")
}
