#!/usr/bin/env bash
# Проба контроллера — ломает нарочно и требует, чтобы он назвал код, причину и выход.
#
#   ./control/probe-control.sh          зелёный прогон: что контроллер обязан уметь
#   ./control/probe-control.sh --red    красный прогон: нарочные поломки
#   ./control/probe-control.sh --both   (умолчание) сначала зелёный, потом красный
#
# ┌─────────────────────────────────────────────────────────────────────────────────────┐
# │ ЧТО ПРОБА СТЕРЕЖЁТ                                                                   │
# │                                                                                      │
# │ Не «работает ли контроллер», а СВОЙСТВА, которые ему заказаны:                        │
# │                                                                                      │
# │   1. до входа нет ничего — ни территорий, ни полей;                                   │
# │   2. ВХОД — ЭТО АДРЕС И ПАРОЛЬ, и больше ничего: хода «завести здесь» не существует;   │
# │   3. личность лежит В РАЗДАЧЕ по адресу, а не на контроллере, и берётся связью;        │
# │   4. территории и ключи живут В СКОУПЕ, а контексты докера — производные от него:      │
# │      поднялись при входе, ушли при выходе, и другая личность видит СВОИ территории;    │
# │   5. подъём вещи зовётся ГОТОВЫЙ (`deploy/remote.sh`), а не написан заново;            │
# │   6. отказ приходит тройкой: код · причина · выходы — и код соседа доезжает СВОИМ;     │
# │   7. неудача не оставляет следов: ключ неудачного подъёма не переживает попытку;       │
# │   8. подъём берёт ГОТОВЫЙ образ и говорит, ЧТО именно поднято, — тег и digest;        │
# │   9. точка входа образа поднимает пульт ОДНОЙ командой: проверяет власть над машиной, │
# │      дожидается ответа стуком, отказывает кодом и не бросает процесс сиротой — а       │
# │      команда в README не разъехалась с файлом запуска;                                │
# │  10. контроллер не знает, какие бывают ВЕЩИ: рецепт кладут в каталог, и вторая вещь    │
# │      поднимается тем же путём — без правки кода и без пересборки образа.              │
# │                                                                                      │
# │ Проба стережёт КОД (`"code":"no-scope"`), а не формулировку: проба, привязанная к     │
# │ тексту, зеленеет на верной правке и учит не трогать буквы (`WORLD2` 4.2).             │
# └─────────────────────────────────────────────────────────────────────────────────────┘
#
# ЧЕГО ЭТА ПРОБА НЕ ПРОВЕРЯЕТ БЕЗ ЖИВОГО КОНТУРА. Настоящий докер, настоящий подъём двери
# на втором ресурсе, настоящий ssh до чужого скоупа и подъём самого контроллера образом.
# Нет их — проба говорит об этом ВСЛУХ и завершается кодом 3: НЕПОЛНЫЙ ПРОГОН. Зелёного
# при неполном прогоне не бывает: «проверили, что смогли» и «проверили» — разные слова.
#
#   код 0   полный зелёный: прогнано всё
#   код 1   краснота: контроллер повёл себя не так, как обязан
#   код 3   неполный прогон: часть проверок не выполнялась, и названо — какая
#
# ЧТО НУЖНО НА МАШИНЕ: `go` (собрать контроллер) и `curl` (постучаться). Больше ничего:
# докер, ssh и второй ресурс проба подменяет заглушками и НЕ делает вид, что проверила то,
# на что их не хватило. Незаявленная зависимость — сама по себе дефект (`WORLD2-96`).
set -euo pipefail
export LC_ALL=C

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd -- "$HERE/.." && pwd)"
PORT="${CONTROL_PROBE_PORT:-18090}"
BASE="http://127.0.0.1:$PORT"

mode=--both
case "${1:-}" in
    --green|--red|--both) mode="$1" ;;
    -h|--help) sed -n '2,34p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    "") ;;
    *)  printf 'непонятный ключ: %s (см. --help)\n' "$1" >&2; exit 2 ;;
esac

total=0; failed=0; skipped=0

part()   { printf '\n\033[1m%s\033[0m\n' "$*" >&2; }
detail() { printf '      %s\n' "$*" >&2; }
ok()     { total=$((total + 1)); printf '  \033[32m✔\033[0m %s\n' "$1" >&2; }
bad()    { total=$((total + 1)); failed=$((failed + 1)); printf '  \033[31m✘\033[0m %s\n' "$1" >&2
           shift; local l; for l in "$@"; do detail "$l"; done; }
skip()   { skipped=$((skipped + 1)); printf '  \033[33m•\033[0m %s — НЕ ПРОГНАНО\n' "$1" >&2
           shift; local l; for l in "$@"; do detail "$l"; done; }

# ------------------------------------------------------------------ обстановка
TMP="$(mktemp -d)"
BIN="$TMP/control"
CALLS="$TMP/calls"
KEYS="$TMP/keys"
# ПОДСТАВНАЯ РАЗДАЧА СКОУПА — то, где теперь лежит личность юзера (`WORLD2` 3.4). Она не
# притворяется нашей: две ручки по одному адресу и пароль, ни одного своего знака. Ровно
# это вправе сделать чужая вилка, и ровно с этим обязан работать контроллер (`0.3`).
SHARE_PORT="${CONTROL_PROBE_SHARE_PORT:-18070}"
SHARE_URL="http://127.0.0.1:$SHARE_PORT/"
SHARE_PASS=тайна
SHARE_FILE="$TMP/состояние.json"
SHARE_PID=""
SHARE_PID_FILE="$TMP/share.pid"
SHARE_LOG="$TMP/share.log"
# Штамп сборки контроллера и явное имя образа от хозяина — значения прогона (`WORLD2-130`).
# ПОДСТАВНАЯ МАШИНА С SSH — та, на которую контроллер заходит ПАРОЛЕМ, чтобы завести ключ
# (`WORLD2-141`). Настоящий ssh-обмен, а не пересказ: команда уходит туда по-настоящему и
# выполняется её шеллом в её домашнем каталоге, а мы смотрим потом В ЕЁ ФАЙЛ.
MACHINE_PORT="${CONTROL_PROBE_SSH_PORT:-18022}"
MACHINE_PASS="рут-пароль-от-впс-9876"
MACHINE_HOME="$TMP/дом-машины"
MACHINE_PID=""
SRV_VERSION="sha-abc1234"
SRV_WORLD_IMAGE=""
SRV_SHARE_IMAGE=""
# Рецепт раздачи: обычный файл рядом с подъёмом. Содержимое неважно — читает его подъём.
SHARE_RECIPE="$TMP/share-compose.yaml"
# Реестр контекстов подставного докера. Он нужен не для красоты: контексты стали
# ПРОИЗВОДНЫМИ от скоупа, и проверить «поднялись при входе, ушли при выходе» можно только
# там, где подставной докер их и правда помнит.
CTXFILE="$TMP/контексты"
# Каталог рецептов — то место, куда хозяин машины кладёт свои вещи. Здесь он свой и
# временный: этим же каталогом проверяется главное свойство захода — вторая вещь
# поднимается БЕЗ правки кода и без пересборки образа (`WORLD2-131`).
RECIPES="$TMP/recipes"
# Каталог пульта — свой, пустой по умолчанию: «пульта нет» это ЗАКОННОЕ состояние, и
# проверять его надо ровно так же, как раздачу.
PULT="$TMP/pult"
SRV_PID=""
TOKEN=""

cleanup() {
    [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null || true
    [ -n "$SHARE_PID" ] && kill "$SHARE_PID" 2>/dev/null || true
    [ -s "$SHARE_PID_FILE" ] && kill "$(cat "$SHARE_PID_FILE")" 2>/dev/null || true
    [ -n "$MACHINE_PID" ] && kill "$MACHINE_PID" 2>/dev/null || true
    # Точка входа держит контроллер в фоне: уйди проба, не сняв её, и на машине остался бы
    # осиротевший процесс на порту пробы — следующий прогон краснел бы «по наследству».
    [ -n "${ENTRY_PID:-}" ] && kill -TERM "$ENTRY_PID" 2>/dev/null || true
    rm -rf "$TMP"
}
trap cleanup EXIT

need() {
    command -v "$1" >/dev/null 2>&1 && return 0
    # Нехватка обстановки — это НЕ краснота: краснота означает «контроллер повёл себя не
    # так». Своё же правило «непрогнанное не проверено» применяем и к своим зависимостям.
    skip "весь прогон" "на машине нет $1 — $2" "поставь его и повтори"
    printf '\nнеполный прогон: не на чем прогонять\n' >&2
    exit 3
}
need go "им собирается контроллер"
need curl "им проба стучится в ручки"

# Заглушки докера и подъёма двери. Они НЕ притворяются настоящими: их дело — записать, что
# позвали, и ответить заданное. Проверяем мы поведение контроллера, а не работу докера.
mk_fakes() {
    cat > "$TMP/docker" <<'FAKE'
#!/usr/bin/env bash
printf 'docker %s\n' "$*" >> "$CALLS"
# Контексты подставной докер ПОМНИТ — в файле. Без этого не проверить главного свойства
# захода: контексты производны от скоупа, поднимаются при входе и уходят при выходе.
# ЧЕМ ПОДНИМАТЬ ВЕЩЬ — контроллер спрашивает у РЕЦЕПТА (`docker compose config --images`) и
# проверяет пин ИЗМЕРЕНИЕМ: называет подстановку и спрашивает заново. Заглушка ведёт себя
# ровно так же, как настоящий компоуз: назвали подстановку — вернула её значение, не назвали
# — своё умолчание. Иначе проверялась бы наша догадка, а не поведение.
if [ "$1" = compose ]; then
    for a in "$@"; do
        case "$a" in
            *"/share-compose.yaml") retsept=share ;;
            *"/compose.yaml")       retsept=dver ;;
            *"весы.yaml")           retsept=svoya ;;
        esac
    done
    case "${retsept:-dver}" in
        share) [ -n "${SHARE_IMAGE:-}" ] && printf '%s\n' "$SHARE_IMAGE" || printf 'ghcr.io/omnifield/world-share:latest\n' ;;
        svoya) printf 'весы:1.2\n' ;;                       # своя вещь: подстановку не читает
        *)     [ -n "${WORLD_IMAGE:-}" ] && printf '%s\n' "$WORLD_IMAGE" || printf 'ghcr.io/omnifield/world:latest\n' ;;
    esac
    exit 0
fi
if [ "$1" = context ]; then
    case "$2" in
        ls)      cat "$CTXFILE" 2>/dev/null; exit 0 ;;
        inspect) grep -qx -- "$3" "$CTXFILE" 2>/dev/null && exit 0 || exit 1 ;;
        create|update)
                 grep -qx -- "$3" "$CTXFILE" 2>/dev/null || printf '%s\n' "$3" >> "$CTXFILE"
                 exit 0 ;;
        rm)      shift 2
                 for a in "$@"; do case "$a" in -*) ;; *) grep -vx -- "$a" "$CTXFILE" > "$CTXFILE.tmp" 2>/dev/null || true
                                                        mv "$CTXFILE.tmp" "$CTXFILE" ;; esac; done
                 exit 0 ;;
    esac
    exit 0
fi
# Дальше — вопросы про то, что стоит на машине. Отвечаем ровно те поля, которые контроллер
# спросил: `ps` — какие контейнеры есть, `inspect` — чьи они (метка проекта компоуза),
# здоровы ли и запущены ли. Своего имени контейнера у контроллера нет: что стоит на
# территории, говорит та машина.
for a in "$@"; do
    case "$a" in
        ps)      printf 'aaa111\n'; exit 0 ;;
        inspect) printf 'world\thealthy\trunning\n'; exit 0 ;;
    esac
done
exit 0
FAKE
    cat > "$TMP/remote.sh" <<'FAKE'
#!/usr/bin/env bash
printf 'remote.sh %s\n' "$*" >> "$CALLS"
# ЧЕМ его позвали — в журнал отдельной строкой: пин уезжает подстановкой рецепта, и увидеть
# его в аргументах нельзя. Не запиши мы это, порча «вернуть latest» прошла бы незамеченной.
printf 'env WORLD_IMAGE=%s SHARE_IMAGE=%s\n' "${WORLD_IMAGE:-}" "${SHARE_IMAGE:-}" >> "$CALLS"
if [ -f "$REFUSE_FILE" ]; then
    printf 'REMOTE-REFUSAL: %s\n' "$(cat "$REFUSE_FILE")"
    printf '\n\033[1;31m✗ отказ:\033[0m ресурс world@10.8.0.5 не принял ключ\n' >&2
    printf '  выход: положи открытый ключ юзеру на тот ресурс\n' >&2
    exit 1
fi
# ПОДЪЁМ ПО РЕЦЕПТУ РАЗДАЧИ — и раздача правда встаёт. Иначе проверялось бы не то: мы
# смотрим, что контроллер зовёт подъём и ЖДЁТ появления раздачи по адресу, а не что он
# сам что-то у себя записал. Вывод уводим в файл: держи заглушка трубу подъёма открытой,
# вызывающий ждал бы её конца.
case " $* " in
    *" add "*)
        case " $* " in
            *" --recipe ${SHARE_RECIPE:-нет-такого} "*)
                "$SHARE_BIN" "127.0.0.1:$SHARE_PORT" "$SHARE_PASS" "$SHARE_FILE" \
                    > "$SHARE_LOG" 2>&1 < /dev/null &
                printf '%s\n' "$!" > "$SHARE_PID_FILE"
                n=0
                while [ "$n" -lt 100 ]; do
                    (exec 3<>/dev/tcp/127.0.0.1/"$SHARE_PORT") 2>/dev/null && break
                    sleep 0.1; n=$((n + 1))
                done ;;
        esac ;;
esac
exit 0
FAKE
    chmod +x "$TMP/docker" "$TMP/remote.sh"
    # Рецепт двери лежит РЯДОМ С ПОДЪЁМОМ — по той же формуле, по которой его выводит сам
    # контроллер. Содержимое неважно: разбирает рецепт компоуз у соседа, а не мы.
    printf 'name: world\n' > "$TMP/compose.yaml"
    # Каталог рецептов — ландшафт машины. Пустой: вещи в него кладёт хозяин, а не образ.
    rm -rf "$RECIPES"; mkdir -p "$RECIPES"
    # Рецепт раздачи скоупа — им контроллер поднимает личность на названной машине.
    printf 'name: world-share\n' > "$SHARE_RECIPE"
}

# ── подставная раздача скоупа ────────────────────────────────────────────────
#
# Личность юзера теперь лежит НЕ ЗДЕСЬ, а по адресу (`WORLD2` 3.4, `WORLD2-124`). Значит и
# проверять её надо там: проба поднимает раздачу, а потом смотрит в ФАЙЛ, который та
# держит, — а не в ответ контроллера. Ответ можно собрать из памяти, файл — нельзя.
#
# Раздача написана здесь заново и нарочно ГОЛОЙ: две ручки, пароль, ни одного своего
# заголовка. Так вправе выглядеть чужая вилка, и ровно с ней обязан работать контроллер.
# Взять настоящую из зоны `share` было бы проще — и проверяли бы мы тогда пару «наш
# контроллер + наша раздача», а не соблюдение формы мира.
mk_share() {
    cat > "$TMP/share.go" <<'GO'
package main

import (
	"io"
	"net/http"
	"os"
)

func main() {
	addr, pass, file := os.Args[1], os.Args[2], os.Args[3]
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if _, p, ok := r.BasicAuth(); !ok || p != pass {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodGet:
			data, err := os.ReadFile(file)
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(data)
		case http.MethodPut:
			data, _ := io.ReadAll(r.Body)
			if err := os.WriteFile(file, data, 0o600); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Allow", "GET, PUT")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	if err := http.ListenAndServe(addr, nil); err != nil {
		os.Exit(1)
	}
}
GO
    (cd "$TMP" && go build -o "$TMP/share" share.go) 2>"$TMP/share-build.log"
}

# mk_machine — собрать подставную машину. Она живёт в зоне обычным файлом (как `run/fake.go`
# у соседей): им пользуются и тесты ручек, и эта проба, а из тестового файла его не взять.
mk_machine() {
    (cd "$HERE" && go build -o "$TMP/машина" ./internal/creds/sshtest/cmd) 2>"$TMP/machine-build.log"
}

# machine_start — поднять машину заново, с пустым домом: «что на ней появилось» проверяется
# по её файлу, и хвост прошлого прогона превратил бы проверку в ложь.
machine_start() {
    machine_stop
    rm -rf "$MACHINE_HOME"; mkdir -p "$MACHINE_HOME"
    "$TMP/машина" "127.0.0.1:$MACHINE_PORT" world "$MACHINE_PASS" "$MACHINE_HOME" \
        > "$TMP/machine.log" 2>&1 &
    MACHINE_PID=$!
    local n=0
    while [ "$n" -lt 100 ]; do
        if (exec 3<>/dev/tcp/127.0.0.1/"$MACHINE_PORT") 2>/dev/null; then return 0; fi
        kill -0 "$MACHINE_PID" 2>/dev/null || return 1
        sleep 0.1; n=$((n + 1))
    done
    return 1
}

machine_stop() {
    [ -n "$MACHINE_PID" ] || return 0
    kill "$MACHINE_PID" 2>/dev/null || true
    wait "$MACHINE_PID" 2>/dev/null || true
    MACHINE_PID=""
}

