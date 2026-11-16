#!/usr/bin/env bash
# EmTDS watchdog: once a minute starts Docker, nginx and stopped tracker
# containers. It never tears the stack down, never removes volumes, never
# recreates ClickHouse, Redis, php or cron.
# While update.sh holds the update lock, watchdog yields its pass.

set -euo pipefail

SELF=$(readlink -f "${BASH_SOURCE[0]:-}" 2>/dev/null || true)
TDS_DIR=
if [[ -n "$SELF" ]]; then
	TDS_DIR=$(dirname "$(dirname "$SELF")")
fi
STATE_DIR="${EMTDS_INSTALL_STATE_DIR:-${KUZTDS_INSTALL_STATE_DIR:-}}"
if [[ -z "$STATE_DIR" ]]; then
	STATE_DIR=/etc/emtds
	if [[ ! -e /etc/emtds && -d /etc/kuztds ]]; then
		STATE_DIR=/etc/kuztds
	fi
fi
SERVER_ENV="${EMTDS_SERVER_ENV:-${KUZTDS_SERVER_ENV:-$STATE_DIR/server.env}}"
INFRA_OVERRIDE="$STATE_DIR/compose.infra.yml"
APP_OVERRIDE="$STATE_DIR/compose.app.yml"
UNIT_DIR="${EMTDS_WATCHDOG_UNIT_DIR:-${KUZTDS_WATCHDOG_UNIT_DIR:-/etc/systemd/system}}"
LOCK_FILE="${EMTDS_WATCHDOG_LOCK:-${KUZTDS_WATCHDOG_LOCK:-/run/emtds-watchdog.lock}}"
UPDATE_LOCK_FILE="${EMTDS_UPDATE_LOCK:-/run/emtds-update.lock}"
APP_CONTAINERS=(emtds-engine emtds-admin emtds-cron emtds-php)
INFRA_CONTAINERS=(deploy-clickhouse-1 deploy-redis-1)

# Назначение: короткое сообщение сторожа в journald.
# Параметры: текст.
# Результат: строка на stdout.
# Ошибки: нет.
log() {
	printf 'emtds-watchdog: %s\n' "$*"
}

