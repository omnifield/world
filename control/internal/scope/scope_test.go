package scope

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/omnifield/world/control/internal/refusal"
	"github.com/omnifield/world/control/internal/state"
)

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ЧТО ЗДЕСЬ СТЕРЕЖЁТСЯ: контроллер разговаривает с РАЗДАЧЕЙ по форме мира (`WORLD2`    │
// │ 3.4, `0.3`) и не узнаёт «свою» ни по чему. Поэтому подставная раздача ниже — голая:  │
// │ она отвечает статусом и телом, каких вправе ответить чужая вилка «хоть на ардуино».  │
// └─────────────────────────────────────────────────────────────────────────────────────┘

// раздача — подставная вилка. Она НЕ притворяется нашей: две ручки, пароль, и больше
// ничего. Ни одного своего заголовка она не ставит — и контроллер обязан работать так же.
type раздача struct {
	*httptest.Server
	пароль    string
	состояние []byte
	принято   []byte
	метод     []string
	// голыйОтказ — отвечать `401` БЕЗ тела, как вправе отвечать чужая раздача.
	голыйОтказ bool
}

func поднятьРаздачу(t *testing.T, пароль string, состояние []byte) *раздача {
	t.Helper()
	ш := &раздача{пароль: пароль, состояние: состояние}
	ш.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ш.метод = append(ш.метод, r.Method+" "+r.URL.Path)
		_, пароль, есть := r.BasicAuth()
		if !есть || пароль != ш.пароль {
			w.WriteHeader(http.StatusUnauthorized)
			if !ш.голыйОтказ {
				_, _ = w.Write([]byte(`{"code":"bad-creds","why":"пароль не подошёл","ways":["проверь пароль"]}`))
			}
			return
		}
		switch r.Method {
		case http.MethodGet:
			if ш.состояние == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(ш.состояние)
		case http.MethodPut:
			buf := make([]byte, 1<<20)
			n, _ := r.Body.Read(buf)
			ш.принято = buf[:n]
			ш.состояние = ш.принято
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Allow", "GET, PUT")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(ш.Close)
	return ш
}

func личность(t *testing.T) []byte {
	t.Helper()
	data, err := state.New("егор", "омнифилд").Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func открыть(t *testing.T, ш *раздача, пароль string) *Scope {
	t.Helper()
	addr, ref := Parse(ш.URL, AskingSignIn)
	if ref != nil {
		t.Fatalf("свой же адрес не разобрался: %s", ref.Why)
	}
	return Open(addr, пароль, 5, AskingSignIn)
}

// ── адрес ────────────────────────────────────────────────────────────────────

func TestАдресСкоупаЭтоКореньРаздачи(t *testing.T) {
	addr, ref := Parse("http://10.8.0.5:8070", AskingSignIn)
	if ref != nil {
		t.Fatalf("законный адрес не принят: %s", ref.Why)
	}
	if addr.URL.Path != "/" || addr.Host != "10.8.0.5" {
		t.Fatalf("адрес разобран не так: %+v", addr)
	}
}

func TestАдресОтказываетТамГдеУгадыватьНельзя(t *testing.T) {
	tests := []struct {
		имя   string
		адрес string
		код   string
	}{
		{"пусто", "", "no-address"},
		{"без протокола", "10.8.0.5:8070", "bad-address"},
		{"чужой протокол", "ssh://world@10.8.0.5/scope", "bad-address"},
		{"путь внутри раздачи", "http://10.8.0.5:8070/егор", "bad-address"},
		{"пароль в адресе", "http://:пароль@10.8.0.5:8070/", "bad-address"},
		{"прежняя форма — путь на машине", "/scope/егор", "bad-address"},
	}
	for _, tt := range tests {
		t.Run(tt.имя, func(t *testing.T) {
			_, ref := Parse(tt.адрес, AskingSignIn)
			if ref == nil {
				t.Fatalf("адрес %q принят молча", tt.адрес)
			}
			if ref.Code != tt.код || len(ref.Ways) == 0 {
				t.Fatalf("отказ не тот: %+v", ref)
			}
		})
	}
}

// ── чтение и запись ──────────────────────────────────────────────────────────

func TestСостояниеБерётсяПоАдресуЦеликом(t *testing.T) {
	ш := поднятьРаздачу(t, "тайна", личность(t))
	sc := открыть(t, ш, "тайна")

	st, ref := sc.Read(context.Background())
	if ref != nil {
		t.Fatalf("состояние не прочиталось: %s — %s", ref.Code, ref.Why)
	}
	if st.Identity.Name != "егор" {
		t.Fatalf("прочиталась не та личность: %+v", st.Identity)
	}
	if len(ш.метод) != 1 || ш.метод[0] != "GET /" {
		t.Fatalf("контроллер ходил в раздачу не одной ручкой в корне: %v", ш.метод)
	}
}

func TestЗаписьИдётЦеликомВКорень(t *testing.T) {
	ш := поднятьРаздачу(t, "тайна", личность(t))
	sc := открыть(t, ш, "тайна")

	st, _ := sc.Read(context.Background())
	_ = st.AddField("дом")
	if ref := sc.Write(context.Background(), st); ref != nil {
		t.Fatalf("состояние не записалось: %s — %s", ref.Code, ref.Why)
	}
	if ш.метод[len(ш.метод)-1] != "PUT /" {
		t.Fatalf("запись пошла не PUT в корень: %v", ш.метод)
	}
	if !strings.Contains(string(ш.принято), `"дом"`) {
		t.Fatalf("в раздачу уехало не состояние целиком:\n%s", ш.принято)
	}
	// Ручек по разделам нет и не заводится (`WORLD2` 3.4): приезжает ФАЙЛ, а в нём все
	// разделы, включая нетронутые.
	for _, раздел := range []string{"личность", "ключи", "территории", "поля"} {
		if !strings.Contains(string(ш.принято), раздел) {
			t.Fatalf("в записанном файле нет раздела %q — записан не файл целиком", раздел)
		}
	}
}

// ── ступени отказа: дорога · ответ · пароль · формат ──────────────────────────

func TestНеверныйПарольЭтоОтказСПричинойИВыходом(t *testing.T) {
	ш := поднятьРаздачу(t, "тайна", личность(t))
	sc := открыть(t, ш, "не-та")

	_, ref := sc.Read(context.Background())
	if ref == nil || ref.Code != "bad-password" || len(ref.Ways) == 0 {
		t.Fatalf("отказ не тот: %+v", ref)
	}
}

// Чужая раздача вправе ответить ГОЛЫМ `401` без тела — и это законная раздача (`0.3`).
// Клиент читает СТАТУС, а тело берёт подробностью: разъедься это правило, мир перестал бы
// принимать чужие вилки.
func TestГолыйОтказЧужойРаздачиЧитаетсяТакЖе(t *testing.T) {
	ш := поднятьРаздачу(t, "тайна", личность(t))
	ш.голыйОтказ = true
	sc := открыть(t, ш, "не-та")

	_, ref := sc.Read(context.Background())
	if ref == nil || ref.Code != "bad-password" {
		t.Fatalf("голый 401 прочитан иначе, чем наш: %+v", ref)
	}
}

func TestРаздачаЕстьАСостоянияНетЭтоРазвилка(t *testing.T) {
	ш := поднятьРаздачу(t, "тайна", nil)
	sc := открыть(t, ш, "тайна")

	_, ref := sc.Read(context.Background())
	if ref == nil || ref.Code != "no-scope" {
		t.Fatalf("свежая раздача прочиталась не как развилка: %+v", ref)
	}
	if !strings.Contains(strings.Join(ref.Ways, " "), "/api/scope") {
		t.Fatalf("отказ не позвал завести скоуп по адресу: %v", ref.Ways)
	}
}

// ФОРМАТ СТАРШЕ СВОЕГО — ОТКАЗ, и это единственная сторона, где отказ уместен: младший
// формат мы читаем как свой (`WORLD2` 3.4). Прочитав старший молча, контроллер стёр бы при
// записи то, о чём не знает: файл принимается ЦЕЛИКОМ.
func TestПоАдресуЛежитФорматСтаршеНашего(t *testing.T) {
	ш := поднятьРаздачу(t, "тайна", []byte(`{"формат":3,"личность":{"имя":"егор"}}`))
	sc := открыть(t, ш, "тайна")

	_, ref := sc.Read(context.Background())
	if ref == nil || ref.Code != "bad-format" {
		t.Fatalf("чужая версия формата принята: %+v", ref)
	}
	if !strings.Contains(ref.Why, "3") || !strings.Contains(ref.Why, "2") {
		t.Fatalf("отказ не назвал, что приехало и что мы умеем: %s", ref.Why)
	}
}

func TestРаздачаНедоступнаНазываетАдресИВыход(t *testing.T) {
	// Порт, на котором заведомо никто не слушает: занимаем и сразу отпускаем.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	мёртвый := "http://" + l.Addr().String() + "/"
	_ = l.Close()

	addr, ref := Parse(мёртвый, AskingSignIn)
	if ref != nil {
		t.Fatal(ref.Why)
	}
	_, ref = Open(addr, "тайна", 2, AskingSignIn).Read(context.Background())
	if ref == nil || ref.Code != "no-answer" {
		t.Fatalf("отказ не назвал ступень связи: %+v", ref)
	}
	if !strings.Contains(ref.Why, мёртвый) || len(ref.Ways) == 0 {
		t.Fatalf("отказ не назвал адрес или выход: %+v", ref)
	}
}

// Ступени связи разные, и чинят их разные люди: имя не разрешается — чинит адрес; порт
// закрыт — поднимает раздачу; тишина — смотрит на машину. Схлопнуть их в одно «не
// дотянулись» значит отправить человека чинить наугад (`WORLD2` 2.3).
//
// Проверяется РАЗБОР ошибки, а не настоящий резолвер: имя, которого нет, в разных сетях
// отвечает по-разному (где отказом, где тишиной), и проба, зависящая от сети, стережёт
// сеть, а не контроллер.
func TestСтупениСвязиНеСхлопываются(t *testing.T) {
	addr, _ := Parse("http://10.8.0.5:8070/", AskingSignIn)
	sc := Open(addr, "тайна", 5, AskingSignIn)

	tests := []struct {
		имя    string
		ошибка error
		код    string
	}{
		{"имя не разрешается", &net.DNSError{Err: "no such host", Name: "10.8.0.5", IsNotFound: true}, "no-route"},
		{"порт закрыт", syscall.ECONNREFUSED, "no-answer"},
		{"машины не видно", syscall.EHOSTUNREACH, "no-route"},
		{"тишина", &таймаут{}, "scope-silent"},
	}
	for _, tt := range tests {
		t.Run(tt.имя, func(t *testing.T) {
			ref := sc.reachFailure(tt.ошибка)
			if ref.Code != tt.код {
				t.Fatalf("ступень названа не та: %q вместо %q (%s)", ref.Code, tt.код, ref.Why)
			}
			if len(ref.Ways) == 0 {
				t.Fatalf("отказ без выхода — тупик: %+v", ref)
			}
		})
	}
}

// таймаут — ошибка сети, которая говорит о себе «я тишина». Настоящую тишину пришлось бы
// ждать секундами, а проверяем мы разбор, а не терпение.
type таймаут struct{}

func (*таймаут) Error() string   { return "i/o timeout" }
func (*таймаут) Timeout() bool   { return true }
func (*таймаут) Temporary() bool { return true }

// ── три состояния адреса ─────────────────────────────────────────────────────

func TestLookРазличаетТриСостояния(t *testing.T) {
	ш := поднятьРаздачу(t, "тайна", личность(t))
	if есть, ref := открыть(t, ш, "тайна").Look(context.Background()); ref != nil || есть != PresenceState {
		t.Fatalf("состояние по адресу не увидено: %v %+v", есть, ref)
	}

	пустая := поднятьРаздачу(t, "тайна", nil)
	if есть, ref := открыть(t, пустая, "тайна").Look(context.Background()); ref != nil || есть != PresenceEmpty {
		t.Fatalf("свежая раздача прочиталась не как пустая: %v %+v", есть, ref)
	}

	l, _ := net.Listen("tcp", "127.0.0.1:0")
	мёртвый := "http://" + l.Addr().String() + "/"
	_ = l.Close()
	addr, _ := Parse(мёртвый, AskingSignIn)
	// Раздачи нет вовсе — это НЕ отказ: заведение скоупа опирается ровно на это состояние.
	if есть, ref := Open(addr, "тайна", 2, AskingSignIn).Look(context.Background()); ref != nil || есть != PresenceNone {
		t.Fatalf("отсутствие раздачи прочиталось как поломка: %v %+v", есть, ref)
	}
}

// Пароль проверяет САМА РАЗДАЧА (`WORLD2` 3.4, «базово: везде пароль»), и мы обязаны его
// приносить. Ходить в чужую личность без предъявленного пароля контроллер не должен уметь.
func TestПарольЕдетКаждымЗапросом(t *testing.T) {
	var виделиПароль []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, пароль, есть := r.BasicAuth()
		виделиПароль = append(виделиПароль, есть && пароль == "тайна")
		if r.Method == http.MethodGet {
			_, _ = w.Write(личность(t))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	addr, _ := Parse(srv.URL, AskingSignIn)
	sc := Open(addr, "тайна", 5, AskingSignIn)
	st, ref := sc.Read(context.Background())
	if ref != nil {
		t.Fatal(ref.Why)
	}
	if ref := sc.Write(context.Background(), st); ref != nil {
		t.Fatal(ref.Why)
	}
	for i, было := range виделиПароль {
		if !было {
			t.Fatalf("запрос %d ушёл без пароля", i)
		}
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ПОРТ СКОУПА — ЭТО ТО, ЧТО ЮЗЕР УЖЕ СКАЗАЛ (`WORLD2-150` B3).                         │
// │                                                                                      │
// │ Раздача обязана слушать там, где он назвал. Порт, выдуманный миром, означал бы адрес, │
// │ которого юзер не называл, — а личность лежит именно по адресу (`WORLD2` 3.4).         │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestПортБерётсяИзАдресаСкоупа(t *testing.T) {
	для := []struct {
		адрес string
		порт  int
	}{
		{"http://10.8.0.5:8071/", 8071},
		{"http://10.8.0.5:8072/", 8072},
		// Порт не назван — его называет схема, и это тоже сказанное юзером: он написал
		// адрес, в котором стоит 80, и раздача встанет там.
		{"http://10.8.0.5/", 80},
		{"https://мой-скоуп.example/", 443},
		{"https://мой-скоуп.example:8443/", 8443},
	}
	for _, п := range для {
		addr, ref := Parse(п.адрес, AskingSignIn)
		if ref != nil {
			t.Fatalf("%s не разобрался: %s", п.адрес, ref.Why)
		}
		if got := addr.Port(); got != п.порт {
			t.Fatalf("у адреса %s порт %d вместо %d", п.адрес, got, п.порт)
		}
	}
}

// ── выход зависит от того, кто спрашивает (`WORLD2-144`) ─────────────────────
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ СТОРОЖ СМОТРИТ НА КЛАСС, А НЕ НА ТРИ ЗАУЧЕННЫХ СЛУЧАЯ.                               │
// │                                                                                      │
// │ Проверяются две разные вещи, и ни одна не заменяет другую:                            │
// │                                                                                      │
// │   таблица  обходится ЦЕЛИКОМ, и у каждого спрашивающего берётся ЕГО ручка. Добавили   │
// │            спрашивающего — он проверился сам, дописывать сюда ничего не надо;         │
// │   отказы   те же ручки прогоняются на ЖИВЫХ отказах: выход, вписанный литералом мимо  │
// │            таблицы, — ровно та порча, ради которой узел и заведён, и таблица её не    │
// │            увидела бы.                                                                │
// └─────────────────────────────────────────────────────────────────────────────────────┘

func TestВыходНеВедётТудаГдеЧеловекУжеСтоит(t *testing.T) {
	// 1. ТАБЛИЦА ЦЕЛИКОМ.
	for кто, а := range askers {
		if а.handle == "" {
			// Не назвавшийся не вправе советовать ручки вовсе: его выходы никем не
			// проверяются, потому что проверять их не с чем.
			if len(а.ways) > 0 {
				t.Fatalf("спрашивающий %d ручки не назвал, а выходы с ручками у него есть: %v", кто, а.ways)
			}
			continue
		}
		for беда, выходы := range а.ways {
			for _, выход := range выходы {
				if strings.Contains(выход, а.handle) {
					t.Fatalf("%s (беда %d): выход ведёт туда, где человек уже стоит — %q", а.handle, беда, выход)
				}
			}
		}
	}

	// 2. ЖИВЫЕ ОТКАЗЫ. Три беды у каждой ручки: адрес не назван · раздача молчит ·
	//    состояния в раздаче нет. Отказ берётся тот самый, что уедет человеку.
	пустая := поднятьРаздачу(t, "тайна", nil)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	мёртвый := "http://" + l.Addr().String() + "/"
	_ = l.Close()

	for кто, а := range askers {
		если := func(беда string, ref *refusal.Refusal) {
			t.Helper()
			if ref == nil {
				t.Fatalf("%s, %s: отказа нет вовсе", а.handle, беда)
			}
			if len(ref.Ways) == 0 {
				t.Fatalf("%s, %s: отказ без выхода — тупик (`WORLD2` 2.3)", а.handle, беда)
			}
			if а.handle == "" {
				return
			}
			for _, выход := range ref.Ways {
				if strings.Contains(выход, а.handle) {
					t.Fatalf("%s, %s (%s): выход советует ту же ручку — %q", а.handle, беда, ref.Code, выход)
				}
			}
		}

		_, ref := Parse("", кто)
		если("адрес не назван", ref)

		мёртвыйАдрес, ref := Parse(мёртвый, кто)
		if ref != nil {
			t.Fatal(ref.Why)
		}
		_, ref = Open(мёртвыйАдрес, "тайна", 2, кто).Read(context.Background())
		если("раздача не отвечает", ref)

		пустойАдрес, ref := Parse(пустая.URL, кто)
		if ref != nil {
			t.Fatal(ref.Why)
		}
		_, ref = Open(пустойАдрес, "тайна", 5, кто).Read(context.Background())
		если("состояния в раздаче нет", ref)
	}
}

// ВХОД СОВЕТУЕТ ЗАВЕСТИ СКОУП, А ЗАВЕДЕНИЕ — НЕТ. Обратная сторона того же свойства:
// проверка «ручку не советуют» зеленела бы и на выходах, вычищенных у всех подряд, а
// подсказка на входе верна и нужна (`WORLD2-144`: «первый выход при этом верный»).
func TestВходПодсказываетЗавестиСкоуп(t *testing.T) {
	_, ref := Parse("", AskingSignIn)
	if ref == nil {
		t.Fatal("пустой адрес принят молча")
	}
	if !strings.Contains(strings.Join(ref.Ways, " "), "POST /api/scope") {
		t.Fatalf("вход перестал подсказывать, где заводят скоуп: %v", ref.Ways)
	}
}

// НЕ НАЗВАВШИЙСЯ ПОЛУЧАЕТ ВЫХОДЫ БЕЗ РУЧЕК. Нулевое значение — безопасная сторона:
// следующая ручка, забывшая назваться, промолчит, а не соврёт.
func TestНеНазвавшийсяРучекНеСоветует(t *testing.T) {
	_, ref := Parse("", AskingUnnamed)
	if ref == nil || len(ref.Ways) == 0 {
		t.Fatalf("отказ без выхода — тупик: %+v", ref)
	}
	for _, выход := range ref.Ways {
		if strings.Contains(выход, "/api/") {
			t.Fatalf("не назвавшийся советует ручку: %q", выход)
		}
	}
}
