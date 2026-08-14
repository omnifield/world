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
# │   3. подъём двери зовётся ГОТОВЫЙ (`deploy/remote.sh`), а не написан заново;          │
# │   4. отказ приходит тройкой: код · причина · выходы — и код соседа доезжает СВОИМ;    │
# │   5. неудача не оставляет следов: ключ снятого ресурса не переживает ресурс.          │
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
SRV_PID=""
TOKEN=""

cleanup() {
    [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null || true
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
for a in "$@"; do
    case "$a" in
        context) printf 'default\tunix:///var/run/docker.sock\nworld-vps\tssh://world@10.8.0.5\n'; exit 0 ;;
        inspect) printf 'healthy\n'; exit 0 ;;
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
}

# start_server [docker] [remote.sh] — поднять контроллер с названными инструментами.
# Подменяемость инструментов — не удобство пробы, а то, ради чего они вынесены значениями:
# поведение контроллера обязано проверяться там, где контура нет.
start_server() {
    local docker="${1:-$TMP/docker}" remote="${2:-$TMP/remote.sh}"
    stop_server
    rm -rf "$KEYS" "$SCOPE"; mkdir -p "$KEYS"
    : > "$CALLS"
    TOKEN=""

    CALLS="$CALLS" REFUSE_FILE="$TMP/refuse" \
    CONTROL_ADDR="127.0.0.1:$PORT" CONTROL_DOCKER="$docker" CONTROL_REMOTE_SH="$remote" \
    CONTROL_KEYS="$KEYS" CONTROL_SSH_TIMEOUT=2 CONTROL_TOOL_TIMEOUT=20 \
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

    call POST /api/resources '{"name":"vps2","addr":"world@10.8.0.6","creds":"-----ключ-----"}'
    want_status 201 "ресурс добавляется"
    if grep -q 'remote.sh add vps2 --addr world@10.8.0.6' "$CALLS"; then
        ok "подъём двери позван ГОТОВЫЙ, своего зона не пишет"
    else
        bad "готовый подъём" "в журнале вызовов нет remote.sh add:" "$(cat "$CALLS")"
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

    # Шов с зоной `deploy`: имена контекста и контейнера двери — ЕЁ, у нас они повторены.
    # Разъедутся молча — список ресурсов опустеет при живых дверях.
    part "ЗЕЛЁНЫЙ — шов с зоной deploy"
    if [ -f "$ROOT/deploy/remote.sh" ]; then
        if grep -q '^PREFIX=world-' "$ROOT/deploy/remote.sh"; then
            ok "приставка контекста та же, что у соседа (world-)"
        else
            bad "приставка контекста" "в deploy/remote.sh она уже другая — наш список ресурсов ослепнет"
        fi
        if grep -q '^DOOR=world-door' "$ROOT/deploy/remote.sh"; then
            ok "имя контейнера двери то же, что у соседа (world-door)"
        else
            bad "имя двери" "в deploy/remote.sh оно уже другое — «жив ли» будет врать"
        fi
    else
        skip "шов с deploy" "deploy/remote.sh рядом нет — сверять не с чем"
    fi
    stop_server
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
}

# ------------------------------------------------------------------ чего не прогнали
unproven() {
    part "ЧЕГО ЭТОТ ПРОГОН НЕ ПРОВЕРИЛ"
    if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
        skip "подъём контроллера образом" \
            "докер на машине есть — прогони живьём: ./control/up.sh, затем ./control/up.sh status" \
            "проба нарочно не поднимает контейнеров: подъём — действие хозяина машины"
    else
        skip "подъём контроллера образом (./control/up.sh)" \
            "докера на этой машине нет — образ не собрать и не поднять" \
            "прогон на машине с докером: ./control/up.sh && ./control/up.sh status"
    fi
    skip "настоящий подъём двери на втором ресурсе" \
        "нужен второй ресурс с докером и ssh; здесь подъём подменён заглушкой" \
        "живьём: добавить ресурс через POST /api/resources и увидеть дверь: ./deploy/remote.sh status <имя>"
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
