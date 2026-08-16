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
# │   1. до входа нет ничего — ни ресурсов, ни полей;                                     │
# │   2. скоуп берётся связью, а не копией: личность лежит ТАМ, куда её положили;         │
# │   3. подъём вещи зовётся ГОТОВЫЙ (`deploy/remote.sh`), а не написан заново;           │
# │   4. отказ приходит тройкой: код · причина · выходы — и код соседа доезжает СВОИМ;    │
# │   5. неудача не оставляет следов: ключ снятого ресурса не переживает ресурс;          │
# │   6. подъём берёт ГОТОВЫЙ образ и говорит, ЧТО именно поднято, — тег и digest;        │
# │   7. точка входа образа поднимает пульт ОДНОЙ командой: проверяет власть над машиной, │
# │      дожидается ответа стуком, отказывает кодом и не бросает процесс сиротой — а       │
# │      команда в README не разъехалась с файлом запуска;                                │
# │   8. контроллер не знает, какие бывают ВЕЩИ: рецепт кладут в каталог, и вторая вещь    │
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
SCOPE="$TMP/scope"
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
# Отвечаем ровно те поля, которые контроллер спросил. `ps` — какие контейнеры есть,
# `inspect` — чьи они (метка проекта компоуза), здоровы ли и запущены ли. Своего имени
# контейнера у контроллера больше нет: что стоит на ресурсе, говорит та машина.
for a in "$@"; do
    case "$a" in
        context) printf 'default\tunix:///var/run/docker.sock\nworld-vps\tssh://world@10.8.0.5\n'; exit 0 ;;
        ps)      printf 'aaa111\n'; exit 0 ;;
        inspect) printf 'world\thealthy\trunning\n'; exit 0 ;;
    esac
done
exit 0
FAKE
    cat > "$TMP/remote.sh" <<'FAKE'
#!/usr/bin/env bash
printf 'remote.sh %s\n' "$*" >> "$CALLS"
if [ -f "$REFUSE_FILE" ]; then
    printf 'REMOTE-REFUSAL: %s\n' "$(cat "$REFUSE_FILE")"
    printf '\n\033[1;31m✗ отказ:\033[0m ресурс world@10.8.0.5 не принял ключ\n' >&2
    printf '  выход: положи открытый ключ юзеру на тот ресурс\n' >&2
    exit 1
fi
exit 0
FAKE
    chmod +x "$TMP/docker" "$TMP/remote.sh"
    # Рецепт двери лежит РЯДОМ С ПОДЪЁМОМ — по той же формуле, по которой его выводит сам
    # контроллер. Содержимое неважно: разбирает рецепт компоуз у соседа, а не мы.
    printf 'name: world\n' > "$TMP/compose.yaml"
    # Каталог рецептов — ландшафт машины. Пустой: вещи в него кладёт хозяин, а не образ.
    rm -rf "$RECIPES"; mkdir -p "$RECIPES"
}

