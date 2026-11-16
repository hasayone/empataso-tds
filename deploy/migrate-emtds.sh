#!/usr/bin/env bash
# One-time server cutover KuzTDS -> EmTDS. Idempotent: a repeated run skips finished steps.
# It never deletes old volumes, never removes old containers, never runs compose down
# and never touches ClickHouse or Redis. Old engine, admin, php and cron are only stopped.
#
#   bash deploy/migrate-emtds.sh --dry-run   show the plan, change nothing
#   bash deploy/migrate-emtds.sh --keitaro-idle  cutover (root); the flag confirms GET /api/keitaro/job shows running: false
#   bash deploy/migrate-emtds.sh --rollback  back to the stopped kuztds-* containers (root)
#   bash deploy/migrate-emtds.sh --self-test checks without root and Docker

set -euo pipefail

OLD_TDS="${MIGRATE_OLD_TDS:-/opt/kuztds}"
NEW_TDS="${MIGRATE_NEW_TDS:-/opt/emtds}"
OLD_STATE="${MIGRATE_OLD_STATE:-/etc/kuztds}"
NEW_STATE="${MIGRATE_NEW_STATE:-/etc/emtds}"
UNIT_DIR="${MIGRATE_UNIT_DIR:-/etc/systemd/system}"
CONF_D="${MIGRATE_CONF_D:-/etc/nginx/conf.d}"
NGINX_LEGACY_DIR="${MIGRATE_NGINX_LEGACY_DIR:-/etc/nginx/kuztds}"
BACKUP_ROOT="${MIGRATE_BACKUP_ROOT:-/var/backups/emtds-migrate}"
OLD_CONTAINERS=(kuztds-engine kuztds-admin kuztds-cron kuztds-php)
NEW_CONTAINERS=(emtds-engine emtds-admin emtds-cron emtds-php)
APP_SERVICES=(engine admin php cron)
# Тома старого проекта kuztds-app (docker-compose.app.yml: том kuztds_state). Копируются только они.
STATE_VOLUMES=(kuztds-app_kuztds_state)
DRY_RUN=0
KEITARO_IDLE=0

# Назначение: сообщение миграции.
# Параметры: текст.
# Результат: строка на stdout.
# Ошибки: нет.
msg() {
	printf 'emtds-migrate: %s\n' "$*"
}

# Назначение: остановить сценарий с сообщением.
# Параметры: текст причины.
# Результат: не возвращается, код 1.
# Ошибки: нет.
die() {
	msg "ошибка: $*" >&2
	exit 1
}

# Назначение: выполнить команду или только показать её в --dry-run.
# Параметры: команда и аргументы.
# Результат: код команды; в --dry-run всегда 0.
# Ошибки: код возврата команды.
run() {
	if (( DRY_RUN == 1 )); then
		printf '+ %s\n' "$*"
		return 0
	fi
	"$@"
}

# Назначение: новое имя Docker-тома по старому.
# Параметры: $1 — имя тома с kuztds.
# Результат: имя с emtds вместо каждого kuztds на stdout.
# Ошибки: нет.
map_volume_name() {
	printf '%s' "${1//kuztds/emtds}"
}

# Назначение: последнее значение KEY= из env-файла (как в watchdog.sh и apply-edge.sh).
# Параметры: $1 — путь; $2 — имя ключа.
# Результат: значение без кавычек на stdout, может быть пустым.
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

# Назначение: хост:порт публикации из host:hostport:containerport (как в watchdog.sh).
# Параметры: $1 — значение ENGINE_PUBLISH; $2 — запасной хост:порт.
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

# Назначение: адрес /healthz движка по EMTDS_ENGINE_PUBLISH (или KUZTDS_) из server.env.
# Параметры: нет.
# Результат: URL на stdout; без ключа — http://127.0.0.1:18080/healthz, как у watchdog.sh.
# Ошибки: нет.
engine_health_url() {
	printf 'http://%s/healthz' "$(publish_listen "$(env_value_compat "${NEW_STATE}/server.env" ENGINE_PUBLISH)" 127.0.0.1:18080)"
}

