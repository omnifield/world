package guard

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omnifield/world/core/internal/build"
)

// свободныйАдрес — порт, на котором сейчас никто не слушает. Нужен, чтобы
// «застройка не отвечает» проверялось настоящим мёртвым адресом, а не выдумкой.
func свободныйАдрес(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("слушатель не поднялся: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// складПодСтройку — каталог стройки места: пустой, ничего ещё не приехало.
func складПодСтройку(t *testing.T) *build.Site {
	t.Helper()
	return build.Open(filepath.Join(t.TempDir(), "стройка"), func(string, ...any) {}, fixedNow)
}

// застройка — то, что на месте СТОИТ и уже поднято: живая служба на своём
// порту внутри места. Не заглушка ради удобства: маршрут места ведёт именно к
// поднятому, и проверять это на снимке файлов значит проверять другую вещь.
func застройка(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// сторожСЗастройкой — место, на котором что-то стоит и заявлено.
func сторожСЗастройкой(t *testing.T, adr string) *Guard {
	t.Helper()
	return New("probe-loc", "проба присутствия", nil,
		OpenStands(adr, "127.0.0.1:0", func(string, ...any) {}), func(string, ...any) {}, fixedNow)
}

// ГЛАВНОЕ ПО ЗАДАЧЕ: есть застройка — по маршруту места открывается ОНА, а не
// рассказ сторожа о себе (канон WORLD2, узлы 1.8 и 1.6).
func TestМаршрутМестаВедётВЗастройку(t *testing.T) {
	adr := застройка(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Who-Answered", "застройка")
		io.WriteString(w, "витрина застройки: "+r.Method+" "+r.URL.RequestURI())
	})
	g := сторожСЗастройкой(t, adr)

	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("код ответа: получено %d (%s), ожидалось %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "витрина застройки") {
		t.Fatalf("по маршруту места ответила не застройка: %s", rec.Body.String())
	}
	// Ответ доезжает целиком, а не пересказывается: связь — это пользование
	// чужой вещью ТАМ, ГДЕ ОНА СТОИТ (`1.6`), а не снимок с неё.
	if rec.Header().Get("X-Who-Answered") != "застройка" {
		t.Errorf("заголовки застройки не доехали: %v", rec.Header())
	}
}

// Внутрь застройки ходят целиком: путь, метод, query и тело — её дело, а не
// сторожа. Срезать здесь нечего: имя места уже срезала дверь.
func TestВЗастройкуУезжаетЗапросЦеликом(t *testing.T) {
	var видел struct{ метод, путь, тело, forwarded string }
	adr := застройка(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		видел.метод, видел.путь, видел.тело = r.Method, r.URL.Path+"?"+r.URL.RawQuery, string(body)
		видел.forwarded = r.Header.Get("X-Forwarded-For")
		w.WriteHeader(http.StatusAccepted)
	})
	g := сторожСЗастройкой(t, adr)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/store/item?x=1", strings.NewReader(`{"взять":2}`))
	g.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("код ответа: получено %d, ожидалось %d — не-GET обязан доезжать до застройки", rec.Code, http.StatusAccepted)
	}
	if видел.метод != http.MethodPost || видел.путь != "/store/item?x=1" || видел.тело != `{"взять":2}` {
		t.Errorf("до застройки доехало %+v — ожидался POST /store/item?x=1 с телом", видел)
	}
	// X-Forwarded-* — то, чем застройка узнаёт, через какую дверь к ней пришли.
	if видел.forwarded == "" {
		t.Error("застройка не получила X-Forwarded-For — ей нечем узнать, кто пришёл")
	}
}

// Пусто — прежний ответ сторожа, СЛОВО В СЛОВО: место существует, внутри
// ничего не построено. Пустая локация законна (`1.0`), и это не отказ.
func TestБезЗастройкиМаршрутОтвечаетКакРаньше(t *testing.T) {
	g, _ := сторожСМестом(t) // stands == nil

	t.Run("корень — присутствие", func(t *testing.T) {
		код, тело := ответ(t, g, http.MethodGet, "/")
		if код != http.StatusOK {
			t.Fatalf("код: получено %d, ожидалось %d", код, http.StatusOK)
		}
		msg, _ := тело["message"].(string)
		if !strings.Contains(msg, "я здесь") || !strings.Contains(msg, "ничего не построено") {
			t.Errorf("ответ пустого места изменился: %q", msg)
		}
		if strings.Contains(msg, "застройк") {
			t.Errorf("пустое место говорит о застройке, которой нет: %q", msg)
		}
	})

	t.Run("прочий путь — nothing-here", func(t *testing.T) {
		код, тело := ответ(t, g, http.MethodGet, "/витрина")
		if код != http.StatusNotFound || тело["error"] != "nothing-here" {
			t.Errorf("получено %d %v, ожидалось 404 nothing-here", код, тело)
		}
	})

	t.Run("не-чтение — guard-accepts-nothing", func(t *testing.T) {
		код, тело := послать(t, g, http.MethodPut, "/витрина", `{}`)
		if код != http.StatusMethodNotAllowed || тело["error"] != "guard-accepts-nothing" {
			t.Errorf("получено %d %v, ожидалось 405 guard-accepts-nothing", код, тело)
		}
	})
}

