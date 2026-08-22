package state

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnifield/world/control/internal/refusal"
)

// Формат состояния — это РОЗЕТКА мира (`WORLD2` 3.4), а не наша внутренняя структура.
// Поэтому проверяется он с двух сторон: что мы пишем ровно ту форму, которую заведёт
// читать чужая вилка, и что чужой файл мы читаем без догадок.

func TestСвежийСкоупЭтоИмяИПустота(t *testing.T) {
	st := New("егор", "")
	data, err := st.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	// Имена полей русские и такие, как в каноне: по ним чужая раздача и чужой инструмент
	// узнают форму. Разъедься они — файл перестанет быть форматом мира.
	for _, field := range []string{`"формат": 2`, `"личность"`, `"ключи": []`, `"территории": []`, `"поля": []`} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("в файле состояния нет %s:\n%s", field, data)
		}
	}

	// Пустой бренд — ЗАКОННОЕ состояние (`WORLD2-135`), а не поломка: свежесозданный скоуп
	// это имя и пустота. Читаться он обязан без единого отказа.
	back, ref := Parse(data)
	if ref != nil {
		t.Fatalf("свой же файл не прочитался: %s", ref.Code)
	}
	if back.Identity.Name != "егор" || back.Identity.Brand != "" {
		t.Fatalf("личность прочиталась не той: %+v", back.Identity)
	}
	if len(back.Territories) != 0 || len(back.Keys) != 0 || len(back.Fields) != 0 {
		t.Fatalf("пустые разделы прочитались не пустыми: %+v", back)
	}
}

func TestПаролюВнутриФайлаМестаНет(t *testing.T) {
	st := New("егор", "омнифилд")
	if err := st.AddTerritory(
		Territory{Name: "vps", Addr: "world@10.8.0.5"},
		Key{Name: "vps", Kind: KindSSH, Value: "-----ключ-----"},
	); err != nil {
		t.Fatal(err.Code)
	}
	data, _ := st.Bytes()
	// Пароль скоупа закрывает РАЗДАЧУ, и внутри файла его быть не может: класть его туда
	// значит запирать ключ внутри замка (`WORLD2` 3.4). Проверяем форму, а не намерение:
	// поля с таким именем в формате нет вовсе.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"пароль", "password"} {
		if _, есть := raw[forbidden]; есть {
			t.Fatalf("в файле состояния появилось поле %q — пароль закрывает раздачу, а не лежит внутри неё", forbidden)
		}
	}
}

func TestЧужойФорматНазываетЧтоПриехалоИЧегоЖдали(t *testing.T) {
	tests := []struct {
		имя  string
		файл string
		код  string
	}{
		{"не JSON вовсе", "<!doctype html>лицо какой-то другой вещи", "scope-broken"},
		{"формат старше нашего", `{"формат":3,"личность":{"имя":"егор"}}`, "bad-format"},
		{"версии нет вовсе", `{"личность":{"имя":"егор"}}`, "scope-broken"},
		{"личность без имени", `{"формат":1,"личность":{"имя":""}}`, "scope-broken"},
		{"пустота", "   ", "scope-broken"},
	}
	for _, tt := range tests {
		t.Run(tt.имя, func(t *testing.T) {
			_, ref := Parse([]byte(tt.файл))
			if ref == nil {
				t.Fatalf("прочиталось молча, а должно было отказать: %s", tt.файл)
			}
			if ref.Code != tt.код {
				t.Fatalf("код отказа %q вместо %q (%s)", ref.Code, tt.код, ref.Why)
			}
			if len(ref.Ways) == 0 || strings.TrimSpace(ref.Why) == "" {
				t.Fatalf("отказ без причины или без выхода — тупик (WORLD2 2.3): %+v", ref)
			}
		})
	}
}

