#!/usr/bin/env bash
# Проба ВЕЩИ НА ЧУЖОМ РЕСУРСЕ — что `remote.sh` делает, чего НЕ делает и как отказывает.
#
#   ./deploy/probe-remote.sh            (умолчание) зелёный прогон, затем красный
#   ./deploy/probe-remote.sh --green    только зелёный: что инструмент обязан делать
#   ./deploy/probe-remote.sh --red      только красный: нарочные поломки
#   ./deploy/probe-remote.sh --live     ЖИВОЙ прогон на настоящем втором ресурсе
#
# ┌─────────────────────────────────────────────────────────────────────────────────────┐
# │ ЧТО ЭТА ПРОБА СТЕРЕЖЁТ                                                               │
# │                                                                                      │
# │  · ВЕЩЬ ОПИСАНА РЕЦЕПТОМ, А НЕ ЗАШИТА: проба кладёт ВТОРОЙ рецепт — чужой образ,     │
# │    чужое имя контейнера, чужой порт — и поднимает его тем же путём. Потребовалась    │
# │    бы правка кода — вещь была зашита, и это дефект (`WORLD2` 3.7);                   │
# │  · ГЛАВНОЕ СВОЙСТВО: на ту машину не кладётся НИЧЕГО. Ни `scp`, ни команд по ssh     │
# │    сверх проверки доступа, ни файлов — только разговор с чужим демоном докера        │
# │    (`TECH` 2). Это проверяется по журналу вызовов, то есть по тому, чего не было;    │
# │  · ступени НЕ СХЛОПЫВАЮТСЯ: «ключ не принят» и «докера на той стороне нет» — разные  │
# │    отказы с разными кодами, потому что чинят их разные люди (`WORLD2` 2.3);          │
# │  · образ не везётся второй раз, когда он там уже лежит;                              │
# │  · снятие убирает и контейнер на той стороне, и контекст на этой, а состояние        │
# │    (тома) — только когда об этом попросили явно.                                     │
# │                                                                                      │
# │ ДОКЕР И SSH ЗДЕСЬ ПОДСТАВНЫЕ — скрипты-заглушки в `PATH`, которые пишут журнал       │
# │ вызовов и отвечают по сценарию. Ни одного контейнера не запускается, ни одного       │
# │ ресурса не трогается. Это сказано прямо, а не подразумевается.                       │
# │                                                                                      │
# │ ЗЕЛЁНАЯ ЭТА ПРОБА НЕ ЗНАЧИТ, ЧТО ДВЕРЬ ВСТАЁТ НА ЧУЖОМ РЕСУРСЕ. Она стережёт         │
# │ ветвление и отказы. Живой прогон — `--live`, и он ничем не заменяется.               │
# └─────────────────────────────────────────────────────────────────────────────────────┘
#
#   код 0   полный зелёный: прогнано всё, включая живой прогон на втором ресурсе
#   код 1   краснота: инструмент повёл себя не так, как обязан
#   код 3   НЕПОЛНЫЙ ПРОГОН: часть проверок не выполнялась, и названо — какая
#
# Зелёного при неполном прогоне не бывает: «проверили, что смогли» и «проверили» — разные
# слова (`WORLD2` 4.2, пункт 5: чего не удалось измерить, не записывается вовсе).
#
# Живой прогон — на настоящем втором ресурсе, где есть докер и ssh с ключом:
#
#   WORLD_PROBE_REMOTE_ADDR=world@10.8.0.5 ./deploy/probe-remote.sh --live
#
# ОН РАЗРУШИТЕЛЕН ДЛЯ МИРА, СТОЯЩЕГО НА ТОМ РЕСУРСЕ: поднимает дверь под своим именем
# ресурса и снимает её вместе с томами и образом. Мир на том ресурсе снимается тем же
# `compose down` — если он там нужен, живой прогон гоняют на другой машине.
#
# Проверки зовутся косвенно, по имени в аргументе; линтер читает это как «функция не вызвана».
# shellcheck disable=SC2329
set -uo pipefail   # без -e: проба сама разбирает коды возврата, падать ей нельзя
export LC_ALL=C

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REMOTE="$HERE/remote.sh"
# ЭТО ОЖИДАНИЯ ПРОБЫ ПРО РЕЦЕПТ ДВЕРИ, А НЕ ЗНАНИЕ МЕХАНИКИ. Проба вправе знать, что лежит в
# `deploy/compose.yaml`, — она его и стережёт: имя образа и имя контейнера обязаны приезжать
# ОТТУДА. Инструменту знать их неоткуда, и отдельная проверка следит, чтобы они не вернулись
# в `remote.sh` константами.
IMAGE=ghcr.io/omnifield/world:latest
NET=omnifield-gateway
DOOR=world-door
# Имя ресурса для живого прогона. Своё и приметное: контекст `world-probe` не спутать с
# ресурсом человека, а `drop` в конце прогона убирает именно его.
LIVE_NAME="${WORLD_PROBE_REMOTE_NAME:-probe}"
LIVE_ADDR="${WORLD_PROBE_REMOTE_ADDR:-}"

mode=--both
case "${1:-}" in
    --green|--red|--both|--live) mode="$1" ;;
    -h|--help)
        sed -n '2,45p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    "") ;;
    *)  printf 'непонятный ключ: %s (см. --help)\n' "$1" >&2; exit 2 ;;
esac

total=0; failed=0; skipped=0

part()   { printf '\n\033[1m%s\033[0m\n' "$*" >&2; }
detail() { printf '      %s\n' "$*" >&2; }
ok()     { total=$((total + 1)); printf '  \033[32m✔\033[0m %s\n' "$1" >&2; }
bad()    { total=$((total + 1)); failed=$((failed + 1))
           printf '  \033[31m✘\033[0m %s\n' "$1" >&2
           shift; local l; for l in "$@"; do detail "$l"; done; }
skip()   { skipped=$((skipped + 1)); printf '  \033[33m•\033[0m %s — НЕ ПРОГНАНО\n' "$1" >&2
           shift; local l; for l in "$@"; do detail "$l"; done; }

