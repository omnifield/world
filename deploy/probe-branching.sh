#!/usr/bin/env bash
# Проба ВЕТВЛЕНИЯ подъёма и сборки. Работает там, где докера нет вообще.
#
#   ./deploy/probe-branching.sh
#
# ┌─────────────────────────────────────────────────────────────────────────────────────┐
# │ ЧТО ЭТА ПРОБА ПРОВЕРЯЕТ — И ЧЕГО НЕ ПРОВЕРЯЕТ. Читать до того, как считать её        │
# │ зелёный итог подтверждением чего-либо.                                              │
# │                                                                                      │
# │  · она проверяет ВЕТВЛЕНИЕ: что скрипт вызвал и, главное, чего НЕ вызвал —           │
# │    не спросил склад, не позвал сборку, не снёс прежнюю;                             │
# │  · она НЕ ПРОВЕРЯЕТ ПОДЪЁМ. Встал ли мир, ответила ли дверь, собрался ли образ —     │
# │    об этом здесь не знают вовсе;                                                    │
# │  · ЗЕЛЁНАЯ ЭТА ПРОБА НЕ ЗНАЧИТ, ЧТО МИР ПОДНИМАЕТСЯ. Живой прогон                    │
# │    (`./deploy/probe.sh`) обязателен отдельно и ничем не заменяется;                 │
# │  · ДОКЕР ЗДЕСЬ ПОДСТАВНОЙ — скрипт-заглушка в `PATH`, который пишет журнал вызовов   │
# │    и отвечает по сценарию. Он не запускает контейнеров и за докер себя не выдаёт:    │
# │    в отчёте это сказано прямо, а не подразумевается.                                │
# └─────────────────────────────────────────────────────────────────────────────────────┘
#
# Зачем она нужна, если есть живая проба. Развилка «образ есть — не собирать» по кодам
# возврата НЕОТЛИЧИМА от той, что собирает всегда: оба исхода зелёные. Отличить можно
# только по журналу вызовов, то есть по тому, чего скрипт не делал. Регрессия здесь
# вернулась бы молча и вернулась бы в подъёме — там, где больно (`kb:WORLD-32`).
#
# И вторая причина: докера нет ни в одной нашей локации, а проверять надо каждый заход.
# Для владельца зоны это единственная доступная проверка вообще — до самого хоста.
#
# ЧТО ОНА ТРОГАЕТ НА ДИСКЕ: `deploy/.build/web-dist` — сценарии сборки кладут туда
# подставной результат. Настоящая сборка, если она там лежала, отодвигается в сторону и
# возвращается на место в конце (в том числе при прерывании). Больше ничего.
#
# Имена переменных латинские: bash не поддерживает не-ASCII в именах переменных.
set -uo pipefail   # без -e: проба сама разбирает коды возврата, падать ей нельзя

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd -- "$HERE/.." && pwd)"
OUT="$HERE/.build/web-dist"
PORT="${WORLD_PROBE_BRANCH_PORT:-18080}"
NODE_IMAGE=node:24-alpine   # по нему заглушка отличает сборщик пульта от проверки склада
# Адрес подставной двери — он же адрес, который проба подставляет прогоняемым скриптам
# (`WORLD_HOST`). Пинуется здесь НАРОЧНО: адрес проверки настраивается (`tasker:WORLD-97`),
# и не подставь мы свой, прогон в окружении с чужим `WORLD_HOST` краснел бы весь — скрипты
# стучались бы по адресу машины, а подставная дверь стоит здесь.
DOOR_HOST=127.0.0.1
# Адрес, на котором заведомо никто не слушает: по нему проверяется, что стук ушёл ТУДА,
# КУДА СКАЗАЛИ, а не по умолчанию. Петля, а не чужая машина: за пределы хоста проба не ходит.
NOWHERE_HOST=127.0.0.9

usage() {
    cat <<'USAGE'
Проба ветвления: ./deploy/probe-branching.sh

  Прогоняет up.sh и build.sh на ПОДСТАВНОМ докере и смотрит журнал вызовов:
  что было вызвано и чего вызвано не было. Докера на машине не требует.

  ГРАНИЦЫ, за которые эта проба не отвечает:
    · она проверяет ВЕТВЛЕНИЕ, а не подъём: встал ли мир — здесь не знают;
    · ЗЕЛЁНАЯ ОНА НЕ ЗНАЧИТ, ЧТО МИР ПОДНИМАЕТСЯ — живой прогон
      (./deploy/probe.sh) обязателен отдельно и ничем не заменяется;
    · докер подставной, контейнеров не запускается ни одного.

  Трогает на диске: deploy/.build/web-dist (настоящая сборка отодвигается
  и возвращается в конце). Разрушительного для живого мира — ничего:
  ни портов, ни контейнеров, ни поля.

  -h, --help   эта подсказка

Переменные:
  WORLD_PROBE_BRANCH_PORT=18080   порт подставной двери (мир на нём не поднимается)
USAGE
}

case "${1:-}" in
    -h|--help) usage; exit 0 ;;
    "")        ;;
    *)         usage >&2; printf '\nнепонятный ключ: %s\n' "$1" >&2; exit 2 ;;
esac

total=0; failed=0; skipped=0
part()   { printf '\n\033[1m%s\033[0m\n' "$*" >&2; }
detail() { printf '      %s\n' "$*" >&2; }
good()   { total=$((total+1)); printf '  \033[32m✔\033[0m %s\n' "$*" >&2; }
bad()    { total=$((total+1)); failed=$((failed+1)); printf '  \033[31m✘\033[0m %s\n' "$*" >&2; }
skip()   { skipped=$((skipped+1)); printf '  \033[33m∅\033[0m %s\n' "$*" >&2; }

# ------------------------------------------------------------------ подставной докер
# Отдельным файлом в PATH, а не функцией: `up.sh` зовёт `build.sh` отдельным процессом,
# и функция до него не доехала бы. Файл рождается на прогон и умирает вместе с ним —
# храниться ему негде и незачем.
STUB_DIR="$(mktemp -d)"
DOOR_PID=

cleanup() {
    [ -n "$DOOR_PID" ] && kill "$DOOR_PID" 2>/dev/null
    rm -rf "$STUB_DIR"
    rm -rf "$OUT"
    [ -d "$OUT.saved" ] && mv "$OUT.saved" "$OUT"
    return 0
}
trap cleanup EXIT

[ -d "$OUT" ] && { rm -rf "$OUT.saved"; mv "$OUT" "$OUT.saved"; }

cat > "$STUB_DIR/docker" <<'STUB'
#!/usr/bin/env bash
# ПОДСТАВНОЙ докер: пишет журнал вызовов и отвечает по сценарию. Контейнеров не запускает.
printf '%s\n' "$*" >> "${STUB_LOG:-/dev/null}"
ALL="$*"   # вызов целиком — по нему отличается мир от локации после того, как ключи сдвинуты

emit_tar() {   # отдать в stdout настоящий tar с названными файлами
    d=$(mktemp -d)
    for f in "$@"; do mkdir -p "$d/$(dirname "$f")"; printf 'подстава\n' > "$d/$f"; done
    tar -C "$d" -cf - .
    rm -rf "$d"
}