# machine_keys — что лежит в `~/.ssh/authorized_keys` подставной машины.
machine_keys() { cat "$MACHINE_HOME/.ssh/authorized_keys" 2>/dev/null || true; }

# share_start [файл-состояния] — поднять раздачу. Без файла раздача отвечает «состояния
# ещё нет» (404) — так выглядит свежая раздача, и заведение скоупа опирается ровно на это.
share_start() {
    share_stop
    rm -f "$SHARE_FILE"
    [ -n "${1:-}" ] && printf '%s' "$1" > "$SHARE_FILE"
    "$TMP/share" "127.0.0.1:$SHARE_PORT" "$SHARE_PASS" "$SHARE_FILE" >"$SHARE_LOG" 2>&1 &
    SHARE_PID=$!
    printf '%s\n' "$SHARE_PID" > "$SHARE_PID_FILE"
    local n=0
    while [ "$n" -lt 100 ]; do
        if (exec 3<>/dev/tcp/127.0.0.1/"$SHARE_PORT") 2>/dev/null; then return 0; fi
        kill -0 "$SHARE_PID" 2>/dev/null || return 1
        sleep 0.1; n=$((n + 1))
    done
    return 1
}

# share_stop снимает раздачу, кем бы она ни была поднята: пробой напрямую или подставным
# подъёмом (тот пишет свой pid в тот же файл — второго списка одного и того же не бывает).
share_stop() {
    if [ -n "$SHARE_PID" ]; then
        kill "$SHARE_PID" 2>/dev/null || true
        wait "$SHARE_PID" 2>/dev/null || true
        SHARE_PID=""
    fi
    if [ -s "$SHARE_PID_FILE" ]; then
        kill "$(cat "$SHARE_PID_FILE")" 2>/dev/null || true
        : > "$SHARE_PID_FILE"
    fi
    # Ждём, пока порт освободится: следующий подъём сядет на тот же адрес.
    local n=0
    while [ "$n" -lt 50 ]; do
        (exec 3<>/dev/tcp/127.0.0.1/"$SHARE_PORT") 2>/dev/null || return 0
        sleep 0.1; n=$((n + 1))
    done
    return 0
}

# личность <имя> [участок:адрес…] — файл состояния формата мира (`WORLD2` 3.4). Собран
# руками, а не нашим кодом: так же его собрала бы чужая вилка.
lichnost() {
    # Имена переменных ЛАТИНСКИЕ: bash не поддерживает не-ASCII в именах и молча
    # превращает `имя=x` в «not a valid identifier». Тексты при этом русские — та же
    # раскладка, что у соседних зон.
    local name="$1"; shift
    local terr="" keys="" sep="" one
    for one in "$@"; do
        terr="$terr$sep{\"имя\":\"${one%%:*}\",\"адрес\":\"${one#*:}\",\"ключ\":\"${one%%:*}\"}"
        keys="$keys$sep{\"имя\":\"${one%%:*}\",\"вид\":\"ssh\",\"значение\":\"-----ключ-----\"}"
        sep=,
    done
    printf '{"формат":1,"личность":{"имя":"%s","бренд":"","описание":""},"ключи":[%s],"территории":[%s],"поля":[]}' \
        "$name" "$keys" "$terr"
}

# start_server [docker] [remote.sh] — поднять контроллер с названными инструментами.
# Подменяемость инструментов — не удобство пробы, а то, ради чего они вынесены значениями:
# поведение контроллера обязано проверяться там, где контура нет.
start_server() {
    local docker="${1:-$TMP/docker}" remote="${2:-$TMP/remote.sh}"
    stop_server
    rm -rf "$KEYS" "$RECIPES"; mkdir -p "$KEYS" "$RECIPES"
    : > "$CALLS"; : > "$CTXFILE"
    TOKEN=""

    # ШТАМП СБОРКИ и явное имя от хозяина — значения прогона: ими проверяются три разных
    # состояния подъёма (пин · версии нет · имя названо снаружи).
    CALLS="$CALLS" REFUSE_FILE="$TMP/refuse" CTXFILE="$CTXFILE" \
    WORLD_VERSION="${SRV_VERSION-sha-abc1234}" \
    WORLD_IMAGE="${SRV_WORLD_IMAGE:-}" SHARE_IMAGE="${SRV_SHARE_IMAGE:-}" \
    SHARE_BIN="$TMP/share" SHARE_PORT="$SHARE_PORT" SHARE_PASS="$SHARE_PASS" \
    SHARE_FILE="$SHARE_FILE" SHARE_RECIPE="$SHARE_RECIPE" \
    SHARE_PID_FILE="$SHARE_PID_FILE" SHARE_LOG="$SHARE_LOG" \
    CONTROL_ADDR="127.0.0.1:$PORT" CONTROL_DOCKER="$docker" CONTROL_REMOTE_SH="$remote" \
    CONTROL_KEYS="$KEYS" CONTROL_SCOPE_TIMEOUT=3 CONTROL_TOOL_TIMEOUT=20 \
    CONTROL_PULT="$PULT" CONTROL_RECIPES="$RECIPES" CONTROL_SHARE_RECIPE="$SHARE_RECIPE" \
        "$BIN" > "$TMP/server.log" 2>&1 &
    SRV_PID=$!

    # Ждём, пока порт ответит. Через bash-петлю `/dev/tcp`, а не питоном: питон — это
    # незаявленная зависимость, на которой уже спотыкались (`WORLD2-96`, пункт 5).
    local n=0
    while [ "$n" -lt 100 ]; do
        if (exec 3<>/dev/tcp/127.0.0.1/"$PORT") 2>/dev/null; then return 0; fi
        kill -0 "$SRV_PID" 2>/dev/null || { cat "$TMP/server.log" >&2; return 1; }
        sleep 0.1; n=$((n + 1))
    done
    cat "$TMP/server.log" >&2
    return 1
}

stop_server() {
    [ -n "$SRV_PID" ] || return 0
    kill "$SRV_PID" 2>/dev/null || true
    wait "$SRV_PID" 2>/dev/null || true
    SRV_PID=""
}

# call МЕТОД ПУТЬ [ТЕЛО] — стучимся. Ответ кладём в STATUS и BODY.
STATUS=""; BODY=""
call() {
    local method="$1" path="$2" body="${3-}"
    local args=(-s -X "$method" -o "$TMP/body" -w '%{http_code}')
    [ -n "$TOKEN" ] && args+=(-H "Authorization: Bearer $TOKEN")
    [ -n "$body" ] && args+=(--data "$body")
    STATUS="$(curl "${args[@]}" "$BASE$path" 2>/dev/null || echo 000)"
    BODY="$(cat "$TMP/body" 2>/dev/null || true)"
}

# put_pult [содержимое index.html] — кладём то, что приезжает из зоны `web` собранным.
# Содержимое неважно: зона `control` пульт не рисует, она его отдаёт.
put_pult() {
    rm -rf "$PULT"; mkdir -p "$PULT/assets"
    printf '%s' "${1:-<!doctype html>лицо мира}" > "$PULT/index.html"
    printf '// пульт' > "$PULT/assets/index-XY.js"
}

# browser МЕТОД ПУТЬ — тот же запрос, но так, как его шлёт адресная строка. Пульт ходит
# сюда `fetch` (Accept: */*) и обязан по-прежнему получать JSON.
HEADERS=""
browser() {
    STATUS="$(curl -s -X "${1:-GET}" -H 'Accept: text/html' -D "$TMP/headers" \
        -o "$TMP/body" -w '%{http_code}' "$BASE$2" 2>/dev/null || echo 000)"
    BODY="$(cat "$TMP/body" 2>/dev/null || true)"
    HEADERS="$(cat "$TMP/headers" 2>/dev/null || true)"
}

# raw_get ПУТЬ — запрос путём КАК ЕСТЬ, без нормализации на стороне curl. Нужен ровно для
# проверки обхода: без `--path-as-is` curl схлопывает `..` сам, и проверка зеленеет, даже
# если контроллер путь не чистит вовсе. Такая ложно-зелёная проверка уже стоила соседней
# зоне ревью (`WORLD2-96`, пункт 1) — здесь она закрыта флагом, а не надеждой.
raw_get() {
    STATUS="$(curl -s --path-as-is -o "$TMP/body" -w '%{http_code}' "$BASE$1" 2>/dev/null || echo 000)"
    BODY="$(cat "$TMP/body" 2>/dev/null || true)"
}

# want_refusal <код> <имя> — контроллер ОБЯЗАН отказать этим кодом, с причиной и выходом.
want_refusal() {
    local code="$1" name="$2"
    case "$BODY" in
        *"\"code\":\"$code\""*) ;;
        *"\"code\":"*) bad "$name" "ждали код $code, а контроллер назвал другой:" "$BODY"; return ;;
        *) bad "$name" "контроллер не отказал вовсе — а обязан был: $code" "$BODY"; return ;;
    esac
    case "$BODY" in
        *'"why":""'*|*'"why":"'*) ;;
        *) bad "$name" "отказ без причины: $BODY"; return ;;
    esac
    case "$BODY" in
        *'"ways":[]'*|*'"ways":null'*) bad "$name" "отказ без выхода — тупик (WORLD2 2.3): $BODY"; return ;;
        *'"ways":['*) ;;
        *) bad "$name" "в отказе нет выходов вовсе: $BODY"; return ;;
    esac
    ok "$name → $code"
}

want_has() {
    local needle="$1" name="$2"
    case "$BODY" in
        *"$needle"*) ok "$name" ;;
        *) bad "$name" "в ответе нет $needle:" "$BODY" ;;
    esac
}

# pin_u_vyzova <кусок вызова> <ожидаемая подстановка> <имя проверки> — чем позвали ИМЕННО
# этот подъём. Искать пин «где-нибудь в журнале» нельзя: снятие пинится тем же значением, и
# порча «подъём перестал пинить» прошла бы незамеченной — журнал остался бы «зелёным» из-за
# соседней строки. Подставной подъём пишет свои значения строкой СРАЗУ за вызовом.
pin_u_vyzova() {
    local vyzov="$1" zhdyom="$2" imya="$3"
    if grep -F -A1 -- "$vyzov" "$CALLS" | grep -qF -- "$zhdyom"; then
        ok "$imya"
    else
        bad "$imya" "у вызова «$vyzov» нет подстановки «$zhdyom»:" "$(grep -F -A1 -- "$vyzov" "$CALLS" | tail -4)"
    fi
}

want_status() {
    local want="$1" name="$2"
    [ "$STATUS" = "$want" ] && ok "$name" || bad "$name" "код HTTP $STATUS вместо $want" "$BODY"
}

# вход — АДРЕС И ПАРОЛЬ, и больше ничего (`WORLD2` 3.7). Ни имени, ни бренда, ни «завести
# здесь»: разница между «состояние есть» и «состояния нет» только в исходе.
вход() {
    call POST /api/session "{\"addr\":\"$SHARE_URL\",\"password\":\"$SHARE_PASS\"}"
    TOKEN="$(printf '%s' "$BODY" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
}

# завести — скоуп заводится ТАМ, где будет лежать: две пары (машина и скоуп) плюс личность.
zavesti() {
    call POST /api/scope "{\"scope\":{\"addr\":\"$SHARE_URL\",\"password\":\"$SHARE_PASS\"},\"identity\":{\"name\":\"егор\",\"brand\":\"\"},\"machine\":{\"name\":\"vps\",\"addr\":\"world@10.8.0.5\",\"creds\":{\"kind\":\"key\",\"value\":\"-----ключ-----\"}}}"
    TOKEN="$(printf '%s' "$BODY" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
}

# ------------------------------------------------------------------ подъём: подставной докер
# ┌─────────────────────────────────────────────────────────────────────────────────────┐
# │ ЧТО ЗДЕСЬ ПРОВЕРЯЕТСЯ, А ЧТО НЕТ. Проверяются РЕШЕНИЯ `up.sh`: какие команды он       │
# │ отдаёт докеру, чем отказывает и что говорит человеку. НЕ проверяется, что докер       │
# │ действительно тянет образ и поднимает контейнер — для этого нужен живой докер, и об   │
# │ этом сказано вслух в конце прогона.                                                   │
# │                                                                                      │
# │ Раньше подъём не проверялся вовсе: он всегда пересобирал образ, и проверять было      │
# │ нечего. Теперь он ТЯНЕТ ГОТОВОЕ, и вместе со сборкой ушла защита от «поднял старый    │
# │ образ и не заметил» (`WORLD2-92`). Её заменили две вещи — явный `pull` и рассказ о     │
# │ том, что поднято, — и обе обязаны стеречься пробой, иначе их некому удержать.         │
# └─────────────────────────────────────────────────────────────────────────────────────┘
UP_STUB="$TMP/stub"
UP_ROOT="$TMP/podjom"
UP_OUT=""; UP_CALLS=""; UP_CODE=0

mk_up_stub() {
    mkdir -p "$UP_STUB"
    cat > "$UP_STUB/docker" <<'STUB'
#!/usr/bin/env bash
# ПОДСТАВНОЙ докер: пишет журнал вызовов и отвечает по сценарию. Ничего не тянет и не
# поднимает — проверяется поведение подъёма, а не работа докера.
printf '%s\n' "$*" >> "${UP_LOG:?}"
ALL="$*"
case "$1" in
    info|ps|volume|port|logs) exit 0 ;;
    pull)
        # Реестр отвечает по сценарию. Тексты — настоящие докеровские: по ним подъём и
        # различает ступень, поэтому подставлять сюда свои формулировки нельзя.
        case "${STUB_PULL:-ok}" in
            ok)      exit 0 ;;
            denied)  echo 'Error response from daemon: denied: requested access to the resource is denied' >&2; exit 1 ;;
            notag)   echo 'Error response from daemon: manifest unknown' >&2; exit 1 ;;
            noroute) echo 'dial tcp 140.82.121.34:443: i/o timeout' >&2; exit 1 ;;
            *)       echo 'Error response from daemon: что-то своё' >&2; exit 1 ;;
        esac ;;
    inspect)
        case "$ALL" in
            *State.Health*) echo healthy ;;
            *Config.Image*) echo "${STUB_IMAGE:?}" ;;
            *)              echo 'sha256:podstavnyj-obraz' ;;
        esac
        exit 0 ;;
    image)
        # Образа нет — значит НЕТ НИ ОДНОГО его поля. Отвечать «полей не знаю, а digest
        # вот» умеет только плохая заглушка: настоящий докер отказывает на любой вопрос
        # про исчезнувший образ, и проверка, построенная на поблажке, зеленела бы зря.
        case "$ALL" in
            *sha256:*) [ -n "${STUB_IMAGE_GONE:-}" ] && exit 1 ;;
        esac
        case "$ALL" in
            *RepoDigests*)  printf '%s\n' "${STUB_DIGEST-}"; exit 0 ;;
            *.Created*)     echo '2026-08-15T00:00:00Z';     exit 0 ;;
            # `image inspect` без формата — вопрос «лежит ли образ здесь», и вопросов этих
            # ДВА РАЗНЫХ: про ИМЯ (можно ли поднять офлайн) и про sha (жив ли ещё тот
            # образ, которым поднят контейнер). Слить их значило бы проверять один случай
            # вместо двух.
            *sha256:*)      [ -z "${STUB_IMAGE_GONE:-}" ] && exit 0 || exit 1 ;;
            *)              [ -n "${STUB_HAVE_LOCAL:-}" ] && exit 0 || exit 1 ;;
        esac ;;
    compose)
        # Ключи до подкоманды сдвигаем парами: у подъёма это `-f ФАЙЛ`.
        shift
        while [ $# -gt 0 ]; do case "$1" in -*) shift 2 ;; *) break ;; esac; done
        case "$1" in
            config) printf '%s\n' "${STUB_IMAGE:?}"; exit 0 ;;
            *)      exit 0 ;;
        esac ;;
esac
exit 0
STUB
    chmod +x "$UP_STUB/docker"
}

# mk_up_root — СВОЙ корень репозитория для подъёма. Нужен потому, что `up.sh` смотрит на
# собранный пульт по пути `../deploy/.build/web-dist`: проверять его на настоящем дереве
# значило бы получать разный прогон у того, кто пульт собирал, и у того, кто нет. Заодно
# чужая зона не трогается вовсе — ни файлом, ни временно.
mk_up_root() {
    rm -rf "$UP_ROOT"
    mkdir -p "$UP_ROOT/control" "$UP_ROOT/deploy"
    cp "$HERE/up.sh" "$HERE/compose.yaml" "$UP_ROOT/control/"
}
put_up_pult() {
    mkdir -p "$UP_ROOT/deploy/.build/web-dist"
    printf '<!doctype html>лицо мира' > "$UP_ROOT/deploy/.build/web-dist/index.html"
}

# Сценарий по умолчанию: реестр отвечает, образа на машине нет, digest у притянутого есть.
reset_stub() {
    STUB_PULL=ok; STUB_HAVE_LOCAL=; STUB_OFFLINE=0; STUB_IMAGE_GONE=
    STUB_IMAGE=ghcr.io/omnifield/world-control:latest
    STUB_DIGEST=ghcr.io/omnifield/world-control@sha256:podstava
}
reset_stub

