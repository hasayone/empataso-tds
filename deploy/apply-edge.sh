#!/usr/bin/env bash

set -euo pipefail

EDGE_SELF=$(readlink -f "${BASH_SOURCE[0]:-}" 2>/dev/null || true)
EDGE_TDS_DIR=
if [[ -n "$EDGE_SELF" ]]; then
	EDGE_TDS_DIR=$(dirname "$(dirname "$EDGE_SELF")")
fi
EDGE_SERVER_ENV="${KUZTDS_SERVER_ENV:-/etc/kuztds/server.env}"
EDGE_NGINX_SRC="${EDGE_TDS_DIR}/deploy/nginx"
EDGE_CF_IPS="${EDGE_NGINX_SRC}/cloudflare-ips.txt"
EDGE_TEMPLATE_ADMIN="${EDGE_NGINX_SRC}/kuztds.conf"
EDGE_TEMPLATE_ADMIN_HOST="${EDGE_NGINX_SRC}/kuztds-admin-host.conf"
EDGE_TEMPLATE_ENGINE="${EDGE_NGINX_SRC}/kuztds-engine.conf"
EDGE_NGINX_KUZTDS_DIR=/etc/nginx/kuztds
EDGE_CONF_D=/etc/nginx/conf.d
EDGE_BACKUP_ROOT=/etc/kuztds/nginx-backup
EDGE_CHECK_MODE=0
EDGE_BACKUP_DIR=
EDGE_NGINX_DIR_EXISTED=0

# Print usage to stderr.
usage() {
	printf 'usage: apply-edge.sh [--check] [public-ipv4]\n' >&2
}

# Log a short message (edge_msg avoids clobbering install.sh msg when sourced).
edge_msg() {
	printf 'kuztds-apply-edge: %s\n' "$*"
}

# Read last value of KEY= from env file.
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