case "$1" in
    info) exit 0 ;;
    network) exit 0 ;;
    pull) [ -n "${STUB_PULL_OK:-}" ] && exit 0 || exit 1 ;;
    image)
        case "$3" in
            omnifield/world:dev)    [ -n "${STUB_HAVE_IMAGE:-}" ]     && exit 0 || exit 1 ;;
            omnifield/location:dev) [ -n "${STUB_HAVE_LOC_IMAGE:-}" ] && exit 0 || exit 1 ;;
            *)                      [ -n "${STUB_HAVE_PROBE:-}" ]     && exit 0 || exit 1 ;;
        esac ;;
    exec)
        # `docker exec` в живой локации: стук в сторожа и команды входа в поле. Тем же
        # бинарём, что стоит внутри, — и заглушка отвечает за него по сценарию.
        case "$ALL" in
            *"/app/world join"*)  [ -n "${STUB_JOIN_OK:-}" ] \
                                      && { echo 'локация "podstava" вошла в поле'; exit 0; } \
                                      || { echo 'world join: подстава отказала' >&2; exit 1; } ;;
            *"/app/world leave"*) [ "${STUB_LEAVE_OK:-1}" = 1 ] \
                                      && { echo 'локация "podstava" снята с поля'; exit 0; } \
                                      || exit 1 ;;
            *wget*)               [ -n "${STUB_GUARD_READY:-}" ] && exit 0 || exit 1 ;;
            *)                    exit 0 ;;
        esac ;;
    compose)
        # Ключи до подкоманды: у мира это `-f ФАЙЛ`, у локации ещё `-p ИМЯ` и
        # `--env-file ФАЙЛ`. Сдвигаем парами, а не на фиксированное число: иначе заглушка
        # знала бы ровно один вызов из двух и молча отвечала бы не на ту подкоманду.
        shift
        while [ $# -gt 0 ]; do
            case "$1" in -*) shift 2 ;; *) break ;; esac
        done
        case "$1" in
            config)
                case "$ALL" in
                    *location-compose.yaml*) echo "omnifield/location:dev" ;;
                    *)                       echo "omnifield/world:dev" ;;
                esac
                exit 0 ;;
            ps) echo "podstavnoy-kontejner-id"; exit 0 ;;
            up)
                # Значения, с которыми компоуз подставит имя двери в сети и настройку
                # локации, видны только в окружении процесса — в аргументах их нет. Пишем в
                # журнал отдельной строкой: иначе проверить, что настройка ДОЕХАЛА, нечем.
                printf 'ENV WORLD_DOOR_ALIAS=%s\n' "${WORLD_DOOR_ALIAS:-<нет>}" >> "${STUB_LOG:-/dev/null}"
                printf 'ENV WORLD_NAME=%s WORLD_GIVES=%s WORLD_DOOR=%s WORLD_NET=%s\n' \
                    "${WORLD_NAME:-<нет>}" "${WORLD_GIVES:-<нет>}" \
                    "${WORLD_DOOR:-<нет>}" "${WORLD_NET:-<нет>}" >> "${STUB_LOG:-/dev/null}"
                # Адрес застройки отдельной строкой: он необязателен, и «не заявлено» —
                # такой же проверяемый исход, как заявленный адрес.
                printf 'ENV WORLD_BUILD_ADDR=%s\n' \
                    "${WORLD_BUILD_ADDR:-<не заявлено>}" >> "${STUB_LOG:-/dev/null}"
                exit 0 ;;
            *)  exit 0 ;;
        esac ;;
    run)
        case "$*" in
            *"${STUB_NODE_IMAGE:?}"*)
                # сборщик пульта. Поток исходника обычно выпивается (`cat`), но случай
                # `deaf` — нарочно НЕ выпивает: так ведёт себя упавший сборщик, и хостовый
                # tar получает SIGPIPE. Проверяем, что отказ назовёт сборщик, а не подачу.
                case "${STUB_BUILDER:-ok}" in
                    deaf)      exit 1 ;;
                    ok)        cat >/dev/null; emit_tar index.html assets/main.js; exit 0 ;;
                    nowebpage) cat >/dev/null; emit_tar assets/main.js;            exit 0 ;;
                    empty)     cat >/dev/null; exit 0 ;;
                    *)         cat >/dev/null; exit 1 ;;
                esac ;;
            "run -d"*)
                # фоновый контейнер (заглушка-локация, держатель порта, сломанные двери
                # красного прогона) — считаем, что запустился
                exit 0 ;;
            *probe-web:4873*)
                # проверка склада
                case "${STUB_WAREHOUSE:-ok}" in
                    ok)      exit 0 ;;
                    nostart) exit 125 ;;
                    *)       exit 1 ;;
                esac ;;
            *probe-loc*)
                # стук в заглушку-локацию: отвечает она или нет
                [ -n "${STUB_LOC_READY:-}" ] && exit 0 || exit 1 ;;
            *) exit 0 ;;
        esac ;;
    *) exit 0 ;;
esac
STUB
chmod +x "$STUB_DIR/docker"
export PATH="$STUB_DIR:$PATH"
export STUB_NODE_IMAGE="$NODE_IMAGE"
export STUB_LOG="$STUB_DIR/log"

