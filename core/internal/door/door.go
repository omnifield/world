// Package door — вход в поле: реестр локаций и маршрутизация к ним.
//
// Дверь `:8080` — вещь МИРА, а не продукта (`kb:WORLD-53`): она умирает вместе
// с полем. Отсюда главный инвариант пакета:
//
//	МАРШРУТ ВЫВОДИТСЯ ИЗ РЕГИСТРАЦИИ, а не задаётся конфигом.
//
// Локация зарегистрировалась — она достижима снаружи по своему имени; снялась —
// маршрут исчез. Это ОДНО действие, а не два, и потому «поднять локацию» больше
// не упирается в человека, правящего чужой конфиг руками.
//
// Имя за дверью = имя локации. Сегодня это совпадение, здесь оно становится
// правилом: разойдись они однажды — и список за дверью снова превратится в
// чью-то память.
//
// Отказы — по `kb:WORLD-31`: каждый называет ПРИЧИНУ и ВЫХОД. Отказ, из которого
// не видно, что делать дальше, здесь считается провалом, а не мелочью; поэтому
// в `detail` всегда стоит следующее действие, а не только диагноз.
//
// Чего пакет НЕ делает сознательно:
//   - не заводит допуск и учётки — форма настройки «кого пускаем» не решена
//     (`kb:WORLD-53`), и вход в первой редакции свободен внутри своей сети;
//   - не изобретает формат объявления локации: берёт минимум, нужный двери
//     (имя · адрес · что даёт). Форма объявления участка открыта (`kb:WORLD-50`).
package door

import (
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// MaxBody — потолок тела запроса к реестру. Регистрация — три строки; всё, что
// больше килобайтов, регистрацией не является.
const MaxBody = 8 << 10

// Имя локации уходит В ПУТЬ за дверью и в имя в сети (`kb:FUND-5`: alias =
// имя = ключ реестра). Поэтому алфавит закрытый и совпадает с алфавитом
// DNS-метки: строчная латиница, цифры, дефис; длина до 63 символов. Так
// «имя за дверью = имя локации» держится САМИМ НАБОРОМ СИМВОЛОВ, а не
// проверкой постфактум, и `../` в маршрут не попадает по построению.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ReservedNames — первые сегменты путей, которые дверь держит за собой.
// Локация с таким именем была бы недостижима: её маршрут перекрыт собственной
// ручкой двери, и снаружи это выглядело бы как «зарегистрировался, а не
// работает». Поэтому такое имя отвергается на регистрации, а не молча.
//
// Список идёт ВМЕСТЕ с маршрутами `cmd/world`: добавил ручку верхнего уровня —
// добавь имя сюда. За рассинхроном следит тест в пакете main.
var ReservedNames = []string{"api", "healthz"}

// Location — строка реестра: всё, что двери нужно знать о локации.
// Больше здесь не появляется намеренно (см. док пакета).
type Location struct {
	Name  string `json:"name"`  // имя = маршрут за дверью
	Addr  string `json:"addr"`  // адрес в общей сети: «имя:порт», без схемы
	Gives string `json:"gives"` // что даёт — одной строкой, для списка за дверью
	Since string `json:"since"` // когда зарегистрирована, RFC3339
}

// Route — путь, по которому локация достижима снаружи. Считается ИЗ имени,
// нигде не хранится: хранить его отдельно значит завести вторую истину.
func (l Location) Route() string { return "/" + l.Name + "/" }

// apiError — отказ, который называет причину и выход. «bad request» без
// подробностей заставляет автора локации гадать, а локация на другой машине.
type apiError struct {
	status int
	Code   string `json:"error"`
	Detail string `json:"detail"`
}

func (e *apiError) Error() string { return e.Code + ": " + e.Detail }

func errf(status int, code, format string, args ...any) *apiError {
	return &apiError{status: status, Code: code, Detail: fmt.Sprintf(format, args...)}
}

// validName проверяет имя ДО любой записи: имя — это маршрут, и неверное имя
// означает недостижимую локацию, а не косметику.
func validName(name string) *apiError {
	if name == "" {
		return errf(http.StatusBadRequest, "field-missing",
			"поле «name» обязательно: имя локации и есть её маршрут за дверью")
	}
	if !nameRe.MatchString(name) {
		return errf(http.StatusBadRequest, "name-invalid",
			"имя %q не подходит под %s — имя идёт в путь за дверью и в имя в сети; возьми строчные латинские буквы, цифры и дефис",
			name, nameRe.String())
	}
	for _, r := range ReservedNames {
		if name == r {
			return errf(http.StatusBadRequest, "name-reserved",
				"имя %q дверь держит за собой (%s) — локация с таким именем была бы недостижима; возьми другое имя",
				name, "/"+r+"/…")
		}
	}
	return nil
}

// validAddr проверяет адрес в общей сети. Схему не принимаем: за дверью живёт
// http, и «адрес» здесь — это ровно то, что видит сосед по сети (`kb:FUND-5`).
func validAddr(addr string) *apiError {
	if addr == "" {
		return errf(http.StatusBadRequest, "field-missing",
			"поле «addr» обязательно: без адреса в сети маршрут вести некуда")
	}
	if strings.Contains(addr, "://") {
		return errf(http.StatusBadRequest, "addr-invalid",
			"адрес %q со схемой — дверь ходит к соседу по сети как «имя:порт»; убери «http://»", addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errf(http.StatusBadRequest, "addr-invalid",
			"адрес %q не разбирается как «имя:порт» (%v); порт внутренний, контейнерный — например «baser:3500»", addr, err)
	}
	if host == "" {
		return errf(http.StatusBadRequest, "addr-invalid",
			"в адресе %q нет хоста — дверь резолвит соседа по имени контейнера, например «baser:3500»", addr)
	}
	if strings.ContainsAny(host, "/\\ \t") {
		return errf(http.StatusBadRequest, "addr-invalid",
			"в адресе %q хост содержит недопустимые символы — ожидается имя в сети либо IP", addr)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errf(http.StatusBadRequest, "addr-invalid",
			"порт %q в адресе %q — не число от 1 до 65535; порт берётся внутренний, наружу торчит только дверь", port, addr)
	}
	return nil
}

// loopback — адрес указывает на петлю. Не запрет, а ПРЕДУПРЕЖДЕНИЕ: `kb:WORLD-31`
// разводит эти два случая явно, и запрет на то, что случается само собой, просто
// обходят. Своя петля соседу по сети не видна (`kb:FUND-5`), но в тестах и в
// одном контейнере с дверью петля законна — поэтому мир произносит это вслух,
// а не отказывает.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// splitFirst режет путь на первый сегмент и остаток: «/baser/api/x» →
// («baser», «/api/x»), «/baser» → («baser», ""). Пустой остаток — отдельный
// случай: см. Route в proxy.go.
func splitFirst(path string) (first, rest string) {
	if !strings.HasPrefix(path, "/") {
		return "", path
	}
	s := path[1:]
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}