// Отказ обязан называть ЧТО ПРИЕХАЛО и ЧЕГО ЖДАЛИ: без этих двух вещей человеку нечего
// чинить, а «не тот формат» звучит одинаково и для чужой вилки, и для опечатки в адресе.
func TestОтказПоФорматуПоказываетОбразец(t *testing.T) {
	_, ref := Parse([]byte("<!doctype html>совсем другая вещь"))
	if !strings.Contains(ref.Why, "<!doctype html>") {
		t.Fatalf("отказ не показал, что приехало: %s", ref.Why)
	}
	if !strings.Contains(strings.Join(ref.Ways, " "), "формат 2") {
		t.Fatalf("отказ не назвал, чего ждали: %v", ref.Ways)
	}
}

// ИМЯ УЧАСТКА НЕ ПОВТОРЯЕТСЯ (`WORLD2` 2.5 п. 11): на нём стоит адрес локации. Следит за
// этим контроллер — раздача файл не разбирает вовсе.
func TestИмяУчасткаНеПовторяется(t *testing.T) {
	st := New("егор", "")
	if ref := st.AddTerritory(Territory{Name: "vps", Addr: "world@10.8.0.5"}, Key{Name: "vps", Value: "к1"}); ref != nil {
		t.Fatal(ref.Code)
	}
	ref := st.AddTerritory(Territory{Name: "vps", Addr: "world@10.8.0.9"}, Key{Name: "vps", Value: "к2"})
	if ref == nil {
		t.Fatal("второй участок с занятым именем прошёл молча — адреса локаций столкнулись бы")
	}
	if ref.Code != "name-taken" || len(ref.Ways) == 0 {
		t.Fatalf("отказ не тот: %+v", ref)
	}
	if len(st.Territories) != 1 || st.Territories[0].Addr != "world@10.8.0.5" {
		t.Fatalf("отказ перезаписал строку в файле, а не отказал: %+v", st.Territories)
	}
}

func TestДубльВЧужомФайлеПойманНаЧтении(t *testing.T) {
	файл := `{"формат":1,"личность":{"имя":"егор"},"территории":[
		{"имя":"vps","адрес":"world@10.8.0.5"},{"имя":"vps","адрес":"world@10.8.0.9"}]}`
	_, ref := Parse([]byte(файл))
	if ref == nil || ref.Code != "scope-broken" {
		t.Fatalf("дубль в чужом файле принят молча: %+v", ref)
	}
}

func TestКлючУходитВместеСУчасткомНоНеЧужой(t *testing.T) {
	st := New("егор", "")
	_ = st.AddTerritory(Territory{Name: "vps", Addr: "world@10.8.0.5"}, Key{Name: "vps", Kind: KindSSH, Value: "к1"})
	// Второй участок на том же ключе — законная раскладка: ключи лежат в одном месте, а
	// территории ссылаются на них по имени (`WORLD2` 3.4, «Почему так», п. 4).
	_ = st.AddTerritory(Territory{Name: "vps2", Addr: "world@10.8.0.6"}, Key{Name: "vps", Kind: KindSSH, Value: "к1"})

	if ref := st.DropTerritory("vps"); ref != nil {
		t.Fatal(ref.Code)
	}
	if _, есть := st.Key("vps"); !есть {
		t.Fatal("ключ снят, пока на него смотрит второй участок — связь с живой машиной оборвана")
	}
	if ref := st.DropTerritory("vps2"); ref != nil {
		t.Fatal(ref.Code)
	}
	if _, есть := st.Key("vps"); есть {
		t.Fatal("ключ пережил все свои участки — это след, переживший вещь")
	}
}

func TestСнятьЧегоНетЭтоОтказСВыходом(t *testing.T) {
	st := New("егор", "")
	ref := st.DropTerritory("нет-такого")
	if ref == nil || ref.Code != "no-such-resource" || len(ref.Ways) == 0 {
		t.Fatalf("отказ не тот: %+v", ref)
	}
}

func TestПолеЗаводитсяОдинРаз(t *testing.T) {
	st := New("егор", "")
	if ref := st.AddField("дом"); ref != nil {
		t.Fatal(ref.Code)
	}
	if ref := st.AddField("дом"); ref == nil || ref.Code != "field-exists" {
		t.Fatalf("второе поле с тем же именем прошло: %+v", ref)
	}
	if ref := st.AddField("  "); ref == nil || ref.Code != "no-name" {
		t.Fatalf("поле без имени прошло: %+v", ref)
	}
}