# ------------------------------------------------------------------ подставная дверь
# `up.sh` в конце стучится в дверь снаружи. Настоящей двери здесь нет и быть не может,
# поэтому поднимаем то, что отвечает 200 на что угодно. Нечем поднять — сценарий всё равно
# идёт: журнал вызовов пишется ДО стука. Меняется только ожидаемый код возврата, и это
# говорится вслух, а не прячется.
door_up() {
    local py
    for py in python3 python; do
        command -v "$py" >/dev/null 2>&1 || continue
        "$py" -c '
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
class H(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.send_header("Content-Length", "2"); self.end_headers()
        self.wfile.write(b"ok")
    def log_message(self, *a): pass
HTTPServer((sys.argv[2], int(sys.argv[1])), H).serve_forever()
' "$PORT" "$DOOR_HOST" >/dev/null 2>&1 &
        DOOR_PID=$!
        sleep 1
        kill -0 "$DOOR_PID" 2>/dev/null && return 0
        DOOR_PID=
    done
    return 1
}

# ------------------------------------------------------------------ прогон сценария
# scenario <имя> · run <ожидаемый код> <команда…> · дальше проверки журнала и вывода
run_case() {
    local want="$1"; shift
    : > "$STUB_LOG"
    # Адрес проверки подставляем так же, как порт: подставная дверь стоит на своём, и
    # прогоняемый скрипт обязан стучаться в неё, а не в то, что назвали в окружении.
    # `CASE_HOST` — для сценариев, которые НАРОЧНО называют другой адрес.
    WORLD_PORT="$PORT" WORLD_HOST="${CASE_HOST:-$DOOR_HOST}" "$@" > "$STUB_DIR/out" 2>&1
    local got=$?
    if [ "$got" = "$want" ]; then good "код возврата $got"
    else bad "код возврата $got, ждали $want"; tail -n 8 "$STUB_DIR/out" >&2; fi
}

called()     { grep -q -- "$1" "$STUB_LOG" && good "звалось: $1" || bad "НЕ звалось: $1"; }
not_called() { grep -q -- "$1" "$STUB_LOG" && { bad "звалось, а не должно: $1"; grep -- "$1" "$STUB_LOG" >&2; } || good "не звалось: $1"; }
said()       { grep -qF -- "$1" "$STUB_DIR/out" && good "сказано: «$1»" || { bad "НЕ сказано: «$1»"; tail -n 8 "$STUB_DIR/out" >&2; }; }

part "проба ВЕТВЛЕНИЯ · докер подставной, контейнеров не запускается ни одного"
detail "зелёный итог этой пробы НЕ значит, что мир поднимается — это отдельный прогон"
if door_up; then detail "подставная дверь на $DOOR_HOST:$PORT поднята"
else detail "подставную дверь поднять нечем (нет python3) — сценарий 1 дойдёт до отказа стука"; fi

# ================================================================== 1. подъём без поля
part "1. образ есть · склада нет · пробником не проверить → подъём не требует поля"
if [ -n "$DOOR_PID" ]; then want=0; else want=1; fi
STUB_HAVE_IMAGE=1 STUB_HAVE_PROBE= STUB_PULL_OK= STUB_WAREHOUSE=silent \
    run_case "$want" "$HERE/up.sh"
[ -n "$DOOR_PID" ] || detail "код 1 здесь — это отказ стука в подставную дверь, а не ветвление"
said "собирать не нужно"
not_called "run --rm --network"          # склад не спрашивали вовсе
not_called "compose -f $HERE/compose.yaml build"

# ================================================================== 2. склад молчит
part "2. образ есть · --build · склад молчит → отказ, и сборка НЕ начата"
STUB_HAVE_IMAGE=1 STUB_HAVE_PROBE=1 STUB_WAREHOUSE=silent \
    run_case 1 "$HERE/up.sh" --build
said "склад probe-web в сети omnifield-gateway не отвечает"
said "выход:"
not_called "$NODE_IMAGE"                 # сборщик пульта не запускался
not_called "compose -f $HERE/compose.yaml build"

# ================================================================== 3–4. «не смог проверить»
part "3. образа нет · пробник не скачался → «не знаю» ≠ «склада нет», сборка идёт"
STUB_HAVE_IMAGE= STUB_HAVE_PROBE= STUB_PULL_OK= STUB_WAREHOUSE=silent STUB_BUILDER=fail \
    run_case 1 "$HERE/build.sh" --only-web
said "проверить склад НЕЧЕМ"
said "это НЕ значит, что склада нет"
called "$NODE_IMAGE"                     # пошёл собирать, а не остановился

part "4. пробник не запустился (код 125) → тот же исход «не знаю»"
STUB_HAVE_IMAGE= STUB_HAVE_PROBE=1 STUB_WAREHOUSE=nostart STUB_BUILDER=fail \
    run_case 1 "$HERE/build.sh" --only-web
said "контейнер-пробник не запустился (код 125)"
called "$NODE_IMAGE"

# ================================================================== 4b. виноват сборщик
# Упавший сборщик закрывает поток, и хостовый `tar` умирает от SIGPIPE. Отказ обязан назвать
# СБОРЩИК: иначе виноватой выглядит зона `web`, и чинить пойдут не то (`kb:WORLD-31`).
part "4b. сборщик упал, не дочитав поток → отказ называет сборщик, а не подачу"
STUB_HAVE_PROBE=1 STUB_WAREHOUSE=ok STUB_BUILDER=deaf \
    run_case 1 "$HERE/build.sh" --only-web
said "пульт не собрался"
grep -qF 'исходник пульта не прочитался' "$STUB_DIR/out" \
    && bad "отказ назвал подачу, хотя сломан сборщик" || good "про «исходник не прочитался» не сказано"

# ================================================================== 5. образ без пульта
part "5. --only-image без собранного пульта → отказ, образ не складывается"
rm -rf "$OUT"
run_case 1 "$HERE/build.sh" --only-image
said "собранного пульта"
not_called "compose -f $HERE/compose.yaml build"

# ================================================================== 6. оба шага подряд
part "6. склад отвечает · сборщик отдал собранное → шаг 1, потом шаг 2"
STUB_HAVE_PROBE=1 STUB_WAREHOUSE=ok STUB_BUILDER=ok \
    run_case 0 "$HERE/build.sh"
called "$NODE_IMAGE"
called "compose -f $HERE/compose.yaml build"
[ -s "$OUT/index.html" ] && good "собранное разложено из потока в .build/web-dist" \
                         || bad "потока не разложили: $OUT/index.html пуст или отсутствует"

# ================================================================== 7. монтирований нет
# Ради этой проверки правка и делалась: на Windows любой путь в аргументе докера
# переписывается, и монтирование уезжает не туда. Здесь стережётся не симптом, а способ:
# в вызовах `docker run` не должно быть НИ монтирований, НИ путей хоста.
part "7. в вызовах docker run нет ни -v, ни путей хоста (иначе Git Bash их перепишет)"
grep '^run ' "$STUB_LOG" | grep -q -- ' -v ' \
    && { bad "в docker run есть монтирование -v"; grep '^run ' "$STUB_LOG" | grep -- ' -v ' >&2; } \
    || good "монтирований -v нет"
grep '^run ' "$STUB_LOG" | grep -qF -- "$ROOT" \
    && { bad "в docker run уехал путь хоста $ROOT"; grep '^run ' "$STUB_LOG" | grep -F -- "$ROOT" >&2; } \
    || good "путей хоста в docker run нет"

# ================================================================== 8–9. поток пришёл не тот
# Сборщик может выйти с нулём и не прислать ничего (лишняя строка в stdout ломает tar).
# Прежняя сборка при этом обязана остаться на месте: неудачная сборка не вправе оставлять
# зону без пульта.
part "8. сборщик вышел с нулём, а поток пуст → отказ, прежняя сборка цела"
printf 'прежняя сборка\n' > "$OUT/index.html"
STUB_HAVE_PROBE=1 STUB_WAREHOUSE=ok STUB_BUILDER=empty \
    run_case 1 "$HERE/build.sh" --only-web
said "поток пуст"
grep -q 'прежняя сборка' "$OUT/index.html" 2>/dev/null \
    && good "прежняя сборка на месте" || bad "прежнюю сборку снесли ради неудачной"

part "9. в потоке нет index.html → отказ, прежняя сборка цела"
STUB_HAVE_PROBE=1 STUB_WAREHOUSE=ok STUB_BUILDER=nowebpage \
    run_case 1 "$HERE/build.sh" --only-web
said "нет index.html"
grep -q 'прежняя сборка' "$OUT/index.html" 2>/dev/null \
    && good "прежняя сборка на месте" || bad "прежнюю сборку снесли ради неудачной"

# ================================================================== 10. уборка и улика
# Живой случай: заглушка не поднялась, отказ отправил смотреть `docker logs world-probe-loc`,
# а контейнера уже не было — его снёс наш же `trap cleanup EXIT`. Выход назван, исполнить
# нечем: это диагноз, а не отказ (`kb:WORLD-31`). Проверяется без докера — по журналу
# вызовов видно, снесли контейнер или оставили.
part "10. заглушка не поднялась → контейнер ОСТАВЛЕН, выход исполним"
detail "сценарий ждёт 15 попыток стука — это ~15 секунд"
STUB_HAVE_IMAGE=1 STUB_HAVE_PROBE=1 STUB_WAREHOUSE=silent \
    run_case 1 "$HERE/probe.sh" --green
said "заглушка-локация не поднялась"
said "контейнер ОСТАВЛЕН нарочно"
called "logs --tail=30 world-probe-loc"          # улику сняли, пока контейнер жив
# `rm -f world-probe-loc` звучит ровно один раз — это подготовка ПЕРЕД запуском заглушки.
# Второй раз означал бы, что уборка её всё-таки снесла.
n=$(grep -c -- 'rm -f world-probe-loc' "$STUB_LOG")
[ "$n" = 1 ] && good "уборка заглушку не сносила (единственный rm — подготовка до запуска)" \
             || bad "заглушку снесли: вызовов rm -f world-probe-loc — $n, ждали 1"

# ================================================================== 11. имя двери в сети
# Адрес соседа — контракт (`door:8080`), и настройка существует затем, чтобы имя можно было
# ОСВОБОДИТЬ, когда в поле оно занято. Без докера проверяется главное: настройка доезжает до
# компоуза, подъём от неё не ломается, а умолчание не разъехалось по файлам.
part "11. имя двери в сети: умолчание, настройка, и подъём от неё не ломается"

if [ -n "$DOOR_PID" ]; then want=0; else want=1; fi
STUB_HAVE_IMAGE=1 run_case "$want" "$HERE/up.sh"
called "ENV WORLD_DOOR_ALIAS=door"                  # умолчание доехало до компоуза
said "имя в сети door"

STUB_HAVE_IMAGE=1 WORLD_DOOR_ALIAS=field-door run_case "$want" "$HERE/up.sh"
called "ENV WORLD_DOOR_ALIAS=field-door"            # настройка доехала
said "имя в сети field-door"
not_called "ENV WORLD_DOOR_ALIAS=door"              # и старое имя не подставилось заодно

# Умолчание живёт в трёх файлах: подъём называет его человеку ДО компоуза, файл запуска
# обязан работать и без скрипта, проба обязана проверять адрес соседа. Разъедутся — мир
# будет объявлять одно имя, а занимать другое, и заметит это только сосед.
part "11b. умолчание имени не разъехалось по файлам"
defaults=$(for f in up.sh compose.yaml probe.sh; do
        sed -n 's/.*WORLD_DOOR_ALIAS:-\([A-Za-z0-9_-]*\)}.*/\1/p' "$HERE/$f" | head -n1
    done | sort -u)
n=$(printf '%s\n' "$defaults" | grep -c .)
if [ "$n" = 1 ] && [ -n "$defaults" ]; then good "во всех трёх файлах одно умолчание: $defaults"
else bad "умолчания разъехались: $(printf '%s' "$defaults" | tr '\n' ' ')"; fi

# ================================================================== 12. адрес проверки
# Опубликованный порт живёт там, где живёт ДЕМОН докера, а команду даёт человек — и это не
# всегда одна петля. Зашитый адрес объявлял живую дверь мёртвой на машине, где докер в WSL:
# прогон был невозможен вовсе (`tasker:WORLD-97`). Отсюда две проверки, и вторая не менее
# важная, чем первая: мало стучаться туда, куда сказали, — отказ обязан назвать ВЫХОД,
# иначе это диагноз (`kb:WORLD-31`).
part "12. адрес проверки: стук уходит по WORLD_HOST, а отказ называет выход"
if [ -z "$DOOR_PID" ]; then
    skip "подставной двери нет (нечем поднять) — кодом возврата тут ничего не докажешь"
elif ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
    skip "ни curl, ни wget — стучаться нечем, и подъём это честно пропускает"
else
    detail "сценарий ждёт 30 попыток стука — это ~30 секунд"
    # Подставная дверь ЖИВА и стоит на $DOOR_HOST. Просим стучаться по другому адресу —
    # значит зелёный исход здесь означал бы, что стук ушёл по умолчанию, мимо настройки.
    CASE_HOST="$NOWHERE_HOST" STUB_HAVE_IMAGE=1 run_case 1 "$HERE/up.sh"
    said "$NOWHERE_HOST"                # назван адрес, по которому стучались
    said "WORLD_HOST=<адрес>"            # назван выход: чем адрес задаётся
    said "порт опубликован так"          # названо, куда машина опубликовала порт
    said "докер живёт в WSL"             # назван случай, из которого дефект и вырос
    # Присваивание перед вызовом ФУНКЦИИ остаётся в сессии — снимаем нарочно, иначе
    # следующие сценарии стучались бы в пустоту и краснели по посторонней причине.
    CASE_HOST=
fi

# Умолчание живёт в пяти файлах: подъём, обе живые пробы и оба скрипта локации. Разъедутся —
# часть зоны будет смотреть по одному адресу, часть по другому, и на машине с настройкой
# половина прогона окажется красной без всякой поломки.
part "12b. умолчание адреса объявлено во всех пяти файлах и не разъехалось"
hosts=; missing=
for f in up.sh probe.sh probe-location.sh location-up.sh location-down.sh; do
    v=$(sed -n 's/.*WORLD_HOST:-\([0-9.]*\)}.*/\1/p' "$HERE/$f" | head -n1)
    if [ -n "$v" ]; then hosts="$hosts$v"$'\n'; else missing="$missing $f"; fi
done
if [ -n "$missing" ]; then
    bad "адрес настраивается не везде — WORLD_HOST не объявлен в:$missing"
else
    n=$(printf '%s' "$hosts" | sort -u | grep -c .)
    first=${hosts%%$'\n'*}
    if [ "$n" = 1 ] && [ "$first" = 127.0.0.1 ]; then
        good "во всех пяти файлах одно умолчание: $first"
    else
        bad "умолчания разъехались либо умолчание не 127.0.0.1: $(printf '%s' "$hosts" | tr '\n' ' ')"
    fi
fi

# Настройка, мимо которой можно зашить адрес заново, — не настройка: одна забытая строка, и
# на машине с WSL снова покраснеет живая дверь. Стережём способ, а не симптом.
part "12c. зашитого адреса в скриптах не осталось — кроме собственной петли контейнера"
hard=$(grep -n 'http://127\.0\.0\.1' "$HERE"/up.sh "$HERE"/probe.sh "$HERE"/probe-location.sh \
        "$HERE"/location-up.sh "$HERE"/location-down.sh | grep -v 'docker_noconv exec')
if [ -z "$hard" ]; then
    good "зашитых адресов нет (петля ВНУТРИ контейнера не в счёт: публикации там нет вовсе)"
else
    bad "адрес зашит заново — он обязан крутиться WORLD_HOST:"
    printf '%s\n' "$hard" >&2
fi

# Улику в отказе подъём снимает по ИМЕНИ контейнера, а имя объявляет файл запуска. Разъедутся
# — отказ скажет «контейнер не найден» о живой двери и подтолкнёт к неверной причине: к
# адресу, хотя дело было бы в имени.
part "12d. имя контейнера двери в отказе совпадает с тем, что объявляет файл запуска"
name_compose=$(sed -n 's/^[[:space:]]*container_name:[[:space:]]*\([A-Za-z0-9_.-]*\).*/\1/p' "$HERE/compose.yaml" | head -n1)
if [ -n "$name_compose" ] && grep -qF "docker port $name_compose" "$HERE/up.sh"; then
    good "подъём снимает улику с контейнера $name_compose — того же, что в compose.yaml"
else
    bad "имя контейнера разъехалось: compose.yaml объявляет «${name_compose:-<нет>}», а up.sh снимает улику не с него"
fi

# ================================================================== 13–19. ОБРАЗ ЛОКАЦИИ
# Второе изделие мира (`kb:WORLD-53`): человек заполняет конфиг и даёт одну команду, локация
# поднимается и САМА входит в поле. Без докера здесь проверяется то, что дороже всего стоит
# в живом прогоне: чем подъём отказывает ДО первого контейнера и в каком порядке он делает
# две вещи, которые нельзя менять местами.
LOC_CFG="$STUB_DIR/location.env"
loc_config() {   # loc_config <строки конфига…>
    : > "$LOC_CFG"
    local line
    for line in "$@"; do printf '%s\n' "$line" >> "$LOC_CFG"; done
}

part "13. конфига локации нет → отказ с образцом, ни одного вызова докера"
run_case 1 "$HERE/location-up.sh" --config "$STUB_DIR/net-takogo-fajla.env"
said "конфига локации нет"
said "cp deploy/location.env.example"
not_called "location-compose.yaml up"

part "14. в конфиге нет имени → отказ называет переменную, локация не поднимается"
loc_config 'WORLD_GIVES=проба входа в поле'
run_case 1 "$HERE/location-up.sh" --config "$LOC_CFG"
said "не сказано имя локации"
said "WORLD_NAME"
not_called "location-compose.yaml up"

part "15. в конфиге нет «что даёт» → отказ называет переменную, локация не поднимается"
loc_config 'WORLD_NAME=probe-place'
run_case 1 "$HERE/location-up.sh" --config "$LOC_CFG"
said "не сказано, что локация даёт"
said "WORLD_GIVES"
not_called "location-compose.yaml up"

# Имя уезжает в имя контейнера, имя проекта и алиас в сети — с неподходящим именем до двери
# дело не дойдёт вовсе. Правило имени в ПОЛЕ при этом здесь не повторяется: его держит дверь.
# Оба случая, а не один: имя может испортиться и первой буквой, и любой следующей, а ветки
# в проверке разные. Стереги мы только одну — вторая перестала бы работать молча.
part "16. имя не годится докеру → отказ до первого контейнера (обе ветки)"
loc_config 'WORLD_NAME=Probe-place' 'WORLD_GIVES=проба входа в поле'
run_case 1 "$HERE/location-up.sh" --config "$LOC_CFG"
said "не годится ни в имя контейнера"
said "начинаться оно обязано"
not_called "location-compose.yaml up"

loc_config 'WORLD_NAME=probe_place' 'WORLD_GIVES=проба входа в поле'
run_case 1 "$HERE/location-up.sh" --config "$LOC_CFG"
said "не годится ни в имя контейнера"
said "строчные латинские буквы, цифры и дефис"
not_called "location-compose.yaml up"

# Конфиг читают ДВОЕ — подъём и компоуз, — и читать они обязаны одинаково. Разойдись они
# на пробеле после `=`, локация объявилась бы в поле под одним именем, а сетевой алиас
# получила бы под другим: дверь ответила бы `addr-unreachable`, а виноватой выглядела бы сеть.
part "16b. конфиг читается как у компоуза: пробелы, кавычки, чужой ключ, кривая строка"
loc_config '  WORLD_NAME = probe-place' 'WORLD_GIVES="что даёт, в кавычках"' \
           'WORLD_TYPO=1' 'кривая строка без равно'
STUB_HAVE_LOC_IMAGE=1 STUB_GUARD_READY=1 STUB_JOIN_OK=1 \
    run_case 0 "$HERE/location-up.sh" --config "$LOC_CFG"
called "ENV WORLD_NAME=probe-place"           # пробелы вокруг «=» снялись, как у компоуза
called "WORLD_GIVES=что даёт, в кавычках"     # кавычки сняты, запятая внутри уцелела
said "ключ WORLD_TYPO конфигу локации неизвестен"
said "не похожа на «КЛЮЧ=значение»"

part "17. конфиг полон · образ есть → не собирает, поднимает и ВХОДИТ В ПОЛЕ"
loc_config 'WORLD_NAME=probe-place' 'WORLD_GIVES=проба входа в поле'
STUB_HAVE_LOC_IMAGE=1 STUB_GUARD_READY=1 STUB_JOIN_OK=1 \
    run_case 0 "$HERE/location-up.sh" --config "$LOC_CFG"
not_called "location-compose.yaml build"       # образ есть — стройки не было
called "location-compose.yaml up -d"
called "-p world-loc-probe-place"              # проект по имени: две локации не сольются
called "/app/world join"                       # вход в поле сделан, а не оставлен человеку
called "ENV WORLD_NAME=probe-place"            # конфиг доехал до компоуза
said "в поле"

part "18. образа локации нет → собирается САМ, поле для этого не нужно"
STUB_HAVE_LOC_IMAGE= STUB_GUARD_READY=1 STUB_JOIN_OK=1 \
    run_case 0 "$HERE/location-up.sh" --config "$LOC_CFG"
called "location-compose.yaml build"
called "location-compose.yaml up -d"
not_called "probe-web:4873"                    # склад пульта локации не нужен вовсе

# Порядок «сначала сторож, потом вход» — единственное место, где эти два шага связаны, и
# переставить их значит получить `self-unreachable` на ровном месте: дверь проверяет адрес.
part "19. сторож не ответил → В ПОЛЕ НЕ ВХОДИМ (порядок, а не удача)"
detail "сценарий ждёт 30 попыток стука — это ~30 секунд"
STUB_HAVE_LOC_IMAGE=1 STUB_GUARD_READY= STUB_JOIN_OK=1 \
    run_case 1 "$HERE/location-up.sh" --config "$LOC_CFG"
said "сторож не ответил"
said "в поле локация НЕ вошла"
not_called "/app/world join"                   # маршрута в пустоту не появилось

part "20. настройка конфига доезжает до компоуза и до сети"
loc_config 'WORLD_NAME=probe-place' 'WORLD_GIVES=проба' \
           'WORLD_DOOR=field-door:8080' 'WORLD_NET=drugaya-set'
STUB_HAVE_LOC_IMAGE=1 STUB_GUARD_READY=1 STUB_JOIN_OK=1 \
    run_case 0 "$HERE/location-up.sh" --config "$LOC_CFG"
called "network inspect drugaya-set"           # входим в названную сеть, а не в умолчальную
called "WORLD_DOOR=field-door:8080"
not_called "WORLD_DOOR=door:8080"              # и умолчание не подставилось заодно

# Умолчание адреса двери живёт в трёх файлах локации, а его первая половина — ещё и в
# файлах мира: локация стучится по имени, которое дверь себе объявляет. Разъедутся — мир
# будет стоять, локация не войдёт, и выглядеть это будет как поломка сети.
part "20b. умолчание адреса двери не разъехалось — ни внутри локации, ни с миром"
doors=$(for f in location-compose.yaml location-config.sh location.env.example; do
        sed -n 's/.*WORLD_DOOR[:=][^A-Za-z0-9]*\([A-Za-z0-9_.-]*:[0-9][0-9]*\).*/\1/p' "$HERE/$f" | head -n1
    done | sort -u)
n=$(printf '%s\n' "$doors" | grep -c .)
if [ "$n" = 1 ] && [ -n "$doors" ]; then good "во всех трёх файлах локации один адрес двери: $doors"
else bad "адрес двери разъехался: $(printf '%s' "$doors" | tr '\n' ' ')"; fi
alias_default=$(sed -n 's/.*WORLD_DOOR_ALIAS:-\([A-Za-z0-9_-]*\)}.*/\1/p' "$HERE/compose.yaml" | head -n1)
[ "${doors%%:*}" = "$alias_default" ] \
    && good "имя в адресе локации совпало с именем, которое объявляет дверь: $alias_default" \
    || bad "локация стучится в «${doors%%:*}», а дверь объявляет себя как «$alias_default»"

# Наружу торчит ровно одна дверь (`kb:FUND-5`), и она принадлежит миру. Хост-публикация у
# локации завела бы второй вход в поле — мимо реестра и мимо маршрутов.
part "21. локация не публикует порт наружу"
grep -qE '^[[:space:]]*ports:' "$HERE/location-compose.yaml" \
    && { bad "в файле запуска локации есть хост-публикация"; grep -nE '^[[:space:]]*ports:' "$HERE/location-compose.yaml" >&2; } \
    || good "хост-публикации нет — снаружи локация доступна только через дверь"

# ================================================================== 22–23. снятие локации
# Снести контейнер, не выйдя из поля, значит оставить в реестре маршрут в пустоту. Порядок
# здесь обратный подъёму, и он тоже не переставляется.
part "22. снятие: сначала ВЫХОД ИЗ ПОЛЯ, потом контейнер"
loc_config 'WORLD_NAME=probe-place' 'WORLD_GIVES=проба входа в поле'
run_case 0 "$HERE/location-down.sh" --config "$LOC_CFG"
called "/app/world leave"
called "location-compose.yaml down"
n_leave=$(grep -n -- '/app/world leave' "$STUB_LOG" | head -n1 | cut -d: -f1)
n_down=$(grep -n -- 'location-compose.yaml down' "$STUB_LOG" | head -n1 | cut -d: -f1)
if [ -n "$n_leave" ] && [ -n "$n_down" ] && [ "$n_leave" -lt "$n_down" ]; then
    good "выход из поля был ДО снятия контейнера (строки $n_leave и $n_down)"
else
    bad "порядок нарушен: leave в строке ${n_leave:-<нет>}, down в строке ${n_down:-<нет>}"
fi

part "23. --keep-in-field: контейнер снят, из поля НЕ выходим, и это сказано вслух"
run_case 0 "$HERE/location-down.sh" --config "$LOC_CFG" --keep-in-field
not_called "/app/world leave"
called "location-compose.yaml down"
said "location-unreachable"

# ================================================================== 24. ЛАНДШАФТ МЕСТА
# Пустое место, на котором ничего нельзя сделать, бесполезно: образ локации несёт ландшафт —
# то, что есть на месте изначально (`tasker:WORLD-102`). Без докера здесь проверяется не то,
# что пакеты работают (это живая проба), а то, что они ОБЪЯВЛЕНЫ и объявлены ровно те:
# «положить заодно ещё один» стоит размера каждому месту и происходит незаметно.
part "24. ландшафт объявлен в образе — ровно четыре, и ничего сверх"
apk=$(grep -n 'apk add' "$HERE/Dockerfile.location" | head -n1)
if [ -z "$apk" ]; then
    bad "в образе локации нет установки пакетов вовсе — ландшафта у места не будет"
else
    for pkg in git nodejs npm ca-certificates curl; do
        case "$apk" in
            *" $pkg"*) good "в образе объявлен: $pkg" ;;
            *)         bad "в образе НЕ объявлен: $pkg" ;;
        esac
    done
    # Запрещённое перечислено поимённо: это другая схема локации, и заводится она под
    # задачу. Базовый ландшафт, растущий «на всякий случай», превращается во «всё сразу».
    extra=
    for pkg in pnpm yarn python python3 go openssh-client bash; do
        case "$apk" in *" $pkg"*) extra="$extra $pkg" ;; esac
    done
    [ -z "$extra" ] && good "сверх четырёх ничего не тащим" \
                     || bad "в базовый образ уехало лишнее:$extra — это другая схема локации"
