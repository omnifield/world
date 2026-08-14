#!/usr/bin/env bash
# Подъём контроллера одной командой: образ → контейнер → отвечает.
#
#   ./control/up.sh                поднять контроллер (петля, порт 8090)
#   ./control/up.sh status         что сейчас: контейнер, здоровье, порт, состояние
#   ./control/up.sh down           снять контроллер; состояние (ключи, скоупы) остаётся
#   ./control/up.sh down --with-state   снять вместе с состоянием: ключи и скоупы стираются
#
# ┌─────────────────────────────────────────────────────────────────────────────────────┐
# │ КОНТРОЛЛЕР СТАВИТ ЧЕЛОВЕК, И ТОЛЬКО ЧЕЛОВЕК. Ни мир, ни другой контроллер его не      │
# │ разворачивают: цепочка начинается с юзера, и первое звено не автоматизируется         │
# │ (`control/README.md`, `WORLD2` 3.7). Эта команда — то самое первое звено.             │
# │                                                                                      │
# │ Ставь его там, где не жалко: у контроллера есть докер-сокет, то есть власть над этой  │
# │ машиной. Захватили контроллер — захватили ЭТОТ ресурс. Поэтому наружу он по умолчанию │
# │ не торчит: публикация только в петлю (`CONTROL_BIND`).                                │
# └─────────────────────────────────────────────────────────────────────────────────────┘
#
# Имена переменных ЛАТИНСКИЕ: bash не поддерживает не-ASCII в именах и молча превращает
# `ПОРТ=8090` в «команда не найдена». Тексты и комментарии — русские.
set -euo pipefail
# Ступени отказов различаются по тексту системных сообщений, а он переводится.
export LC_ALL=C

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd -- "$HERE/.." && pwd)"
COMPOSE_FILE="$HERE/compose.yaml"
NAME=world-control

# Порт, петля и время ожидания — значения, а не константы: их называет хозяин машины.
PORT="${CONTROL_PORT:-8090}"
BIND="${CONTROL_BIND:-127.0.0.1}"
# Адрес, по которому МАШИНА видит свой же опубликованный порт. Отдельным значением по той
# же причине, что у соседа (`deploy/up.sh`): демон докера и человек не всегда на одной
# петле (живой случай — докер в WSL, команда из Git Bash).
HOST="${CONTROL_HOST:-127.0.0.1}"
WAIT="${CONTROL_WAIT:-60}"

usage() {
    cat <<'USAGE'
Контроллер мира — руки юзера: ./control/up.sh <команда>

  (без команды)          поднять контроллер
  status                 что сейчас: контейнер, здоровье, порт, состояние
  down [--with-state]    снять; --with-state стирает ключи и заведённые здесь скоупы
  -h --help              эта подсказка

Значения:
  CONTROL_PORT=8090        хост-порт контроллера
  CONTROL_BIND=127.0.0.1   на какой петле публиковать; 0.0.0.0 — наружу, СОЗНАТЕЛЬНО
  CONTROL_HOST=127.0.0.1   адрес, по которому эта машина видит свой опубликованный порт
  CONTROL_WAIT=60          сколько секунд ждём, пока контроллер станет здоровым
  WORLD_PORT=8080          хост-порт двери на ресурсах, которые контроллер будет добавлять

Что появляется на этой машине: контейнер world-control и три тома (ключи, контексты
докера, скоупы, заведённые здесь). Больше ничего — ни файлов, ни пакетов.

Контроллер раздаёт ПУЛЬТ (лицо для человека) тем же адресом, что и ручки, поэтому к
подъёму пульт должен быть собран: ./deploy/build.sh --only-web

Отказ печатается двумя строками: причина и выход — человеку, `CONTROL-REFUSAL: <код>` —
машине. Коды: no-docker · no-compose · no-daemon · no-compose-file · no-pult-artifact ·
port-busy · up-failed · control-silent · no-container.
USAGE
}

# ------------------------------------------------------------------ разговор
step()   { printf '\n\033[1m→ %s\033[0m\n' "$*" >&2; }
ok()     { printf '  ✓ %s\n' "$*" >&2; }
warn()   { printf '  \033[33m!\033[0m %s\n' "$*" >&2; }
say()    { printf '%s\n' "$*" >&2; }
detail() { printf '      %s\n' "$*" >&2; }

