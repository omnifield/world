#!/usr/bin/env bash
# Дверь на НАЗВАННОМ ресурсе: назвал адрес — дверь появилась там сама.
#
#   ./deploy/remote.sh add <имя> --addr user@host[:порт]   поставить дверь на ресурсе
#   ./deploy/remote.sh drop <имя>                          снять её оттуда
#   ./deploy/remote.sh list                                какие ресурсы заведены
#   ./deploy/remote.sh status <имя>                         что сейчас на том ресурсе
#
# ┌─────────────────────────────────────────────────────────────────────────────────────┐
# │ ЧЕЛОВЕК НА ТОТ РЕСУРС НЕ ЗАХОДИТ. Ни консоли, ни `git clone`, ни `apt install`, ни    │
# │ положенных туда скриптов. На той машине нужен ТОЛЬКО докер и ssh с ключом — всё      │
# │ остальное приезжает образом (`TECH` 2, решение user 2026-08-14, рамочное).           │
# │                                                                                      │
# │ Своего подъёма мы для этого НЕ ПИШЕМ. Докер умеет управлять чужим демоном по SSH из   │
# │ коробки — контекст (`docker context`), и это встроенный механизм, а не находка       │
# │ (сверка с рынком `TECH` 3, 2026-08-14: класс 1, «агента нет вообще»). Здесь только    │
# │ то, чего у контекста нет: производное имя, поставка образа, порядок шагов и отказ с  │
# │ причиной и выходом (`WORLD2` 2.3).                                                   │
# └─────────────────────────────────────────────────────────────────────────────────────┘
#
# ЧТО ПОЯВЛЯЕТСЯ НА ТОЙ СТОРОНЕ — и ничего сверх этого:
#
#   контейнер `world-door`      снимается `drop`
#   сеть `omnifield-gateway`    снимается `drop`, если в ней больше никого нет
#   тома `world-field`/`world-stand`   состояние поля — остаётся, снимается `drop --with-state`
#   образ `omnifield/world:dev` остаётся, снимается `drop --with-image`
#
# На ЭТОЙ машине появляется ровно одна вещь — контекст докера `world-<имя>`. Он же и есть
# всё наше состояние: адрес ресурса хранится в нём, своего файла реестра зона не заводит.
# Снимается тем же `drop`. Двух списков одного и того же не бывает: разъедутся — `drop`
# начнёт снимать не тот ресурс, о котором думает человек.
#
# КРЕДЫ МИР НЕ ЗАВОДИТ И НЕ ХРАНИТ. Докер по SSH ходит системным `ssh` и берёт ключ там, где
# его берёт ssh: агент либо `~/.ssh/config`. Ключом-значением это подменить нельзя — у
# `docker context` нет такого поля вовсе, и наш ключ докер бы молча проигнорировал. Поэтому
# ключа-ключа здесь нет: креды принадлежат юзеру и его связке (`WORLD2` 3.0), а отказ
# `access-denied` называет, куда именно ключ кладут.
#
# Имена переменных ЛАТИНСКИЕ, хотя репозиторий говорит по-русски: bash не поддерживает
# не-ASCII в именах и молча превращает `ИМЯ=x` в «команда не найдена». Тексты — русские.
set -euo pipefail
# Ступени «нет докера на той стороне» и «ключ не принят» различаются по тексту ответа
# системы, а он переводится. Без этого на локализованной машине два разных отказа
# схлопнулись бы в один — и человека отправляло бы чинить не то (`WORLD2` 2.3).
export LC_ALL=C

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="$HERE/compose.yaml"

# Общая сеть — та же, что и на своей машине: имя контрактное и живёт в файле запуска
# (`compose.yaml`, `external: true`). Компоуз её не создаёт, поэтому создаём мы — так же,
# как это делает `up.sh` у себя, и по той же причине: войти в несуществующую сеть докер
# не даёт, и первый подъём на чистой машине без этого не встаёт.
NET=omnifield-gateway
# Имя контейнера двери задано в `compose.yaml` (`container_name`). Здесь оно нужно, чтобы
# спросить у ЧУЖОГО демона про здоровье двери; вторая копия имени сторожится пробой.
DOOR=world-door
# Хост-порт двери НА ТОМ РЕСУРСЕ. То же значение, что у `up.sh`, и подставляется в тот же
# `compose.yaml` — раскладка на чужой машине не отличается от своей ничем.
PORT="${WORLD_PORT:-8080}"
# Сколько ждём ответа ssh. Меньше — быстрее краснеет, больше — терпимее к медленной связи.
TIMEOUT="${WORLD_REMOTE_TIMEOUT:-10}"
# Сколько ждём, пока дверь на той стороне станет здоровой. Дольше, чем у `up.sh`: там образ
# уже лежит и запуск местный, здесь между нами сеть.
WAIT="${WORLD_REMOTE_WAIT:-60}"