# Return 0 if argument is a dotted IPv4 address.
valid_ipv4() {
	local ip=$1
	[[ "$ip" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || return 1
	local o
	IFS=. read -r o1 o2 o3 o4 <<<"$ip"
	for o in "$o1" "$o2" "$o3" "$o4"; do
		(( 10#$o <= 255 )) || return 1
	done
	return 0
}

# Gateway of docker network deploy_default (compose infra project).
deploy_default_gateway() {
	docker network inspect deploy_default --format '{{range .IPAM.Config}}{{.Gateway}}{{end}}' 2>/dev/null || true
}

# Trusted proxy CIDR: docker bridge gateway /32 only.
trusted_proxies_from_docker() {
	local gw
	gw=$(deploy_default_gateway)
	if [[ -z "$gw" ]]; then
		return 1
	fi
	printf '%s/32' "$gw"
}

# CIDR for KUZTDS_*_TRUSTED_PROXIES: file values, then docker gateway /32.
resolve_trusted_proxies_cidr() {
	local file=$1 tp admin_tp
	tp=$(env_value "$file" KUZTDS_TRUSTED_PROXIES)
	if [[ -n "$tp" ]]; then
		printf '%s' "$tp"
		return 0
	fi
	admin_tp=$(env_value "$file" KUZTDS_ADMIN_TRUSTED_PROXIES)
	if [[ -n "$admin_tp" ]]; then
		printf '%s' "$admin_tp"
		return 0
	fi
	trusted_proxies_from_docker
}

# Append KEY=value to file if key is missing; preserve file mode.
append_env_if_missing() {
	local file=$1 key=$2 val=$3 mode line
	if [[ ! -f "$file" ]]; then
		edge_msg "файл ${file} не найден" >&2
		return 1
	fi
	if grep -qE "^${key}=" "$file" 2>/dev/null; then
		return 0
	fi
	mode=$(stat -c '%a' "$file" 2>/dev/null || printf '644')
	line="${key}=${val}"
	printf '%s\n' "$line" >>"$file"
	chmod "$mode" "$file"
}

# Write KEY=value when key is missing or value empty; leave non-empty values unchanged.
ensure_env_key_nonempty() {
	local file=$1 key=$2 val=$3 mode current
	if [[ ! -f "$file" ]]; then
		edge_msg "файл ${file} не найден" >&2
		return 1
	fi
	current=$(env_value "$file" "$key")
	if [[ -n "$current" ]]; then
		return 0
	fi
	mode=$(stat -c '%a' "$file" 2>/dev/null || printf '644')
	if grep -qE "^${key}=" "$file" 2>/dev/null; then
		sed -i "s|^${key}=.*|${key}=${val}|" "$file"
	else
		printf '%s\n' "${key}=${val}" >>"$file"
	fi
	chmod "$mode" "$file"
}

# Public IPv4: KUZTDS_PUBLIC_IP when it is set, otherwise curl -4.
# Prints the address. Returns 1 when the value is missing or not an IPv4 address.
# curl's failure is not printed. A non-address from curl does not count as success.
edge_public_ipv4() {
	local ip=
	if [[ -n "${KUZTDS_PUBLIC_IP:-}" ]]; then
		ip=$KUZTDS_PUBLIC_IP
	elif command -v curl >/dev/null 2>&1; then
		ip=$(curl -fsS --max-time 15 -4 https://ifconfig.me/ip 2>/dev/null || true)
	fi
	if [[ -z "$ip" ]] || ! valid_ipv4 "$ip"; then
		return 1
	fi
	printf '%s' "$ip"
}

# Ensure KUZTDS_PUBLIC_IP and KUZTDS_TRUSTED_PROXIES in server.env.
# An empty public address is filled from the environment, the argument, or curl.
# A non-empty address is left as it is.
ensure_server_env_keys() {
	local public_arg=${1:-} ip tp
	if [[ ! -f "$EDGE_SERVER_ENV" ]]; then
		edge_msg "не найден ${EDGE_SERVER_ENV}" >&2
		return 1
	fi
	ip=$(env_value "$EDGE_SERVER_ENV" KUZTDS_PUBLIC_IP)
	if [[ -z "$ip" ]]; then
		if [[ -n "${KUZTDS_PUBLIC_IP:-}" ]]; then
			ip=$KUZTDS_PUBLIC_IP
		elif [[ -n "$public_arg" ]]; then
			ip=$public_arg
		elif ! ip=$(edge_public_ipv4); then
			edge_msg "задайте KUZTDS_PUBLIC_IP в ${EDGE_SERVER_ENV}, переменной окружения или аргументом" >&2
			return 1
		fi
		if ! valid_ipv4 "$ip"; then
			edge_msg "KUZTDS_PUBLIC_IP должен быть IPv4: ${ip}" >&2
			return 1
		fi
		ensure_env_key_nonempty "$EDGE_SERVER_ENV" KUZTDS_PUBLIC_IP "$ip"
	fi
	if ! valid_ipv4 "$ip"; then
		edge_msg "KUZTDS_PUBLIC_IP должен быть IPv4: ${ip}" >&2
		return 1
	fi

	tp=$(resolve_trusted_proxies_cidr "$EDGE_SERVER_ENV") || {
		edge_msg "не удалось получить gateway deploy_default для KUZTDS_TRUSTED_PROXIES" >&2
		return 1
	}
	ensure_env_key_nonempty "$EDGE_SERVER_ENV" KUZTDS_TRUSTED_PROXIES "$tp"
	tp=$(env_value "$EDGE_SERVER_ENV" KUZTDS_TRUSTED_PROXIES)
	ensure_env_key_nonempty "$EDGE_SERVER_ENV" KUZTDS_ADMIN_TRUSTED_PROXIES "$tp"
}

# Parse host:port from KUZTDS_ENGINE_PUBLISH (e.g. 127.0.0.1:18080:8080).
engine_upstream_from_publish() {
	local publish=${1:-127.0.0.1:18080:8080} host port rest
	if [[ "$publish" == *:*:* ]]; then
		host=${publish%%:*}
		rest=${publish#*:}
		port=${rest%%:*}
		printf '%s:%s' "$host" "$port"
		return 0
	fi
	if [[ "$publish" == *:* ]]; then
		port=${publish%%:*}
		printf '127.0.0.1:%s' "$port"
		return 0
	fi
	printf '127.0.0.1:18080'
}

# Build cloudflare-realip.conf and cloudflare-geo.conf in DEST_DIR.
render_cloudflare_snippets() {
	local dest=$1 line cidr
	: >"${dest}/cloudflare-realip.conf"
	: >"${dest}/cloudflare-geo.conf"
	while IFS= read -r line || [[ -n "$line" ]]; do
		line=${line%%#*}
		line=$(echo "$line" | tr -d '[:space:]')
		[[ -z "$line" ]] && continue
		cidr=$line
		printf 'set_real_ip_from %s;\n' "$cidr" >>"${dest}/cloudflare-realip.conf"
		printf '%s 1;\n' "$cidr" >>"${dest}/cloudflare-geo.conf"
	done <"$EDGE_CF_IPS"
}

# Substitute placeholders in nginx templates into DEST_DIR.
render_nginx_configs() {
	local dest=$1 public_ip=$2 upstream=$3
	sed "s/__PUBLIC_IP__/${public_ip}/g" "$EDGE_TEMPLATE_ADMIN" >"${dest}/kuztds.conf"
	sed "s/__ENGINE_UPSTREAM__/${upstream}/g" "$EDGE_TEMPLATE_ADMIN_HOST" >"${dest}/kuztds-admin-host.conf"
	sed "s/__ENGINE_UPSTREAM__/${upstream}/g" "$EDGE_TEMPLATE_ENGINE" >"${dest}/kuztds-engine.conf"
}

# Render all edge artifacts into DEST_DIR; print paths on success.
render_all() {
	local dest=$1 public_ip upstream publish
	public_ip=$(env_value "$EDGE_SERVER_ENV" KUZTDS_PUBLIC_IP)
	publish=$(env_value "$EDGE_SERVER_ENV" KUZTDS_ENGINE_PUBLISH)
	if [[ -z "$publish" ]]; then
		publish=127.0.0.1:18080:8080
	fi
	upstream=$(engine_upstream_from_publish "$publish")
	render_cloudflare_snippets "$dest"
	render_nginx_configs "$dest" "$public_ip" "$upstream"
}

# Copy rendered tree into live nginx paths; sets EDGE_BACKUP_DIR after backup, before writes.
install_rendered() {
	local src=$1 ts backup_dir f
	EDGE_NGINX_DIR_EXISTED=0
	if [[ -d "$EDGE_NGINX_KUZTDS_DIR" ]]; then
		EDGE_NGINX_DIR_EXISTED=1
	fi
	ts=$(date -u +%Y%m%dT%H%M%SZ)
	backup_dir="${EDGE_BACKUP_ROOT}/${ts}"
	install -d -m 0755 "$backup_dir"
	install -d -m 0755 "${backup_dir}/conf.d"
	install -d -m 0755 "${backup_dir}/kuztds"

	for f in "${EDGE_CONF_D}"/kuztds*.conf; do
		[[ -e "$f" ]] || continue
		cp -a "$f" "${backup_dir}/conf.d/"
	done
	if [[ "$EDGE_NGINX_DIR_EXISTED" -eq 1 ]]; then
		cp -a "${EDGE_NGINX_KUZTDS_DIR}/." "${backup_dir}/kuztds/" 2>/dev/null || true
	fi

	EDGE_BACKUP_DIR=$backup_dir

	install -d -m 0755 "$EDGE_NGINX_KUZTDS_DIR"
	install -m 0644 "${src}/cloudflare-realip.conf" "${EDGE_NGINX_KUZTDS_DIR}/cloudflare-realip.conf"
	install -m 0644 "${src}/cloudflare-geo.conf" "${EDGE_NGINX_KUZTDS_DIR}/cloudflare-geo.conf"
	install -m 0644 "${src}/kuztds.conf" "${EDGE_CONF_D}/kuztds.conf"
	install -m 0644 "${src}/kuztds-admin-host.conf" "${EDGE_CONF_D}/kuztds-admin-host.conf"
	install -m 0644 "${src}/kuztds-engine.conf" "${EDGE_CONF_D}/kuztds-engine.conf"
}

# Restore nginx files from backup; only touches kuztds* conf.d and /etc/nginx/kuztds/.
restore_from_backup() {
	local backup_dir=$1 nginx_dir_existed=${2:-$EDGE_NGINX_DIR_EXISTED} f base
	[[ -n "$backup_dir" && -d "$backup_dir" ]] || return 0

	for f in "${EDGE_CONF_D}"/kuztds*.conf; do
		[[ -e "$f" ]] || continue
		base=$(basename "$f")
		if [[ ! -f "${backup_dir}/conf.d/${base}" ]]; then
			rm -f "$f"
		fi
	done
	if [[ -d "${backup_dir}/conf.d" ]]; then
		for f in "${backup_dir}/conf.d/"*; do
			[[ -e "$f" ]] || continue
			install -m 0644 "$f" "${EDGE_CONF_D}/$(basename "$f")"
		done
	fi

	if [[ "$nginx_dir_existed" -eq 1 ]]; then
		install -d -m 0755 "$EDGE_NGINX_KUZTDS_DIR"
		if [[ -d "${backup_dir}/kuztds" ]]; then
			find "$EDGE_NGINX_KUZTDS_DIR" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
			cp -a "${backup_dir}/kuztds/." "$EDGE_NGINX_KUZTDS_DIR/" 2>/dev/null || true
		fi
	else
		rm -rf "$EDGE_NGINX_KUZTDS_DIR"
	fi
}

# --check: render to temp and dump to stdout.
run_check() {
	local tmp public_ip tp admin_tp
	tmp=$(mktemp -d)
	if ! ensure_server_env_keys "${1:-}"; then
		rm -rf "$tmp"
		return 1
	fi
	render_all "$tmp"
	public_ip=$(env_value "$EDGE_SERVER_ENV" KUZTDS_PUBLIC_IP)
	tp=$(env_value "$EDGE_SERVER_ENV" KUZTDS_TRUSTED_PROXIES)
	admin_tp=$(env_value "$EDGE_SERVER_ENV" KUZTDS_ADMIN_TRUSTED_PROXIES)
	edge_msg "check mode: public_ip=${public_ip} trusted_proxies=${tp} admin_trusted_proxies=${admin_tp}"
	for f in cloudflare-realip.conf cloudflare-geo.conf kuztds.conf kuztds-admin-host.conf kuztds-engine.conf; do
		printf '===== %s =====\n' "$f"
		cat "${tmp}/${f}"
	done
	rm -rf "$tmp"
}

# Full install with nginx -t and reload.
run_apply() {
	local tmp backup_dir public_ip tp admin_tp
	if [[ $(id -u) -ne 0 ]]; then
		edge_msg "нужны права root" >&2
		return 1
	fi
	for f in "$EDGE_CF_IPS" "$EDGE_TEMPLATE_ADMIN" "$EDGE_TEMPLATE_ADMIN_HOST" "$EDGE_TEMPLATE_ENGINE"; do
		if [[ ! -f "$f" ]]; then
			edge_msg "не найден шаблон: ${f}" >&2
			return 1
		fi
	done
	ensure_server_env_keys "${1:-}" || return 1
	tmp=$(mktemp -d)
	backup_dir=
	EDGE_NGINX_DIR_EXISTED=0
	trap 'restore_from_backup "${EDGE_BACKUP_DIR:-}"; rm -rf "${tmp}"' ERR
	render_all "$tmp"
	install_rendered "$tmp"
	backup_dir=$EDGE_BACKUP_DIR
	if ! nginx -t; then
		edge_msg "nginx -t failed; restoring backup ${backup_dir}" >&2
		restore_from_backup "$backup_dir"
		rm -rf "$tmp"
		trap - ERR
		return 1
	fi
	systemctl reload nginx
	trap - ERR
	rm -rf "$tmp"
	public_ip=$(env_value "$EDGE_SERVER_ENV" KUZTDS_PUBLIC_IP)
	tp=$(env_value "$EDGE_SERVER_ENV" KUZTDS_TRUSTED_PROXIES)
	admin_tp=$(env_value "$EDGE_SERVER_ENV" KUZTDS_ADMIN_TRUSTED_PROXIES)
	edge_msg "ok: public_ip=${public_ip} trusted_proxies=${tp} admin_trusted_proxies=${admin_tp} backup=${backup_dir}"
}

main() {
	local public_arg=
	while [[ $# -gt 0 ]]; do
		case "$1" in
			--check)
				EDGE_CHECK_MODE=1
				shift
				;;
			-h | --help)
				usage
				exit 0
				;;
			-*)
				usage
				exit 2
				;;
			*)
				public_arg=$1
				shift
				;;
		esac
	done
	if [[ "$EDGE_CHECK_MODE" -eq 1 ]]; then
		run_check "$public_arg"
	else
		run_apply "$public_arg"
	fi
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
	main "$@"
fi
