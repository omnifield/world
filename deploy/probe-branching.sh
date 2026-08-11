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
            omnifield/world:dev) [ -n "${STUB_HAVE_IMAGE:-}" ] && exit 0 || exit 1 ;;
            *)                   [ -n "${STUB_HAVE_PROBE:-}" ] && exit 0 || exit 1 ;;
        esac ;;
    compose)
        shift 3   # compose -f <файл>
        case "$1" in
            config) echo "omnifield/world:dev"; exit 0 ;;
            *)      exit 0 ;;
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
            *)
                # проверка склада
                case "${STUB_WAREHOUSE:-ok}" in
                    ok)      exit 0 ;;
                    nostart) exit 125 ;;
                    *)       exit 1 ;;
                esac ;;
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
HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
' "$PORT" >/dev/null 2>&1 &
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
    WORLD_PORT="$PORT" "$@" > "$STUB_DIR/out" 2>&1
    local got=$?
    if [ "$got" = "$want" ]; then good "код возврата $got"
    else bad "код возврата $got, ждали $want"; tail -n 8 "$STUB_DIR/out" >&2; fi
}

called()     { grep -q -- "$1" "$STUB_LOG" && good "звалось: $1" || bad "НЕ звалось: $1"; }
not_called() { grep -q -- "$1" "$STUB_LOG" && { bad "звалось, а не должно: $1"; grep -- "$1" "$STUB_LOG" >&2; } || good "не звалось: $1"; }
said()       { grep -qF -- "$1" "$STUB_DIR/out" && good "сказано: «$1»" || { bad "НЕ сказано: «$1»"; tail -n 8 "$STUB_DIR/out" >&2; }; }

part "проба ВЕТВЛЕНИЯ · докер подставной, контейнеров не запускается ни одного"
detail "зелёный итог этой пробы НЕ значит, что мир поднимается — это отдельный прогон"
if door_up; then detail "подставная дверь на 127.0.0.1:$PORT поднята"
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

# ================================================================== 10. подсказки
# `--help` обязан работать там, где докера нет вовсе, — иначе человек не узнает про ключи
# ровно тогда, когда они ему и нужны.
part "10. подсказки и непонятный ключ"
run_case 0 "$HERE/up.sh" --help
run_case 0 "$HERE/build.sh" --help
run_case 0 "$HERE/probe.sh" --help
run_case 2 "$HERE/up.sh" --nesuschestvuyuschiy-klyuch

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