# Приставка контекста. Имя контекста ПРОИЗВОДНО от имени ресурса (`world-<имя>`), а не
# выдумывается заново: производное имя разбирается в обе стороны — по ресурсу находится
# контекст, по контексту виден ресурс. Выдуманное имя пришлось бы где-то записать, и это
# был бы второй список того же самого.
PREFIX=world-

usage() {
    cat <<'USAGE'
Дверь на названном ресурсе: ./deploy/remote.sh <команда>

  add <имя> --addr user@host[:порт]   поставить дверь на том ресурсе одной командой
  drop <имя> [--with-state] [--with-image]   снять её оттуда
  list                                какие ресурсы заведены с этой машины
  status <имя>                        что сейчас на том ресурсе
  -h --help                           эта подсказка

Имя ресурса — своё, короткое: буквы, цифры и дефис. Имя контекста докера производно от
него (`world-<имя>`) и отдельно не называется.

  ./deploy/remote.sh add vps --addr world@10.8.0.5
  ./deploy/remote.sh add vps --addr world@10.8.0.5:2222   порт ssh, если он не 22
  WORLD_PORT=8090 ./deploy/remote.sh add vps --addr …     хост-порт двери НА ТОМ ресурсе

Ключи `add`:
  --addr АДРЕС      адрес ресурса. Обязателен, пока ресурс не заведён; заведённому не
                    нужен — адрес лежит в контексте. Названный заново — адрес меняется
  --resend-image    везти образ, даже если на той стороне он уже есть

Ключи `drop` — по умолчанию снимаются контейнер, сеть и контекст:
  --with-state      снять и ТОМА: реестр поля и журнал стенда. Состояние стирается
  --with-image      снять и образ мира с того ресурса

Переменные:
  WORLD_PORT=8080              хост-порт двери на том ресурсе
  WORLD_REMOTE_TIMEOUT=10      сколько секунд ждём ответа ssh
  WORLD_REMOTE_WAIT=60         сколько секунд ждём, пока дверь станет здоровой

ЧТО НУЖНО НА ТОМ РЕСУРСЕ: докер и ssh с ключом. Больше ничего — ни пакетов, ни клона
репозитория, ни наших скриптов. Ключ берётся оттуда же, откуда его берёт ssh: агент
(`ssh-add`) либо `~/.ssh/config`; своего хранилища ключей мир не заводит (WORLD2 3.0).

Отказ печатается двумя строками: причина и выход — человеку, `REMOTE-REFUSAL: <код>` —
машине. Коды: no-name · bad-name · no-address · bad-address · no-docker · no-daemon ·
no-compose · no-ssh · access-denied · no-remote-docker · no-image · image-send-failed ·
no-network · port-busy · up-failed · door-silent · no-such-resource · down-failed ·
context-failed.
USAGE
}

# ------------------------------------------------------------------ разговор
step() { printf '\n\033[1m→ %s\033[0m\n' "$*" >&2; }
ok()   { printf '  ✓ %s\n' "$*" >&2; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*" >&2; }
say()  { printf '%s\n' "$*" >&2; }
detail() { printf '      %s\n' "$*" >&2; }

# refuse <код> <причина> <выход…> — единственный способ выйти с ошибкой. Причина и выход
# обязательны оба: «нельзя» без выхода — провал самого мира, юзеру нечего чинить
# (`WORLD2` 2.3). Код печатается в stdout и предназначен машине; проба стережёт КОД, а не
# формулировку — проба, привязанная к тексту, зеленеет на верной правке (`WORLD2` 4.2).
refuse() {
    local code="$1" why="$2"; shift 2
    printf 'REMOTE-REFUSAL: %s\n' "$code"
    printf '\n\033[1;31m✗ отказ:\033[0m %s\n' "$why" >&2
    local line; for line in "$@"; do printf '  выход: %s\n' "$line" >&2; done
    exit 1
}

# ------------------------------------------------------------------ имя и адрес
NAME=""; ADDR=""; CTX=""
USER_AT=""; HOST=""; SSH_PORT=22

# ctx_name — ЕДИНСТВЕННОЕ место, где имя ресурса превращается в имя контекста. Разъедутся
# две таких формулы — `add` заведёт один контекст, а `drop` пойдёт снимать другой, и дверь
# останется висеть на чужой машине, о которой человек уже думает, что убрал её.
ctx_name() { printf '%s%s' "$PREFIX" "$1"; }

# need_name — имя ресурса называет человек. Умолчания нет намеренно: угаданное имя однажды
# укажет на чужой ресурс, и мы снесём дверь не там.
need_name() {
    local cmd="$1"
    [ -n "$NAME" ] || refuse no-name \
        "имя ресурса не названо" \
        "назови его: ./deploy/remote.sh $cmd <имя>" \
        "имя своё и короткое — по нему потом искать: vps, home, work"

    # Годность имени проверяем СВОИМ правилом, а не отдаём докеру. Докер про своё имя
    # контекста скажет «invalid reference format» — про контекст, которого человек не
    # называл вовсе; отказ указывал бы на вещь, которой в его команде нет.
    case "$NAME" in
        *[!a-z0-9-]*|-*|*-|"")
            refuse bad-name \
                "имя ресурса «$NAME» не годится" \
                "буквы латиницей в нижнем регистре, цифры и дефис внутри: vps-1, home" \
                "имя едет в имя контекста докера ($(ctx_name '<имя>')), и там оно ограничено не нами" ;;
    esac
    CTX="$(ctx_name "$NAME")"
}