# run_up [ключи…] — позвать подъём с подставным докером. Итог кладём в UP_CODE, UP_OUT
# (вывод целиком: и человеку, и машинный код) и UP_CALLS (журнал вызовов докера).
run_up() {
    : > "$TMP/up-calls"
    UP_CODE=0
    UP_LOG="$TMP/up-calls" PATH="$UP_STUB:$PATH" \
    STUB_PULL="$STUB_PULL" STUB_IMAGE="$STUB_IMAGE" STUB_DIGEST="$STUB_DIGEST" \
    STUB_HAVE_LOCAL="$STUB_HAVE_LOCAL" STUB_IMAGE_GONE="$STUB_IMAGE_GONE" \
    CONTROL_OFFLINE="$STUB_OFFLINE" CONTROL_WAIT=3 \
        bash "$UP_ROOT/control/up.sh" "$@" >"$TMP/up-out" 2>&1 || UP_CODE=$?
    UP_OUT="$(cat "$TMP/up-out" 2>/dev/null || true)"
    UP_CALLS="$(cat "$TMP/up-calls" 2>/dev/null || true)"
}

# want_up_code <код> <имя> — подъём обязан отказать ЭТИМ кодом и вести куда-то дальше.
# Стережём код, а не формулировку (`WORLD2` 4.2); от выходов проверяем, что они ЕСТЬ —
# отказ без выхода это тупик (`WORLD2` 2.3), а не отказ.
want_up_code() {
    local want="$1" name="$2" ways
    case "$UP_OUT" in
        *"CONTROL-REFUSAL: $want"*) ;;
        *"CONTROL-REFUSAL:"*)
            bad "$name" "подъём отказал ДРУГИМ кодом:" "$(printf '%s\n' "$UP_OUT" | grep CONTROL-REFUSAL)"; return ;;
        *)  bad "$name" "подъём не отказал вовсе — а обязан был: $want" "код выхода $UP_CODE"; return ;;
    esac
    [ "$UP_CODE" = 1 ] || { bad "$name" "отказ $want, а код выхода $UP_CODE вместо 1"; return; }
    ways="$(printf '%s\n' "$UP_OUT" | grep -c '^  выход: ' || true)"
    [ "${ways:-0}" -ge 2 ] || { bad "$name" "у отказа $want выходов меньше двух ($ways) — это диагноз, а не отказ"; return; }
    ok "$name → $want"
}
want_called()     { case "$UP_CALLS" in *"$1"*) ok "$2" ;; *) bad "$2" "в журнале вызовов докера нет «$1»:" "$UP_CALLS" ;; esac; }
want_not_called() { case "$UP_CALLS" in *"$1"*) bad "$2" "докера позвали с «$1» — а не должны были:" "$UP_CALLS" ;; *) ok "$2" ;; esac; }
want_said()       { case "$UP_OUT" in *"$1"*) ok "$2" ;; *) bad "$2" "в выводе подъёма нет «$1»:" "$UP_OUT" ;; esac; }
want_not_said()   { case "$UP_OUT" in *"$1"*) bad "$2" "подъём сказал «$1» — а это неправда:" "$UP_OUT" ;; *) ok "$2" ;; esac; }

# ------------------------------------------------------------------ точка входа образа
# ┌─────────────────────────────────────────────────────────────────────────────────────┐
# │ ЧТО ЗДЕСЬ ПРОВЕРЯЕТСЯ. `control/control-entry.sh` — то, что делает контроллер ИЗНУТРИ │
# │ образа (`WORLD2-120`): проверяет власть над машиной и состояние, поднимает           │
# │ контроллер, ДОЖИДАЕТСЯ его ответа и говорит, чего не хватает для добавления ресурса.  │
# │ Раньше это делал `up.sh` снаружи — но на машине юзера снаружи нет ничего, и           │
# │ проверка, не уехавшая в образ, не существует вовсе.                                   │
# │                                                                                      │
# │ ПРОГОНЯЕТСЯ НАСТОЯЩИЙ ФАЙЛ, а не его пересказ: рядом кладём подставной докер и        │
# │ временное состояние, а контроллером ставим то НАСТОЯЩИЙ бинарь (тогда проверяется     │
# │ ответ и стук), то заглушку (тогда проверяется, что бывает с мёртвым и молчащим).      │
# │                                                                                      │
# │ ЧЕГО ЭТА ЧАСТЬ НЕ ПРОВЕРЯЕТ: ни одного контейнера здесь не поднимается и докера нет   │
# │ вовсе. Что образ собирается с этой точкой входа и что `docker run` из README и правда │
# │ поднимает пульт — живой прогон, и он назван непрогнанным в конце.                     │
# └─────────────────────────────────────────────────────────────────────────────────────┘
ENTRY_DIR="$TMP/obraz"
ENTRY_STATE="$TMP/sostoyanie"
ENTRY_CALLS="$TMP/entry-calls"
ENTRY_LOG="$TMP/entry-control.log"
ENTRY_OUT="$TMP/entry-out"
ENTRY_SOCK="$TMP/docker.sock"
ENTRY_PORT="${CONTROL_PROBE_ENTRY_PORT:-18091}"
ENTRY_PID=""
ENTRY_CODE=0
# Имя образа двери, которое подставной докер отдаёт на вопрос «что стоит в файле запуска
# соседа». Настоящее имя лежит в `deploy/compose.yaml` и принадлежит зоне `deploy`; здесь
# важно не оно, а то, что мы СПРАШИВАЕМ, а не пишем копией.
ENTRY_DOOR_IMAGE=ghcr.io/omnifield/world:latest
# ПРЕЖНЕЕ имя двери — то, под которым зона знала её до `WORLD2-121`. Собрано из кусков
# нарочно: этим же образцом проба ищет прежнее имя в самой зоне, и написанное целиком оно
# нашло бы себя.
ENTRY_STAROE_IMYA="omnifield/world"":dev"

mk_entry() {
    mkdir -p "$ENTRY_DIR"
    cp "$HERE/control-entry.sh" "$ENTRY_DIR/control-entry.sh"
    # Стук кладём под тем именем, под которым он едет в образ: точка входа ищет его рядом
    # с собой, и разъедься имена — прогон зеленел бы там, где живой образ молчит.
    cp "$HERE/knock.sh" "$ENTRY_DIR/control-knock.sh"

    # ПОДСТАВНОЙ ДОКЕР: пишет журнал вызовов и отвечает по сценарию. Ничего не поднимает —
    # проверяется поведение точки входа, а не работа докера.
    cat > "$ENTRY_DIR/docker" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${ENTRY_CALLS:?}"
case "$1" in
    info)  [ -n "${STUB_DAEMON_DEAD:-}" ] && exit 1
           exit 0 ;;
    image) # `image inspect <имя>` — лежит ли образ двери на этой машине
           [ -n "${STUB_DOOR_HERE:-}" ] && exit 0
           exit 1 ;;
    compose)
           # Ключи до подкоманды сдвигаем парами: у точки входа это `-f ФАЙЛ`.
           shift
           while [ $# -gt 0 ]; do case "$1" in -*) shift 2 ;; *) break ;; esac; done
           case "$1" in config) printf '%s\n' "${STUB_DOOR_IMAGE-}"; exit 0 ;; esac
           exit 0 ;;
esac
exit 0
STUB

    # ПОДСТАВНОЙ КОНТРОЛЛЕР: живёт сколько сказали и пишет в журнал, что с ним делали. По
    # этому журналу видно то, чего не видно по коду возврата, — запускали ли его вообще и
    # дошёл ли до него сигнал. Порта он не слушает намеренно: им проверяются ровно те
    # случаи, где контроллер не отвечает.
    cat > "$ENTRY_DIR/zaglushka" <<'ZAG'
#!/usr/bin/env bash
printf 'контроллер запущен\n' >> "${STUB_CONTROL_LOG:-/dev/null}"
trap 'printf "контроллер получил TERM\n" >> "${STUB_CONTROL_LOG:-/dev/null}"; exit 0' TERM
i=0
while [ "$i" -lt "${STUB_CONTROL_LIFE:-2}" ]; do sleep 1; i=$((i + 1)); done
exit "${STUB_CONTROL_CODE:-0}"
ZAG
    chmod +x "$ENTRY_DIR/docker" "$ENTRY_DIR/zaglushka"

    # ДОКЕР-СОКЕТ. `[ -S ]` требует настоящего файла-сокета, а сделать его оболочкой нечем;
    # подложить вместо него обычный файл нельзя — проверка зеленела бы там, где сокета нет.
    # Делаем тем, что у пробы УЖЕ заявлено (`go`): программа создаёт unix-сокет и выходит,
    # файл остаётся. Не вышло — сценарии с сокетом честно пропускаются, а не подменяются.
    cat > "$TMP/sokket.go" <<'GO'
package main

import (
	"net"
	"os"
)

func main() {
	if _, err := net.Listen("unix", os.Args[1]); err != nil {
		os.Exit(1)
	}
}
GO
    rm -f "$ENTRY_SOCK"
    (cd "$TMP" && go run sokket.go "$ENTRY_SOCK") >/dev/null 2>&1 || true
}

# entry_prepare <чем поднимать> — чистое состояние и выбранный «контроллер» рядом с точкой
# входа. Пульт кладём собранным, связку — с правами, которые примет ssh.
entry_prepare() {
    rm -rf "$ENTRY_STATE"
    # Каталога под скоупы здесь больше нет: личность лежит по адресу, а не на контроллере
    # (`WORLD2-124`). Зато рядом с подъёмом лежит РЕЦЕПТ РАЗДАЧИ — им личность и заводится.
    mkdir -p "$ENTRY_STATE/keys" "$ENTRY_STATE/docker" \
             "$ENTRY_STATE/pult" "$ENTRY_STATE/deploy" "$ENTRY_STATE/share"
    chmod 700 "$ENTRY_STATE/keys"
    printf '<!doctype html>лицо мира' > "$ENTRY_STATE/pult/index.html"
    # Файл запуска соседа: имя образа двери отдаёт подставной докер, но САМ файл должен
    # лежать там, где его ищет точка входа, — рядом с готовым подъёмом двери.
    : > "$ENTRY_STATE/deploy/compose.yaml"
    # Рецепт раздачи — там, где его ищет точка входа: рядом с зоной подъёма, ярусом выше.
    printf 'name: world-share\n' > "$ENTRY_STATE/share/compose.yaml"
    # Вывод обнуляем ЗДЕСЬ, а не полагаемся на перенаправление в запуске. Перенаправление
    # срабатывает уже в потомке, а фоновый прогон начинает ждать своего «контроллер поднят»
    # раньше — и однажды находит его от ПРОШЛОГО прогона, после чего проверки идут по чужому
    # выводу. Проба при этом мигает: пять прогонов зелёных, шестой красный без правок.
    : > "$ENTRY_LOG"; : > "$ENTRY_CALLS"; : > "$ENTRY_OUT"
    cp "$1" "$ENTRY_DIR/control"
}

# entry_env — обстановка прогона: подставной докер первым в PATH, состояние во временном
# каталоге, ожидание короткое (иначе прогон стоил бы минут). Массивом, а не строкой: пути
# приходят из `mktemp`, и однажды в них окажется пробел.
ENTRY_ENV=()
entry_env() {
    ENTRY_ENV=(
        "PATH=$ENTRY_DIR:$PATH"
        "ENTRY_CALLS=$ENTRY_CALLS"
        "CONTROL_ADDR=127.0.0.1:$ENTRY_PORT"
        "CONTROL_KEYS=$ENTRY_STATE/keys"
        "DOCKER_CONFIG=$ENTRY_STATE/docker"
        "CONTROL_SHARE_RECIPE=$ENTRY_STATE/share/compose.yaml"
        "CONTROL_PULT=$ENTRY_STATE/pult"
        "CONTROL_REMOTE_SH=$ENTRY_STATE/deploy/remote.sh"
        "CONTROL_SOCKET=$ENTRY_SOCK"
        "CONTROL_WAIT=3"
        "CONTROL_KNOCK_TIMEOUT=1"
        "STUB_CONTROL_LOG=$ENTRY_LOG"
        "STUB_DOOR_IMAGE=$ENTRY_DOOR_IMAGE"
    )
}

# entry_case <ожидаемый код> <чем поднимать> <переменные…> — прогон до конца. Годится для
# отказов и для случаев, где контроллер выходит сам.
entry_case() {
    local want="$1" binar="$2"; shift 2
    entry_prepare "$binar"; entry_env
    local got=0
    env "${ENTRY_ENV[@]}" "$@" "$ENTRY_DIR/control-entry.sh" > "$ENTRY_OUT" 2>&1 || got=$?
    [ "$got" = "$want" ] && ok "код возврата $got" \
        || bad "код возврата $got, ждали $want" "$(tail -n 8 "$ENTRY_OUT")"
}

# entry_live <переменные…> — поднять точку входа в фоне НАСТОЯЩИМ контроллером и дождаться
# итогового «контроллер поднят». Возврат 1 — не дождались; сценарий это назовёт сам.
entry_live() {
    entry_prepare "$BIN"; entry_env
    env "${ENTRY_ENV[@]}" "$@" "$ENTRY_DIR/control-entry.sh" > "$ENTRY_OUT" 2>&1 &
    ENTRY_PID=$!
    local n=0
    while [ "$n" -lt 60 ]; do
        grep -q 'контроллер поднят' "$ENTRY_OUT" 2>/dev/null && return 0
        kill -0 "$ENTRY_PID" 2>/dev/null || return 1
        sleep 0.5; n=$((n + 1))
    done
    return 1
}

entry_stop() {
    [ -n "$ENTRY_PID" ] || return 0
    kill -TERM "$ENTRY_PID" 2>/dev/null || true
    ENTRY_CODE=0
    wait "$ENTRY_PID" 2>/dev/null || ENTRY_CODE=$?
    ENTRY_PID=""
}

# want_entry_code <код> <имя> — точка входа обязана отказать ИМЕННО этим кодом и вести
# куда-то дальше. Стережём код, а не формулировку (`WORLD2` 4.2).
want_entry_code() {
    local want="$1" name="$2" ways
    if ! grep -q "^CONTROL-REFUSAL: $want\$" "$ENTRY_OUT"; then
        local got; got="$(sed -n 's/^CONTROL-REFUSAL: //p' "$ENTRY_OUT" | head -n1 || true)"
        bad "$name" "ждали отказ $want, получили «${got:-отказа нет вовсе}»" "$(tail -n 8 "$ENTRY_OUT")"
        return
    fi
    ways="$(grep -c '^  выход: ' "$ENTRY_OUT" || true)"
    [ "${ways:-0}" -ge 2 ] || { bad "$name" "у отказа $want выходов меньше двух ($ways) — это диагноз, а не отказ"; return; }
    ok "$name → $want"
}
want_no_entry_code() {
    if grep -q '^CONTROL-REFUSAL: ' "$ENTRY_OUT"; then
        bad "$1" "отказ там, где его быть не должно:" "$(grep '^CONTROL-REFUSAL: ' "$ENTRY_OUT")"
    else
        ok "$1"
    fi
}
entry_said()     { grep -qF -- "$1" "$ENTRY_OUT" && ok "$2" || bad "$2" "в выводе точки входа нет «$1»:" "$(tail -n 12 "$ENTRY_OUT")"; }
entry_not_said() { grep -qF -- "$1" "$ENTRY_OUT" && bad "$2" "точка входа сказала «$1» — а это неправда:" "$(tail -n 12 "$ENTRY_OUT")" || ok "$2"; }
entry_called()   { grep -qF -- "$1" "$ENTRY_CALLS" && ok "$2" || bad "$2" "в журнале вызовов докера нет «$1»:" "$(cat "$ENTRY_CALLS")"; }
entry_started()  { grep -q 'контроллер запущен' "$ENTRY_LOG" && ok "$1" || bad "$1" "контроллер не запускался, а должен был"; }
entry_not_started() {
    grep -q 'контроллер запущен' "$ENTRY_LOG" \
        && bad "$1" "контроллер запустили ДО проверок — отказ пришёл бы поверх работающего пульта" \
        || ok "$1"
}
# Сырая ошибка оболочки в выводе — это не наш язык и не наш формат. Та же находка ревью
# `WORLD2-121`, что и у соседа: сломанное значение обязано отказывать НАШИМИ словами.
entry_bez_syrykh() {
    local syr
    syr="$(grep -nE 'Illegal number|integer expression|not found|syntax error|unexpected' "$ENTRY_OUT" || true)"
    [ -z "$syr" ] && ok "в выводе нет сырых ошибок оболочки — говорим своим языком" \
                  || bad "в вывод уехала ошибка оболочки" "$syr"
}

# ------------------------------------------------------------------ сборка
part "собираю контроллер"
if (cd "$HERE" && go build -o "$BIN" ./cmd/control) 2>"$TMP/build.log"; then
    ok "бинарь собран"
else
    bad "сборка" "$(tail -3 "$TMP/build.log")"
    printf '\nсобрать не вышло — дальше проверять нечего\n' >&2
    exit 1
fi
mk_fakes
mk_share
mk_machine
mk_up_stub
mk_entry

