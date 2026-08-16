// Пакет recipe — рецепты: то, ЧЕМ на ресурсе поднимается вещь.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ СПИСОК ВЕЩЕЙ МИРА НЕ ЯВЛЯЕТСЯ МЕХАНИКОЙ (`WORLD2` 3.7). Контроллер не знает, какие    │
// │ бывают вещи: он знает, где лежат рецепты, и читает каталог. Положили файл — вещь      │
// │ появилась; ни строки здесь править не надо и образ пересобирать не надо.             │
// │                                                                                      │
// │ Поэтому перечня вещей в коде нет и не заводится. Появится — значит вещь снова зашита. │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// Что такое рецепт, решает не эта зона: рецепт — файл запуска (compose), и всё про вещь
// (имя, образ, контейнеры, порты, тома) читает из него сам подъём (`deploy/remote.sh`,
// `WORLD2-131`). Здесь только ландшафт: где рецепты лежат и как называется тот, который
// человек назвал.
//
// ДВА ИСТОЧНИКА, и оба — ландшафт, а не реестр:
//
//   - каталог рецептов (`CONTROL_RECIPES`). В образе он пуст, и это место, куда хозяин
//     машины кладёт свои вещи — монтированием, а не пересборкой образа;
//   - путь, названный юзером в запросе. Косая черта в имени и означает «это путь»:
//     контроллер и так стоит на сокете докера, и прятать от его хозяина файловую систему
//     той же машины было бы театром, а не границей.
//
// Дверь — ОДИН ИЗ РЕЦЕПТОВ и лежит рядом с подъёмом, приехав тем же образом. В каталоге
// её нет намеренно: второй копией того же файла она разъехалась бы с первой молча.
package recipe

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/omnifield/world/control/internal/refusal"
)

// DoorName — имя, под которым в списке стоит дверь. Занятое: файл каталога с этим именем
// не поднимается, иначе «дверь» означала бы то одно, то другое.
const DoorName = "door"

// Catalog — где контроллер берёт рецепты. Оба поля — ЗНАЧЕНИЯ, а не константы: в образе
// они свои, в девбоксе свои, а проба кладёт свой каталог и проверяет ПОВЕДЕНИЕ там, где
// ни докера, ни второй вещи нет.
type Catalog struct {
	// Dir — каталог рецептов. Нет каталога — ландшафт пуст, и это законно: отказывать
	// человеку за то, что он не положил вещей, не за что.
	Dir string
	// Door — рецепт двери: файл запуска, приехавший в образ вместе с подъёмом.
	Door string
}

// Recipe — рецепт глазами человека. Ни имени вещи, ни образа здесь нет намеренно: их
// знает сам файл, и спрашивать их полагается у компоуза, а не разбирать YAML своими
// глазами (`deploy/remote.sh`, read_recipe). Разъехавшийся разбор хуже отсутствующего —
// он выглядит знанием (`WORLD2` 4.2).
type Recipe struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// From — откуда рецепт взялся: словами, потому что чинятся эти два места по-разному.
	From string `json:"from"`
}

// расширения рецептов. Компоуз читает `.yaml` и `.yml`; всё прочее в каталоге — не рецепт,
// и выдавать его за рецепт значило бы обещать вещь, которой нет.
func isRecipe(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yaml" || ext == ".yml"
}

// List — что контроллер умеет поднимать ПРЯМО СЕЙЧАС. Дверь первой: она и есть умолчание.
func (c *Catalog) List() ([]Recipe, *refusal.Refusal) {
	var out []Recipe
	if c.Door != "" {
		out = append(out, Recipe{Name: DoorName, Path: c.Door, From: "дверь мира — приехала в образе рядом с подъёмом"})
	}
	if c.Dir == "" {
		return out, nil
	}

	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Каталога нет — вещей не положили. Это состояние, а не поломка.
			return out, nil
		}
		return nil, refusal.New(http.StatusInternalServerError, "no-recipes-dir",
			fmt.Sprintf("каталог рецептов %s не прочитать: %v", c.Dir, err),
			"проверь, что каталог отдан контроллеру при подъёме: control/README.md, CONTROL_RECIPES")
	}

	var found []Recipe
	for _, e := range entries {
		if e.IsDir() || !isRecipe(e.Name()) {
			continue
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if name == DoorName && c.Door != "" {
			// Имя занято дверью. Молча подменить её чужим файлом нельзя: человек назвал бы
			// дверь, а поднялось бы другое.
			continue
		}
		found = append(found, Recipe{
			Name: name,
			Path: filepath.Join(c.Dir, e.Name()),
			From: "каталог рецептов " + c.Dir,
		})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	return append(out, found...), nil
}

// Find — путь рецепта, названного человеком. Пусто означает «дверь»: прежний путь («поставь
// дверь на ресурс») остаётся запросом без единого лишнего поля.
//
// Косая черта в названном — это ПУТЬ, и он уезжает подъёму как есть. Своей проверки «есть
// ли такой файл» здесь нет намеренно: она уже написана у соседа и отвечает тройкой
// (`no-recipe` · `bad-recipe` · `recipe-no-image`), а вторая такая же разъехалась бы с ней
// на первой правке.
func (c *Catalog) Find(name string) (string, *refusal.Refusal) {
	name = strings.TrimSpace(name)
	if name == "" || name == DoorName {
		if c.Door == "" {
			return "", refusal.New(http.StatusInternalServerError, "no-door-recipe",
				"рецепта двери в образе контроллера нет — поднимать нечем",
				"это дефект образа: файл запуска двери едет рядом с подъёмом, см. control/README.md",
				"назови другой рецепт: он лежит в каталоге рецептов (GET /api/recipes)")
		}
		return c.Door, nil
	}
	if strings.ContainsAny(name, "/\\") {
		return name, nil
	}

	list, ref := c.List()
	if ref != nil {
		return "", ref
	}
	for _, r := range list {
		if r.Name == name {
			return r.Path, nil
		}
	}

	var have []string
	for _, r := range list {
		have = append(have, r.Name)
	}
	return "", refusal.New(http.StatusNotFound, "no-such-recipe",
		fmt.Sprintf("рецепта «%s» контроллер не знает — поднимать нечего", name),
		"какие есть: GET /api/recipes"+listing(have),
		"положи свой в каталог рецептов — он монтируется хозяином машины: control/README.md, CONTROL_RECIPES",
		"либо назови путь целиком: в названии с косой чертой контроллер видит путь, а не имя")
}

func listing(names []string) string {
	if len(names) == 0 {
		return " (сейчас там пусто)"
	}
	return " (сейчас там: " + strings.Join(names, ", ") + ")"
}
