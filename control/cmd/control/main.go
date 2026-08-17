// Контроллер — руки юзера в мире (`WORLD2` 3.7).
//
// Мир сам не действует, поэтому всё, что происходит, происходит через контроллер: юзер
// входит в свой скоуп, добавляет ресурсы, на них встают вещи. Ставится контроллер РУКАМИ
// человека — ни мир, ни другой контроллер его не разворачивают: цепочка начинается с
// юзера, и первое звено не автоматизируется.
//
// Здесь только процесс: настройки, маршруты, журнал. Смысл — в internal/.
//
//	go run ./cmd/control          поднять контроллер на :8090
//	go run ./cmd/control help     что умеет и какими значениями настраивается
//
// ПОЧЕМУ ПОРТ НЕ 8080. 8080 принадлежит ДВЕРИ и торчит наружу машины (`kb:FUND-5`).
// Контроллер — не дверь: он не вход в мир, а власть над машиной, и наружу ему торчать
// нечем и незачем. Разные порты здесь не косметика, а то же разделение, ради которого
// управление вообще вынесли из двери: кто вошёл в дверь, тот не получил ключи от дома.
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/omnifield/world/control/internal/api"
	"github.com/omnifield/world/control/internal/pult"
	"github.com/omnifield/world/control/internal/run"
)

