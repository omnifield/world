#!/usr/bin/env bash
# Снятие ЛОКАЦИИ: сначала ВЫХОД ИЗ ПОЛЯ, потом контейнер.
#
#   ./deploy/location-down.sh
#
# ПОРЯДОК ЗДЕСЬ — ГЛАВНОЕ, и он обратный подъёму. Снести контейнер первым значит оставить в
# поле запись о локации, до которой уже не достучаться: дверь будет честно вести маршрут в
# пустоту и отвечать `location-unreachable` каждому, кто по нему пойдёт. Реестр за дверью —
# это СОСТОЯНИЕ ПОЛЯ (`kb:WORLD-28`), а не чья-то память, и убирать себя из него обязана
# сама уходящая локация — тем же бинарём, которым входила (`world leave`).
#
# Отсюда и существование этого файла. Соблазн сказать «снимается обычным `docker compose
# down`» велик, но тогда правильное снятие оказывается на человеке — и однажды он его не
# сделает, а маршрут в пустоту заметит не он.
#
# ПОЛЕ НЕ ТРОГАЕТСЯ БОЛЬШЕ, ЧЕМ НУЖНО: снимается ровно одна локация — та, что названа в
# конфиге. Ни дверь, ни соседи, ни тома мира отсюда не затрагиваются вовсе.
set -euo pipefail

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
CONFIG="${WORLD_LOCATION_ENV:-$HERE/location.env}"

usage() {
    cat <<'USAGE'
Снятие локации: ./deploy/location-down.sh [--config ФАЙЛ] [--keep-in-field]

  (без ключей)      выйти из поля (world leave), затем снять контейнер
  --config ФАЙЛ     другой конфиг (умолчание deploy/location.env) — тот же, которым
                    локацию поднимали: им она и находится
  --keep-in-field   снять только контейнер, из поля НЕ выходить. Нужно редко и
                    осознанно: локация останется в реестре, и дверь будет вести
                    маршрут в пустоту, пока она не поднимется обратно
  -h, --help        эта подсказка

Переменные среды:
  WORLD_LOCATION_ENV=ФАЙЛ   то же, что --config

Снимается ровно одна локация — названная в конфиге. Дверь, соседи и поле в целом
не трогаются: у мира своё снятие, docker compose -f deploy/compose.yaml down.
USAGE
}

keep_in_field=
while [ $# -gt 0 ]; do
    case "$1" in
        --keep-in-field) keep_in_field=1 ;;
        --config)  shift; [ $# -gt 0 ] || { printf 'у --config нет значения: --config deploy/location.env\n' >&2; exit 2; }
                   CONFIG="$1" ;;
        --config=*) CONFIG="${1#--config=}" ;;
        -h|--help) usage; exit 0 ;;
        *) usage >&2; printf '\nнепонятный ключ: %s\n' "$1" >&2; exit 2 ;;
    esac
    shift
done

step() { printf '\n\033[1m→ %s\033[0m\n' "$*" >&2; }
ok()   { printf '  ✓ %s\n' "$*" >&2; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*" >&2; }

fail() {
    local why="$1"; shift
    printf '\n\033[1;31m✗ локация не снята: %s\033[0m\n' "$why" >&2
    local line
    for line in "$@"; do printf '  выход: %s\n' "$line" >&2; done
    exit 1
}

# Правило зоны: путь хоста в аргументе — обычный `docker`; путей хоста нет, а контейнерные
# есть (`/app/world`) — `docker_noconv`. Довод целиком — в `build.sh`.
docker_noconv() { MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*' docker "$@"; }

# ------------------------------------------------------------------ 1. какая локация
step "конфиг локации: ${CONFIG#"$HERE/"}"
# shellcheck source=location-config.sh
. "$HERE/location-config.sh"
ok "снимаю «$WORLD_NAME» (дверь $WORLD_DOOR, сеть $WORLD_NET)"

command -v docker >/dev/null 2>&1 || fail \
    "докера на машине нет — снимать нечем" \
    "поставь Docker Engine либо Docker Desktop: https://docs.docker.com/engine/install/"

docker info >/dev/null 2>&1 || fail \
    "докер есть, а демон не отвечает" \
    "запусти его: systemctl start docker (либо открой Docker Desktop)"

# ------------------------------------------------------------------ 2. выход из поля
# Выходим ИЗ КОНТЕЙНЕРА ЛОКАЦИИ, пока он ещё жив, — тем же бинарём, которым входили. Так
# адрес двери берётся из той же настройки, что и на входе, и второй копии здесь не заводится.
CID="$("${COMPOSE[@]}" ps -q "$SERVICE" 2>/dev/null | head -n1 || true)"

if [ -n "$keep_in_field" ]; then
    warn "--keep-in-field: из поля НЕ выхожу — запись о локации останется в реестре двери"
    warn "пока локация не поднимется обратно, дверь будет отвечать location-unreachable на её маршруте"
elif [ -z "$CID" ]; then
    # Контейнера нет, а запись в поле могла остаться — от прошлого снятия руками, например.
    # Молчать нельзя: это ровно тот маршрут в пустоту, ради которого файл и написан.
    warn "контейнера локации нет — выходить из поля нечем (из него выходит сама локация)"
    warn "если запись о «$WORLD_NAME» в поле осталась, снять её можно с хоста, где поднят мир:"
    warn "  curl -X DELETE http://127.0.0.1:<хост-порт двери>/api/locations/$WORLD_NAME"
else
    step "выхожу из поля — дверь $WORLD_DOOR"
    leave_err="$(mktemp)"
    trap 'rm -f "$leave_err"' EXIT
    set +e
    leave_out="$(docker_noconv exec "$CID" /app/world leave 2>"$leave_err")"
    leave_status=$?
    set -e
    if [ "$leave_status" = 0 ]; then
        printf '  %s\n' "$leave_out" >&2
    else
        # Не отказ всего снятия: контейнер снять всё равно надо, иначе останется и он, и
        # запись в поле. Но сказать, что запись могла остаться, обязаны — своими словами
        # дверь уже всё объяснила, и мы их не переписываем (`core/README.md`).
        printf '\n--- что сказала локация ---\n' >&2
        cat "$leave_err" >&2
        warn "из поля выйти не вышло — снимаю контейнер, но запись в поле могла остаться"
        warn "чем убрать её с хоста: curl -X DELETE http://127.0.0.1:<хост-порт двери>/api/locations/$WORLD_NAME"
    fi
fi

# ------------------------------------------------------------------ 3. контейнер
step "снимаю контейнер локации"
"${COMPOSE[@]}" down --remove-orphans || fail \
    "контейнер локации не снялся" \
    "посмотри, что осталось: docker compose -p $PROJECT -f deploy/location-compose.yaml ps -a" \
    "снести силой: docker rm -f world-loc-$WORLD_NAME"
ok "контейнер снят"

cat >&2 <<GOTOVO

$(printf '\033[1;32m✓ локация «%s» снята\033[0m' "$WORLD_NAME")

  томов у локации нет — терять было нечего; поле мира при этом цело и стоит как стояло
  кто сейчас в поле   curl -sS http://127.0.0.1:<хост-порт двери>/api/locations
  поднять обратно     ./deploy/location-up.sh$CONFIG_KEY   (повторный вход не ошибка)
GOTOVO