# ------------------------------------------------------------------ зелёный
green() {
    part "ЗЕЛЁНЫЙ — что контроллер обязан уметь"
    rm -f "$TMP/refuse"
    if ! [ -x "$TMP/share" ]; then
        skip "весь разговор со скоупом" \
            "подставная раздача не собралась: $(tail -2 "$TMP/share-build.log" 2>/dev/null)" \
            "она крошечная и лежит рядом с пробой: cd $TMP && go build -o share share.go"
        return
    fi
    # Раздачи по адресу ещё НЕТ — так и выглядит машина юзера до заведения скоупа.
    # Поднимет её подъём по рецепту, когда его позовёт контроллер.
    share_stop; rm -f "$SHARE_FILE"
    start_server || { bad "подъём контроллера" "процесс не поднялся"; return; }

    call GET /api/me
    want_refusal not-signed-in "до входа «кто я» отвечает «не входил»"

    call GET /api/resources
    want_refusal not-signed-in "до входа территорий не существует"

    # ┌─────────────────────────────────────────────────────────────────────────────────┐
    # │ ЗАВЕДЕНИЕ СКОУПА — ЭТО ПОДЪЁМ РАЗДАЧИ НА НАЗВАННОЙ МАШИНЕ, а не запись у себя.    │
    # │ Юзер называет ДВЕ ПАРЫ: машину (адрес, креды, имя участка) и сам скоуп (адрес     │
    # │ раздачи и пароль). Слияние этих пар — известная ошибка, на ней выросла мёртвая    │
    # │ `WORLD2-77` (`WORLD2` 3.4, «Два адреса, и путать их дорого»).                     │
    # └─────────────────────────────────────────────────────────────────────────────────┘
    zavesti
    want_status 201 "скоуп заводится ТАМ, где будет лежать"
    want_has '"created":true' "заведение сказало, что личность заведена"
    if grep -q -- "remote.sh add vps --addr world@10.8.0.5 --recipe $SHARE_RECIPE" "$CALLS"; then
        ok "раздача поднята РЕЦЕПТОМ и готовым подъёмом — своего зона не пишет"
    else
        bad "подъём раздачи" "в журнале вызовов нет подъёма раздачи рецептом:" "$(cat "$CALLS")"
    fi

    # ЛИЧНОСТЬ ЛЕЖИТ В РАЗДАЧЕ, А НЕ НА КОНТРОЛЛЕРЕ (`WORLD2-124`). Смотрим в ФАЙЛ, который
    # держит раздача, а не в ответ: ответ можно собрать из памяти, файл — нельзя.
    if grep -q '"имя": *"егор"' "$SHARE_FILE" 2>/dev/null; then
        ok "личность легла В РАЗДАЧУ по адресу — контроллер её у себя не держит"
    else
        bad "личность в раздаче" "в $SHARE_FILE её нет:" "$(cat "$SHARE_FILE" 2>/dev/null | head -3)"
    fi
    # Формат состояния — РОЗЕТКА мира (`WORLD2` 3.4): версия первым полем и четыре раздела.
    for razdel in '"формат"' '"личность"' '"ключи"' '"территории"' '"поля"'; do
        if grep -q -- "$razdel" "$SHARE_FILE"; then
            ok "в файле состояния есть раздел $razdel"
        else
            bad "формат состояния" "в файле нет раздела $razdel — это не форма мира:" "$(head -3 "$SHARE_FILE")"
        fi
    done
    # Креды машины уехали В СКОУП, в связку ключей: дальше они берутся оттуда, а не от
    # юзера. Руками он назвал их ровно один раз — когда скоупа ещё не было.
    if grep -q -- '-----ключ-----' "$SHARE_FILE"; then
        ok "креды машины записались в скоуп — дальше берутся оттуда"
    else
        bad "креды в скоупе" "их там нет:" "$(cat "$SHARE_FILE" | head -3)"
    fi
    # Пароль скоупа закрывает РАЗДАЧУ и внутри файла не лежит: запирать ключ внутри замка
    # нельзя (`WORLD2` 3.4). Он уезжает подъёму значением — там ему и место.
    if grep -q -- "$SHARE_PASS" "$SHARE_FILE"; then
        bad "пароль внутри файла" "пароль скоупа лежит в состоянии — это ключ, запертый внутри замка"
    else
        ok "пароля скоупа внутри файла состояния нет"
    fi

    call GET /api/me
    want_has '"name":"егор"' "«кто я» отвечает той же личностью"
    want_has "$SHARE_URL" "скоуп назван своим АДРЕСОМ, а не путём на машине"

    call GET /api/fields
    want_has '"fields":[]' "поля пусты, но список есть"

    call POST /api/fields '{"name":"дом"}'
    want_status 201 "поле заводится"
    want_has 'не поднимается' "сказано вслух, что поле пока не поднимается"
    if grep -q '"дом"' "$SHARE_FILE"; then
        ok "поле уехало В СКОУП, а не осталось в памяти контроллера"
    else
        bad "поле в скоупе" "в файле состояния его нет"
    fi

    call GET /api/resources
    want_has '"name":"vps"' "машина, на которой подняли раздачу, стала первой территорией"
    # РЕСУРС — МАШИНА, А НЕ «МАШИНА С ДВЕРЬЮ» (`WORLD2-131`). Что на ней стоит — отдельный
    # ответ: список вещей плюс отдельный вопрос «отвечает ли сама машина».
    want_has '"reach":"отвечает"' "территория говорит про СЕБЯ — отвечает она или молчит"
    want_has '"things":[{"name":"world"' "что стоит на территории — СПИСОК вещей, а не одно поле про дверь"
    case "$BODY" in
        *'"here":true'*) bad "ресурс «здесь»" "машина контроллера снова в списке личности — контроллер это времянка, а не территория юзера: $BODY" ;;
        *) ok "машины контроллера в списке территорий нет — она не участок юзера" ;;
    esac
    case "$BODY" in
        *'"door"'*) bad "поле двери" "в ответе снова одно поле про дверь — вторая вещь опять потребует правки кода: $BODY" ;;
        *) ok "поля «дверь» в ответе больше нет" ;;
    esac

    # Контексты докера — ПРОИЗВОДНОЕ от скоупа: они поднялись при входе, из территорий.
    if grep -qx 'world-vps' "$CTXFILE"; then
        ok "контекст территории поднят из скоупа"
    else
        bad "контекст из скоупа" "контекста world-vps нет:" "$(cat "$CTXFILE")"
    fi

    call GET /api/recipes
    want_has '"name":"door"' "чем поднимать — отдельный список, и дверь в нём один из рецептов"

    call POST /api/resources '{"name":"vps2","addr":"world@10.8.0.6","creds":{"kind":"key","value":"-----ключ-----"}}'
    want_status 201 "территория заводится"
    if grep -q 'remote.sh add vps2 --addr world@10.8.0.6' "$CALLS"; then
        ok "подъём вещи позван ГОТОВЫЙ, своего зона не пишет"
    else
        bad "готовый подъём" "в журнале вызовов нет remote.sh add:" "$(cat "$CALLS")"
    fi
    # Рецепт называется ВСЕГДА. Умолчание принадлежит команде соседа, а не нашему вызову:
    # смени он умолчание — контроллер молча поднимал бы другую вещь.
    if grep -q -- "remote.sh add vps2 .*--recipe $TMP/compose.yaml" "$CALLS"; then
        ok "подъём позван С РЕЦЕПТОМ — вещь названа, а не взята из умолчания соседа"
    else
        bad "рецепт в вызове" "подъём позван без --recipe:" "$(cat "$CALLS")"
    fi
    if grep -q '"имя": *"vps2"' "$SHARE_FILE"; then
        ok "участок записался В СКОУП — список территорий это личность, а не машина"
    else
        bad "участок в скоупе" "в файле состояния его нет:" "$(cat "$SHARE_FILE" | head -5)"
    fi
    if [ -f "$KEYS/world-vps2" ] && grep -q "IdentityFile $KEYS/world-vps2" "$KEYS/config" 2>/dev/null; then
        ok "ключ лёг в связку и назван в config — докер возьмёт его оттуда"
    else
        bad "ключ в связке" "ключа или блока в config нет: $(ls "$KEYS" 2>/dev/null | tr '\n' ' ')"
    fi

    call DELETE /api/resources/vps2
    want_status 200 "территория снимается"
    want_has '"left"' "снятие называет, что осталось на той машине"
    if grep -q '"имя": *"vps2"' "$SHARE_FILE"; then
        bad "участок после снятия" "он остался в скоупе — список врёт про то, что у юзера есть"
    else
        ok "участок ушёл из скоупа вместе с вещью"
    fi
    if [ ! -f "$KEYS/world-vps2" ]; then
        ok "ключ снятой территории не пережил её"
    else
        bad "след после снятия" "ключ остался в связке"
    fi

    # ┌─────────────────────────────────────────────────────────────────────────────────┐
    # │ ГЛАВНАЯ ПРОВЕРКА СТУПЕНИ (`WORLD2-132`, пункт 6 приёмки): ВЫШЕЛ, ВОШЁЛ ДРУГОЙ     │
    # │ ЛИЧНОСТЬЮ — ВИДНЫ ЕЁ ТЕРРИТОРИИ, А НЕ ЧУЖИЕ.                                     │
    # │                                                                                  │
    # │ Если после смены личности видно чужое, значит состояние осело в контроллере, и    │
    # │ «личность» не значит ничего. Остальные проверки без этой ничего не доказывают.    │
    # └─────────────────────────────────────────────────────────────────────────────────┘
    part "ЗЕЛЁНЫЙ — выход и вход другой личностью: своё, а не чужое"
    call DELETE /api/session
    want_status 200 "выход состоялся"
    if grep -q '"имя": *"егор"' "$SHARE_FILE"; then
        ok "выход не тронул скоуп — он лежит там, где лежал"
    else
        bad "выход тронул скоуп" "личность в раздаче изменилась, а выход её трогать не вправе"
    fi
    if [ -f "$KEYS/world-vps" ] || [ -s "$CTXFILE" ]; then
        bad "времянки пережили выход" \
            "ключи: $(ls "$KEYS" 2>/dev/null | tr '\n' ' ')" "контексты: $(cat "$CTXFILE" | tr '\n' ' ')"
    else
        ok "выход снял времянки: ни ключей, ни контекстов прежней личности"
    fi
    call GET /api/me
    want_refusal not-signed-in "после выхода метка больше не пускает"

    # Другая личность — другая раздача по другому адресу. Ровно так это и выглядит у
    # юзера: скоуп определяется АДРЕСОМ, и двух «тех же самых» скоупов не бывает (`3.4`).
    share_start "$(lichnost маша home:world@10.8.0.9)" \
        || { bad "вторая раздача" "не поднялась"; return; }
    вход
    want_status 200 "вход другой личностью по тому же адресу"
    want_has '"name":"маша"' "вошли под другой личностью"

    call GET /api/resources
    want_has '"name":"home"' "видны СВОИ территории"
    case "$BODY" in
        *'"name":"vps"'*) bad "чужая территория" "вошедший видит территории ПРЕЖНЕЙ личности — состояние осело в контроллере: $BODY" ;;
        *) ok "территорий прежней личности не видно — контроллер пуст" ;;
    esac
    if grep -qx 'world-home' "$CTXFILE" && ! grep -qx 'world-vps' "$CTXFILE"; then
        ok "контексты переехали вместе с личностью: свои есть, чужих нет"
    else
        bad "контексты после смены личности" "в реестре докера:" "$(cat "$CTXFILE")"
    fi

    # Возвращаем первую личность: дальше проба работает с ней.
    share_start "$(lichnost егор vps:world@10.8.0.5)" || { bad "первая раздача" "не поднялась"; return; }
    вход

    # ┌─────────────────────────────────────────────────────────────────────────────────┐
    # │ ВТОРАЯ ВЕЩЬ ПОДНИМАЕТСЯ ТЕМ ЖЕ ПУТЁМ (`WORLD2-131`) — БЕЗ единой правки кода и    │
    # │ БЕЗ пересборки образа. Кладём второй рецепт в каталог (это и есть то, что сделает │
    # │ хозяин машины монтированием) и зовём его по имени.                                │
    # └─────────────────────────────────────────────────────────────────────────────────┘
    part "ЗЕЛЁНЫЙ — вторая вещь: положили рецепт, и она поднимается"
    printf 'name: весы\n' > "$RECIPES/весы.yaml"

    call GET /api/recipes
    want_has '"name":"весы"' "положенный рецепт появился в списке сам — перечня вещей в коде нет"

    call POST /api/resources '{"name":"vps3","addr":"world@10.8.0.7","creds":{"kind":"key","value":"ключ"},"recipe":"весы"}'
    want_status 201 "вторая вещь поднимается тем же путём"
    if grep -q -- "remote.sh add vps3 .*--recipe $RECIPES/весы.yaml" "$CALLS"; then
        ok "подъём позван ИМЕННО ТЕМ рецептом, который назвал человек"
    else
        bad "рецепт второй вещи" "в журнале вызовов нет подъёма по рецепту весов:" "$(cat "$CALLS")"
    fi

    call DELETE "/api/resources/vps3?recipe=весы"
    want_status 200 "вторая вещь снимается тем же рецептом"
    if grep -q -- "remote.sh drop vps3 --recipe $RECIPES/весы.yaml" "$CALLS"; then
        ok "снятие названо рецептом — своего реестра вещей зона не заводит"
    else
        bad "рецепт при снятии" "снятие пошло без рецепта:" "$(cat "$CALLS")"
    fi

    call POST /api/resources '{"name":"vps4","addr":"world@10.8.0.8","creds":{"kind":"key","value":"ключ"},"recipe":"часы"}'
    want_refusal no-such-recipe "рецепта, которого нет, контроллер не выдумывает"

    # ┌─────────────────────────────────────────────────────────────────────────────────┐
    # │ КРЕДЫ ДВУХ ВИДОВ: КЛЮЧ ЛИБО ПАРОЛЬ, КАК В PuTTY (`WORLD2-141`).                   │
    # │                                                                                   │
    # │ Пароль транспортом не становится — докер ходит системным ssh, а тот пароля не      │
    # │ берёт. Паролем контроллер заходит ОДИН раз и кладёт публичный ключ юзера в         │
    # │ `~/.ssh/authorized_keys` машины; дальше всё по ключу. Юзер терминал не открывает.  │
    # │                                                                                   │
    # │ Проверяется это на НАСТОЯЩЕМ ssh-обмене с подставной машиной, и смотрим мы В ЕЁ    │
    # │ ФАЙЛ: ответ контроллера мог бы сказать что угодно.                                 │
    # └─────────────────────────────────────────────────────────────────────────────────┘
    part "ЗЕЛЁНЫЙ — креды двух видов: ключ либо пароль"
    if ! [ -x "$TMP/машина" ]; then
        skip "весь путь пароля" \
            "подставная машина не собралась: $(tail -2 "$TMP/machine-build.log" 2>/dev/null)" \
            "она лежит в зоне: cd $HERE && go build ./internal/creds/sshtest/cmd"
    else
        machine_start || bad "подставная машина" "не поднялась на :$MACHINE_PORT"
        call POST /api/resources "{\"name\":\"vpsp\",\"addr\":\"world@127.0.0.1:$MACHINE_PORT\",\"creds\":{\"kind\":\"password\",\"value\":\"$MACHINE_PASS\"}}"
        want_status 201 "машина с ПАРОЛЕМ и без ключа заводится территорией"

        # Смотрим в файл МАШИНЫ: ровно одна строка, и она подписана.
        if [ "$(machine_keys | grep -c . || true)" = "1" ]; then
            ok "на машине ровно одна строка в ~/.ssh/authorized_keys"
        else
            bad "строки на машине" "их не одна:" "$(machine_keys)"
        fi
        case "$(machine_keys)" in
            *world-control*) ok "строка подписана — человек поймёт, откуда она и чем её убрать" ;;
            *) bad "подпись" "строка без подписи:" "$(machine_keys)" ;;
        esac

        # ПОВТОРНЫЙ ЗАХОД НЕ ПЛОДИТ СТРОК: ключ юзера один на скоуп, второй раз кладётся то
        # же самое. Иначе в чужом файле копился бы десяток наших ключей.
        call POST /api/resources "{\"name\":\"vpsp2\",\"addr\":\"world@127.0.0.1:$MACHINE_PORT\",\"creds\":{\"kind\":\"password\",\"value\":\"$MACHINE_PASS\"}}"
        want_status 201 "вторая территория на той же машине заводится"
        if [ "$(machine_keys | grep -c . || true)" = "1" ]; then
            ok "повторный заход строк НЕ плодит — ключ тот же"
        else
            bad "вторая строка" "заход дописал ещё одну:" "$(machine_keys)"
        fi

        # В СКОУП УШЁЛ КЛЮЧ, А НЕ ПАРОЛЬ.
        if grep -q 'PRIVATE KEY' "$SHARE_FILE"; then
            ok "в скоуп лёг ключ, который завёл контроллер"
        else
            bad "ключ в скоупе" "его там нет:" "$(head -c 300 "$SHARE_FILE")"
        fi
        # ┌─────────────────────────────────────────────────────────────────────────────┐
        # │ ПАРОЛЬ НЕ ЖИВЁТ НИГДЕ: ни в скоупе, ни в связке, ни в журнале, ни в ответе.  │
        # │ Это четыре разных места, и проверяются они по отдельности — утёкший в одно   │
        # │ из них пароль не менее утёкший оттого, что в трёх других его нет.            │
        # └─────────────────────────────────────────────────────────────────────────────┘
        if grep -qF -- "$MACHINE_PASS" "$SHARE_FILE"; then
            bad "пароль в скоупе" "он утёк в состояние юзера"
        else
            ok "пароля нет в скоупе"
        fi
        if grep -rqF -- "$MACHINE_PASS" "$KEYS" 2>/dev/null; then
            bad "пароль в связке" "он утёк в связку контроллера"
        else
            ok "пароля нет в связке контроллера"
        fi
        if grep -qF -- "$MACHINE_PASS" "$TMP/server.log"; then
            bad "пароль в журнале" "он утёк в журнал — а журнал читают и хранят"
        else
            ok "пароля нет в журнале"
        fi
        if grep -qF -- "$MACHINE_PASS" "$TMP/body"; then
            bad "пароль в ответе" "он вернулся человеку обратно"
        else
            ok "пароля нет в ответе ручки"
        fi

        # ЦЕНА НАЗВАНА: контроллер написал в ЧУЖУЮ машину, и человек об этом узнаёт.
        if grep -q 'authorized_keys' "$TMP/server.log"; then
            ok "в журнале сказано, что контроллер пишет в ~/.ssh/authorized_keys чужой машины"
        else
            bad "молчание про запись" "человек узнал бы об этом от кого угодно, только не от нас"
        fi

        # ПРЕЖНИЙ ПУТЬ ПО КЛЮЧУ НЕ СЛОМАН.
        call POST /api/resources '{"name":"vpsk","addr":"world@10.8.0.55","creds":{"kind":"key","value":"-----ключ-----"}}'
        want_status 201 "машина с КЛЮЧОМ заводится как раньше"
        if grep -q -- '-----ключ-----' "$SHARE_FILE"; then
            ok "названный юзером ключ лёг в скоуп как есть — за него никто не ходил на машину"
        else
            bad "путь по ключу" "ключ юзера в скоуп не попал:" "$(head -c 300 "$SHARE_FILE")"
        fi
    fi

    # ┌─────────────────────────────────────────────────────────────────────────────────┐
    # │ ПОДЪЁМ ИДЁТ СВОЕЙ СБОРКОЙ (`WORLD2-130`). Выпуск возит тройку одной сборки под     │
    # │ одним `sha-<7>`; подъём эту гарантию выбрасывал — рецепты по умолчанию говорят     │
    # │ `latest`, и контроллер одной сборки ставил юзеру дверь другой. Молча.              │
    # │                                                                                    │
    # │ Здесь стерегутся три состояния и они РАЗНЫЕ: пин сработал · версии нет · имя       │
    # │ названо снаружи. Плюс главное — при каждом подъёме сказано, ЧЕМ поднято.           │
    # └─────────────────────────────────────────────────────────────────────────────────┘
    part "ЗЕЛЁНЫЙ — подъём идёт СВОЕЙ сборкой и говорит, чем поднято"

    call POST /api/resources '{"name":"vps5","addr":"world@10.8.0.5","creds":{"kind":"key","value":"ключ"}}'
    want_status 201 "территория заводится"
    pin_u_vyzova "remote.sh add vps5 " "WORLD_IMAGE=ghcr.io/omnifield/world:sha-abc1234" \
        "дверь поднимается СВОЕЙ сборкой — пин уехал подстановкой рецепта"
    # Строка ищется НЕ по словам, а по тому, что она обязана назвать: рецепт, который
    # поднимали, и образ, которым поднято. Проверка на слово «поднимаю» уже зеленела на
    # стартовой строке («вещи ПОДНИМАЮТся») — то есть стерегла не то, что думала.
    # Строка ищется у ТОГО САМОГО подъёма — по имени территории, которую поднимали. Искать
    # «где-нибудь в журнале» нельзя: снятие говорит о себе тем же образом, и порча «подъём
    # замолчал» зеленела бы на соседней строке про другое действие.
    if grep -F -- "vps5" "$TMP/server.log" | grep -q 'sha-abc1234'; then
        ok "в журнале названо, ЧЕМ поднято — рецептом и образом, который и правда поехал"
    else
        bad "подъём молчит" "в журнале контроллера нет строки про то, чем поднято:" "$(tail -3 "$TMP/server.log")"
    fi
    # Тег берётся КАК ЕСТЬ: значение штампа — готовый тег, и собирать его второй раз нельзя.
    if grep -q 'sha-sha-' "$CALLS"; then
        bad "тег собран заново" "приставка разъехалась с той, что ставит выпуск:" "$(grep '^env ' "$CALLS" | tail -2)"
    else
        ok "тег подставлен как есть — второй раз не собран"
    fi
    # Раздача — та же тройка одной сборки, и пинится тем же путём.
    pin_u_vyzova "remote.sh add vps --addr world@10.8.0.5" "SHARE_IMAGE=ghcr.io/omnifield/world-share:sha-abc1234" \
        "раздача скоупа поднимается той же сборкой, что и контроллер"
    # СВОЯ вещь юзера пином не пинится: её образ не наш, и нашего тега у него нет. Журнал
    # обязан сказать это как есть, а не выдать намерение за сделанное.
    call POST /api/resources '{"name":"vps6","addr":"world@10.8.0.6","creds":{"kind":"key","value":"ключ"},"recipe":"весы"}'
    if grep -q 'весы:1.2' "$TMP/server.log"; then
        ok "чужой рецепт пином не пинится, и сказано, чем он поднят на самом деле"
    else
        bad "чужой рецепт" "в журнале не названо, чем поднята своя вещь юзера:" "$(tail -3 "$TMP/server.log")"
    fi

    # ВЕРСИИ НЕТ — законное состояние (собран не выпуском, а на месте). Тогда вещь идёт тем,
    # что назовёт рецепт, и МОЛЧАТЬ об этом нельзя: для разработчика зоны норма, для юзера нет.
    SRV_VERSION=""
    share_start "$(lichnost егор vps:world@10.8.0.5)" || { bad "раздача" "не поднялась"; return; }
    start_server || { bad "подъём контроллера" "процесс не поднялся"; return; }
    вход
    call POST /api/resources '{"name":"vps7","addr":"world@10.8.0.7","creds":{"kind":"key","value":"ключ"}}'
    # Значение пустое — это и есть «не пинили»: подставной подъём печатает обе подстановки
    # всегда, поэтому смотрим на то, ЧТО стоит после знака равенства, а не на само имя.
    if grep -F -A1 -- "remote.sh add vps7 " "$CALLS" | grep -q 'WORLD_IMAGE=[^ ]'; then
        bad "пин без версии" "штампа нет, а контроллер всё равно чем-то пинит:" "$(grep -F -A1 -- "remote.sh add vps7 " "$CALLS")"
    else
        ok "без штампа контроллер не выдумывает версию"
    fi
    if grep -F -- "vps7" "$TMP/server.log" | grep -q 'latest'; then
        ok "«версии не знаю, беру latest» сказано ВСЛУХ у самого подъёма, а не проглочено"
    else
        bad "молчание про версию" "у подъёма нет строки про то, чем поднято без штампа:" "$(tail -3 "$TMP/server.log")"
    fi
    if grep -q 'версии своей не знаю' "$TMP/server.log"; then
        ok "и сказано это ещё при подъёме контроллера, а не только при первом добавлении"
    else
        bad "стартовая строка" "контроллер не назвал своё состояние версии при подъёме:" "$(head -5 "$TMP/server.log")"
    fi

    # ЯВНОЕ ИМЯ ОТ ЮЗЕРА СТАРШЕ ПИНА (`WORLD2` 0.1): мир не решает за юзера.
    SRV_VERSION="sha-abc1234"; SRV_WORLD_IMAGE="своя/дверь:мояверсия"
    share_start "$(lichnost егор vps:world@10.8.0.5)" || { bad "раздача" "не поднялась"; return; }
    start_server || { bad "подъём контроллера" "процесс не поднялся"; return; }
    вход
    call POST /api/resources '{"name":"vps8","addr":"world@10.8.0.8","creds":{"kind":"key","value":"ключ"}}'
    pin_u_vyzova "remote.sh add vps8 " "WORLD_IMAGE=своя/дверь:мояверсия" \
        "имя, названное хозяином снаружи, СТАРШЕ пина — оно и поехало"
    if grep -q 'старше' "$TMP/server.log"; then
        ok "и сказано вслух, что имя названо снаружи"
    else
        bad "молчание про чужое имя" "человек будет искать, почему поднялось не то:" "$(tail -3 "$TMP/server.log")"
    fi
    SRV_WORLD_IMAGE=""
    stop_server

    part "ЗЕЛЁНЫЙ — пульт раздаётся тем же адресом, что и ручки"
    put_pult
    start_server || { bad "подъём контроллера" "процесс не поднялся"; return; }

    call GET /
    want_status 200 "корень отдаёт пульт"
    want_has "лицо мира" "по корню приехала страница, а не JSON"

    call GET /assets/index-XY.js
    want_status 200 "файлы сборки отдаются"

    # Граница ручек и пульта. Перехватит пульт ручку — машина получит страницу вместо
    # JSON; перехватит ручка пульт — человек увидит JSON вместо лица.
    call GET /api/me
    want_refusal not-signed-in "ручка не перехвачена пультом"
    call GET /api/мее
    want_refusal unknown-endpoint "опечатка в имени ручки осталась промахом машины"
    call GET /api
    want_status 404 "/api отвечает отказом, а не перенаправлением"

    # Настоящий пульт зоны `web`, если он собран рядом. Заглушка проверяет раздачу, а этот
    # кусок — что раздаётся ИМЕННО ТО, что собирает соседняя зона.
    if [ -s "$ROOT/web/dist/index.html" ] && ! grep -q '/src/main.tsx' "$ROOT/web/dist/index.html"; then
        # Каталог пульта на время подменяем настоящим и возвращаем обратно: остальные
        # проверки работают с заглушкой, и оставить им чужой каталог значит проверять
        # потом не то, что думаешь.
        saved_pult="$PULT"; PULT="$ROOT/web/dist"
        start_server || { bad "подъём с настоящим пультом" "процесс не поднялся"; PULT="$saved_pult"; return; }
        call GET /
        want_status 200 "настоящий собранный пульт зоны web раздаётся"
        asset="$(sed -n 's/.*src="\(\/assets\/[^"]*\)".*/\1/p' "$ROOT/web/dist/index.html" | head -1)"
        if [ -n "$asset" ]; then
            call GET "$asset"
            want_status 200 "файл настоящей сборки ($asset) отдаётся"
        else
            skip "файл настоящей сборки" "в index.html не нашлось ссылки на /assets/ — нечего дёрнуть"
        fi
        PULT="$saved_pult"
    else
        skip "настоящий собранный пульт зоны web" \
            "рядом нет web/dist со сборкой — проверена только раздача заглушки" \
            "собрать: ./deploy/build.sh --only-web (для этого шага нужно поле)"
    fi
    stop_server

    # ┌─────────────────────────────────────────────────────────────────────────────────┐
    # │ ШОВ С ЗОНОЙ `deploy` — СТОРОЖИМ ТО, ЧЕМ ПОЛЬЗУЕМСЯ СЕЙЧАС.                        │
    # │                                                                                  │
    # │ Здесь сторожилось имя контейнера двери (`DOOR=world-door`): зона его повторяла,   │
    # │ и разъезд молча ослепил бы «жив ли». Константы больше нет ни у соседа, ни у нас — │
    # │ имя контейнера принадлежит РЕЦЕПТУ (`WORLD2-131`). Сторож не погашен, а            │
    # │ переставлен: зелёная проверка, которой больше нечего проверять, хуже              │
    # │ отсутствующей — она учит, что шов цел, когда его уже нет (`WORLD2` 4.2).           │
    # │                                                                                  │
    # │ Что осталось швом на самом деле: приставка контекста (по ней мы узнаём СВОИ       │
    # │ ресурсы), ключ `--recipe` (им контроллер называет вещь), место рецепта двери (по   │
    # │ нему он его находит), метка проекта компоуза (по ней мы читаем список вещей) и     │
    # │ `WORLD_PORT` (его шлём мы, а читает его теперь РЕЦЕПТ, а не подъём).               │
    # └─────────────────────────────────────────────────────────────────────────────────┘
    part "ЗЕЛЁНЫЙ — шов с зоной deploy"
    if [ -f "$ROOT/deploy/remote.sh" ]; then
        if grep -q '^PREFIX=world-' "$ROOT/deploy/remote.sh"; then
            ok "приставка контекста та же, что у соседа (world-)"
        else
            bad "приставка контекста" "в deploy/remote.sh она уже другая — наш список ресурсов ослепнет"
        fi
        # Ключом `--recipe` контроллер называет ВЕЩЬ. Пропади он у соседа — подъём ответил
        # бы «непонятный ключ», и добавление ресурса отказывало бы на ровном месте.
        if grep -q -- '--recipe)' "$ROOT/deploy/remote.sh"; then
            ok "ключ --recipe у соседа есть — им контроллер называет, ЧТО поднимает"
        else
            bad "ключ --recipe" "в deploy/remote.sh его больше нет — контроллер зовёт подъём ключом, которого тот не знает"
        fi
        # Рецепт двери контроллер находит ПО ФОРМУЛЕ «рядом с подъёмом» — той же, по
        # которой сосед берёт своё умолчание. Разъедься она — дверь перестала бы
        # находиться, и отказ был бы про рецепт, а не про раскладку образа.
        if grep -q 'RECIPE="\$HERE/compose.yaml"' "$ROOT/deploy/remote.sh"; then
            ok "рецепт двери лежит рядом с подъёмом — формула та же, что у нас"
        else
            bad "место рецепта двери" "у соседа оно другое — контроллер ищет дверь не там"
        fi
        # Метку проекта читают ДВОЕ: сосед (тома и «вещи там») и мы (список вещей на
        # ресурсе). Это правило докера, а не наше знание о вещах, — но читаем мы его вместе.
        if grep -q 'com.docker.compose.project' "$ROOT/deploy/remote.sh"; then
            ok "список вещей у обоих читается по метке проекта компоуза"
        else
            bad "метка проекта" "сосед читает вещи иначе — наши списки вещей разъедутся"
        fi
    else
        skip "шов с deploy (ключи и приставка)" "deploy/remote.sh рядом нет — сверять не с чем"
    fi
    # `WORLD_PORT` контроллер ШЛЁТ, а читает его теперь РЕЦЕПТ двери, а не подъём. Шов
    # переехал вместе со значением, и сторожить его надо там, где он теперь.
    if [ -f "$ROOT/deploy/compose.yaml" ]; then
        if grep -q 'WORLD_PORT' "$ROOT/deploy/compose.yaml"; then
            ok "хост-порт двери читает её РЕЦЕПТ — тем же именем, каким мы его шлём"
        else
            bad "WORLD_PORT" "рецепт двери его больше не читает — CONTROL_DOOR_PORT уезжает в пустоту"
        fi
        # ПИН едет той же дорогой: подстановкой имени образа в рецепте (`WORLD2-130`).
        # Пропади она у соседа — контроллер снова ставил бы юзеру дверь подвижным latest, и
        # заметить это было бы нечем: подъём прошёл бы успешно.
        if grep -q 'WORLD_IMAGE' "$ROOT/deploy/compose.yaml"; then
            ok "рецепт двери берёт имя образа подстановкой — ею мы и пиним свою сборку"
        else
            bad "WORLD_IMAGE" "рецепт двери подстановку потерял — пин уедет в пустоту, дверь поднимется latest"
        fi
    else
        skip "шов с deploy (WORLD_PORT)" "deploy/compose.yaml рядом нет — сверять не с чем"
    fi

    # Зашитого имени двери в работающих строках зоны не осталось — а вернуться оно может
    # тихо, одной «удобной» константой. Это ровно тот сорт правки, ради которого заход и
    # делался: имя контейнера принадлежит рецепту, и знать его контроллеру неоткуда.
    # Образец собран из кусков, иначе проба нашла бы саму себя.
    dver="world-""door"
    zashito="$( { grep -rn "$dver" "$HERE"/*.sh "$HERE"/*.yaml "$HERE"/Dockerfile "$HERE"/cmd "$HERE"/internal || true; } \
                | grep -vE '^[^:]+:[0-9]+:[[:space:]]*(#|//)' || true)"
    [ -z "$zashito" ] && ok "зашитого имени контейнера двери в зоне нет — вещь описывается рецептом" \
                      || bad "имя двери вернулось в зону" "вторая вещь снова потребует правки кода:" "$zashito"
    # ┌─────────────────────────────────────────────────────────────────────────────────┐
    # │ ШОВ С ЗОНОЙ `share` — ОДНО ЗНАЧЕНИЕ И ОДНА ФОРМА.                                 │
    # │                                                                                  │
    # │ Пароль скоупа контроллер отдаёт подъёму раздачи её же именем (`SHARE_PASSWORD`):  │
    # │ значение принадлежит рецепту соседа, а не нам. Пропади оно там — раздача встала    │
    # │ бы БЕЗ пароля либо не встала вовсе, а контроллер об этом не узнал бы.              │
    # └─────────────────────────────────────────────────────────────────────────────────┘
    if [ -f "$ROOT/share/compose.yaml" ]; then
        if grep -q 'SHARE_PASSWORD' "$ROOT/share/compose.yaml"; then
            ok "пароль скоупа рецепт раздачи читает тем же именем, каким мы его шлём"
        else
            bad "SHARE_PASSWORD" "рецепт раздачи его больше не читает — пароль уедет в пустоту"
        fi
        # Тот же шов, что у двери: пин раздачи едет её подстановкой. Раздача — часть той же
        # тройки одной сборки, и поднимать её подвижным тегом значит терять гарантию выпуска.
        if grep -q 'SHARE_IMAGE' "$ROOT/share/compose.yaml"; then
            ok "рецепт раздачи берёт имя образа подстановкой — ею мы пиним и её"
        else
            bad "SHARE_IMAGE" "рецепт раздачи подстановку потерял — раздача поднимется latest при пине контроллера"
        fi
        # Форма стыковки со скоупом принадлежит МИРУ (`WORLD2` 3.4, `0.3`), а не зоне: две
        # ручки по одному адресу и `Basic`. Сторожим то, чем пользуемся: контроллер зовёт
        # корень раздачи, и путь внутри неё не заводится ни у нас, ни у соседа.
        if grep -q '8070' "$ROOT/share/compose.yaml"; then
            ok "порт раздачи в её рецепте назван — по нему юзер и собирает адрес скоупа"
        else
            bad "порт раздачи" "в рецепте соседа его нет — адрес скоупа собирать не из чего"
        fi
    else
        skip "шов с share (пароль и порт)" "share/compose.yaml рядом нет — сверять не с чем"
    fi

    # Второй шов: откуда образ контроллера берёт собранный пульт. Путь принадлежит зоне
    # `deploy` (`build.sh`), у нас он повторён в Dockerfile и в up.sh.
    if [ -f "$ROOT/deploy/build.sh" ]; then
        if grep -q 'OUT="\$HERE/.build/web-dist"' "$ROOT/deploy/build.sh"; then
            ok "каталог собранного пульта тот же, что у соседа (deploy/.build/web-dist)"
        else
            bad "каталог пульта" "в deploy/build.sh он уже другой — образ контроллера уедет без лица"
        fi
    else
        skip "шов с deploy (каталог пульта)" "deploy/build.sh рядом нет — сверять не с чем"
    fi

    # Третий шов, и он появился вместе с готовым образом: имя из `compose.yaml` читают
    # ДВОЕ — подъём (откуда тянуть) и выпуск соседа (куда отдавать). Не неси оно реестра,
    # докер достроил бы своё умолчание, а выпуск отдал бы образ по `WORLD_REGISTRY` — и
    # юзер тянул бы из места, куда мы не публикуем. Правило распознавания реестра не наше,
    # а докера: первый сегмент — хост, если это `localhost` либо в нём есть точка или
    # двоеточие.
    control_image="$(sed -n 's/^[[:space:]]*image:[[:space:]]*//p' "$HERE/compose.yaml" | head -n1)"
    # Разворачиваем `${ПЕРЕМЕННАЯ:-умолчание}`: сверяем именно УМОЛЧАНИЕ — юзер приходит
    # без переменных, и решает за него оно.
    case "$control_image" in
        '${'*':-'*'}') control_image="${control_image#*:-}"; control_image="${control_image%\}}" ;;
    esac
    if [ -z "$control_image" ]; then
        bad "имя образа в файле запуска" "в control/compose.yaml не нашлось строки image:"
    else
        case "${control_image%%/*}" in
            localhost|*.*|*:*) ok "имя образа несёт реестр — подъём знает, откуда тянуть, а выпуск куда отдавать" ;;
            *) bad "реестр в имени образа" \
                   "имя «$control_image» реестра не несёт: докер достроит своё умолчание, а выпуск публикует по WORLD_REGISTRY — юзер потянет не оттуда" ;;
        esac
    fi

    up_green
    entry_green
    komanda_green
}