# Назначение: последнее значение KEY= из env-файла.
# Параметры: $1 — путь; $2 — имя ключа.
# Результат: значение на stdout, может быть пустым.
# Ошибки: нет.
env_value() {
	local file=$1 key=$2 line val
	line=$(grep -E "^${key}=" "$file" 2>/dev/null | tail -n 1 || true)
	if [[ -z "$line" ]]; then
		return 0
	fi
	val=${line#*=}
	if [[ "$val" == \"*\" && "$val" == *\" ]]; then
		val=${val:1:${#val}-2}
	elif [[ "$val" == \'*\' && "$val" == *\' ]]; then
		val=${val:1:${#val}-2}
	fi
	printf '%s' "$val"
}

# Назначение: значение EMTDS_<имя> из env-файла, при пустом — старое KUZTDS_<имя>.
# Параметры: $1 — путь; $2 — имя без префикса.
# Результат: значение на stdout, может быть пустым.
# Ошибки: нет.
env_value_compat() {
	local file=$1 name=$2 val
	val=$(env_value "$file" "EMTDS_${name}")
	if [[ -z "$val" ]]; then
		val=$(env_value "$file" "KUZTDS_${name}")
	fi
	printf '%s' "$val"
}

# Назначение: хост:порт публикации из host:hostport:containerport.
# Параметры: $1 — значение EMTDS_*_PUBLISH; $2 — запасной хост:порт.
# Результат: host:port на stdout.
# Ошибки: нет.
publish_listen() {
	local publish=${1:-} fallback=${2:-127.0.0.1:18080} host port rest
	if [[ -z "$publish" ]]; then
		printf '%s' "$fallback"
		return 0
	fi
	IFS=: read -r host port rest <<<"$publish"
	if [[ -n "$host" && -n "$port" && -n "$rest" ]]; then
		printf '%s:%s' "$host" "$port"
		return 0
	fi
	printf '%s' "$fallback"
}

# Назначение: HTTP GET без тела, короткий таймаут.
# Параметры: $1 — URL.
# Результат: код 0 при HTTP 2xx.
# Ошибки: сеть/таймаут, код 1.
http_ok() {
	local url=$1
	curl -fsS --max-time 4 "$url" >/dev/null 2>&1
}

# Назначение: HTTPS GET к локальному nginx, самоподписанный сертификат допустим.
# Параметры: $1 — URL.
# Результат: код 0 при HTTP 2xx.
# Ошибки: сеть/таймаут, код 1.
https_ok() {
	local url=$1
	curl -kfsS --max-time 4 "$url" >/dev/null 2>&1
}

# Назначение: юнит systemd активен.
# Параметры: $1 — имя юнита.
# Результат: код 0 если active.
# Ошибки: нет (systemctl отсутствует — код 1).
unit_active() {
	local name=$1
	command -v systemctl >/dev/null 2>&1 || return 1
	systemctl is-active --quiet "$name" 2>/dev/null
}

# Назначение: включить и запустить юнит, если он есть и не active.
# Параметры: $1 — имя юнита.
# Результат: код 0 если active или юнита нет.
# Ошибки: start не удался, код 1.
ensure_unit() {
	local name=$1
	command -v systemctl >/dev/null 2>&1 || return 0
	if ! systemctl list-unit-files "$name" --no-legend 2>/dev/null | grep -q .; then
		return 0
	fi
	systemctl enable "$name" >/dev/null 2>&1 || true
	if unit_active "$name"; then
		return 0
	fi
	log "start ${name}"
	systemctl start "$name"
}

# Назначение: состояние контейнера: running, exited или missing.
# Параметры: $1 — имя.
# Результат: слово на stdout.
# Ошибки: нет.
container_state() {
	local name=$1 running
	if ! docker container inspect "$name" >/dev/null 2>&1; then
		printf 'missing\n'
		return 0
	fi
	running=$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null || echo false)
	if [[ "$running" == true ]]; then
		printf 'running\n'
		return 0
	fi
	printf 'exited\n'
}

# Назначение: поднять уже существующий остановленный контейнер.
# Параметры: $1 — имя.
# Результат: код 0 если running.
# Ошибки: start не удался или контейнера нет, код 1.
start_existing() {
	local name=$1 state
	state=$(container_state "$name")
	if [[ "$state" == running ]]; then
		return 0
	fi
	if [[ "$state" != exited ]]; then
		return 1
	fi
	log "docker start ${name}"
	timeout 25 docker start "$name" >/dev/null
	[[ "$(container_state "$name")" == running ]]
}

# Назначение: compose инфраструктуры без остановки стека и без пересоздания.
# Параметры: аргументы compose.
# Результат: код возврата compose.
# Ошибки: нет файлов — код 1.
infra_compose() {
	local envf=("$TDS_DIR/.env")
	[[ -f "$SERVER_ENV" ]] && envf+=("$SERVER_ENV")
	[[ -f "$TDS_DIR/.env" ]] || return 1
	[[ -f "$TDS_DIR/deploy/docker-compose.yml" ]] || return 1
	local args=(docker compose -p deploy)
	local f
	for f in "${envf[@]}"; do
		args+=(--env-file "$f")
	done
	args+=(-f "$TDS_DIR/deploy/docker-compose.yml")
	if [[ -f "$INFRA_OVERRIDE" ]]; then
		args+=(-f "$INFRA_OVERRIDE")
	fi
	timeout 60 "${args[@]}" "$@"
}

# Назначение: compose приложения без остановки стека и без пересоздания.
# Параметры: аргументы compose.
# Результат: код возврата compose.
# Ошибки: нет файлов — код 1.
app_compose() {
	local envf=("$TDS_DIR/.env")
	[[ -f "$SERVER_ENV" ]] && envf+=("$SERVER_ENV")
	[[ -f "$TDS_DIR/.env" ]] || return 1
	[[ -f "$TDS_DIR/deploy/docker-compose.app.yml" ]] || return 1
	local args=(docker compose)
	local f
	for f in "${envf[@]}"; do
		args+=(--env-file "$f")
	done
	args+=(-f "$TDS_DIR/deploy/docker-compose.app.yml")
	if [[ -f "$APP_OVERRIDE" ]]; then
		args+=(-f "$APP_OVERRIDE")
	fi
	timeout 90 "${args[@]}" "$@"
}

# Назначение: Docker слушает, сеть deploy_default есть.
# Параметры: нет.
# Результат: код 0.
# Ошибки: docker не поднялся, код 1.
ensure_docker() {
	local n
	ensure_unit docker.service
	for n in 1 2 3 4 5 6 7 8 9 10; do
		if docker info >/dev/null 2>&1; then
			break
		fi
		sleep 2
	done
	docker info >/dev/null 2>&1 || { log "docker не отвечает"; return 1; }
	if docker network inspect deploy_default >/dev/null 2>&1; then
		return 0
	fi
	log "сеть deploy_default отсутствует, infra up -d"
	infra_compose up -d
}

# Назначение: ClickHouse и Redis running; не recreate.
# Параметры: нет.
# Результат: код 0 если оба running.
# Ошибки: не поднялись, код 1.
ensure_infra() {
	local name need_up=0
	for name in "${INFRA_CONTAINERS[@]}"; do
		case "$(container_state "$name")" in
			running) ;;
			exited)
				start_existing "$name" || need_up=1
				;;
			*)
				need_up=1
				;;
		esac
	done
	if (( need_up == 1 )); then
		log "infra up -d"
		infra_compose up -d || true
	fi
	for name in "${INFRA_CONTAINERS[@]}"; do
		if [[ "$(container_state "$name")" != running ]]; then
			log "${name} не running"
			return 1
		fi
	done
	return 0
}

# Назначение: контейнер приложения running; php/cron только start, без recreate.
# Параметры: $1 — имя; $2 — сервис compose (engine|admin|php|cron).
# Результат: код 0 если running.
# Ошибки: не поднялся, код 1.
ensure_app_container() {
	local name=$1 service=$2
	if [[ "$(container_state "$name")" == running ]]; then
		return 0
	fi
	if start_existing "$name"; then
		return 0
	fi
	if [[ "$service" == php || "$service" == cron ]]; then
		log "${name} нет, php/cron не создаём"
		return 1
	fi
	if [[ "$(container_state "kuztds-${service}")" != missing ]]; then
		log "${name} нет, есть старый kuztds-${service}: сначала deploy/migrate-emtds.sh, новый контейнер не создаём"
		return 1
	fi
	log "app up -d --no-deps ${service}"
	app_compose up -d --no-deps "$service"
	[[ "$(container_state "$name")" == running ]]
}

# Назначение: секунды с момента старта контейнера.
# Параметры: $1 — имя.
# Результат: целое на stdout, 9999 если неизвестно.
# Ошибки: нет.
started_ago() {
	local name=$1 started now
	started=$(docker inspect -f '{{.State.StartedAt}}' "$name" 2>/dev/null || true)
	if [[ -z "$started" ]]; then
		printf '9999'
		return 0
	fi
	now=$(date -u +%s)
	started=$(date -u -d "$started" +%s 2>/dev/null || printf '%s' "$now")
	printf '%s' $((now - started))
}

# Назначение: healthz движка; restart только если процесс жив, но проба молчит дольше start_period.
# Параметры: $1 — URL healthz.
# Результат: код 0 если проба проходит.
# Ошибки: код 1.
ensure_engine_health() {
	local url=$1 ago
	ensure_app_container emtds-engine engine || return 1
	if http_ok "$url"; then
		return 0
	fi
	ago=$(started_ago emtds-engine)
	if (( ago < 40 )); then
		log "движок стартовал ${ago}s назад, ждём healthz"
		sleep 5
		http_ok "$url" && return 0
		return 1
	fi
	log "restart emtds-engine (healthz)"
	timeout 30 docker restart emtds-engine >/dev/null
	sleep 8
	http_ok "$url"
}

# Назначение: поднять admin, если контейнер стоит. Running admin не restart — перенос Keitaro в памяти.
# Параметры: $1 — URL /api/health.
# Результат: код 0 если проба проходит или контейнер running.
# Ошибки: контейнер так и не running, код 1.
ensure_admin() {
	local url=$1
	if [[ "$(container_state emtds-admin)" == running ]]; then
		http_ok "$url" && return 0
		log "admin running, health молчит — не restart (keitaro job)"
		return 0
	fi
	ensure_app_container emtds-admin admin || return 1
	http_ok "$url" && return 0
	log "admin поднят, health ещё молчит"
	return 0
}

# Назначение: один проход проверки и подъёма.
# Параметры: нет.
# Результат: код 0 если движок отвечает.
# Ошибки: трекер недоступен, код 1.
run_check() {
	local engine_url admin_url ch_url rc=0
	if [[ -z "$TDS_DIR" || ! -d "$TDS_DIR/deploy" ]]; then
		log "не найден каталог tds"
		return 1
	fi
	ensure_docker || return 1
	ensure_unit nginx.service || true
	ensure_infra || rc=1
	ensure_app_container emtds-php php || true
	ensure_app_container emtds-cron cron || true
	engine_url="http://$(publish_listen "$(env_value_compat "$SERVER_ENV" ENGINE_PUBLISH)" "127.0.0.1:18080")/healthz"
	admin_url="http://$(publish_listen "$(env_value_compat "$SERVER_ENV" ADMIN_PUBLISH)" "127.0.0.1:8090")/api/health"
	ch_url="http://$(publish_listen "$(env_value_compat "$SERVER_ENV" CH_HTTP_PUBLISH)" "127.0.0.1:8123")/ping"
	ensure_engine_health "$engine_url" || rc=1
	ensure_admin "$admin_url" || true
	if ! http_ok "$ch_url"; then
		log "ClickHouse /ping не отвечает"
		rc=1
	fi
	if ! https_ok "https://127.0.0.1/healthz"; then
		log "https://127.0.0.1/healthz не отвечает"
		rc=1
	fi
	return "$rc"
}

# Назначение: systemd unit и timer раз в минуту.
# Параметры: нет.
# Результат: timer enabled.
# Ошибки: запись юнитов, код 1.
install_units() {
	local service="$UNIT_DIR/emtds-watchdog.service"
	local timer="$UNIT_DIR/emtds-watchdog.timer"
	if [[ -z "$SELF" || ! -f "$SELF" ]]; then
		log "нет пути к watchdog.sh"
		return 1
	fi
	install -d -m 0755 "$UNIT_DIR"
	cat >"$service" <<EOF
[Unit]
Description=EmTDS watchdog
After=docker.service network-online.target
Wants=docker.service

[Service]
Type=oneshot
ExecStart=${SELF}
Nice=5
TimeoutStartSec=50
EOF
	cat >"$timer" <<EOF
[Unit]
Description=EmTDS watchdog every minute

[Timer]
OnBootSec=20s
OnUnitActiveSec=60s
AccuracySec=5s
Persistent=true
Unit=emtds-watchdog.service

[Install]
WantedBy=timers.target
EOF
	chmod 0644 "$service" "$timer"
	if [[ "${EMTDS_WATCHDOG_SKIP_SYSTEMCTL:-${KUZTDS_WATCHDOG_SKIP_SYSTEMCTL:-}}" == 1 ]]; then
		log "юниты записаны в ${UNIT_DIR}"
		return 0
	fi
	systemctl daemon-reload
	systemctl enable --now docker.service >/dev/null 2>&1 || true
	if systemctl list-unit-files nginx.service --no-legend 2>/dev/null | grep -q .; then
		systemctl enable --now nginx.service >/dev/null 2>&1 || true
	fi
	systemctl enable --now emtds-watchdog.timer
	log "timer emtds-watchdog включён"
}

# Назначение: проверки скрипта без Docker и без systemd.
# Параметры: нет.
# Результат: self-test ok.
# Ошибки: код 1.
run_self_test() {
	local tmp pub
	tmp=$(mktemp -d)
	# shellcheck disable=SC2064
	trap "rm -rf '$tmp'" EXIT
	bash -n "$SELF"
	pub=$(publish_listen "127.0.0.1:18080:8080" "127.0.0.1:9")
	[[ "$pub" == 127.0.0.1:18080 ]] || { printf 'self-test FAIL: publish_listen engine\n'; exit 1; }
	pub=$(publish_listen "" "127.0.0.1:8090")
	[[ "$pub" == 127.0.0.1:8090 ]] || { printf 'self-test FAIL: publish_listen empty\n'; exit 1; }
	EMTDS_WATCHDOG_UNIT_DIR="$tmp" EMTDS_WATCHDOG_SKIP_SYSTEMCTL=1 "$SELF" --install
	[[ -f "$tmp/emtds-watchdog.service" ]] || { printf 'self-test FAIL: service\n'; exit 1; }
	[[ -f "$tmp/emtds-watchdog.timer" ]] || { printf 'self-test FAIL: timer\n'; exit 1; }
	grep -q 'OnUnitActiveSec=60s' "$tmp/emtds-watchdog.timer" || { printf 'self-test FAIL: interval\n'; exit 1; }
	grep -q "ExecStart=${SELF}" "$tmp/emtds-watchdog.service" || { printf 'self-test FAIL: ExecStart\n'; exit 1; }
	printf 'self-test ok\n'
}

# Назначение: точка входа.
# Параметры: --self-test | --install | пусто (проверка).
# Результат: код сценария.
# Ошибки: неизвестный аргумент, код 2.
main() {
	case "${1:-}" in
		--self-test)
			run_self_test
			;;
		--install)
			install_units
			;;
		'')
			if ! flock -n "$UPDATE_LOCK_FILE" true 2>/dev/null; then
				log "прогон update.sh активен, пропуск прохода"
				exit 0
			fi
			if [[ -e "$LOCK_FILE" || -d "$(dirname "$LOCK_FILE")" ]]; then
				exec 9>"$LOCK_FILE"
				if ! flock -n 9; then
					log "уже выполняется"
					exit 0
				fi
			fi
			run_check
			;;
		*)
			log "использование: watchdog.sh | watchdog.sh --install | watchdog.sh --self-test" >&2
			exit 2
			;;
	esac
}

main "$@"
