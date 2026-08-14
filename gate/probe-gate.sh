#!/usr/bin/env bash
# Проба шлюза — ломает связь нарочно и требует, чтобы шлюз назвал ступень.
#
#   ./gate/probe-gate.sh          зелёный прогон: что шлюз обязан уметь
#   ./gate/probe-gate.sh --red    красный прогон: восемь нарочных поломок
#   ./gate/probe-gate.sh --both   (умолчание) сначала зелёный, потом красный
#
# ┌─────────────────────────────────────────────────────────────────────────────────────┐
# │ ЧТО ПРОБА СТЕРЕЖЁТ                                                                   │
# │                                                                                      │
# │ Не «работает ли шлюз», а СВОЙСТВО: на каждую поломку он называет СВОЮ ступень и свой │
# │ код, а не общее «не получилось». Ступени чинят разные люди (`gate/README.md`), и      │
# │ перепутанная ступень отправляет чинить не туда — это хуже молчания.                  │
# │                                                                                      │
# │ Проба стережёт КОД (`GATE-REFUSAL: no-route`), а не формулировку. Проба, привязанная  │
# │ к тексту, зеленеет на верной правке и учит не трогать буквы (`WORLD2` 4.2).           │
# └─────────────────────────────────────────────────────────────────────────────────────┘
#
# ЧЕГО ЭТА ПРОБА НЕ ПРОВЕРЯЕТ БЕЗ ВТОРОЙ МАШИНЫ. Шов (том ресурса B, поднятый на этой
# машине) требует живого второго ресурса, `rclone` и `/dev/fuse`. Нет их — проба говорит об
# этом вслух и завершается кодом 3: НЕПОЛНЫЙ ПРОГОН. Зелёного при неполном прогоне не
# бывает: «проверили, что смогли» и «проверили» — разные слова.
#
#   код 0   полный зелёный: прогнано всё, включая шов
#   код 1   краснота: шлюз повёл себя не так, как обязан
#   код 3   неполный прогон: часть проверок не выполнялась, и названо — какая
#
# Живой прогон шва (на машине, где есть rclone, FUSE и второй ресурс):
#
#   GATE_ADDR=user@10.8.0.2 GATE_REMOTE=/srv/scope ./gate/probe-gate.sh --both
#
# Проверки ниже зовутся косвенно, по имени в аргументе; линтер читает это как
# «функция не вызвана».
# shellcheck disable=SC2329
set -euo pipefail
export LC_ALL=C

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
GATE="$HERE/gate.sh"

# Адрес-чёрная дыра: TEST-NET-1 (RFC 5737) отведён под документацию, маршрутизировать его
# в интернет не должен никто. Берём именно его, а не «какой-нибудь чужой IP»: чужой адрес
# однажды ответит, и красная проверка позеленеет по причине, которой мы не заметим.
BLACKHOLE="${GATE_PROBE_BLACKHOLE:-192.0.2.1}"
# Имя, которого нет: `.invalid` зарезервирован RFC 2606 и не разрешается нигде.
BADNAME="${GATE_PROBE_BADNAME:-resource.invalid}"

mode=--both
case "${1:-}" in
    --green|--red|--both) mode="$1" ;;
    -h|--help)
        sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    "") ;;
    *)  printf 'непонятный ключ: %s (см. --help)\n' "$1" >&2; exit 2 ;;
esac

total=0; failed=0; skipped=0

part()   { printf '\n\033[1m%s\033[0m\n' "$*" >&2; }
detail() { printf '      %s\n' "$*" >&2; }
skip()   { skipped=$((skipped + 1)); printf '  \033[33m•\033[0m %s — НЕ ПРОГНАНО\n' "$1" >&2
           shift; local l; for l in "$@"; do detail "$l"; done; }

ok()   { total=$((total + 1)); printf '  \033[32m✔\033[0m %s\n' "$1" >&2; }
bad()  { total=$((total + 1)); failed=$((failed + 1))
         printf '  \033[31m✘\033[0m %s\n' "$1" >&2
         shift; local l; for l in "$@"; do detail "$l"; done; }

# ------------------------------------------------------------------ помощники
# Шлюз печатает машинные строки в stdout, человеческие — в stderr. Проба смотрит на обе:
# отказ обязан быть и читаемым, и разбираемым.
gate_out() { ( "$GATE" "$@" 2>&1 ) || true; }

