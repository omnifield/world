// Пакет refusal — единственная форма, которой контроллер говорит «нет».
//
// Тройка обязательна целиком: код — машине, причина и выход — человеку (`WORLD2` 2.3).
// Пустое «не получилось» — провал самого мира: юзеру нечего чинить.
//
//	{"code":"no-scope","why":"по этому адресу скоупа нет…","ways":["завести здесь: …"]}
//
// Почему `ways` — массив, а не строка: выходов почти всегда несколько (починить, назвать
// иначе, посмотреть список), и склеенные в одну строку они перестают быть выходами и
// становятся абзацем, который не читают.
//
// Почему коды соседних зон приходят СВОИМИ, а не переводятся в наши: контроллер зовёт
// готовое (`deploy/remote.sh`), и `no-docker` от него — это и есть точная причина. Таблица
// перевода чужих кодов в свои завела бы второй словарь того же самого: он разъедется с
// первым на первой же правке соседа, и человека начнёт отправлять чинить не то. Откуда
// пришёл код, видно по полю `from`.
package refusal

import (
	"fmt"
	"net/http"
)

// Refusal — отказ. Он же ошибка: `*Refusal` возвращается вместо `error` там, где отказ
// обязан доехать до человека целиком, а не схлопнуться в строку.
type Refusal struct {
	// Status — код HTTP. В теле его нет намеренно: два разных кода одного и того же
	// разъедутся, а машине хватает `code`.
	Status int `json:"-"`
	// Code — машине. Проба стережёт ЕГО, а не формулировку: проба, привязанная к тексту,
	// зеленеет на верной правке и учит не трогать буквы (`WORLD2` 4.2).
	Code string `json:"code"`
	// Why — причина человеку. Одной фразой и по-человечески.
	Why string `json:"why"`
	// Ways — выходы. Пустым не бывает: отказ без выхода — тупик.
	Ways []string `json:"ways"`
	// From — чей это код, если он пришёл от соседнего инструмента (`deploy/remote.sh`).
	// Своим отказам поле не нужно и в ответе не появляется.
	From string `json:"from,omitempty"`
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Why }

// New — отказ со своим кодом. Хотя бы один выход обязателен: без него отказ не собрать,
// и это проверяется не ревью, а сигнатурой.
func New(status int, code, why string, way string, more ...string) *Refusal {
	return &Refusal{
		Status: status,
		Code:   code,
		Why:    why,
		Ways:   append([]string{way}, more...),
	}
}

// Newf — то же, но причина собирается по образцу.
func Newf(status int, code, why string, args ...any) *Refusal {
	return &Refusal{Status: status, Code: code, Why: fmt.Sprintf(why, args...)}
}

// With добавляет выходы отказу, собранному через Newf.
func (r *Refusal) With(ways ...string) *Refusal {
	r.Ways = append(r.Ways, ways...)
	return r
}

// FromTool — отказ, названный соседним инструментом. Код остаётся ЕГО, причина и выходы —
// его же слова, если он их сказал; наше дело — довезти их до человека, не переписывая.
func FromTool(status int, tool, code, why string, ways ...string) *Refusal {
	if status == 0 {
		status = http.StatusBadGateway
	}
	if len(ways) == 0 {
		ways = []string{"что именно случилось — в журнале контроллера: " + tool}
	}
	return &Refusal{Status: status, Code: code, Why: why, Ways: ways, From: tool}
}