# refuse <код> <причина> <выход…> — единственный способ выйти с ошибкой. Причина и выход
# обязательны оба: «нельзя» без выхода — провал самого мира (`WORLD2` 2.3). Код идёт в
# stdout и предназначен машине; проба стережёт КОД, а не формулировку (`WORLD2` 4.2).
refuse() {
    local code="$1" why="$2"; shift 2
    printf 'CONTROL-REFUSAL: %s\n' "$code"
    printf '\n\033[1;31m✗ отказ:\033[0m %s\n' "$why" >&2
    local line; for line in "$@"; do printf '  выход: %s\n' "$line" >&2; done
    exit 1
}

# ------------------------------------------------------------------ чем поднимаем
need_tools() {
    command -v docker >/dev/null 2>&1 || refuse no-docker \
        "докера на этой машине нет — поднимать контроллер нечем" \
        "поставь Docker Engine либо Docker Desktop: https://docs.docker.com/engine/install/" \
        "докер — единственное, что мир требует на машине юзера (TECH 2)"

    docker compose version >/dev/null 2>&1 || refuse no-compose \
        "у докера нет compose v2 (плагина docker-compose-plugin)" \
        "поставь плагин: https://docs.docker.com/compose/install/" \
        "им же поднимается дверь — тот же плагин нужен и зоне deploy"

    docker info >/dev/null 2>&1 || refuse no-daemon \
        "докер есть, а демон не отвечает — контроллеру нечем распоряжаться машиной" \
        "запусти его: systemctl start docker (либо открой Docker Desktop)" \
        "демон работает — дело в правах: usermod -aG docker \$USER и перезайти"

    [ -f "$COMPOSE_FILE" ] || refuse no-compose-file \
        "файла запуска $COMPOSE_FILE нет" \
        "зови скрипт из репозитория: ./control/up.sh" \
        "файл потерян — возьми его из репозитория заново"
}

compose() { docker compose -f "$COMPOSE_FILE" "$@"; }

# ------------------------------------------------------------------ up
cmd_up() {
    need_tools

    step "порт $BIND:$PORT"
    # Занятый порт ловим ДО подъёма: иначе компоуз скажет своё «bind: address already in
    # use», и человек будет искать поломку в контроллере, а не в том, кто занял порт.
    if docker ps --format '{{.Names}}\t{{.Ports}}' | grep -v "^$NAME	" | grep -q ":$PORT->"; then
        refuse port-busy \
            "порт $PORT на этой машине уже занят другим контейнером" \
            "возьми другой: CONTROL_PORT=8091 ./control/up.sh" \
            "посмотри, кто занял: docker ps --format '{{.Names}}\t{{.Ports}}'"
    fi
    ok "свободен"

    # Пульт ДОЛЖЕН БЫТЬ СОБРАН к этому моменту: контроллер раздаёт лицо для человека
    # (`WORLD2` 3.7), и образ кладёт его внутрь. Ловим здесь, до сборки: иначе тем же
    # отказом ответит `RUN test -s` внутри докера — верно, но чужими словами и в середине
    # длинного вывода сборки.
    #
    # Собрать пульт САМИ мы не беремся, хотя команда известна. Сборка требует поля (склад
    # пакетов пульта виден только соседям по сети мира), а подъём поля требовать не должен —
    # это правило зоны `deploy`, и нарушать его чужой командой нельзя.
    step "собранный пульт"
    PULT="$ROOT/deploy/.build/web-dist/index.html"
    [ -s "$PULT" ] || refuse no-pult-artifact \
        "собранного пульта нет: $PULT пуст или отсутствует" \
        "собери его первым шагом: ./deploy/build.sh --only-web (для этого шага нужно поле)" \
        "и повтори подъём: ./control/up.sh" \
        "ручки контроллера работают и без лица — но образ с пустым пультом мы не собираем"
    ok "пульт собран — уедет в образ"

    step "поднимаю контроллер"
    # `--build` намеренно всегда: образ контроллера собирается из этого же репозитория за
    # секунды (бинарь на стандартной библиотеке), а «поднял старый образ и не заметил» —
    # это ровно тот ложный результат, из-за которого проба потом зеленеет впустую
    # (`WORLD2-92`).
    CONTROL_PORT="$PORT" CONTROL_BIND="$BIND" compose up -d --build || refuse up-failed \
        "контроллер не поднялся" \
        "посмотри, что сказал докер: docker compose -f $COMPOSE_FILE up --build" \
        "образ не собрался — сборка идёт из корня репозитория, зови команду из него"
    ok "контейнер $NAME запущен"

    step "жду, пока контроллер ответит (до ${WAIT}с)"
    local waited=0 health
    while [ "$waited" -lt "$WAIT" ]; do
        health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$NAME" 2>/dev/null || true)"
        case "$health" in
            healthy|running) break ;;
            exited|dead)
                say ""
                docker logs --tail 20 "$NAME" >&2 || true
                refuse up-failed \
                    "контроллер поднялся и тут же вышел" \
                    "журнал выше — в нём причина" \
                    "полный журнал: docker logs $NAME" ;;
        esac
        sleep 1; waited=$((waited + 1))
    done
    [ "$health" = healthy ] || [ "$health" = running ] || refuse control-silent \
        "контроллер за ${WAIT}с так и не стал здоровым (сейчас: ${health:-неизвестно})" \
        "журнал: docker logs $NAME" \
        "дай больше времени: CONTROL_WAIT=120 ./control/up.sh"
    ok "контроллер здоров"

    # Образ мира на ЭТОЙ машине — не требование к подъёму контроллера, но требование к
    # добавлению ресурса: дверь туда едет копией отсюда (`WORLD2` 1.5). Сказать об этом
    # надо СЕЙЧАС, а не в момент, когда человек нажал «добавить ресурс».
    if ! docker image inspect omnifield/world:dev >/dev/null 2>&1; then
        warn "образа мира omnifield/world:dev на этой машине нет"
        detail "добавление ресурса откажет кодом no-image: дверь туда везётся копией отсюда"
        detail "собрать: ./deploy/build.sh"
    fi

    say ""
    say "контроллер: http://$HOST:$PORT"
    say ""
    say "  лицо для человека: открой http://$HOST:$PORT в браузере"
    say "  кто я сейчас   : curl http://$HOST:$PORT/api/me"
    say "  войти в скоуп  : curl -X POST http://$HOST:$PORT/api/session -d '{\"addr\":\"/scope/я\",\"create\":true,\"name\":\"я\"}'"
    say "  что дальше     : control/README.md"
}

