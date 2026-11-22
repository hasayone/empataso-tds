#!/bin/sh
# Точка входа локального образа: запускает engine, admin или cron.
# Engine и admin после нескольких подряд провалов своего health завершаются,
# чтобы политика restart подняла контейнер. Cron этот цикл не использует.

set -eu

# Сколько подряд неудач своего health завершают процесс.
# Один провал не считается поводом. Интервал и таймаут те же, что у healthcheck в compose.
HEALTH_FAILS=3
HEALTH_INTERVAL=30
HEALTH_TIMEOUT=3

# Назначение: создать каталоги состояния, если том примонтирован без них.
# Параметры: нет.
# Результат: существуют data, keys и geo; код 0.
# Ошибки: нет прав на запись — mkdir завершает процесс из-за set -e.
ensure_state_dirs() {
    mkdir -p /var/lib/emtds/data /var/lib/emtds/keys /var/lib/emtds/geo /var/lib/emtds/landings
}

# Назначение: заполнить EMTDS_ADMIN_PASSWORD_HASH argon2id-хэшем пароля админки.
# Параметры: нет. Читает EMTDS_ADMIN_PASSWORD и EMTDS_ADMIN_PASSWORD_HASH.
# Результат: хэш экспортирован и пароль убран из окружения, код 0.
# Пустые пароль и хэш тоже код 0: первый вход задаёт их формой, процесс стартует без хэша.
# Ошибки: emtds-admin -hash завершился с ошибкой или вернул пустой вывод — выход 1.
# Пароль и хэш в сообщения не попадают.
prepare_admin_hash() {
    EMTDS_ADMIN_PASSWORD="${EMTDS_ADMIN_PASSWORD:-}"
    EMTDS_ADMIN_PASSWORD_HASH="${EMTDS_ADMIN_PASSWORD_HASH:-}"
    if [ -n "$EMTDS_ADMIN_PASSWORD_HASH" ]; then
        export EMTDS_ADMIN_PASSWORD_HASH
        unset EMTDS_ADMIN_PASSWORD
        return 0
    fi
    if [ -z "${EMTDS_ADMIN_PASSWORD:-}" ]; then
        return 0
    fi
    hash=$(/usr/local/bin/emtds-admin -hash "$EMTDS_ADMIN_PASSWORD") || {
        echo "emtds-entrypoint: не удалось посчитать хэш пароля админки" >&2
        exit 1
    }
    hash=$(printf '%s' "$hash" | tr -d '\r\n')
    if [ -z "$hash" ]; then
        echo "emtds-entrypoint: хэш пароля админки пуст" >&2
        exit 1
    fi
    export EMTDS_ADMIN_PASSWORD_HASH="$hash"
    unset EMTDS_ADMIN_PASSWORD
}

# Назначение: накопить число подряд идущих провалов health.
# Параметры: $1 — сколько провалов уже было, $2 — 0 если проба удалась, иначе нет.
# Результат: печатает новое число. Удачная проба даёт 0.
# Ошибки: нет.
health_fail_count() {
    if [ "$2" -eq 0 ]; then
        printf '0\n'
        return 0
    fi
    printf '%s\n' "$(($1 + 1))"
}

# Назначение: решить, завершать ли процесс после пробы.
# Параметры: $1 — подряд провалов, $2 — порог.
# Результат: код 0, если провалов не меньше порога.
# Ошибки: нет.
health_should_stop() {
    [ "$1" -ge "$2" ]
}

# Назначение: адрес своей пробы health внутри контейнера.
# Параметры: $1 — роль engine или admin.
# Результат: печатает URL. У cron своего health нет, код 1.
# Ошибки: неизвестная роль — код 1.
health_probe_url() {
    case "$1" in
        engine) printf 'http://127.0.0.1:8080/healthz\n' ;;
        admin) printf 'http://127.0.0.1:8090/api/health\n' ;;
        *) return 1 ;;
    esac
}

# Назначение: секунды старта, в которые провал health процесс не завершает.
# Параметры: $1 — роль engine или admin.
# Результат: печатает число. У cron этого окна нет, код 1.
# Ошибки: неизвестная роль — код 1.
health_start_period() {
    case "$1" in
        engine) printf '40\n' ;;
        admin) printf '60\n' ;;
        *) return 1 ;;
    esac
}

# Назначение: отличить живой дочерний процесс от уже завершившегося.
# Параметры: $1 — pid.
# Результат: код 0, если процесс ещё исполняется. Код 1, если его нет или он зомби.
# Ошибки: нет каталога /proc — код 1, вызывающий забирает статус через wait.
child_alive() {
    status="/proc/$1/status"
    if [ ! -r "$status" ]; then
        return 1
    fi
    state=$(sed -n 's/^State:[[:space:]]*\([A-Za-z]\).*/\1/p' "$status" 2>/dev/null || true)
    if [ -z "$state" ] || [ "$state" = "Z" ]; then
        return 1
    fi
    return 0
}

