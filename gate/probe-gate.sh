#!/usr/bin/env bash
# Проба шлюза — ломает связь нарочно и требует, чтобы шлюз назвал ступень.
#
#   ./gate/probe-gate.sh          зелёный прогон: что шлюз обязан уметь
#   ./gate/probe-gate.sh --red    красный прогон: нарочные поломки
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
# ПРОБА ГОНЯЕТСЯ В КОНТЕЙНЕРЕ И НИЧЕГО НЕ ПРОСИТ ПОСТАВИТЬ НА ХОСТ (`TECH` 2). Второй
# ресурс она поднимает СЕБЕ САМА — тем же `rclone`, который есть в образе ради шва:
# `rclone serve sftp` даёт настоящий ssh-конец, `rclone serve http` — настоящий не-ssh.
# Ни второй машины, ни `python3`, ни `nc`, ни `sshd` для этого не нужно.
#
# Отсюда важное следствие для ступени «доступ»: она проверяется на НАСТОЯЩЕМ ssh-конце, а
# не по недостижимости. Прошлая редакция зеленела на том, что до блока проверки дело просто
# не доходило (ревью `WORLD2-96`, пункт 1) — такая проверка проходит и в сломанном шлюзе.
#
#   код 0   полный зелёный: прогнано всё, включая шов
#   код 1   краснота: шлюз повёл себя не так, как обязан
#   код 3   неполный прогон: часть проверок не выполнялась, и названо — какая
#
# Код 3 — это и про обстановку тоже: нет `rclone` или FUSE — прогон НЕПОЛНЫЙ, а не красный.
# Своё же правило «непрогнанное не проверено» проба применяет и к себе (`WORLD2-96`, п. 5).
#
# Проверки ниже зовутся косвенно, по имени в аргументе; линтер читает это как
# «функция не вызвана».
# shellcheck disable=SC2329
set -euo pipefail
export LC_ALL=C

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
GATE="$HERE/gate.sh"

# Инструмент шва — он же то, чем проба поднимает себе ресурс. Значением, а не именем:
# в образе он может лежать не в `$PATH`, и проба обязана уметь на него показать.
RCLONE="${GATE_RCLONE:-rclone}"

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

# ------------------------------------------------------------------ обстановка прогона
# Своя песочница на весь прогон. Ключи хостов проба пишет В СВОЙ файл, а не в тот, которым
# живёт человек: проба, которая правит настоящее состояние машины, — уже не проба.
WORK="$(mktemp -d)"
export GATE_KNOWN_HOSTS="$WORK/known_hosts"
# Значения человека в прогон не текут: ресурс проба поднимает свой, и адрес из окружения
# увёл бы её проверять чужую машину, молча и не тем.
unset GATE_ADDR GATE_REMOTE GATE_MOUNT GATE_KEY 2>/dev/null || true

SERVE_PIDS=()
cleanup() {
    local p
    for p in ${SERVE_PIDS[@]+"${SERVE_PIDS[@]}"}; do kill "$p" 2>/dev/null || true; done
    rm -rf "$WORK" 2>/dev/null || true
}
trap cleanup EXIT

have_rclone() { command -v "$RCLONE" >/dev/null 2>&1; }
have_fuse()   { [ -e /dev/fuse ] && { command -v fusermount3 >/dev/null 2>&1 || command -v fusermount >/dev/null 2>&1; }; }

