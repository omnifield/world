// Подкоманды бинаря: ОДИН бинарь — два режима (`kb:WORLD-53`).
//
//	без подкоманды (и `world door`) — ДВЕРЬ: мир, реестр локаций, пульт;
//	world guard                     — СТОРОЖ: локация говорит «я здесь»;
//	world join · leave · who        — вход в поле, выход и список поля.
//
// Почему без подкоманды остаётся дверь: образ мира зовёт бинарь без аргументов
// (`ENTRYPOINT ["/app/world"]`), и менять это значит ломать уже собранный образ
// ради красоты раскладки. Явный `world door` добавлен рядом — чтобы файл
// запуска мог назвать режим вслух, а не полагаться на пустоту.
//
// Настройка команд — ПЕРЕМЕННЫЕ СРЕДЫ, флаг перекрывает переменную. Причина в
// том, кто ими пользуется: локацию поднимает докер по конфигу, а переменные —
// его родной способ настройки (образ мира уже так настроен, `deploy/compose.yaml`);
// человек же с консоли уточняет одно значение, и ради этого экспортировать
// переменную неудобно. Обе стороны получают своё, третьего способа не заводим.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/omnifield/world/core/internal/door"
	"github.com/omnifield/world/core/internal/field"
	"github.com/omnifield/world/core/internal/guard"
)

// streams — куда уходит РЕЗУЛЬТАТ и куда ДИАГНОСТИКА. Разведены не для
// красоты: результат читают глазами и скриптом (`world who | …`), а
// трассировку и отказы — только глазами, и смешивать их в одном потоке значит
// испортить оба.
type streams struct {
	out io.Writer
	err io.Writer
}

func (s streams) trace(format string, a ...any) {
	fmt.Fprintf(s.err, format+"\n", a...)
}

// runCommand выполняет подкоманду и отдаёт код возврата процесса.
// Отказ уходит в stderr одной строкой: причина и выход в ней уже есть
// (`kb:WORLD-31`), и дополнять её нечем.
func runCommand(args []string, s streams) int {
	cmd, rest := args[0], args[1:]

	var err error
	switch cmd {
	case "door":
		// У двери своих флагов нет — она настраивается переменными среды.
		// Молча проглотить лишнее слово значит поднять дверь не так, как
		// человек просил, и не сказать ему об этом.
		if len(rest) > 0 {
			fmt.Fprintf(s.err, "world door: лишнее слово %q: дверь настраивается переменными среды "+
				"(WORLD_ADDR · WORLD_WEB_DIR · WORLD_DOOR_FILE · WORLD_STAND_DIR), флагов у неё нет — см. core/README.md\n", rest[0])
			return 1
		}
		runDoor()
		return 0
	case "guard":
		err = cmdGuard(s, rest)
	case "join":
		err = cmdJoin(s, rest)
	case "leave":
		err = cmdLeave(s, rest)
	case "who":
		err = cmdWho(s, rest)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(s.out, usage)
		return 0
	default:
		err = fmt.Errorf("неизвестная подкоманда %q — бинарь мира знает: door · guard · join · leave · who; "+
			"без подкоманды он поднимает дверь. Что делает каждая: world help", cmd)
	}

	// Подсказку попросили — её показали, и делать больше нечего. Это не отказ:
	// печатать после неё «world join: подсказка показана» значит выдавать
	// выполненную просьбу за ошибку.
	if errors.Is(err, errHelpShown) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(s.err, "world %s: %v\n", cmd, err)
		return 1
	}
	return 0
}

// errHelpShown — `-h` у подкоманды: флаги напечатаны, команду выполнять НЕ надо.
// Без этого `world join -h` показал бы подсказку и следом полез в поле — то
// есть сделал бы ровно то, от чего человек хотел сначала прочитать.
var errHelpShown = errors.New("подсказка показана")

const usage = `world — бинарь мира: дверь поля и сторож локации в одном изделии.

Режимы:
  world                     без подкоманды — ДВЕРЬ: реестр локаций, маршруты
                            к ним, пульт мира (запуск как был, ничего не менялось)
  world door                та же дверь, названная вслух
  world guard               сторож локации: отвечает «я здесь» и больше ничего

Команды входа в поле:
  world join                проверить себя, войти в поле, получить маршрут
  world leave               сняться с поля
  world who                 кто сейчас в поле

Настройка — переменные среды; флаг перекрывает переменную:
  WORLD_ADDR        :8080       что слушает дверь либо сторож          (-listen)
  WORLD_DOOR        door:8080   адрес двери в общей сети «имя:порт»    (-door)
  WORLD_NAME                    имя локации в поле = её маршрут        (-name)
  WORLD_SELF_ADDR               свой адрес «имя:порт», каким видит дверь (-addr)
  WORLD_GIVES                   что локация даёт, одной строкой        (-gives)

Умолчание WORLD_SELF_ADDR выводится из имени и порта прослушивания
(«имя:порт»): имя локации — это и её имя в общей сети. Разошлось с
действительностью — дверь ответит addr-unreachable, и адрес задаётся явно.

Переменные режима двери (WORLD_WEB_DIR, WORLD_DOOR_FILE, WORLD_STAND_DIR)
и всё остальное — core/README.md.
`

