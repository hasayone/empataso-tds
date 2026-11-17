#!/usr/bin/env bash

set -euo pipefail

SELF=$(readlink -f "${BASH_SOURCE[0]:-}" 2>/dev/null || true)
TDS_DIR=
if [[ -n "$SELF" ]]; then
	TDS_DIR=$(dirname "$(dirname "$SELF")")
fi
STATE_DIR="${EMTDS_INSTALL_STATE_DIR:-/etc/emtds}"
MARKER="$STATE_DIR/installed"
SERVER_ENV="$STATE_DIR/server.env"
INFRA_OVERRIDE="$STATE_DIR/compose.infra.yml"
APP_OVERRIDE="$STATE_DIR/compose.app.yml"
UP_SCRIPT="$STATE_DIR/up.sh"
OUR_DEPLOY_DIR="${TDS_DIR}/deploy"
INSTALL_CHANNEL="${EMTDS_INSTALL_CHANNEL:-release}"
INSTALL_REPO="${EMTDS_INSTALL_REPO:-}"
# Каталог клона EmTDS, куда загрузчик кладёт код (рядом с ним создаётся временный клон).
INSTALL_DIR="${EMTDS_INSTALL_DIR:-/opt/emtds}"
# Общий замок установщика, агента обновлений и watchdog: пока он занят, watchdog уступает.
LOCK_FILE="${EMTDS_INSTALL_LOCK_FILE:-/run/emtds-update.lock}"
MEMINFO="${EMTDS_INSTALL_MEMINFO:-/proc/meminfo}"
OS_RELEASE="${EMTDS_INSTALL_OS_RELEASE:-/etc/os-release}"
# Скрипт агента обновлений, который ставит update.sh --install.
UPDATE_LIB_SCRIPT=/usr/local/lib/emtds/update.sh
# Корень, где ищутся каталоги прежних установок (<корень>/*tds/installed).
OLD_ETC_ROOT="${EMTDS_INSTALL_OLD_ETC_ROOT:-/etc}"
ALLOWED_SIGNERS="$STATE_DIR/allowed_signers"
# Необязательный файл ключей подписи (--allowed-signers): без него подпись не проверяется и не включается.
INSTALL_SIGNERS=
# 1 — не ставить nginx и не применять apply-edge.sh (--no-edge).
NO_EDGE=0
# 1 — apply-edge.sh отработал: админка открыта на https://<публичный IP>/.
INSTALL_EDGE_APPLIED=0
# Исходные аргументы командной строки: загрузчик передаёт их клону через exec.
ORIG_ARGS=()
# Временный клон загрузчика; удаляется при любом выходе, пока не переименован в INSTALL_DIR.
LOADER_TMP=
export EMTDS_SERVER_ENV="$SERVER_ENV"

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

# Назначение: нужен ли режим загрузчика: установщик запущен не из deploy/ клона или в каталоге нет кода.
# Параметры: $1 — путь к install.sh; $2 — каталог кода (на уровень выше deploy/).
# Результат: код 0, если нужен загрузчик.
# Ошибки: код 1 — код уже на месте, работает основной сценарий.
loader_mode_needed() {
	local path=$1 tds_dir=$2
	if ! can_self_delete "$path"; then
		return 0
	fi
	[[ ! -f "${tds_dir}/deploy/docker-compose.app.yml" ]]
}

# Библиотека apply-edge.sh подключается только из дерева EmTDS (deploy/install.sh с compose-файлом рядом):
# загрузчик, лежащий, например, в /root, не должен исполнять файл из каталога выше себя.
if ! loader_mode_needed "$SELF" "$TDS_DIR" && [[ -f "${OUR_DEPLOY_DIR}/apply-edge.sh" ]]; then
	# shellcheck source=apply-edge.sh
	source "${OUR_DEPLOY_DIR}/apply-edge.sh"
fi
APP_CONTAINERS=(emtds-engine emtds-admin emtds-cron emtds-php)
INFRA_COMPOSE=(docker compose -p emtds-infra --env-file "$TDS_DIR/.env" --env-file "$SERVER_ENV" -f "$TDS_DIR/deploy/docker-compose.yml" -f "$INFRA_OVERRIDE")
APP_COMPOSE=(docker compose --env-file "$TDS_DIR/.env" --env-file "$SERVER_ENV" -f "$TDS_DIR/deploy/docker-compose.app.yml" -f "$APP_OVERRIDE")

# Назначение: вывести сообщение пользователю с префиксом установщика.
# Параметры: текст сообщения.
# Результат: строка на stdout.
# Ошибки: нет.
msg() {
	printf 'emtds-install: %s\n' "$*"
}

# Назначение: прервать установку с сообщением об ошибке.
# Параметры: текст сообщения.
# Результат: строка на stderr, выход с кодом 1.
# Ошибки: всегда код 1.
die() {
	msg "$*" >&2
	exit 1
}

# Назначение: проверить права root; песочница тестов (EMTDS_INSTALL_SANDBOX=1) проверку пропускает.
# Параметры: нет.
# Результат: код 0, если права есть.
# Ошибки: код 1.
is_root() {
	[[ "${EMTDS_INSTALL_SANDBOX:-}" == 1 ]] || [[ $(id -u) -eq 0 ]]
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
EMTDS_ENV=production
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
	val=$(env_value "$file" EMTDS_ADMIN_PASSWORD)
	if [[ -n "$val" && "$val" == admin ]]; then
		printf 'emtds-install: EMTDS_ADMIN_PASSWORD не должен быть значением admin\n' >&2
		return 1
	fi
	return 0
}