const (
	defaultAddr = ":8090"
	// Путь к готовому подъёму вещи. Умолчание — от корня репозитория, как его видит
	// запуск из `control/`; образ кладёт репозиторий в /opt/world и называет путь явно.
	defaultRemoteSh = "../deploy/remote.sh"
	// Хост-порт двери НА ТОМ ресурсе. Значение уезжает `deploy/remote.sh` его же
	// переменной: два разных умолчания одного и того же однажды разъедутся. Рецепт,
	// который его не читает, о нём и не узнает.
	defaultDoorPort = 8080
	// Сколько ждём внешний инструмент. Подъём вещи на чистом ресурсе везёт туда образ —
	// это минуты, а не секунды.
	defaultToolTimeout = 300
	// Сколько ждём ответа РАЗДАЧИ СКОУПА. Это обычный HTTP-запрос до машины юзера: меньше
	// — быстрее краснеет, и «раздача молчит» человек узнаёт сразу, а не через минуту.
	defaultScopeTimeout = 10
	// Где лежит СОБРАННЫЙ пульт. Умолчание — результат сборки зоны `web`, как его видит
	// запуск из `control/`: разработчику `go run` плюс `pnpm dev` рядом должны работать
	// без единой настройки. Образ кладёт пульт в `/opt/world/pult` и называет путь явно —
	// умолчание относительное, а таких каталогов в образе нет.
	defaultPultDir = "../web/dist"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "help", "-h", "--help":
			usage(os.Stdout)
			return
		default:
			usage(os.Stderr)
			fmt.Fprintf(os.Stderr, "\nнепонятная команда: %s\n", os.Args[1])
			os.Exit(2)
		}
	}

	addr := env("CONTROL_ADDR", defaultAddr)
	keys := env("CONTROL_KEYS", filepath.Join(home(), ".ssh"))
	remoteSh := env("CONTROL_REMOTE_SH", defaultRemoteSh)
	pultDir := env("CONTROL_PULT", defaultPultDir)
	// Рецепт двери и каталог рецептов ВЫВОДЯТСЯ ИЗ ПУТИ К ПОДЪЁМУ, а не задаются вторым
	// умолчанием: подъём и файл запуска двери едут в образ вместе и лежат рядом (то же
	// правило и та же формула, что у точки входа образа — `control-entry.sh`). Второе
	// независимое умолчание того же самого разъехалось бы с первым молча.
	doorRecipe := env("CONTROL_DOOR_RECIPE", filepath.Join(filepath.Dir(remoteSh), "compose.yaml"))
	recipesDir := env("CONTROL_RECIPES", filepath.Join(filepath.Dir(remoteSh), "recipes"))
	// Рецепт РАЗДАЧИ СКОУПА выводится оттуда же и той же формулой: зона `share` лежит
	// рядом с зоной `deploy` и в репозитории, и в образе. Второе независимое умолчание
	// того же самого разъехалось бы с первым молча.
	shareRecipe := env("CONTROL_SHARE_RECIPE", filepath.Join(filepath.Dir(filepath.Dir(remoteSh)), "share", "compose.yaml"))

	handler := api.New(api.Options{
		Runner:       run.Exec{Timeout: time.Duration(number("CONTROL_TOOL_TIMEOUT", defaultToolTimeout)) * time.Second},
		RemoteSh:     remoteSh,
		RecipesDir:   recipesDir,
		DoorRecipe:   doorRecipe,
		ShareRecipe:  shareRecipe,
		Docker:       env("CONTROL_DOCKER", "docker"),
		KeysDir:      keys,
		DoorPort:     number("CONTROL_DOOR_PORT", defaultDoorPort),
		ScopeTimeout: number("CONTROL_SCOPE_TIMEOUT", defaultScopeTimeout),
		PultDir:      pultDir,
	})

	log.Printf("control: руки юзера на %s", addr)
	log.Printf("control: подъём вещи — %s; докер — %s; связка — %s",
		remoteSh, env("CONTROL_DOCKER", "docker"), keys)
	// Ландшафт рецептов называется В СТАРТОВОЙ СТРОКЕ: контроллер не знает, какие бывают
	// вещи, — он знает, где они лежат, и человеку это надо видеть до первого добавления
	// ресурса, а не по отказу `no-such-recipe`.
	log.Printf("control: рецепт двери — %s; каталог рецептов — %s (%s)",
		doorRecipe, recipesDir, recipesState(recipesDir))
	// Рецепт раздачи называется ТАМ ЖЕ и по той же причине: без него «завести скоуп»
	// откажет кодом `no-share-recipe`, и узнать об этом лучше при подъёме, а не в момент,
	// когда человек уже назвал имя, бренд и пароль.
	log.Printf("control: рецепт раздачи скоупа — %s (%s)", shareRecipe, fileState(shareRecipe))
	// Состояние пульта называется В СТАРТОВОЙ СТРОКЕ, а не только на первом запросе:
	// образ, уехавший без пульта, обнаруживается иначе тогда, когда человек уже открыл
	// адрес и увидел отказ. Поднятию это не мешает — ручки работают и без лица.
	log.Printf("control: пульт — %s", pult.New(pultDir).State())
	// Проверяем то, чем будем пользоваться, СРАЗУ и говорим вслух: инструмент, которого
	// нет, обнаружится иначе только в момент, когда человек уже нажал «добавить ресурс».
	if _, err := os.Stat(remoteSh); err != nil {
		log.Printf("control: ВНИМАНИЕ — подъёма вещи по пути %s нет (%v); добавление ресурса откажет кодом no-remote-tool", remoteSh, err)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("control: не поднялся: %v", err)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Контроллер мира — руки юзера: вход в скоуп, источники ресурса, подъём вещей.

  control            поднять контроллер (по умолчанию :8090)
  control help       эта подсказка

Ручки (тело и ответы — в control/README.md):
  POST   /api/scope            завести скоуп: машина (адрес, креды, имя участка) + скоуп
                               (адрес раздачи и пароль) + личность (имя, бренд)
  POST   /api/session          вход: АДРЕС скоупа и ПАРОЛЬ, и больше ничего
  DELETE /api/session          выход: времянки контроллера снимаются, скоуп не тронут
  GET    /api/me               кто я сейчас
  GET    /api/resources        территории юзера: имя, адрес, отвечает ли, что на ней стоит
  POST   /api/resources        завести территорию — на ней встаёт вещь по рецепту
  DELETE /api/resources/{имя}  снять территорию; в ответе — что осталось на той машине
  GET    /api/recipes          чем контроллер умеет поднимать: каталог рецептов
  GET    /api/fields           поля юзера
  POST   /api/fields           завести поле
  GET    /                     пульт — лицо для человека, собранное зоной web

СКОУП ЛЕЖИТ ПО АДРЕСУ, А НЕ ЗДЕСЬ. Личность юзера раздаётся раздачей на его машине, и
контроллер до неё дотягивается: адрес и пароль называет юзер. Хода «завести здесь, ничего
не спрашивая» НЕ СУЩЕСТВУЕТ (WORLD2 3.7) — контроллер это времянка, и держателем чужой
личности он быть не должен. Снёс контроллер, поднял на другой машине, вошёл тем же адресом
— всё на месте.

ЧТО ПОДНИМАЕТСЯ — говорит РЕЦЕПТ (файл запуска), а не контроллер: перечня вещей в нём нет
вовсе. Не назвал рецепт — ставится дверь; назвал — то, что назвал. Свои вещи хозяин машины
кладёт в каталог рецептов, и для этого не нужны ни правка кода, ни пересборка образа.

Значения:
  CONTROL_ADDR=:8090                 где слушать
  CONTROL_KEYS=~/.ssh                связка контроллера: ключи территорий и config.
                                     ВРЕМЯНКА: раскладывается из скоупа при входе
  CONTROL_REMOTE_SH=../deploy/remote.sh   готовый подъём вещи — его контроллер зовёт
  CONTROL_RECIPES=<рядом с подъёмом>/recipes  каталог рецептов (в образе /opt/world/recipes)
  CONTROL_DOOR_RECIPE=<рядом с подъёмом>/compose.yaml   рецепт двери — умолчание подъёма
  CONTROL_SHARE_RECIPE=<рядом с зоной deploy>/share/compose.yaml  рецепт раздачи скоупа
  CONTROL_PULT=../web/dist           где лежит СОБРАННЫЙ пульт (в образе /opt/world/pult)
  CONTROL_DOCKER=docker              чем говорим с докером
  CONTROL_DOOR_PORT=8080             хост-порт двери на добавляемой территории
  CONTROL_TOOL_TIMEOUT=300           сколько секунд ждём внешний инструмент
  CONTROL_SCOPE_TIMEOUT=10           сколько секунд ждём ответа раздачи скоупа

Отказ приходит тройкой: code (машине), why (причина человеку), ways[] (выходы).
Контроллеру нужен сокет докера — его даёт хозяин при подъёме (./control/up.sh).
`)
}

// recipesState — что в каталоге рецептов сейчас лежит, одной строкой. Пустой каталог и
// каталога нет — РАЗНЫЕ состояния: первое чинит хозяин, положив файл, второе — раскладка
// подъёма. Сказать «пусто» про обоих значило бы отправить чинить не то.
func recipesState(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "каталога нет — вещей, кроме двери, контроллер не поднимет; смонтируй свой: control/README.md"
		}
		return "не прочитать: " + err.Error()
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".yaml", ".yml":
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "пуст — кроме двери поднимать нечего"
	}
	return "рецептов: " + strings.Join(names, ", ")
}

// fileState — есть ли файл, названный при подъёме. «Нет» и «не прочитать» — разные
// состояния, и чинят их разные люди: первое — раскладка образа, второе — права.
func fileState(path string) string {
	info, err := os.Stat(path)
	switch {
	case err == nil && info.Mode().IsRegular():
		return "на месте"
	case os.IsNotExist(err):
		return "файла нет — «завести скоуп» откажет кодом no-share-recipe; назови свой: CONTROL_SHARE_RECIPE"
	default:
		return "не прочитать: " + err.Error()
	}
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func number(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		// Кривое значение не подменяем умолчанием молча: человек его назвал и будет
		// думать, что оно применилось.
		log.Fatalf("control: %s=%q — это не число секунд", name, v)
	}
	return n
}

func home() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "/root"
}