# Назначение: убедиться, что оператор проверил перенос Keitaro перед остановкой admin.
# Параметры: нет (читает KEITARO_IDLE и DRY_RUN).
# Результат: код 0 при --keitaro-idle или в --dry-run (там только напоминание).
# Ошибки: без флага код 1. Состояние переноса живёт в памяти admin и отдаётся только
# авторизованным GET /api/keitaro/job, скрипт проверить его сам не может.
require_keitaro_idle() {
	if (( KEITARO_IDLE == 1 )); then
		return 0
	fi
	if (( DRY_RUN == 1 )); then
		msg "перед переходом проверьте GET /api/keitaro/job (running: false) и запускайте с --keitaro-idle"
		return 0
	fi
	die "перенос Keitaro не проверен: откройте GET /api/keitaro/job, дождитесь running: false и запустите с --keitaro-idle"
}

# Назначение: переписать ключи KUZTDS_* в EMTDS_* в env-файле со stdin на stdout, значения как есть.
# Параметры: нет (stdin — содержимое файла).
# Результат: KUZTDS_X=… становится EMTDS_X=…; если EMTDS_X уже есть, старая строка
# остаётся закомментированной, чтобы не было двух значений. Прочие строки не меняются.
# Ошибки: нет.
rewrite_env_stream() {
	awk '
		{ lines[NR] = $0 }
		match($0, /^(export[ \t]+)?EMTDS_[A-Za-z0-9_]+=/) {
			k = substr($0, RSTART, RLENGTH - 1); sub(/^export[ \t]+/, "", k); have[k] = 1
		}
		END {
			for (i = 1; i <= NR; i++) {
				l = lines[i]
				if (match(l, /^(export[ \t]+)?KUZTDS_[A-Za-z0-9_]+=/)) {
					k = substr(l, RSTART, RLENGTH - 1); sub(/^export[ \t]+/, "", k)
					nk = "EMTDS_" substr(k, 8)
					if (nk in have) { print "# " l; continue }
					sub(/KUZTDS_/, "EMTDS_", l)
				}
				print l
			}
		}'
}

# Назначение: имена ключей KUZTDS_* в env-файле (без значений, для --dry-run).
# Параметры: $1 — путь.
# Результат: имена через пробел на stdout.
# Ошибки: нет.
legacy_env_keys() {
	grep -oE '^(export[[:space:]]+)?KUZTDS_[A-Za-z0-9_]+=' "$1" 2>/dev/null | sed -E 's/^export[[:space:]]+//; s/=$//' | tr '\n' ' ' || true
}

# Назначение: переписать файл на месте, сохранив владельца и права.
# Параметры: $1 — путь; далее — команда-фильтр stdin→stdout.
# Результат: файл заменён, если содержимое изменилось.
# Ошибки: фильтр или запись.
rewrite_file() {
	local file=$1 tmp
	shift
	tmp=$(mktemp "${file}.emtds.XXXXXX")
	"$@" <"$file" >"$tmp"
	if cmp -s "$file" "$tmp"; then
		rm -f "$tmp"
		return 0
	fi
	cat "$tmp" >"$file"
	rm -f "$tmp"
}

# Назначение: фильтр compose-override и up.sh: ключи и пути старого имени.
# Параметры: нет (stdin → stdout).
# Результат: KUZTDS_ → EMTDS_, /opt/kuztds → /opt/emtds, /etc/kuztds → /etc/emtds, /var/lib/kuztds → /var/lib/emtds.
# Ошибки: нет.
rewrite_paths_stream() {
	sed -e 's/KUZTDS_/EMTDS_/g' \
		-e "s#${OLD_TDS}#${NEW_TDS}#g" \
		-e "s#${OLD_STATE}#${NEW_STATE}#g" \
		-e 's#/var/lib/kuztds#/var/lib/emtds#g'
}