# Назначение: собрать .env коробки с паролем ClickHouse, токеном первого входа и без пароля админки.
# Параметры: $1 — пароль ClickHouse; $2 — токен первого входа (EMTDS_SETUP_TOKEN).
# Результат: содержимое файла на stdout.
# Ошибки: нет.
render_box_env() {
	local ch_pw=$1 setup_token=$2
	printf 'CLICKHOUSE_PASSWORD=%s\n' "$ch_pw"
	printf 'EMTDS_SETUP_TOKEN=%s\n' "$setup_token"
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

# Назначение: дописать в существующий .env токен первого входа, если его нет или он не 32 знака hex.
# Параметры: $1 — путь к .env.
# Результат: в файле EMTDS_SETUP_TOKEN из 32 строчных шестнадцатеричных знаков (пустое, с пробелами
# и любое другое значение заменяется новым); остальные строки и режим файла не меняются.
# Ошибки: openssl или запись, код 1.
ensure_setup_token() {
	local file=$1 token rest
	if [[ "$(env_value "$file" EMTDS_SETUP_TOKEN)" =~ ^[0123456789abcdef]{32}$ ]]; then
		return 0
	fi
	if ! token=$(openssl rand -hex 16); then
		msg "не удалось сгенерировать токен первого входа" >&2
		return 1
	fi
	rest=$(grep -v '^EMTDS_SETUP_TOKEN=' "$file" || true)
	{
		[[ -z "$rest" ]] || printf '%s\n' "$rest"
		printf 'EMTDS_SETUP_TOKEN=%s\n' "$token"
	} | write_atomic "$file" 0600
}

# Назначение: создать .env коробки, если его нет, либо проверить существующий и дополнить токеном.
# Параметры: $1 — путь к .env.
# Результат: файл с паролем ClickHouse и токеном первого входа; пароль админки не добавляется и не печатается.
# Ошибки: openssl, запись или check_env, код 1.
ensure_box_env() {
	local file=$1 pw token
	if [[ -f "$file" ]]; then
		check_env "$file" || return 1
		ensure_setup_token "$file"
		return
	fi
	if ! pw=$(openssl rand -hex 24); then
		msg "не удалось сгенерировать пароль ClickHouse" >&2
		return 1
	fi
	if ! token=$(openssl rand -hex 16); then
		msg "не удалось сгенерировать токен первого входа" >&2
		return 1
	fi
	render_box_env "$pw" "$token" | write_atomic "$file" 0600
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

# Назначение: убедиться до сборки, что у git-дерева определён полный коммит.
# Параметры: нет (EMTDS_COMMIT из export_build_args, каталог TDS_DIR).
# Результат: код 0, если .git нет или EMTDS_COMMIT состоит из 40 шестнадцатеричных знаков.
# Ошибки: .git есть, а коммит не определён (пустое дерево, повреждение) — код 1: образ получил бы dev-сборку.
require_build_commit() {
	if [[ -d "${TDS_DIR}/.git" && ! "${EMTDS_COMMIT:-}" =~ ^[0-9a-f]{40}$ ]]; then
		msg "не удалось определить коммит git-дерева ${TDS_DIR}; сборка отменена" >&2
		return 1
	fi
	return 0
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
	awk '/^MemTotal:/ { printf "%d", $2/1024 }' "$MEMINFO"
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
			msg "проект emtds-infra принадлежит другому стеку" >&2
			exit 1
		fi
		our_count=$((our_count + 1))
	done < <(docker ps -aq --filter label=com.docker.compose.project=emtds-infra 2>/dev/null || true)

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

	if docker network inspect emtds-infra_default >/dev/null 2>&1; then
		proj=$(docker network inspect -f '{{ index .Labels "com.docker.compose.project"}}' emtds-infra_default 2>/dev/null || true)
		if [[ "$proj" != emtds-infra ]]; then
			msg "сеть emtds-infra_default принадлежит другому проекту" >&2
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
		out=$(infra_compose exec -T clickhouse sh -c 'clickhouse-client --user "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" -q "SELECT count() FROM system.columns WHERE database = '\''emtds'\'' AND table = '\''events'\'' AND name = '\''step'\''"' 2>/dev/null || true)
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

# Назначение: установить пакеты apt без вопросов; замок установщика (fd 9) в apt не наследуется.
# Параметры: имена пакетов.
# Результат: пакеты установлены.
# Ошибки: apt-get завершился с ошибкой, код 1.
apt_install() {
	export DEBIAN_FRONTEND=noninteractive
	apt-get -o DPkg::Lock::Timeout=300 update -y 9>&-
	apt-get -o DPkg::Lock::Timeout=300 install -y "$@" 9>&-
}

# Назначение: выполнить git загрузчика с закреплёнными параметрами безопасности:
# без системной и пользовательской конфигурации, хуков, запросов пароля и транспорта ext.
# Параметры: аргументы git.
# Результат: код возврата git.
# Ошибки: код git.
loader_git() {
	GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0 \
		git -c core.hooksPath=/dev/null -c core.fsmonitor=false -c core.sshCommand= \
		-c gpg.program=gpg -c gpg.ssh.program=ssh-keygen -c protocol.ext.allow=never "$@"
}

# Назначение: выполнить loader_git внутри клона (safe.directory закреплён на этот каталог).
# Параметры: $1 — каталог клона; остальные — аргументы git.
# Результат: код возврата git.
# Ошибки: код git.
loader_git_in() {
	local dir=$1
	shift
	loader_git -c "safe.directory=${dir}" -C "$dir" "$@"
}

# Назначение: выбрать ссылку для checkout по каналу установки.
# Параметры: $1 — каталог клона; $2 — канал (main или release).
# Результат: refs/remotes/origin/main или refs/tags/<максимальный vX.Y.Z> на stdout; тег release обязан
# быть предком origin/main, как в update.sh.
# Ошибки: нет origin/main, нет тегов vX.Y.Z или тег вне истории main — сообщение на stderr, код 1.
loader_pick_ref() {
	local dir=$1 channel=$2 tag sha
	case "$channel" in
		main)
			if ! loader_git_in "$dir" rev-parse --verify --quiet refs/remotes/origin/main >/dev/null; then
				msg "в репозитории нет ветки main" >&2
				return 1
			fi
			printf 'refs/remotes/origin/main'
			;;
		release)
			tag=$(loader_git_in "$dir" tag -l 'v*' | LC_ALL=C grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1 || true)
			if [[ -z "$tag" ]]; then
				msg "в репозитории нет тегов vX.Y.Z: канал release невозможен, используйте --channel main" >&2
				return 1
			fi
			if ! sha=$(loader_git_in "$dir" rev-parse --verify --quiet "refs/tags/${tag}^{commit}"); then
				msg "не удалось разрешить тег ${tag}" >&2
				return 1
			fi
			if ! loader_git_in "$dir" rev-parse --verify --quiet refs/remotes/origin/main >/dev/null; then
				msg "в репозитории нет ветки main: тег ${tag} нельзя сверить с ней" >&2
				return 1
			fi
			if ! loader_git_in "$dir" merge-base --is-ancestor "$sha" refs/remotes/origin/main; then
				msg "тег ${tag} не является предком origin/main" >&2
				return 1
			fi
			printf 'refs/tags/%s' "$tag"
			;;
		*)
			msg "--channel: допустимо release или main" >&2
			return 1
			;;
	esac
}

# Назначение: проверить подпись SSH у того, что вытащено в каталог клона, до запуска его установщика.
# Параметры: $1 — каталог клона; $2 — ссылка из loader_pick_ref; $3 — файл allowed_signers.
# Результат: код 0, если HEAD равен коммиту ссылки и подпись верна. Ссылка refs/tags/* — подпись тега,
# иначе — подпись коммита HEAD.
# Ошибки: файл не читается, HEAD не совпал со ссылкой, подписи нет или она неверна — сообщение на stderr, код 1.
loader_verify() {
	local dir=$1 ref=$2 signers=$3 out head want
	if [[ ! -r "$signers" ]]; then
		msg "--allowed-signers: файл ${signers} не читается" >&2
		return 1
	fi
	head=$(loader_git_in "$dir" rev-parse HEAD 2>/dev/null || true)
	want=$(loader_git_in "$dir" rev-parse --verify --quiet "${ref}^{commit}" 2>/dev/null || true)
	if [[ -z "$head" || "$head" != "$want" ]]; then
		msg "HEAD клона не совпадает с ${ref}: подпись проверять не у чего" >&2
		return 1
	fi
	if [[ "$ref" == refs/tags/* ]]; then
		out=$(loader_git_in "$dir" -c gpg.format=ssh -c "gpg.ssh.allowedSignersFile=${signers}" verify-tag "$ref" 2>&1) || {
			printf '%s\n' "$out" >&2
			msg "подпись тега ${ref#refs/tags/} не прошла проверку" >&2
			return 1
		}
		return 0
	fi
	out=$(loader_git_in "$dir" -c gpg.format=ssh -c "gpg.ssh.allowedSignersFile=${signers}" verify-commit HEAD 2>&1) || {
		printf '%s\n' "$out" >&2
		msg "подпись коммита HEAD не прошла проверку" >&2
		return 1
	}
}

# Назначение: удалить временный клон загрузчика (ловушка EXIT).
# Параметры: нет.
# Результат: каталог LOADER_TMP удалён, если он ещё не переименован в INSTALL_DIR.
# Ошибки: нет.
loader_cleanup() {
	if [[ -n "$LOADER_TMP" && -d "$LOADER_TMP" ]]; then
		rm -rf -- "$LOADER_TMP"
	fi
}

# Назначение: удалить остатки прерванных прогонов загрузчика (например, после kill -9): каталоги
# <INSTALL_DIR>.clone.XXXXXX, где XXXXXX — шесть знаков [A-Za-z0-9], внутри есть .git, это не ссылка,
# и они не менялись больше часа (параллельный прогон не задевается). Чужие имена не трогаются.
# Параметры: нет (INSTALL_DIR).
# Результат: старые временные клоны удалены.
# Ошибки: нет.
loader_sweep_stale() {
	local dir prefix="${INSTALL_DIR}.clone."
	for dir in "${prefix}"??????; do
		[[ "${dir#"$prefix"}" =~ ^[A-Za-z0-9]{6}$ ]] || continue
		[[ -d "$dir" && ! -L "$dir" && -d "${dir}/.git" ]] || continue
		[[ -n "$(find "$dir" -maxdepth 0 -mmin +60 2>/dev/null)" ]] || continue
		rm -rf -- "$dir"
	done
}

# Назначение: обновить в существующем клоне ветки и теги по origin. Локальные теги сначала удаляются,
# чтобы снятый в origin тег не остался кандидатом в release; ветки берутся явным refspec, а настроенные
# у клона refspec игнорируются.
# Параметры: $1 — каталог клона.
# Результат: refs/remotes/origin/* и теги совпадают с origin.
# Ошибки: удаление тегов или fetch не удались — код 1.
loader_fetch() {
	local dir=$1 tags
	tags=$(loader_git_in "$dir" tag -l) || return 1
	if [[ -n "$tags" ]]; then
		# Имена ссылок git не содержат пробелов и шаблонных знаков, разбиение по словам безопасно.
		# shellcheck disable=SC2086
		loader_git_in "$dir" tag -d $tags >/dev/null || return 1
	fi
	loader_git_in "$dir" -c 'remote.origin.fetch=+refs/heads/*:refs/remotes/origin/*' \
		fetch --quiet --tags --force origin '+refs/heads/*:refs/remotes/origin/*'
}

# Назначение: перевести клон на версию канала (общий шаг для нового и существующего клона):
# выбрать ссылку, checkout --detach, проверить подпись при --allowed-signers, убедиться в наличии
# файлов, которым передаётся управление.
# Параметры: $1 — каталог клона (INSTALL_CHANNEL, INSTALL_SIGNERS).
# Результат: код 0, клон на detached HEAD выбранной версии.
# Ошибки: версия не выбрана, checkout, подпись или нет deploy/install.sh и deploy/docker-compose.app.yml —
# сообщение на stderr, код 1.
loader_checkout() {
	local dir=$1 ref file
	ref=$(loader_pick_ref "$dir" "$INSTALL_CHANNEL") || return 1
	if ! loader_git_in "$dir" checkout --quiet --detach "${ref}^{commit}"; then
		msg "не удалось перейти на ${ref}" >&2
		return 1
	fi
	if [[ -n "$INSTALL_SIGNERS" ]]; then
		loader_verify "$dir" "$ref" "$INSTALL_SIGNERS" || return 1
	fi
	for file in deploy/install.sh deploy/docker-compose.app.yml; do
		if [[ ! -f "${dir}/${file}" ]]; then
			msg "в ${dir} нет ${file}: это не код EmTDS" >&2
			return 1
		fi
	done
}

# Назначение: склонировать репозиторий целиком во временный каталог рядом с INSTALL_DIR,
# перейти на версию канала (loader_checkout) и атомарно переименовать.
# Параметры: нет (INSTALL_REPO, INSTALL_DIR).
# Результат: INSTALL_DIR — клон на detached HEAD выбранной версии с историей и тегами.
# Ошибки: clone, выбор версии, подпись или rename не удались — сообщение, код 1, временный клон удалён.
loader_clone() {
	local tmp
	install -d -m 0755 "$(dirname "$INSTALL_DIR")"
	tmp=$(mktemp -d "${INSTALL_DIR}.clone.XXXXXX")
	LOADER_TMP=$tmp
	trap loader_cleanup EXIT
	loader_git clone --quiet -- "$INSTALL_REPO" "$tmp" || die "не удалось клонировать ${INSTALL_REPO}"
	loader_checkout "$tmp" || exit 1
	chmod 0755 "$tmp"
	mv -T -- "$tmp" "$INSTALL_DIR" || die "не удалось переименовать клон в ${INSTALL_DIR}"
	LOADER_TMP=
}

# Назначение: принять уже существующий INSTALL_DIR: только чистый клон с тем же origin; затем он
# обновляется и переводится на версию канала так же, как свежий клон (fetch и loader_checkout).
# Параметры: нет (INSTALL_DIR, INSTALL_REPO).
# Результат: код 0, клон на detached HEAD выбранной версии; при --allowed-signers подпись проверена
# именно у неё.
# Ошибки: не клон, другой origin, локальные правки, fetch, выбор версии или подпись — сообщение, код 1.
loader_reuse_clone() {
	local dir=$INSTALL_DIR origin
	if [[ -L "$dir" || ! -d "${dir}/.git" ]]; then
		die "каталог ${dir} уже существует и не является клоном: переместите его и повторите"
	fi
	origin=$(loader_git_in "$dir" remote get-url origin 2>/dev/null || true)
	if [[ "$origin" != "$INSTALL_REPO" ]]; then
		die "в ${dir} клон другого репозитория (origin не совпадает с --repo)"
	fi
	if [[ -n "$(loader_git_in "$dir" status --porcelain 2>/dev/null || echo dirty)" ]]; then
		die "в клоне ${dir} есть локальные изменения: очистите дерево и повторите"
	fi
	loader_fetch "$dir" || die "не удалось получить обновления из ${INSTALL_REPO}"
	loader_checkout "$dir" || exit 1
}

# Назначение: проверить адрес репозитория по правилу update.sh (validate_repo_url): https://хост/путь,
# git@хост:путь.git или ssh://git@хост/путь, без учётных данных и пробельных знаков. В песочнице тестов
# (EMTDS_INSTALL_SANDBOX=1) допустим ещё абсолютный локальный путь; http://, git:// и file:// отклоняются всегда.
# Параметры: $1 — адрес.
# Результат: код 0, если адрес допустим.
# Ошибки: код 1 без вывода: адрес в сообщения не попадает.
valid_repo_url() {
	local url=$1
	local creds='^https?://[^@]+@' allowed='^(git@[^:]+:.+\.git|ssh://git@[^/]+/.+|https://[^/]+/.+)$' local_path='^/.+'
	[[ -n "$url" && "$url" != *[[:space:]]* && "$url" != *[[:cntrl:]]* ]] || return 1
	[[ ! "$url" =~ $creds ]] || return 1
	[[ "$url" =~ $allowed ]] && return 0
	[[ "${EMTDS_INSTALL_SANDBOX:-}" == 1 && "$url" =~ $local_path ]]
}

# Назначение: отказать до любых действий, если --repo не проходит valid_repo_url.
# Параметры: нет (INSTALL_REPO).
# Результат: код 0, адрес допустим.
# Ошибки: сообщение без адреса и код 1.
require_valid_repo() {
	valid_repo_url "$INSTALL_REPO" || die "--repo: недопустимый адрес (нужен https://хост/путь, git@хост:путь.git или ssh://git@хост/путь без учётных данных)"
}

# Назначение: режим загрузчика — получить код из репозитория и передать управление его установщику.
# Параметры: нет (ORIG_ARGS, INSTALL_REPO и остальные настройки; адрес уже проверен require_valid_repo).
# Результат: exec bash INSTALL_DIR/deploy/install.sh с теми же аргументами и EMTDS_INSTALL_LOADED=1;
# сюда управление не возвращается.
# Ошибки: повторный вход в загрузчик, нет --repo, apt, клон, версия, подпись или нет compose-файла —
# сообщение, код 1.
run_loader() {
	[[ "${EMTDS_INSTALL_LOADED:-}" != 1 ]] || die "загрузчик запущен повторно из уже полученного кода: установщик клона сам не распознал свой каталог; остановка вместо бесконечного перезапуска"
	[[ -n "$INSTALL_REPO" ]] || die "режим загрузчика: задайте --repo URL репозитория"
	[[ -z "$INSTALL_SIGNERS" || -r "$INSTALL_SIGNERS" ]] || die "--allowed-signers: файл ${INSTALL_SIGNERS} не читается"
	msg "режим загрузчика: код EmTDS из ${INSTALL_REPO} в ${INSTALL_DIR}"
	apt_install git ca-certificates curl
	loader_sweep_stale
	if [[ -e "$INSTALL_DIR" || -L "$INSTALL_DIR" ]]; then
		loader_reuse_clone
	else
		loader_clone
	fi
	[[ -f "${INSTALL_DIR}/deploy/docker-compose.app.yml" ]] || die "в ${INSTALL_DIR} нет deploy/docker-compose.app.yml: передавать управление нечему"
	EMTDS_INSTALL_LOADED=1 exec bash "${INSTALL_DIR}/deploy/install.sh" "${ORIG_ARGS[@]+"${ORIG_ARGS[@]}"}"
}

# Назначение: взять общий замок установщика и агента обновлений на весь прогон (fd 9).
# Параметры: нет.
# Результат: замок удерживается до выхода или до release_run_lock.
# Ошибки: замок занят другим процессом — сообщение, код 1.
acquire_run_lock() {
	mkdir -p "$(dirname "$LOCK_FILE")"
	exec 9>"$LOCK_FILE"
	flock -n 9 || die "уже идёт установка или обновление (занят ${LOCK_FILE}); дождитесь завершения и повторите"
}

# Назначение: отпустить замок: update.sh берёт тот же файл сам и отказал бы при занятом замке.
# Параметры: нет.
# Результат: fd 9 закрыт, замок свободен.
# Ошибки: нет.
release_run_lock() {
	exec 9>&-
}

# Назначение: положить файл ключей подписи на место для агента обновлений, если он передан.
# Параметры: нет (INSTALL_SIGNERS).
# Результат: STATE_DIR/allowed_signers с правами 0644; без флага файл не создаётся, подпись выключена.
# Ошибки: файл не читается или запись не удалась, код 1.
install_allowed_signers() {
	[[ -n "$INSTALL_SIGNERS" ]] || return 0
	[[ -r "$INSTALL_SIGNERS" ]] || die "--allowed-signers: файл ${INSTALL_SIGNERS} не читается"
	write_atomic "$ALLOWED_SIGNERS" 0644 <"$INSTALL_SIGNERS"
}

# Назначение: поставить nginx пакетом, если его нет.
# Параметры: $1 — ubuntu или debian на поддерживаемой версии, иначе пусто.
# Результат: nginx доступен в PATH.
# Ошибки: неподдерживаемая система или сбой apt — сообщение, код 1.
ensure_nginx() {
	local distro=$1
	if command -v nginx >/dev/null 2>&1; then
		return 0
	fi
	if [[ -z "$distro" ]]; then
		die "nginx не найден, а пакеты ставятся только на Ubuntu 22.04, 24.04 и Debian 12: установите nginx вручную или запустите с --no-edge"
	fi
	msg "nginx не найден, ставлю пакет"
	apt_install nginx || die "не удалось установить nginx"
	command -v nginx >/dev/null 2>&1 || die "после установки nginx недоступен"
}

# Назначение: напечатать в консоль адрес первого входа и токен (только stdout, в файлы не пишется).
# Параметры: $1 — публичный IPv4.
# Результат: строки на stdout.
# Ошибки: нет.
print_first_login() {
	local public_ip=$1 token
	token=$(env_value "$TDS_DIR/.env" EMTDS_SETUP_TOKEN)
	if (( INSTALL_EDGE_APPLIED )); then
		msg "вход: https://${public_ip}/ (сертификат самоподписанный, браузер предупредит)"
	else
		msg "вход: ssh -L 8090:127.0.0.1:8090 user@host, затем http://127.0.0.1:8090/"
	fi
	if [[ -n "$token" ]]; then
		msg "токен первого входа (нужен при создании администратора, сохраните): ${token}"
	fi
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
	assert_eq "$(echo "$env_out" | wc -l | tr -d ' ')" 7 server_env_lines
	if [[ "$env_out" != *'EMTDS_ENGINE_PUBLISH=127.0.0.1:18080:8080'* ]]; then
		printf 'self-test FAIL: server.env без порта движка 18080\n'
		exit 1
	fi
	if [[ "$env_out" != *'EMTDS_ENV=production'* ]]; then
		printf 'self-test FAIL: server.env без EMTDS_ENV=production\n'
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
	printf 'CLICKHOUSE_PASSWORD=ch\nEMTDS_ADMIN_PASSWORD=\n' >"$f"
	check_env "$f" 2>/dev/null || { printf 'self-test FAIL: check_env empty admin\n'; exit 1; }
	printf 'CLICKHOUSE_PASSWORD=ch\n' >"$f"
	check_env "$f" 2>/dev/null || { printf 'self-test FAIL: check_env no admin key\n'; exit 1; }
	local box_env
	box_env=$(render_box_env 'cafebabe' 'feedface')
	if [[ "$box_env" != $'CLICKHOUSE_PASSWORD=cafebabe\nEMTDS_SETUP_TOKEN=feedface' || "$box_env" == *EMTDS_ADMIN_PASSWORD* ]]; then
		printf 'self-test FAIL: render_box_env\n'
		exit 1
	fi
	local box_file="$SELFTEST_TMP/box.env"
	printf 'CLICKHOUSE_PASSWORD=ch' >"$box_file"
	ensure_box_env "$box_file" || { printf 'self-test FAIL: ensure_box_env existing\n'; exit 1; }
	[[ "$(env_value "$box_file" CLICKHOUSE_PASSWORD)" == ch ]] || { printf 'self-test FAIL: ensure_box_env keeps password\n'; exit 1; }
	assert_eq "$(env_value "$box_file" EMTDS_SETUP_TOKEN | tr -d '0-9a-f' | wc -c | tr -d ' ')" 0 setup_token_hex
	assert_eq "$(env_value "$box_file" EMTDS_SETUP_TOKEN | wc -c | tr -d ' ')" 32 setup_token_len
	local token_before
	token_before=$(env_value "$box_file" EMTDS_SETUP_TOKEN)
	ensure_box_env "$box_file"
	assert_eq "$(env_value "$box_file" EMTDS_SETUP_TOKEN)" "$token_before" setup_token_kept
	assert_eq "$(grep -c '^EMTDS_SETUP_TOKEN=' "$box_file")" 1 setup_token_once
	printf 'CLICKHOUSE_PASSWORD=ch\nEMTDS_SETUP_TOKEN=\n' >"$box_file"
	ensure_box_env "$box_file"
	assert_eq "$(grep -c '^EMTDS_SETUP_TOKEN=' "$box_file")" 1 setup_token_empty_replaced
	assert_eq "$(env_value "$box_file" EMTDS_SETUP_TOKEN | wc -c | tr -d ' ')" 32 setup_token_empty_filled
	local bad_token good_token
	good_token=$(printf 'ab%.0s' {1..16})
	for bad_token in " ${good_token}" "${good_token} " short "${good_token^^}" "${good_token:0:31}g" "${good_token}00"; do
		printf 'CLICKHOUSE_PASSWORD=ch\nEMTDS_SETUP_TOKEN=%s\n' "$bad_token" >"$box_file"
		ensure_box_env "$box_file"
		assert_eq "$(grep -c '^EMTDS_SETUP_TOKEN=' "$box_file")" 1 "setup_token_bad_once:${bad_token}"
		[[ "$(env_value "$box_file" EMTDS_SETUP_TOKEN)" =~ ^[0123456789abcdef]{32}$ && "$(env_value "$box_file" EMTDS_SETUP_TOKEN)" != "${bad_token// /}" ]] \
			|| { printf 'self-test FAIL: setup_token_bad_replaced:%s\n' "$bad_token"; exit 1; }
	done
	local repo_ok repo_bad
	for repo_ok in 'https://example.com/owner/repo.git' 'https://example.com/owner/repo' 'git@example.com:owner/repo.git' 'ssh://git@example.com/owner/repo.git'; do
		EMTDS_INSTALL_SANDBOX= valid_repo_url "$repo_ok" || { printf 'self-test FAIL: valid_repo_url должен принять %s\n' "$repo_ok"; exit 1; }
	done
	for repo_bad in '' 'https://user:pass@example.com/o/r.git' 'https://user@example.com/o/r.git' 'http://example.com/o/r.git' 'git://example.com/o/r.git' \
		'file:///tmp/r.git' '/tmp/r.git' 'ftp://example.com/o/r.git' 'git@example.com:owner/repo' 'https://example.com' 'https://example.com/o/r .git' $'https://example.com/o/r.git\nhttps://example.org/x' '-oops'; do
		EMTDS_INSTALL_SANDBOX= valid_repo_url "$repo_bad" && { printf 'self-test FAIL: valid_repo_url должен отклонить %q\n' "$repo_bad"; exit 1; }
	done
	EMTDS_INSTALL_SANDBOX=1 valid_repo_url /tmp/r.git || { printf 'self-test FAIL: в песочнице локальный путь допустим\n'; exit 1; }
	EMTDS_INSTALL_SANDBOX=1 valid_repo_url file:///tmp/r.git && { printf 'self-test FAIL: file:// отклоняется и в песочнице\n'; exit 1; }
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

	loader_mode_needed "$SELFTEST_TMP/other/install.sh" "$SELFTEST_TMP" || { printf 'self-test FAIL: файл вне deploy/ должен включать режим загрузчика\n'; exit 1; }
	loader_mode_needed "$SELFTEST_TMP/deploy/install.sh" "$SELFTEST_TMP" || { printf 'self-test FAIL: нет docker-compose.app.yml должен включать режим загрузчика\n'; exit 1; }
	: >"$SELFTEST_TMP/deploy/docker-compose.app.yml"
	loader_mode_needed "$SELFTEST_TMP/deploy/install.sh" "$SELFTEST_TMP" && { printf 'self-test FAIL: код на месте, загрузчик не нужен\n'; exit 1; }
	loader_mode_needed "$SELFTEST_TMP/deploy/link.sh" "$SELFTEST_TMP" || { printf 'self-test FAIL: ссылка должна включать режим загрузчика\n'; exit 1; }
	loader_mode_needed "" "$SELFTEST_TMP" || { printf 'self-test FAIL: пустой путь должен включать режим загрузчика\n'; exit 1; }

	local up_out up_file
	up_out=$(render_up_sh)
	for frag in '-p emtds-infra' 'compose.infra.yml' 'compose.app.yml' 'server.env' '--wait'; do
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
		TDS_DIR="$notgit" EMTDS_COMMIT= require_build_commit || { printf 'self-test FAIL: без .git пустой коммит допустим\n'; exit 1; }
		mkdir -p "$notgit/.git"
		TDS_DIR="$notgit" EMTDS_COMMIT= require_build_commit 2>/dev/null && { printf 'self-test FAIL: при .git пустой коммит должен отклоняться\n'; exit 1; }
		TDS_DIR="$notgit" EMTDS_COMMIT=abc123 require_build_commit 2>/dev/null && { printf 'self-test FAIL: короткий коммит должен отклоняться\n'; exit 1; }
		TDS_DIR="$notgit" EMTDS_COMMIT=$(printf 'ab%.0s' {1..20}) require_build_commit || { printf 'self-test FAIL: полный коммит должен приниматься\n'; exit 1; }
	) || exit 1

	printf 'self-test ok\n'
}

# Назначение: основной сценарий установки EmTDS из клона (deploy/install.sh на месте). Только root,
# весь прогон под общим замком установщика и агента обновлений.
# Параметры: нет.
# Результат: маркер installed, сообщения об успехе, адрес и токен первого входа.
# Ошибки: любая проверка или шаг развёртывания, код 1.
run_install() {
	acquire_run_lock

	local docker_distro= docker_codename= channel watchdog_failed=0
	if [[ -f "$OS_RELEASE" ]]; then
		# shellcheck source=/dev/null
		source "$OS_RELEASE"
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
	install_allowed_signers

	cd "$TDS_DIR"
	export_build_args "${INSTALL_CHANNEL}"
	require_build_commit || exit 1
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

	if (( NO_EDGE )); then
		msg "--no-edge: nginx и apply-edge.sh пропущены, стек установлен без edge"
	else
		ensure_nginx "$docker_distro"
		if [[ -f "${OUR_DEPLOY_DIR}/apply-edge.sh" ]]; then
			if ! bash "${OUR_DEPLOY_DIR}/apply-edge.sh" "$public_ip"; then
				msg "не удалось применить конфигурацию nginx (apply-edge.sh)" >&2
				exit 1
			fi
			INSTALL_EDGE_APPLIED=1
		fi
	fi

	if [[ -f "${OUR_DEPLOY_DIR}/update.sh" ]]; then
		if [[ ! -d "${TDS_DIR}/.git" ]]; then
			msg "агент обновлений не установлен: нет git-дерева, см. update.sh --adopt"
		elif command -v systemctl >/dev/null 2>&1; then
			local update_args=(--install "--channel" "${INSTALL_CHANNEL}" "--tds-dir" "${TDS_DIR}")
			[[ -n "${INSTALL_REPO}" ]] && update_args+=(--repo "${INSTALL_REPO}")
			release_run_lock
			if ! bash "${OUR_DEPLOY_DIR}/update.sh" "${update_args[@]}"; then
				die "не удалось установить агент обновлений (update.sh --install); исправьте причину и повторите установку"
			fi
		else
			msg "агент обновлений не установлен: нет systemd, запустите update.sh --install вручную"
		fi
	fi

	date -Iseconds | write_atomic "$MARKER" 0644

	# Таймер сторожа включается последним: пока шли сборка и update.sh --install, замок на миг
	# освобождался, и сторож мог вмешаться в недоустановленный стек. Сбой не отменяет установку.
	if [[ -f "${OUR_DEPLOY_DIR}/watchdog.sh" ]] && ! bash "${OUR_DEPLOY_DIR}/watchdog.sh" --install; then
		watchdog_failed=1
		msg "не удалось включить emtds-watchdog.timer; стек установлен, включите сторож вручную: bash ${OUR_DEPLOY_DIR}/watchdog.sh --install" >&2
	fi

	if can_self_delete "$SELF" && [[ ! -d "${TDS_DIR}/.git" ]]; then
		rm -f -- "$SELF"
		msg "установщик удалён; повторный запуск install.sh заблокирован маркером ${MARKER}"
	elif [[ -d "${TDS_DIR}/.git" ]]; then
		msg "установщик не удалён: каталог кода под git, дерево не должно быть грязным; повторный запуск заблокирован маркером ${MARKER}"
	else
		msg "файл установщика не удалён (ожидался deploy/install.sh); повторный запуск заблокирован маркером ${MARKER}"
	fi

	msg "готово: движок 127.0.0.1:18080, админка 127.0.0.1:8090"
	if [[ -z "$(env_value "$TDS_DIR/.env" EMTDS_ADMIN_PASSWORD)" ]]; then
		msg "первый вход спросит логин, пароль и почту главного администратора"
	fi
	print_first_login "$public_ip"
	msg "повторный старт: ${UP_SCRIPT}"
	(( watchdog_failed == 0 )) || exit 1
}

# Назначение: вывести краткую справку по аргументам.
# Параметры: нет.
# Результат: текст usage на stderr.
# Ошибки: нет.
usage() {
	msg "использование: sudo bash install.sh --repo URL [--channel release|main] [--allowed-signers FILE] [--no-edge] | --self-test" >&2
}

# Назначение: найти прежнюю установку: каталог <OLD_ETC_ROOT>/*tds с маркером installed, кроме STATE_DIR.
# Параметры: нет.
# Результат: код 0 и путь первого найденного каталога на stdout.
# Ошибки: код 1 — прежней установки нет.
find_old_install() {
	local dir
	for dir in "$OLD_ETC_ROOT"/*tds; do
		[[ "$dir" != "$STATE_DIR" && -f "$dir/installed" ]] || continue
		printf '%s' "$dir"
		return 0
	done
	return 1
}

# Назначение: разобрать аргументы командной строки в глобальные настройки установщика.
# Параметры: аргументы командной строки.
# Результат: INSTALL_CHANNEL, INSTALL_REPO, INSTALL_SIGNERS, NO_EDGE, MODE заданы.
# Ошибки: неизвестный аргумент, аргумент без значения или канал не release/main — usage, код 2.
parse_args() {
	MODE=install
	while (( $# > 0 )); do
		case "$1" in
			--self-test) MODE=self-test; shift ;;
			--no-edge)   NO_EDGE=1; shift ;;
			--channel | --repo | --allowed-signers)
				(( $# >= 2 )) || { usage; exit 2; }
				case "$1" in
					--channel)         INSTALL_CHANNEL=$2 ;;
					--repo)            INSTALL_REPO=$2 ;;
					--allowed-signers) INSTALL_SIGNERS=$(readlink -f -- "$2" 2>/dev/null || printf '%s' "$2") ;;
				esac
				shift 2
				;;
			*)           usage; exit 2 ;;
		esac
	done
	if [[ ! "$INSTALL_CHANNEL" =~ ^(release|main)$ ]]; then
		msg "--channel: допустимо release или main" >&2
		exit 2
	fi
}

# Назначение: точка входа установщика. Порядок: аргументы, root, маркер установки, отказ на старой
# установке, затем загрузчик (кода нет) или основной сценарий (код на месте).
# Параметры: [--channel release|main] [--repo URL] [--allowed-signers FILE] [--no-edge] | --self-test.
# Результат: код выхода сценария.
# Ошибки: см. run_loader, run_install и run_self_test.
main() {
	local old_dir
	ORIG_ARGS=("$@")
	parse_args "$@"
	if [[ "$MODE" == self-test ]]; then
		run_self_test
		return
	fi
	is_root || die "нужны права root (sudo)"
	if [[ -f "$MARKER" ]]; then
		msg "EmTDS уже установлен, поднять снова: ${UP_SCRIPT}"
		msg "проверить обновления: ${UPDATE_LIB_SCRIPT} --check"
		exit 0
	fi
	if old_dir=$(find_old_install); then
		die "найдена прежняя установка в ${old_dir}: переносите её отдельно, этот установщик её не трогает"
	fi
	if [[ -n "$INSTALL_REPO" ]]; then
		require_valid_repo
	fi
	if loader_mode_needed "$SELF" "$TDS_DIR"; then
		run_loader
	else
		run_install
	fi
}

main "$@"
