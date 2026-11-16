#!/usr/bin/env bash

# -E: ловушка ERR (откат nginx) срабатывает и внутри вложенных функций.
set -Eeuo pipefail

EDGE_SELF=$(readlink -f "${BASH_SOURCE[0]:-}" 2>/dev/null || true)
EDGE_TDS_DIR=
if [[ -n "$EDGE_SELF" ]]; then
	EDGE_TDS_DIR=$(dirname "$(dirname "$EDGE_SELF")")
fi
# State directory: explicit EMTDS_/KUZTDS_INSTALL_STATE_DIR, then /etc/emtds,
# then the pre-rename /etc/kuztds when only that one exists.
EDGE_STATE_DIR="${EMTDS_INSTALL_STATE_DIR:-${KUZTDS_INSTALL_STATE_DIR:-}}"
if [[ -z "$EDGE_STATE_DIR" ]]; then
	EDGE_STATE_DIR=/etc/emtds
	if [[ ! -e /etc/emtds && -d /etc/kuztds ]]; then
		EDGE_STATE_DIR=/etc/kuztds
	fi
fi
EDGE_SERVER_ENV="${EMTDS_SERVER_ENV:-${KUZTDS_SERVER_ENV:-${EDGE_STATE_DIR}/server.env}}"
EDGE_NGINX_SRC="${EDGE_TDS_DIR}/deploy/nginx"
EDGE_CF_IPS="${EDGE_NGINX_SRC}/cloudflare-ips.txt"
EDGE_TEMPLATE_ADMIN="${EDGE_NGINX_SRC}/emtds.conf"
EDGE_TEMPLATE_ADMIN_HOST="${EDGE_NGINX_SRC}/emtds-admin-host.conf"
EDGE_TEMPLATE_ENGINE="${EDGE_NGINX_SRC}/emtds-engine.conf"
EDGE_NGINX_EMTDS_DIR=/etc/nginx/emtds
# Snippet directory and conf.d prefix written before the rename; apply removes them with a backup.
EDGE_NGINX_LEGACY_DIR=/etc/nginx/kuztds
EDGE_LEGACY_PREFIX=kuztds
EDGE_CONF_D=/etc/nginx/conf.d
EDGE_BACKUP_ROOT="${EDGE_STATE_DIR}/nginx-backup"
EDGE_CHECK_MODE=0
EDGE_BACKUP_DIR=
EDGE_NGINX_DIR_EXISTED=0
# Главный конфиг nginx; правится только с --tune-events (путь переопределяется для проверок).
EDGE_NGINX_CONF="${EMTDS_NGINX_CONF:-/etc/nginx/nginx.conf}"
EDGE_TUNE_EVENTS=0
# Нижние пороги --tune-events: значения выше порога остаются как есть.
EDGE_WORKER_CONNECTIONS_MIN=4096
EDGE_WORKER_RLIMIT_NOFILE_MIN=16384

# Print usage to stderr.
usage() {
	printf 'usage: apply-edge.sh [--check] [--tune-events] [public-ipv4]\n' >&2
	printf '  --check        показать отрендеренные конфиги и текущие worker_connections / worker_rlimit_nofile, ничего не записывать\n' >&2
	printf '  --tune-events  поднять в /etc/nginx/nginx.conf worker_connections до %s и worker_rlimit_nofile до %s (не меньше, существующие большие значения не трогать)\n' "$EDGE_WORKER_CONNECTIONS_MIN" "$EDGE_WORKER_RLIMIT_NOFILE_MIN" >&2
}