# parse_addr — разбор адреса ОДИН раз и в одном месте: разъедутся разборы — проверка
# доступа пойдёт на один порт, а контекст докера на другой, и отказ назовёт ступень,
# которой на самом деле нет.
parse_addr() {
    local rest="$ADDR"
    case "$rest" in
        *@*) USER_AT="${rest%@*}"; rest="${rest##*@}" ;;
        *)   USER_AT="" ;;
    esac
    # Больше одного двоеточия — это IPv6, и в этой форме он неразличим с портом. Отказываем
    # вслух, а не разбираем как попало: молчаливый разбор увёл бы и стук, и контекст на
    # выдуманный порт. Тот же довод и тот же отказ, что у зоны `gate`.
    case "${rest//[!:]/}" in
        ::*) refuse bad-address \
                "адрес «$ADDR» похож на IPv6 — эта форма не разбирается" \
                "назови имя или адрес IPv4: --addr world@10.8.0.5" \
                "нужен именно IPv6 — это отдельная работа, скажи владельцу зоны deploy" ;;
    esac
    case "$rest" in
        *:*) HOST="${rest%:*}"; SSH_PORT="${rest##*:}" ;;
        *)   HOST="$rest"; SSH_PORT=22 ;;
    esac
    [ -n "$HOST" ] || refuse bad-address \
        "в адресе «$ADDR» нет хоста" \
        "форма адреса: --addr user@хост или user@хост:порт"
    case "$SSH_PORT" in
        ""|*[!0-9]*) refuse bad-address \
                "порт «$SSH_PORT» в адресе «$ADDR» — не число" \
                "форма адреса: --addr user@хост:2222" ;;
    esac
}

ssh_target() { [ -n "$USER_AT" ] && printf '%s@%s' "$USER_AT" "$HOST" || printf '%s' "$HOST"; }
# Адрес докера — то, что ложится в контекст. Собирается из тех же кусков, что и цель ssh:
# один разбор, две сборки, разъехаться нечему.
docker_endpoint() { printf 'ssh://%s:%s' "$(ssh_target)" "$SSH_PORT"; }

# ------------------------------------------------------------------ чем поднимаем
# have_local_tools — что нужно на ЭТОЙ машине. Демон здесь НЕ проверяется: он нужен только
# поставке образа, и спрашивается ровно там (`need_local_daemon`). Ресурсу, где образ уже
# лежит, наш демон не нужен вовсе — и требовать его значило бы отказывать без причины.
have_local_tools() {
    command -v docker >/dev/null 2>&1 || refuse no-docker \
        "докера на ЭТОЙ машине нет — управлять чужим демоном нечем" \
        "поставь Docker Engine либо Docker Desktop: https://docs.docker.com/engine/install/" \
        "докер нужен здесь как клиент: на тот ресурс он ходит сам, по ssh"

    docker compose version >/dev/null 2>&1 || refuse no-compose \
        "у докера нет compose v2 (плагина docker-compose-plugin)" \
        "поставь плагин: https://docs.docker.com/compose/install/" \
        "compose работает ЗДЕСЬ и говорит с чужим демоном — на том ресурсе он не нужен"

    # Клиент ssh — не наша прихоть: транспорт `ssh://` докер делает системным `ssh`, своего
    # у него нет. Без него `docker --context` упал бы ответом про exec, а человек искал бы
    # поломку в ресурсе.
    command -v ssh >/dev/null 2>&1 || refuse no-ssh \
        "клиента ssh на этой машине нет" \
        "поставь openssh-client — докер ходит на чужой демон системным ssh, своего у него нет" \
        "на Windows он есть в Git Bash и в самой системе (OpenSSH Client)"
}

need_local_daemon() {
    docker info >/dev/null 2>&1 || refuse no-daemon \
        "докер есть, а свой демон не отвечает — образ нечем прочитать" \
        "запусти его: systemctl start docker (либо открой Docker Desktop)" \
        "демон работает — дело в правах: usermod -aG docker \$USER и перезайти" \
        "свой демон нужен ТОЛЬКО чтобы взять образ отсюда; если образ уже есть на том ресурсе, он не понадобится"
}

# ------------------------------------------------------------------ ресурс
ctx_exists() { docker context inspect "$CTX" >/dev/null 2>&1; }

# ctx_endpoint — адрес, записанный в контексте. Это и есть наш реестр ресурсов: своего
# файла зона не заводит, потому что второй список того же самого рано или поздно разъедется
# с первым, и `drop` пойдёт не туда.
ctx_endpoint() {
    docker context inspect "$CTX" --format '{{.Endpoints.docker.Host}}' 2>/dev/null || true
}