# Назначение: передать остановку контейнера дочернему процессу.
# Параметры: нет. Использует pid в child.
# Результат: ребёнок получает SIGTERM, точка входа завершается с кодом 0.
# Ошибки: процесса уже нет — код 0. Это не провал health: docker stop не должен поднимать контейнер заново.
on_stop() {
    trap - TERM INT
    if [ -n "${child:-}" ]; then
        kill -TERM "$child" 2>/dev/null || true
        wait "$child" 2>/dev/null || true
    fi
    exit 0
}

# Назначение: держать процесс и завершить его, когда свой health не отвечает несколько раз подряд.
# Параметры: $1 — URL пробы, $2 — секунды окна старта, далее команда процесса.
# Результат: не возвращается. Код 0 — сигнал остановки, код ребёнка — он сам вышел, код 1 — порог провалов.
# Ошибки: пустой URL — код 1. Один провал и промахи в окне старта процесс не убивают.
supervise_health() {
    url=$1
    start_period=$2
    shift 2
    if [ -z "$url" ] || [ -z "$start_period" ]; then
        echo "emtds-entrypoint: нет адреса health" >&2
        exit 1
    fi
    "$@" &
    child=$!
    trap on_stop TERM INT
    fails=0
    elapsed=0
    next=$start_period
    while child_alive "$child"; do
        sleep 1
        if ! child_alive "$child"; then
            break
        fi
        elapsed=$((elapsed + 1))
        if [ "$elapsed" -lt "$next" ]; then
            continue
        fi
        if curl -fsS --max-time "$HEALTH_TIMEOUT" "$url" >/dev/null 2>&1; then
            fails=$(health_fail_count "$fails" 0)
        else
            fails=$(health_fail_count "$fails" 1)
        fi
        next=$((elapsed + HEALTH_INTERVAL))
        if health_should_stop "$fails" "$HEALTH_FAILS"; then
            trap - TERM INT
            kill -TERM "$child" 2>/dev/null || true
            i=0
            while [ "$i" -lt 15 ] && child_alive "$child"; do
                sleep 1
                i=$((i + 1))
            done
            if child_alive "$child"; then
                kill -KILL "$child" 2>/dev/null || true
            fi
            wait "$child" 2>/dev/null || true
            exit 1
        fi
    done
    wait "$child" || exit $?
    exit 0
}

# Назначение: заменить текущий процесс бинарником engine, admin или cron.
# Параметры: $1 — роль, строка engine, admin или cron.
# Результат: engine и admin уходят в supervise_health, cron заменяется через exec.
# Ошибки: пустая или неизвестная роль — выход 1; для admin — ошибки prepare_admin_hash.
run_role() {
    role="$1"
    ensure_state_dirs
    case "$role" in
        engine)
            supervise_health "$(health_probe_url engine)" "$(health_start_period engine)" /usr/local/bin/emtds-engine
            ;;
        admin)
            prepare_admin_hash
            supervise_health "$(health_probe_url admin)" "$(health_start_period admin)" /usr/local/bin/emtds-admin
            ;;
        cron)
            exec /usr/local/bin/emtds-cron
            ;;
        *)
            echo "emtds-entrypoint: роль должна быть engine, admin или cron" >&2
            exit 1
            ;;
    esac
}

# Назначение: проверить счётчик провалов health без запуска процессов и без Docker.
# Параметры: нет.
# Результат: печатает self-test ok, код 0.
# Ошибки: несовпадение — self-test FAIL и код 1.
self_test() {
    fail() {
        printf 'self-test FAIL: %s\n' "$1" >&2
        exit 1
    }
    n=$(health_fail_count 0 1)
    [ "$n" -eq 1 ] || fail "один провал"
    health_should_stop "$n" "$HEALTH_FAILS" && fail "один провал завершает"
    n=$(health_fail_count 1 1)
    [ "$n" -eq 2 ] || fail "два провала"
    health_should_stop "$n" "$HEALTH_FAILS" && fail "два провала завершают"
    n=$(health_fail_count 2 1)
    [ "$n" -eq 3 ] || fail "три провала"
    health_should_stop "$n" "$HEALTH_FAILS" || fail "три провала не завершают"
    n=$(health_fail_count 2 0)
    [ "$n" -eq 0 ] || fail "успех не сбрасывает"
    health_should_stop "$n" "$HEALTH_FAILS" && fail "успех завершает"
    if health_probe_url cron >/dev/null 2>&1; then
        fail "у cron есть проба"
    fi
    if health_start_period cron >/dev/null 2>&1; then
        fail "у cron есть окно старта"
    fi
    engine_url=$(health_probe_url engine) || fail "url engine"
    [ "$engine_url" = "http://127.0.0.1:8080/healthz" ] || fail "url engine"
    admin_url=$(health_probe_url admin) || fail "url admin"
    [ "$admin_url" = "http://127.0.0.1:8090/api/health" ] || fail "url admin"
    engine_start=$(health_start_period engine) || fail "старт engine"
    [ "$engine_start" -ge 30 ] || fail "старт engine короткий"
    admin_start=$(health_start_period admin) || fail "старт admin"
    [ "$admin_start" -ge 30 ] || fail "старт admin короткий"
    printf 'self-test ok\n'
}

if [ "${1:-}" = "--self-test" ]; then
    self_test
    exit 0
fi

run_role "${1:-}"