# ------------------------------------------------------------------ подставные инструменты
# Отдельными файлами в PATH, а не функциями: `remote.sh` — отдельный процесс, и функция до
# него не доехала бы. Файлы рождаются на прогон и умирают вместе с ним.
STUB_DIR="$(mktemp -d)"
LOG="$STUB_DIR/journal"

cleanup() { rm -rf "$STUB_DIR"; return 0; }
trap cleanup EXIT

cat > "$STUB_DIR/docker" <<'STUB'
#!/usr/bin/env bash
# ПОДСТАВНОЙ докер: пишет журнал вызовов и отвечает по сценарию. Контейнеров не запускает,
# на чужие машины не ходит. Сценарий задаётся переменными STUB_* — их выставляет проба.
#
# РЕЦЕПТ ОН ЧИТАЕТ ПО-НАСТОЯЩЕМУ — тот файл, который ему назвали ключом `-f`. Иначе проба
# зеленела бы на подставе: подмени рецепт, а ответы те же — и «вещь берётся из рецепта»
# осталось бы словами (`WORLD2` 4.2).
printf 'docker %s\n' "$*" >> "${STUB_LOG:-/dev/null}"

RECIPE=""; prev=""; LAST=""; PROJ_FILTER=""
for a in "$@"; do
    [ "$prev" = "-f" ] && RECIPE="$a"
    case "$a" in label=com.docker.compose.project=*) PROJ_FILTER="${a##*=}" ;; esac
    prev="$a"; LAST="$a"
done

# proj — имя проекта, как его вывел бы компоуз: `name:` из рецепта, иначе имя файла.
proj() {
    local n=""
    [ -n "$RECIPE" ] && [ -f "$RECIPE" ] && n="$(sed -n 's/^name: *//p' "$RECIPE" | head -n1)"
    [ -n "$n" ] || { n="$(basename -- "${RECIPE%.*}")"; }
    printf '%s' "$n"
}
# ids — по контейнеру на каждую службу с образом. Столько же, сколько назвал рецепт.
ids() {
    local n i
    n="$(grep -c '^ *image:' "$RECIPE" 2>/dev/null || printf 0)"
    [ "$n" -gt 0 ] || return 0
    for i in $(seq 1 "$n"); do printf '%s-cid%s\n' "$(proj)" "$i"; done
}

CTX=""
if [ "${1:-}" = "--context" ]; then CTX="${2:-}"; shift 2; fi

case "$1" in
    info)     exit "${STUB_LOCAL_DAEMON:-0}" ;;
    version)
        # `version` без контекста здесь не спрашивают; с контекстом — это вопрос ЧУЖОМУ
        # демону, и он отвечает по сценарию.
        [ -n "$CTX" ] || exit 0
        [ "${STUB_REMOTE_DOCKER:-1}" = 1 ] || { printf 'error during connect: Cannot connect to the Docker daemon\n' >&2; exit 1; }
        printf '27.5.1\n'; exit 0 ;;
    context)
        case "$2" in
            inspect)
                # `context inspect <имя>` — есть ли контекст; с `--format` — его адрес.
                [ "${STUB_CTX_EXISTS:-0}" = 1 ] || exit 1
                case "$*" in *--format*) printf '%s\n' "${STUB_CTX_ENDPOINT:-ssh://world@10.8.0.5:22}" ;; esac
                exit 0 ;;
            create|update|rm) exit "${STUB_CTX_WRITE:-0}" ;;
            ls)
                [ "${STUB_CTX_EXISTS:-0}" = 1 ] || exit 0
                printf 'world-vps\tssh://world@10.8.0.5:22\n'; exit 0 ;;
        esac
        exit 0 ;;
    compose)
        case "$*" in
            *"config --images"*)
                # Имя образа ЧИТАЕТСЯ из рецепта. Нет его там — печатать нечего, и это ровно
                # тот случай, в котором инструмент обязан отказать, а не подставить своё.
                sed -n 's/^ *image: *//p' "$RECIPE" 2>/dev/null | head -n1; exit 0 ;;
            *config*)
                # Рецепт, который компоуз не разбирает: слово-метка в файле. Настоящий
                # компоуз спорит с отступами, подстановками и полями — заглушке хватит
                # одного признака, потому что проверяем мы ВЕТВЛЕНИЕ, а не разбор YAML.
                if grep -q 'СЛОМАНО' "$RECIPE" 2>/dev/null; then
                    printf 'validating %s: services must be a mapping\n' "$RECIPE" >&2; exit 1
                fi
                printf 'name: %s\n' "$(proj)"; exit 0 ;;
            *" ps -aq"*|*" ps -q"*) ids; exit 0 ;;
            *version*)           exit "${STUB_LOCAL_COMPOSE:-0}" ;;
            *" up "*|*" up")
                case "${STUB_UP:-ok}" in
                    ok)   printf 'Container world-door  Started\n'; exit 0 ;;
                    busy) printf 'Error response from daemon: driver failed programming external connectivity: Bind for 0.0.0.0:8080 failed: port is already allocated\n' >&2; exit 1 ;;
                    *)    printf 'Error response from daemon: something else entirely\n' >&2; exit 1 ;;
                esac ;;
            *" down"*) exit "${STUB_DOWN:-0}" ;;
            *logs*)    printf '(журнал двери)\n'; exit 0 ;;
        esac
        exit 0 ;;
    image)
        case "$2" in
            inspect)
                if [ -n "$CTX" ]; then exit $(( ${STUB_REMOTE_IMAGE:-0} == 1 ? 0 : 1 ))
                else exit $(( ${STUB_LOCAL_IMAGE:-1} == 1 ? 0 : 1 )); fi ;;
            rm) exit 0 ;;
        esac
        exit 0 ;;
    pull)
        # Реестр отвечает по сценарию. Отказ выглядит как отказ докера, а не как наш:
        # инструмент обязан разобрать ЧУЖОЙ ответ, а не свой собственный.
        [ "${STUB_PULL:-0}" = 0 ] && exit 0
        printf 'Error response from daemon: denied\n' >&2; exit 1 ;;
    save) printf 'подставной образ\n'; exit 0 ;;
    load) cat >/dev/null; printf 'Loaded image: ghcr.io/omnifield/world:latest\n'; exit "${STUB_LOAD:-0}" ;;
    network)
        case "$2" in
            inspect) exit $(( ${STUB_NET_EXISTS:-0} == 1 ? 0 : 1 )) ;;
            create)  exit "${STUB_NET_CREATE:-0}" ;;
            rm)      exit "${STUB_NET_RM:-0}" ;;
        esac
        exit 0 ;;
    inspect)
        # Один вызов, два вопроса: здоровье и имя контейнера. Имя выводим из проекта — то
        # есть из рецепта, а не из константы: спросили про чужую вещь — получили её имя.
        case "$*" in
            *".State.Health"*) printf '%s\n' "${STUB_HEALTH:-healthy}" ;;
            *".Name"*)         printf '/%s\n' "$LAST" ;;
        esac
        exit 0 ;;
    ps)
        case "$*" in
            # `ps -a --format '{{.Label "com.docker.compose.project"}}'` — какие ВЕЩИ вообще
            # стоят на ресурсе. Ответ идёт от демона, а не от нашего рецепта.
            *Label*) printf '%s\n' "${STUB_THINGS:-world}" ;;
            *)       printf 'Up 3 minutes (healthy)\n' ;;
        esac
        exit 0 ;;
    port)     printf '8080/tcp -> 0.0.0.0:%s\n' "${STUB_HOST_PORT:-8080}"; exit 0 ;;
    volume)
        # Тома ищутся по метке проекта — значит и отвечать надо от имени проекта.
        if [ -n "$PROJ_FILTER" ]; then
            printf '%s-field\n%s-stand\n' "$PROJ_FILTER" "$PROJ_FILTER"
        else
            printf 'world-field\nworld-stand\n'
        fi
        exit 0 ;;