# check_access — принимает ли ресурс креды юзера. Отдельной ступенью ДО докера: «ключ не
# принят» и «на той стороне нет докера» чинят разные вещи, и схлопывать их в одно «не
# получилось» нельзя (`WORLD2` 2.3).
#
# BatchMode=yes обязателен: без него ssh спросит пароль и проверка повиснет вместо того,
# чтобы покраснеть. Пароль докеру всё равно не годится — по SSH он умеет только ключ
# (`TECH` 3), — так что спрашивать его не за чем.
check_access() {
    local out rc
    set +e
    out=$(ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new \
              -o "ConnectTimeout=$TIMEOUT" -p "$SSH_PORT" "$(ssh_target)" true 2>&1); rc=$?
    set -e
    [ "$rc" -eq 0 ] && return 0

    detail "${out:-(ssh промолчал)}"
    refuse access-denied \
        "ресурс $ADDR не пустил: креды не приняты либо до него не дойти" \
        "положи открытый ключ юзера на тот ресурс: ssh-copy-id -p $SSH_PORT $(ssh_target)" \
        "ключ есть — добавь его в агент (ssh-add ~/.ssh/id_ed25519) или назови в ~/.ssh/config для этого хоста: докер берёт ключ ОТТУДА, своим значением его не задать" \
        "имя юзера в адресе — часть кред, а не украшение: проверь его" \
        "ресурс вообще не отвечает — ступени «дорога · ответ · доступ» разбирает ./gate/gate.sh check"
}

# check_remote_docker — есть ли докер на той стороне и отвечает ли он. Спрашиваем через
# контекст, то есть ровно тем путём, которым потом пойдёт подъём: спроси мы иначе (своим
# `ssh docker version`), проверка зеленела бы там, где настоящий путь ломается.
check_remote_docker() {
    local out rc
    set +e
    out=$(docker --context "$CTX" version --format '{{.Server.Version}}' 2>&1); rc=$?
    set -e
    [ "$rc" -eq 0 ] && { ok "докер на ресурсе есть: версия $out"; return 0; }

    detail "${out:-(докер промолчал)}"
    refuse no-remote-docker \
        "на ресурсе $ADDR ssh пускает, а докер не отвечает" \
        "поставь докер на том ресурсе: https://docs.docker.com/engine/install/ — это единственное, что мир там требует" \
        "докер стоит — дай юзеру право говорить с демоном: usermod -aG docker $(printf '%s' "${USER_AT:-<юзер>}") и перезайти" \
        "проверить руками: ssh -p $SSH_PORT $(ssh_target) docker version"
}

# image_name — имя образа берём из файла запуска, а не держим вторую копию здесь: разъедутся
# — повезём на ресурс не тот образ, который там потом поднимется.
image_name() {
    local img
    img="$(docker compose -f "$COMPOSE_FILE" config --images 2>/dev/null | head -n1 || true)"
    [ -n "$img" ] || img=omnifield/world:dev
    printf '%s' "$img"
}

