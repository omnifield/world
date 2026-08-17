// Сторож формы записи файла запуска — как команда.
//
//	go run ./cmd/recipecheck ../share/compose.yaml    прочитать и сказать, пройдёт ли компоузом
//
// ЗАЧЕМ ОТДЕЛЬНАЯ КОМАНДА, если то же самое стережёт `go test ./...`. Затем, что пробе
// зоны нужен ВИДИМЫЙ шаг и нужна нарочная порча: проба берёт файл запуска, портит его
// копию и требует, чтобы сторож покраснел. Через `go test` порчу не прогнать, не правя
// исходники, — а проба, которая правит исходники, врёт про то, что проверяет.
//
// В ОБРАЗ ЭТА КОМАНДА НЕ ЕДЕТ: `share/Dockerfile` собирает только `./cmd/share`. Сторож —
// инструмент разработчика зоны, а не часть поставки, которая держит личность юзера.
//
//	код 0   файл чист по тому классу, который сторож стережёт
//	код 1   нашлось: причина и выход названы по каждой находке
//	код 2   файл не назван или не прочитался
package main

import (
	"fmt"
	"os"

	"github.com/omnifield/world/share/internal/recipe"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, `не назван файл запуска.

  recipecheck <файл.yaml> [ещё файлы]

Сторож НЕ заменяет `+"`docker compose config`"+`: полный разбор делает компоуз. Здесь ловится
класс поломок формы записи — тот, что уже стоил выпуска (`+"`WORLD2-140`"+`), и ловится он без
докера, а значит всегда.
`)
		os.Exit(2)
	}

	bad := false
	for _, path := range os.Args[1:] {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "не прочитать %s: %v\n", path, err)
			os.Exit(2)
		}
		found := recipe.Check(data)
		if len(found) == 0 {
			fmt.Printf("%s — чист\n", path)
			continue
		}
		bad = true
		fmt.Fprintf(os.Stderr, "%s — не пройдёт компоузом, находок %d:\n", path, len(found))
		for _, f := range found {
			fmt.Fprintf(os.Stderr, "  %v\n", f)
		}
	}
	if bad {
		os.Exit(1)
	}
}