// ── команды ──────────────────────────────────────────────────────────────────

func cmdGuard(s streams, args []string) error {
	fs := flagSet(s, "guard")
	listen := fs.String("listen", envOr("WORLD_ADDR", defaultAddr), "что слушать (WORLD_ADDR)")
	name := fs.String("name", os.Getenv("WORLD_NAME"), "имя локации — только для ответа человеку (WORLD_NAME)")
	gives := fs.String("gives", os.Getenv("WORLD_GIVES"), "что локация даёт, одной строкой (WORLD_GIVES)")
	if err := parse(fs, args); err != nil {
		return err
	}

	// Имя сторожу НЕ обязательно, и это решение: сторож отвечает «место
	// существует», а тождество места — отдельная задача (`tasker:WORLD-81`).
	// Требовать здесь имя значило бы опережать её.
	кто := *name
	if кто == "" {
		кто = "без имени"
	}
	log.Printf("world: сторож локации (%s) слушает %s; проба жизни GET /healthz, вход в поле — world join", кто, *listen)
	return guard.Run(*listen, guard.New(*name, *gives, log.Printf, time.Now))
}

func cmdJoin(s streams, args []string) error {
	fs := flagSet(s, "join")
	doorAddr := fs.String("door", envOr("WORLD_DOOR", field.DefaultDoor), "адрес двери «имя:порт» (WORLD_DOOR)")
	name := fs.String("name", os.Getenv("WORLD_NAME"), "имя локации в поле (WORLD_NAME)")
	addr := fs.String("addr", os.Getenv("WORLD_SELF_ADDR"), "свой адрес «имя:порт» в общей сети (WORLD_SELF_ADDR)")
	gives := fs.String("gives", os.Getenv("WORLD_GIVES"), "что локация даёт (WORLD_GIVES)")
	listen := fs.String("listen", envOr("WORLD_ADDR", defaultAddr), "что слушает сторож — из него выводится порт своего адреса (WORLD_ADDR)")
	if err := parse(fs, args); err != nil {
		return err
	}

	объявление, err := announce(*name, *addr, *gives, *listen)
	if err != nil {
		return err
	}

	client := field.New(*doorAddr, nil, s.trace, time.Now)
	вошли, err := client.Join(объявление)
	if err != nil {
		return err
	}

	// Исходов ТРИ, и человек обязан видеть, какой у него (`tasker:WORLD-81`):
	//   вошла      — места с таким именем в поле не было;
	//   подтвердила — тот же адрес: локация переобъявляется на каждом подъёме, и
	//                 требовать снятия перед каждым значило бы вернуть тот самый
	//                 ручной шаг, который здесь и убран;
	//   вернулась   — тот же адрес не тот же, а место ТО ЖЕ: пересоздали
	//                 контейнер. Напечатать это как «вошла» значит соврать
	//                 ровно в том месте, ради которого всё и делалось.
	событие := "вошла в поле"
	switch вошли.Outcome {
	case door.OutcomeConfirmed:
		событие = "подтвердила присутствие в поле"
	case door.OutcomeReturned:
		событие = "ВЕРНУЛАСЬ в поле по новому адресу — это то же место, а не второе"
	}
	fmt.Fprintf(s.out, "локация %q %s\n", вошли.Location.Name, событие)
	строки := [][2]string{
		{"дверь", *doorAddr},
		{"адрес", вошли.Location.Addr},
		{"маршрут", вошли.Route},
		{"что даёт", вошли.Location.Gives},
		{"в поле с", вошли.Location.Since},
	}
	if вошли.Location.Returned != "" {
		строки = append(строки, [2]string{"вернулась", вошли.Location.Returned})
	}
	таблица(s.out, строки)
	return nil
}

func cmdLeave(s streams, args []string) error {
	fs := flagSet(s, "leave")
	doorAddr := fs.String("door", envOr("WORLD_DOOR", field.DefaultDoor), "адрес двери «имя:порт» (WORLD_DOOR)")
	name := fs.String("name", os.Getenv("WORLD_NAME"), "имя локации в поле (WORLD_NAME)")
	if err := parse(fs, args); err != nil {
		return err
	}

	client := field.New(*doorAddr, nil, s.trace, time.Now)
	if err := client.Leave(*name); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "локация %q снята с поля — маршрут /%s/ ушёл вместе с ней (дверь %s)\n", *name, *name, *doorAddr)
	return nil
}

