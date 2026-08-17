// Пакет state — ФОРМАТ СОСТОЯНИЯ СКОУПА: то, что раздача отдаёт и принимает файлом целиком.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ФОРМА ЗДЕСЬ НЕ ПРИДУМАНА — ОНА ЗАВЕДЕНА КАНОНОМ (`WORLD2` 3.4, «Формат состояния —    │
// │ базовый»). Это РОЗЕТКА (`0.3`): форма одна на всех, а раздача под неё — чья угодно.   │
// │                                                                                      │
// │ Один файл, JSON, версия формата первым полем, разделы `личность` · `ключи` ·          │
// │ `территории` · `поля`. Пустые списки законны: свежесозданный скоуп — это имя и        │
// │ пустота, и пустой бренд тоже законен (`WORLD2-135`).                                 │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// ПОЧЕМУ РАЗБОР ЖИВЁТ У КОНТРОЛЛЕРА, А НЕ У РАЗДАЧИ. Раздача файл не разбирает вовсе —
// она отдаёт и принимает байты (`share/PROTOCOL.md`). Значит следит за содержимым тот, кто
// пишет: версия формата, состав разделов и НЕПОВТОРИМОСТЬ имени участка (`2.5` п. 11) —
// наша забота. Вторая проверка того же самого на стороне раздачи завела бы вторую правду
// о формате, и разъехались бы они молча.
//
// Имена полей русские, потому что таков формат мира, а не потому, что так удобнее коду.
package state

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/omnifield/world/control/internal/refusal"
)

// Version — версия формата, которую контроллер умеет. Одна: «умею 1» — это знание, а
// «умею всё» — обещание, которого никто не давал. Приехала другая — отказ с причиной и
// выходом (`WORLD2` 3.4, «Почему так», п. 3).
const Version = 1

// UserKeyName — имя ключа, который контроллер заводит САМ, когда юзер дал пароль вместо
// ключа (`WORLD2-141`). Имя ОДНО на скоуп, и это не мелочь: территории ссылаются на ключ
// по имени (`3.4`, «Почему так», п. 4), а один ключ на все машины означает, что повторный
// заход не плодит строк в чужих `authorized_keys` — второй раз кладётся то же самое.
const UserKeyName = "ключ юзера"

// KindSSH — вид креды в связке. Видов в формате три (`ssh|токен|пароль`); контроллер
// кладёт ssh-ключ, потому что этим ходит докер до чужой машины (`TECH` 3).
const KindSSH = "ssh"

// State — состояние скоупа целиком. Именно ЦЕЛИКОМ: раздача отдаёт и принимает файл, и
// частей у него нет (`WORLD2` 3.4, «Что именно раздаётся»).
type State struct {
	Format      int         `json:"формат"`
	Identity    Identity    `json:"личность"`
	Keys        []Key       `json:"ключи"`
	Territories []Territory `json:"территории"`
	Fields      []Field     `json:"поля"`
}

// Identity — сам юзер: имя, бренд, описание. Пароля скоупа здесь нет и быть не может — им
// закрыта РАЗДАЧА, и класть его внутрь файла значит запирать ключ внутри замка (`3.4`).
type Identity struct {
	Name  string `json:"имя"`
	Brand string `json:"бренд"`
	About string `json:"описание"`
}

// Key — связка ключей. Ключи лежат В ОДНОМ МЕСТЕ, а территории и поля ссылаются на них по
// имени (`3.4`, «Почему так», п. 4): иначе одни и те же креды расползаются и однажды
// разъезжаются.
type Key struct {
	Name  string `json:"имя"`
	Kind  string `json:"вид"`
	Value string `json:"значение"`
}

// Territory — участок юзера: чем его зовут, чем до него дотягиваются и каким ключом.
//
// `имя` — ЕДИНСТВЕННОЕ поле формата с требованием неповторимости (`2.5` п. 11): на нём
// стоит адрес локации (`2.1` п. 2), и два участка под одним именем означают столкнувшиеся
// адреса. Имя даёт юзер — мир его не выдумывает и из адреса машины не выводит.
type Territory struct {
	Name string `json:"имя"`
	Addr string `json:"адрес"`
	Key  string `json:"ключ"`
}

// Field — поле юзера. Пока это ЗАПИСЬ о поле: само поле разбирается следующей ступенью, и
// опережать её здесь нечем.
type Field struct {
	Name  string `json:"имя"`
	Addr  string `json:"адрес"`
	State string `json:"состояние"`
}

// New — свежее состояние: личность и пустота. Пустые списки заводятся сразу, а не «когда
// появится первое»: отсутствующий раздел и пустой раздел читались бы одинаково, а чинятся
// по-разному (`WORLD2` 4.2).
func New(name, brand string) *State {
	return &State{
		Format:      Version,
		Identity:    Identity{Name: name, Brand: brand},
		Keys:        []Key{},
		Territories: []Territory{},
		Fields:      []Field{},
	}
}

