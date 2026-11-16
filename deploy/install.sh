#!/usr/bin/env bash

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
MARKER="$STATE_DIR/installed"
SERVER_ENV="$STATE_DIR/server.env"
INFRA_OVERRIDE="$STATE_DIR/compose.infra.yml"
APP_OVERRIDE="$STATE_DIR/compose.app.yml"
UP_SCRIPT="$STATE_DIR/up.sh"
OUR_DEPLOY_DIR="${TDS_DIR}/deploy"
INSTALL_CHANNEL="${EMTDS_INSTALL_CHANNEL:-release}"
INSTALL_REPO="${EMTDS_INSTALL_REPO:-}"
export EMTDS_SERVER_ENV="$SERVER_ENV"
if [[ -f "${OUR_DEPLOY_DIR}/apply-edge.sh" ]]; then
	# shellcheck source=apply-edge.sh
	source "${OUR_DEPLOY_DIR}/apply-edge.sh"
fi
APP_CONTAINERS=(emtds-engine emtds-admin emtds-cron emtds-php)
LEGACY_APP_CONTAINERS=(kuztds-engine kuztds-admin kuztds-cron kuztds-php)
INFRA_COMPOSE=(docker compose -p deploy --env-file "$TDS_DIR/.env" --env-file "$SERVER_ENV" -f "$TDS_DIR/deploy/docker-compose.yml" -f "$INFRA_OVERRIDE")
APP_COMPOSE=(docker compose --env-file "$TDS_DIR/.env" --env-file "$SERVER_ENV" -f "$TDS_DIR/deploy/docker-compose.app.yml" -f "$APP_OVERRIDE")

# Назначение: вывести сообщение пользователю с префиксом установщика.
# Параметры: текст сообщения.
# Результат: строка на stdout.
# Ошибки: нет.
msg() {
	printf 'emtds-install: %s\n' "$*"
}

# Назначение: классифицировать сервер по RAM, CPU и свободному месту.
# Параметры: $1 — RAM MiB; $2 — число CPU; $3 — свободно GiB.
# Результат: refuse, warn или ok на stdout.
# Ошибки: нет.
size_tier() {
	local mem=$1 cpus=$2 free_gib=$3
	if (( mem < 7000 || cpus < 2 || free_gib < 40 )); then
		printf 'refuse\n'
		return 0
	fi
	if (( mem < 15000 || cpus < 4 || free_gib < 80 )); then
		printf 'warn\n'
		return 0
	fi
	printf 'ok\n'
}

# Назначение: лимит памяти ClickHouse по объёму RAM сервера.
# Параметры: $1 — RAM MiB.
# Результат: строка лимита (3g, 6g или 8g) на stdout.
# Ошибки: нет.
ch_mem_limit() {
	local mem=$1
	if (( mem < 12000 )); then
		printf '3g\n'
	elif (( mem < 28000 )); then
		printf '6g\n'
	else
		printf '8g\n'
	fi
}

# Назначение: maxmemory Redis по объёму RAM сервера. Окно уникальности в Redis
# не длиннее суток (EMTDS_UNIQ_MAX_SECONDS), около 120 байт на клик: 2gb держат
# около 17 млн кликов в сутки (цель — 14,4 млн при 167 кликах в секунду), 1gb —
# только около 9 млн, 512mb — около 4 млн, поэтому от 12000 MiB берётся 2gb.
# Параметры: $1 — RAM MiB.
# Результат: 512mb или 2gb на stdout.
# Ошибки: нет.
redis_maxmemory() {
	local mem=$1
	if (( mem < 12000 )); then
		printf '512mb\n'
	else
		printf '2gb\n'
	fi
}

# Назначение: лимит памяти контейнера Redis, в полтора раза выше maxmemory.
# Параметры: $1 — RAM MiB.
# Результат: 768m или 3g на stdout.
# Ошибки: нет.
redis_mem_limit() {
	local mem=$1
	if (( mem < 12000 )); then
		printf '768m\n'
	else
		printf '3g\n'
	fi
}

# Назначение: лимит памяти engine и admin.
# Параметры: $1 — RAM MiB.
# Результат: 1g или 2g на stdout.
# Ошибки: нет.
app_mem_limit() {
	local mem=$1
	if (( mem < 15000 )); then
		printf '1g\n'
	else
		printf '2g\n'
	fi
}

# Назначение: мягкий лимит кучи Go (GOMEMLIMIT) для engine и admin, около 90% лимита контейнера.
# Параметры: $1 — лимит контейнера из app_mem_limit (1g или 2g).
# Результат: 920MiB или 1840MiB на stdout.
# Ошибки: другой лимит — код 1 без вывода.
app_gomemlimit() {
	case $1 in
	1g) printf '920MiB\n' ;;
	2g) printf '1840MiB\n' ;;
	*) return 1 ;;
	esac
}

# Назначение: определить публичный IPv4 для server.env и TLS.
# Параметры: нет (EMTDS_PUBLIC_IP или HTTP-запрос через edge_public_ipv4).
# Результат: адрес на stdout.
# Ошибки: не удалось определить, код 1. Сообщение на stderr, адрес в него не попадает.
detect_public_ipv4() {
	local ip
	if ! declare -F edge_public_ipv4 >/dev/null; then
		msg "задайте EMTDS_PUBLIC_IP (IPv4) в окружении перед установкой" >&2
		return 1
	fi
	if ! ip=$(edge_public_ipv4); then
		msg "задайте EMTDS_PUBLIC_IP (IPv4) в окружении перед установкой" >&2
		return 1
	fi
	printf '%s' "$ip"
}