fi

# Каталог стройки и том под него — одна вещь, названная в двух файлах. Разъедься они, том
# смонтировался бы мимо каталога: постройка легла бы в слой контейнера и пропала бы при
# первом же пересоздании, а «пишется» осталось бы зелёным.
part "24b. каталог стройки в образе и точка монтирования тома — одно и то же место"
build_dir=$(sed -n 's/.*WORLD_BUILD_DIR="\([^"]*\)".*/\1/p' "$HERE/Dockerfile.location" | head -n1)
mount_at=$(sed -n 's/^[[:space:]]*-[[:space:]]*place:\([^[:space:]]*\).*/\1/p' "$HERE/location-compose.yaml" | head -n1)
if [ -z "$build_dir" ]; then
    bad "WORLD_BUILD_DIR в образе локации не задан — сторож поднимется без каталога стройки (build-off)"
elif [ -z "$mount_at" ]; then
    bad "тома под застройку в файле запуска локации нет — построенное не переживёт пересоздания"
elif [ "$build_dir" = "$mount_at" ]; then
    good "и там и там $build_dir"
else
    bad "разъехались: образ строит в $build_dir, том смонтирован в $mount_at"
fi

# Сторож бежит не из-под root, а писать в склад обязан именно он. Права каталога докер
# переносит на пустой том при первом монтировании — не отдай мы каталог пользователю здесь,
# том приехал бы принадлежащим root, и стройка отказала бы `build-dir-unusable`.
# in_dockerfile <команда> <путь> — назван ли путь в аргументах такой команды образа.
# Смотрим ПОСПИСОЧНО, а не «строка целиком совпала»: каталогов в одной команде может быть
# несколько (`mkdir -p /home/world /place`), и сверка целой строкой краснела бы от порядка
# слов — то есть стерегла бы форму записи вместо свойства.
in_dockerfile() {
    local cmd="$1" want="$2" line rest
    while IFS= read -r line; do
        case "$line" in *"$cmd"*) ;; *) continue ;; esac
        rest=" ${line#*"$cmd"} "
        case "$rest" in *" $want "*) return 0 ;; esac
    done < "$HERE/Dockerfile.location"
    return 1
}