# ------------------------------------------------------------------ add
cmd_add() {
    need_name add
    have_local_tools

    # Адрес обязателен, пока ресурс не заведён. Заведённому он не нужен: адрес лежит в
    # контексте, и повторный `add` — это законный способ поднять дверь заново, не повторяя
    # адрес. Названный заново — адрес меняется, и об этом говорится вслух.
    if [ -z "$ADDR" ]; then
        ctx_exists || refuse no-address \
            "адрес ресурса «$NAME» не назван, а сам ресурс ещё не заведён" \
            "назови адрес: ./deploy/remote.sh add $NAME --addr user@10.8.0.5" \
            "адрес угадывать нечем — умолчания у него нет намеренно: угаданный однажды укажет на чужую машину" \
            "какие ресурсы уже заведены: ./deploy/remote.sh list"
        local ep; ep="$(ctx_endpoint)"
        ADDR="${ep#ssh://}"
    fi
    parse_addr

    step "ресурс $NAME → $ADDR (контекст $CTX)"

    # ---------------------------------------------------------- 1. доступ
    step "проверяю доступ: ssh на $(ssh_target):$SSH_PORT"
    check_access
    ok "ресурс пускает — креды приняты"

    # ---------------------------------------------------------- 2. контекст
    step "контекст докера $CTX"
    local endpoint; endpoint="$(docker_endpoint)"
    if ctx_exists; then
        local was; was="$(ctx_endpoint)"
        if [ "$was" = "$endpoint" ]; then
            ok "контекст уже есть и смотрит туда же"
        else
            docker context update "$CTX" --docker "host=$endpoint" >/dev/null 2>&1 || refuse context-failed \
                "контекст $CTX не переставить на $endpoint" \
                "посмотри, что мешает: docker context update $CTX --docker host=$endpoint" \
                "снести и завести заново: ./deploy/remote.sh drop $NAME, затем add"
            warn "адрес ресурса сменился: было $was, стало $endpoint"
        fi
    else
        docker context create "$CTX" \
            --description "мир: дверь на ресурсе $NAME" \
            --docker "host=$endpoint" >/dev/null 2>&1 || refuse context-failed \
            "контекст $CTX не создать" \
            "посмотри, что мешает: docker context create $CTX --docker host=$endpoint" \
            "имя занято чем-то чужим — возьми другое имя ресурса: ./deploy/remote.sh add <другое> --addr $ADDR"
        ok "контекст создан — дальше докер ходит на ресурс сам"
    fi

    # ---------------------------------------------------------- 3. докер на той стороне
    step "докер на ресурсе"
    check_remote_docker

    # ---------------------------------------------------------- 4. образ
    # Поставка КОПИИ, а не реестр: интернет на той стороне не нужен вовсе, и это ложится на
    # `WORLD2` 1.5 «переходит копия». Развилка названа явно в `WORLD2-98`; реестр добавится
    # позже и эту поставку не отменит — это два способа доставки одной вещи.
    local image; image="$(image_name)"
    step "образ $image на ресурсе"
    local have_remote=
    docker --context "$CTX" image inspect "$image" >/dev/null 2>&1 && have_remote=1

    if [ -n "$have_remote" ] && [ -z "$RESEND" ]; then
        ok "образ на ресурсе уже есть — не везём (перевезти заново: --resend-image)"
    else
        need_local_daemon
        docker image inspect "$image" >/dev/null 2>&1 || refuse no-image \
            "образа $image нет ни на ресурсе $NAME, ни на этой машине — везти нечего" \
            "собери его здесь: ./deploy/build.sh — и повтори эту команду" \
            "образ есть под другим именем — переименуй: docker tag <твой> $image" \
            "поля для этой сборки не нужно: пульт в образ двери не едет — подробности в deploy/README.md"

        [ -n "$have_remote" ] && warn "образ там есть, но велено перевезти заново (--resend-image)"
        # Поток идёт через ssh целиком: ни временного файла на той стороне, ни `scp`. На
        # чужой машине не появляется даже tar-архива — только слои в её докере.
        warn "везу образ копией через ssh — это самый долгий шаг: минуты, а не секунды"
        local rc=0
        docker save "$image" | docker --context "$CTX" load >/dev/null || rc=$?
        [ "$rc" -eq 0 ] || refuse image-send-failed \
            "образ $image не доехал на ресурс $NAME" \
            "проверь, что на той стороне есть место: ssh -p $SSH_PORT $(ssh_target) df -h" \
            "связь рвётся на длинной передаче — повтори: ./deploy/remote.sh add $NAME --resend-image" \
            "образ большой, а канал узкий — это цена поставки копией; реестр будет отдельным решением (WORLD2-98)"
        ok "образ приехал копией"
    fi

    # ---------------------------------------------------------- 5. общая сеть
    # Идемпотентно, ровно как у себя (`up.sh`): сеть уже есть — не сделано ничего. Компоуз
    # её не создаёт (`external: true`), а войти в несуществующую сеть докер не даёт.
    step "общая сеть $NET на ресурсе"
    if docker --context "$CTX" network inspect "$NET" >/dev/null 2>&1; then
        ok "сеть уже есть"
    else
        docker --context "$CTX" network create "$NET" >/dev/null 2>&1 || refuse no-network \
            "общую сеть $NET на ресурсе $NAME не создать" \
            "посмотри, что мешает: docker --context $CTX network create $NET" \
            "подсети кончились — почисти неиспользуемые: docker --context $CTX network prune"
        ok "сеть создана"
    fi

    # ---------------------------------------------------------- 6. контейнер
    # Файл запуска ЛЕЖИТ ЗДЕСЬ и остаётся здесь: компоуз читает его на этой машине и говорит
    # с чужим демоном. На тот ресурс не уезжает ни файла — в этом весь смысл контекста.
    step "поднимаю дверь на $NAME: хост-порт $PORT"
    local out rc=0
    set +e
    out=$(WORLD_PORT="$PORT" docker --context "$CTX" compose -f "$COMPOSE_FILE" \
              up -d --no-build --remove-orphans 2>&1); rc=$?
    set -e
    if [ "$rc" -ne 0 ]; then
        printf '%s\n' "$out" >&2
        # Занятый порт — отдельная ступень, а не «не встало»: чинит его хозяин того ресурса,
        # и чинится он другим действием. Схлопнуть его в общий отказ значит отправить
        # человека читать журнал там, где ответ уже известен.
        case "$out" in
            *"port is already allocated"*|*"address already in use"*|*"Ports are not available"*|*"bind: address already in use"*)
                refuse port-busy \
                    "порт $PORT на ресурсе $NAME занят кем-то ещё" \
                    "возьми другой: WORLD_PORT=8090 ./deploy/remote.sh add $NAME" \
                    "кто держит порт: ssh -p $SSH_PORT $(ssh_target) ss -lntp | grep :$PORT" \
                    "наружу торчит ровно одна дверь на ресурс — второй мир на той же машине поднимают на другом хост-порту" ;;
        esac
        refuse up-failed \
            "дверь на ресурсе $NAME не встала" \
            "ответ докера напечатан выше — он же первая причина" \
            "журнал двери: docker --context $CTX compose -f deploy/compose.yaml logs --tail=40" \
            "снять недоделанное и начать заново: ./deploy/remote.sh drop $NAME"
    fi
    ok "контейнер запущен"

    # ---------------------------------------------------------- 7. дверь отвечает
    # «Запущен» и «отвечает» — разные вещи, и подтверждать надо второе. Спрашиваем ЗДОРОВЬЕ у
    # чужого демона, а не стучимся сюда curl-ом: опубликованный порт того ресурса может быть
    # закрыт для нас фаерволом, и живая дверь выглядела бы мёртвой. HEALTHCHECK стучится
    # изнутри контейнера — это ровно тот вопрос, на который тут надо ответить.
    step "жду, пока дверь на $NAME станет здоровой (до ${WAIT}с)"
    local health="" waited=0
    while [ "$waited" -lt "$WAIT" ]; do
        health="$(docker --context "$CTX" inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$DOOR" 2>/dev/null || true)"
        case "$health" in
            healthy) break ;;
            none)
                # Образ без HEALTHCHECK — сказать это вслух, а не выдать «встал» за
                # «отвечает». Приблизительная запись хуже отсутствующей: она выглядит
                # знанием (`WORLD2` 4.2).
                warn "у образа нет HEALTHCHECK — «отвечает ли дверь» этой командой не проверить"
                break ;;
        esac
        sleep 2; waited=$((waited + 2))
    done

    if [ "$health" = healthy ]; then
        ok "дверь отвечает: $DOOR здоров на ресурсе $NAME"
    elif [ "$health" != none ]; then
        printf '\n--- последние строки журнала двери ---\n' >&2
        docker --context "$CTX" compose -f "$COMPOSE_FILE" logs --tail=40 >&2 || true
        refuse door-silent \
            "дверь на $NAME запущена, но за ${WAIT}с здоровой не стала (состояние: ${health:-неизвестно})" \
            "журнал выше — если дверь падает, мир называет причину сам, первой же строкой" \
            "тома с прошлого мира могли остаться от другой редакции: ./deploy/remote.sh drop $NAME --with-state, затем add" \
            "медленный ресурс — дай больше времени: WORLD_REMOTE_WAIT=180 ./deploy/remote.sh add $NAME"
    fi

    # ---------------------------------------------------------- 8. виден ли порт отсюда
    # Стук с ЭТОЙ машины — не проверка двери, а проверка ДОСТУПА снаружи, и это разные
    # вещи. Не дошло — не отказ: дверь здорова у себя, а порт может быть закрыт фаерволом
    # или NAT-ом, и чинит это хозяин ресурса.
    if [ "$health" = healthy ]; then
        local knocked=
        if command -v curl >/dev/null 2>&1; then
            curl -fsS -m 3 -o /dev/null "http://$HOST:$PORT/healthz" && knocked=1 || true
        elif command -v wget >/dev/null 2>&1; then
            wget -q -T 3 -O /dev/null "http://$HOST:$PORT/healthz" && knocked=1 || true
        else
            warn "ни curl, ни wget здесь нет — снаружи дверь не проверить (изнутри она здорова)"
            knocked=skip
        fi
        if [ "$knocked" = 1 ]; then
            ok "порт виден и с этой машины: http://$HOST:$PORT/healthz отвечает"
        elif [ -z "$knocked" ]; then
            warn "с этой машины http://$HOST:$PORT/healthz не отвечает, хотя дверь здорова"
            detail "это не поломка двери: снаружи порт может быть закрыт фаерволом или NAT-ом"
            detail "открыть его — дело хозяина ресурса; изнутри ресурса дверь работает"
        fi
    fi

    cat >&2 <<GOTOVO