# ------------------------------------------------------------------ зелёный: точка входа
entry_green() {
    part "ЗЕЛЁНЫЙ — точка входа образа: пульт встаёт одной командой, без репозитория"
    if [ ! -S "$ENTRY_SOCK" ]; then
        skip "точка входа образа целиком" \
            "подставной докер-сокет не завёлся — без него проверяется не то, что у юзера" \
            "сокет делает крошечная программа на go рядом с пробой; посмотри, чем упал: cd $TMP && go run sokket.go ./s"
        return
    fi

    # Штатный подъём — НАСТОЯЩИМ контроллером: только он и правда отвечает на стук, а стук
    # это половина требования («запущен» и «отвечает» — разные вещи).
    if entry_live STUB_DOOR_HERE=1; then
        ok "точка входа подняла контроллер и дождалась его ответа"
        want_no_entry_code "штатный подъём проходит без отказов"
        entry_said "контроллер отвечает" "ответ подтверждён стуком, а не выведен из запуска"
        entry_said "демон отвечает" "власть над машиной проверена ДО подъёма"
        entry_said "пульт на месте" "лицо для человека найдено"
        entry_said "образ двери $ENTRY_DOOR_IMAGE на этой машине есть" "образ двери проверен по имени соседа"
        entry_said "docker port world-control" "сказано, чем смотреть публикацию, которой изнутри не видно"
        entry_bez_syrykh
        # Имя образа двери СПРОШЕНО у файла запуска соседа, а не написано копией. Именно
        # эта копия уже разъезжалась молча (`WORLD2-121` сменил имя двери).
        entry_called "compose -f $ENTRY_STATE/deploy/compose.yaml config --images" \
            "имя образа двери спрошено у файла запуска соседа"
        entry_not_said "$ENTRY_STAROE_IMYA" "прежнего имени двери в выводе не осталось"

        # Сигнал: `docker stop` обязан ЗАКРЫВАТЬ контроллер, а не добивать по таймауту.
        # Точка входа — оболочка, и первым процессом сигнал приходит ей.
        entry_stop
        case "$ENTRY_CODE" in
            0|143) ok "по TERM точка входа вышла сама (код $ENTRY_CODE) — docker stop не ждёт таймаута" ;;
            *)     bad "выход по сигналу" "код $ENTRY_CODE" "докер счёл бы это падением контроллера" ;;
        esac
        if "$ENTRY_DIR/control-knock.sh" "$ENTRY_PORT" >/dev/null 2>&1; then
            bad "контроллер после сигнала" "он всё ещё отвечает — TERM до него не дошёл, процесс осиротел"
        else
            ok "контроллер после сигнала не отвечает — TERM дошёл до него, а не съеден оболочкой"
        fi
    else
        entry_stop
        bad "штатный подъём точки входа" "итогового «контроллер поднят» не дождались:" "$(tail -n 12 "$ENTRY_OUT")"
    fi

    # ┌─────────────────────────────────────────────────────────────────────────────────┐
    # │ ЧЕМ ПОДНЯТ КОНТРОЛЛЕР — на пути ЮЗЕРА, а не только разработчика (`WORLD2-130`).   │
    # │                                                                                  │
    # │ Юзер поднимает пульт одной командой и читает ровно этот вывод. Полезное, лежащее  │
    # │ там, куда он не ходит, для него не существует — на этом узел и заведён.           │
    # └─────────────────────────────────────────────────────────────────────────────────┘
    part "ЗЕЛЁНЫЙ — точка входа называет, чем поднят контроллер"
    if entry_live STUB_DOOR_HERE=1 WORLD_VERSION=sha-abc1234; then
        entry_said "версия образа sha-abc1234" "точка входа назвала свою сборку — без сокета, из своего окружения"
        # И образ двери проверяется ТЕМ ЖЕ именем, каким его повезёт подъём. Проверь она
        # `latest`, пока подъём идёт пином, — предупреждение врало бы в обе стороны.
        entry_said "ghcr.io/omnifield/world:sha-abc1234" "образ двери проверен ПИНОМ — тем, чем и поедет"
        entry_not_said "ghcr.io/omnifield/world:latest на этой машине" "подвижный тег не выдан за то, что повезут"
    else
        bad "подъём со штампом" "точка входа не довела подъём до конца:" "$(tail -n 12 "$ENTRY_OUT")"
    fi
    entry_stop

    # Штампа нет — законное состояние, и оно НАЗЫВАЕТСЯ, а не выдумывается.
    if entry_live STUB_DOOR_HERE=1; then
        entry_said "версии не знаю" "без штампа сказано честно, а unknown не выдуман"
        entry_said "latest" "и названо, чем вещи поднимутся вместо пина"
    else
        bad "подъём без штампа" "точка входа не довела подъём до конца:" "$(tail -n 12 "$ENTRY_OUT")"
    fi
    entry_stop

    # Явное имя от хозяина СТАРШЕ пина — и точка входа говорит об этом там же, где человек
    # будет искать, почему поднялось не то.
    if entry_live STUB_DOOR_HERE=1 WORLD_VERSION=sha-abc1234 WORLD_IMAGE=своя/дверь:мояверсия; then
        entry_said "имя образа названо снаружи" "названо, что слово хозяина старше пина"
    else
        bad "подъём с явным именем" "точка входа не довела подъём до конца:" "$(tail -n 12 "$ENTRY_OUT")"
    fi
    entry_stop

    part "ЗЕЛЁНЫЙ — чего изнутри не хватает: сказано, а не спрятано"
    # Образа двери на машине нет. Это ПРЕДУПРЕЖДЕНИЕ, а не отказ: контроллер на пустой
    # машине законен — вход в скоуп и список ресурсов работают и без образа двери.
    if entry_live; then
        want_no_entry_code "без образа двери контроллер всё равно поднимается"
        entry_said "образа двери $ENTRY_DOOR_IMAGE на этой машине нет" "названо, чего не хватает — и под ПРАВИЛЬНЫМ именем"
        entry_said "no-image" "названо, каким кодом откажет добавление ресурса"
        entry_said "docker pull $ENTRY_DOOR_IMAGE" "и чем это чинится, командой"
    else
        bad "подъём без образа двери" "точка входа не довела подъём до конца:" "$(tail -n 12 "$ENTRY_OUT")"
    fi
    entry_stop

    # Имени образа не спросить вовсе (файл запуска соседа не читается). Своей копии имени в
    # зоне нет намеренно — значит здесь честное «не проверил», а не выдуманное имя.
    if entry_live STUB_DOOR_IMAGE= ; then
        want_no_entry_code "неспрошенное имя образа двери не останавливает подъём"
        entry_said "не спросить" "сказано, что имя образа двери спросить не вышло"
        entry_not_said "на этой машине нет" "не выдумано, будто образа нет: мы не знаем, чего искать"
    else
        bad "имя образа двери не спросить" "точка входа не довела подъём до конца:" "$(tail -n 12 "$ENTRY_OUT")"
    fi
    entry_stop

    # Пульт перекрыт монтированием — ручки работают, и это названо.
    if entry_live STUB_DOOR_HERE=1 CONTROL_PULT="$TMP/пульта-нет"; then
        want_no_entry_code "без пульта контроллер поднимается — ручки не при чём"
        entry_said "no-pult" "названо, каким кодом ответит корень"
    else
        bad "подъём без пульта" "точка входа не довела подъём до конца:" "$(tail -n 12 "$ENTRY_OUT")"
    fi
    entry_stop

    # Состояние. Не отказ — у контроллера три разных тома под разные части работы, и
    # каждая называет СВОЙ будущий отказ. Молча проглотить нельзя: ключ, который некуда
    # положить, всплыл бы на первом добавлении ресурса.
    entry_prepare "$BIN"; entry_env
    chmod 500 "$ENTRY_STATE/keys"
    env "${ENTRY_ENV[@]}" STUB_DOOR_HERE=1 "$ENTRY_DIR/control-entry.sh" > "$ENTRY_OUT" 2>&1 &
    ENTRY_PID=$!
    n=0
    while [ "$n" -lt 60 ] && ! grep -q 'контроллер поднят' "$ENTRY_OUT" 2>/dev/null; do sleep 0.5; n=$((n + 1)); done
    entry_said "не пишется" "непишущаяся связка названа"
    entry_said "no-keyring" "и назван код, которым откажет добавление ресурса"
    want_no_entry_code "непишущаяся связка не отменяет подъёма — вход и раздача от неё не зависят"
    chmod 700 "$ENTRY_STATE/keys"
    entry_stop

    # Права связки: ssh молча не возьмёт ключ из каталога, открытого всем. Тихая поломка,
    # которая стоит человеку часа, — значит она обязана быть названной.
    entry_prepare "$BIN"; entry_env
    chmod 755 "$ENTRY_STATE/keys"
    env "${ENTRY_ENV[@]}" STUB_DOOR_HERE=1 "$ENTRY_DIR/control-entry.sh" > "$ENTRY_OUT" 2>&1 &
    ENTRY_PID=$!
    n=0
    while [ "$n" -lt 60 ] && ! grep -q 'контроллер поднят' "$ENTRY_OUT" 2>/dev/null; do sleep 0.5; n=$((n + 1)); done
    entry_said "ssh" "слишком открытая связка названа именем того, кто на ней споткнётся"
    entry_stop
}