// Parse — то, что приехало по адресу, прочитанное как состояние.
//
// Отказ обязан называть, ЧТО приехало и чего ждали (`WORLD2` 2.3, порча 2 приёмки
// `WORLD2-132`): «не тот формат» без этих двух вещей человеку нечего чинить.
func Parse(data []byte) (*State, *refusal.Refusal) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, refusal.New(http.StatusBadGateway, "scope-broken",
			"по адресу ответили пустотой, а состояние — это файл",
			"посмотри глазами, что там лежит: curl -u :<пароль> <адрес скоупа>",
			"состояние стёрто — заведи его заново: POST /api/scope")
	}

	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, refusal.New(http.StatusBadGateway, "scope-broken",
			fmt.Sprintf("по адресу лежит не состояние: приехало %s, а прочитать это как JSON не вышло (%v)", sample(data), err),
			"ждали формат "+fmt.Sprint(Version)+": JSON с полями формат · личность · ключи · территории · поля (`WORLD2` 3.4)",
			"посмотри глазами: curl -u :<пароль> <адрес скоупа>",
			"по адресу может стоять не раздача скоупа, а что-то другое — проверь адрес")
	}

	if st.Format == 0 {
		return nil, refusal.New(http.StatusBadGateway, "scope-broken",
			fmt.Sprintf("в файле по адресу нет поля «формат», а без него неизвестно, как его читать: приехало %s", sample(data)),
			"ждали формат "+fmt.Sprint(Version)+": версия формата — первое поле (`WORLD2` 3.4)",
			"если файл писали руками — допиши «формат»: "+fmt.Sprint(Version))
	}
	if st.Format != Version {
		return nil, refusal.New(http.StatusBadGateway, "bad-format",
			fmt.Sprintf("по адресу лежит состояние формата %d, а этот контроллер умеет %d", st.Format, Version),
			"возьми контроллер, который умеет формат "+fmt.Sprint(st.Format),
			"или назови адрес, где лежит состояние формата "+fmt.Sprint(Version))
	}
	if strings.TrimSpace(st.Identity.Name) == "" {
		return nil, refusal.New(http.StatusBadGateway, "scope-broken",
			"в личности по этому адресу нет имени — а имя это то, чем юзер зовётся в мире",
			"поправь файл: раздел «личность», поле «имя»",
			"или назови другой адрес")
	}
	// Пустой бренд НЕ поломка: свежесозданный скоуп — это имя и пустота (`WORLD2-135`).
	// Пустые списки — тоже (`3.4`, «Почему так», п. 5).

	if ref := st.checkNames(); ref != nil {
		return nil, ref
	}
	st.fill()
	return &st, nil
}

// checkNames — неповторимость имени участка, проверенная НА ЧТЕНИИ. Файл могли написать
// руками или другой вилкой: приняв дубль молча, контроллер повёз бы дальше столкнувшиеся
// адреса локаций и обнаружилось бы это не здесь (`2.5` п. 11, `2.1` п. 4).
func (s *State) checkNames() *refusal.Refusal {
	seen := map[string]bool{}
	for _, t := range s.Territories {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			return refusal.New(http.StatusBadGateway, "scope-broken",
				"в разделе «территории» есть участок без имени, а имя участка — часть адреса локации",
				"поправь файл: у каждого участка своё имя (`WORLD2` 2.5 п. 11)")
		}
		if seen[name] {
			return refusal.New(http.StatusBadGateway, "scope-broken",
				fmt.Sprintf("в разделе «территории» два участка с именем «%s» — на имени стоит адрес локации, и адреса столкнулись", name),
				"поправь файл: имя участка в скоупе не повторяется (`WORLD2` 2.5 п. 11)")
		}
		seen[name] = true
	}
	return nil
}

// fill — пустые разделы вместо `null`. Разница видна не нам, а тому, кто потом читает
// файл глазами: `null` читается как «раздела нет», а его законное состояние — пустой.
func (s *State) fill() {
	if s.Keys == nil {
		s.Keys = []Key{}
	}
	if s.Territories == nil {
		s.Territories = []Territory{}
	}
	if s.Fields == nil {
		s.Fields = []Field{}
	}
}

