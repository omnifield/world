package main

import (
	"os"
	"strings"
	"testing"
)

// Подсказка процесса — это первая дока, которую человек видит, и рассыхается она тише
// всего: ручку добавили, строчку не дописали. Проверяем, что она называет ВСЕ ручки и все
// значения, которыми процесс настраивается.

func TestПодсказкаНазываетВсеРучки(t *testing.T) {
	var sb strings.Builder
	usage(&sb)
	for _, path := range []string{
		"POST   /api/session", "GET    /api/me",
		"GET    /api/resources", "POST   /api/resources", "DELETE /api/resources/{имя}",
		"GET    /api/fields", "POST   /api/fields",
		"GET    /                     пульт",
	} {
		if !strings.Contains(sb.String(), path) {
			t.Fatalf("в подсказке нет ручки %q — человек о ней не узнает", path)
		}
	}
}

func TestПодсказкаНазываетВсеЗначения(t *testing.T) {
	var sb strings.Builder
	usage(&sb)
	for _, name := range []string{
		"CONTROL_ADDR", "CONTROL_KEYS", "CONTROL_REMOTE_SH",
		"CONTROL_DOCKER", "CONTROL_DOOR_PORT", "CONTROL_TOOL_TIMEOUT", "CONTROL_SSH_TIMEOUT",
		"CONTROL_PULT",
	} {
		if !strings.Contains(sb.String(), name) {
			t.Fatalf("в подсказке нет значения %s — неназванная настройка равна отсутствующей", name)
		}
	}
}

func TestЗначенияБерутсяИзОкруженияИначеУмолчание(t *testing.T) {
	if got := env("CONTROL_ЧЕГО-ТО-НЕТ", ":8090"); got != ":8090" {
		t.Fatalf("умолчание не сработало: %q", got)
	}
	t.Setenv("CONTROL_ADDR", "127.0.0.1:9999")
	if got := env("CONTROL_ADDR", ":8090"); got != "127.0.0.1:9999" {
		t.Fatalf("названное человеком значение не применилось: %q", got)
	}

	if got := number("CONTROL_НЕТ", 10); got != 10 {
		t.Fatalf("умолчание числа не сработало: %d", got)
	}
	t.Setenv("CONTROL_SSH_TIMEOUT", "30")
	if got := number("CONTROL_SSH_TIMEOUT", 10); got != 30 {
		t.Fatalf("названное число не применилось: %d", got)
	}
}

// Умолчания портов и путей повторены в файле запуска (`control/compose.yaml`) и в образе.
// Разъедутся — контроллер будет слушать не там, куда стучится проба и человек.
func TestУмолчанияСовпадаютСФайломЗапуска(t *testing.T) {
	data, err := os.ReadFile("../../compose.yaml")
	if err != nil {
		t.Skipf("файла запуска рядом нет: %v", err)
	}
	if !strings.Contains(string(data), `CONTROL_ADDR: ":8090"`) {
		t.Fatalf("порт в файле запуска разъехался с умолчанием %s", defaultAddr)
	}
	if !strings.Contains(string(data), "CONTROL_REMOTE_SH: /opt/world/deploy/remote.sh") {
		t.Fatal("путь к подъёму двери в файле запуска разъехался с тем, что кладёт образ")
	}
	if !strings.Contains(string(data), "CONTROL_PULT: /opt/world/pult") {
		t.Fatal("путь к пульту в файле запуска разъехался с тем, куда его кладёт образ")
	}
}