// Застройка есть, но не отвечает — отказ по форме: причина И выход (`2.3`).
// Ни пустой страницы, ни голого 502: и то и другое читается как «мир сломан».
func TestМёртваяЗастройкаНазываетПричинуИВыход(t *testing.T) {
	мёртвый := свободныйАдрес(t) // порт, на котором никто не слушает
	g := сторожСЗастройкой(t, мёртвый)

	код, тело := ответ(t, g, http.MethodGet, "/")

	if код != http.StatusBadGateway {
		t.Fatalf("код: получено %d (%v), ожидалось %d", код, тело, http.StatusBadGateway)
	}
	if тело["error"] != "build-unreachable" {
		t.Fatalf("отказ не назван: %v", тело)
	}
	detail, _ := тело["detail"].(string)
	for _, кусок := range []string{мёртвый, "не отвечает", "Выход:", "подними застройку", AddrVar} {
		if !strings.Contains(detail, кусок) {
			t.Errorf("в отказе нет %q: %q", кусок, detail)
		}
	}
	// Место при этом СТОИТ: мёртвая застройка — не смерть локации.
	if код, _ := ответ(t, g, http.MethodGet, "/healthz"); код != http.StatusOK {
		t.Errorf("место легло из-за мёртвой застройки: healthz дал %d", код)
	}
}

// Дурной адрес застройки НЕ роняет место: скоуп не зависит от того, что на нём
// стоит. Отказ называет форму адреса и то, чем это чинится.
func TestДурнойАдресЗастройкиНеРоняетМесто(t *testing.T) {
	for _, случай := range []struct{ имя, адрес string }{
		{"со схемой", "http://127.0.0.1:3000"},
		{"без порта", "127.0.0.1"},
		{"порт не число", "127.0.0.1:порт"},
		{"порт вне диапазона", "127.0.0.1:70000"},
	} {
		t.Run(случай.имя, func(t *testing.T) {
			g := сторожСЗастройкой(t, случай.адрес)

			код, тело := ответ(t, g, http.MethodGet, "/")
			if код != http.StatusServiceUnavailable || тело["error"] != "build-addr-invalid" {
				t.Fatalf("получено %d %v, ожидалось 503 build-addr-invalid", код, тело)
			}
			if detail, _ := тело["detail"].(string); !strings.Contains(detail, AddrVar) {
				t.Errorf("в отказе нет выхода: %q", detail)
			}
			// Место стоит, и проба жизни говорит, что с адресом не так.
			код, здоровье := ответ(t, g, http.MethodGet, "/healthz")
			if код != http.StatusOK {
				t.Fatalf("место легло из-за дурного адреса застройки: %d", код)
			}
			if msg, _ := здоровье["message"].(string); !strings.Contains(msg, "не годится") {
				t.Errorf("проба жизни молчит о дурном адресе: %q", msg)
			}
		})
	}
}

// Петля: застройку заявили по адресу самого сторожа. Ловится ДО первого
// запроса — иначе место легло бы, ходя само к себе.
func TestЗастройкаПоАдресуСторожаЛовитсяДоПервогоЗапроса(t *testing.T) {
	for _, случай := range []struct {
		имя, адрес, слушает string
		петля               bool
	}{
		{имя: "тот же порт на всех интерфейсах", адрес: "127.0.0.1:8080", слушает: ":8080", петля: true},
		{имя: "ровно тот же адрес", адрес: "127.0.0.1:8080", слушает: "127.0.0.1:8080", петля: true},
		{имя: "свой порт под другим именем", адрес: "probe-loc:8080", слушает: ":8080", петля: true},
		{имя: "другой порт — не петля", адрес: "127.0.0.1:3000", слушает: ":8080", петля: false},
	} {
		t.Run(случай.имя, func(t *testing.T) {
			s := OpenStands(случай.адрес, случай.слушает, func(string, ...any) {})
			изъян := s.Изъян()
			if !случай.петля {
				if изъян != nil {
					t.Fatalf("адрес %q при слушателе %q сочтён петлёй: %v", случай.адрес, случай.слушает, изъян)
				}
				return
			}
			if изъян == nil {
				t.Fatalf("петля не поймана: адрес %q, слушаем %q", случай.адрес, случай.слушает)
			}
			if !strings.Contains(изъян.Error(), "build-addr-is-guard") {
				t.Errorf("изъян назван не своим именем: %v", изъян)
			}

			g := New("probe-loc", "", nil, s, func(string, ...any) {}, fixedNow)
			код, тело := ответ(t, g, http.MethodGet, "/")
			if код != http.StatusServiceUnavailable || тело["error"] != "build-addr-is-guard" {
				t.Fatalf("получено %d %v, ожидалось 503 build-addr-is-guard", код, тело)
			}
			if detail, _ := тело["detail"].(string); !strings.Contains(detail, "СВОИМ процессом") {
				t.Errorf("в отказе нет выхода: %q", detail)
			}
		})
	}
}

