package recipe

import (
	"os"
	"strings"
	"testing"
)

// Законный рецепт в самом коротком виде: всё, чем две раздачи различаются, приходит
// снаружи. Порчи ниже делаются ИЗ НЕГО — так порча и законный образец не разъедутся.
const разведённый = `name: "${SHARE_NAME:-world-share}"
services:
  share:
    image: "${SHARE_IMAGE:-ghcr.io/omnifield/world-share:latest}"
    container_name: "${SHARE_NAME:-world-share}"
    ports:
      - "${SHARE_BIND:-0.0.0.0}:${SHARE_PORT:-8070}:8070"
    volumes:
      - state:/state
volumes:
  state:
    name: "${SHARE_NAME:-world-share}-state"
`

// Настоящий файл запуска — тот самый, которым раздачу поднимают на машине юзера
// (`WORLD2` 4.2: важно, ГДЕ мы проверяем). Он обязан разводить две раздачи, а не только
// читаться компоузом.
func TestНастоящийФайлЗапускаРазводитДвеРаздачи(t *testing.T) {
	data, err := os.ReadFile("../../compose.yaml")
	if err != nil {
		t.Fatalf("файл запуска не прочитался: %v", err)
	}
	if found := Coexist(data); len(found) > 0 {
		for _, f := range found {
			t.Errorf("%v", f)
		}
		t.Fatal("этим рецептом вторая раздача встанет поверх первой — правь его, а не эту пробу")
	}
}

func TestРазведённыйНеКраснеет(t *testing.T) {
	if found := Coexist([]byte(разведённый)); len(found) > 0 {
		t.Fatalf("законный рецепт покраснел: %v", found)
	}
}

// Каждая порча — своё столкновение на одной машине. Ни одна не ловится сторожем формы
// записи: испорченный файл остаётся безупречным YAML, и в этом вся беда.
func TestСтолкновениеКраснеет(t *testing.T) {
	cases := map[string]string{
		"имя проекта зашито": strings.Replace(разведённый,
			`name: "${SHARE_NAME:-world-share}"`+"\n", "name: \"world-share\"\n", 1),
		"имя проекта пропало вовсе": strings.Replace(разведённый,
			`name: "${SHARE_NAME:-world-share}"`+"\n", "", 1),
		"имя контейнера зашито": strings.Replace(разведённый,
			`container_name: "${SHARE_NAME:-world-share}"`, `container_name: "world-share"`, 1),
		"имя тома зашито": strings.Replace(разведённый,
			`name: "${SHARE_NAME:-world-share}-state"`, `name: "world-share-state"`, 1),
		"порт зашит": strings.Replace(разведённый,
			`- "${SHARE_BIND:-0.0.0.0}:${SHARE_PORT:-8070}:8070"`, `- "0.0.0.0:8070:8070"`, 1),
		"порт зашит короткой записью": strings.Replace(разведённый,
			`- "${SHARE_BIND:-0.0.0.0}:${SHARE_PORT:-8070}:8070"`, `- "8070:8070"`, 1),
		"хостового порта нет — назначит докер": strings.Replace(разведённый,
			`- "${SHARE_BIND:-0.0.0.0}:${SHARE_PORT:-8070}:8070"`, `- "8070"`, 1),
		"публикации нет вовсе": strings.Replace(разведённый,
			"    ports:\n      - \"${SHARE_BIND:-0.0.0.0}:${SHARE_PORT:-8070}:8070\"\n", "", 1),
		"порт зашит длинной записью": strings.Replace(разведённый,
			`      - "${SHARE_BIND:-0.0.0.0}:${SHARE_PORT:-8070}:8070"`,
			"      - target: 8070\n        published: \"8070\"", 1),
	}
	for имя, текст := range cases {
		// Порча обязана НАЛОЖИТЬСЯ: `strings.Replace` без совпадения молча вернёт исходный
		// текст, и проба стерегла бы неиспорченный файл, ничего об этом не сказав.
		if текст == разведённый {
			t.Errorf("%s: порча не наложилась — поправь её, она портит несуществующую строку", имя)
			continue
		}
		found := Coexist([]byte(текст))
		if len(found) == 0 {
			t.Errorf("%s: сторож промолчал — «две раздачи на одной машине» закрыто на бумаге", имя)
			continue
		}
		for _, f := range found {
			if f.Why == "" || f.Way == "" {
				t.Errorf("%s: находка без причины или без выхода — это молчание с номером строки (`WORLD2` 2.3)", имя)
			}
		}
	}
}

// Законная длинная запись публикации краснеть НЕ должна: правило — «порт приходит
// снаружи», а не «пиши коротко».
func TestДлиннаяЗаписьПортаЗаконна(t *testing.T) {
	текст := strings.Replace(разведённый,
		`      - "${SHARE_BIND:-0.0.0.0}:${SHARE_PORT:-8070}:8070"`,
		"      - target: 8070\n        published: \"${SHARE_PORT:-8070}\"\n        protocol: tcp", 1)
	if found := Coexist([]byte(текст)); len(found) > 0 {
		t.Fatalf("длинная запись публикации покраснела зря: %v", found)
	}
}

// Список портов кончается там, где кончается его отступ. Без этого сторож принял бы за
// публикацию всё, что стоит ниже по файлу.
func TestСписокПортовКончаетсяПоОтступу(t *testing.T) {
	текст := strings.Replace(разведённый,
		"    volumes:\n      - state:/state\n", "    volumes:\n      - state:/state\n      - журнал:/log\n", 1)
	if found := Coexist([]byte(текст)); len(found) > 0 {
		t.Fatalf("строки ниже списка портов приняты за публикацию: %v", found)
	}
}

// Находка про файл целиком не смеет называть «строку 0»: человек пойдёт её искать.
func TestНаходкаБезСтрокиНеНазываетНоль(t *testing.T) {
	текст := strings.Replace(разведённый, `name: "${SHARE_NAME:-world-share}"`+"\n", "", 1)
	found := Coexist([]byte(текст))
	if len(found) != 1 {
		t.Fatalf("находок %d вместо одной: %v", len(found), found)
	}
	if strings.Contains(found[0].String(), "строка 0") {
		t.Fatalf("находка про файл целиком названа строкой: %s", found[0])
	}
	if !strings.Contains(found[0].String(), "выход:") {
		t.Fatalf("находка без выхода: %s", found[0])
	}
}

// Сторож формы записи и сторож сосуществования краснеют на РАЗНОМ. Зашитое имя — законный
// YAML: сторож формы обязан промолчать, иначе человек не поймёт, что именно он сломал.
func TestДваСторожаСтерегутРазное(t *testing.T) {
	зашито := strings.Replace(разведённый,
		`name: "${SHARE_NAME:-world-share}"`+"\n", "name: \"world-share\"\n", 1)
	if found := Check([]byte(зашито)); len(found) > 0 {
		t.Fatalf("сторож формы записи полез в сосуществование: %v", found)
	}
	if found := Coexist([]byte(зашито)); len(found) == 0 {
		t.Fatal("сторож сосуществования промолчал на зашитом имени")
	}

	неЧитается := "image: ${SHARE_IMAGE:-ghcr.io/omnifield/world-share:latest}\n"
	if found := Check([]byte(неЧитается)); len(found) == 0 {
		t.Fatal("сторож формы записи промолчал на незакавыченной подстановке")
	}
}