# Назначение: состояние контейнера.
# Параметры: $1 — имя.
# Результат: running, exited или missing на stdout.
# Ошибки: нет.
container_state() {
	local running
	if ! docker container inspect "$1" >/dev/null 2>&1; then
		printf 'missing'
		return 0
	fi
	running=$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null || echo false)
	if [[ "$running" == true ]]; then
		printf 'running'
	else
		printf 'exited'
	fi
}

# Назначение: перенести каталог и оставить на старом месте симлинк.
# Параметры: $1 — старый путь; $2 — новый путь.
# Результат: новый каталог есть, старый путь — симлинк на него.
# Ошибки: оба пути — настоящие каталоги (непонятно, какой главный), код 1.
move_with_symlink() {
	local old=$1 new=$2
	if [[ -L "$old" && -d "$new" ]]; then
		msg "${old} уже симлинк на ${new}"
		return 0
	fi
	if [[ -d "$old" && ! -L "$old" && -e "$new" ]]; then
		die "есть и ${old}, и ${new}; разберите вручную"
	fi
	if [[ ! -e "$old" && -d "$new" ]]; then
		run ln -s "$new" "$old"
		return 0
	fi
	[[ -d "$old" ]] || die "нет ${old} и ${new}"
	run mv "$old" "$new"
	run ln -s "$new" "$old"
}

# Назначение: проверить, что новый проект не тронет ClickHouse и Redis.
# Параметры: нет.
# Результат: код 0.
# Ошибки: сеть deploy_default не от проекта deploy, код 1.
check_infra() {
	local proj
	proj=$(docker network inspect -f '{{ index .Labels "com.docker.compose.project"}}' deploy_default 2>/dev/null || true)
	[[ "$proj" == deploy ]] || die "сеть deploy_default не найдена или не от проекта deploy"
	msg "ClickHouse и Redis (проект deploy) не трогаем"
}

# Назначение: остановить и отключить старый сторож, сохранив юниты в бэкап.
# Параметры: $1 — каталог бэкапа.
# Результат: kuztds-watchdog.timer остановлен, файлы юнитов в бэкапе и удалены.
# Ошибки: systemctl.
stop_legacy_watchdog() {
	local backup=$1 f found=0
	for f in "$UNIT_DIR"/kuztds-watchdog.service "$UNIT_DIR"/kuztds-watchdog.timer; do
		[[ -e "$f" ]] || continue
		found=1
		run install -d -m 0700 "${backup}/systemd"
		run cp -a "$f" "${backup}/systemd/"
	done
	if (( found == 0 )); then
		msg "старого сторожа нет"
		return 0
	fi
	run systemctl disable --now kuztds-watchdog.timer
	run systemctl stop kuztds-watchdog.service
	run rm -f "$UNIT_DIR"/kuztds-watchdog.service "$UNIT_DIR"/kuztds-watchdog.timer
	run systemctl daemon-reload
}

# Назначение: бэкап конфигурации до изменений.
# Параметры: $1 — каталог бэкапа.
# Результат: tar каталога состояния, .env, nginx kuztds* и /etc/nginx/kuztds.
# Ошибки: tar/cp.
backup_config() {
	local backup=$1 state f
	run install -d -m 0700 "$backup"
	state=$OLD_STATE
	[[ -d "$state" ]] || state=$NEW_STATE
	if [[ -d "$state" ]]; then
		run tar -C "$(dirname "$state")" -czf "${backup}/state.tgz" "$(basename "$(readlink -f "$state")")"
	fi
	for f in "${OLD_TDS}/.env" "${NEW_TDS}/.env"; do
		if [[ -f "$f" ]]; then
			run install -m 0600 "$f" "${backup}/app.env"
			break
		fi
	done
	run install -d -m 0700 "${backup}/nginx"
	for f in "$CONF_D"/kuztds*.conf; do
		[[ -e "$f" ]] || continue
		run cp -a "$f" "${backup}/nginx/"
	done
	if [[ -d "$NGINX_LEGACY_DIR" ]]; then
		run cp -a "$NGINX_LEGACY_DIR" "${backup}/nginx/legacy-dir"
	fi
	msg "бэкап: ${backup}"
}