// Bytes — состояние файлом. С отступами и переводом строки в конце: файл читает ещё и
// человек, а «форма записи мягкая» (`0.3` п. 3) — читаемость здесь бесплатна.
func (s *State) Bytes() ([]byte, error) {
	s.fill()
	if s.Format == 0 {
		s.Format = Version
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// ── территории ───────────────────────────────────────────────────────────────

// Territory — участок по имени. Второе возвращаемое значение отличает «нет такого» от
// «есть, и он пустой».
func (s *State) Territory(name string) (Territory, bool) {
	for _, t := range s.Territories {
		if t.Name == name {
			return t, true
		}
	}
	return Territory{}, false
}

// AddTerritory — завести участок. Имя занято — это ОТКАЗ МЕХАНИКИ (`2.3`, первый род):
// проверяем мы, по содержимому скоупа, до всякого докера. Причина и выход обязательны —
// молчаливая перезапись строки в файле означала бы потерянный участок.
func (s *State) AddTerritory(t Territory, key Key) *refusal.Refusal {
	if _, busy := s.Territory(t.Name); busy {
		return refusal.New(http.StatusConflict, "name-taken",
			fmt.Sprintf("участок с именем «%s» в твоём скоупе уже есть, а на имени стоит адрес локации — два участка под одним именем столкнули бы адреса", t.Name),
			"назови другое имя",
			"посмотри, какие уже есть: GET /api/resources",
			"это тот же участок и ты хочешь его пересобрать — сними его сначала: DELETE /api/resources/"+t.Name)
	}
	s.fill()
	if key.Value != "" {
		s.setKey(key)
		t.Key = key.Name
	}
	s.Territories = append(s.Territories, t)
	return nil
}

// DropTerritory — убрать участок и его ключ. Ключ уходит вместе с участком: связка держит
// ключи к тому, что есть, а ключ без участка — это след, переживший свою вещь.
func (s *State) DropTerritory(name string) *refusal.Refusal {
	t, found := s.Territory(name)
	if !found {
		return refusal.New(http.StatusNotFound, "no-such-resource",
			fmt.Sprintf("участка «%s» в твоём скоупе нет — снимать нечего", name),
			"посмотри, какие есть: GET /api/resources")
	}
	kept := s.Territories[:0]
	for _, cur := range s.Territories {
		if cur.Name != name {
			kept = append(kept, cur)
		}
	}
	s.Territories = kept
	if t.Key != "" && !s.keyUsed(t.Key) {
		s.dropKey(t.Key)
	}
	s.fill()
	return nil
}

// ── связка ключей ────────────────────────────────────────────────────────────

// Key — ключ по имени.
func (s *State) Key(name string) (Key, bool) {
	for _, k := range s.Keys {
		if k.Name == name {
			return k, true
		}
	}
	return Key{}, false
}

// SetKey — положить ключ в связку скоупа. Нужен там, где ключ заводится РАНЬШЕ территории:
// пароль обменивается на ключ до того, как на машине что-либо поднято.
func (s *State) SetKey(k Key) {
	s.fill()
	s.setKey(k)
}

func (s *State) setKey(k Key) {
	for i := range s.Keys {
		if s.Keys[i].Name == k.Name {
			s.Keys[i] = k
			return
		}
	}
	s.Keys = append(s.Keys, k)
}

func (s *State) dropKey(name string) {
	kept := s.Keys[:0]
	for _, k := range s.Keys {
		if k.Name != name {
			kept = append(kept, k)
		}
	}
	s.Keys = kept
}

// keyUsed — на ключ ссылается кто-то ещё. Ключ у мира один на связку, и снимать его вместе
// с одним участком, пока на него смотрит второй, значит оборвать связь с живой машиной.
func (s *State) keyUsed(name string) bool {
	for _, t := range s.Territories {
		if t.Key == name {
			return true
		}
	}
	return false
}

// ── поля ─────────────────────────────────────────────────────────────────────

// AddField — завести ЗАПИСЬ о поле. Само поле при этом не поднимается, и говорить об этом
// надо вслух: человек, не увидевший этой строки, ждал бы поднятого поля.
func (s *State) AddField(name string) *refusal.Refusal {
	name = strings.TrimSpace(name)
	if name == "" {
		return refusal.New(http.StatusBadRequest, "no-name",
			"у поля должно быть имя, а оно не названо",
			"назови его: name=«дом»")
	}
	for _, f := range s.Fields {
		if f.Name == name {
			return refusal.New(http.StatusConflict, "field-exists",
				fmt.Sprintf("поле «%s» в твоём списке уже есть", name),
				"назови другое имя",
				"или посмотри список: GET /api/fields")
		}
	}
	s.fill()
	s.Fields = append(s.Fields, Field{Name: name})
	return nil
}

// sample — что именно приехало, коротким куском. Целиком чужой файл в отказ не кладём:
// человеку нужна причина, а не дамп; но БЕЗ образца отказ «не тот формат» не проверить.
func sample(data []byte) string {
	const limit = 60
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "пустота"
	}
	if !utf8.ValidString(text) {
		return fmt.Sprintf("%d байт, и это не текст", len(data))
	}
	if line, _, cut := strings.Cut(text, "\n"); cut {
		text = line
	}
	runes := []rune(text)
	if len(runes) > limit {
		return fmt.Sprintf("%d байт, начинается с «%s…»", len(data), string(runes[:limit]))
	}
	return fmt.Sprintf("%d байт: «%s»", len(data), text)
}
