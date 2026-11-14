#!/bin/sh
# Точка входа локального образа: запускает engine, admin или cron.

set -eu

# Назначение: создать каталоги состояния, если том примонтирован без них.
# Параметры: нет.
# Результат: существуют data, keys и geo; код 0.
# Ошибки: нет прав на запись — mkdir завершает процесс из-за set -e.
ensure_state_dirs() {
    mkdir -p /var/lib/kuztds/data /var/lib/kuztds/keys /var/lib/kuztds/geo
}

# Назначение: заполнить KUZTDS_ADMIN_PASSWORD_HASH argon2id-хэшем пароля админки.
# Параметры: нет. Читает KUZTDS_ADMIN_PASSWORD и KUZTDS_ADMIN_PASSWORD_HASH.
# Результат: хэш экспортирован и пароль убран из окружения, код 0.
# Пустые пароль и хэш тоже код 0: первый вход задаёт их формой, процесс стартует без хэша.
# Ошибки: kuztds-admin -hash завершился с ошибкой или вернул пустой вывод — выход 1.
# Пароль и хэш в сообщения не попадают.
prepare_admin_hash() {
    if [ -n "${KUZTDS_ADMIN_PASSWORD_HASH:-}" ]; then
        unset KUZTDS_ADMIN_PASSWORD
        return 0
    fi
    if [ -z "${KUZTDS_ADMIN_PASSWORD:-}" ]; then
        return 0
    fi
    hash=$(/usr/local/bin/kuztds-admin -hash "$KUZTDS_ADMIN_PASSWORD") || {
        echo "kuztds-entrypoint: не удалось посчитать хэш пароля админки" >&2
        exit 1
    }
    hash=$(printf '%s' "$hash" | tr -d '\r\n')
    if [ -z "$hash" ]; then
        echo "kuztds-entrypoint: хэш пароля админки пуст" >&2
        exit 1
    fi
    export KUZTDS_ADMIN_PASSWORD_HASH="$hash"
    unset KUZTDS_ADMIN_PASSWORD
}

# Назначение: заменить текущий процесс бинарником engine или admin.
# Параметры: $1 — роль, строка engine, admin или cron.
# Результат: процесс заменён через exec; при успехе функция не возвращается.
# Ошибки: пустая или неизвестная роль — выход 1; для admin — ошибки prepare_admin_hash.
run_role() {
    role="$1"
    ensure_state_dirs
    case "$role" in
        engine)
            exec /usr/local/bin/kuztds-engine
            ;;
        admin)
            prepare_admin_hash
            exec /usr/local/bin/kuztds-admin
            ;;
        cron)
            exec /usr/local/bin/kuztds-cron
            ;;
        *)
            echo "kuztds-entrypoint: роль должна быть engine, admin или cron" >&2
            exit 1
            ;;
    esac
}

run_role "${1:-}"
