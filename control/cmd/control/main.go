// Контроллер — руки юзера в мире (`WORLD2` 3.7).
//
// Мир сам не действует, поэтому всё, что происходит, происходит через контроллер: юзер
// входит в свой скоуп, добавляет ресурсы, на них встают двери. Ставится контроллер РУКАМИ
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
	"time"

	"github.com/omnifield/world/control/internal/api"
	"github.com/omnifield/world/control/internal/run"
)

const (
	defaultAddr = ":8090"
	// Путь к готовому подъёму двери. Умолчание — от корня репозитория, как его видит
	// запуск из `control/`; образ кладёт репозиторий в /opt/world и называет путь явно.
	defaultRemoteSh = "../deploy/remote.sh"
	// Хост-порт двери НА ТОМ ресурсе. Значение уезжает `deploy/remote.sh` его же
	// переменной: два разных умолчания одного и того же однажды разъедутся.
	defaultDoorPort = 8080
	// Сколько ждём внешний инструмент. Подъём двери на чистом ресурсе везёт туда образ —
	// это минуты, а не секунды.
	defaultToolTimeout = 300
	// Сколько ждём ответа ssh при работе со скоупом. Меньше — быстрее краснеет.
	defaultSSHTimeout = 10
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

	handler := api.New(api.Options{
		Runner:     run.Exec{Timeout: time.Duration(number("CONTROL_TOOL_TIMEOUT", defaultToolTimeout)) * time.Second},
		RemoteSh:   remoteSh,
		Docker:     env("CONTROL_DOCKER", "docker"),
		KeysDir:    keys,
		DoorPort:   number("CONTROL_DOOR_PORT", defaultDoorPort),
		SSHTimeout: number("CONTROL_SSH_TIMEOUT", defaultSSHTimeout),
	})

	log.Printf("control: руки юзера на %s", addr)
	log.Printf("control: подъём двери — %s; докер — %s; связка — %s",
		remoteSh, env("CONTROL_DOCKER", "docker"), keys)
	// Проверяем то, чем будем пользоваться, СРАЗУ и говорим вслух: инструмент, которого
	// нет, обнаружится иначе только в момент, когда человек уже нажал «добавить ресурс».
	if _, err := os.Stat(remoteSh); err != nil {
		log.Printf("control: ВНИМАНИЕ — подъёма двери по пути %s нет (%v); добавление ресурса откажет кодом no-remote-tool", remoteSh, err)
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
	fmt.Fprint(w, `Контроллер мира — руки юзера: вход в скоуп, источники ресурса, подъём дверей.

  control            поднять контроллер (по умолчанию :8090)
  control help       эта подсказка

Ручки (тело и ответы — в control/README.md):
  POST   /api/session          вход: адрес скоупа и креды; create=true — завести здесь
  GET    /api/me               кто я сейчас
  GET    /api/resources        источники ресурса: имя, адрес, жив ли
  POST   /api/resources        добавить ресурс — на нём встаёт дверь
  DELETE /api/resources/{имя}  снять ресурс; в ответе — что осталось на той машине
  GET    /api/fields           поля юзера
  POST   /api/fields           завести поле

Значения:
  CONTROL_ADDR=:8090                 где слушать
  CONTROL_KEYS=~/.ssh                связка контроллера: ключи ресурсов и config
  CONTROL_REMOTE_SH=../deploy/remote.sh   готовый подъём двери — его контроллер зовёт
  CONTROL_DOCKER=docker              чем говорим с докером
  CONTROL_DOOR_PORT=8080             хост-порт двери на добавляемом ресурсе
  CONTROL_TOOL_TIMEOUT=300           сколько секунд ждём внешний инструмент
  CONTROL_SSH_TIMEOUT=10             сколько секунд ждём ответа ssh при работе со скоупом

Отказ приходит тройкой: code (машине), why (причина человеку), ways[] (выходы).
Контроллеру нужен сокет докера — его даёт хозяин при подъёме (./control/up.sh).
`)
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
