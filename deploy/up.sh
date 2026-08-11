#!/usr/bin/env bash
# Подъём мира одной командой: сеть → образ → контейнер → дверь отвечает.
#
#   ./deploy/up.sh                 дверь на :8080
#   WORLD_PORT=8090 ./deploy/up.sh дверь на :8090
#
# Скрипт ничего не решает сам — вся раскладка в `deploy/compose.yaml`. Он существует ради
# двух вещей, которых у компоуза нет: общая сеть создаётся ДО контейнера (войти в
# несуществующую сеть докер не даёт, и первый подъём на чистой машине без этого не встаёт),
# и каждый отказ называет причину и выход, а не только код возврата (`kb:WORLD-31`).
#
# Имена переменных ЛАТИНСКИЕ, хотя весь репозиторий говорит по-русски. Это не небрежность:
# bash не поддерживает не-ASCII в именах переменных и молча превращает `ПОРТ=8080` в
# «команда не найдена», а `$ПОРТ` — в четыре буквы текста. Тексты и комментарии русские,
# идентификаторы латинские.
set -euo pipefail

NET=omnifield-gateway
PORT="${WORLD_PORT:-8080}"
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE=("docker" "compose" "-f" "$HERE/compose.yaml")

step() { printf '\n\033[1m→ %s\033[0m\n' "$*" >&2; }
ok()   { printf '  ✓ %s\n' "$*" >&2; }

# fail ПРИЧИНА ВЫХОД… — единственный способ выйти с ошибкой. Выход обязателен: отказ без
# следующего действия это диагноз, а не отказ.
fail() {
    local why="$1"; shift
    printf '\n\033[1;31m✗ не поднялось: %s\033[0m\n' "$why" >&2
    local line
    for line in "$@"; do printf '  выход: %s\n' "$line" >&2; done
    exit 1
}

# ------------------------------------------------------------------ 1. чем поднимаем
step "проверяю, чем поднимать"

command -v docker >/dev/null 2>&1 || fail \
    "докера на машине нет" \
    "поставь Docker Engine либо Docker Desktop: https://docs.docker.com/engine/install/" \
    "больше миру ничего не нужно — ни ноды, ни девконтейнера"

docker info >/dev/null 2>&1 || fail \
    "докер есть, а демон не отвечает" \
    "запусти его: systemctl start docker (либо открой Docker Desktop)" \
    "если демон работает — дело в правах: usermod -aG docker \$USER и перезайти"

"${COMPOSE[@]}" version >/dev/null 2>&1 || fail \
    "у докера нет compose v2 (плагина docker-compose-plugin)" \
    "поставь плагин: https://docs.docker.com/compose/install/" \
    "проверка: docker compose version"

ok "докер и compose v2 на месте"

# ------------------------------------------------------------------ 2. общая сеть
# Идемпотентно: сеть уже есть — не сделано ничего. Ровно то же делает девбокс своим
# initializeCommand, и по той же причине.
step "общая сеть $NET"
if docker network inspect "$NET" >/dev/null 2>&1; then
    ok "сеть уже есть"
else
    docker network create "$NET" >/dev/null || fail \
        "общую сеть $NET не создать" \
        "посмотри, что мешает: docker network create $NET"
    ok "сеть создана"
fi

# ------------------------------------------------------------------ 3. склад пульта
# Проверяем ДО сборки, а не посреди неё: без склада `pnpm install` упадёт минуты через две
# сообщением про ненайденный пакет, и искать причину пойдут в зону web.
step "склад пакетов пульта (probe-web)"
if docker run --rm --network "$NET" alpine:3 \
       wget -q -T 5 -O /dev/null http://probe-web:4873/ >/dev/null 2>&1; then
    ok "склад отвечает — фронт соберётся"