func cmdWho(s streams, args []string) error {
	fs := flagSet(s, "who")
	doorAddr := fs.String("door", envOr("WORLD_DOOR", field.DefaultDoor), "адрес двери «имя:порт» (WORLD_DOOR)")
	if err := parse(fs, args); err != nil {
		return err
	}

	client := field.New(*doorAddr, nil, s.trace, time.Now)
	локации, err := client.Who()
	if err != nil {
		return err
	}

	// Пустое поле — рабочее состояние, а не сбой: дверь стоит до первой локации
	// (`kb:FUND-5`). Поэтому здесь не «ничего не найдено», а сказанное вслух
	// состояние и следующее действие.
	if len(локации) == 0 {
		fmt.Fprintf(s.out, "в поле пусто (дверь %s) — ни одной локации; локация входит сама: world join\n", *doorAddr)
		return nil
	}

	fmt.Fprintf(s.out, "в поле %d (дверь %s):\n", len(локации), *doorAddr)
	w := tabwriter.NewWriter(s.out, 0, 0, 2, ' ', 0)
	for _, l := range локации {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", l.Name, l.Route(), l.Addr, l.Gives, история(l))
	}
	return w.Flush()
}

// история — что реестр ПОМНИТ о месте: с какого момента оно в поле и когда
// возвращалось. Столбец не украшение: «то же место после пересоздания» видно
// только по нему, а без него список снова отвечает лишь на «кто сейчас».
//
// Времени может не быть вовсе — реестр правят и руками. Тогда так и сказано:
// мир не притворяется, что знает (`kb:WORLD-32`).
func история(l door.Location) string {
	когда := "в поле с " + l.Since
	if l.Since == "" {
		когда = "в поле с неизвестных пор"
	}
	if l.Returned != "" {
		когда += " · вернулась " + l.Returned
	}
	return когда
}

// ── настройка ────────────────────────────────────────────────────────────────

// announce собирает объявление локации из настройки. Каждый недобор называет и
// переменную, и флаг: локацию поднимает докер по конфигу, а чинит человек
// руками, и он должен видеть оба входа сразу (`kb:WORLD-31`).
func announce(name, addr, gives, listen string) (field.Announce, error) {
	if strings.TrimSpace(name) == "" {
		return field.Announce{}, fmt.Errorf(
			"не сказано, как локация называется в поле: имя — это её маршрут за дверью, вывести его не из чего; " +
				"задай WORLD_NAME либо -name")
	}
	if strings.TrimSpace(gives) == "" {
		return field.Announce{}, fmt.Errorf(
			"не сказано, что локация даёт: без этой строки она попадёт в список за дверью безымянной, и взять её осмысленно будет нельзя; " +
				"задай WORLD_GIVES либо -gives")
	}
	if strings.TrimSpace(addr) == "" {
		выведенный, err := selfAddr(name, listen)
		if err != nil {
			return field.Announce{}, err
		}
		addr = выведенный
	}
	return field.Announce{Name: name, Addr: addr, Gives: gives}, nil
}

// selfAddr выводит свой адрес в общей сети из имени и порта прослушивания.
//
// Это ПРАВИЛО, а не догадка: имя локации идёт и в путь за дверью, и в имя в
// сети (`kb:FUND-5`: alias = имя = ключ реестра), поэтому «имя:порт» — не
// эвристика, а то же самое имя, произнесённое в адресной форме. Ошиблись —
// дверь ответит `addr-unreachable`, и адрес задаётся явно; молчаливого
// расхождения тут случиться не может.
func selfAddr(name, listen string) (string, error) {
	_, порт, err := net.SplitHostPort(listen)
	if err != nil || порт == "" {
		return "", fmt.Errorf(
			"свой адрес не выведен: в адресе прослушивания %q нет порта (%v), а имя локации без порта адресом не является; "+
				"задай WORLD_SELF_ADDR либо -addr в форме «имя:порт»", listen, err)
	}
	return name + ":" + порт, nil
}

// flagSet — общий разбор флагов: свой вывод (чтобы подсказка не уезжала мимо
// потока) и своя строка использования.
func flagSet(s streams, cmd string) *flag.FlagSet {
	fs := flag.NewFlagSet("world "+cmd, flag.ContinueOnError)
	fs.SetOutput(s.err)
	fs.Usage = func() {
		fmt.Fprintf(s.err, "world %s — флаги (каждый перекрывает свою переменную среды):\n", cmd)
		fs.PrintDefaults()
	}
	return fs
}

// parse отделяет «попросили подсказку» от «ошиблись флагом»: первое не отказ.
// Позиционные аргументы отвергаются — иначе `world join baser` молча вошло бы
// в поле не под тем именем, которое человек назвал.
func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return errHelpShown
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("лишнее слово %q: настройка идёт флагами и переменными среды, а не позицией; %s -h покажет флаги",
			fs.Arg(0), fs.Name())
	}
	return nil
}

// таблица печатает «поле — значение» ровным столбцом.
func таблица(w io.Writer, строки [][2]string) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, с := range строки {
		fmt.Fprintf(tw, "  %s\t%s\n", с[0], с[1])
	}
	tw.Flush()
}