# ------------------------------------------------------------------ зелёный: команда юзера
# Команда `docker run` напечатана в `control/README.md` и должна работать КОПИРОВАНИЕМ, а не
# пересказом. Проверяем не текст вокруг, а саму команду: имя образа, тома, публикацию в
# петлю и — главное — НАЛИЧИЕ докер-сокета. У двери он в команде запрещён, у контроллера
# обязателен: он и есть руки юзера (`WORLD2` 3.7), и власть даёт хозяин явно.
komanda_green() {
    part "ЗЕЛЁНЫЙ — команда юзера в README: одна, с сокетом, и не разъехалась с файлом запуска"

    local komanda
    komanda="$(awk '
        /^docker run/ { blok = $0; v = ($0 ~ /\\$/); if (!v) sdat(); next }
        v             { blok = blok "\n" $0; if ($0 !~ /\\$/) { v = 0; sdat() } }
        function sdat() { if (blok ~ /--name world-control/) print blok; blok = "" }
    ' "$HERE/README.md")"
    if [ -z "$komanda" ]; then
        bad "команда подъёма в README" "команды docker run с world-control нет — юзеру нечего копировать"
        return
    fi
    ok "команда подъёма контроллера в README есть"

    case "$komanda" in
        *"-v /var/run/docker.sock:/var/run/docker.sock"*)
            ok "сокет в команде ЕСТЬ и виден глазами — власть даёт хозяин, а не образ себе сам" ;;
        *)  bad "сокет в команде" "его нет — контроллер поднимется и откажет no-socket: без сокета он не руки, а лицо" ;;
    esac

    local nado
    for nado in "--name world-control" "-p 127.0.0.1:" "-v world-control-keys:/root/.ssh" \
                "-v world-control-docker:/root/.docker" "restart"; do
        case "$komanda" in
            *"$nado"*) ok "в команде есть «$nado»" ;;
            *) bad "в команде нет «$nado»" "скопированная команда дала бы не тот пульт, что описан рядом" ;;
        esac
    done

    # Публикация ТОЛЬКО В ПЕТЛЮ. У контроллера есть сокет: кто дотянулся до него, тот
    # распоряжается машиной. Наружу хозяин открывает сознательно, но умолчанием такого не
    # делают — и уж точно не в команде, которую копируют не глядя.
    case "$komanda" in
        *"-p 0.0.0.0:"*|*"-p 8090:8090"*)
            bad "публикация в команде" "порт открыт наружу машины — вместе с ним наружу открыт и докер-сокет" ;;
        *)  ok "публикация только в петлю — наружу контроллер умолчанием не торчит" ;;
    esac

    # Имя образа в команде и в файле запуска — одно и то же. Разъедутся: юзер тянет одно,
    # разработчик поднимает другое, и «почему у меня иначе» становится вопросом без ответа.
    local imya
    imya="$(sed -n 's/^[[:space:]]*image:[[:space:]]*//p' "$HERE/compose.yaml" | head -n1)"
    case "$imya" in
        '${'*':-'*'}') imya="${imya#*:-}"; imya="${imya%\}}" ;;
    esac
    case "$komanda" in
        *"$imya"*) ok "образ в команде тот же, что в файле запуска: $imya" ;;
        *) bad "образ в команде разъехался с файлом запуска ($imya)" "$komanda" ;;
    esac

    # ТОМА ПОД СКОУП В КОМАНДЕ БОЛЬШЕ НЕТ И БЫТЬ НЕ ДОЛЖНО (`WORLD2-124`): личность лежит по
    # адресу, а не на контроллере. Останься он в команде — человек снова решил бы, что снос
    # контроллера уносит его самого, и раскладка врала бы про устройство.
    case "$komanda" in
        *"/scope"*|*"world-control-scope"*)
            bad "том скоупа в команде" "личность снова лежит томом контроллера — снёс контроллер, потерял себя" ;;
        *)  ok "тома под скоуп в команде нет — контроллер это времянка" ;;
    esac

    # ТОМА В КОМАНДЕ И В ФАЙЛЕ ЗАПУСКА — ОДНИ И ТЕ ЖЕ. Иначе путь юзера и путь разработчика
    # молча заведут два разных состояния на одной машине, и человек, перешедший с одного на
    # другой, увидит пустую связку при живых ключах рядом.
    local tom
    for tom in world-control-keys world-control-docker; do
        if grep -q "name: $tom" "$HERE/compose.yaml"; then
            ok "том $tom назван поимённо и в файле запуска"
        else
            bad "том $tom" "в compose.yaml он собирается из имени проекта — состояние путей разъедется"
        fi
    done

    # Раскладка состояния объявлена в ЧЕТЫРЁХ местах, и это цена одной команды. Разъедутся
    # молча — контроллер будет искать ключи не там, куда их положил докер.
    part "ЗЕЛЁНЫЙ — раскладка состояния не разъехалась по файлам"
    local put
    for put in /root/.ssh /root/.docker; do
        if grep -q "mkdir -p .*$put\|mkdir -p /root/.ssh /root/.docker" "$HERE/Dockerfile" \
           && grep -qF "$put" "$HERE/compose.yaml" && grep -qF "$put" "$HERE/control-entry.sh"; then
            ok "каталог $put объявлен и в образе, и в файле запуска, и в точке входа"
        else
            bad "каталог $put" "он объявлен не везде — состояние ляжет не туда, где его ищут"
        fi
    done

    # Каталог рецептов — то место, куда хозяин машины кладёт свои вещи. Он назван ЯВНО в
    # образе и в файле запуска: разъедься эти два имени, положенный рецепт не нашёлся бы, и
    # отказ был бы про имя вещи, а не про раскладку.
    if grep -q 'CONTROL_RECIPES="/opt/world/recipes"' "$HERE/Dockerfile" \
       && grep -q 'mkdir -p /opt/world/recipes' "$HERE/Dockerfile" \
       && grep -q 'CONTROL_RECIPES: /opt/world/recipes' "$HERE/compose.yaml"; then
        ok "каталог рецептов заведён в образе и назван так же в файле запуска"
    else
        bad "каталог рецептов" "он объявлен не везде — положенный хозяином рецепт не найдётся"
    fi

    # РЕЦЕПТ РАЗДАЧИ СКОУПА — им контроллер поднимает личность юзера. Назван он в тех же
    # трёх местах, что и всё остальное: образ кладёт файл, файл запуска и точка входа знают
    # тот же путь. Разъедься они — «завести скоуп» отказало бы кодом no-share-recipe при
    # целом образе, и чинить человек пошёл бы не туда.
    if grep -q 'COPY share/compose.yaml /opt/world/share/compose.yaml' "$HERE/Dockerfile" \
       && grep -q 'CONTROL_SHARE_RECIPE="/opt/world/share/compose.yaml"' "$HERE/Dockerfile" \
       && grep -q 'CONTROL_SHARE_RECIPE: /opt/world/share/compose.yaml' "$HERE/compose.yaml" \
       && grep -q 'SHARE_RECIPE=' "$HERE/control-entry.sh"; then
        ok "рецепт раздачи скоупа лежит в образе и назван так же в файле запуска и в точке входа"
    else
        bad "рецепт раздачи" "он объявлен не везде — «завести скоуп» откажет при целом образе"
    fi

    # Здоровье объявлено ОДИН раз и в образе: у юзера файла запуска нет вовсе.
    if grep -q '^HEALTHCHECK' "$HERE/Dockerfile"; then
        ok "здоровье объявлено в ОБРАЗЕ — оно доедет до того, кто поднял docker run"
    else
        bad "здоровье в образе" "HEALTHCHECK-а в Dockerfile нет: у юзера, поднявшего одной командой, здоровья не будет вовсе"
    fi
    if grep -q 'healthcheck:' "$HERE/compose.yaml"; then
        bad "второе объявление здоровья" "оно и в образе, и в файле запуска — две правды разъедутся молча"
    else
        ok "второго объявления здоровья в файле запуска нет"
    fi
    # Стук один на двоих — точка входа и HEALTHCHECK. Копия разошлась бы молча.
    if grep -q 'control-knock.sh' "$HERE/Dockerfile" && grep -q 'KNOCK=' "$HERE/control-entry.sh"; then
        ok "стучатся оба одним файлом — второй копии стука нет"
    else
        bad "стук" "HEALTHCHECK и точка входа стучатся по-разному — «здоров по докеру, мёртв по нам» станет вопросом без ответа"
    fi

    # Прежнее имя двери в РАБОТАЮЩИХ строках зоны не осталось: с ним предупреждение врало бы
    # при правильно названном образе на месте — то есть учило бы не читать предупреждения.
    #
    # Смотрим только туда, где имя было бы УПОТРЕБЛЕНО: непустые не-комментарные строки
    # скриптов, файл запуска, Dockerfile. Прозу README не сканируем нарочно — там прежнее имя
    # названо как ИСТОРИЯ («было `omnifield/world:dev`, стало …»), и запрет на его упоминание
    # заставил бы стирать причину правки. Что README не врёт про имя, стережёт проверка выше:
    # оно спрашивается у файла запуска соседа, а не пишется где-либо копией.
    #
    # Образец собран из кусков там, где объявлен (`ENTRY_STAROE_IMYA`), иначе проба нашла бы
    # саму себя: она тоже лежит в зоне и тоже кончается на `.sh`.
    local staroe obrazec
    obrazec="$ENTRY_STAROE_IMYA"
    staroe="$( { grep -n "$obrazec" "$HERE"/*.sh "$HERE"/*.yaml "$HERE"/Dockerfile || true; } \
               | grep -vE '^[^:]+:[0-9]+:[[:space:]]*#' || true)"
    [ -z "$staroe" ] && ok "прежнего имени двери ($obrazec) в работающих строках зоны не осталось" \
                     || bad "прежнее имя двери" "оно врёт с WORLD2-121:" "$staroe"
}