# owned_by_world <путь> — отдан ли путь пользователю сторожа. Отдельно от `in_dockerfile`
# потому, что у `chown` бывают ключи (`-R`), и «команда» тут не начало строки, а её суть:
# сверять надо, кому и что отдали, а не как записали.
owned_by_world() {
    local want="$1" line rest
    while IFS= read -r line; do
        case "$line" in *chown*world:world*) ;; *) continue ;; esac
        rest=" ${line#*world:world} "
        case "$rest" in *" $want "*) return 0 ;; esac
    done < "$HERE/Dockerfile.location"
    return 1
}

part "24c. каталог стройки создан в образе и отдан пользователю сторожа"
if in_dockerfile 'mkdir -p' "${build_dir:-/place}" && owned_by_world "${build_dir:-/place}"; then
    good "каталог создаётся и отдаётся world:world"
else
    bad "каталог стройки не создан в образе либо не отдан пользователю world"
fi

# Ландшафт приезжает из репозиториев alpine — значит у сборки образа локации ЕСТЬ сеть.
# Прежний запрет остался бы тихой поломкой: пакеты не приедут, а выглядеть это будет как
# неполадка сети.
part "24d. у сборки локации нет запрета сети — иначе ландшафт не приедет"
grep -qE '^[[:space:]]*network:[[:space:]]*none' "$HERE/location-compose.yaml" \
    && bad "в файле запуска локации остался network: none — пакеты ландшафта не приедут" \
    || good "запрета сети у сборки нет (поле по-прежнему не нужно — нужен выход наружу)"