# Назначение: сгенерировать server.env с привязкой портов к loopback.
# Параметры: $1 — публичный IPv4.
# Результат: содержимое файла на stdout.
# Ошибки: нет.
render_server_env() {
	local public_ip=$1
	cat <<EOF
EMTDS_CH_HTTP_PUBLISH=127.0.0.1:8123:8123
EMTDS_CH_NATIVE_PUBLISH=127.0.0.1:9000:9000
EMTDS_REDIS_PUBLISH=127.0.0.1:6379:6379
EMTDS_ADMIN_PUBLISH=127.0.0.1:8090:8090
EMTDS_ENGINE_PUBLISH=127.0.0.1:18080:8080
EMTDS_PUBLIC_IP=${public_ip}
EOF
}

# Назначение: YAML-override для инфраструктуры (лимиты и логи).
# Параметры: $1 — RAM MiB сервера.
# Результат: compose-override на stdout.
# Ошибки: нет.
render_infra_override() {
	local mem=$1
	local ch_lim redis_lim rmax
	ch_lim=$(ch_mem_limit "$mem")
	redis_lim=$(redis_mem_limit "$mem")
	rmax=$(redis_maxmemory "$mem")
	cat <<EOF
services:
  clickhouse:
    mem_limit: ${ch_lim}
    logging:
      driver: json-file
      options:
        max-size: "20m"
        max-file: "3"
  redis:
    mem_limit: ${redis_lim}
    logging:
      driver: json-file
      options:
        max-size: "20m"
        max-file: "3"
    command: ["redis-server", "--save", "", "--appendonly", "no", "--maxmemory", "${rmax}", "--maxmemory-policy", "noeviction"]
EOF
}

# Назначение: YAML-override для приложения (лимиты памяти, GOMEMLIMIT и логи).
# Параметры: $1 — RAM MiB сервера.
# Результат: compose-override на stdout; GOMEMLIMIT переопределяется EMTDS_GOMEMLIMIT из env-файлов compose.
# Ошибки: нет.
render_app_override() {
	local mem=$1
	local app_lim go_lim
	app_lim=$(app_mem_limit "$mem")
	go_lim=$(app_gomemlimit "$app_lim")
	cat <<EOF
services:
  engine:
    mem_limit: ${app_lim}
    environment:
      GOMEMLIMIT: \${EMTDS_GOMEMLIMIT:-${go_lim}}
    logging:
      driver: json-file
      options:
        max-size: "20m"
        max-file: "3"
  admin:
    mem_limit: ${app_lim}
    environment:
      GOMEMLIMIT: \${EMTDS_GOMEMLIMIT:-${go_lim}}
    logging:
      driver: json-file
      options:
        max-size: "20m"
        max-file: "3"
  php:
    mem_limit: 512m
    logging:
      driver: json-file
      options:
        max-size: "20m"
        max-file: "3"
  cron:
    mem_limit: 512m
    logging:
      driver: json-file
      options:
        max-size: "20m"
        max-file: "3"
EOF
}

# Назначение: сгенерировать скрипт повторного старта стека.
# Параметры: нет (использует INFRA_COMPOSE, APP_COMPOSE, TDS_DIR).
# Результат: bash-скрипт на stdout.
# Ошибки: нет.
render_up_sh() {
	{
		printf '#!/usr/bin/env bash\n'
		printf 'set -euo pipefail\n'
		printf 'cd %q\n' "$TDS_DIR"
		printf '%q ' "${INFRA_COMPOSE[@]}"
		printf 'up -d\n'
		printf '%q ' "${APP_COMPOSE[@]}"
		printf 'up -d --wait --wait-timeout 300\n'
	}
}

# Назначение: вызвать docker compose для инфраструктуры с override.
# Параметры: аргументы compose (up, exec, ps, …).
# Результат: код возврата docker compose.
# Ошибки: compose завершился с ошибкой.
infra_compose() {
	"${INFRA_COMPOSE[@]}" "$@"
}

# Назначение: вызвать docker compose для приложения с override.
# Параметры: аргументы compose.
# Результат: код возврата docker compose.
# Ошибки: compose завершился с ошибкой.
app_compose() {
	"${APP_COMPOSE[@]}" "$@"
}

# Назначение: прочитать метку Docker-контейнера по имени или ID.
# Параметры: $1 — имя или ID; $2 — ключ метки.
# Результат: значение метки на stdout или пустая строка.
# Ошибки: контейнер не найден — пустой stdout.
container_label() {
	docker inspect -f "{{ index .Config.Labels \"${2}\"}}" "$1" 2>/dev/null || true
}

# Назначение: прочитать значение переменной из env-файла (последнее вхождение).
# Параметры: $1 — путь к файлу; $2 — имя ключа.
# Результат: значение на stdout (может быть пустым).
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

# Назначение: проверить .env перед установкой.
# Параметры: $1 — путь к .env.
# Результат: код 0 при успехе.
# Ошибки: одна причина на stderr, код 1.
check_env() {
	local file=$1 val
	if [[ ! -f "$file" ]]; then
		printf 'emtds-install: файл .env не найден; скопируйте: cp .env.example .env\n' >&2
		return 1
	fi
	if grep -q $'\r' "$file" 2>/dev/null; then
		printf 'emtds-install: в .env обнаружены символы CR (Windows-перевод строки)\n' >&2
		return 1
	fi
	val=$(env_value "$file" CLICKHOUSE_PASSWORD)
	if [[ -z "$val" ]]; then
		printf 'emtds-install: задайте непустой CLICKHOUSE_PASSWORD в .env\n' >&2
		return 1
	fi
	val=$(env_value_compat "$file" ADMIN_PASSWORD)
	if [[ -n "$val" && "$val" == admin ]]; then
		printf 'emtds-install: EMTDS_ADMIN_PASSWORD не должен быть значением admin\n' >&2
		return 1
	fi
	return 0
}