// Сторож остаётся ХОЗЯИНОМ ПОРТА: два пути он держит за собой и при живой
// застройке — проба жизни и ручка постройки. Это названная граница, и отказ на
// затенённом пути говорит, кто ответил.
func TestСторожДержитЗаСобойДваПути(t *testing.T) {
	adr := застройка(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "витрина застройки")
	})
	site := складПодСтройку(t)
	g := New("probe-loc", "проба присутствия", site,
		OpenStands(adr, "127.0.0.1:0", func(string, ...any) {}), func(string, ...any) {}, fixedNow)

	t.Run("проба жизни — сторожа", func(t *testing.T) {
		код, тело := ответ(t, g, http.MethodGet, "/healthz")
		if код != http.StatusOK || тело["mode"] != "guard" {
			t.Fatalf("получено %d %v, ожидался ответ сторожа", код, тело)
		}
	})

	t.Run("ручка постройки — сторожа", func(t *testing.T) {
		код, тело := ответ(t, g, http.MethodGet, BuildPath)
		if код != http.StatusOK || тело["built"] != false {
			t.Fatalf("получено %d %v, ожидался ответ сторожа о пустом месте", код, тело)
		}
	})

	t.Run("затенённый путь называет, кто ответил", func(t *testing.T) {
		код, тело := послать(t, g, http.MethodPut, BuildPath, `{}`)
		if код != http.StatusMethodNotAllowed || тело["error"] != "guard-accepts-nothing" {
			t.Fatalf("получено %d %v, ожидалось 405 guard-accepts-nothing", код, тело)
		}
		if detail, _ := тело["detail"].(string); !strings.Contains(detail, adr) {
			t.Errorf("отказ не говорит, куда ведёт маршрут места: %q", detail)
		}
	})

	t.Run("всё остальное — застройки", func(t *testing.T) {
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/что-то-своё", nil))
		if !strings.Contains(rec.Body.String(), "витрина застройки") {
			t.Errorf("путь мимо двух зарезервированных не уехал в застройку: %s", rec.Body.String())
		}
	})
}

// Трассировка обязана называть, КУДА ушёл запрос: иначе «код 502» на маршруте
// места не отличить от отказа самого сторожа.
func TestТрассировкаНазываетКудаУшёлЗапрос(t *testing.T) {
	adr := застройка(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ок") })
	var строки []string
	g := New("probe-loc", "", nil, OpenStands(adr, "127.0.0.1:0", func(string, ...any) {}),
		func(f string, a ...any) { строки = append(строки, fmt.Sprintf(f, a...)) }, fixedNow)

	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/витрина", nil))

	if len(строки) != 1 {
		t.Fatalf("строк трассировки: получено %d, ожидалась 1", len(строки))
	}
	for _, кусок := range []string{"→ застройка", adr, "code=200"} {
		if !strings.Contains(строки[0], кусок) {
			t.Errorf("в строке трассировки нет %q: %q", кусок, строки[0])
		}
	}
}

// Проба жизни говорит, куда ведёт маршрут места. Без этого при живой застройке
// узнать это снаружи негде: сам маршрут отвечает уже не сторожем.
func TestПробаЖизниГоворитКудаВедётМаршрут(t *testing.T) {
	adr := застройка(t, func(w http.ResponseWriter, _ *http.Request) {})
	g := сторожСЗастройкой(t, adr)

	_, тело := ответ(t, g, http.MethodGet, "/healthz")

	msg, _ := тело["message"].(string)
	if !strings.Contains(msg, "маршрут места ведёт в застройку") || !strings.Contains(msg, adr) {
		t.Errorf("проба жизни не говорит, куда ведёт маршрут: %q", msg)
	}
}

// Пустой хост («:3000») — законный адрес застройки: он значит «внутри места».
// Требовать здесь хост значило бы отказывать на том, что работает.
func TestАдресБезХостаЧитаетсяКакПетляВнутриМеста(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "застройка на петле")
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("слушатель не поднялся: %v", err)
	}
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	_, порт, _ := net.SplitHostPort(ln.Addr().String())
	g := сторожСЗастройкой(t, ":"+порт)

	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "застройка на петле") {
		t.Fatalf("адрес без хоста не довёл до застройки: %d %s", rec.Code, rec.Body.String())
	}
}