# Снести место и потерять построенное — разные события. `down -v` сделал бы их одним, и
# человек узнал бы об этом уже после.
part "24e. снятие локации НЕ сносит склад (down без -v)"
if grep -E '\$\{COMPOSE\[@\]\}" down' "$HERE/location-down.sh" | grep -q -- ' -v'; then
    bad "снятие локации идёт с -v — построенное исчезнет вместе с контейнером"
else
    good "склад снятие переживает; стереть его — отдельное действие"
fi

# ================================================================== 24f. МЕСТО ЖИЛОЕ
# Инструменты без человека бесполезны: юзер строит на месте руками (`WORLD2` 1.0), и войти
# он обязан МОЧЬ. На живом это стоило трёх упоров подряд (`tasker:WORLD-106`): дом записан
# в паспорте, а каталога нет; оболочка `/sbin/nologin`; правки по живому контейнеру от root,
# умирающие при первом пересоздании. Здесь стережётся объявленное в образе — живой вход
# проверяет `probe-location.sh`.
part "24f. в место можно войти работать: дом и живая оболочка"
passwd_line=$(grep -n 'adduser' "$HERE/Dockerfile.location" | head -n1)
case "$passwd_line" in
    *'/sbin/nologin'*) bad "оболочка пользователя — /sbin/nologin: терминал в месте не стартует" ;;
    *'-s /bin/sh'*)    good "оболочка пользователя — /bin/sh" ;;
    *)                 bad "оболочка пользователя в образе не названа: ${passwd_line:-<adduser не найден>}" ;;