# Назначение: собрать .env коробки с паролем ClickHouse и без пароля админки.
# Параметры: $1 — пароль ClickHouse.
# Результат: содержимое файла на stdout.
# Ошибки: нет.
render_box_env() {
	local ch_pw=$1
	printf 'CLICKHOUSE_PASSWORD=%s\n' "$ch_pw"
}

# Назначение: канал apt Docker для поддерживаемой системы.
# Параметры: $1 — ID из os-release; $2 — VERSION_CODENAME.
# Результат: «debian bookworm» или «ubuntu jammy|noble» на stdout.
# Ошибки: другая система, код 1.
docker_apt_channel() {
	local id=$1 codename=$2
	case "${id}:${codename}" in
		ubuntu:jammy|ubuntu:noble|debian:bookworm)
			printf '%s %s' "$id" "$codename"
			;;
		*)
			return 1
			;;
	esac
}

# Назначение: создать .env коробки, если его нет, либо проверить существующий.
# Параметры: $1 — путь к .env.
# Результат: файл с паролем ClickHouse; пароль админки не добавляется и не печатается.
# Ошибки: openssl, запись или check_env, код 1.
ensure_box_env() {
	local file=$1 pw
	if [[ -f "$file" ]]; then
		check_env "$file"
		return
	fi
	if ! pw=$(openssl rand -hex 24); then
		msg "не удалось сгенерировать пароль ClickHouse" >&2
		return 1
	fi
	render_box_env "$pw" | write_atomic "$file" 0600
}

# Назначение: можно ли удалить одноразовый установщик после успеха.
# Параметры: $1 — путь к install.sh.
# Результат: код 0 если удаление разрешено.
# Ошибки: код 1.
can_self_delete() {
	local path=$1 base parent
	if [[ ! -f "$path" ]] || [[ -L "$path" ]]; then
		return 1
	fi
	base=$(basename "$path")
	[[ "$base" == install.sh ]] || return 1
	parent=$(basename "$(dirname "$path")")
	[[ "$parent" == deploy ]] || return 1
	return 0
}

# Назначение: экспортировать build-args из git, если .git присутствует в TDS_DIR.
# Параметры: $1 — канал установки (release|main|dev).
# Результат: EMTDS_VERSION, EMTDS_COMMIT, EMTDS_COMMIT_DATE, EMTDS_CHANNEL экспортированы.
# Ошибки: git недоступен или нет .git — пустые значения, не падение.
export_build_args() {
	local channel=${1:-release}
	export EMTDS_VERSION= EMTDS_COMMIT= EMTDS_COMMIT_DATE=
	export EMTDS_CHANNEL="$channel"
	if [[ -d "${TDS_DIR}/.git" ]]; then
		EMTDS_VERSION=$(git -c safe.directory="$TDS_DIR" -C "$TDS_DIR" describe --tags --always 2>/dev/null || true)
		EMTDS_COMMIT=$(git -c safe.directory="$TDS_DIR" -C "$TDS_DIR" rev-parse HEAD 2>/dev/null || true)
		EMTDS_COMMIT_DATE=$(git -c safe.directory="$TDS_DIR" -C "$TDS_DIR" show -s --format=%cI HEAD 2>/dev/null || true)
	fi
}

# Назначение: записать содержимое stdin в файл атомарно.
# Параметры: $1 — путь; $2 — режим chmod (например 0644).
# Результат: файл создан или заменён.
# Ошибки: mktemp/mv/chmod.
write_atomic() {
	local dest=$1 mode=$2 dir tmp
	dir=$(dirname "$dest")
	tmp=$(mktemp "${dir}/.emtds-install.XXXXXX")
	cat >"$tmp"
	chmod "$mode" "$tmp"
	mv -f "$tmp" "$dest"
}

# Назначение: краткая диагностика при сбое (ничего не останавливает).
# Параметры: нет.
# Результат: вывод ps и хвостов логов на stdout/stderr.
# Ошибки: отдельные команды игнорируются.
diagnose() {
	infra_compose ps 2>/dev/null || true
	app_compose ps 2>/dev/null || true
	docker logs --tail 40 emtds-engine 2>/dev/null || true
	docker logs --tail 40 emtds-admin 2>/dev/null || true
}

# Назначение: прочитать RAM сервера в MiB.
# Параметры: нет.
# Результат: число на stdout.
# Ошибки: нет.
mem_mib() {
	awk '/^MemTotal:/ { printf "%d", $2/1024 }' /proc/meminfo
}

# Назначение: свободное место в GiB для Docker или /var.
# Параметры: нет.
# Результат: целое GiB на stdout.
# Ошибки: нет.
free_gib() {
	local target=/var
	if [[ -d /var/lib/docker ]]; then
		target=/var/lib/docker
	fi
	df -BG --output=avail "$target" 2>/dev/null | tail -n 1 | tr -d ' G'
}

