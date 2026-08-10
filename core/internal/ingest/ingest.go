// Package ingest — приём журнала испытательного стенда: манифест прогона,
// события попыток постановки и снимок мира.
//
// Пакет называется `ingest`, а не `stand`, по внешней причине: корневой
// .gitignore закрывает `stand/` без привязки к корню, и папка с таким именем
// молча выпадает из гита ГДЕ УГОДНО в дереве. Правило корневое, зона не наша —
// причина названа в отчёте по задаче; переименовывать обратно, не поправив
// правило, значит потерять пакет на первом же коммите.
//
// Граница ответственности проведена жёстко: пакет проверяет ТОЛЬКО ФОРМУ —
// имя сессии, наличие обязательных полей, размер тела. Смысл мира здесь не
// живёт: правило отказа не сверяется ни с каким списком, покрытие правил не
// считается. Это работа проверялки `unity/check-run.mjs`. Сервис пишет —
// гейт судит; смешаешь их, и журнал начнёт подстраиваться под проверку.
//
// Второй инвариант — событие ложится на диск КАК ПРИШЛО. Разбор минимальный
// (наличие полей и seq), а на диск идут исходные байты, сжатые в одну строку
// json.Compact. Распарсить в структуру и сериализовать обратно нельзя: стенд
// имеет право слать больше полей, чем описано, и незнакомые молча пропали бы.
package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
)

// MaxBody — потолок тела запроса. Стенд шлёт строки журнала и снимок мира;
// мегабайта хватает с запасом, а всё, что больше, — не журнал, а авария.
const MaxBody = 1 << 20

// Имя сессии уходит в путь на диске, поэтому список разрешённых символов
// закрытый: строчная латиница, цифры, точка, дефис, подчёркивание. Так `../`
// и абсолютные пути отсекаются самим алфавитом, а не проверкой постфактум.
var sessionRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// apiError — отказ, который называет причину и поле. «bad request» без
// подробностей заставляет автора стенда гадать, а стенд на другой машине.
type apiError struct {
	status int
	Code   string `json:"error"`
	Detail string `json:"detail"`
}

func (e *apiError) Error() string { return e.Code + ": " + e.Detail }

func errf(status int, code, format string, args ...any) *apiError {
	return &apiError{status: status, Code: code, Detail: fmt.Sprintf(format, args...)}
}

// doc — тело запроса, разобранное ровно до уровня «какие поля есть».
// Значения остаются сырыми: их содержимое нас не касается.
type doc map[string]json.RawMessage

// validSession проверяет имя сессии до любого обращения к диску.
func validSession(name string) *apiError {
	if name == "" {
		return errf(http.StatusBadRequest, "session-missing", "поле «session» обязательно")
	}
	if !sessionRe.MatchString(name) {
		return errf(http.StatusBadRequest, "session-invalid",
			"имя сессии %q не подходит под %s — имя идёт в путь на диске", name, sessionRe.String())
	}
	return nil
}

// parseObject разбирает тело как JSON-объект. Массив, число и строка на этих
// ручках — ошибка формы, и названа она именно так, а не «invalid json».
func parseObject(raw []byte) (doc, *apiError) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errf(http.StatusBadRequest, "body-empty", "тело запроса пустое")
	}
	if trimmed[0] != '{' {
		return nil, errf(http.StatusBadRequest, "body-not-object", "ожидался JSON-объект, тело начинается с %q", string(trimmed[0]))
	}
	var d doc
	if err := json.Unmarshal(trimmed, &d); err != nil {
		return nil, errf(http.StatusBadRequest, "json-invalid", "тело не разбирается как JSON: %v", err)
	}
	return d, nil
}

// sessionOf достаёт и проверяет имя сессии из тела.
func sessionOf(d doc) (string, *apiError) {
	raw, ok := d["session"]
	if !ok {
		return "", errf(http.StatusBadRequest, "session-missing", "поле «session» обязательно")
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return "", errf(http.StatusBadRequest, "session-invalid", "поле «session» должно быть строкой, получено %s", raw)
	}
	if e := validSession(name); e != nil {
		return "", e
	}
	return name, nil
}

// checkEvent — форма события: без ts/attempt/allowed журнал нечитаем, а
// allowed обязан быть булевым, иначе «разрешено или отказано» не ответить.
func checkEvent(d doc, where string) *apiError {
	for _, field := range []string{"ts", "attempt", "allowed"} {
		if _, ok := d[field]; !ok {
			return errf(http.StatusBadRequest, "field-missing", "%s: нет обязательного поля «%s»", where, field)
		}
	}
	switch string(bytes.TrimSpace(d["allowed"])) {
	case "true", "false":
	default:
		return errf(http.StatusBadRequest, "field-invalid",
			"%s: поле «allowed» должно быть true или false, получено %s", where, d["allowed"])
	}
	return nil
}

// seqOf — номер события для фильтра `?since=`. Отсутствует или не число —
// ноль: это не повод отказать в записи, seq в обязательных полях не значится.
func seqOf(d doc) int64 {
	raw, ok := d["seq"]
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// compact сжимает объект в одну строку, сохраняя все поля как есть —
// включая те, о которых мы не знаем. Именно это едет в events.jsonl.
func compact(raw []byte) ([]byte, *apiError) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, bytes.TrimSpace(raw)); err != nil {
		return nil, errf(http.StatusBadRequest, "json-invalid", "тело не разбирается как JSON: %v", err)
	}
	return buf.Bytes(), nil
}
