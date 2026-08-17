package recipe

import (
	"os"
	"strings"
	"testing"
)

// Настоящий файл запуска — тот самый, которым раздачу поднимают на машине юзера. Проба
// читает ЕГО, а не образец рядом: сторож, стерегущий копию, не стережёт ничего
// (`WORLD2` 4.2 — важно, ГДЕ проверяем).
func TestНастоящийФайлЗапускаЧист(t *testing.T) {
	data, err := os.ReadFile("../../compose.yaml")
	if err != nil {
		t.Fatalf("файл запуска не прочитался: %v", err)
	}
	if found := Check(data); len(found) > 0 {
		for _, f := range found {
			t.Errorf("%v", f)
		}
		t.Fatal("файл запуска не пройдёт компоузом — правь его, а не эту пробу")
	}
}

// Та самая строка, которая остановила живой выпуск (`WORLD2-140`). Проба стоит здесь
// дословно: вернётся поломка — покраснеет она, а не машина юзера.
func TestДвоеточиеВТекстеПодстановкиКраснеет(t *testing.T) {
	поломка := "      SHARE_PASSWORD: ${SHARE_PASSWORD:?пароль не назван: подними с SHARE_PASSWORD=<пароль>}\n"
	found := Check([]byte(поломка))
	if len(found) != 1 {
		t.Fatalf("находок %d вместо одной: %v", len(found), found)
	}
	if found[0].Line != 1 {
		t.Fatalf("названа строка %d вместо 1", found[0].Line)
	}
	if found[0].Why == "" || found[0].Way == "" {
		t.Fatal("находка без причины или без выхода — это молчание с номером строки (`WORLD2` 2.3)")
	}
}

// И обратное: та же строка в кавычках законна целиком. Правило — «закавычь значение», а
// НЕ «сократи текст отказа»: текст обязан оставаться человечьим.
func TestТотЖеТекстВКавычкахЗаконен(t *testing.T) {
	хорошо := "      SHARE_PASSWORD: \"${SHARE_PASSWORD:?пароль не назван: подними с SHARE_PASSWORD=<пароль> — им закрыта раздача}\"\n"
	if found := Check([]byte(хорошо)); len(found) > 0 {
		t.Fatalf("закавыченное значение покраснело: %v", found)
	}
}

func TestКраснеетНаКлассе(t *testing.T) {
	cases := map[string]string{
		"подстановка без кавычек":  "image: ${SHARE_IMAGE:-ghcr.io/omnifield/world-share:latest}\n",
		"двоеточие с пробелом":     "description: имя: значение\n",
		"решётка обрежет значение": "note: значение и хвост # уже комментарий\n",
		"кавычка не закрыта":       "name: \"${SHARE_NAME:-world-share}\n",
		"таб в отступе":            "services:\n\tshare:\n",
	}
	for имя, текст := range cases {
		if found := Check([]byte(текст)); len(found) == 0 {
			t.Errorf("%s: сторож промолчал", имя)
		}
	}
}

// Чего сторож краснеть НЕ ДОЛЖЕН. Красный на законном файле учит не смотреть на его
// красноту — это хуже, чем его отсутствие.
func TestЗаконноеНеКраснеет(t *testing.T) {
	cases := map[string]string{
		"короткая запись тома":              "    volumes:\n      - state:/state\n",
		"короткая запись порта":             "    ports:\n      - \"${SHARE_BIND:-0.0.0.0}:${SHARE_PORT:-8070}:8070\"\n",
		"ключ без значения":                 "services:\n  share:\n",
		"комментарий с двоеточием":          "# причина: тут двоеточие и ${подстановка} — и это комментарий\n",
		"пустые строки":                     "\n\n   \n",
		"обычное значение":                  "restart: unless-stopped\n",
		"значение с двоеточием без пробела": "image: ghcr.io/omnifield/world-share:latest\n",
	}
	for имя, текст := range cases {
		if found := Check([]byte(текст)); len(found) > 0 {
			t.Errorf("%s: покраснело зря — %v", имя, found)
		}
	}
}

// Блочный скаляр — это текст, и двоеточия в нём законны. Пропуск устроен по отступу, а не
// по счётчику строк: иначе он разъехался бы с файлом на первой же правке.
func TestБлочныйСкалярПропускается(t *testing.T) {
	текст := "labels:\n  описание: |\n    причина: двоеточие внутри текста законно\n    и ${подстановка} тоже\nname: \"конец\"\n"
	if found := Check([]byte(текст)); len(found) > 0 {
		t.Fatalf("блочный скаляр покраснел: %v", found)
	}
}

// Находка обязана называть саму строку: без неё человек ищет её глазами по файлу.
func TestНаходкаНазываетСтроку(t *testing.T) {
	found := Check([]byte("image: ${X:-y}\n"))
	if len(found) != 1 {
		t.Fatalf("находок %d вместо одной", len(found))
	}
	if !strings.Contains(found[0].String(), "image: ${X:-y}") {
		t.Fatalf("находка не показывает строку: %s", found[0])
	}
}
