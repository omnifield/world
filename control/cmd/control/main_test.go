package main

import (
	"os"
	"path/filepath"
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
		"POST   /api/scope", "POST   /api/session", "DELETE /api/session", "GET    /api/me",
		"GET    /api/resources", "POST   /api/resources", "DELETE /api/resources/{имя}",
		"GET    /api/recipes",
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
		"CONTROL_DOCKER", "CONTROL_DOOR_PORT", "CONTROL_TOOL_TIMEOUT", "CONTROL_SCOPE_TIMEOUT",
		"CONTROL_PULT", "CONTROL_RECIPES", "CONTROL_DOOR_RECIPE", "CONTROL_SHARE_RECIPE",
		// Не наши, но контроллер их читает: штамп своей сборки и явное имя от хозяина
		// (`WORLD2-130`). Неназванная настройка равна отсутствующей — и эти две тем более:
		// человек, не знающий про пин, будет искать, почему поднялось не то.
		"WORLD_VERSION", "WORLD_IMAGE", "SHARE_IMAGE",
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
	t.Setenv("CONTROL_SCOPE_TIMEOUT", "30")
	if got := number("CONTROL_SCOPE_TIMEOUT", 10); got != 30 {
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
	// Каталог рецептов — то место, куда хозяин машины кладёт свои вещи. Разъедься путь с
	// образом, положенный рецепт просто не нашёлся бы, и отказ был бы про имя, а не про
	// раскладку.
	if !strings.Contains(string(data), "CONTROL_RECIPES: /opt/world/recipes") {
		t.Fatal("каталог рецептов в файле запуска разъехался с тем, что заводит образ")
	}
}

// Рецепт двери и каталог рецептов ВЫВОДЯТСЯ из пути к подъёму: файл запуска двери едет в
// образ рядом с ним. Второе независимое умолчание того же самого разъехалось бы молча —
// ровно так же, как разъезжалось имя образа двери (`WORLD2-121`).
func TestРецептыВыводятсяИзПутиКПодъёму(t *testing.T) {
	dir := filepath.Dir(defaultRemoteSh)
	if got := filepath.Join(dir, "compose.yaml"); got != "../deploy/compose.yaml" {
		t.Fatalf("рецепт двери выводится не туда: %q", got)
	}
	if got := filepath.Join(dir, "recipes"); got != "../deploy/recipes" {
		t.Fatalf("каталог рецептов выводится не туда: %q", got)
	}
	// Рецепт раздачи скоупа выводится той же формулой, но на ярус выше: зона `share` лежит
	// рядом с зоной `deploy` — и в репозитории, и в образе.
	if got := filepath.Join(filepath.Dir(dir), "share", "compose.yaml"); got != "../share/compose.yaml" {
		t.Fatalf("рецепт раздачи скоупа выводится не туда: %q", got)
	}
}

// Личность юзера больше НЕ ЛЕЖИТ НА КОНТРОЛЛЕРЕ (`WORLD2-124`): том под скоупы ушёл из
// раскладки вместе с путём. Останься он в файле запуска — человек снова решил бы, что
// снос контроллера уносит личность, и раскладка стала бы врать про устройство.
func TestТомаПодСкоупыВФайлеЗапускаНет(t *testing.T) {
	data, err := os.ReadFile("../../compose.yaml")
	if err != nil {
		t.Skipf("файла запуска рядом нет: %v", err)
	}
	for _, gone := range []string{"world-control-scope", "CONTROL_SCOPE_DIR", ":/scope"} {
		if strings.Contains(string(data), gone) {
			t.Fatalf("в файле запуска остался след скоупа на контроллере: %q — скоуп лежит по адресу, а не здесь", gone)
		}
	}
}
