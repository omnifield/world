#!/usr/bin/env bash
# Сборка образа мира — ДВА ШАГА, и они разные по природе.
#
#   ./deploy/build.sh
#
#   шаг 1  собрать пульт    обычный контейнер В СЕТИ `omnifield-gateway`
#   шаг 2  сложить образ    `docker build` БЕЗ СЕТИ вообще
#
# Почему не одним `docker build` с `build.network`. Потому что так не работает на обычной
# машине: buildkit произвольную сеть в сборке не поддерживает — сети умеет только
# контейнерный драйвер, созданный заранее (`buildx create --driver-opt network=…`). То есть
# «сборка внутри поля» шла бы лишь там, где машину к этому специально готовили, а на чистой
# падала бы посреди сборки. Проверено живым прогоном (`tasker:WORLD-37`).
#
# А обычный `docker run --network omnifield-gateway` работает всегда — им же проверяется и
# склад. Отсюда раскладка: **стройка снаружи образа, образ только складывает готовое**
# (`kb:WORLD-54`). Заодно исчезает единственное место, где сборка требовала особой
# настройки машины, а `docker build` становится проверяемо офлайновым: `network: none`
# в файле запуска — не украшение, а доказательство, что на втором шаге не тянется ничего.
#
# Что куда ложится:
#
#   web/                      исходник пульта — читается, НЕ правится (чужая зона)
#   deploy/.build/web-dist/   собранный пульт — сюда пишет шаг 1, отсюда читает шаг 2
#
# Собранное кладётся в СВОЮ зону, а не в `web/dist`: зона `web` — чужая, и затирать в ней
# рабочую сборку разработчика сборка мира не вправе.
#
# Имена переменных латинские: bash не поддерживает не-ASCII в именах переменных и молча
# превращает `ПОРТ=8080` в «команда не найдена». Тексты и комментарии — русские.
set -euo pipefail

NET=omnifield-gateway
NODE_IMAGE=node:24-alpine
PNPM_VERSION=11.17.0
PROBE_IMAGE=alpine:3

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd -- "$HERE/.." && pwd)"
OUT="$HERE/.build/web-dist"
COMPOSE=("docker" "compose" "-f" "$HERE/compose.yaml")

usage() {
    cat <<'USAGE'
Сборка образа мира: ./deploy/build.sh [--only-web|--only-image]

  (без ключей)    оба шага: собрать пульт в сети, потом сложить образ без сети
  --only-web      только шаг 1 — собрать пульт в deploy/.build/web-dist
  --only-image    только шаг 2 — сложить образ из того, что уже собрано
  -h, --help      эта подсказка

Шаг 1 идёт В СЕТИ omnifield-gateway: пакеты @omnifield/probe-web-* лежат на складе
локации probe-web, и наружу он не торчит (kb:FUND-5). То есть СБОРКЕ нужно поле.
Шаг 2 сети не имеет вовсе — он только копирует готовое.

ПОДЪЁМУ поле не нужно: ./deploy/up.sh поднимает готовый образ и ничего не собирает.
USAGE
}

do_web=1
do_image=1
while [ $# -gt 0 ]; do
    case "$1" in
        --only-web)   do_image= ;;
        --only-image) do_web= ;;
        -h|--help)    usage; exit 0 ;;
        *) usage >&2; printf '\nнепонятный ключ: %s\n' "$1" >&2; exit 2 ;;
    esac
    shift
done