# free_port — порт, на котором заведомо никого нет. Стучимся встроенным в bash `/dev/tcp`:
# отказ соединения и означает «свободен». Раньше порт выбирал `python3`, и проба падала
# кодом 1 на машине без него — то есть краснела обстановкой, а не шлюзом (`WORLD2-96`, п. 5).
#
# Гонка названа честно: между выбором и прогоном порт может занять кто-то другой. Тогда
# проверка «адрес не отвечает» покраснеет чужой причиной — это будет видно в её отказе.
free_port() {
    local p n=0
    while [ "$n" -lt 50 ]; do
        p=$(( 20000 + RANDOM % 20000 ))
        (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null || { printf '%s' "$p"; return 0; }
        n=$((n + 1))
    done
    return 1
}

# Ключи прогона: одна пара юзера (ей пускают) и одна ЧУЖАЯ (ей обязаны не пустить), плюс
# ключ хоста для нашего ssh-конца. Ключ хоста задаётся ЯВНО, а не берётся из кэша rclone:
# так конец воспроизводим, а в файле ключей хостов оказывается ровно один алгоритм.
KEYS_READY=0
make_keys() {
    [ "$KEYS_READY" = 1 ] && return 0
    command -v ssh-keygen >/dev/null 2>&1 || return 1
    ssh-keygen -q -t ed25519 -N '' -f "$WORK/id"      </dev/null >/dev/null 2>&1 || return 1
    ssh-keygen -q -t ed25519 -N '' -f "$WORK/id-alien"</dev/null >/dev/null 2>&1 || return 1
    ssh-keygen -q -t ed25519 -N '' -f "$WORK/hostkey" </dev/null >/dev/null 2>&1 || return 1
    KEYS_READY=1
}

# serve_port <лог> — дождаться, пока поднятый конец назовёт свой порт. Порт спрашиваем У
# НЕГО (`--addr 127.0.0.1:0`), а не выбираем сами: выбранный заранее порт — это гонка на
# ровном месте, а гонка в пробе однажды покраснеет не тем.
serve_port() {
    local log="$1" n=0 line=""
    while [ "$n" -lt 100 ]; do
        # Ищем просто «127.0.0.1:порт»: у `serve sftp` и `serve http` строки разные
        # («listening on …» против «Server started on [http://…]»), и стеречь надо адрес,
        # а не формулировку чужого инструмента — она поменяется в первом же его выпуске.
        line="$(grep -oE '127\.0\.0\.1:[0-9]+' "$log" 2>/dev/null | tail -1)"
        [ -n "$line" ] && { printf '%s' "${line##*:}"; return 0; }
        sleep 0.1; n=$((n + 1))
    done
    return 1
}

# Имя тома, которым проверяется дыра с экранированием (ревью `WORLD2-96`, пункт 2): кавычки
# внутри пути. Путь называет ЧЕЛОВЕК, и пока он подставлялся в команду чужой оболочки,
# одинарная кавычка ломала команду на той стороне. Стережём свойство «путь уезжает целым»,
# а не то, каким способом он экранирован.
TRICKY='том с '"'"'кавычкой'"'"' и "двойной"'

# Настоящий ssh-конец: `rclone serve sftp`. Это не заглушка — по нему ходит тот же `sftp`,
# которым шлюз проверяет доступ, и тот же `rclone`, которым он поднимает шов.
SSH_PORT=""; SSH_DIR=""
start_ssh_end() {
    [ -n "$SSH_PORT" ] && return 0
    have_rclone || return 1
    make_keys   || return 1
    SSH_DIR="$WORK/ресурс-B"; mkdir -p "$SSH_DIR/том" "$SSH_DIR/$TRICKY"
    "$RCLONE" serve sftp --addr 127.0.0.1:0 --user probe \
        --authorized-keys "$WORK/id.pub" --key "$WORK/hostkey" \
        --dir-cache-time 1s "$SSH_DIR" > "$WORK/ssh-end.log" 2>&1 &
    SERVE_PIDS+=($!)
    SSH_PORT="$(serve_port "$WORK/ssh-end.log")" || { SSH_PORT=""; return 1; }
}

# Настоящий НЕ-ssh конец: живой слушатель, который ключа хоста не даёт. На нём проверяется
# ступень «доступ»: дорога есть, ответ есть, а ssh на порту нет.
WEB_PORT=""
start_web_end() {
    [ -n "$WEB_PORT" ] && return 0
    have_rclone || return 1
    "$RCLONE" serve http --addr 127.0.0.1:0 "$WORK" > "$WORK/web-end.log" 2>&1 &
    SERVE_PIDS+=($!)
    WEB_PORT="$(serve_port "$WORK/web-end.log")" || { WEB_PORT=""; return 1; }
}

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
want_stage() {
    local st="$1" want="$2" name="$3"; shift 3
    local out; out=$(gate_out "$@")
    case "$out" in
        *"GATE-STAGE: $st $want"*) ok "$name" ;;
        *)  bad "$name" "ступени «$st $want» в ответе нет:" \
                "$(printf '%s\n' "$out" | grep 'GATE-STAGE:' || echo '(ступеней не напечатано вовсе)')" ;;
    esac
}