# want_refusal <код> <имя проверки> — шлюз ОБЯЗАН отказать этим кодом.
want_refusal() {
    local code="$1" name="$2"; shift 2
    local out; out=$(gate_out "$@")
    case "$out" in
        *"GATE-REFUSAL: $code"*) ok "$name → $code" ;;
        *"GATE-REFUSAL: "*)
            bad "$name" "ждали код $code, а шлюз назвал другой:" \
                "$(printf '%s\n' "$out" | grep 'GATE-REFUSAL:' | head -1)" ;;
        *)  bad "$name" "шлюз не отказал вовсе — а обязан был: $code" \
                "$(printf '%s\n' "$out" | tail -3)" ;;
    esac
}

# want_stage <ступень> <ok|fail> <имя проверки> — ступень обязана быть пройдена (или нет).
# Смотрим именно на ступень, а не на итог команды: пока ресурс не настоящий, итог всегда
# отказ, а вот дорога и ответ на живом слушателе обязаны быть зелёными.
want_stage() {
    local st="$1" want="$2" name="$3"; shift 3
    local out; out=$(gate_out "$@")
    case "$out" in
        *"GATE-STAGE: $st $want"*) ok "$name" ;;
        *)  bad "$name" "ступени «$st $want» в ответе нет:" \
                "$(printf '%s\n' "$out" | grep 'GATE-STAGE:' || echo '(ступеней не напечатано вовсе)')" ;;
    esac
}

# Слушатель-заглушка. Нужен, чтобы «дорога» и «ответ» были НАСТОЯЩИМИ: адрес, который
# действительно отвечает по TCP. Ssh на нём нет намеренно — на этом же слушателе проверяется
# ступень «доступ»: служба отвечает, а креды не приняты.
LISTEN_PID=""; PORTFILE=""
start_listener() {
    PORTFILE="$(mktemp)"
    python3 -c '
import socket, sys, time
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 0)); s.listen(8)
print(s.getsockname()[1], flush=True)
# Принимаем и молчим: это НЕ ssh, и ssh обязан на этом покраснеть доступом.
while True:
    try:
        c, _ = s.accept(); time.sleep(0.2); c.close()
    except Exception:
        break
' > "$PORTFILE" &
    LISTEN_PID=$!
    local n=0
    while [ ! -s "$PORTFILE" ] && [ $n -lt 50 ]; do sleep 0.1; n=$((n + 1)); done
    [ -s "$PORTFILE" ] || { printf 'проба не смогла поднять слушателя — python3 есть?\n' >&2; exit 1; }
    LISTEN_PORT="$(head -1 "$PORTFILE")"
}

# Свободный порт: занимаем и сразу отпускаем. Гонка теоретически возможна — за 5 секунд
# порт может занять кто-то другой; тогда проверка «адрес не отвечает» покраснеет чужой
# причиной, и это будет видно в её отказе, а не спрятано.
free_port() {
    python3 -c '
import socket
s = socket.socket(); s.bind(("127.0.0.1", 0)); p = s.getsockname()[1]; s.close(); print(p)'
}

cleanup() {
    [ -n "$LISTEN_PID" ] && kill "$LISTEN_PID" 2>/dev/null || true
    [ -n "$PORTFILE" ] && rm -f "$PORTFILE" || true
}
trap cleanup EXIT