else
    fail \
        "склада probe-web в сети $NET не видно, а без него не собрать пульт" \
        "образ собирается ВНУТРИ ПОЛЯ: пакеты @omnifield/probe-web-* лежат на складе локации probe-web, и наружу он не торчит (kb:FUND-5)" \
        "подними probe-web в этой сети — либо собери образ там, где поле есть, и перенеси: docker save omnifield/world:dev | ssh <машина> docker load" \
        "проверка руками: docker run --rm --network $NET alpine:3 wget -qO- http://probe-web:4873/"
fi

# ------------------------------------------------------------------ 4. образ
step "собираю образ мира (фронт → бинарь → тонкий слой)"
"${COMPOSE[@]}" build || fail \
    "образ не собрался" \
    "полный вывод сборки — выше этой строки, причина названа там" \
    "чаще всего это зона web (склад/локфайл) либо зона core (компиляция): пересобрать начисто — docker compose -f deploy/compose.yaml build --no-cache"
ok "образ собран"

# ------------------------------------------------------------------ 5. контейнер
step "поднимаю дверь на хост-порту $PORT"
if ! WORLD_PORT="$PORT" "${COMPOSE[@]}" up -d --remove-orphans; then
    fail \
        "контейнер не встал на порт $PORT" \
        "порт $PORT занят кем-то ещё — возьми другой: WORLD_PORT=8090 $0" \
        "кто держит порт: docker ps --filter publish=$PORT (либо ss -ltnp | grep :$PORT)" \
        "наружу торчит ровно одна дверь (kb:FUND-5) — второй мир на той же машине поднимают на другом хост-порту"
fi
ok "контейнер запущен"

# ------------------------------------------------------------------ 6. дверь отвечает
# «Запущен» и «отвечает» — разные вещи, и подтверждать надо второе. Стучимся СНАРУЖИ
# контейнера, по опубликованному порту: изнутри опубликованный порт не проверить, а
# HEALTHCHECK образа как раз изнутри и стучится — он про процесс, а не про публикацию.
#
# Чем стучимся — тем, что есть на машине. Стук не часть подъёма (миру нужен только докер),
# поэтому отсутствие curl и wget здесь не отказ, а пропуск с честной строкой.
knock() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsS -m 2 -o /dev/null "$1"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -T 2 -O /dev/null "$1"
    else
        return 2   # нечем
    fi
}

step "жду, пока дверь ответит"
for try in {1..30}; do
    knock "http://127.0.0.1:$PORT/healthz" && answer=yes || answer=$?
    if [ "$answer" = yes ]; then
        ok "дверь отвечает на http://127.0.0.1:$PORT/healthz"
        break
    fi
    if [ "$answer" = 2 ]; then
        printf '  · ни curl, ни wget на машине нет — проверить дверь снаружи нечем\n' >&2
        printf '    состояние процесса покажет сам докер: docker ps --filter name=world-door\n' >&2
        break
    fi
    if [ "$try" = 30 ]; then
        printf '\n--- последние строки журнала двери ---\n' >&2
        "${COMPOSE[@]}" logs --tail=40 >&2 || true
        fail \
            "дверь не ответила за 30 секунд" \
            "журнал выше — мир называет причину сам, первой же строкой" \
            "весь журнал: docker compose -f deploy/compose.yaml logs -f" \
            "состояние контейнера: docker ps -a --filter name=world-door"
    fi
    sleep 1
done

# ------------------------------------------------------------------ куда смотреть
cat >&2 <<GOTOVO

$(printf '\033[1;32m✓ мир поднят\033[0m')

  вход в поле   http://127.0.0.1:$PORT/            пульт: кто сейчас в поле
  проба жизни   http://127.0.0.1:$PORT/healthz
  кто в поле    http://127.0.0.1:$PORT/api/locations

  состояние поля живёт на томе world-field и переживает пересоздание контейнера
  журнал        docker compose -f deploy/compose.yaml logs -f
  снять         docker compose -f deploy/compose.yaml down          (поле остаётся)
  снять с полем docker compose -f deploy/compose.yaml down -v       (поле стирается)

  проверить подъём целиком: ./deploy/probe.sh
GOTOVO
