package recipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const дверь = "/opt/world/deploy/compose.yaml"

func каталог(t *testing.T) *Catalog {
	t.Helper()
	return &Catalog{Dir: t.TempDir(), Door: дверь}
}

func положить(t *testing.T, c *Catalog, name, body string) string {
	t.Helper()
	path := filepath.Join(c.Dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ГЛАВНОЕ свойство ландшафта: положили файл — вещь появилась. Перечня вещей в коде нет, и
// заводить его нельзя: он и есть та зашитость, из-за которой вторая вещь требовала правки
// (`WORLD2` 3.7, `WORLD2-131`).
func TestСписокЧитаетсяИзКаталога(t *testing.T) {
	c := каталог(t)

	list, ref := c.List()
	if ref != nil {
		t.Fatalf("список не собрался: %v", ref)
	}
	if len(list) != 1 || list[0].Name != DoorName || list[0].Path != дверь {
		t.Fatalf("в пустом ландшафте обязана быть одна дверь: %+v", list)
	}

	весы := положить(t, c, "весы.yaml", "name: весы\n")
	list, _ = c.List()
	if len(list) != 2 || list[1].Name != "весы" || list[1].Path != весы {
		t.Fatalf("положенный рецепт не появился в списке: %+v", list)
	}
	if list[0].Name != DoorName {
		t.Fatalf("дверь обязана идти первой — она умолчание: %+v", list)
	}
}

// Каталога нет — это ландшафт без вещей, а не поломка: отказывать человеку за то, что он
// ничего не положил, не за что.
func TestКаталогаНетЭтоНеПоломка(t *testing.T) {
	c := &Catalog{Dir: filepath.Join(t.TempDir(), "каталога-нет"), Door: дверь}
	list, ref := c.List()
	if ref != nil {
		t.Fatalf("отсутствие каталога названо поломкой: %v", ref)
	}
	if len(list) != 1 || list[0].Name != DoorName {
		t.Fatalf("дверь потерялась вместе с каталогом: %+v", list)
	}
}

// Не всё, что лежит в каталоге, — рецепт. Выдать чужой файл за вещь значило бы обещать
// подъём, которого не будет.
func TestНеРецептыВСписокНеПопадают(t *testing.T) {
	c := каталог(t)
	положить(t, c, "заметка.txt", "не рецепт")
	положить(t, c, "весы.yml", "name: весы\n")
	if err := os.Mkdir(filepath.Join(c.Dir, "каталог-внутри"), 0o755); err != nil {
		t.Fatal(err)
	}

	list, _ := c.List()
	if len(list) != 2 || list[1].Name != "весы" {
		t.Fatalf("в список попало лишнее: %+v", list)
	}
}

func TestИмяРецептаПревращаетсяВПуть(t *testing.T) {
	c := каталог(t)
	весы := положить(t, c, "весы.yaml", "name: весы\n")

	cases := []struct{ имя, путь string }{
		{"", дверь},
		{DoorName, дверь},
		{"весы", весы},
		// Косая черта — это ПУТЬ, и он уезжает подъёму как есть: годность файла проверяет
		// сосед и отвечает своей тройкой отказов.
		{"/чужой/путь/весы.yaml", "/чужой/путь/весы.yaml"},
	}
	for _, c2 := range cases {
		got, ref := c.Find(c2.имя)
		if ref != nil {
			t.Fatalf("%q → отказ %v", c2.имя, ref)
		}
		if got != c2.путь {
			t.Fatalf("%q → %q, а ждали %q", c2.имя, got, c2.путь)
		}
	}
}

// Отказ обязан назвать, что есть на самом деле: «такого рецепта нет» без списка — тупик.
func TestНеизвестноеИмяОтказываетСВыходом(t *testing.T) {
	c := каталог(t)
	положить(t, c, "весы.yaml", "name: весы\n")

	_, ref := c.Find("часы")
	if ref == nil || ref.Code != "no-such-recipe" {
		t.Fatalf("ждали no-such-recipe, получили %v", ref)
	}
	if len(ref.Ways) == 0 {
		t.Fatal("отказ без выхода — тупик (WORLD2 2.3)")
	}
	var назвал bool
	for _, w := range ref.Ways {
		if strings.Contains(w, "весы") {
			назвал = true
		}
	}
	if !назвал {
		t.Fatalf("в выходах не названо, какие рецепты есть: %v", ref.Ways)
	}
}

// Имя двери занято: файл `door.yaml` не подменяет её молча — иначе «дверь» означала бы то
// одно, то другое, и человек поднял бы не то, что назвал.
func TestИмяДвериНеПодменяется(t *testing.T) {
	c := каталог(t)
	положить(t, c, DoorName+".yaml", "name: чужое\n")

	list, _ := c.List()
	if len(list) != 1 || list[0].Path != дверь {
		t.Fatalf("дверь подменилась файлом из каталога: %+v", list)
	}
	got, ref := c.Find(DoorName)
	if ref != nil || got != дверь {
		t.Fatalf("«дверь» перестала быть дверью: %q %v", got, ref)
	}
}

// Рецепта двери нет вовсе (дефект образа) — это отказ СВОИМ кодом, а не молчаливый вызов
// подъёма без рецепта: тот поднял бы вещь по своему умолчанию, и человек не узнал бы, что
// поднялось не то.
func TestБезРецептаДвериОтказСвоимКодом(t *testing.T) {
	c := &Catalog{Dir: t.TempDir()}
	_, ref := c.Find("")
	if ref == nil || ref.Code != "no-door-recipe" {
		t.Fatalf("ждали no-door-recipe, получили %v", ref)
	}
}