$(printf '\033[1;32m✓ дверь стоит на ресурсе %s\033[0m' "$NAME")

  проба жизни       http://$HOST:$PORT/healthz     (если порт открыт хозяином ресурса)
  кто в поле        http://$HOST:$PORT/api/locations
  лица тут нет      корень отвечает 410 face-elsewhere: дверь не показывает, пульт при
                    контроллере
  что там сейчас    ./deploy/remote.sh status $NAME
  журнал            docker --context $CTX compose -f deploy/compose.yaml logs -f
  снять             ./deploy/remote.sh drop $NAME

  на ресурсе появились: контейнер $DOOR · сеть $NET · тома world-field и world-stand ·
  образ $image. Ничего сверх этого туда не клали — ни файлов, ни пакетов.
GOTOVO
}

# ------------------------------------------------------------------ drop
cmd_drop() {
    need_name drop
    have_local_tools

    ctx_exists || refuse no-such-resource \
        "ресурса «$NAME» с этой машины не заводили — контекста $CTX нет" \
        "посмотри, какие есть: ./deploy/remote.sh list" \
        "ресурс заводили с ДРУГОЙ машины — снимай оттуда: контекст живёт там, где его создали"

    ADDR="$(ctx_endpoint)"; ADDR="${ADDR#ssh://}"
    parse_addr

    step "снимаю дверь с ресурса $NAME ($ADDR)"

    # Ресурс мог умереть, уехать или сменить ключ — а контекст на этой машине остался. Снять
    # его мы обязаны в любом случае: иначе `list` будет врать про ресурсы, которых нет.
    # Поэтому сначала пробуем снять на той стороне, а свой контекст убираем всегда.
    local reachable=1
    docker --context "$CTX" version >/dev/null 2>&1 || reachable=

    if [ -n "$reachable" ]; then
        local out rc=0
        local -a args=(compose -f "$COMPOSE_FILE" down --remove-orphans)
        [ -n "$WITH_STATE" ] && args+=(-v)
        set +e
        out=$(WORLD_PORT="$PORT" docker --context "$CTX" "${args[@]}" 2>&1); rc=$?
        set -e
        if [ "$rc" -ne 0 ]; then
            printf '%s\n' "$out" >&2
            refuse down-failed \
                "контейнер на ресурсе $NAME не снялся" \
                "ответ докера напечатан выше" \
                "снять руками: docker --context $CTX compose -f deploy/compose.yaml down" \
                "контекст на этой машине НЕ снят — повтори команду, когда ресурс ответит" \
                "ресурса больше нет вовсе — снеси только контекст: docker context rm $CTX"
        fi
        ok "контейнер снят"
        [ -n "$WITH_STATE" ] && ok "тома сняты: реестр поля и журнал стенда стёрты"

        # Сеть снимаем, если в ней больше никого нет. Занята — оставляем и говорим об этом:
        # снести общую сеть, в которой стоят соседи, значило бы сломать чужое ради своей
        # чистоты. `network rm` сам откажет на занятой сети, и это ровно та проверка,
        # которая нужна, — спрашивать состав отдельно значило бы гадать между двумя вызовами.
        if docker --context "$CTX" network inspect "$NET" >/dev/null 2>&1; then
            if docker --context "$CTX" network rm "$NET" >/dev/null 2>&1; then
                ok "общая сеть $NET снята"
            else
                warn "сеть $NET осталась — в ней стоит кто-то ещё"
                detail "это соседи по полю, а не наш след: снести её значит сломать их связь"
            fi
        fi

        if [ -n "$WITH_IMAGE" ]; then
            local image; image="$(image_name)"
            if docker --context "$CTX" image rm "$image" >/dev/null 2>&1; then
                ok "образ $image снят с ресурса"
            else
                warn "образ $image снять не вышло — возможно, его держит другой контейнер"
            fi
        fi
    else
        warn "ресурс $ADDR не отвечает — на той стороне снять нечего этой командой"
        detail "контекст с этой машины всё равно убираем: иначе list будет врать про живой ресурс"
        detail "ресурс вернётся — заведи заново: ./deploy/remote.sh add $NAME --addr $ADDR"
    fi

    docker context rm "$CTX" >/dev/null 2>&1 || refuse context-failed \
        "контекст $CTX с этой машины не убрать" \
        "посмотри, что мешает: docker context rm $CTX" \
        "контекст выбран текущим — переключись и повтори: docker context use default"
    ok "контекст $CTX убран — на этой машине следов ресурса не осталось"

    say ""
    if [ -n "$reachable" ]; then
        printf '\033[1;32m✓ дверь снята с ресурса %s\033[0m\n' "$NAME" >&2
        say ""
        say "  что осталось на той машине:"
        if [ -n "$WITH_STATE" ]; then
            say "    состояние поля — стёрто (--with-state)"
        else
            say "    тома world-field и world-stand — состояние поля, оно переживает снятие"
            say "      стереть: ./deploy/remote.sh add $NAME --addr $ADDR, затем drop $NAME --with-state"
            say "      состояние переживает снятие НАРОЧНО: поднятая заново дверь находит своё поле,"
            say "      а не пустой реестр. Молча стирать его снятие не вправе"
        fi
        if [ -n "$WITH_IMAGE" ]; then
            say "    образ мира — снят (--with-image)"
        else
            say "    образ мира — остался: он тяжёлый, и второй подъём с ним мгновенный"
        fi
        say "    больше НИЧЕГО: ни файлов, ни пакетов, ни клона репозитория — их туда и не клали"
    else
        printf '\033[1;33m! ресурс %s убран только с этой машины\033[0m\n' "$NAME" >&2
        say "  на самой машине контейнер, тома и образ могли остаться — снять их можно, когда она ответит"
    fi
}

