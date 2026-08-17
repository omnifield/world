package state

import (
	"encoding/json"
	"strings"
	"testing"
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
	for _, field := range []string{`"формат": 1`, `"личность"`, `"ключи": []`, `"территории": []`, `"поля": []`} {
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
		{"формат другой версии", `{"формат":2,"личность":{"имя":"егор"}}`, "bad-format"},
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
	if !strings.Contains(strings.Join(ref.Ways, " "), "формат 1") {
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