step() { printf '\n\033[1m→ %s\033[0m\n' "$*" >&2; }
ok()   { printf '  ✓ %s\n' "$*" >&2; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*" >&2; }

fail() {
    local why="$1"; shift
    printf '\n\033[1;31m✗ не собралось: %s\033[0m\n' "$why" >&2
    local line
    for line in "$@"; do printf '  выход: %s\n' "$line" >&2; done
    exit 1
}

command -v docker >/dev/null 2>&1 || fail "докера на машине нет" \
    "поставь Docker Engine: https://docs.docker.com/engine/install/"
docker info >/dev/null 2>&1 || fail "докер есть, а демон не отвечает" \
    "запусти его: systemctl start docker (либо открой Docker Desktop)"

# ============================================================== ШАГ 1 — собрать пульт
if [ -n "$do_web" ]; then
    # Сеть нужна самому шагу: без неё контейнер-сборщик не увидит склад.
    step "общая сеть $NET"
    if docker network inspect "$NET" >/dev/null 2>&1; then
        ok "сеть уже есть"
    else
        docker network create "$NET" >/dev/null || fail "общую сеть $NET не создать" \
            "посмотри, что мешает: docker network create $NET"
        ok "сеть создана"
    fi

    # ---------- склад: три исхода, а не два ----------
    # Проверяем ПЕРЕД сборкой пульта — то есть перед тем шагом, которому склад и нужен.
    # «Не смог проверить» и «склад не отвечает» — разные вещи, и выдавать первое за второе
    # значит отправить человека чинить не то (`kb:WORLD-31`).
    step "склад пакетов пульта (probe-web) — нужен шагу 1"
    probe_ready=1
    if ! docker image inspect "$PROBE_IMAGE" >/dev/null 2>&1; then
        docker pull -q "$PROBE_IMAGE" >/dev/null 2>&1 || probe_ready=
    fi

    if [ -z "$probe_ready" ]; then
        warn "проверить склад НЕЧЕМ: образа-пробника $PROBE_IMAGE нет локально и скачать его не вышло"
        warn "это НЕ значит, что склада нет — значит, что я не знаю. Иду собирать; если склада"
        warn "и правда нет, сборка упадёт на pnpm install, и причина будет названа там"
    else
        set +e
        docker run --rm --network "$NET" "$PROBE_IMAGE" \
            wget -q -T 5 -O /dev/null http://probe-web:4873/ >/dev/null 2>&1
        probe_status=$?
        set -e
        case "$probe_status" in
            0) ok "склад отвечает — пульт соберётся" ;;
            125|126|127)
                warn "проверить склад НЕЧЕМ: контейнер-пробник не запустился (код $probe_status)"
                warn "это НЕ значит, что склада нет — значит, что я не знаю. Иду собирать" ;;
            *)
                fail \
                    "склад probe-web в сети $NET не отвечает, а без него не собрать пульт" \
                    "если образ мира уже есть на машине — сборка не нужна вовсе: ./deploy/up.sh поднимет из него, и поле не понадобится" \
                    "собрать здесь: подними probe-web в сети $NET (пакеты @omnifield/probe-web-* лежат только там и наружу не торчат, kb:FUND-5)" \
                    "собрать не здесь: собери образ там, где поле есть, и перенеси — docker save omnifield/world:dev | ssh <машина> docker load" \
                    "проверка руками: docker run --rm --network $NET $PROBE_IMAGE wget -qO- http://probe-web:4873/" ;;
        esac
    fi

    # ---------- сама сборка пульта ----------
    # Исходник монтируется ТОЛЬКО НА ЧТЕНИЕ и копируется внутрь контейнера: зона `web`
    # чужая, и ни `node_modules`, ни `dist` в ней от сборки мира появиться не должны.
    # Стор pnpm — на именованном томе: иначе каждая сборка качает половину npm заново.
    step "шаг 1 — собираю пульт в контейнере (сеть $NET)"
    mkdir -p "$OUT"
    docker run --rm \
        --network "$NET" \
        -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
        -v "$ROOT/web:/src/web:ro" \
        -v "$OUT:/out" \
        -v world-pnpm-store:/pnpm-store \
        "$NODE_IMAGE" sh -euc '
            npm install -g pnpm@'"$PNPM_VERSION"' >/dev/null 2>&1
            mkdir -p /work
            cp -R /src/web/. /work/
            rm -rf /work/node_modules /work/dist
            cd /work
            pnpm install --frozen-lockfile --store-dir /pnpm-store
            pnpm build
            rm -rf /out/..?* /out/.[!.]* /out/* 2>/dev/null || true
            cp -R /work/dist/. /out/
            chown -R "$HOST_UID:$HOST_GID" /out
        ' || fail \
            "пульт не собрался" \
            "полный вывод сборщика — выше, причина названа там" \
            "чаще всего это склад (@omnifield/probe-web-* не приехали) либо локфайл разъехался с package.json" \
            "повторить только этот шаг: ./deploy/build.sh --only-web"

    [ -s "$OUT/index.html" ] || fail \
        "сборщик отработал, а собранной страницы в $OUT нет" \
        'посмотри вывод шага 1 выше: pnpm build мог отработать вхолостую' \
        "повторить: ./deploy/build.sh --only-web"

    ok "пульт собран в ${OUT#"$ROOT"/}"
fi

# ============================================================== ШАГ 2 — сложить образ
if [ -n "$do_image" ]; then
    # Сети у этого шага нет вовсе (`network: none` в файле запуска). Он ничего не тянет —
    # только копирует собранное и компилирует Go, у которого зависимостей нет.
    step "шаг 2 — складываю образ (без сети)"

    [ -s "$OUT/index.html" ] || fail \
        "собранного пульта в $OUT нет, а образ его только складывает" \
        "собери пульт: ./deploy/build.sh --only-web (для этого шага нужно поле)" \
        "либо оба шага сразу: ./deploy/build.sh"

    "${COMPOSE[@]}" build || fail \
        "образ не сложился" \
        "полный вывод — выше, причина названа там" \
        "начисто: docker compose -f deploy/compose.yaml build --no-cache"
    ok "образ сложен"
fi

printf '\n\033[1;32m✓ готово\033[0m — поднять: ./deploy/up.sh\n' >&2