# ------------------------------------------------------------------ list
cmd_list() {
    have_local_tools
    local rows
    rows="$(docker context ls --format '{{.Name}}	{{.DockerEndpoint}}' 2>/dev/null | grep "^$PREFIX" || true)"
    say ""
    if [ -z "$rows" ]; then
        say "ресурсов с этой машины не заводили"
        say ""
        say "  завести: ./deploy/remote.sh add <имя> --addr user@10.8.0.5"
        return 0
    fi
    printf '%-16s %-32s %s\n' "РЕСУРС" "АДРЕС" "КОНТЕКСТ" >&2
    local name endpoint
    while IFS="	" read -r name endpoint; do
        [ -n "$name" ] || continue
        printf '%-16s %-32s %s\n' "${name#$PREFIX}" "${endpoint#ssh://}" "$name" >&2
    done <<< "$rows"
    say ""
    say "  что на ресурсе сейчас: ./deploy/remote.sh status <имя>"
    say "  список — это контексты докера, своего реестра зона не заводит: второй список"
    say "  того же самого однажды разъехался бы с первым"
}

# ------------------------------------------------------------------ status
cmd_status() {
    need_name status
    have_local_tools
    ctx_exists || refuse no-such-resource \
        "ресурса «$NAME» с этой машины не заводили — контекста $CTX нет" \
        "посмотри, какие есть: ./deploy/remote.sh list" \
        "завести: ./deploy/remote.sh add $NAME --addr user@10.8.0.5"

    ADDR="$(ctx_endpoint)"; ADDR="${ADDR#ssh://}"
    parse_addr
    local image; image="$(image_name)"

    say ""
    say "ресурс     : $NAME"
    say "адрес      : $ADDR"
    say "контекст   : $CTX"

    # Каждая строка ниже — ОТДЕЛЬНЫЙ вопрос отдельному демону, и ни одна не выводится из
    # другой. «Докер отвечает» не значит «дверь стоит», «контейнер есть» не значит «дверь
    # отвечает»: состояние, выведенное вместо измеренного, врёт ровно тогда, когда на него
    # смотрят (`WORLD2` 4.2).
    if ! docker --context "$CTX" version >/dev/null 2>&1; then
        say "докер там  : не отвечает"
        say ""
        say "  ресурс молчит: он выключен, сменил адрес или не пускает креды"
        say "  разобрать по ступеням: ./gate/gate.sh check (GATE_ADDR=$ADDR)"
        return 0
    fi
    say "докер там  : отвечает"

    if docker --context "$CTX" image inspect "$image" >/dev/null 2>&1; then
        say "образ      : $image есть"
    else
        say "образ      : $image НЕТ — дверь поднять нечем"
    fi

    local state health published
    state="$(docker --context "$CTX" ps -a --filter "name=^$DOOR$" --format '{{.Status}}' 2>/dev/null | head -n1 || true)"
    say "контейнер  : ${state:-нет}"
    if [ -n "$state" ]; then
        health="$(docker --context "$CTX" inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}HEALTHCHECK-а нет{{end}}' "$DOOR" 2>/dev/null || true)"
        say "дверь      : ${health:-неизвестно}"
        published="$(docker --context "$CTX" port "$DOOR" 2>/dev/null | tr '\n' ' ' || true)"
        say "порт       : ${published:-публикации не видно}"
    fi

    if docker --context "$CTX" network inspect "$NET" >/dev/null 2>&1; then
        say "сеть       : $NET есть"
    else
        say "сеть       : $NET нет"
    fi

    local vols
    vols="$(docker --context "$CTX" volume ls --format '{{.Name}}' 2>/dev/null | grep -E '^world-(field|stand)$' | tr '\n' ' ' || true)"
    say "состояние  : ${vols:-томов нет}"

    say ""
    say "  поднять заново : ./deploy/remote.sh add $NAME"
    say "  снять          : ./deploy/remote.sh drop $NAME"
}