# Log a short message (edge_msg avoids clobbering install.sh msg when sourced).
edge_msg() {
	printf 'emtds-apply-edge: %s\n' "$*"
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

# Read EMTDS_<name> from env file; an empty value falls back to the pre-rename KUZTDS_<name>.
env_value_compat() {
	local file=$1 name=$2 val
	val=$(env_value "$file" "EMTDS_${name}")
	if [[ -z "$val" ]]; then
		val=$(env_value "$file" "KUZTDS_${name}")
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

# CIDR for EMTDS_*_TRUSTED_PROXIES: file values, then docker gateway /32.
resolve_trusted_proxies_cidr() {
	local file=$1 tp admin_tp
	tp=$(env_value_compat "$file" TRUSTED_PROXIES)
	if [[ -n "$tp" ]]; then
		printf '%s' "$tp"
		return 0
	fi
	admin_tp=$(env_value_compat "$file" ADMIN_TRUSTED_PROXIES)
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
	if [[ -z "$current" && "$key" == EMTDS_* ]]; then
		current=$(env_value "$file" "KUZTDS_${key#EMTDS_}")
	fi
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

# Public IPv4: EMTDS_PUBLIC_IP when it is set, otherwise curl -4.
# Prints the address. Returns 1 when the value is missing or not an IPv4 address.
# curl's failure is not printed. A non-address from curl does not count as success.
edge_public_ipv4() {
	local ip=
	if [[ -n "${EMTDS_PUBLIC_IP:-${KUZTDS_PUBLIC_IP:-}}" ]]; then
		ip=${EMTDS_PUBLIC_IP:-${KUZTDS_PUBLIC_IP:-}}
	elif command -v curl >/dev/null 2>&1; then
		ip=$(curl -fsS --max-time 15 -4 https://ifconfig.me/ip 2>/dev/null || true)
	fi
	if [[ -z "$ip" ]] || ! valid_ipv4 "$ip"; then
		return 1
	fi
	printf '%s' "$ip"
}

# Ensure EMTDS_PUBLIC_IP and EMTDS_TRUSTED_PROXIES in server.env.
# An empty public address is filled from the environment, the argument, or curl.
# A non-empty address is left as it is.
ensure_server_env_keys() {
	local public_arg=${1:-} ip tp
	if [[ ! -f "$EDGE_SERVER_ENV" ]]; then
		edge_msg "не найден ${EDGE_SERVER_ENV}" >&2
		return 1
	fi
	ip=$(env_value_compat "$EDGE_SERVER_ENV" PUBLIC_IP)
	if [[ -z "$ip" ]]; then
		if [[ -n "${EMTDS_PUBLIC_IP:-${KUZTDS_PUBLIC_IP:-}}" ]]; then
			ip=${EMTDS_PUBLIC_IP:-${KUZTDS_PUBLIC_IP:-}}
		elif [[ -n "$public_arg" ]]; then
			ip=$public_arg
		elif ! ip=$(edge_public_ipv4); then
			edge_msg "задайте EMTDS_PUBLIC_IP в ${EDGE_SERVER_ENV}, переменной окружения или аргументом" >&2
			return 1
		fi
		if ! valid_ipv4 "$ip"; then
			edge_msg "EMTDS_PUBLIC_IP должен быть IPv4: ${ip}" >&2
			return 1
		fi
		ensure_env_key_nonempty "$EDGE_SERVER_ENV" EMTDS_PUBLIC_IP "$ip"
	fi
	if ! valid_ipv4 "$ip"; then
		edge_msg "EMTDS_PUBLIC_IP должен быть IPv4: ${ip}" >&2
		return 1
	fi

	tp=$(resolve_trusted_proxies_cidr "$EDGE_SERVER_ENV") || {
		edge_msg "не удалось получить gateway deploy_default для EMTDS_TRUSTED_PROXIES" >&2
		return 1
	}
	ensure_env_key_nonempty "$EDGE_SERVER_ENV" EMTDS_TRUSTED_PROXIES "$tp"
	tp=$(env_value_compat "$EDGE_SERVER_ENV" TRUSTED_PROXIES)
	ensure_env_key_nonempty "$EDGE_SERVER_ENV" EMTDS_ADMIN_TRUSTED_PROXIES "$tp"
}

# Parse host:port from EMTDS_ENGINE_PUBLISH (e.g. 127.0.0.1:18080:8080).
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
	sed -e "s/__PUBLIC_IP__/${public_ip}/g" -e "s|__STATE_DIR__|${EDGE_STATE_DIR}|g" "$EDGE_TEMPLATE_ADMIN" >"${dest}/emtds.conf"
	sed -e "s/__ENGINE_UPSTREAM__/${upstream}/g" -e "s|__STATE_DIR__|${EDGE_STATE_DIR}|g" "$EDGE_TEMPLATE_ADMIN_HOST" >"${dest}/emtds-admin-host.conf"
	sed -e "s/__ENGINE_UPSTREAM__/${upstream}/g" -e "s|__STATE_DIR__|${EDGE_STATE_DIR}|g" "$EDGE_TEMPLATE_ENGINE" >"${dest}/emtds-engine.conf"
}

# Render all edge artifacts into DEST_DIR; print paths on success.
render_all() {
	local dest=$1 public_ip upstream publish
	public_ip=$(env_value_compat "$EDGE_SERVER_ENV" PUBLIC_IP)
	publish=$(env_value_compat "$EDGE_SERVER_ENV" ENGINE_PUBLISH)
	if [[ -z "$publish" ]]; then
		publish=127.0.0.1:18080:8080
	fi
	upstream=$(engine_upstream_from_publish "$publish")
	render_cloudflare_snippets "$dest"
	render_nginx_configs "$dest" "$public_ip" "$upstream"
}

# Run MODE over nginx.conf SRC with a tokenizer that skips comments and quoted strings
# (braces, semicolons and # inside quotes do not count) and tracks block depth.
# Modes (arg 4, default render):
#   render    print SRC with worker_connections (inside events) >= WC_MIN and worker_rlimit_nofile
#             (main context) >= NOFILE_MIN: larger values stay, a missing directive is added
#             (nofile before the events block, connections inside it);
#   values    print worker_connections=N and worker_rlimit_nofile=N ("не задано" when absent);
#   includes  print include paths of the main context and of events.
# Args: src wc_min nofile_min [mode]. Writes the result to stdout.
# Returns 3 with a message on stderr when the file cannot be parsed unambiguously:
# unbalanced braces or quotes, no or several events blocks, a directive set twice
# or with a value that is not a number.
render_nginx_events() {
	local src=$1 wc_min=$2 nofile_min=$3 mode=${4:-render}
	awk -v WC="$wc_min" -v NOFILE="$nofile_min" -v MODE="$mode" '
	function fail(msg) {
		print "emtds-apply-edge: nginx.conf: " msg > "/dev/stderr"
		exit 3
	}
	function sanitize(s,   n, k, ch, out, prev) {
		n = length(s); out = ""; prev = " "
		for (k = 1; k <= n; k++) {
			ch = substr(s, k, 1)
			if (q != "") {
				if (ch == "\\" && k < n) { out = out "xx"; k++; prev = "x"; continue }
				out = out (ch == q ? ch : "x")
				if (ch == q) q = ""
				prev = ch
				continue
			}
			if (ch == "#" && prev ~ "[ \t\r;{}]") {
				for (; k <= n; k++) out = out " "
				break
			}
			if (ch == "\"" || ch == "\047") q = ch
			out = out ch
			prev = ch
		}
		return out
	}
	function stmt(   name) {
		name = tx[1]
		if (name == "worker_rlimit_nofile" && depth == 0) {
			nf_n++; nf_line = tl[2]; nf_col = tc[2]; nf_val = tx[2]; nf_args = ntok
		} else if (name == "worker_connections" && depth == 1 && stk[1] == "events") {
			wc_n++; wc_line = tl[2]; wc_col = tc[2]; wc_val = tx[2]; wc_args = ntok
		} else if (name == "include" && ntok == 2 && (depth == 0 || (depth == 1 && stk[1] == "events"))) {
			inc_list = inc_list unquote(substr(L[tl[2]], tc[2], length(tx[2]))) "\n"
		}
	}
	function unquote(s) {
		if (s ~ /^".*"$/ || s ~ /^\047.*\047$/) s = substr(s, 2, length(s) - 2)
		return s
	}
	function open_block(ln, col,   name) {
		name = ntok > 0 ? tx[1] : ""
		if (depth == 0 && name == "events") {
			ev_n++; ev_tl = tl[1]; ev_bl = ln; ev_bc = col
		}
		depth++
		stk[depth] = name
	}
	function close_block(ln, col) {
		if (depth == 0) fail("лишняя закрывающая скобка в строке " ln)
		if (depth == 1 && stk[1] == "events") { ev_cl = ln }
		depth--
	}
	function edit(ln, col, len, txt) {
		ne[ln]++
		ec[ln, ne[ln]] = col; el[ln, ne[ln]] = len; et[ln, ne[ln]] = txt
	}
	function emit(ln,   s, r, t, b) {
		s = L[ln]
		printf "%s", pre[ln]
		for (t = 1; t <= ne[ln]; t++) used[ln, t] = 0
		for (r = 1; r <= ne[ln]; r++) {
			b = 0
			for (t = 1; t <= ne[ln]; t++)
				if (!used[ln, t] && (b == 0 || ec[ln, t] > ec[ln, b])) b = t
			used[ln, b] = 1
			s = substr(s, 1, ec[ln, b] - 1) et[ln, b] substr(s, ec[ln, b] + el[ln, b])
		}
		print s
	}
	{ L[NR] = $0 }
	END {
		n = NR
		for (i = 1; i <= n; i++) S[i] = sanitize(L[i])
		if (q != "") fail("кавычка не закрыта")
		for (i = 1; i <= n; i++) {
			m = length(S[i])
			for (j = 1; j <= m + 1; j++) {
				ch = j <= m ? substr(S[i], j, 1) : " "
				if (ch ~ "[ \t\r]") { intok = 0; continue }
				if (ch == ";") { intok = 0; if (ntok > 0) stmt(); ntok = 0; continue }
				if (ch == "{") { intok = 0; open_block(i, j); ntok = 0; continue }
				if (ch == "}") { intok = 0; close_block(i, j); ntok = 0; continue }
				if (!intok) { ntok++; tl[ntok] = i; tc[ntok] = j; tx[ntok] = ""; intok = 1 }
				tx[ntok] = tx[ntok] ch
			}
		}
		if (depth != 0) fail("скобки { } не сбалансированы")
		if (MODE == "includes") { printf "%s", inc_list; exit 0 }
		if (ev_n == 0) fail("блок events не найден (возможно, он в подключаемом файле)")
		if (ev_n > 1) fail("найдено несколько блоков events")
		if (nf_n > 1) fail("worker_rlimit_nofile задан несколько раз")
		if (wc_n > 1) fail("worker_connections задан в events несколько раз")
		if (nf_n == 1 && (nf_args != 2 || nf_val !~ /^[0-9]+$/)) fail("worker_rlimit_nofile: значение не число: " nf_val)
		if (wc_n == 1 && (wc_args != 2 || wc_val !~ /^[0-9]+$/)) fail("worker_connections: значение не число: " wc_val)
		if (MODE == "values") {
			print "worker_connections=" (wc_n ? wc_val : "не задано")
			print "worker_rlimit_nofile=" (nf_n ? nf_val : "не задано")
			exit 0
		}
		if (nf_n == 1) {
			if (nf_val + 0 < NOFILE + 0) edit(nf_line, nf_col, length(nf_val), NOFILE)
		} else {
			pre[ev_tl] = pre[ev_tl] "worker_rlimit_nofile " NOFILE ";\n"
		}
		if (wc_n == 1) {
			if (wc_val + 0 < WC + 0) edit(wc_line, wc_col, length(wc_val), WC)
		} else if (ev_cl > ev_bl) {
			pre[ev_cl] = pre[ev_cl] "    worker_connections " WC ";\n"
		} else {
			edit(ev_bl, ev_bc + 1, 0, " worker_connections " WC ";")
		}
		for (i = 1; i <= n; i++) emit(i)
	}
	' "$src"
}

# Print files other than nginx.conf REAL (its resolved path) that set worker_connections or
# worker_rlimit_nofile. Both directives live only in the main context and in events, so a hit
# in another file means an include. Source: the nginx -T dump when it works; otherwise the include
# paths of the main context and of events (one level, globs relative to the nginx.conf directory).
# Prints nothing when there are no hits.
nginx_foreign_directive_files() {
	local real=$1 dump f inc base re
	re='(^|[[:space:];{])(worker_connections|worker_rlimit_nofile)[[:space:]]+[^[:space:];]'
	if command -v nginx >/dev/null 2>&1 && dump=$(nginx -T 2>/dev/null); then
		while IFS= read -r f; do
			[[ "$(readlink -f "$f" 2>/dev/null || true)" == "$real" ]] || printf '%s\n' "$f"
		done < <(printf '%s\n' "$dump" | awk -v re='(^|[ \t;{])(worker_connections|worker_rlimit_nofile)[ \t]+[^ \t;]' '
			/^# configuration file .*:$/ { f = $0; sub(/^# configuration file /, "", f); sub(/:$/, "", f); next }
			{ c = $0; sub(/#.*/, "", c); if (f != "" && c ~ re) hits[f] = 1 }
			END { for (k in hits) print k }')
		return 0
	fi
	base=$(dirname "$EDGE_NGINX_CONF")
	while IFS= read -r inc; do
		[[ -n "$inc" ]] || continue
		[[ "$inc" == /* ]] || inc="${base}/${inc}"
		# shellcheck disable=SC2086 # шаблон include раскрывается намеренно
		for f in $inc; do
			[[ -f "$f" ]] || continue
			if [[ -n "$(sed -E 's/#.*//' "$f" | grep -E "$re" || true)" ]]; then
				printf '%s\n' "$f"
			fi
		done
	done < <(render_nginx_events "$real" 0 0 includes)
}

# Return 0 when running as root. A separate function so tests can replace it.
edge_is_root() {
	[[ $(id -u) -eq 0 ]]
}

# Plan the nginx.conf change: write the tuned text to OUT without touching nginx.conf.
# Refuses (message on stderr, return 1) when nginx.conf is missing, the directives are set in
# included files, or the file cannot be parsed unambiguously.
plan_nginx_events() {
	local out=$1 real hits
	if [[ ! -f "$EDGE_NGINX_CONF" ]]; then
		edge_msg "не найден ${EDGE_NGINX_CONF}" >&2
		return 1
	fi
	real=$(readlink -f "$EDGE_NGINX_CONF")
	hits=$(nginx_foreign_directive_files "$real")
	if [[ -n "$hits" ]]; then
		edge_msg "worker_connections/worker_rlimit_nofile заданы в подключаемых файлах: ${hits//$'\n'/, }; --tune-events остановлен, правьте их вручную" >&2
		return 1
	fi
	render_nginx_events "$real" "$EDGE_WORKER_CONNECTIONS_MIN" "$EDGE_WORKER_RLIMIT_NOFILE_MIN" >"$out"
}

# Apply plan_nginx_events to EDGE_NGINX_CONF. A symlink is followed and the target is edited.
# Does nothing when nothing changes (the second run leaves the file untouched). The target is
# replaced through a sibling temp file with owner and mode kept; temp files are removed on failure.
# Returns 1 when the plan is refused or the write fails; the caller rolls back from the backup.
tune_nginx_events() {
	local real new next
	new=$(mktemp) || return 1
	if ! plan_nginx_events "$new"; then
		rm -f "$new"
		return 1
	fi
	real=$(readlink -f "$EDGE_NGINX_CONF")
	if cmp -s "$new" "$real"; then
		rm -f "$new"
		return 0
	fi
	next="${real}.emtds-new"
	if ! cp -a "$real" "$next" || ! cat "$new" >"$next" || ! mv -f "$next" "$real"; then
		edge_msg "не удалось записать ${real}" >&2
		rm -f "$new" "$next"
		return 1
	fi
	rm -f "$new"
	edge_msg "nginx.conf: worker_connections>=${EDGE_WORKER_CONNECTIONS_MIN}, worker_rlimit_nofile>=${EDGE_WORKER_RLIMIT_NOFILE_MIN}"
}

# Print the value of KEY from "key=value" lines in TEXT.
edge_kv() {
	local text=$1 key=$2
	sed -n "s/^${key}=//p" <<<"$text"
}

# --check: print current worker_connections (events) and worker_rlimit_nofile (main context) and
# what --tune-events would change or refuse. TMP is a scratch directory.
# A missing or unparsable nginx.conf is reported, not an error.
report_nginx_events() {
	local tmp=$1 cur new hits real
	if [[ ! -f "$EDGE_NGINX_CONF" ]]; then
		edge_msg "check mode: ${EDGE_NGINX_CONF} не найден"
		return 0
	fi
	real=$(readlink -f "$EDGE_NGINX_CONF")
	if ! cur=$(render_nginx_events "$real" 0 0 values 2>&1); then
		printf '%s\n' "$cur"
		edge_msg "check mode: --tune-events остановится на этой ошибке"
		return 0
	fi
	edge_msg "check mode: nginx.conf worker_connections=$(edge_kv "$cur" worker_connections) worker_rlimit_nofile=$(edge_kv "$cur" worker_rlimit_nofile)"
	hits=$(nginx_foreign_directive_files "$real")
	if [[ -n "$hits" ]]; then
		edge_msg "check mode: директивы заданы в подключаемых файлах: ${hits//$'\n'/, }; --tune-events остановится"
		return 0
	fi
	render_nginx_events "$real" "$EDGE_WORKER_CONNECTIONS_MIN" "$EDGE_WORKER_RLIMIT_NOFILE_MIN" >"${tmp}/nginx.conf"
	if cmp -s "${tmp}/nginx.conf" "$real"; then
		edge_msg "check mode: --tune-events ничего не изменит"
		return 0
	fi
	new=$(render_nginx_events "${tmp}/nginx.conf" 0 0 values)
	edge_msg "check mode: --tune-events изменит worker_connections $(edge_kv "$cur" worker_connections) -> $(edge_kv "$new" worker_connections), worker_rlimit_nofile $(edge_kv "$cur" worker_rlimit_nofile) -> $(edge_kv "$new" worker_rlimit_nofile)"
}

# Copy rendered tree into live nginx paths; sets EDGE_BACKUP_DIR after backup, before writes.
# Pre-rename kuztds*.conf and /etc/nginx/kuztds/ go into the same backup and are removed,
# otherwise nginx would see two default 443 servers.
install_rendered() {
	local src=$1 ts backup_dir f
	EDGE_NGINX_DIR_EXISTED=0
	if [[ -d "$EDGE_NGINX_EMTDS_DIR" ]]; then
		EDGE_NGINX_DIR_EXISTED=1
	fi
	ts=$(date -u +%Y%m%dT%H%M%SZ)
	backup_dir="${EDGE_BACKUP_ROOT}/${ts}"
	install -d -m 0755 "$backup_dir"
	install -d -m 0755 "${backup_dir}/conf.d"
	install -d -m 0755 "${backup_dir}/emtds"

	for f in "${EDGE_CONF_D}"/emtds*.conf "${EDGE_CONF_D}/${EDGE_LEGACY_PREFIX}"*.conf; do
		[[ -e "$f" ]] || continue
		cp -a "$f" "${backup_dir}/conf.d/"
	done
	if [[ "$EDGE_NGINX_DIR_EXISTED" -eq 1 ]]; then
		cp -a "${EDGE_NGINX_EMTDS_DIR}/." "${backup_dir}/emtds/" 2>/dev/null || true
	fi
	if [[ -d "$EDGE_NGINX_LEGACY_DIR" ]]; then
		install -d -m 0755 "${backup_dir}/legacy"
		cp -a "${EDGE_NGINX_LEGACY_DIR}/." "${backup_dir}/legacy/" 2>/dev/null || true
	fi
	if [[ "$EDGE_TUNE_EVENTS" -eq 1 && -f "$EDGE_NGINX_CONF" ]]; then
		cp -aL "$EDGE_NGINX_CONF" "${backup_dir}/nginx.conf"
	fi

	EDGE_BACKUP_DIR=$backup_dir

	install -d -m 0755 "$EDGE_NGINX_EMTDS_DIR"
	install -m 0644 "${src}/cloudflare-realip.conf" "${EDGE_NGINX_EMTDS_DIR}/cloudflare-realip.conf"
	install -m 0644 "${src}/cloudflare-geo.conf" "${EDGE_NGINX_EMTDS_DIR}/cloudflare-geo.conf"
	install -m 0644 "${src}/emtds.conf" "${EDGE_CONF_D}/emtds.conf"
	install -m 0644 "${src}/emtds-admin-host.conf" "${EDGE_CONF_D}/emtds-admin-host.conf"
	install -m 0644 "${src}/emtds-engine.conf" "${EDGE_CONF_D}/emtds-engine.conf"
	rm -f "${EDGE_CONF_D}/${EDGE_LEGACY_PREFIX}"*.conf
	rm -rf "$EDGE_NGINX_LEGACY_DIR"
}

# Restore nginx files from backup; only touches emtds*/kuztds* conf.d, /etc/nginx/{emtds,kuztds}/
# and nginx.conf when the backup holds a copy of it (made only with --tune-events).
restore_from_backup() {
	local backup_dir=$1 nginx_dir_existed=${2:-$EDGE_NGINX_DIR_EXISTED} f base
	[[ -n "$backup_dir" && -d "$backup_dir" ]] || return 0

	for f in "${EDGE_CONF_D}"/emtds*.conf "${EDGE_CONF_D}/${EDGE_LEGACY_PREFIX}"*.conf; do
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
		install -d -m 0755 "$EDGE_NGINX_EMTDS_DIR"
		if [[ -d "${backup_dir}/emtds" ]]; then
			find "$EDGE_NGINX_EMTDS_DIR" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
			cp -a "${backup_dir}/emtds/." "$EDGE_NGINX_EMTDS_DIR/" 2>/dev/null || true
		fi
	else
		rm -rf "$EDGE_NGINX_EMTDS_DIR"
	fi
	if [[ -d "${backup_dir}/legacy" ]]; then
		install -d -m 0755 "$EDGE_NGINX_LEGACY_DIR"
		cp -a "${backup_dir}/legacy/." "$EDGE_NGINX_LEGACY_DIR/" 2>/dev/null || true
	fi
	if [[ -f "${backup_dir}/nginx.conf" ]]; then
		cp -a "${backup_dir}/nginx.conf" "$(readlink -f "$EDGE_NGINX_CONF")"
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
	public_ip=$(env_value_compat "$EDGE_SERVER_ENV" PUBLIC_IP)
	tp=$(env_value_compat "$EDGE_SERVER_ENV" TRUSTED_PROXIES)
	admin_tp=$(env_value_compat "$EDGE_SERVER_ENV" ADMIN_TRUSTED_PROXIES)
	edge_msg "check mode: public_ip=${public_ip} trusted_proxies=${tp} admin_trusted_proxies=${admin_tp}"
	for f in cloudflare-realip.conf cloudflare-geo.conf emtds.conf emtds-admin-host.conf emtds-engine.conf; do
		printf '===== %s =====\n' "$f"
		cat "${tmp}/${f}"
	done
	report_nginx_events "$tmp"
	rm -rf "$tmp"
}

# Full install with nginx -t and reload.
run_apply() {
	local tmp backup_dir public_ip tp admin_tp
	if ! edge_is_root; then
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
	if [[ "$EDGE_TUNE_EVENTS" -eq 1 ]]; then
		plan_nginx_events /dev/null || return 1
	fi
	tmp=$(mktemp -d)
	backup_dir=
	EDGE_NGINX_DIR_EXISTED=0
	trap 'restore_from_backup "${EDGE_BACKUP_DIR:-}"; rm -rf "${tmp}"' ERR
	render_all "$tmp"
	install_rendered "$tmp"
	backup_dir=$EDGE_BACKUP_DIR
	if [[ "$EDGE_TUNE_EVENTS" -eq 1 ]]; then
		tune_nginx_events
	fi
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
	public_ip=$(env_value_compat "$EDGE_SERVER_ENV" PUBLIC_IP)
	tp=$(env_value_compat "$EDGE_SERVER_ENV" TRUSTED_PROXIES)
	admin_tp=$(env_value_compat "$EDGE_SERVER_ENV" ADMIN_TRUSTED_PROXIES)
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
			--tune-events)
				EDGE_TUNE_EVENTS=1
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
