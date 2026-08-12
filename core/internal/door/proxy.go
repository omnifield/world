package door

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Probe — «адрес отвечает?». Отдельным типом, потому что на регистрации это
// единственная проверка, которую дверь может сделать честно: всё остальное
// (что там действительно локация и что она даёт обещанное) она знать не может
// и притворяться не будет.
type Probe func(addr string) error

// DialProbe — проба по умолчанию: одно TCP-соединение с таймаутом. Не HTTP-запрос
// сознательно: локация имеет право отвечать 404 на корень, и это не повод
// отказать в регистрации. Вопрос ровно один — есть ли по адресу кто-нибудь.
func DialProbe(timeout time.Duration) Probe {
	return func(addr string) error {
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			return err
		}
		return conn.Close()
	}
}

// proxyEntry — построенный маршрут. Держим его между запросами не ради
// «оптимизации»: у ReverseProxy внутри пул соединений, и пересобирать его на
// каждый запрос значит открывать соседу новое соединение каждый раз.
type proxyEntry struct {
	addr string
	rp   *httputil.ReverseProxy
}

// proxyFor выдаёт маршрут к локации, пересобирая его при смене адреса.
func (h *Handler) proxyFor(loc Location) *httputil.ReverseProxy {
	h.mu.Lock()
	defer h.mu.Unlock()

	if e, ok := h.proxies[loc.Name]; ok && e.addr == loc.Addr {
		return e.rp
	}

	target := &url.URL{Scheme: "http", Host: loc.Addr}
	name := loc.Name
	prefix := "/" + name

	rp := &httputil.ReverseProxy{
		// Rewrite, а не Director: Director объявлен устаревшим, и на нём не
		// видно исходного запроса целиком (сверено с доком net/http/httputil
		// 2026-08-10). Порядок важен — префикс срезаем ДО SetURL, иначе он
		// уедет соседу в путь.
		Rewrite: func(pr *httputil.ProxyRequest) {
			stripPrefix(pr.Out.URL, prefix)
			pr.SetURL(target)
			pr.SetXForwarded()
		},
		// Локация не ответила — это отказ ДВЕРИ, и он называет причину и выход
		// так же, как остальные (`kb:WORLD-31`). Молчаливый 502 от прокси
		// выглядит как «мир сломался», хотя сломана одна локация.
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeErr(w, errf(http.StatusBadGateway, "location-unreachable",
				"локация %q по адресу %s не отвечает: %v; подними её либо сними маршрут: DELETE /api/locations/%s",
				name, loc.Addr, err, name))
		},
	}

	h.proxies[name] = &proxyEntry{addr: loc.Addr, rp: rp}
	return rp
}

// forget убирает построенный маршрут. Зовётся там, где сосед по прежнему адресу
// УШЁЛ: при снятии локации и при её возвращении по новому адресу. Иначе пул
// соединений к ушедшему и запись о нём жили бы до перезапуска двери, а поле уже
// другое.
func (h *Handler) forget(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.proxies, name)
}

// stripPrefix срезает «/<имя>» с пути, уходящего к локации: локация живёт у
// себя в корне и о своём имени за дверью не знает — и знать не должна, иначе
// её нельзя будет поднять без двери.
func stripPrefix(u *url.URL, prefix string) {
	if rest, ok := strings.CutPrefix(u.Path, prefix); ok {
		u.Path = rest
		if u.Path == "" {
			u.Path = "/"
		}
	}
	if u.RawPath == "" {
		return
	}
	if rest, ok := strings.CutPrefix(u.RawPath, prefix); ok {
		u.RawPath = rest
		if u.RawPath == "" {
			u.RawPath = "/"
		}
	}
}

// Route — раздача по первому сегменту пути: имя зарегистрированной локации
// уводит запрос к ней, всё прочее уходит в fallback (статика зоны web).
//
// Порядок именно такой, и это правило, а не удобство: маршрут выводится из
// РЕГИСТРАЦИИ, поэтому реестр спрашивают раньше файловой системы. Иначе файл
// с именем локации молча перехватил бы её маршрут.
func (h *Handler) Route(fallback http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, rest := splitFirst(r.URL.Path)
		if name == "" {
			fallback.ServeHTTP(w, r)
			return
		}
		loc, ok := h.reg.Lookup(name)
		if !ok {
			fallback.ServeHTTP(w, r)
			return
		}

		started := h.now()
		rec := &recorder{ResponseWriter: w, code: http.StatusOK}

		if rest == "" {
			// «/baser» без завершающего слэша: относительные ссылки внутри
			// локации считались бы от корня двери и уехали бы мимо. Отсюда
			// редирект на «/baser/» — ровно то, что делает на этом месте любой
			// обратный прокси.
			u := *r.URL
			u.Path += "/"
			if u.RawPath != "" {
				u.RawPath += "/"
			}
			http.Redirect(rec, r, u.RequestURI(), http.StatusPermanentRedirect)
		} else {
			h.proxyFor(loc).ServeHTTP(rec, r)
		}

		h.logf("door: → %s %s %s addr=%s code=%d dur=%s",
			name, r.Method, r.URL.Path, loc.Addr, rec.code, h.now().Sub(started).Round(time.Microsecond))
	})
}

// recorder — счётчик кода ответа: без него в трассировке нечего писать.
type recorder struct {
	http.ResponseWriter
	code int
}

func (r *recorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap отдаёт исходный ResponseWriter — по нему net/http находит Flush и
// Hijack. Без этого через дверь не пройдут ни поток событий, ни вебсокет:
// прокси просто не увидит нужных интерфейсов у обёртки.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