# ------------------------------------------------------------------ разбор ключей
# Ключи разбираем ДО всего: `--help` обязан работать и там, где нет ни докера, ни ssh, ни
# заведённых ресурсов.
RESEND=""; WITH_STATE=""; WITH_IMAGE=""
CMD="${1:-}"
[ $# -gt 0 ] && shift || true

case "$CMD" in
    -h|--help) usage; exit 0 ;;
    "")        usage >&2; printf '\nкоманда не названа\n' >&2; exit 2 ;;
    add|drop|status|list) ;;
    *)         usage >&2; printf '\nнепонятная команда: %s\n' "$CMD" >&2; exit 2 ;;
esac

while [ $# -gt 0 ]; do
    case "$1" in
        --addr)  shift; [ $# -gt 0 ] || { printf 'у --addr нет значения: --addr user@10.8.0.5\n' >&2; exit 2; }
                 ADDR="$1" ;;
        --addr=*)       ADDR="${1#--addr=}" ;;
        --resend-image) RESEND=1 ;;
        --with-state)   WITH_STATE=1 ;;
        --with-image)   WITH_IMAGE=1 ;;
        -h|--help)      usage; exit 0 ;;
        -*)             usage >&2; printf '\nнепонятный ключ: %s\n' "$1" >&2; exit 2 ;;
        *)              [ -z "$NAME" ] || { usage >&2; printf '\nимя ресурса названо дважды: %s и %s\n' "$NAME" "$1" >&2; exit 2; }
                        NAME="$1" ;;
    esac
    shift
done

case "$CMD" in
    add)    cmd_add ;;
    drop)   cmd_drop ;;
    list)   cmd_list ;;
    status) cmd_status ;;
esac