esac
exit 0
STUB

cat > "$STUB_DIR/ssh" <<'STUB'
#!/usr/bin/env bash
# ПОДСТАВНОЙ ssh: пишет журнал вызовов и отвечает по сценарию. Никуда не ходит.
printf 'ssh %s\n' "$*" >> "${STUB_LOG:-/dev/null}"
[ "${STUB_SSH_OK:-1}" = 1 ] || { printf 'Permission denied (publickey).\n' >&2; exit 255; }
exit 0
STUB

# scp и rsync — заглушки-ловушки. Настоящих здесь звать некому, и если позовут, это увидит
# журнал: класть что-либо на чужую машину зона не вправе (`TECH` 2).
for trap_tool in scp rsync; do
    cat > "$STUB_DIR/$trap_tool" <<STUB
#!/usr/bin/env bash
printf '$trap_tool %s\n' "\$*" >> "\${STUB_LOG:-/dev/null}"
exit 0
STUB
done

chmod +x "$STUB_DIR"/*
PATH="$STUB_DIR:$PATH"; export PATH

# ------------------------------------------------------------------ ВТОРОЙ РЕЦЕПТ
# Ради него вся ступень и делалась: чужая вещь, ни одним словом не совпадающая с дверью.
# Своё имя проекта, свой образ, своё имя контейнера, свой порт. Если она поднимается тем же
# путём и без единой правки кода — вещь описана рецептом, а не зашита (`WORLD2` 3.7).
#
# Файл временный: класть второй рецепт в зону значило бы завести вещь, которой никто не
# заказывал. Проверяем МЕХАНИКУ, а не пополняем каталог.
OTHER="$STUB_DIR/scales.yaml"
cat > "$OTHER" <<'RECIPE'
name: scales
services:
  scales:
    image: ghcr.io/example/scales:v1
    container_name: scales-box
    ports:
      - "9099:9099"
RECIPE

OTHER_IMAGE=ghcr.io/example/scales:v1

# Рецепт, который компоуз не разберёт, и рецепт без имени образа — две РАЗНЫЕ поломки.
BROKEN="$STUB_DIR/сломанный.yaml"
printf 'name: СЛОМАНО\nservices: [это не отображение]\n' > "$BROKEN"
NOIMAGE="$STUB_DIR/безобраза.yaml"
printf 'name: безобраза\nservices:\n  что-то:\n    ports:\n      - "1:1"\n' > "$NOIMAGE"

# ------------------------------------------------------------------ прогон инструмента
# run <сценарий…> -- <аргументы remote.sh> — прогнать `remote.sh` с чистым журналом.
# Вывод целиком кладём в OUT, журнал вызовов — в JOURNAL: проба смотрит на обе вещи, потому
# что отказ обязан быть и читаемым, и разбираемым, а ветвление видно только по журналу.
OUT=""; JOURNAL=""; RC=0
run() {
    local -a env=()
    while [ "$1" != "--" ]; do env+=("$1"); shift; done
    shift
    : > "$LOG"
    OUT="$(env STUB_LOG="$LOG" "${env[@]}" "$REMOTE" "$@" 2>&1)"; RC=$?
    JOURNAL="$(cat "$LOG")"
}

# Сценарий по умолчанию: ресурс не заведён, ssh пускает, докер на той стороне есть,
# образа там нет, локально есть, сети нет, подъём удаётся, дверь здорова.
DEFAULT=(STUB_CTX_EXISTS=0 STUB_SSH_OK=1 STUB_REMOTE_DOCKER=1
         STUB_REMOTE_IMAGE=0 STUB_LOCAL_IMAGE=1 STUB_NET_EXISTS=0
         STUB_UP=ok STUB_HEALTH=healthy)

has()    { case "$JOURNAL" in *"$1"*) return 0 ;; esac; return 1; }
in_out() { case "$OUT" in *"$1"*) return 0 ;; esac; return 1; }

# want_refusal <код> <имя> <сценарий…> -- <аргументы> — инструмент ОБЯЗАН отказать этим кодом.
# Стережём КОД, а не формулировку: проба, привязанная к тексту, зеленеет на верной правке и
# учит не трогать буквы (`WORLD2` 4.2).
want_refusal() {
    local code="$1" name="$2"; shift 2
    run "$@"
    if in_out "REMOTE-REFUSAL: $code"; then
        ok "$name → $code"
    elif in_out "REMOTE-REFUSAL: "; then
        local got; got="$(printf '%s\n' "$OUT" | sed -n 's/^REMOTE-REFUSAL: //p' | head -n1)"
        bad "$name → ждали $code, получили $got" \
            "ступени схлопнулись: разные поломки чинят разные люди, и общий код отправляет чинить не туда"
    else
        bad "$name → отказа нет вовсе (код возврата $RC)" \
            "молчание хуже отказа: юзеру нечего чинить (WORLD2 2.3)"
    fi
}

# ------------------------------------------------------------------ зелёный прогон
green() {
    part "ЗЕЛЁНЫЙ ПРОГОН — что инструмент обязан делать (докер и ssh подставные)"

    run "${DEFAULT[@]}" -- add vps --addr world@10.8.0.5
    if [ "$RC" -eq 0 ]; then ok "подъём на чистом ресурсе прошёл целиком"
    else bad "подъём на чистом ресурсе не прошёл (код $RC)" "$OUT"; fi

    # Прежний путь не сломан: команда без ключей ставит дверь — рецепт двери остался
    # умолчанием КОМАНДЫ. Съедь это умолчание, и человек, поднимавший дверь одной строкой,
    # поднял бы неизвестно что.
    if has "compose -f $HERE/compose.yaml"; then ok "без --recipe берётся рецепт двери — прежний путь цел"
    else bad "умолчание команды съехало с рецепта двери" "$JOURNAL"; fi

    # Имя контекста ПРОИЗВОДНО от имени ресурса. Стереги мы «какое-то имя», инструмент мог
    # бы выдумывать его заново каждый раз, и `drop` перестал бы находить свой же ресурс.
    if has "context create world-vps"; then ok "контекст назван производно: vps → world-vps"
    else bad "контекста world-vps не создано" "имя контекста обязано выводиться из имени ресурса, а не выдумываться"; fi

    if has "ssh -o BatchMode=yes"; then ok "доступ проверен отдельно, ssh с BatchMode"
    else bad "доступ не проверялся ssh с BatchMode" "без BatchMode ssh спросит пароль и проверка повиснет вместо красноты"; fi

    # Порядок ступеней: доступ проверяется ДО того, как заводится контекст. Иначе на машине
    # человека остаётся контекст к ресурсу, который его не пустил.
    if [ "$(printf '%s\n' "$JOURNAL" | grep -n '^ssh ' | head -n1 | cut -d: -f1)" -lt \
         "$(printf '%s\n' "$JOURNAL" | grep -n 'context create' | head -n1 | cut -d: -f1)" ]; then
        ok "доступ проверяется ДО создания контекста"
    else
        bad "контекст заводится раньше проверки доступа" \
            "не пустивший ресурс оставил бы за собой контекст на машине человека"
    fi

    if has "save $IMAGE" && has "--context world-vps load"; then
        ok "образ едет ПОСТАВКОЙ КОПИИ: save здесь → load туда, через ssh"
    else
        bad "образ не поехал копией" "WORLD2-98: до решения user принята поставка копии, а не реестр"
    fi

    if has "--context world-vps network create $NET"; then ok "общая сеть создаётся НА ТОМ ресурсе"
    else bad "сеть $NET на ресурсе не создаётся" "войти в несуществующую сеть докер не даёт — первый подъём не встал бы"; fi

    if has "--context world-vps compose" && has " up -d --no-build"; then
        ok "дверь поднимается компоузом через контекст, без сборки на той стороне"
    else
        bad "подъём идёт не через контекст либо со сборкой" "сборка на чужом ресурсе потребовала бы там исходников — их туда не кладут"
    fi

    if has "--context world-vps inspect"; then ok "здоровье двери спрашивается у ЧУЖОГО демона"
    else bad "здоровье двери не проверялось" "«запущен» и «отвечает» — разные вещи"; fi

    # ГЛАВНАЯ проверка зоны: на ту машину не положено ничего.
    part "НА ТУ МАШИНУ НЕ ПОЛОЖЕНО НИЧЕГО (TECH 2)"
    if has "scp " || has "rsync "; then
        bad "файлы копируются на чужую машину (scp/rsync)" "на ресурсе должен появиться только контейнер и образ"
    else
        ok "ни scp, ни rsync — файлов на ту сторону не уезжает"
    fi

    # Единственная законная команда по ssh — проверка доступа (`true`). Всё остальное —
    # это уже работа НА чужой машине, а её мир не делает.
    local ssh_calls stray
    ssh_calls="$(printf '%s\n' "$JOURNAL" | grep '^ssh ' || true)"
    stray="$(printf '%s\n' "$ssh_calls" | grep -v ' true$' || true)"
    if [ -n "$stray" ]; then
        bad "по ssh идут команды сверх проверки доступа" "$stray" \
            "всё, что мир умеет, живёт в образе; ставить и делать что-либо на машине юзера нельзя"
    else
        ok "по ssh идёт ровно одна команда — проверка доступа"
    fi

    for word in "apt" "yum" "curl http" "git clone" "mkdir" "install"; do
        if printf '%s\n' "$ssh_calls" | grep -q -- "$word"; then
            bad "на чужой машине выполняется «$word»" "машина юзера остаётся его, а не нашей свалкой"
        fi
    done
    ok "ни apt, ни git clone, ни установки чего-либо на той стороне"

    part "ВТОРАЯ ВЕЩЬ ПОДНИМАЕТСЯ ТЕМ ЖЕ ПУТЁМ — без единой правки кода (WORLD2 3.7)"
    # Это главная проверка ступени. Она отвечает на один вопрос: описана вещь рецептом или
    # всё-таки зашита. Второй рецепт не совпадает с дверью ничем — ни именем проекта, ни
    # образом, ни именем контейнера, ни портом.
    run "${DEFAULT[@]}" -- add vps --addr world@10.8.0.5 --recipe "$OTHER"
    if [ "$RC" -eq 0 ]; then ok "чужая вещь встала тем же add — правок в коде не потребовалось"
    else bad "чужая вещь по своему рецепту не встала (код $RC)" "$OUT" \
        "потребовала правки — значит вещь была зашита, а не описана, и это дефект (WORLD2 3.7)"; fi

    if has "compose -f $OTHER"; then ok "подъём идёт по НАЗВАННОМУ рецепту"
    else bad "подъём пошёл не по названному рецепту" "$JOURNAL"; fi

    if has "save $OTHER_IMAGE"; then ok "везётся образ из рецепта: $OTHER_IMAGE"
    else bad "образ второй вещи не поехал" "имя образа обязано читаться из рецепта, а не из константы"; fi

    if has "$IMAGE"; then
        bad "в подъёме чужой вещи всплыл образ двери ($IMAGE)" \
            "механика подставляет своё умолчание вместо того, что назвал рецепт"
    else
        ok "образа двери в подъёме чужой вещи нет ни разу"
    fi

    # Здоровье спрашивается у контейнеров ЭТОГО проекта, а не у имени, известного заранее.
    if has "scales-cid1"; then ok "здоровье спрашивается у контейнера этой вещи (его назвал компоуз)"
    else bad "здоровье спрашивалось не у контейнеров рецепта" "$JOURNAL" \
        "имя контейнера — свойство рецепта; знать его механике неоткуда"; fi

    if in_out "scales"; then ok "имя вещи в отчёте взято из рецепта"
    else bad "в отчёте нет имени вещи из рецепта" "$OUT"; fi
    if in_out "world-door" || in_out "дверь стоит"; then
        bad "отчёт про чужую вещь говорит про дверь" "$OUT"
    else
        ok "в отчёте про чужую вещь двери не поминают вовсе"
    fi

    part "СНЯТЬ И СПРОСИТЬ — ТОЖЕ ПО РЕЦЕПТУ, А НЕ ПО ЗАШИТОМУ ИМЕНИ"
    run STUB_CTX_EXISTS=1 STUB_CTX_ENDPOINT=ssh://world@10.8.0.5:22 STUB_REMOTE_DOCKER=1 \
        STUB_NET_EXISTS=1 -- drop vps --recipe "$OTHER" --with-image
    if [ "$RC" -eq 0 ] && has "compose -f $OTHER" && has " down"; then
        ok "снимается вещь названного рецепта"
    else
        bad "снятие пошло не по рецепту (код $RC)" "$JOURNAL"
    fi
    if has "image rm $OTHER_IMAGE"; then ok "--with-image снимает образ ИЗ РЕЦЕПТА"
    else bad "снят не тот образ" "$JOURNAL" "имя образа при снятии обязано быть тем же, что при подъёме"; fi

    run STUB_CTX_EXISTS=1 STUB_CTX_ENDPOINT=ssh://world@10.8.0.5:22 STUB_REMOTE_DOCKER=1 \
        STUB_REMOTE_IMAGE=1 STUB_NET_EXISTS=1 STUB_THINGS=scales -- status vps --recipe "$OTHER"
    if [ "$RC" -eq 0 ] && in_out "scales"; then ok "status говорит про вещь названного рецепта"
    else bad "status не назвал вещь рецепта (код $RC)" "$OUT"; fi
    if in_out "вещи там"; then ok "status называет и то, ЧТО ВООБЩЕ стоит на ресурсе — отдельным вопросом"
    else bad "status не говорит, какие вещи стоят на ресурсе" "$OUT" \
        "ресурс — машина, до которой дотянулись; что на ней стоит, выводить из нашего рецепта нельзя"; fi

    part "В МЕХАНИКЕ НЕ ОСТАЛОСЬ ЗАШИТОЙ ВЕЩИ"
    # Сторож на будущее: вернуть в путь подъёма имя контейнера двери или её образ — и проба
    # покраснеет здесь. Зелёная проба, которой больше нечего проверять, хуже отсутствующей
    # (`WORLD2` 4.2), поэтому старую сверку констант заменили на обратную: их быть НЕ ДОЛЖНО.
    hard="$(grep -n 'world-door\|ghcr\.io/omnifield/world' "$REMOTE" || true)"
    if [ -z "$hard" ]; then
        ok "в remote.sh нет ни имени контейнера двери, ни её образа"
    else
        bad "в remote.sh вернулась зашитая вещь" "$hard" \
            "имя контейнера и образ принадлежат рецепту: вторая вещь потребовала бы правки кода"
    fi

    part "ОБРАЗ НЕ ВЕЗЁТСЯ ДВАЖДЫ"
    run STUB_CTX_EXISTS=1 STUB_CTX_ENDPOINT=ssh://world@10.8.0.5:22 STUB_SSH_OK=1 \
        STUB_REMOTE_DOCKER=1 STUB_REMOTE_IMAGE=1 STUB_LOCAL_IMAGE=1 STUB_NET_EXISTS=1 \
        STUB_UP=ok STUB_HEALTH=healthy -- add vps
    if has "save $IMAGE"; then
        bad "образ везётся, хотя на ресурсе он уже есть" "поставка копии — самый долгий шаг; повторять его без нужды значит платить минутами за ничто"
    else
        ok "образ на ресурсе есть — не везём"
    fi
    if [ "$RC" -eq 0 ]; then ok "заведённому ресурсу адрес повторно не нужен — он в контексте"
    else bad "повторный add без адреса не прошёл (код $RC)" "$OUT"; fi

    run STUB_CTX_EXISTS=1 STUB_CTX_ENDPOINT=ssh://world@10.8.0.5:22 STUB_SSH_OK=1 \
        STUB_REMOTE_DOCKER=1 STUB_REMOTE_IMAGE=1 STUB_LOCAL_IMAGE=1 STUB_NET_EXISTS=1 \
        STUB_UP=ok STUB_HEALTH=healthy -- add vps --resend-image
    if has "save $IMAGE"; then ok "--resend-image везёт образ заново"
    else bad "--resend-image образ не повёз" "ключ есть, а свойства нет — это хуже отсутствия ключа"; fi

    # ДВА СПОСОБА ДОСТАВКИ ОДНОЙ ВЕЩИ (`WORLD2` 1.5). Реестр стал возможен только с полным
    # именем образа (`WORLD2-121`): пока имя было коротким, `docker pull` на той стороне
    # пошёл бы в Docker Hub — не туда, куда мы публикуем.
    part "ВТОРОЙ ПУТЬ ДОСТАВКИ — реестр (--pull), а не замена копии"
    run "${DEFAULT[@]}" -- add vps --addr world@10.8.0.5 --pull
    if [ "$RC" -eq 0 ]; then ok "подъём с --pull прошёл целиком"
    else bad "подъём с --pull не прошёл (код $RC)" "$OUT"; fi
    if has "--context world-vps pull $IMAGE"; then ok "образ тянет САМ РЕСУРС, из реестра"
    else bad "образ из реестра не тянулся" "ключ есть, а свойства нет — это хуже отсутствия ключа"; fi
    if has "save $IMAGE"; then
        bad "образ поехал ещё и копией" "два способа доставки не складываются: канал занят зря, а приедет всё равно одно"
    else
        ok "копия при этом НЕ везётся — путь один за прогон"
    fi

    # Реестр снимает требование иметь образ ЗДЕСЬ: тянет его та машина, а не эта.
    run STUB_CTX_EXISTS=0 STUB_SSH_OK=1 STUB_REMOTE_DOCKER=1 STUB_REMOTE_IMAGE=0 \
        STUB_LOCAL_IMAGE=0 STUB_NET_EXISTS=0 STUB_UP=ok STUB_HEALTH=healthy \
        -- add vps --addr world@10.8.0.5 --pull
    if [ "$RC" -eq 0 ]; then ok "локального образа не нужно вовсе — с --pull его берут в реестре"
    else bad "с --pull подъём требует образа на этой машине (код $RC)" "$OUT" \
        "весь смысл реестра в том, что вещь берут не у нас"; fi

    # А умолчание осталось прежним — копия. Это проверяется отдельно, потому что «добавили
    # ключ и заодно перевернули умолчание» — самая тихая из возможных правок.
    run "${DEFAULT[@]}" -- add vps --addr world@10.8.0.5
    if has "save $IMAGE" && ! has "pull $IMAGE"; then
        ok "без ключа умолчание прежнее: копия, а не реестр"
    else
        bad "умолчание съехало на реестр" \
            "копия возит ровно то, что у тебя на руках; подмена её опубликованным образом видна только по digest'у"
    fi

    # Образ без HEALTHCHECK — это НЕ отказ и НЕ «здорова»: третье состояние, и оно называется
    # вслух. Вещь, у которой проверки нет вовсе, обязана вставать (`WORLD2` 4.2).
    run STUB_CTX_EXISTS=0 STUB_SSH_OK=1 STUB_REMOTE_DOCKER=1 STUB_REMOTE_IMAGE=1 \
        STUB_LOCAL_IMAGE=1 STUB_NET_EXISTS=1 STUB_UP=ok STUB_HEALTH=none \
        -- add vps --addr world@10.8.0.5 --recipe "$OTHER"
    if [ "$RC" -eq 0 ] && in_out "HEALTHCHECK"; then
        ok "вещь без HEALTHCHECK встаёт, и непроверенное названо непроверенным"
    else
        bad "вещь без HEALTHCHECK не встала либо промолчала об этом (код $RC)" "$OUT" \
            "чужая вещь не обязана иметь нашу проверку здоровья; выдать «встало» за «отвечает» тоже нельзя"
    fi

    part "СНЯТИЕ — на той стороне контейнер, на этой контекст"
    run STUB_CTX_EXISTS=1 STUB_CTX_ENDPOINT=ssh://world@10.8.0.5:22 STUB_REMOTE_DOCKER=1 \
        STUB_NET_EXISTS=1 -- drop vps
    if [ "$RC" -eq 0 ]; then ok "снятие прошло целиком"
    else bad "снятие не прошло (код $RC)" "$OUT"; fi
    if has "--context world-vps compose" && has " down"; then ok "контейнер снимается на том ресурсе"
    else bad "контейнер на ресурсе не снимается" "одна команда ставит — одна снимает, иначе за миром надо убирать руками"; fi
    if has "context rm world-vps"; then ok "контекст убирается с ЭТОЙ машины"
    else bad "контекст остаётся на машине человека" "list врал бы про ресурс, которого уже нет"; fi
    if has "--context world-vps network rm $NET"; then ok "общая сеть снимается, если свободна"
    else bad "сеть на ресурсе не снимается" "след остался бы там, где обещана чистота"; fi
    if printf '%s\n' "$JOURNAL" | grep -q 'down.*-v'; then
        bad "тома сносятся без спроса" "состояние поля — не мусор: стирать его молча нельзя"
    else
        ok "тома НЕ сносятся без --with-state — состояние поля переживает снятие"
    fi

    run STUB_CTX_EXISTS=1 STUB_CTX_ENDPOINT=ssh://world@10.8.0.5:22 STUB_REMOTE_DOCKER=1 \
        STUB_NET_EXISTS=1 -- drop vps --with-state --with-image
    if printf '%s\n' "$JOURNAL" | grep -q 'down.*-v'; then ok "--with-state сносит и тома"
    else bad "--with-state тома не снёс" "ключ есть, а свойства нет"; fi
    if has "--context world-vps image rm"; then ok "--with-image снимает образ с ресурса"
    else bad "--with-image образ не снял" "после него на машине не должно остаться и образа"; fi

    part "СПИСОК И СОСТОЯНИЕ"
    run STUB_CTX_EXISTS=1 -- list
    if in_out "vps" && [ "$RC" -eq 0 ]; then ok "list показывает заведённые ресурсы"
    else bad "list ресурсов не показал (код $RC)" "$OUT"; fi

    run STUB_CTX_EXISTS=1 STUB_CTX_ENDPOINT=ssh://world@10.8.0.5:22 STUB_REMOTE_DOCKER=1 \
        STUB_REMOTE_IMAGE=1 STUB_NET_EXISTS=1 -- status vps
    # Состояние спрашивается у ЧУЖОГО демона по каждой вещи отдельно: выведенное состояние
    # врёт ровно тогда, когда на него смотрят (`WORLD2` 4.2).
    if [ "$RC" -eq 0 ] && in_out "контейнер" && in_out "образ" && in_out "здоровье"; then
        ok "status говорит, что на ресурсе сейчас"
    else
        bad "status не назвал состояние ресурса (код $RC)" "$OUT"
    fi
    # Состояние вещи — её тома, и имена их читаются по метке проекта, а не по нашему списку.
    if in_out "world-field"; then ok "тома вещи названы по факту, а не по зашитому списку"
    else bad "тома вещи не названы" "$OUT" "своего списка томов у механики нет — он принадлежит рецепту"; fi
}

# ------------------------------------------------------------------ красный прогон
red() {
    part "КРАСНЫЙ ПРОГОН — нарочные поломки, у каждой СВОЙ код"

    want_refusal no-name "имя ресурса не названо" "${DEFAULT[@]}" -- add
    want_refusal bad-name "имя ресурса не годится" "${DEFAULT[@]}" -- add "ВПС"
    want_refusal no-address "ресурс не заведён и адрес не назван" "${DEFAULT[@]}" -- add vps
    want_refusal bad-address "адрес похож на IPv6" "${DEFAULT[@]}" -- add vps --addr "world@fe80::1:22"
    # ТРИ ОТКАЗА ПРО РЕЦЕПТ, а не один «плохой рецепт»: файла нет · файл не разбирается · в
    # разобранном не названо имя образа. Чинятся они разными действиями, и общий код отправлял
    # бы человека чинить не то (`WORLD2` 2.3).
    want_refusal no-recipe "рецепта, которого нет" "${DEFAULT[@]}" \
        -- add vps --addr world@10.8.0.5 --recipe "$STUB_DIR/такого-рецепта-нет.yaml"
    want_refusal bad-recipe "рецепт, который компоуз не разбирает" "${DEFAULT[@]}" \
        -- add vps --addr world@10.8.0.5 --recipe "$BROKEN"
    want_refusal recipe-no-image "рецепт без имени образа" "${DEFAULT[@]}" \
        -- add vps --addr world@10.8.0.5 --recipe "$NOIMAGE"
    # Отказ про рецепт обязан случиться ДО того, как мы тронули ресурс: непригодный рецепт не
    # вправе оставить за собой ни контекста, ни соединения.
    if has "context create" || has "ssh "; then
        bad "отказ по рецепту случился, когда ресурс уже трогали" "$JOURNAL" \
            "рецепт читается до первого шага наружу: чинить его — работа на этой машине"
    else
        ok "отказ по рецепту — до первого касания ресурса"
    fi

    want_refusal access-denied "ключ не принят" \
        STUB_CTX_EXISTS=0 STUB_SSH_OK=0 STUB_REMOTE_DOCKER=1 STUB_LOCAL_IMAGE=1 \
        -- add vps --addr world@10.8.0.5
    want_refusal no-remote-docker "ssh пускает, а докера на той стороне нет" \
        STUB_CTX_EXISTS=0 STUB_SSH_OK=1 STUB_REMOTE_DOCKER=0 STUB_LOCAL_IMAGE=1 \
        -- add vps --addr world@10.8.0.5
    want_refusal no-image "образа нет ни там, ни здесь" \
        STUB_CTX_EXISTS=0 STUB_SSH_OK=1 STUB_REMOTE_DOCKER=1 STUB_REMOTE_IMAGE=0 \
        STUB_LOCAL_IMAGE=0 STUB_LOCAL_DAEMON=0 STUB_NET_EXISTS=1 \
        -- add vps --addr world@10.8.0.5
    # Реестр отказал — это СВОЯ ступень, а не «образ не доехал»: чинится она не повтором
    # передачи, а доступом той машины к реестру либо открытием пакета.
    want_refusal image-pull-failed "ресурсу не дают притянуть образ из реестра" \
        STUB_CTX_EXISTS=0 STUB_SSH_OK=1 STUB_REMOTE_DOCKER=1 STUB_REMOTE_IMAGE=0 \
        STUB_LOCAL_IMAGE=1 STUB_NET_EXISTS=1 STUB_PULL=1 \
        -- add vps --addr world@10.8.0.5 --pull
    want_refusal port-busy "порт на том ресурсе занят" \
        STUB_CTX_EXISTS=0 STUB_SSH_OK=1 STUB_REMOTE_DOCKER=1 STUB_REMOTE_IMAGE=1 \
        STUB_LOCAL_IMAGE=1 STUB_NET_EXISTS=1 STUB_UP=busy \
        -- add vps --addr world@10.8.0.5
    want_refusal no-such-resource "снимают ресурс, которого не заводили" \
        STUB_CTX_EXISTS=0 -- drop vps
    # «Запущено» и «отвечает» — разные вещи, и вторую мы обязаны дождаться либо отказать.
    # Ждём ВСЕ контейнеры рецепта, поэтому нездоровый обязан покраснеть, а не потеряться
    # среди здоровых соседей.
    want_refusal thing-silent "вещь запущена, а здоровой за отведённое время не стала" \
        WORLD_REMOTE_WAIT=0 STUB_CTX_EXISTS=0 STUB_SSH_OK=1 STUB_REMOTE_DOCKER=1 \
        STUB_REMOTE_IMAGE=1 STUB_LOCAL_IMAGE=1 STUB_NET_EXISTS=1 STUB_UP=ok \
        STUB_HEALTH=unhealthy -- add vps --addr world@10.8.0.5

    part "ОТКАЗ ОБЯЗАН НАЗЫВАТЬ ВЫХОД, А НЕ ТОЛЬКО ПРИЧИНУ (WORLD2 2.3)"
    run STUB_CTX_EXISTS=0 STUB_SSH_OK=0 STUB_REMOTE_DOCKER=1 STUB_LOCAL_IMAGE=1 \
        -- add vps --addr world@10.8.0.5
    if in_out "выход: "; then ok "у отказа есть выход, а не только диагноз"
    else bad "отказ без выхода" "«нельзя» без выхода — провал самого мира: юзеру нечего чинить"; fi

    part "ОТКАЗ НИЧЕГО НЕ ЛОМАЕТ (WORLD2 2.3, пункт 4)"
    # Ресурс не пустил — значит на нём не должно оказаться ни сети, ни контейнера, а на
    # машине человека — ни контекста. Попытка не состоялась целиком, а не наполовину.
    if has "context create" || has "network create" || has " up -d"; then
        bad "после отказа остались следы" "$JOURNAL" \
            "попытка обязана не состояться целиком, а не наполовину"
    else
        ok "после отказа не создано ни контекста, ни сети, ни контейнера"
    fi
}

# ------------------------------------------------------------------ живой прогон
# Единственная проверка, которая отвечает на вопрос задачи: встаёт ли дверь на чистом
# ресурсе. Подставной докер на него не отвечает и отвечать не может.
live() {
    part "ЖИВОЙ ПРОГОН на настоящем втором ресурсе"

    # Подставные инструменты убираем: здесь нужен настоящий докер и настоящий ssh.
    local real_path="${PATH#$STUB_DIR:}"

    if [ -z "$LIVE_ADDR" ]; then
        skip "дверь встаёт на чистом ресурсе" \
            "второго ресурса не названо — назови адрес и повтори:" \
            "  WORLD_PROBE_REMOTE_ADDR=world@10.8.0.5 ./deploy/probe-remote.sh --live" \
            "на том ресурсе нужен только докер и ssh с ключом"
        return
    fi
    if ! PATH="$real_path" command -v docker >/dev/null 2>&1 \
       || ! PATH="$real_path" docker info >/dev/null 2>&1; then
        skip "дверь встаёт на чистом ресурсе" \
            "на ЭТОЙ машине нет живого докера — образ нечем взять и отправить" \
            "гоняй живой прогон там, где докер настоящий"
        return
    fi

    local rc out
    printf '  · поднимаю дверь на %s (имя ресурса %s)\n' "$LIVE_ADDR" "$LIVE_NAME" >&2
    out=$(PATH="$real_path" "$REMOTE" add "$LIVE_NAME" --addr "$LIVE_ADDR" 2>&1); rc=$?
    if [ "$rc" -eq 0 ]; then ok "дверь встала на чистом ресурсе одной командой"
    else bad "дверь на ресурсе не встала (код $rc)" "$out"; fi

    out=$(PATH="$real_path" "$REMOTE" status "$LIVE_NAME" 2>&1)
    case "$out" in
        *healthy*) ok "дверь на ресурсе здорова" ;;
        *)         bad "дверь на ресурсе не здорова" "$out" ;;
    esac

    printf '  · снимаю дверь вместе с состоянием и образом\n' >&2
    out=$(PATH="$real_path" "$REMOTE" drop "$LIVE_NAME" --with-state --with-image 2>&1); rc=$?
    if [ "$rc" -eq 0 ]; then ok "дверь снялась одной командой"
    else bad "дверь не снялась (код $rc)" "$out"; fi

    # Проверяем ЧУЖИМИ глазами — спрашиваем сам ресурс, а не свой контекст: контекста уже
    # нет, да и верить надо тому, что осталось на машине, а не тому, что мы о ней думаем.
    # Это ЧТЕНИЕ, а не работа на чужой машине: ничего туда не ставится и не кладётся.
    #
    # Адрес разбираем так же, как его разбирает сам инструмент: возьми мы `${ADDR%%:*}`,
    # ресурс с нестандартным ssh-портом проверялся бы на 22-м — и проверка «ничего не
    # осталось» краснела бы связью вместо того, чтобы отвечать на свой вопрос.
    local live_target="$LIVE_ADDR" live_port=22
    case "${live_target##*@}" in
        *:*) live_port="${live_target##*:}"; live_target="${live_target%:*}" ;;
    esac
    local left
    left=$(PATH="$real_path" ssh -o BatchMode=yes -p "$live_port" "$live_target" \
              "docker ps -a --filter name=$DOOR --format '{{.Names}}'; docker volume ls --format '{{.Name}}' | grep -E '^world-(field|stand)\$'" 2>&1)
    if [ -z "$(printf '%s' "$left" | tr -d '[:space:]')" ]; then
        ok "на ресурсе не осталось ни контейнера, ни томов"
    else
        bad "на ресурсе остались наши вещи" "$left" \
            "одна команда ставит — одна снимает; за миром не должно оставаться уборки"
    fi
}

# ------------------------------------------------------------------ прогон
case "$mode" in
    --green) green ;;
    --red)   red ;;
    --live)  live ;;
    --both)  green; red; live ;;
esac

part "ИТОГ"
printf '  проверок: %s, из них не сошлись: %s, НЕ ПРОГНАНО: %s\n' "$total" "$failed" "$skipped" >&2
printf '  докер и ssh в этом прогоне ПОДСТАВНЫЕ — контейнеров не запускалось ни одного\n' >&2

if [ "$failed" -gt 0 ]; then
    printf '\n\033[1;31m✘ краснота: %s из %s\033[0m\n' "$failed" "$total" >&2
    exit 1
fi
if [ "$skipped" -gt 0 ]; then
    printf '\n\033[1;33m• НЕПОЛНЫЙ ПРОГОН: %s не проверено\033[0m\n' "$skipped" >&2
    printf '  зелёного при неполном прогоне не бывает: «проверили, что смогли» и\n' >&2
    printf '  «проверили» — разные слова (WORLD2 4.2)\n' >&2
    exit 3
fi
printf '\n\033[1;32m✔ полный зелёный: %s из %s, включая живой прогон\033[0m\n' "$total" "$total" >&2
exit 0