# ------------------------------------------------------------------ зелёный прогон
run_green() {
    part 'ЗЕЛЁНЫЙ — что шлюз обязан уметь'

    # 1. Подсказка работает там, где нет ничего. Это не формальность: без адреса, сети и
    #    инструмента шлюз обязан хотя бы объяснить, чего от него хотят.
    local out
    out=$( "$GATE" --help 2>&1 ) || true
    case "$out" in
        *GATE_ADDR*) ok 'подсказка работает без сети, адреса и инструмента шва' ;;
        *) bad 'подсказка работает без сети' 'в --help нет даже значений' ;;
    esac

    # 2. Живой слушатель: дорога и ответ обязаны быть зелёными.
    start_listener
    GATE_ADDR="probe@127.0.0.1:$LISTEN_PORT" want_stage дорога ok \
        'дорога до живого адреса — пройдена' check
    GATE_ADDR="probe@127.0.0.1:$LISTEN_PORT" want_stage ответ ok \
        'живой адрес отвечает — ступень «ответ» пройдена' check

    # 3. Том не назван — про том шлюз не спрашивает вовсе. Проверка на то, что `check`
    #    годен ДО того, как человек выбрал том: иначе дорогу нечем проверить.
    out=$( GATE_ADDR="probe@127.0.0.1:$LISTEN_PORT" GATE_REMOTE= gate_out check )
    case "$out" in
        *'GATE-STAGE: том'*) bad 'без названного тома ступень «том» не появляется' \
                                 'шлюз спросил про том, которого ему не называли' ;;
        *) ok 'без названного тома ступень «том» не появляется' ;;
    esac

    # 4. Ступень «имя» есть у имени и её нет у адреса числом. Печатать ступень, которой не
    #    было, — это врать зелёным: у числового адреса разрешать нечего.
    out=$( GATE_ADDR="probe@127.0.0.1:$LISTEN_PORT" gate_out check )
    case "$out" in
        *'GATE-STAGE: имя'*) bad 'у адреса числом ступени «имя» нет' \
                                 'шлюз напечатал ступень «имя» там, где имени не было' ;;
        *) ok 'у адреса числом ступени «имя» нет' ;;
    esac

    # 5. Отказ несёт ответ системы, а не пустую строку. Проверка появилась после находки
    #    2026-08-14: ступени звались через `$( )`, то есть в подоболочке, и подробность
    #    умирала вместе с ней — отказ выглядел полным, а объяснения в нём не было.
    local dead_port; dead_port="$(free_port)"
    out=$( GATE_ADDR="probe@127.0.0.1:$dead_port" GATE_TIMEOUT=2 gate_out check )
    if printf '%s\n' "$out" | grep -qE '^      [^[:space:]]'; then
        ok 'отказ несёт ответ системы, а не пустую подробность'
    else
        bad 'отказ несёт ответ системы, а не пустую подробность' \
            'у ступени, которая не прошла, подробность пустая' "$out"
    fi

    # 6. Состояние читается до всякой связи и не врёт: шва нет — так и сказано.
    out=$( GATE_MOUNT="$(mktemp -d)" gate_out status )
    case "$out" in
        *'шов             : не поднят'*) ok 'status на пустой точке говорит «шов не поднят»' ;;
        *) bad 'status на пустой точке говорит «шов не поднят»' "$out" ;;
    esac

    # 5. Шов — живьём. Без второй машины, rclone и FUSE не проверяется НИЧЕМ.
    if [ -n "${GATE_ADDR:-}" ] && [ -n "${GATE_REMOTE:-}" ] \
       && command -v "${GATE_RCLONE:-rclone}" >/dev/null 2>&1 && [ -e /dev/fuse ]; then
        local point; point="$(mktemp -d)"
        out=$( GATE_MOUNT="$point" gate_out up )
        case "$out" in
            *'шов поднят'*) ok 'шов встал: том ресурса B виден на этой машине' ;;
            *) bad 'шов встал: том ресурса B виден на этой машине' "$(printf '%s' "$out" | tail -4)" ;;
        esac
        out=$( GATE_MOUNT="$point" gate_out status )
        case "$out" in
            *'шов             : поднят'*) ok 'status видит поднятый шов' ;;
            *) bad 'status видит поднятый шов' "$out" ;;
        esac

        # Главная проверка шва: связь, а не копия (`WORLD2` 1.6, 3.0). Кладём метку ЧЕРЕЗ
        # шов и ищем её на самом ресурсе ssh'ом. Проверять «каталог непустой» бессмысленно:
        # непустым он бывает и у локальной копии, и проба зеленела бы на оторванном ресурсе.
        local mark="gate-probe-$$" target="$GATE_ADDR" tport=22
        case "$GATE_ADDR" in *:*) target="${GATE_ADDR%:*}"; tport="${GATE_ADDR##*:}" ;; esac
        if : > "$point/$mark" 2>/dev/null \
           && ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -p "$tport" \
                  ${GATE_KEY:+-i "$GATE_KEY"} "$target" \
                  "test -f '${GATE_REMOTE}/$mark'" 2>/dev/null; then
            ok 'том берётся связью, а не копией: метка через шов видна на самом ресурсе'
            rm -f "$point/$mark" 2>/dev/null || true
        else
            bad 'том берётся связью, а не копией' \
                "метка $mark, положенная через шов, на ресурсе B не нашлась" \
                "если метка не создалась вовсе — шов смонтирован только на чтение"
        fi

        out=$( GATE_MOUNT="$point" gate_out down )
        case "$out" in
            *'шов снят'*) ok 'шов снимается' ;;
            *) bad 'шов снимается' "$(printf '%s' "$out" | tail -4)" ;;
        esac
    else
        local why=()
        [ -n "${GATE_ADDR:-}" ]   || why+=('GATE_ADDR не назван — второго ресурса нет')
        [ -n "${GATE_REMOTE:-}" ] || why+=('GATE_REMOTE не назван — тома нет')
        command -v "${GATE_RCLONE:-rclone}" >/dev/null 2>&1 || why+=('rclone на этой машине не стоит')
        [ -e /dev/fuse ] || why+=('/dev/fuse на этой машине нет — FUSE недоступен')
        skip 'шов: том ресурса B виден на этой машине' "${why[@]}" \
             'прогнать так: GATE_ADDR=user@хост GATE_REMOTE=/путь ./gate/probe-gate.sh'
    fi
}