# Почему обстановки нет — одной причиной, одинаково во всех пропусках.
why_no_env() {
    have_rclone || { printf 'инструмента шва «%s» здесь нет — а он обязан быть в образе (TECH 2)' "$RCLONE"; return; }
    command -v ssh-keygen >/dev/null 2>&1 || { printf 'ssh-keygen здесь нет — пакет openssh-client обязан быть в образе'; return; }
    printf 'ssh-конец не поднялся: %s' "$(tail -1 "$WORK/ssh-end.log" 2>/dev/null || echo '(лога нет)')"
}

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

    # 2. Состояние читается до всякой связи и не врёт: шва нет — так и сказано.
    out=$( GATE_MOUNT="$(mktemp -d -p "$WORK")" gate_out status )
    case "$out" in
        *'шов             : не поднят'*) ok 'status на пустой точке говорит «шов не поднят»' ;;
        *) bad 'status на пустой точке говорит «шов не поднят»' "$out" ;;
    esac

    if ! start_ssh_end; then
        skip 'весь зелёный прогон на настоящем ресурсе' "$(why_no_env)" \
             'без ssh-конца недостижимы: доступ · том · ключ хоста · шов' \
             'в образе с rclone и openssh-client прогон идёт целиком и без второй машины'
        return 0
    fi

    local addr="probe@127.0.0.1:$SSH_PORT"

    # 3. Живой ресурс: дорога, ответ и ДОСТУП обязаны быть зелёными. Доступ — на настоящем
    #    ssh-конце, а не по недостижимости: это и есть пункт 1 ревью.
    GATE_ADDR="$addr" GATE_KEY="$WORK/id" want_stage дорога ok \
        'дорога до живого ресурса — пройдена' check
    GATE_ADDR="$addr" GATE_KEY="$WORK/id" want_stage ответ ok \
        'живой ресурс отвечает — ступень «ответ» пройдена' check
    GATE_ADDR="$addr" GATE_KEY="$WORK/id" want_stage доступ ok \
        'креды приняты настоящим ssh — ступень «доступ» пройдена' check

    # 4. Том назван и он есть — ступень «том» пройдена.
    GATE_ADDR="$addr" GATE_KEY="$WORK/id" GATE_REMOTE=/том want_stage том ok \
        'названный том найден на ресурсе — ступень «том» пройдена' check

    # 4a. Том с кавычками в имени доезжает целым. Шлюз на той стороне ничего не запускает,
    #     ломать нечего — и эта проверка стережёт именно это, а не форму экранирования.
    GATE_ADDR="$addr" GATE_KEY="$WORK/id" GATE_REMOTE="/$TRICKY" want_stage том ok \
        'путь с кавычками доезжает целым — ступень «том» пройдена' check

    # 5. Том НЕ назван — про том шлюз не спрашивает вовсе. Проверка на то, что `check` годен
    #    ДО того, как человек выбрал том. Раньше она зеленела, потому что до этого места
    #    прогон не доходил вовсе (`WORLD2-96`, п. 1); теперь доходит — и стережёт свойство.
    out=$( GATE_ADDR="$addr" GATE_KEY="$WORK/id" GATE_REMOTE= gate_out check )
    case "$out" in
        *'GATE-STAGE: том'*) bad 'без названного тома ступень «том» не появляется' \
                                 'шлюз спросил про том, которого ему не называли' ;;
        *'GATE-STAGE: доступ ok'*) ok 'без названного тома ступень «том» не появляется' ;;
        *)  bad 'без названного тома ступень «том» не появляется' \
                'до ступени «том» прогон вообще не дошёл — проверка ничего не стережёт' \
                "$(printf '%s\n' "$out" | grep 'GATE-' | tail -2)" ;;
    esac

    # 6. Ступень «имя» есть у имени и её нет у адреса числом. Печатать ступень, которой не
    #    было, — это врать зелёным: у числового адреса разрешать нечего.
    out=$( GATE_ADDR="$addr" GATE_KEY="$WORK/id" gate_out check )
    case "$out" in
        *'GATE-STAGE: имя'*) bad 'у адреса числом ступени «имя» нет' \
                                 'шлюз напечатал ступень «имя» там, где имени не было' ;;
        *) ok 'у адреса числом ступени «имя» нет' ;;
    esac

    # 7. Ключ хоста: первый контакт записывается, второй СВЕРЯЕТСЯ. Свойство, а не текст:
    #    без записи доверие раздавалось бы заново каждый раз и подмену никто бы не заметил.
    rm -f "$GATE_KNOWN_HOSTS"
    out=$( GATE_ADDR="$addr" GATE_KEY="$WORK/id" gate_out check )
    case "$out" in
        *'ключ хоста: pinned'*) ok 'первый контакт: ключ хоста записан' ;;
        *) bad 'первый контакт: ключ хоста записан' "$(printf '%s\n' "$out" | tail -3)" ;;
    esac
    out=$( GATE_ADDR="$addr" GATE_KEY="$WORK/id" gate_out check )
    case "$out" in
        *'ключ хоста: verified'*) ok 'второй контакт: ключ хоста сверен с записанным' ;;
        *) bad 'второй контакт: ключ хоста сверен с записанным' "$(printf '%s\n' "$out" | tail -3)" ;;
    esac

    # 8. Отказ несёт ответ системы, а не пустую строку. Проверка появилась после находки
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

    # 9. Шов — живьём, в этом же контейнере. Нужны FUSE и устройство: их даёт образ и его
    #    запуск, и без них это НЕ ПРОГНАНО, а не зелено.
    if have_fuse; then
        local point; point="$(mktemp -d -p "$WORK")"
        out=$( GATE_ADDR="$addr" GATE_KEY="$WORK/id" GATE_REMOTE=/том GATE_MOUNT="$point" gate_out up )
        case "$out" in
            *'шов поднят'*) ok 'шов встал: том ресурса B виден на этой машине' ;;
            *) bad 'шов встал: том ресурса B виден на этой машине' "$(printf '%s' "$out" | tail -4)" ;;
        esac
        out=$( GATE_MOUNT="$point" gate_out status )
        case "$out" in
            *'шов             : поднят'*) ok 'status видит поднятый шов' ;;
            *) bad 'status видит поднятый шов' "$out" ;;
        esac

        # Главная проверка шва: связь, а не копия (`WORLD2` 1.6). Кладём метку ЧЕРЕЗ шов и
        # ищем её ТАМ, ГДЕ ЛЕЖИТ ТОМ. Проверять «каталог непустой» бессмысленно: непустым он
        # бывает и у локальной копии, и проба зеленела бы на оторванном ресурсе.
        local mark="метка-$$" n=0
        if : > "$point/$mark" 2>/dev/null; then
            while [ ! -e "$SSH_DIR/том/$mark" ] && [ "$n" -lt 30 ]; do sleep 0.1; n=$((n + 1)); done
        fi
        if [ -e "$SSH_DIR/том/$mark" ]; then
            ok 'том берётся связью, а не копией: метка через шов легла на сам ресурс'
            rm -f "$point/$mark" 2>/dev/null || true
        else
            bad 'том берётся связью, а не копией' \
                "метка «$mark», положенная через шов, на ресурсе не появилась" \
                'если метка не создалась вовсе — шов смонтирован только на чтение'
        fi

        out=$( GATE_MOUNT="$point" gate_out down )
        case "$out" in
            *'шов снят'*) ok 'шов снимается' ;;
            *) bad 'шов снимается' "$(printf '%s' "$out" | tail -4)" ;;
        esac
    else
        local why=()
        [ -e /dev/fuse ] || why+=('/dev/fuse нет — устройство даёт запуск контейнера: --device /dev/fuse')
        command -v fusermount3 >/dev/null 2>&1 || command -v fusermount >/dev/null 2>&1 \
            || why+=('fusermount3 нет — пакет fuse3 обязан лежать в образе')
        skip 'шов: том ресурса B виден на этой машине' "${why[@]}" \
             'это дефект образа или его запуска, а не задание человеку (TECH 2)'
    fi
}