# ------------------------------------------------------------------ зелёный: подъём
up_green() {
    part "ЗЕЛЁНЫЙ — подъём берёт ГОТОВЫЙ образ, а не собирает на машине юзера"
    mk_up_root; reset_stub          # пульта в этом корне НЕТ — как у юзера
    run_up
    [ "$UP_CODE" = 0 ] \
        && ok "подъём прошёл БЕЗ собранного пульта — поле ушло с пути юзера" \
        || bad "подъём без пульта" "код выхода $UP_CODE:" "$UP_OUT"
    want_called "pull $STUB_IMAGE" "образ притянут ЯВНО, отдельной командой"
    want_called "up -d --no-build" "компоузу сборка запрещена ключом, а не понадеялись"
    want_not_called "up -d --build" "на пути юзера ничего не собирается"

    # Главное требование захода: молчаливого «поднял то, что валялось» быть не должно.
    # Спрошено у ЖИВОГО контейнера, а не выведено из своих намерений.
    want_called "inspect --format {{.Config.Image}} world-control" "спрошено у контейнера, чем он поднят"
    want_said "$STUB_IMAGE" "сказано, каким тегом поднят контроллер"
    want_said "sha256:podstava" "и каким digest'ом — без него latest неотличим от latest недельной давности"

    # Digest'а может не быть вовсе (образ собран здесь и в реестре не лежит). Это тоже
    # ответ, и он обязан прозвучать: пустая строка читается как «всё в порядке».
    STUB_DIGEST=""
    run_up
    [ "$UP_CODE" = 0 ] || bad "подъём без digest'а" "код выхода $UP_CODE:" "$UP_OUT"
    want_said "digest" "образ без digest'а не выдаётся за притянутый — сказано и про это"
    reset_stub

    # Пара, которую поодиночке не поймать: «полей у образа нет» и «образа нет вовсе»
    # выглядят одинаково — пустым ответом. Первое значит «собран здесь», второе — «тем
    # образом, которым поднят контейнер, на машине уже никто не располагает». Выдать одно
    # за другое значит соврать ровно в той строке, ради которой всё это и печатается.
    STUB_IMAGE_GONE=1
    run_up
    want_not_said "собран на этой машине" "исчезнувший образ не выдан за собранный здесь"
    want_said "уже нет" "про исчезнувший образ сказано, что спросить не у чего"
    reset_stub

    part "ЗЕЛЁНЫЙ — реестр не спрашивали, и это названо вслух"
    STUB_HAVE_LOCAL=1; STUB_OFFLINE=1
    run_up
    [ "$UP_CODE" = 0 ] && ok "офлайн поднимает то, что уже лежит на машине" \
        || bad "офлайн" "код выхода $UP_CODE:" "$UP_OUT"
    want_not_called "pull " "реестр действительно не спрашивали"
    want_said "CONTROL_OFFLINE" "человеку сказано, ЧЕМ он это получил — тихого офлайна не бывает"
    reset_stub

    part "ЗЕЛЁНЫЙ — сборка осталась ключом разработчика зоны"
    mk_up_root; put_up_pult; reset_stub
    run_up --build
    [ "$UP_CODE" = 0 ] && ok "--build поднимает из исходников" \
        || bad "--build" "код выхода $UP_CODE:" "$UP_OUT"
    want_called "up -d --build" "по --build компоуз собирает"
    want_not_called "pull " "и реестр при этом не трогает"
}

# ------------------------------------------------------------------ красный
red() {
    part "КРАСНЫЙ — нарочные поломки: контроллер обязан назвать код, причину и выход"
    rm -f "$TMP/refuse"
    if ! [ -x "$TMP/share" ]; then
        skip "красный прогон по скоупу" "подставная раздача не собралась — ломать нечего"
        return
    fi
    share_start "$(lichnost егор vps:world@10.8.0.5)" || { bad "подставная раздача" "не поднялась"; return; }
    start_server || { bad "подъём контроллера" "процесс не поднялся"; return; }

    call POST /api/session '{"addr":"просто-слово","password":"тайна"}'
    want_refusal bad-address "адрес скоупа не похож на HTTP-адрес"

    # ПРЕЖНЯЯ ФОРМА АДРЕСА — путь на машине контроллера — обязана краснеть, а не молча
    # заводить личность у себя: ровно этим контроллер и держал чужое состояние.
    call POST /api/session '{"addr":"/scope/я","password":"тайна"}'
    want_refusal bad-address "путь на машине больше не адрес скоупа — личность лежит по адресу"

    call POST /api/session ''
    want_refusal no-body "тело пустое"

    call POST /api/session '{"addr":"http://x:8070/","адрес":"y"}'
    want_refusal bad-body "лишнее поле не проглатывается молча"

    # ┌─────────────────────────────────────────────────────────────────────────────────┐
    # │ ХОД «ЗАВЕСТИ ЗДЕСЬ» ОБЯЗАН КРАСНЕТЬ (`WORLD2` 3.7, `WORLD2-129`). Он не           │
    # │ игнорируется молча — молча проглоченное поле выглядит как сработавшее, — а        │
    # │ отказывает. Это порча номер 4 из приёмки ступени.                                 │
    # └─────────────────────────────────────────────────────────────────────────────────┘
    call POST /api/session "{\"addr\":\"$SHARE_URL\",\"create\":true,\"name\":\"я\"}"
    want_refusal bad-body "хода «завести здесь» не существует — поле create краснеет"

    call POST /api/session "{\"addr\":\"$SHARE_URL\"}"
    want_refusal no-password "вход без пароля не бывает: доступ к личности доказывается кредами"

    call POST /api/session "{\"addr\":\"$SHARE_URL\",\"password\":\"не-та\"}"
    want_refusal bad-password "неверный пароль назван причиной, а не «что-то пошло не так»"

    call PUT /api/session '{}'
    want_refusal wrong-method "не тот глагол"

    call GET /api/такой-ручки-нет
    want_refusal unknown-endpoint "неизвестная ручка"

    вход
    # Имя переменной ЛАТИНСКОЕ: bash не поддерживает не-ASCII в именах и говорит
    # «not a valid identifier» — та же грабля, о которой предупреждают шапки соседей.
    local real_token="$TOKEN"
    TOKEN=чужая-метка
    call GET /api/me
    want_refusal not-signed-in "чужая метка не пускает"
    TOKEN="$real_token"

    call POST /api/scope "{\"scope\":{\"addr\":\"$SHARE_URL\",\"password\":\"$SHARE_PASS\"},\"identity\":{\"name\":\"другой\"}}"
    want_refusal scope-exists "личность не заводится поверх личности"

    # ВТОРОЙ УЧАСТОК С ЗАНЯТЫМ ИМЕНЕМ — отказ МЕХАНИКИ (`WORLD2` 2.3, `2.5` п. 11): на имени
    # стоит адрес локации, и молчаливая перезапись строки столкнула бы адреса. Порча 5.
    : > "$CALLS"
    call POST /api/resources '{"name":"vps","addr":"world@10.8.0.99","creds":{"kind":"key","value":"другой"}}'
    want_refusal name-taken "участок с занятым именем не заводится"
    if grep -q 'remote.sh' "$CALLS"; then
        bad "отказ после действия" "до отказа успели тронуть машину — проверять надо ДО докера:" "$(cat "$CALLS")"
    else
        ok "отказ по имени случился ДО всякого докера"
    fi

    call POST /api/fields '{"name":"дом"}' >/dev/null
    call POST /api/fields '{"name":"дом"}'
    want_refusal field-exists "поле не заводится дважды"

    call POST /api/resources '{"name":"../побег","addr":"world@10.8.0.5","creds":{"kind":"key","value":"ключ"}}'
    want_refusal bad-name "имя территории, уводящее из связки, не принимается"

    # ┌─────────────────────────────────────────────────────────────────────────────────┐
    # │ ВИД КРЕД НАЗЫВАЕТСЯ ЯВНО (`WORLD2-141`). «Не сказали» и «сказали не то» — разные  │
    # │ отказы: первое чинится тем, что человек выберет вид, второе — тем, что он назовёт │
    # │ существующий. Угадывать вид по виду строки нельзя вовсе: угаданный однажды примет │
    # │ ключ за пароль, и разбираться человек будет с отказом ssh, а не с нашей догадкой.  │
    # └─────────────────────────────────────────────────────────────────────────────────┘
    call POST /api/resources '{"name":"vps5","addr":"world@10.8.0.5"}'
    want_refusal no-creds-kind "вид кред не назван — и он не угадывается"

    call POST /api/resources '{"name":"vps5","addr":"world@10.8.0.5","creds":{"kind":"key"}}'
    want_refusal no-creds "вид назван, а самих кред нет"

    call POST /api/resources '{"name":"vps5","addr":"world@10.8.0.5","creds":{"kind":"колдовство","value":"х"}}'
    want_refusal bad-creds-kind "неизвестный вид кред назван отдельно от «не назван»"

    # Пароль не подошёл — отказ ДОСТУПА, и на машине после него ничего не появляется.
    if [ -x "$TMP/машина" ] && machine_start; then
        call POST /api/resources "{\"name\":\"vpsx\",\"addr\":\"world@127.0.0.1:$MACHINE_PORT\",\"creds\":{\"kind\":\"password\",\"value\":\"не-тот\"}}"
        want_refusal access-denied "неверный пароль машины назван причиной, а не «не получилось»"
        if [ -z "$(machine_keys)" ]; then
            ok "на машине после неверного пароля не появилось ничего"
        else
            bad "след на чужой машине" "там что-то записалось:" "$(machine_keys)"
        fi

        # НЕ ЗАПИСАЛОСЬ — ЭТО ОТКАЗ, А НЕ УСПЕХ. Дом машины закрыт на запись: `~/.ssh` не
        # завести, файла не создать. Контроллер обязан это ИЗМЕРИТЬ и сказать, а не вывести
        # успех из того, что шелл дошёл до конца (`WORLD2` 4.2 п. 5).
        chmod 500 "$MACHINE_HOME" 2>/dev/null || true
        call POST /api/resources "{\"name\":\"vpsz\",\"addr\":\"world@127.0.0.1:$MACHINE_PORT\",\"creds\":{\"kind\":\"password\",\"value\":\"$MACHINE_PASS\"}}"
        want_refusal key-not-installed "ключ не записался — сказано отказом, а не отчётом об успехе"
        chmod 700 "$MACHINE_HOME" 2>/dev/null || true
        machine_stop
    else
        skip "неверный пароль машины" "подставная машина не поднялась — ломать нечего"
    fi

    # Отказ соседа обязан доехать СВОИМ кодом: свой словарь тех же отказов разъехался бы
    # с его словарём на первой правке.
    printf 'access-denied' > "$TMP/refuse"
    call POST /api/resources '{"name":"vps2","addr":"world@10.8.0.5","creds":{"kind":"key","value":"ключ"}}'
    want_refusal access-denied "код подъёма доезжает своим, а не переписанным"
    want_has '"from":"deploy/remote.sh"' "названо, чей это отказ"
    if [ ! -f "$KEYS/world-vps2" ]; then
        ok "неудачный подъём не оставил ключ за собой"
    else
        bad "след после неудачи" "ключ остался в связке — вторая попытка пойдёт ключом-призраком"
    fi
    if grep -q '"имя": *"vps2"' "$SHARE_FILE"; then
        bad "след после неудачи" "участок записался в скоуп, хотя вещь не поднялась"
    else
        ok "неудачный подъём не записал участок в скоуп"
    fi
    rm -f "$TMP/refuse"

    # ┌─────────────────────────────────────────────────────────────────────────────────┐
    # │ ПО АДРЕСУ ЛЕЖИТ НЕ НАШ ФОРМАТ — порча 2 из приёмки. Отказ обязан назвать, ЧТО     │
    # │ приехало и ЧЕГО ЖДАЛИ: без этих двух вещей человеку нечего чинить (`0.3`, `2.3`). │
    # └─────────────────────────────────────────────────────────────────────────────────┘
    TOKEN=""
    printf '<!doctype html>совсем другая вещь' > "$SHARE_FILE"
    call POST /api/session "{\"addr\":\"$SHARE_URL\",\"password\":\"$SHARE_PASS\"}"
    want_refusal scope-broken "по адресу лежит не состояние — названо поломкой, а не «нет скоупа»"
    case "$BODY" in
        *doctype*) ok "отказ показал, ЧТО приехало" ;;
        *) bad "отказ по формату" "не сказано, что именно приехало:" "$BODY" ;;
    esac

    printf '{"формат":2,"личность":{"имя":"егор"}}' > "$SHARE_FILE"
    call POST /api/session "{\"addr\":\"$SHARE_URL\",\"password\":\"$SHARE_PASS\"}"
    want_refusal bad-format "чужая версия формата названа версией, а не поломкой"

    # РАЗДАЧА НЕДОСТУПНА — порча 3: отказ называет адрес и что делать, а не пустой экран.
    share_stop
    call POST /api/session "{\"addr\":\"$SHARE_URL\",\"password\":\"$SHARE_PASS\"}"
    case "$BODY" in
        *'"code":"no-answer"'*|*'"code":"no-route"'*|*'"code":"scope-silent"'*|*'"code":"scope-unreachable"'*)
            ok "недоступная раздача названа ступенью связи, а не общим «не получилось»" ;;
        *)  bad "недоступная раздача" "ждали ступень связи, получили:" "$BODY" ;;
    esac
    case "$BODY" in
        *"$SHARE_URL"*) ok "отказ назвал адрес, по которому не дозвонились" ;;
        *) bad "отказ без адреса" "человеку нечего проверять:" "$BODY" ;;
    esac
    stop_server

    part "КРАСНЫЙ — пульта нет и пульт не тот"
    rm -rf "$PULT"
    start_server || { bad "подъём контроллера" "процесс не поднялся"; return; }

    call GET /
    want_refusal no-pult "пульта в сборке нет — сказано кодом, а не пустой страницей"

    # Тот же отказ человеку в браузере: читаемым текстом, с тем же кодом в заголовке.
    browser GET /
    case "$BODY" in
        \{*) bad "отказ человеку" "в браузер приехал JSON:" "$BODY" ;;
        *no-pult*) ok "человеку в браузере отказ приехал читаемым текстом" ;;
        *)  bad "отказ человеку" "в тексте нет кода отказа:" "$BODY" ;;
    esac
    case "$HEADERS" in
        *"X-Control-Refusal: no-pult"*) ok "машинный код уехал заголовком" ;;
        *) bad "код в заголовке" "заголовка X-Control-Refusal нет:" "$(printf '%s' "$HEADERS" | head -3)" ;;
    esac
    stop_server

    # Исходник вместо сборки — отдельный отказ: он открывается и НЕ ПОКАЗЫВАЕТ НИЧЕГО,
    # и человек ищет поломку в мире, а не в раскладке.
    put_pult '<script type="module" src="/src/main.tsx"></script>'
    start_server || { bad "подъём контроллера" "процесс не поднялся"; return; }
    call GET /
    want_refusal pult-not-built "исходник зоны web вместо сборки назван отдельно"
    stop_server

    put_pult
    start_server || { bad "подъём контроллера" "процесс не поднялся"; return; }
    call GET /assets/
    want_refusal unknown-page "каталог не листается"
    case "$BODY" in
        *index-XY.js*) bad "список файлов" "перечень файлов образа уехал наружу: $BODY" ;;
        *) ok "перечня файлов в ответе нет" ;;
    esac

    # Обход пути. Проверка стережёт ИТОГ ВСЕЙ СТОЙКИ, а не одну мою строку: `..` чистят
    # оба слоя — маршрутизатор `net/http` (он отвечает перенаправлением на вычищенный путь)
    # и сама раздача. Поэтому она НЕ покраснеет, если убрать чистку только у раздачи, и
    # честно об этом говорит: за мою строку отвечает тест `TestОбходПутиНеРаботает`, где
    # раздача зовётся напрямую, мимо маршрутизатора. Молча выдавать это за проверку своего
    # кода нельзя — ровно такая ложная зелень уже стоила ревью соседней зоне (`WORLD2-96`).
    printf 'ключ юзера' > "$TMP/секрет"
    raw_get "/../$(basename "$TMP/секрет")"
    case "$BODY" in
        *"ключ юзера"*) bad "обход пути" "чтение ушло за каталог пульта — а прав у контроллера много" ;;
        *) ok "обход пути наружу не отдаёт чужой файл (стойка целиком: маршрутизатор + раздача)" ;;
    esac

    call POST /
    want_refusal wrong-method "в страницу не постучаться глаголом ручки"
    stop_server

    part "КРАСНЫЙ — инструмента нет вовсе"
    share_start "$(lichnost егор vps:world@10.8.0.5)" || { bad "подставная раздача" "не поднялась"; return; }
    start_server "$TMP/docker" "$TMP/нет-такого-подъёма" || { bad "подъём контроллера" "не поднялся"; return; }
    вход
    call POST /api/resources '{"name":"vps2","addr":"world@10.8.0.5","creds":{"kind":"key","value":"ключ"}}'
    want_refusal no-remote-tool "подъёма вещи нет в образе — сказано прямо"
    stop_server

    # Докера нет вовсе. Вход при этом обязан ОТКАЗАТЬ, а не притвориться удавшимся: без
    # докера времянки из скоупа не разложить, и territории останутся недостижимыми.
    start_server "$TMP/нет-такого-докера" "$TMP/remote.sh" || { bad "подъём контроллера" "не поднялся"; return; }
    TOKEN=""
    вход
    want_refusal no-docker "докера нет — сказано прямо, а не «список пуст»"
    stop_server

    up_red
    entry_red
}