// ── формат 2: комьюнити у территории ─────────────────────────────────────────

// ФОРМАТ `1` ЧИТАЕТСЯ КАК СВОЙ, и юзеру делать при этом нечего: у его территорий просто
// пустой список комьюнити. Версия поднимается при первой ЗАПИСИ (`WORLD2` 3.4, формат `2`).
func TestФорматМладшеЧитаетсяАПриЗаписиПоднимается(t *testing.T) {
	старый := `{"формат":1,"личность":{"имя":"егор"},"ключи":[],"поля":[],
		"территории":[{"имя":"vps","адрес":"world@10.8.0.5","ключ":"vps"}]}`
	st, ref := Parse([]byte(старый))
	if ref != nil {
		t.Fatalf("состояние формата 1 не прочиталось: %s — %s", ref.Code, ref.Why)
	}
	t.Run("у территории пустой список комьюнити", func(t *testing.T) {
		if len(st.Territories) != 1 {
			t.Fatalf("территория потерялась: %+v", st.Territories)
		}
		// Пустой список, а не `nil`: разница видна тому, кто читает файл глазами.
		if st.Territories[0].Fields == nil || len(st.Territories[0].Fields) != 0 {
			t.Fatalf("список комьюнити прочитался не пустым: %+v", st.Territories[0].Fields)
		}
	})
	t.Run("запись поднимает формат", func(t *testing.T) {
		data, err := st.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"формат": 2`) {
			t.Fatalf("формат при записи не поднялся:\n%s", data)
		}
		if !strings.Contains(string(data), `"поля": []`) {
			t.Fatalf("у территории не появился раздел комьюнити:\n%s", data)
		}
	})
}

// ГЛАВНОЕ СВОЙСТВО СТУПЕНИ: адрес локации от комьюнити не зависит вовсе (`WORLD2` 2.1 п. 5,
// `2.2` п. 4). Присоединился к трём, ушёл из всех — адрес тот же.
func TestАдресЛокацииНеДвигаетсяОтКомьюнити(t *testing.T) {
	st := New("егор", "")
	if ref := st.AddTerritory(Territory{Name: "vps", Addr: "world@10.8.0.5"}, Key{Name: "vps", Value: "к"}); ref != nil {
		t.Fatal(ref.Code)
	}
	for _, имя := range []string{"дом", "работа"} {
		if ref := st.AddField(имя); ref != nil {
			t.Fatal(ref.Code)
		}
	}

	было, ref := st.LocationAddress("vps", "baser")
	if ref != nil {
		t.Fatal(ref.Code)
	}
	if было.String() != "егор/vps/baser" {
		t.Fatalf("адрес собрался не тремя ярусами: %s", было)
	}

	for _, имя := range []string{"дом", "работа"} {
		if ref := st.JoinField("vps", имя); ref != nil {
			t.Fatalf("присоединение к «%s» не прошло: %s", имя, ref.Why)
		}
		стало, ref := st.LocationAddress("vps", "baser")
		if ref != nil {
			t.Fatal(ref.Code)
		}
		if стало != было {
			t.Fatalf("адрес сдвинулся от присоединения к «%s»: %s → %s", имя, было, стало)
		}
	}
	if len(st.Territories[0].Fields) != 2 {
		t.Fatalf("участок не состоит в двух комьюнити разом: %+v", st.Territories[0].Fields)
	}

	for _, имя := range []string{"дом", "работа"} {
		if ref := st.LeaveField("vps", имя); ref != nil {
			t.Fatalf("отвязка от «%s» не прошла: %s", имя, ref.Why)
		}
		стало, ref := st.LocationAddress("vps", "baser")
		if ref != nil {
			t.Fatal(ref.Code)
		}
		if стало != было {
			t.Fatalf("адрес сдвинулся от отвязки от «%s»: %s → %s", имя, было, стало)
		}
	}
}

// А ЧТО АДРЕС ДВИГАЕТ — ровно два хода (`2.1`, «Цена названа»): перенос локации на другой
// участок и переименование самого участка. Проверяем оба, иначе проверка выше зеленела бы и
// на адресе, который не двигается вообще ни от чего, — то есть ничего не стерегла бы.
func TestАдресДвигаютТолькоЯрусы(t *testing.T) {
	st := New("егор", "")
	for _, имя := range []string{"vps", "home"} {
		if ref := st.AddTerritory(Territory{Name: имя, Addr: "world@10.8.0.5"}, Key{Name: имя, Value: "к"}); ref != nil {
			t.Fatal(ref.Code)
		}
	}
	на := func(участок, локация string) string {
		а, ref := st.LocationAddress(участок, локация)
		if ref != nil {
			t.Fatal(ref.Code)
		}
		return а.String()
	}
	if на("vps", "baser") == на("home", "baser") {
		t.Fatal("одна и та же локация на двух участках дала один адрес — территории в формуле нет")
	}
	if на("vps", "baser") == на("vps", "весы") {
		t.Fatal("две локации на одном участке дали один адрес — имени в формуле нет")
	}
	// Переименование участка = снять и завести под другим именем: своей ручки у этого хода
	// сегодня нет, и выдумывать её здесь нечего.
	до := на("vps", "baser")
	if ref := st.DropTerritory("vps"); ref != nil {
		t.Fatal(ref.Code)
	}
	if ref := st.AddTerritory(Territory{Name: "vps-2", Addr: "world@10.8.0.5"}, Key{Name: "vps-2", Value: "к"}); ref != nil {
		t.Fatal(ref.Code)
	}
	if до == на("vps-2", "baser") {
		t.Fatal("переименование участка адрес не сдвинуло — а оно обязано двигать (`2.1`)")
	}
}

// Участок без комьюнити — ОБЫЧНОЕ состояние (`2.5` п. 4), а не недоделка.
func TestУчастокБезКомьюнитиЗаконен(t *testing.T) {
	st := New("егор", "")
	if ref := st.AddTerritory(Territory{Name: "vps", Addr: "world@10.8.0.5"}, Key{Name: "vps", Value: "к"}); ref != nil {
		t.Fatal(ref.Code)
	}
	data, err := st.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"поля": []`) {
		t.Fatalf("у участка без комьюнити раздел не пустой список:\n%s", data)
	}
	back, ref := Parse(data)
	if ref != nil {
		t.Fatalf("свой же файл не прочитался: %s — %s", ref.Code, ref.Why)
	}
	if _, ref := back.LocationAddress("vps", "baser"); ref != nil {
		t.Fatalf("у участка без комьюнити не считается адрес локации: %s", ref.Why)
	}
}

// Ни одного молчаливого исхода (`WORLD2` 2.3): каждое «не вышло» приходит кодом, причиной и
// выходом.
func TestОтказыПриСоединенииИОтвязке(t *testing.T) {
	собрать := func() *State {
		st := New("егор", "")
		if ref := st.AddTerritory(Territory{Name: "vps", Addr: "world@10.8.0.5"}, Key{Name: "vps", Value: "к"}); ref != nil {
			t.Fatal(ref.Code)
		}
		if ref := st.AddField("дом"); ref != nil {
			t.Fatal(ref.Code)
		}
		return st
	}
	tests := []struct {
		имя string
		ход func(*State) *refusal.Refusal
		код string
	}{
		{"участка нет", func(s *State) *refusal.Refusal { return s.JoinField("нет-такого", "дом") }, "no-such-resource"},
		{"комьюнити не названо", func(s *State) *refusal.Refusal { return s.JoinField("vps", "  ") }, "no-name"},
		{"комьюнити не записано в личности", func(s *State) *refusal.Refusal { return s.JoinField("vps", "чужое") }, "no-such-field"},
		{"состоит уже", func(s *State) *refusal.Refusal {
			if ref := s.JoinField("vps", "дом"); ref != nil {
				t.Fatal(ref.Code)
			}
			return s.JoinField("vps", "дом")
		}, "already-joined"},
		{"не состоит вовсе", func(s *State) *refusal.Refusal { return s.LeaveField("vps", "дом") }, "not-joined"},
		{"отвязка с чужого участка", func(s *State) *refusal.Refusal { return s.LeaveField("нет-такого", "дом") }, "no-such-resource"},
	}
	for _, tt := range tests {
		t.Run(tt.имя, func(t *testing.T) {
			st := собрать()
			ref := tt.ход(st)
			if ref == nil {
				t.Fatal("прошло молча, а должно было отказать")
			}
			if ref.Code != tt.код {
				t.Fatalf("код отказа %q вместо %q (%s)", ref.Code, tt.код, ref.Why)
			}
			if strings.TrimSpace(ref.Why) == "" || len(ref.Ways) == 0 {
				t.Fatalf("отказ без причины или без выхода — тупик: %+v", ref)
			}
		})
	}
}

// Дубль в списке комьюнити — такая же поломка чужого файла, как дубль имени участка: при
// отвязке участок остался бы там наполовину.
func TestДубльКомьюнитиВЧужомФайлеПойманНаЧтении(t *testing.T) {
	файл := `{"формат":2,"личность":{"имя":"егор"},"территории":[
		{"имя":"vps","адрес":"world@10.8.0.5","поля":["дом","дом"]}]}`
	_, ref := Parse([]byte(файл))
	if ref == nil || ref.Code != "scope-broken" {
		t.Fatalf("дубль комьюнити принят молча: %+v", ref)
	}
}

// РЕСУРС ЗАЯВЛЯЕТСЯ, А НЕ ИЗМЕРЯЕТСЯ (`2.5` пп. 2, 6, 7). Мир его не меряет и им не
// отказывает: нехватка ресурса — данность, а не отказ (`0.2`).
func TestРесурсЗаявляетсяИНеСторожится(t *testing.T) {
	st := New("егор", "")
	if ref := st.AddTerritory(Territory{Name: "vps", Addr: "world@10.8.0.5"}, Key{Name: "vps", Value: "к"}); ref != nil {
		t.Fatal(ref.Code)
	}
	// Пустое значение законно и означает «не заявлял»: в файл оно не уезжает вовсе.
	data, _ := st.Bytes()
	if strings.Contains(string(data), `"ресурс"`) {
		t.Fatalf("незаявленный ресурс уехал в файл пустой строкой:\n%s", data)
	}
	// Заявить можно ЧТО УГОДНО: ни единицы измерения, ни разбора, ни отказа.
	for _, что := range []string{"2 ядра, 4 ГБ", "слабая, только дверь", "很大", "   "} {
		if ref := st.Declare("vps", что); ref != nil {
			t.Fatalf("мир отказал по ресурсу %q — а нехватка ресурса это данность: %+v", что, ref)
		}
	}
	if ref := st.Declare("нет-такого", "что-то"); ref == nil || ref.Code != "no-such-resource" {
		t.Fatalf("заявление за несуществующий участок прошло: %+v", ref)
	}
	if ref := st.Declare("vps", "2 ядра, 4 ГБ"); ref != nil {
		t.Fatal(ref.Code)
	}
	back, ref := Parse(мустБайты(t, st))
	if ref != nil {
		t.Fatal(ref.Code)
	}
	if back.Territories[0].Resource != "2 ядра, 4 ГБ" {
		t.Fatalf("заявленное не пережило запись и чтение: %q", back.Territories[0].Resource)
	}
}

func мустБайты(t *testing.T, s *State) []byte {
	t.Helper()
	data, err := s.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Раздача файл не разбирает и `null` вместо пустого списка вернуть может кто угодно. Читаем
// это как пустое, а пишем всегда пустым: `null` читается человеком как «раздела нет».
func TestNullРазделыЧитаютсяКакПустые(t *testing.T) {
	st, ref := Parse([]byte(`{"формат":1,"личность":{"имя":"егор"},"ключи":null,"территории":null,"поля":null}`))
	if ref != nil {
		t.Fatal(ref.Code)
	}
	data, _ := st.Bytes()
	if strings.Contains(string(data), "null") {
		t.Fatalf("в записанный файл уехал null:\n%s", data)
	}
}