# ------------------------------------------------------------------ красный прогон
run_red() {
    part 'КРАСНЫЙ — ломаем нарочно, шлюз обязан назвать ступень'

    # Поломки идут по ступеням сверху вниз: настройка → имя → дорога → ответ → доступ → том → шов.
    want_refusal no-address 'адрес не назван' check
    # Юзер назван пустым — прошлая редакция проглатывала это молча и шла текущим
    # пользователем машины, то есть не тем, кого назвали (`WORLD2-96`, п. 4).
    GATE_ADDR="@127.0.0.1" want_refusal no-address 'юзер в адресе пуст — сказано вслух' check

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

    # Инструментов ssh в образе нет. Ломаем честно — прячем их из `$PATH` целиком, а не
    # подменяем значением: шлюз зовёт их по имени, и проверять надо ровно это.
    if command -v sftp >/dev/null 2>&1; then
        local bin="$WORK/bin-без-ssh" d f
        mkdir -p "$bin"
        for d in ${PATH//:/ }; do
            [ -d "$d" ] || continue
            for f in "$d"/*; do
                [ -e "$f" ] || continue
                case "${f##*/}" in sftp|ssh-keyscan) continue ;; esac
                [ -e "$bin/${f##*/}" ] || ln -s "$f" "$bin/${f##*/}" 2>/dev/null || true
            done
        done
        GATE_ADDR="probe@127.0.0.1" PATH="$bin" \
            want_refusal no-ssh-tool 'инструментов ssh в образе нет — назван дефект образа' check
    else
        skip 'инструментов ssh в образе нет' 'sftp здесь и так отсутствует — прятать нечего'
    fi

    # Живой не-ssh на порту: дорога есть, ответ есть, а ключа хоста никто не даёт.
    if start_web_end; then
        GATE_ADDR="probe@127.0.0.1:$WEB_PORT" GATE_TIMEOUT=3 \
            want_refusal access-denied 'доступ не принят — на порту не ssh' check
    else
        skip 'доступ не принят — на порту не ssh' "$(why_no_env)"
    fi

    if start_ssh_end; then
        local addr="probe@127.0.0.1:$SSH_PORT"
        # Чужой ключ. Ступень та же — «доступ», — но причина другая: служба ssh настоящая,
        # а креды не те. Раньше эта поломка не проверялась вовсе: ssh-конца не было.
        GATE_ADDR="$addr" GATE_KEY="$WORK/id-alien" GATE_TIMEOUT=3 \
            want_refusal access-denied 'креды не приняты настоящим ssh — чужой ключ' check

        # Подмена ключа хоста. Ломаем записанный ключ и требуем, чтобы шлюз ЗАМЕТИЛ: без
        # этого доверие при первом контакте раздавалось бы молча и навсегда.
        rm -f "$GATE_KNOWN_HOSTS"
        GATE_ADDR="$addr" GATE_KEY="$WORK/id" gate_out check >/dev/null
        sed -i 's/AAAAC3NzaC1lZDI1NTE5AAAAI/AAAAC3NzaC1lZDI1NTE5AAAAX/' "$GATE_KNOWN_HOSTS" 2>/dev/null || true
        GATE_ADDR="$addr" GATE_KEY="$WORK/id" GATE_TIMEOUT=3 \
            want_refusal host-key-changed 'ключ хоста подменён — шлюз не пошёл' check
        rm -f "$GATE_KNOWN_HOSTS"

        # Том назван, а его на ресурсе нет.
        GATE_ADDR="$addr" GATE_KEY="$WORK/id" GATE_REMOTE=/нет-такого-тома GATE_TIMEOUT=3 \
            want_refusal no-remote-path 'тома по названному пути нет' check
    else
        skip 'поломки на настоящем ssh-конце (чужой ключ · подмена ключа хоста · тома нет)' \
             "$(why_no_env)"
    fi

    GATE_ADDR="probe@127.0.0.1" GATE_REMOTE= \
        want_refusal no-remote 'том не назван — шов поднимать нечем' up
    GATE_ADDR="probe@127.0.0.1" GATE_REMOTE=/srv/scope \
        GATE_RCLONE=/nonexistent/rclone GATE_MOUNT="$(mktemp -d -p "$WORK")" \
        want_refusal no-seam-tool 'инструмента шва нет — назван дефект образа' up
    GATE_MOUNT="$(mktemp -d -p "$WORK")" want_refusal no-mount 'снимать нечего — шва на точке нет' down

    # Шов в чёрную дыру. Проверяется только там, где шов вообще возможен: без FUSE отказ
    # пришёл бы раньше и другой, то есть проверялось бы не то.
    if have_rclone && have_fuse; then
        GATE_ADDR="probe@$BLACKHOLE" GATE_REMOTE=/srv/scope GATE_TIMEOUT=2 \
            GATE_MOUNT="$(mktemp -d -p "$WORK")" \
            want_refusal no-route 'шов в чёрную дыру не встаёт — и это сказано ступенью' up
    else
        skip 'шов в чёрную дыру не встаёт' \
             'FUSE здесь нет — ломать нечего; это дефект образа или запуска, а не машины юзера'
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