# start_server [docker] [remote.sh] — поднять контроллер с названными инструментами.
# Подменяемость инструментов — не удобство пробы, а то, ради чего они вынесены значениями:
# поведение контроллера обязано проверяться там, где контура нет.
start_server() {
    local docker="${1:-$TMP/docker}" remote="${2:-$TMP/remote.sh}"
    stop_server
    rm -rf "$KEYS" "$SCOPE" "$RECIPES"; mkdir -p "$KEYS" "$RECIPES"
    : > "$CALLS"
    TOKEN=""

    CALLS="$CALLS" REFUSE_FILE="$TMP/refuse" \
    CONTROL_ADDR="127.0.0.1:$PORT" CONTROL_DOCKER="$docker" CONTROL_REMOTE_SH="$remote" \
    CONTROL_KEYS="$KEYS" CONTROL_SSH_TIMEOUT=2 CONTROL_TOOL_TIMEOUT=20 \
    CONTROL_PULT="$PULT" CONTROL_RECIPES="$RECIPES" \
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

want_status() {
    local want="$1" name="$2"
    [ "$STATUS" = "$want" ] && ok "$name" || bad "$name" "код HTTP $STATUS вместо $want" "$BODY"
}

вход() {
    call POST /api/session "{\"addr\":\"$SCOPE\",\"create\":true,\"name\":\"егор\",\"brand\":\"омнифилд\"}"
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
    mkdir -p "$ENTRY_STATE/keys" "$ENTRY_STATE/docker" "$ENTRY_STATE/scope" \
             "$ENTRY_STATE/pult" "$ENTRY_STATE/deploy"
    chmod 700 "$ENTRY_STATE/keys"
    printf '<!doctype html>лицо мира' > "$ENTRY_STATE/pult/index.html"
    # Файл запуска соседа: имя образа двери отдаёт подставной докер, но САМ файл должен
    # лежать там, где его ищет точка входа, — рядом с готовым подъёмом двери.
    : > "$ENTRY_STATE/deploy/compose.yaml"
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
        "CONTROL_SCOPE_DIR=$ENTRY_STATE/scope"
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
mk_up_stub
mk_entry

# ------------------------------------------------------------------ зелёный
green() {
    part "ЗЕЛЁНЫЙ — что контроллер обязан уметь"
    rm -f "$TMP/refuse"
    start_server || { bad "подъём контроллера" "процесс не поднялся"; return; }

    call GET /api/me
    want_refusal not-signed-in "до входа «кто я» отвечает «не входил»"

    call GET /api/resources
    want_refusal not-signed-in "до входа ресурсов не существует"

    call POST /api/session "{\"addr\":\"$SCOPE\"}"
    want_refusal no-scope "вход в пустоту зовёт завести скоуп здесь"

    вход
    want_status 201 "первый вход заводит личность"
    want_has '"created":true' "вход сказал, что личность заведена"

    # Скоуп — это МЕСТО, а не память процесса: личность обязана лежать там, куда её
    # положили (`WORLD2` 1.6). Смотрим на диск, а не на ответ.
    if [ -f "$SCOPE/identity.json" ] && grep -q 'егор' "$SCOPE/identity.json"; then
        ok "личность лежит в скоупе, а не в памяти контроллера"
    else
        bad "личность в скоупе" "файла $SCOPE/identity.json нет или он не про того"
    fi

    call GET /api/me
    want_has '"name":"егор"' "«кто я» отвечает той же личностью"

    call GET /api/fields
    want_has '"fields":[]' "поля пусты, но список есть"

    call POST /api/fields '{"name":"дом"}'
    want_status 201 "поле заводится"
    want_has 'не поднимается' "сказано вслух, что поле пока не поднимается"

    call GET /api/resources
    want_has '"here":true' "в списке есть ресурс, где стоит контроллер"
    want_has '"name":"vps"' "в списке есть заведённый ресурс"
    # РЕСУРС — МАШИНА, А НЕ «МАШИНА С ДВЕРЬЮ» (`WORLD2-131`). Что на ней стоит — отдельный
    # ответ: список вещей плюс отдельный вопрос «отвечает ли сам ресурс».
    want_has '"reach":"отвечает"' "ресурс говорит про СЕБЯ — отвечает он или молчит"
    want_has '"things":[{"name":"world"' "что стоит на ресурсе — СПИСОК вещей, а не одно поле про дверь"
    case "$BODY" in
        *'"door"'*) bad "поле двери" "в ответе снова одно поле про дверь — вторая вещь опять потребует правки кода: $BODY" ;;
        *) ok "поля «дверь» в ответе больше нет" ;;
    esac

    call GET /api/recipes
    want_has '"name":"door"' "чем поднимать — отдельный список, и дверь в нём один из рецептов"

    call POST /api/resources '{"name":"vps2","addr":"world@10.8.0.6","creds":"-----ключ-----"}'
    want_status 201 "ресурс добавляется"
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
    if [ -f "$KEYS/world-vps2" ] && grep -q "IdentityFile $KEYS/world-vps2" "$KEYS/config" 2>/dev/null; then
        ok "ключ лёг в связку и назван в config — докер возьмёт его оттуда"
    else
        bad "ключ в связке" "ключа или блока в config нет: $(ls "$KEYS" 2>/dev/null | tr '\n' ' ')"
    fi

    call DELETE /api/resources/vps2
    want_status 200 "ресурс снимается"
    want_has '"left"' "снятие называет, что осталось на той машине"
    if [ ! -f "$KEYS/world-vps2" ]; then
        ok "ключ снятого ресурса не пережил ресурс"
    else
        bad "след после снятия" "ключ остался в связке"
    fi

    # ┌─────────────────────────────────────────────────────────────────────────────────┐
    # │ ГЛАВНАЯ ПРОВЕРКА ЗАХОДА (`WORLD2-131`): вторая вещь поднимается тем же путём —    │
    # │ БЕЗ единой правки кода и БЕЗ пересборки образа. Кладём второй рецепт в каталог    │
    # │ (это и есть то, что сделает хозяин машины монтированием) и зовём его по имени.    │
    # │ Не сработало — задача не сделана, чем бы ни были зелены остальные проверки.       │
    # └─────────────────────────────────────────────────────────────────────────────────┘
    part "ЗЕЛЁНЫЙ — вторая вещь: положили рецепт, и она поднимается"
    printf 'name: весы\n' > "$RECIPES/весы.yaml"

    call GET /api/recipes
    want_has '"name":"весы"' "положенный рецепт появился в списке сам — перечня вещей в коде нет"

    call POST /api/resources '{"name":"vps3","addr":"world@10.8.0.7","recipe":"весы"}'
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

    call POST /api/resources '{"name":"vps4","addr":"world@10.8.0.8","recipe":"часы"}'
    want_refusal no-such-recipe "рецепта, которого нет, контроллер не выдумывает"

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
    want_no_entry_code "непишущаяся связка не отменяет подъёма — скоуп на другом ресурсе законен"
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
                "-v world-control-docker:/root/.docker" "-v world-control-scope:/scope" "restart"; do
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

    # ТОМА В КОМАНДЕ И В ФАЙЛЕ ЗАПУСКА — ОДНИ И ТЕ ЖЕ. Иначе путь юзера и путь разработчика
    # молча заведут два разных состояния на одной машине, и человек, перешедший с одного на
    # другой, увидит пустую связку при живых ключах рядом.
    local tom
    for tom in world-control-keys world-control-docker world-control-scope; do
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
    for put in /root/.ssh /root/.docker /scope; do
        if grep -q "mkdir -p .*$put\|mkdir -p /root/.ssh /root/.docker /scope" "$HERE/Dockerfile" \
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
    start_server || { bad "подъём контроллера" "процесс не поднялся"; return; }

    call POST /api/session '{"addr":"просто-слово"}'
    want_refusal bad-address "адрес не похож ни на путь, ни на юзер@машина:путь"

    call POST /api/session '{"addr":"@10.8.0.5:/srv/scope"}'
    want_refusal bad-address "юзер в адресе не назван — молча брать текущего нельзя"

    call POST /api/session ''
    want_refusal no-body "тело пустое"

    call POST /api/session '{"addr":"/x","адрес":"/y"}'
    want_refusal bad-body "лишнее поле не проглатывается молча"

    call POST /api/session "{\"addr\":\"$SCOPE\",\"creds\":\"ключ\",\"create\":true,\"name\":\"я\"}"
    want_refusal creds-here "креды к местному скоупу не проглатываются молча"

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

    call POST /api/session "{\"addr\":\"$SCOPE\",\"create\":true,\"name\":\"другой\"}"
    want_refusal scope-exists "личность не заводится поверх личности"

    call POST /api/fields '{"name":"дом"}' >/dev/null
    call POST /api/fields '{"name":"дом"}'
    want_refusal field-exists "поле не заводится дважды"

    call DELETE /api/resources/here
    want_refusal drop-here "ресурс, на котором стоит контроллер, снять нельзя"

    call POST /api/resources '{"name":"../побег","addr":"world@10.8.0.5"}'
    want_refusal bad-name "имя ресурса, уводящее из связки, не принимается"

    # Отказ соседа обязан доехать СВОИМ кодом: свой словарь тех же отказов разъехался бы
    # с его словарём на первой правке.
    printf 'access-denied' > "$TMP/refuse"
    call POST /api/resources '{"name":"vps2","addr":"world@10.8.0.5","creds":"ключ"}'
    want_refusal access-denied "код подъёма доезжает своим, а не переписанным"
    want_has '"from":"deploy/remote.sh"' "названо, чей это отказ"
    if [ ! -f "$KEYS/world-vps2" ]; then
        ok "неудачный подъём не оставил ключ за собой"
    else
        bad "след после неудачи" "ключ остался в связке — вторая попытка пойдёт ключом-призраком"
    fi
    rm -f "$TMP/refuse"

    # Покалеченная личность не должна притворяться входом.
    printf '{это не json' > "$SCOPE/identity.json"
    TOKEN=""
    call POST /api/session "{\"addr\":\"$SCOPE\"}"
    want_refusal scope-broken "покалеченная личность названа поломкой, а не «нет скоупа»"

    # Чужой ресурс, до которого нет дороги. Адрес — TEST-NET-1 (RFC 5737), отведён под
    # документацию: маршрутизировать его в интернет не должен никто. Чужой «какой-нибудь»
    # адрес однажды ответил бы, и проверка позеленела бы по причине, которой мы не видим.
    call POST /api/session '{"addr":"world@192.0.2.1:/srv/scope","creds":"ключ"}'
    case "$BODY" in
        *'"code":"no-route"'*|*'"code":"no-answer"'*|*'"code":"scope-silent"'*|*'"code":"scope-unreachable"'*|*'"code":"no-ssh"'*)
            ok "недостижимый скоуп назван ступенью связи, а не общим «не получилось»" ;;
        *)  bad "недостижимый скоуп" "ждали ступень связи, получили:" "$BODY" ;;
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
    start_server "$TMP/docker" "$TMP/нет-такого-подъёма" || { bad "подъём контроллера" "не поднялся"; return; }
    вход
    call POST /api/resources '{"name":"vps2","addr":"world@10.8.0.5"}'
    want_refusal no-remote-tool "подъёма двери нет в образе — сказано прямо"
    stop_server

    start_server "$TMP/нет-такого-докера" "$TMP/remote.sh" || { bad "подъём контроллера" "не поднялся"; return; }
    вход
    call GET /api/resources
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
    skip "скоуп на настоящем чужом ресурсе по ssh" \
        "нужен второй ресурс; проверена только ступень отказа на недостижимом адресе" \
        "живьём: POST /api/session с addr=user@<ресурс>:/srv/scope и настоящим ключом"
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