# Назначение: переписать ключи и пути в .env, server.env, override и up.sh.
# Параметры: нет.
# Результат: ключи EMTDS_*, значения прежние. В --dry-run печатаются только имена ключей.
# Ошибки: запись файлов.
rewrite_configs() {
	local f
	for f in "${NEW_TDS}/.env" "${NEW_STATE}/server.env"; do
		f=$(effective_path "$f")
		[[ -f "$f" ]] || continue
		if (( DRY_RUN == 1 )); then
			msg "ключи к переименованию в ${f}: $(legacy_env_keys "$f")"
			continue
		fi
		rewrite_file "$f" rewrite_env_stream
	done
	for f in "${NEW_STATE}/compose.app.yml" "${NEW_STATE}/compose.infra.yml" "${NEW_STATE}/up.sh"; do
		f=$(effective_path "$f")
		[[ -f "$f" ]] || continue
		if (( DRY_RUN == 1 )); then
			msg "пути и ключи старого имени в ${f}: $(grep -cE 'KUZTDS_|kuztds' "$f" || true) строк"
			continue
		fi
		rewrite_file "$f" rewrite_paths_stream
	done
}

# Назначение: путь файла с учётом --dry-run, где каталоги ещё не перенесены.
# Параметры: $1 — путь в новом каталоге.
# Результат: тот же путь, а в --dry-run при его отсутствии — путь в старом каталоге.
# Ошибки: нет.
effective_path() {
	local p=$1
	if (( DRY_RUN == 1 )) && [[ ! -e "$p" ]]; then
		p=${p/#"$NEW_TDS"/"$OLD_TDS"}
		p=${p/#"$NEW_STATE"/"$OLD_STATE"}
	fi
	printf '%s' "$p"
}

# Назначение: остановить старые engine, admin, cron и php (без удаления).
# Параметры: нет.
# Результат: контейнеры kuztds-* в состоянии exited.
# Ошибки: docker stop.
stop_old_app() {
	local name
	for name in "${OLD_CONTAINERS[@]}"; do
		if [[ "$(container_state "$name")" == running ]]; then
			run docker stop -t 30 "$name"
		fi
	done
}

# Назначение: образ для копирования томов без сети.
# Параметры: нет.
# Результат: emtds:local или kuztds:local на stdout.
# Ошибки: ни одного образа, код 1.
copy_image() {
	local img
	for img in "${MIGRATE_COPY_IMAGE:-}" emtds:local kuztds:local; do
		[[ -n "$img" ]] || continue
		if docker image inspect "$img" >/dev/null 2>&1; then
			printf '%s' "$img"
			return 0
		fi
	done
	return 1
}

# Назначение: отпечаток содержимого тома (пути, размеры, права, владельцы).
# Параметры: $1 — образ; $2 — имя тома.
# Результат: sha256 списка на stdout.
# Ошибки: docker run.
volume_fingerprint() {
	docker run --rm --user 0:0 --network none --entrypoint sh -v "$2":/v:ro "$1" \
		-c 'cd /v && find . -printf "%p %s %m %U %G\n" | LC_ALL=C sort | sha256sum | cut -d" " -f1'
}

# Назначение: скопировать тома из STATE_VOLUMES в тома с emtds вместо kuztds, старые не трогать.
# Параметры: нет.
# Результат: для каждого тома копия с меткой emtds.migrated-from и совпавший отпечаток;
# метка о готовности пишется в ${NEW_STATE}/migrate/volumes/.
# Ошибки: нет исходного тома, чужой непустой целевой том, сбой копирования
# или несовпадение отпечатка, код 1.
copy_volumes() {
	local img src dst from stamp_dir a b
	stamp_dir="${NEW_STATE}/migrate/volumes"
	img=$(copy_image) || die "нет образа emtds:local или kuztds:local для копирования томов"
	for src in "${STATE_VOLUMES[@]}"; do
		dst=$(map_volume_name "$src")
		if [[ -f "${stamp_dir}/${dst}" ]]; then
			msg "том ${dst} уже скопирован из ${src}"
			continue
		fi
		docker volume inspect "$src" >/dev/null 2>&1 || die "нет исходного тома ${src}"
		if docker volume inspect "$dst" >/dev/null 2>&1; then
			from=$(docker volume inspect -f '{{ index .Labels "emtds.migrated-from"}}' "$dst" 2>/dev/null || true)
			[[ "$from" == "$src" ]] || die "том ${dst} уже есть и создан не миграцией; не перезаписываю"
			msg "том ${dst} создан миграцией без отметки о проверке: копирую заново"
			run docker run --rm --user 0:0 --network none --entrypoint sh \
				-v "$dst":/to "$img" -c 'find /to -mindepth 1 -delete'
		else
			run docker volume create --label "emtds.migrated-from=${src}" "$dst"
		fi
		msg "копирую том ${src} -> ${dst}"
		run docker run --rm --user 0:0 --network none --entrypoint sh \
			-v "$src":/from:ro -v "$dst":/to "$img" -c 'cp -a /from/. /to/'
		if (( DRY_RUN == 1 )); then
			continue
		fi
		a=$(volume_fingerprint "$img" "$src")
		b=$(volume_fingerprint "$img" "$dst")
		[[ -n "$a" && "$a" == "$b" ]] || die "том ${dst} не совпал с ${src}; старый том цел"
		install -d -m 0700 "$stamp_dir"
		printf '%s %s\n' "$src" "$a" >"${stamp_dir}/${dst}"
		msg "том ${dst} проверен"
	done
}

# Назначение: docker compose нового проекта с env и override сервера.
# Параметры: аргументы compose.
# Результат: код compose.
# Ошибки: compose.
app_compose() {
	local args=(docker compose --env-file "${NEW_TDS}/.env")
	[[ -f "${NEW_STATE}/server.env" ]] && args+=(--env-file "${NEW_STATE}/server.env")
	args+=(-f "${NEW_TDS}/deploy/docker-compose.app.yml")
	if [[ -f "$(effective_path "${NEW_STATE}/compose.app.yml")" ]]; then
		args+=(-f "${NEW_STATE}/compose.app.yml")
	fi
	if (( DRY_RUN == 1 )); then
		printf '+ (cd %s) %s %s\n' "$NEW_TDS" "${args[*]}" "$*"
		return 0
	fi
	(cd "$NEW_TDS" && "${args[@]}" "$@")
}

# Назначение: дождаться строк подключения ClickHouse и Redis в логах контейнера.
# Параметры: $1 — имя контейнера.
# Результат: код 0.
# Ошибки: таймаут 90 с, код 1.
wait_connected() {
	local name=$1 deadline=$((SECONDS + 90)) logs
	while (( SECONDS < deadline )); do
		logs=$(docker logs "$name" 2>&1 || true)
		if grep -q 'clickhouse connected' <<<"$logs" && grep -q 'redis connected' <<<"$logs"; then
			return 0
		fi
		sleep 3
	done
	return 1
}

# Назначение: собрать образ emtds:local, пока старое приложение ещё принимает клики.
# Параметры: нет.
# Результат: образ готов, старые контейнеры не тронуты.
# Ошибки: сбой сборки, код 1; kuztds-* продолжают работать.
build_new_image() {
	app_compose build engine || die "сборка образа не удалась; kuztds-* не останавливались и продолжают работать"
}

# Назначение: поднять engine, admin, php и cron в проекте emtds-app из готового образа.
# Параметры: нет.
# Результат: контейнеры emtds-* работают, health отвечает.
# Ошибки: старт или проверки, код 1 с подсказкой про --rollback.
start_new_app() {
	app_compose up -d --no-deps "${APP_SERVICES[@]}"
	if (( DRY_RUN == 1 )); then
		return 0
	fi
	wait_connected emtds-engine || die "emtds-engine не подключился к ClickHouse и Redis; откат: --rollback"
	wait_connected emtds-admin || die "emtds-admin не подключился к ClickHouse и Redis; откат: --rollback"
	curl -fsS --max-time 5 "$(engine_health_url)" >/dev/null || die "healthz движка не отвечает; откат: --rollback"
	curl -fsS --max-time 5 http://127.0.0.1:8090/api/health >/dev/null || die "health админки не отвечает; откат: --rollback"
	msg "engine, admin, php и cron работают в проекте emtds-app"
}

# Назначение: nginx emtds* через apply-edge.sh (бэкап, nginx -t, откат), старые kuztds* убираются.
# Параметры: нет.
# Результат: nginx перезагружен с новыми файлами.
# Ошибки: apply-edge.sh.
apply_nginx() {
	command -v nginx >/dev/null 2>&1 || { msg "nginx не установлен, пропуск"; return 0; }
	if (( DRY_RUN == 1 )); then
		printf '+ bash %s/deploy/apply-edge.sh\n' "$NEW_TDS"
		return 0
	fi
	EMTDS_INSTALL_STATE_DIR="$NEW_STATE" bash "${NEW_TDS}/deploy/apply-edge.sh"
}

# Назначение: полный переход на EmTDS.
# Параметры: нет.
# Результат: сообщения о каждом шаге.
# Ошибки: любой шаг, код 1.
run_migrate() {
	local backup
	require_keitaro_idle
	if (( DRY_RUN == 0 )) && [[ $(id -u) -ne 0 ]]; then
		die "нужны права root"
	fi
	command -v docker >/dev/null 2>&1 || die "нет docker"
	check_infra
	backup="${BACKUP_ROOT}/$(date -u +%Y%m%dT%H%M%SZ)"
	backup_config "$backup"
	stop_legacy_watchdog "$backup"
	move_with_symlink "$OLD_TDS" "$NEW_TDS"
	move_with_symlink "$OLD_STATE" "$NEW_STATE"
	rewrite_configs
	build_new_image
	stop_old_app
	copy_volumes
	start_new_app
	apply_nginx
	if (( DRY_RUN == 1 )); then
		printf '+ bash %s/deploy/watchdog.sh --install\n' "$NEW_TDS"
	else
		bash "${NEW_TDS}/deploy/watchdog.sh" --install
	fi
	msg "готово. Старые контейнеры kuztds-* остановлены, старые тома целы. Бэкап: ${backup}"
}

# Назначение: вернуть остановленные kuztds-* и старый nginx из последнего бэкапа миграции.
# Параметры: нет.
# Результат: работают kuztds-*, emtds-* остановлены, nginx прежний, сторож выключен,
# отметки о скопированных томах убраны в бэкап.
# Ошибки: нет бэкапа, nginx -t не прошёл, код 1.
run_rollback() {
	local backup nginx_backup name f d
	[[ $(id -u) -eq 0 ]] || die "нужны права root"
	backup=$(ls -1d "${BACKUP_ROOT}"/*/ 2>/dev/null | sort | tail -n 1 || true)
	[[ -n "$backup" ]] || die "нет бэкапа в ${BACKUP_ROOT}"
	backup=${backup%/}
	nginx_backup=
	while read -r d; do
		if compgen -G "${d%/}/nginx/kuztds*.conf" >/dev/null; then
			nginx_backup=${d%/}
		fi
	done < <(ls -1d "${BACKUP_ROOT}"/*/ 2>/dev/null | sort)
	if command -v systemctl >/dev/null 2>&1; then
		systemctl disable --now emtds-watchdog.timer 2>/dev/null || true
		systemctl disable --now emtds-update.timer 2>/dev/null || true
	fi
	for name in "${NEW_CONTAINERS[@]}"; do
		if [[ "$(container_state "$name")" == running ]]; then
			docker stop -t 30 "$name"
		fi
	done
	for name in "${OLD_CONTAINERS[@]}"; do
		if [[ "$(container_state "$name")" == exited ]]; then
			docker start "$name"
		fi
	done
	if ! command -v nginx >/dev/null 2>&1; then
		msg "nginx не установлен, пропуск"
	elif [[ -z "$nginx_backup" ]]; then
		msg "в бэкапах нет nginx kuztds*.conf; nginx не трогаю"
	else
		for f in "${nginx_backup}/nginx/"kuztds*.conf; do
			install -m 0644 "$f" "${CONF_D}/$(basename "$f")"
		done
		if [[ -d "${nginx_backup}/nginx/legacy-dir" ]]; then
			install -d -m 0755 "$NGINX_LEGACY_DIR"
			cp -a "${nginx_backup}/nginx/legacy-dir/." "$NGINX_LEGACY_DIR/"
		fi
		install -d -m 0700 "${backup}/nginx-emtds"
		for f in "$CONF_D"/emtds*.conf; do
			[[ -e "$f" ]] || continue
			mv "$f" "${backup}/nginx-emtds/"
		done
		nginx -t || die "nginx -t не прошёл; файлы emtds* лежат в ${backup}/nginx-emtds"
		systemctl reload nginx
	fi
	if [[ -d "${NEW_STATE}/migrate/volumes" ]]; then
		mv "${NEW_STATE}/migrate/volumes" "${backup}/volumes-stamps"
	fi
	msg "откат: работают kuztds-*. Изменения, сделанные в EmTDS после перехода, остались только в томах emtds*"
	msg "сторож выключен: в ${OLD_TDS} теперь код EmTDS. Старый юнит в бэкапе systemd, включайте его только вместе со старым кодом"
	msg "повторный переход скопирует тома заново"
}

# Назначение: проверки без root и Docker.
# Параметры: нет.
# Результат: self-test ok.
# Ошибки: self-test FAIL, код 1.
run_self_test() {
	local tmp got want
	tmp=$(mktemp -d)
	# shellcheck disable=SC2064
	trap "rm -rf '$tmp'" EXIT
	[[ "$(map_volume_name kuztds-app_kuztds_state)" == emtds-app_emtds_state ]] || { printf 'self-test FAIL: map_volume_name\n'; exit 1; }
	printf 'CLICKHOUSE_PASSWORD=x\nKUZTDS_ADMIN_PASSWORD="a=b"\nexport KUZTDS_PUBLIC_IP=1.2.3.4\nKUZTDS_CID_KEY=k\nEMTDS_CID_KEY=n\n# KUZTDS_NOTE=c\n' >"$tmp/env"
	got=$(rewrite_env_stream <"$tmp/env")
	want=$'CLICKHOUSE_PASSWORD=x\nEMTDS_ADMIN_PASSWORD="a=b"\nexport EMTDS_PUBLIC_IP=1.2.3.4\n# KUZTDS_CID_KEY=k\nEMTDS_CID_KEY=n\n# KUZTDS_NOTE=c'
	[[ "$got" == "$want" ]] || { printf 'self-test FAIL: rewrite_env_stream\n%s\n' "$got"; exit 1; }
	[[ "$(rewrite_env_stream <<<"$got")" == "$want" ]] || { printf 'self-test FAIL: rewrite_env_stream idempotent\n'; exit 1; }
	[[ "$(legacy_env_keys "$tmp/env")" == "KUZTDS_ADMIN_PASSWORD KUZTDS_PUBLIC_IP KUZTDS_CID_KEY " ]] || { printf 'self-test FAIL: legacy_env_keys\n'; exit 1; }
	chmod 0640 "$tmp/env"
	rewrite_file "$tmp/env" rewrite_env_stream
	[[ "$(stat -c '%a' "$tmp/env")" == 640 ]] || { printf 'self-test FAIL: rewrite_file mode\n'; exit 1; }
	[[ "$(printf 'cd /opt/kuztds\n-f /etc/kuztds/compose.app.yml\n' | rewrite_paths_stream)" == $'cd /opt/emtds\n-f /etc/emtds/compose.app.yml' ]] || { printf 'self-test FAIL: rewrite_paths_stream\n'; exit 1; }
	mkdir -p "$tmp/old"
	touch "$tmp/old/file"
	move_with_symlink "$tmp/old" "$tmp/new" >/dev/null
	[[ -L "$tmp/old" && -f "$tmp/new/file" ]] || { printf 'self-test FAIL: move_with_symlink\n'; exit 1; }
	move_with_symlink "$tmp/old" "$tmp/new" >/dev/null
	[[ -L "$tmp/old" && -f "$tmp/new/file" ]] || { printf 'self-test FAIL: move_with_symlink repeat\n'; exit 1; }
	NEW_STATE=$tmp/state
	mkdir -p "$NEW_STATE"
	[[ "$(engine_health_url)" == http://127.0.0.1:18080/healthz ]] || { printf 'self-test FAIL: engine_health_url default\n'; exit 1; }
	printf 'KUZTDS_ENGINE_PUBLISH=127.0.0.1:18081:8080\n' >"$NEW_STATE/server.env"
	[[ "$(engine_health_url)" == http://127.0.0.1:18081/healthz ]] || { printf 'self-test FAIL: engine_health_url legacy key\n'; exit 1; }
	printf 'KUZTDS_ENGINE_PUBLISH=127.0.0.1:18081:8080\nEMTDS_ENGINE_PUBLISH="10.0.0.5:18082:8080"\n' >"$NEW_STATE/server.env"
	[[ "$(engine_health_url)" == http://10.0.0.5:18082/healthz ]] || { printf 'self-test FAIL: engine_health_url EMTDS key\n'; exit 1; }
	(KEITARO_IDLE=0 DRY_RUN=0 require_keitaro_idle) >/dev/null 2>&1 && { printf 'self-test FAIL: require_keitaro_idle без флага\n'; exit 1; }
	(KEITARO_IDLE=1 DRY_RUN=0 require_keitaro_idle) >/dev/null 2>&1 || { printf 'self-test FAIL: require_keitaro_idle с флагом\n'; exit 1; }
	(KEITARO_IDLE=0 DRY_RUN=1 require_keitaro_idle) >/dev/null 2>&1 || { printf 'self-test FAIL: require_keitaro_idle в dry-run\n'; exit 1; }
	printf 'self-test ok\n'
}

# Назначение: подсказка по аргументам.
# Параметры: нет.
# Результат: строка на stderr.
# Ошибки: нет.
usage() {
	msg "использование: migrate-emtds.sh [--dry-run] [--keitaro-idle] | --rollback | --self-test" >&2
}

# Назначение: точка входа.
# Параметры: --dry-run, --keitaro-idle (с миграцией) | --rollback | --self-test | пусто.
# Результат: код сценария.
# Ошибки: неизвестный аргумент или --dry-run вместе с --rollback/--self-test, код 2.
main() {
	local mode=migrate arg
	for arg in "$@"; do
		case "$arg" in
			--dry-run) DRY_RUN=1 ;;
			--keitaro-idle) KEITARO_IDLE=1 ;;
			--rollback) mode=rollback ;;
			--self-test) mode=self-test ;;
			*)
				usage
				exit 2
				;;
		esac
	done
	if (( DRY_RUN == 1 )) && [[ "$mode" != migrate ]]; then
		usage
		exit 2
	fi
	case "$mode" in
		rollback) run_rollback ;;
		self-test) run_self_test ;;
		*) run_migrate ;;
	esac
}

main "$@"