# Назначение: проверить наличие плагина docker compose.
# Параметры: нет.
# Результат: код 0 если compose доступен.
# Ошибки: код 1.
have_compose() {
	docker compose version >/dev/null 2>&1
}

# Назначение: установить Docker CE и compose-plugin из официального репозитория.
# Параметры: $1 — ubuntu или debian; $2 — codename (jammy, noble, bookworm).
# Результат: Docker запущен, compose доступен.
# Ошибки: apt/curl/systemctl, код 1.
install_docker_packages() {
	local distro=$1 codename=$2 arch
	arch=$(dpkg --print-architecture)
	export DEBIAN_FRONTEND=noninteractive
	apt-get -o DPkg::Lock::Timeout=300 update -y
	apt-get -o DPkg::Lock::Timeout=300 install -y ca-certificates curl
	install -m 0755 -d /etc/apt/keyrings
	curl -fsSL "https://download.docker.com/linux/${distro}/gpg" -o /etc/apt/keyrings/docker.asc
	chmod a+r /etc/apt/keyrings/docker.asc
	printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/%s %s stable\n' \
		"$arch" "$distro" "$codename" >/etc/apt/sources.list.d/docker.list
	apt-get -o DPkg::Lock::Timeout=300 update -y
	apt-get -o DPkg::Lock::Timeout=300 install -y \
		docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
	systemctl enable --now docker
}

# Назначение: убедиться, что docker compose доступен (установить при необходимости).
# Параметры: $1 — ubuntu или debian, пусто если пакеты ставить нельзя; $2 — codename.
# Результат: код 0. Уже установленный compose не переустанавливается.
# Ошибки: snap, docker без compose, неподдерживаемая система без docker, код 1.
ensure_docker_compose() {
	local distro=$1 codename=$2
	if have_compose; then
		return 0
	fi
	if snap list docker >/dev/null 2>&1; then
		msg "обнаружен Docker из snap; такая установка не поддерживается" >&2
		exit 1
	fi
	if command -v docker >/dev/null 2>&1; then
		msg "Docker установлен, но без плагина compose; установите docker-compose-plugin вручную" >&2
		exit 1
	fi
	if [[ -z "$distro" || -z "$codename" ]]; then
		msg "нет Docker. Пакеты ставятся на Ubuntu 22.04, 24.04 и Debian 12; на другой системе нужен уже установленный docker compose" >&2
		exit 1
	fi
	install_docker_packages "$distro" "$codename"
	if ! have_compose; then
		msg "после установки Docker compose по-прежнему недоступен" >&2
		exit 1
	fi
}

# Назначение: проверить чужие compose-проекты, порты и конфликты перед установкой.
# Параметры: нет.
# Результат: код 0.
# Ошибки: конфликт, код 1.
check_foreign_resources() {
	local cid name proj wd our_count=0 p out
	while read -r cid; do
		[[ -z "$cid" ]] && continue
		wd=$(container_label "$cid" com.docker.compose.project.working_dir)
		if [[ "$wd" != "$OUR_DEPLOY_DIR" ]]; then
			msg "проект deploy принадлежит другому стеку" >&2
			exit 1
		fi
		our_count=$((our_count + 1))
	done < <(docker ps -aq --filter label=com.docker.compose.project=deploy 2>/dev/null || true)

	for name in "${LEGACY_APP_CONTAINERS[@]}"; do
		if docker container inspect "$name" >/dev/null 2>&1; then
			msg "найден контейнер ${name} стека до переименования; запустите deploy/migrate-emtds.sh" >&2
			exit 1
		fi
	done

	for name in "${APP_CONTAINERS[@]}"; do
		if docker container inspect "$name" >/dev/null 2>&1; then
			proj=$(container_label "$name" com.docker.compose.project)
			wd=$(container_label "$name" com.docker.compose.project.working_dir)
			if [[ "$proj" != emtds-app || "$wd" != "$OUR_DEPLOY_DIR" ]]; then
				msg "контейнер ${name} принадлежит другому стеку" >&2
				exit 1
			fi
			our_count=$((our_count + 1))
		fi
	done

	if docker network inspect deploy_default >/dev/null 2>&1; then
		proj=$(docker network inspect -f '{{ index .Labels "com.docker.compose.project"}}' deploy_default 2>/dev/null || true)
		if [[ "$proj" != deploy ]]; then
			msg "сеть deploy_default принадлежит другому проекту" >&2
			exit 1
		fi
	fi

	if (( our_count == 0 )); then
		for p in 8123 9000 6379 18080 8090; do
			out=$(ss -ltnH "sport = :${p}" 2>/dev/null || true)
			if [[ -n "$out" ]]; then
				msg "порт ${p} уже занят на хосте" >&2
				exit 1
			fi
		done
	fi

	if pgrep -x emtds-cron >/dev/null 2>&1; then
		if ! docker container inspect emtds-cron >/dev/null 2>&1; then
			msg "cron EmTDS уже работает вне Docker" >&2
			exit 1
		fi
	fi

	if systemctl list-unit-files 'emtds*' --no-legend 2>/dev/null | awk '{print $1}' | grep -Ev '^emtds-(watchdog|update)\.' | grep -q .; then
		msg "найдены systemd-юниты emtds*; установка прервана" >&2
		exit 1
	fi

	if docker volume inspect emtds-app_emtds_state >/dev/null 2>&1; then
		msg "предупреждение: пароль админки берётся из admin.hash в томе, если он там есть; EMTDS_ADMIN_PASSWORD из .env применяется только на новом томе"
	fi
}