# ------------------------------------------------------------------ красный: точка входа
entry_red() {
    part "КРАСНЫЙ — точка входа образа: отказывает кодом, причиной и выходом"

    # Значение с формой проверяется ДО подъёма. Находка ревью `WORLD2-121` у соседа:
    # сломанное ожидание не «подождало бы иначе», а объявило бы ЗДОРОВЫЙ контроллер
    # молчащим — то есть дало бы уверенный неверный диагноз.
    entry_case 1 "$ENTRY_DIR/zaglushka" CONTROL_WAIT=abc
    want_entry_code bad-value "CONTROL_WAIT не число — свой отказ, а не ложное «контроллер молчит»"
    entry_not_started "до запуска контроллера дело не дошло — проверки идут первыми"
    entry_said "CONTROL_WAIT=abc" "названо, ЧТО задано, а не «значение неверно»"
    entry_said "CONTROL_WAIT=120" "и что вместо"
    entry_bez_syrykh

    entry_case 1 "$ENTRY_DIR/zaglushka" CONTROL_WAIT=0
    want_entry_code bad-value "ждать нисколько — это ложный control-silent на живом контроллере"

    # ВЛАСТЬ НАД МАШИНОЙ. Без сокета контроллер поднялся бы и отвечал `no-docker` на каждый
    # осмысленный запрос — то есть выглядел бы работающим, не будучи им.
    entry_case 1 "$ENTRY_DIR/zaglushka" CONTROL_SOCKET="$TMP/сокета-нет"
    want_entry_code no-socket "сокета нет — отказ, а не молчаливый пульт без рук"
    entry_not_started "контроллер без власти над машиной не поднимается вовсе"
    entry_said "-v /var/run/docker.sock:/var/run/docker.sock" "в выходе названа сама строка команды"

    # Демона назвали снаружи — сокет в контейнере тогда ни при чём, и требовать его файл
    # значило бы отказывать на верной раскладке.
    entry_case 1 "$ENTRY_DIR/zaglushka" CONTROL_SOCKET="$TMP/сокета-нет" \
        DOCKER_HOST=ssh://world@10.8.0.5 STUB_CONTROL_LIFE=30
    want_entry_code control-silent "при названном DOCKER_HOST сокет в контейнере не спрашивается"
    entry_said "DOCKER_HOST=ssh://world@10.8.0.5" "и сказано, почему не спрашивается"

    entry_case 1 "$ENTRY_DIR/zaglushka" STUB_DAEMON_DEAD=1
    want_entry_code no-daemon "сокет отдан, а демон молчит — это другая поломка и другой выход"
    entry_not_started "молчащий демон останавливает подъём до контроллера"

    entry_case 1 "$ENTRY_DIR/zaglushka" CONTROL_DOCKER="$TMP/докера-нет"
    want_entry_code no-docker "докера нет в самом образе — сказано, что это поломка образа"

    # Контроллер вышел сам. Это НЕ `control-silent`: «умер» — диагноз точнее, чем «не
    # ответил», и порядок разбора у точки входа именно такой.
    entry_case 1 "$ENTRY_DIR/zaglushka" STUB_CONTROL_LIFE=0 STUB_CONTROL_CODE=3
    want_entry_code control-dead "контроллер вышел сам — назван своим кодом, а не молчанием"
    entry_said "код 3" "чужой код назван, а не спрятан за своим"

    # Контроллер жив, но не отвечает. Отказ обязан НЕ ОСТАВЛЯТЬ его сиротой: контейнер
    # переживёт точку входа, и `docker stop` потом добивал бы его по таймауту.
    entry_case 1 "$ENTRY_DIR/zaglushka" STUB_CONTROL_LIFE=30
    want_entry_code control-silent "молчащий контроллер назван молчащим, а не мёртвым"
    entry_started "контроллер при этом запускался"
    for _ in 1 2 3; do grep -q 'контроллер получил TERM' "$ENTRY_LOG" && break; sleep 1; done
    grep -q 'контроллер получил TERM' "$ENTRY_LOG" \
        && ok "молчащему контроллеру передан TERM — процесс не брошен" \
        || bad "след после отказа" "контроллер остался работать: точка входа вышла, а он живёт — это осиротевший процесс"

    # Адрес принадлежит контроллеру, а не нам: судить его мы не вправе, но и стучаться по
    # нему не умеем. Значит ожидание пропускается ВСЛУХ, а живой процесс не трогается —
    # убить его из-за того, что мы не умеем постучаться, было бы подменой причины.
    entry_case 0 "$ENTRY_DIR/zaglushka" CONTROL_ADDR=:http
    want_no_entry_code "негодный для стука адрес — не отказ: он принадлежит контроллеру"
    entry_said "не вывести" "сказано, что порт для стука не выводится"
    entry_said "здоровье контроллера НЕ проверено" "непроверенное названо непроверенным"
    grep -q 'контроллер получил TERM' "$ENTRY_LOG" \
        && bad "живой контроллер убит" "из-за значения, которое принадлежит ему же" \
        || ok "контроллер не тронут — он сам решит, годен ли ему такой адрес"

    # Чужая команда: `docker run … ls` и `--entrypoint` обязаны продолжать работать. Иначе
    # в образ, который нельзя открыть и посмотреть, придётся лезть живым подъёмом.
    entry_prepare "$ENTRY_DIR/zaglushka"; entry_env
    local got=0
    env "${ENTRY_ENV[@]}" "$ENTRY_DIR/control-entry.sh" printf 'чужая команда\n' > "$ENTRY_OUT" 2>&1 || got=$?
    [ "$got" = 0 ] && ok "чужая команда выполнена (код 0)" || bad "чужая команда" "код возврата $got"
    entry_said "чужая команда" "выполнилась именно она"
    entry_not_started "контроллер при этом не поднимался"
}

# ------------------------------------------------------------------ красный: подъём
up_red() {
    part "КРАСНЫЙ — образ не притянулся: названа СТУПЕНЬ, а не общее «не скачалось»"
    # Ступени чинят РАЗНЫЕ люди: дороги нет — хозяин машины, тега нет — тот, кто выпускает,
    # доступ закрыт — хозяин организации в реестре. Общий отказ отправил бы чинить наугад.
    mk_up_root

    reset_stub; STUB_PULL=noroute; run_up
    want_up_code no-registry "до реестра нет дороги"

    reset_stub; STUB_PULL=notag;   run_up
    want_up_code no-image-tag "реестр ответил, а такого образа у него нет"

    reset_stub; STUB_PULL=denied;  run_up
    want_up_code image-denied "доступ к образу закрыт"
    # Выход обязан быть КОМАНДОЙ, а не советом «разберись»: первый выпуск создаёт приватный
    # пакет, и снаружи это выглядит как «образа нет».
    want_said "docker login" "в выходе названо, чем открывают закрытый доступ"

    reset_stub; STUB_PULL=other;   run_up
    want_up_code pull-failed "причина не разобрана — сказано прямо, а не подогнано под ступень"

    # Пара «реестр недоступен + образ на машине есть» — выход обязан вести к нему. Отказ,
    # который упирается в тупик при живом образе рядом, это половина отказа.
    reset_stub; STUB_PULL=noroute; STUB_HAVE_LOCAL=1; run_up
    want_said "CONTROL_OFFLINE=1" "при недоступном реестре выход ведёт к образу, который уже лежит здесь"

    part "КРАСНЫЙ — офлайн и ключи не по адресу"
    reset_stub; STUB_OFFLINE=1; STUB_HAVE_LOCAL=; run_up
    want_up_code no-image-local "офлайн без образа на машине — поднимать нечего"

    reset_stub; STUB_OFFLINE=true; run_up
    want_up_code bad-value "значение, которого мы не понимаем, не проглатывается молча"

    reset_stub; run_up --build      # пульта в этом корне нет
    want_up_code no-pult-artifact "--build без собранного пульта отказывает ДО сборки"

    reset_stub; run_up down --build
    [ "$UP_CODE" = 2 ] && ok "ключ подъёма, приставленный к снятию, не проглочен молча" \
        || bad "чужой ключ" "ждали код 2, получили $UP_CODE:" "$UP_OUT"

    reset_stub; run_up up down
    [ "$UP_CODE" = 2 ] && ok "две команды сразу — отказ, а не догадка, какую человек имел в виду" \
        || bad "две команды" "ждали код 2, получили $UP_CODE:" "$UP_OUT"
}

# ------------------------------------------------------------------ чего не прогнали
unproven() {
    part "ЧЕГО ЭТОТ ПРОГОН НЕ ПРОВЕРИЛ"
    # РЕШЕНИЯ подъёма прогнаны выше на подставном докере, а вот САМ подъём — нет, и путать
    # это нельзя: проверено, какие команды он отдаёт и чем отказывает, но не то, что образ
    # действительно приезжает из реестра и контейнер встаёт.
    if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
        skip "НАСТОЯЩИЙ подъём контроллера (докер, реестр, контейнер)" \
            "решения up.sh прогнаны на подставном докере; живой pull и живой контейнер — нет" \
            "докер на машине есть — прогони живьём: ./control/up.sh, затем ./control/up.sh status" \
            "проба нарочно не поднимает контейнеров: подъём — действие хозяина машины"
    else
        skip "НАСТОЯЩИЙ подъём контроллера (докер, реестр, контейнер)" \
            "решения up.sh прогнаны на подставном докере; живой pull и живой контейнер — нет" \
            "докера на этой машине нет — образ не притянуть и не поднять" \
            "прогон на машине с докером: ./control/up.sh && ./control/up.sh status"
    fi
    # Точка входа прогнана НАСТОЯЩИМ файлом, но без докера: образ с ней не собирался, и
    # команда из README руками не запускалась. Путать «файл ведёт себя так» и «образ с ним
    # встаёт» нельзя — это разные утверждения, и второе проверяется только живым подъёмом.
    skip "ОДНА КОМАНДА docker run живьём (образ с этой точкой входа, вставший пульт)" \
        "решения точки входа прогнаны на подставном докере и настоящем бинаре контроллера" \
        "что образ СОБИРАЕТСЯ с ней и что скопированная команда поднимает пульт — не проверено" \
        "живьём, на машине с докером: команда из раздела «Одна команда» в control/README.md"
    skip "настоящий подъём вещи на втором ресурсе" \
        "нужен второй ресурс с докером и ssh; здесь подъём подменён заглушкой" \
        "живьём: добавить ресурс через POST /api/resources и увидеть вещь: ./deploy/remote.sh status <имя> --recipe <путь>" \
        "вторая вещь живьём: положить рецепт в каталог (CONTROL_RECIPES) и позвать его по имени"
    skip "скоуп в настоящей раздаче на чужой машине" \
        "нужен второй ресурс; проверена только ступень отказа на недостижимом адресе" \
        "живьём: подними раздачу рецептом на второй машине и войди по её адресу и паролю"
}

case "$mode" in
    --green) green ;;
    --red)   red ;;
    --both)  green; red ;;
esac
unproven

part "ИТОГ"
printf '  проверок: %d, сошлось: %d, красноты: %d, НЕ прогнано: %d\n' \
    "$total" "$((total - failed))" "$failed" "$skipped" >&2

if [ "$failed" -gt 0 ]; then
    printf '\n\033[1;31mКРАСНО\033[0m — контроллер повёл себя не так, как обязан\n' >&2
    exit 1
fi
if [ "$skipped" -gt 0 ]; then
    printf '\n\033[1;33mНЕПОЛНЫЙ ПРОГОН\033[0m — %d проверок не выполнялось, они названы выше\n' "$skipped" >&2
    exit 3
fi
printf '\n\033[32mЗЕЛЕНО\033[0m — прогнано всё\n' >&2
exit 0