esac
case "$passwd_line" in
    *'-H'*)             bad "дом пользователя не создаётся (-H): редактор упрётся в Permission denied" ;;
    *'-h /home/world'*) good "дом пользователя объявлен: /home/world" ;;
    *)                  bad "дом пользователя в образе не объявлен" ;;
esac
if in_dockerfile 'mkdir -p' /home/world && owned_by_world /home/world; then
    good "дом создан в образе и отдан world:world"
else
    bad "дом не создан в образе либо не отдан пользователю — вход в место упрётся в права"
fi
grep -q 'HOME="/home/world"' "$HERE/Dockerfile.location" \
    && good "HOME назван явно — на него смотрят оболочка и редактор" \
    || bad "HOME в образе не назван: редактор снова упрётся в пустой дом"

# Дом и оболочка — про ЧЕЛОВЕКА, который входит. Служба как бежала не из-под root, так и
# бежит: дай мы ей root «заодно», место стало бы удобнее ровно один раз.
part "24g. сторож по-прежнему бежит не из-под root"
grep -qE '^USER world' "$HERE/Dockerfile.location" \
    && good "USER world в образе на месте" \
    || bad "в образе локации нет USER world — сторож побежит из-под root"

# ================================================================== 24h. МЕСТО-МАСТЕРСКАЯ
# Жилое место — ещё не мастерская: в нём надо чем-то РАБОТАТЬ. Инструменты приносит юзер
# (`WORLD2` 0.1 — мир не изготовитель, руки это юзер), а связка ключей приходит с ним из его
# пространства (`3.0`). Живой упор 2026-08-13 (`tasker:WORLD-108`): ключей в месте не видно,
# `npm i -g` падает `EACCES`, редактор открывает не тот каталог.
part "24h. инструменты ставятся без root: префикс npm в доме и в PATH"
prefix=$(sed -n 's/.*NPM_CONFIG_PREFIX="\([^"]*\)".*/\1/p' "$HERE/Dockerfile.location" | head -n1)
if [ -z "$prefix" ]; then
    bad "префикс npm в образе не задан — npm i -g уйдёт в /usr/local и упрётся в EACCES"
else
    case "$prefix" in
        /home/world/*) good "префикс npm в доме пользователя: $prefix" ;;
        *) bad "префикс npm вне дома пользователя ($prefix) — писать туда world не сможет" ;;
    esac
    # `PATH` образа — для неинтерактивной оболочки (`docker exec … sh -c`), профиль — для
    # логина (терминал редактора). Одного мало: без первого не найдёт `docker exec`, без
    # второго — терминал, и оба раза это выглядит как «поставилось, но не работает».
    path_line=$(sed -n 's/.*PATH="\([^"]*\)".*/\1/p' "$HERE/Dockerfile.location" | head -n1)
    case ":$path_line:" in
        *":$prefix/bin:"*) good "каталог префикса в PATH образа" ;;
        *) bad "каталог префикса не в PATH образа: ${path_line:-<PATH не задан>}" ;;
    esac
    # Спрашиваем ДВА разных факта, и оба точечно: что профиль вообще пишется и что в нём
    # именно наш префикс. Ищи мы «есть ли где-то в файле слово profile.d и где-то путь» —
    # проверка зеленела бы на осиротевшем `mkdir` и на упоминании в комментарии.
    grep -q '> /etc/profile.d/' "$HERE/Dockerfile.location" \
        && good "профиль для оболочки-логина пишется" \
        || bad "профиль для оболочки-логина не пишется — терминал редактора поставленного не найдёт"
    grep -qF "export PATH=$prefix/bin" "$HERE/Dockerfile.location" \
        && good "и в профиле именно каталог префикса" \
        || bad "в профиле нет каталога префикса ($prefix/bin) — PATH логина соберётся без него"
    in_dockerfile 'mkdir -p' "$prefix" \
        && good "каталог префикса создан в образе" \
        || bad "каталог префикса в образе не создан"
fi

# Чем открывать место, знает САМО МЕСТО: метку читает привязка редактора, и настраивать у
# себя человеку нечего. Пустая метка вернула бы ровно тот случай, с которого начали, —
# редактор открывает дом, а работать надо в складе.
part "24i. метка для редактора: заходит от world и сразу в склад"
# Ищем ОБЪЯВЛЕНИЕ, а не упоминание: про метку сказано и в комментарии рядом, и первым
# совпадением идёт именно он — сверяли бы мы комментарий, проверка молча стерегла бы текст.
meta=$(grep -n '^LABEL devcontainer\.metadata' "$HERE/Dockerfile.location" | head -n1)
if [ -z "$meta" ]; then
    bad "метки devcontainer.metadata в образе нет — редактор откроет дом и от кого попало"
else
    case "$meta" in *'"remoteUser":"world"'*) good "remoteUser: world" ;;
        *) bad "в метке не сказано remoteUser: world" ;; esac
    case "$meta" in *'"workspaceFolder":"/place"'*) good "workspaceFolder: /place" ;;
        *) bad "в метке не сказан workspaceFolder: /place — редактор откроет не склад" ;; esac
fi

# Связка ключей — состояние ПРОСТРАНСТВА ЮЗЕРА, и месту она доступна только вниз. Проверяем
# три свойства надстройки: она только для чтения · том берётся готовый (внешний) · её нет в
# основном файле запуска, то есть без имени в конфиге место поднимается как раньше.
part "24j. связка ключей: надстройка, только чтение, том готовый"
SECRETS_FILE="$HERE/location-secrets.yaml"
if [ ! -f "$SECRETS_FILE" ]; then
    bad "надстройки location-secrets.yaml нет — связку ключей месту не подключить"