# ------------------------------------------------------------------ status
cmd_status() {
    need_tools
    local state
    state="$(docker ps -a --filter "name=^$NAME$" --format '{{.Status}}' 2>/dev/null | head -n1 || true)"

    say ""
    say "контейнер : ${state:-нет}"
    [ -n "$state" ] || { say ""; say "  поднять: ./control/up.sh"; return 0; }

    # Каждая строка — отдельный вопрос. Состояние, выведенное вместо измеренного, врёт
    # ровно тогда, когда на него смотрят (`WORLD2` 4.2).
    say "здоровье  : $(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}HEALTHCHECK-а нет{{end}}' "$NAME" 2>/dev/null || echo неизвестно)"
    say "порт      : $(docker port "$NAME" 2>/dev/null | tr '\n' ' ' || echo 'публикации не видно')"
    say "сокет     : $(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/var/run/docker.sock"}}отдан{{end}}{{end}}' "$NAME" 2>/dev/null || true)"
    say "состояние : $(docker volume ls --format '{{.Name}}' | grep -E '^world-control_control-(keys|docker|scope)$' | tr '\n' ' ' || echo 'томов нет')"
    say ""
    say "  журнал: docker logs $NAME"
    say "  снять : ./control/up.sh down"
}

# ------------------------------------------------------------------ down
cmd_down() {
    need_tools
    local with_state="${1:-}"

    docker ps -a --format '{{.Names}}' | grep -qx "$NAME" || refuse no-container \
        "контроллера на этой машине нет — снимать нечего" \
        "посмотри, что есть: docker ps -a" \
        "поднять: ./control/up.sh"

    step "снимаю контроллер"
    if [ "$with_state" = "--with-state" ]; then
        # Состояние стирается только по явному слову: в томах лежат ключи юзера и его
        # скоупы, заведённые здесь. Снести их «заодно» значит потерять личность.
        compose down --volumes || refuse up-failed \
            "контроллер не снялся" \
            "посмотри, что сказал докер: docker compose -f $COMPOSE_FILE down --volumes"
        ok "контроллер и его состояние сняты — ключи и скоупы стёрты"
    else
        compose down || refuse up-failed \
            "контроллер не снялся" \
            "посмотри, что сказал докер: docker compose -f $COMPOSE_FILE down"
        ok "контроллер снят"
        say ""
        say "  осталось на машине: ключи ресурсов, контексты докера и скоупы, заведённые здесь"
        say "  снять и их       : ./control/up.sh down --with-state"
        say "  двери на ресурсах НЕ сняты — они живут своей жизнью: ./deploy/remote.sh list"
    fi
}

# ------------------------------------------------------------------ разбор
case "${1:-up}" in
    -h|--help)  usage; exit 0 ;;
    up|"")      cmd_up ;;
    status)     cmd_status ;;
    down)       shift || true; cmd_down "${1:-}" ;;
    *)          usage >&2; printf '\nнепонятная команда: %s\n' "$1" >&2; exit 2 ;;
esac
