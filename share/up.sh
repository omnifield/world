#!/usr/bin/env bash
# Подъём раздачи ИЗ РЕПОЗИТОРИЯ: образ → контейнер → отвечает.
#
# ┌─────────────────────────────────────────────────────────────────────────────────────┐
# │ ЭТО ПУТЬ РАЗРАБОТЧИКА ЗОНЫ, А НЕ ПУТЬ ЮЗЕРА. У юзера раздача поднимается САМА — её    │
# │ ставит контроллер на названной машине по рецепту (`share/compose.yaml`), и ни этого   │
# │ скрипта, ни репозитория там нет (`TECH` 2: на машине только докер).                   │
# │                                                                                      │
# │ Здесь остаётся то, чего у юзера нет и не должно быть: сборка из исходников, подъём    │
# │ компоузом здесь же и снятие вместе с состоянием.                                     │
# └─────────────────────────────────────────────────────────────────────────────────────┘
#
#   SHARE_PASSWORD=… ./share/up.sh              поднять раздачу (порт 8070)
#   SHARE_PASSWORD=… ./share/up.sh --build      собрать образ из исходников и поднять
#   ./share/up.sh status                        что сейчас: контейнер, здоровье, состояние
#   ./share/up.sh down                          снять раздачу; ЛИЧНОСТЬ ОСТАЁТСЯ в томе
#   ./share/up.sh down --with-state             снять вместе с личностью — она стирается
#
# Имена переменных ЛАТИНСКИЕ: bash не поддерживает не-ASCII в именах и молча превращает
# `ПОРТ=8070` в «команда не найдена». Тексты и комментарии — русские.
set -euo pipefail
export LC_ALL=C

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
RECIPE="$HERE/compose.yaml"
NAME="${SHARE_NAME:-world-share}"
PORT="${SHARE_PORT:-8070}"

say()    { printf '%s\n' "$*" >&2; }
refuse() { # отказ = причина + выход (`WORLD2` 2.3)
    local why="$1"; shift
    printf '\nне вышло: %s\n' "$why" >&2
    local way; for way in "$@"; do printf '  выход: %s\n' "$way" >&2; done
    exit 1
}

docker version >/dev/null 2>&1 || refuse \
    "докера на этой машине нет или он не отвечает" \
    "поставь докер и подними демон — им поднимается всё в этом репозитории" \
    "проверь руками: docker version"
docker compose version >/dev/null 2>&1 || refuse \
    "докер есть, а compose к нему не подключён" \
    "поставь плагин docker-compose-plugin" \
    "проверь руками: docker compose version"

case "${1:-up}" in
    -h|--help) sed -n '2,21p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;

    up|--build)
        [ -n "${SHARE_PASSWORD:-}" ] || refuse \
            "пароль скоупа не назван, а раздача им закрыта (\`WORLD2\` 3.4)" \
            "назови его: SHARE_PASSWORD=<пароль> ./share/up.sh" \
            "внутри файла состояния его нет и быть не может — это ключ от замка, а не из замка"

        args=(-f "$RECIPE" up -d)
        if [ "${1:-up}" = "--build" ]; then args+=(--build); else args+=(--no-build); fi
        # `--no-build` в обычной ветке не украшение: не найдя образа, компоуз собрал бы его
        # молча, и «поднял готовое» перестало бы отличаться от «собрал своё».
        docker compose "${args[@]}" || refuse \
            "раздача не поднялась" \
            "журнал: docker logs $NAME" \
            "если образа нет — собери его: ./share/up.sh --build"

        # Ждём не «контейнер создан», а ОТВЕТ: поднятый и повисший процесс выглядит
        # одинаково зелёным, пока в него не постучали.
        for _ in $(seq 30); do
            if [ "$(docker inspect -f '{{.State.Health.Status}}' "$NAME" 2>/dev/null)" = healthy ]; then
                say "раздача отвечает: http://127.0.0.1:$PORT/ (том состояния — $NAME-state)"
                say "взять состояние:   curl -u :\$SHARE_PASSWORD http://127.0.0.1:$PORT/"
                say "положить целиком:  curl -u :\$SHARE_PASSWORD -T состояние.json http://127.0.0.1:$PORT/"
                exit 0
            fi
            sleep 1
        done
        refuse \
            "раздача поднялась, но за 30 секунд не ответила стуку" \
            "журнал: docker logs $NAME" \
            "стук изнутри: docker exec $NAME /share knock — он же стоит в HEALTHCHECK образа"
        ;;

    status)
        docker ps -a --filter "name=^${NAME}$" --format 'контейнер: {{.Names}} · {{.Status}} · {{.Ports}}'
        docker volume inspect "${NAME}-state" --format 'том личности: {{.Name}} · {{.Mountpoint}}' 2>/dev/null \
            || say "тома личности нет — раздачу ещё не поднимали, либо его сняли с --with-state"
        ;;

    down)
        args=(-f "$RECIPE" down --remove-orphans)
        if [ "${2:-}" = "--with-state" ]; then
            args+=(-v)
            say "ВНИМАНИЕ: снимаю вместе с томом — личность, лежащая в нём, стирается"
        fi
        # Компоузу нужен пароль даже на снятие: он читает рецепт целиком и упрётся в `:?`.
        # Подставляем пустышку — снятию она безразлична, а человеку не придётся называть
        # настоящий пароль, чтобы убрать контейнер.
        SHARE_PASSWORD="${SHARE_PASSWORD:-снятие}" docker compose "${args[@]}"
        ;;

    *) refuse "непонятная команда: ${1}" "что умеет: ./share/up.sh --help" ;;
esac