else
    mount_line=$(grep -n 'secrets:/' "$SECRETS_FILE" | head -n1)
    case "$mount_line" in
        *':ro'*) good "связка монтируется только для чтения" ;;
        '')      bad "в надстройке нет монтирования связки" ;;
        *)       bad "связка монтируется НА ЗАПИСЬ — место начнёт владеть чужим состоянием: $mount_line" ;;
    esac
    grep -qE '^[[:space:]]*external:[[:space:]]*true' "$SECRETS_FILE" \
        && good "том берётся готовый (external) — опечатка в имени станет отказом, а не пустым томом" \
        || bad "том не объявлен внешним: опечатка заведёт пустой том, и связка «будет на месте» пустой"
    grep -q 'secrets' "$HERE/location-compose.yaml" \
        && bad "связка просочилась в основной файл запуска — место без неё перестанет подниматься" \
        || good "в основном файле запуска связки нет: без имени в конфиге место встаёт как раньше"
fi

part "24k. связка названа → надстройка подключается, не названа → нет"
loc_config 'WORLD_NAME=probe-place' 'WORLD_GIVES=проба' 'WORLD_SECRETS=omnifield-secrets'
STUB_HAVE_LOC_IMAGE=1 STUB_GUARD_READY=1 STUB_JOIN_OK=1 \
    run_case 0 "$HERE/location-up.sh" --config "$LOC_CFG"
called "location-secrets.yaml"                     # надстройка уехала в вызов компоуза
said "связка ключей omnifield-secrets приехала"    # и подъём сказал об этом человеку
said "только чтение"

loc_config 'WORLD_NAME=probe-place' 'WORLD_GIVES=проба'
STUB_HAVE_LOC_IMAGE=1 STUB_GUARD_READY=1 STUB_JOIN_OK=1 \
    run_case 0 "$HERE/location-up.sh" --config "$LOC_CFG"
not_called "location-secrets.yaml"                 # без имени надстройки нет вовсе
said "связки ключей не назвали"                    # и это сказано, а не проглочено

# Снятие обязано видеть ТОТ ЖЕ состав файлов, что и подъём: разойдись они — компоуз считал
# бы проектом не то же самое, и снятие оставило бы за собой контейнер.
part "24l. снятие видит ту же надстройку, что и подъём"
loc_config 'WORLD_NAME=probe-place' 'WORLD_GIVES=проба' 'WORLD_SECRETS=omnifield-secrets'
run_case 0 "$HERE/location-down.sh" --config "$LOC_CFG"
called "location-secrets.yaml"

# ================================================================== 25. АДРЕС ЗАСТРОЙКИ
# Мир не сканирует место и не угадывает: где внутри места стоит застройка — ЗАЯВЛЯЕТСЯ
# настройкой («не заявлено — не видно», `core/README.md`, `tasker:WORLD-104`). Значит у
# конфига появилась пятая строка, и проверяются здесь обе ветки её разбора: заявлено и нет.
# Ветка «нет» не менее важная — пустое место законно и остаётся нормой.
part "25. адрес застройки заявлен → доезжает до компоуза, и подъём говорит об этом"
loc_config 'WORLD_NAME=probe-place' 'WORLD_GIVES=проба' 'WORLD_BUILD_ADDR=127.0.0.1:3000'
STUB_HAVE_LOC_IMAGE=1 STUB_GUARD_READY=1 STUB_JOIN_OK=1 \
    run_case 0 "$HERE/location-up.sh" --config "$LOC_CFG"
# Журнал заглушки показывает ОКРУЖЕНИЕ вызова компоуза, то есть что подъём прочитал из
# конфига и экспортировал. Что значение доедет до КОНТЕЙНЕРА, отсюда не следует — за это
# отвечает проброс в файле запуска, и он проверяется отдельно (25c).
called "ENV WORLD_BUILD_ADDR=127.0.0.1:3000"      # конфиг прочитан, значение экспортировано
said "застройка заявлена: 127.0.0.1:3000"          # и подъём назвал его человеку
# Ключ обязан быть ЗНАКОМЫМ: попади он в «неизвестен, он его не читает» — человек решил бы,
# что настройки нет вовсе, и пошёл бы искать её в другом месте.
grep -qF 'ключ WORLD_BUILD_ADDR' "$STUB_DIR/out" \
    && bad "конфиг считает WORLD_BUILD_ADDR неизвестным ключом" \
    || good "ключ WORLD_BUILD_ADDR конфигу знаком"

part "25b. адрес не заявлен → место пустое, и чужой адрес не подставляется"
loc_config 'WORLD_NAME=probe-place' 'WORLD_GIVES=проба'
STUB_HAVE_LOC_IMAGE=1 STUB_GUARD_READY=1 STUB_JOIN_OK=1 \
    run_case 0 "$HERE/location-up.sh" --config "$LOC_CFG"
called "ENV WORLD_BUILD_ADDR=<не заявлено>"        # в компоуз уехало пусто, а не умолчание
not_called "ENV WORLD_BUILD_ADDR=127.0.0.1:3000"   # и прошлое значение не прилипло
said "застройки не заявлено"
said "законное состояние"                          # пустое место — норма, а не недоделка

# Проброс в контейнер живёт в файле запуска, и по журналу вызовов его не увидеть: заглушка
# показывает окружение компоуза, а не то, что компоуз положит внутрь. Значит смотрим в сам
# файл — и заодно стережём отсутствие умолчания: подставь его кто-нибудь «для удобства»,
# маршрут места повёл бы в вещь, которой человек не поднимал, и молча.
part "25c. файл запуска пробрасывает адрес застройки — и БЕЗ умолчания"
pass_line=$(grep -n '^[[:space:]]*WORLD_BUILD_ADDR:' "$HERE/location-compose.yaml" | head -n1)
if [ -z "$pass_line" ]; then
    bad "в файле запуска локации нет проброса WORLD_BUILD_ADDR — заявленный адрес не доедет до места"
else
    case "$pass_line" in
        *'${WORLD_BUILD_ADDR:-}'*) good "проброс есть, умолчания нет: пусто значит пустое место" ;;
        *) bad "у адреса застройки завелось умолчание — маршрут поведёт в вещь, которой не поднимали:"
           printf '%s\n' "$pass_line" >&2 ;;
    esac
fi

# ================================================================== 26. подсказки
# `--help` обязан работать там, где докера нет вовсе, — иначе человек не узнает про ключи
# ровно тогда, когда они ему и нужны.
part "26. подсказки и непонятный ключ"
run_case 0 "$HERE/up.sh" --help
run_case 0 "$HERE/build.sh" --help
run_case 0 "$HERE/probe.sh" --help
run_case 0 "$HERE/location-up.sh" --help
run_case 0 "$HERE/location-down.sh" --help
run_case 0 "$HERE/probe-location.sh" --help
run_case 2 "$HERE/up.sh" --nesuschestvuyuschiy-klyuch
run_case 2 "$HERE/location-up.sh" --nesuschestvuyuschiy-klyuch

# ================================================================== итог
part "── итог"
printf '  докер был ПОДСТАВНОЙ: ни одного контейнера не запускалось, мир не поднимался.\n' >&2
printf '  проверено ветвление — что вызвано и чего не вызвано. Подъём проверяет ./deploy/probe.sh\n' >&2
[ "$skipped" = 0 ] || printf '  пропущено (нечем проверить): %s\n' "$skipped" >&2
if [ "$failed" = 0 ]; then
    printf '  \033[1;32m✔ проверок %s, провалов нет\033[0m\n' "$total" >&2
    exit 0
fi
printf '  \033[1;31m✘ проверок %s, провалено %s\033[0m\n' "$total" "$failed" >&2
exit 1