# ------------------------------------------------------------------ красный прогон
run_red() {
    part 'КРАСНЫЙ — ломаем нарочно, шлюз обязан назвать ступень'

    # Поломки идут по ступеням сверху вниз: настройка → дорога → ответ → доступ → шов.
    want_refusal no-address 'адрес не назван' check

    # Ступень «имя» существует только там, где машина умеет разрешать имена. Нет резолвера —
    # проверка не выполняется и говорит об этом вслух, а не краснеет на пустом месте.
    if gate_out status | grep -q 'резолвер имён   : нет'; then
        skip "имя не разрешается ($BADNAME)" \
             'на этой машине нет ни getent, ни python3, ни nslookup — ступени «имя» нет'
    else
        GATE_ADDR="$BADNAME" GATE_TIMEOUT=3 \
            want_refusal no-name "имя не разрешается ($BADNAME)" check
    fi
    GATE_ADDR="probe@$BLACKHOLE" GATE_TIMEOUT=2 \
        want_refusal no-route "канала нет — чёрная дыра ($BLACKHOLE)" check

    local dead; dead="$(free_port)"
    GATE_ADDR="probe@127.0.0.1:$dead" GATE_TIMEOUT=2 \
        want_refusal no-answer "адрес не отвечает — порт $dead пуст" check

    [ -n "${LISTEN_PORT:-}" ] || start_listener
    GATE_ADDR="probe@127.0.0.1:$LISTEN_PORT" GATE_TIMEOUT=2 \
        want_refusal access-denied 'доступ не принят — на порту не ssh' check

    GATE_ADDR="probe@127.0.0.1:${LISTEN_PORT}" GATE_REMOTE= \
        want_refusal no-remote 'том не назван — шов поднимать нечем' up
    GATE_ADDR="probe@127.0.0.1:${LISTEN_PORT}" GATE_REMOTE=/srv/scope \
        GATE_RCLONE=/nonexistent/rclone GATE_MOUNT="$(mktemp -d)" \
        want_refusal no-seam-tool 'инструмента шва нет — отказ назван, а не «не получилось»' up
    GATE_MOUNT="$(mktemp -d)" want_refusal no-mount 'снимать нечего — шва на точке нет' down

    # Шов в чёрную дыру. Проверяется только там, где инструмент шва есть: без него отказ
    # пришёл бы раньше и другой (`no-seam-tool`), то есть проверялось бы не то.
    if command -v "${GATE_RCLONE:-rclone}" >/dev/null 2>&1 && [ -e /dev/fuse ]; then
        GATE_ADDR="probe@$BLACKHOLE" GATE_REMOTE=/srv/scope GATE_TIMEOUT=2 \
            GATE_MOUNT="$(mktemp -d)" \
            want_refusal mount-failed 'шов в чёрную дыру не встаёт — и это сказано' up
    else
        skip 'шов в чёрную дыру не встаёт' \
             'rclone или /dev/fuse на этой машине нет — ломать нечего'
    fi
}

# ------------------------------------------------------------------ прогон
case "$mode" in
    --green) run_green ;;
    --red)   run_red ;;
    --both)  run_green; run_red ;;
esac

printf '\n' >&2
if [ "$failed" -gt 0 ]; then
    printf '\033[1;31m✗ красный: %d из %d проверок не сошлись\033[0m\n' "$failed" "$total" >&2
    exit 1
fi
if [ "$skipped" -gt 0 ]; then
    printf '\033[1;33m• НЕПОЛНЫЙ ПРОГОН: %d из %d сошлись, %d НЕ ПРОГНАНО (см. выше)\033[0m\n' \
        "$total" "$total" "$skipped" >&2
    printf '  зелёным это не считается: непрогнанное не проверено\n' >&2
    exit 3
fi
printf '\033[1;32m✔ зелёный: %d проверок, непрогнанного нет\033[0m\n' "$total" >&2