# Назначение: дождаться готовности схемы ClickHouse (колонка step в events).
# Параметры: нет.
# Результат: код 0.
# Ошибки: таймаут или неполная схема, код 1.
wait_clickhouse_schema() {
	local deadline=$((SECONDS + 300)) out
	while (( SECONDS < deadline )); do
		out=$(infra_compose exec -T clickhouse sh -c 'clickhouse-client --user "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" -q "SELECT count() FROM system.columns WHERE database = '\''kuztds'\'' AND table = '\''events'\'' AND name = '\''step'\''"' 2>/dev/null || true)
		if [[ "$(echo "$out" | tr -d '[:space:]')" == 1 ]]; then
			return 0
		fi
		sleep 5
	done
	msg "ClickHouse не ответил или схема неполная: схема применяется только на пустом томе, недостающие файлы migrations/clickhouse примените вручную" >&2
	diagnose
	exit 1
}

# Назначение: дождаться ответа Redis PONG.
# Параметры: нет.
# Результат: код 0.
# Ошибки: таймаут, код 1.
wait_redis_ping() {
	local deadline=$((SECONDS + 60)) out
	while (( SECONDS < deadline )); do
		out=$(infra_compose exec -T redis redis-cli ping 2>/dev/null || true)
		if [[ "$out" == PONG ]]; then
			return 0
		fi
		sleep 2
	done
	msg "Redis не ответил на ping" >&2
	diagnose
	exit 1
}

# Назначение: дождаться строк подключения в логах контейнера.
# Параметры: $1 — имя контейнера.
# Результат: код 0.
# Ошибки: таймаут, код 1.
wait_service_connections() {
	local name=$1 started deadline=$((SECONDS + 60)) logs
	started=$(docker inspect -f '{{.State.StartedAt}}' "$name" 2>/dev/null || true)
	while (( SECONDS < deadline )); do
		logs=$(docker logs --since "$started" "$name" 2>&1 || true)
		if grep -q 'clickhouse connected' <<<"$logs" && grep -q 'redis connected' <<<"$logs"; then
			return 0
		fi
		sleep 2
	done
	return 1
}

# Назначение: финальные проверки health и cron после старта приложения.
# Параметры: нет.
# Результат: код 0.
# Ошибки: провал проверки, код 1.
verify_running_stack() {
	local n
	if ! wait_service_connections emtds-engine; then
		msg "движок не сообщил о подключении к ClickHouse и Redis" >&2
		diagnose
		exit 1
	fi
	if ! wait_service_connections emtds-admin; then
		msg "админка не сообщила о подключении к ClickHouse и Redis" >&2
		diagnose
		exit 1
	fi
	if ! curl -fsS http://127.0.0.1:18080/healthz >/dev/null; then
		msg "healthz движка недоступен" >&2
		diagnose
		exit 1
	fi
	if ! curl -fsS http://127.0.0.1:8090/api/health >/dev/null; then
		msg "health админки недоступен" >&2
		diagnose
		exit 1
	fi
	n=$(docker ps -q --filter label=com.docker.compose.project=emtds-app --filter label=com.docker.compose.service=cron | wc -l | tr -d ' ')
	if [[ "$n" != 1 ]]; then
		msg "ожидался ровно один контейнер cron в проекте emtds-app" >&2
		diagnose
		exit 1
	fi
	if [[ "$(docker inspect -f '{{.State.Running}}' emtds-cron 2>/dev/null || echo false)" != true ]]; then
		msg "контейнер emtds-cron не запущен" >&2
		diagnose
		exit 1
	fi
}

# Назначение: записать файлы состояния и override в STATE_DIR.
# Параметры: $1 — RAM MiB.
# Результат: server.env, overrides и up.sh созданы.
# Ошибки: запись файлов, код 1.
write_state_files() {
	local mem=$1 public_ip=$2
	install -d -m 0755 "$STATE_DIR"
	render_server_env "$public_ip" | write_atomic "$SERVER_ENV" 0644
	render_infra_override "$mem" | write_atomic "$INFRA_OVERRIDE" 0644
	render_app_override "$mem" | write_atomic "$APP_OVERRIDE" 0644
	render_up_sh | write_atomic "$UP_SCRIPT" 0755
}

# Назначение: внутренний self-test без root и Docker.
# Параметры: нет.
# Результат: self-test ok, код 0.
# Ошибки: self-test FAIL, код 1.
SELFTEST_TMP=
run_self_test() {
	SELFTEST_TMP=$(mktemp -d)
	cleanup_selftest() { rm -rf "${SELFTEST_TMP:-}"; }
	trap cleanup_selftest EXIT

	assert_eq() {
		local got=$1 want=$2 label=$3
		if [[ "$got" != "$want" ]]; then
			printf 'self-test FAIL: %s (ожидали %q, получили %q)\n' "$label" "$want" "$got"
			exit 1
		fi
	}

	assert_eq "$(size_tier 6999 2 40)" refuse tier6999
	assert_eq "$(size_tier 7000 2 40)" warn tier7000
	assert_eq "$(size_tier 15000 4 80)" ok tier15000
	assert_eq "$(size_tier 14999 4 80)" warn tier14999mem
	assert_eq "$(size_tier 15000 3 80)" warn tier3cpu
	assert_eq "$(size_tier 15000 4 79)" warn tier79free
	assert_eq "$(size_tier 7000 1 40)" refuse tier1cpu
	assert_eq "$(size_tier 7000 2 39)" refuse tier39free

	assert_eq "$(ch_mem_limit 11999)" 3g ch11999
	assert_eq "$(ch_mem_limit 12000)" 6g ch12000
	assert_eq "$(ch_mem_limit 27999)" 6g ch27999
	assert_eq "$(ch_mem_limit 28000)" 8g ch28000

	assert_eq "$(redis_maxmemory 11999)" 512mb rmax11999
	assert_eq "$(redis_maxmemory 12000)" 2gb rmax12000
	assert_eq "$(redis_maxmemory 27999)" 2gb rmax27999
	assert_eq "$(redis_maxmemory 28000)" 2gb rmax28000
	assert_eq "$(redis_mem_limit 11999)" 768m rlim11999
	assert_eq "$(redis_mem_limit 12000)" 3g rlim12000
	assert_eq "$(redis_mem_limit 28000)" 3g rlim28000
	assert_eq "$(app_mem_limit 14999)" 1g app14999
	assert_eq "$(app_mem_limit 15000)" 2g app15000
	assert_eq "$(app_gomemlimit 1g)" 920MiB gomem1g
	assert_eq "$(app_gomemlimit 2g)" 1840MiB gomem2g

	local infra app env_out big
	infra=$(render_infra_override 8000)
	app=$(render_app_override 8000)
	if grep -q 'ports:' <<<"$infra"; then
		printf 'self-test FAIL: infra override содержит ports:\n'
		exit 1
	fi
	for frag in '--save' '""' '--appendonly' 'no' '--maxmemory-policy' 'noeviction'; do
		if [[ "$infra" != *"$frag"* ]]; then
			printf 'self-test FAIL: infra override без фрагмента %s\n' "$frag"
			exit 1
		fi
	done
	assert_eq "$(grep -c '"512mb"' <<<"$infra")" 1 infra_small_rmax
	big=$(render_infra_override 12000)
	assert_eq "$(grep -c '"2gb"' <<<"$big")" 1 infra_big_rmax
	assert_eq "$(grep -c 'mem_limit: 3g' <<<"$big")" 1 infra_big_redis_limit
	if grep -q 'ports:' <<<"$app"; then
		printf 'self-test FAIL: app override содержит ports:\n'
		exit 1
	fi
	if grep -q 'command:' <<<"$app"; then
		printf 'self-test FAIL: app override содержит command:\n'
		exit 1
	fi
	assert_eq "$(grep -c 'GOMEMLIMIT: ${EMTDS_GOMEMLIMIT:-920MiB}' <<<"$app")" 2 app_gomemlimit
	assert_eq "$(grep -c 'GOMEMLIMIT: ${EMTDS_GOMEMLIMIT:-1840MiB}' <<<"$(render_app_override 16000)")" 2 app_gomemlimit_2g

	env_out=$(render_server_env 203.0.113.1)
	assert_eq "$(echo "$env_out" | wc -l | tr -d ' ')" 6 server_env_lines
	if [[ "$env_out" != *'EMTDS_ENGINE_PUBLISH=127.0.0.1:18080:8080'* ]]; then
		printf 'self-test FAIL: server.env без порта движка 18080\n'
		exit 1
	fi
	if [[ "$env_out" != *'EMTDS_PUBLIC_IP=203.0.113.1'* ]]; then
		printf 'self-test FAIL: server.env без EMTDS_PUBLIC_IP\n'
		exit 1
	fi
	if ! declare -F valid_ipv4 >/dev/null; then
		printf 'self-test FAIL: valid_ipv4 не загружен из apply-edge.sh\n'
		exit 1
	fi
	valid_ipv4 203.0.113.1 || { printf 'self-test FAIL: valid_ipv4\n'; exit 1; }
	valid_ipv4 999.1.1.1 && { printf 'self-test FAIL: valid_ipv4 reject\n'; exit 1; }
	local detected
	detected=$(EMTDS_PUBLIC_IP=198.51.100.7 detect_public_ipv4)
	assert_eq "$detected" 198.51.100.7 detect_env_ip

	local f
	f="$SELFTEST_TMP/missing.env"
	if check_env "$f" 2>/dev/null; then
		printf 'self-test FAIL: check_env missing file\n'
		exit 1
	fi
	printf 'CLICKHOUSE_PASSWORD=x\r\n' >"$f"
	if check_env "$f" 2>/dev/null; then
		printf 'self-test FAIL: check_env CRLF\n'
		exit 1
	fi
	printf 'CLICKHOUSE_PASSWORD=\nEMTDS_ADMIN_PASSWORD=secret\n' >"$f"
	if check_env "$f" 2>/dev/null; then
		printf 'self-test FAIL: check_env empty CH\n'
		exit 1
	fi
	printf 'CLICKHOUSE_PASSWORD=ch\nEMTDS_ADMIN_PASSWORD=admin\n' >"$f"
	if check_env "$f" 2>/dev/null; then
		printf 'self-test FAIL: check_env admin=admin\n'
		exit 1
	fi
	printf 'CLICKHOUSE_PASSWORD=ch\nKUZTDS_ADMIN_PASSWORD=admin\n' >"$f"
	if check_env "$f" 2>/dev/null; then
		printf 'self-test FAIL: check_env legacy admin=admin\n'
		exit 1
	fi
	printf 'CLICKHOUSE_PASSWORD=ch\nEMTDS_ADMIN_PASSWORD=\n' >"$f"
	check_env "$f" 2>/dev/null || { printf 'self-test FAIL: check_env empty admin\n'; exit 1; }
	printf 'CLICKHOUSE_PASSWORD=ch\n' >"$f"
	check_env "$f" 2>/dev/null || { printf 'self-test FAIL: check_env no admin key\n'; exit 1; }
	local box_env
	box_env=$(render_box_env 'cafebabe')
	if [[ "$box_env" != 'CLICKHOUSE_PASSWORD=cafebabe' || "$box_env" == *EMTDS_ADMIN_PASSWORD* ]]; then
		printf 'self-test FAIL: render_box_env\n'
		exit 1
	fi
	local channel
	channel=$(docker_apt_channel debian bookworm) || { printf 'self-test FAIL: debian bookworm\n'; exit 1; }
	assert_eq "$channel" "debian bookworm" docker_debian
	channel=$(docker_apt_channel ubuntu noble) || { printf 'self-test FAIL: ubuntu noble\n'; exit 1; }
	assert_eq "$channel" "ubuntu noble" docker_noble
	channel=$(docker_apt_channel ubuntu jammy) || { printf 'self-test FAIL: ubuntu jammy\n'; exit 1; }
	assert_eq "$channel" "ubuntu jammy" docker_jammy
	if docker_apt_channel ubuntu focal >/dev/null 2>&1; then
		printf 'self-test FAIL: ubuntu focal must be refused\n'
		exit 1
	fi
	printf 'CLICKHOUSE_PASSWORD=ch\nEMTDS_ADMIN_PASSWORD="secret"\n' >"$f"
	check_env "$f" 2>/dev/null || { printf 'self-test FAIL: quoted admin\n'; exit 1; }
	printf 'CLICKHOUSE_PASSWORD=chpass\nEMTDS_ADMIN_PASSWORD=goodpass\n' >"$f"
	check_env "$f" 2>/dev/null || { printf 'self-test FAIL: normal env\n'; exit 1; }

	mkdir -p "$SELFTEST_TMP/deploy"
	cp "$SELF" "$SELFTEST_TMP/deploy/install.sh" 2>/dev/null || cp "$0" "$SELFTEST_TMP/deploy/install.sh"
	can_self_delete "$SELFTEST_TMP/deploy/install.sh" || { printf 'self-test FAIL: can_self_delete valid\n'; exit 1; }
	printf '#!/bin/bash\n' >"$SELFTEST_TMP/deploy/other.sh"
	can_self_delete "$SELFTEST_TMP/deploy/other.sh" && { printf 'self-test FAIL: wrong basename\n'; exit 1; }
	ln -sf install.sh "$SELFTEST_TMP/deploy/link.sh"
	can_self_delete "$SELFTEST_TMP/deploy/link.sh" && { printf 'self-test FAIL: symlink\n'; exit 1; }
	mkdir -p "$SELFTEST_TMP/other"
	cp "$SELFTEST_TMP/deploy/install.sh" "$SELFTEST_TMP/other/install.sh"
	can_self_delete "$SELFTEST_TMP/other/install.sh" && { printf 'self-test FAIL: wrong parent dir\n'; exit 1; }
	can_self_delete /dev/fd/63 && { printf 'self-test FAIL: fd path\n'; exit 1; }

	local up_out up_file
	up_out=$(render_up_sh)
	for frag in '-p deploy' 'compose.infra.yml' 'compose.app.yml' 'server.env' '--wait'; do
		if [[ "$up_out" != *"$frag"* ]]; then
			printf 'self-test FAIL: render_up_sh без фрагмента %s\n' "$frag"
			exit 1
		fi
	done
	up_file="$SELFTEST_TMP/up.sh"
	printf '%s' "$up_out" >"$up_file"
	if ! bash -n "$up_file"; then
		printf 'self-test FAIL: render_up_sh не проходит bash -n\n'
		exit 1
	fi

	(
		notgit="$SELFTEST_TMP/notgit"
		mkdir -p "$notgit"
		TDS_DIR="$notgit" export_build_args release
		[[ "${EMTDS_CHANNEL}" == release ]] || { printf 'self-test FAIL: build-args channel release\n'; exit 1; }
		[[ -z "${EMTDS_VERSION}" ]] || { printf 'self-test FAIL: EMTDS_VERSION без .git должен быть пустым\n'; exit 1; }
		[[ -z "${EMTDS_COMMIT}" ]] || { printf 'self-test FAIL: EMTDS_COMMIT без .git должен быть пустым\n'; exit 1; }
		TDS_DIR="$notgit" export_build_args main
		[[ "${EMTDS_CHANNEL}" == main ]] || { printf 'self-test FAIL: build-args channel main\n'; exit 1; }
	) || exit 1

	printf 'self-test ok\n'
}

# Назначение: основной сценарий установки EmTDS. Только root.
# Параметры: нет.
# Результат: маркер installed, сообщения об успехе.
# Ошибки: любая проверка или шаг развёртывания, код 1.
run_install() {
	if [[ -z "$SELF" || ! -f "$SELF" ]] || [[ "${BASH_SOURCE[0]:-}" != "$0" ]]; then
		msg "запустите: sudo bash deploy/install.sh" >&2
		exit 1
	fi

	if [[ -f "$MARKER" ]]; then
		msg "EmTDS уже установлен, поднять снова: ${UP_SCRIPT}"
		exit 0
	fi

	if [[ $(id -u) -ne 0 ]]; then
		msg "нужны права root (sudo)" >&2
		exit 1
	fi

	local docker_distro= docker_codename= channel
	if [[ -f /etc/os-release ]]; then
		# shellcheck source=/dev/null
		source /etc/os-release
		if channel=$(docker_apt_channel "${ID:-}" "${VERSION_CODENAME:-}"); then
			docker_distro=${channel%% *}
			docker_codename=${channel#* }
		fi
	fi

	local mem cpus free tier
	mem=$(mem_mib)
	cpus=$(nproc)
	free=$(free_gib)
	tier=$(size_tier "$mem" "$cpus" "$free")
	case "$tier" in
		refuse)
			msg "недостаточно ресурсов: RAM ${mem} MiB, CPU ${cpus}, свободно ${free} GiB" >&2
			exit 1
			;;
		warn)
			msg "предупреждение: для долгой работы нужно 16 ГБ RAM, 4 ядра, 80 ГБ"
			;;
	esac

	ensure_box_env "$TDS_DIR/.env" || exit 1

	ensure_docker_compose "$docker_distro" "$docker_codename"

	check_foreign_resources

	local public_ip
	public_ip=$(detect_public_ipv4) || exit 1

	write_state_files "$mem" "$public_ip"

	cd "$TDS_DIR"
	export_build_args "${INSTALL_CHANNEL}"
	if ! timeout 1800 "${APP_COMPOSE[@]}" build engine; then
		msg "сборка образа не завершилась; проверьте сеть и повторите" >&2
		diagnose
		exit 1
	fi

	if ! infra_compose up -d; then
		diagnose
		exit 1
	fi
	if ! declare -F ensure_server_env_keys >/dev/null; then
		msg "не найден apply-edge.sh (ensure_server_env_keys)" >&2
		exit 1
	fi
	if ! ensure_server_env_keys "$public_ip"; then
		exit 1
	fi
	wait_clickhouse_schema
	wait_redis_ping

	if ! app_compose up -d --wait --wait-timeout 300; then
		diagnose
		exit 1
	fi

	verify_running_stack

	if command -v nginx >/dev/null 2>&1; then
		if [[ -f "${OUR_DEPLOY_DIR}/apply-edge.sh" ]]; then
			if ! bash "${OUR_DEPLOY_DIR}/apply-edge.sh" "$public_ip"; then
				msg "не удалось применить конфигурацию nginx (apply-edge.sh)" >&2
				exit 1
			fi
		fi
	else
		msg "nginx не установлен; пропуск apply-edge.sh (стек установлен без edge)"
	fi

	if [[ -f "${OUR_DEPLOY_DIR}/watchdog.sh" ]]; then
		if ! bash "${OUR_DEPLOY_DIR}/watchdog.sh" --install; then
			msg "не удалось включить emtds-watchdog.timer" >&2
			exit 1
		fi
	fi

	if [[ -f "${OUR_DEPLOY_DIR}/update.sh" ]]; then
		if [[ ! -d "${TDS_DIR}/.git" ]]; then
			msg "агент обновлений не установлен: нет git-дерева, см. update.sh --adopt"
		elif command -v systemctl >/dev/null 2>&1; then
			local update_args=(--install "--channel" "${INSTALL_CHANNEL}" "--tds-dir" "${TDS_DIR}")
			[[ -n "${INSTALL_REPO}" ]] && update_args+=(--repo "${INSTALL_REPO}")
			if ! bash "${OUR_DEPLOY_DIR}/update.sh" "${update_args[@]}"; then
				msg "предупреждение: не удалось установить агент обновлений; продолжение"
			fi
		else
			msg "агент обновлений не установлен: нет systemd, запустите update.sh --install вручную"
		fi
	fi

	date -Iseconds | write_atomic "$MARKER" 0644

	if can_self_delete "$SELF" && [[ ! -d "${TDS_DIR}/.git" ]]; then
		rm -f -- "$SELF"
		msg "установщик удалён; повторный запуск install.sh заблокирован маркером ${MARKER}"
	elif [[ -d "${TDS_DIR}/.git" ]]; then
		msg "установщик не удалён: каталог кода под git, дерево не должно быть грязным; повторный запуск заблокирован маркером ${MARKER}"
	else
		msg "файл установщика не удалён (ожидался deploy/install.sh); повторный запуск заблокирован маркером ${MARKER}"
	fi

	msg "готово: движок 127.0.0.1:18080, админка 127.0.0.1:8090"
	if [[ -z "$(env_value_compat "$TDS_DIR/.env" ADMIN_PASSWORD)" ]]; then
		msg "первый вход спросит логин, пароль и почту главного администратора"
	fi
	msg "вход: ssh -L 8090:127.0.0.1:8090 user@host"
	msg "повторный старт: ${UP_SCRIPT}"
}

# Назначение: вывести краткую справку по аргументам.
# Параметры: нет.
# Результат: текст usage на stderr.
# Ошибки: нет.
usage() {
	msg "использование: sudo bash deploy/install.sh | install.sh --self-test" >&2
}

# Назначение: точка входа установщика.
# Параметры: [--channel release|main] [--repo URL] | --self-test.
# Результат: код выхода сценария.
# Ошибки: см. run_install и run_self_test.
main() {
	local mode=install
	while (( $# > 0 )); do
		case "$1" in
			--self-test) mode=self-test; shift ;;
			--channel)   INSTALL_CHANNEL="${2:-release}"; shift 2 ;;
			--repo)      INSTALL_REPO="${2:-}"; shift 2 ;;
			*)           usage; exit 2 ;;
		esac
	done
	case "$mode" in
		self-test) run_self_test ;;
		*)         run_install ;;
	esac
}

main "$@"
