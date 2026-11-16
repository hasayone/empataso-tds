#!/usr/bin/env bash
# Назначение: привести maxmemory Redis и лимит памяти его контейнера к окну уникальности в сутки,
# не пересоздавая контейнер: CONFIG SET maxmemory и docker update --memory/--memory-swap.
# Скрипт идемпотентен: совпавшее значение не пишется. ClickHouse, engine и admin не трогает.
# Запускается вручную на сервере и только по явной просьбе. CONFIG SET maxmemory действует до
# перезапуска процесса Redis (рестарт контейнера или перезагрузка сервера вернут значение из command
# контейнера), docker update сохраняется. Постоянные значения для новых установок задаёт install.sh.

set -euo pipefail

REDIS_CONTAINER="${EMTDS_REDIS_CONTAINER:-deploy-redis-1}"
REDIS_STATE_DIR="${EMTDS_INSTALL_STATE_DIR:-${KUZTDS_INSTALL_STATE_DIR:-}}"
if [[ -z "$REDIS_STATE_DIR" ]]; then
	REDIS_STATE_DIR=/etc/emtds
	if [[ ! -e /etc/emtds && -d /etc/kuztds ]]; then
		REDIS_STATE_DIR=/etc/kuztds
	fi
fi
REDIS_SERVER_ENV="${EMTDS_SERVER_ENV:-${KUZTDS_SERVER_ENV:-${REDIS_STATE_DIR}/server.env}}"
REDIS_CHECK_MODE=0
REDIS_WANT_MAXMEMORY=
REDIS_WANT_MEMORY=
# Запас контейнера над maxmemory в процентах: фрагментация и буферы клиентов.
REDIS_HEADROOM_PERCENT=25
# Запас в процентах над текущим потреблением (RSS Redis и docker stats), без которого лимит контейнера не снижается.
REDIS_SHRINK_HEADROOM_PERCENT=20

# Назначение: сообщение оператору.
# Параметры: текст.
# Результат: строка на stdout.
# Ошибки: нет.
redis_msg() {
	printf 'emtds-apply-redis: %s\n' "$*"
}

# Назначение: справка по аргументам.
# Параметры: нет.
# Результат: текст на stderr.
# Ошибки: нет.
usage() {
	printf 'usage: apply-redis.sh [--check] [--maxmemory РАЗМЕР] [--memory РАЗМЕР]\n' >&2
	printf '  --check              показать текущие значения и цель, ничего не менять\n' >&2
	printf '  --maxmemory РАЗМЕР   цель для CONFIG SET maxmemory (512mb, 2gb); по умолчанию по RAM сервера\n' >&2
	printf '  --memory РАЗМЕР      цель для лимита памяти контейнера (768m, 3g); по умолчанию по RAM сервера\n' >&2
	printf 'контейнер: EMTDS_REDIS_CONTAINER (по умолчанию deploy-redis-1); пароль Redis берётся из\n' >&2
	printf 'EMTDS_REDIS_PASSWORD или из server.env и передаётся в redis-cli через REDISCLI_AUTH, в вывод не попадает\n' >&2
}

# Назначение: перевести размер Redis в байты (1kb = 1024, суффиксы kb, mb, gb, k, m, g, регистр любой).
# Параметры: $1 — размер, например 512mb, 1536m, 3g, 1073741824.
# Результат: число байт на stdout.
# Ошибки: пустой, нулевой или неразобранный размер — код 1 без вывода.
to_bytes() {
	local raw=${1,,} num unit mult
	if [[ ! "$raw" =~ ^([0-9]+)(kb|mb|gb|k|m|g)?$ ]]; then
		return 1
	fi
	num=${BASH_REMATCH[1]}
	unit=${BASH_REMATCH[2]}
	case $unit in
	kb | k) mult=1024 ;;
	mb | m) mult=1048576 ;;
	gb | g) mult=1073741824 ;;
	*) mult=1 ;;
	esac
	if (( 10#$num == 0 )); then
		return 1
	fi
	printf '%s\n' $(( 10#$num * mult ))
}

# Назначение: MiB оперативной памяти сервера.
# Параметры: нет (EMTDS_HOST_MEM_MIB переопределяет значение, /proc/meminfo по умолчанию).
# Результат: число MiB на stdout.
# Ошибки: память не определена — код 1.
host_mem_mib() {
	local kb
	if [[ -n "${EMTDS_HOST_MEM_MIB:-}" ]]; then
		printf '%s\n' "$EMTDS_HOST_MEM_MIB"
		return 0
	fi
	kb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null || true)
	if [[ -z "$kb" ]]; then
		return 1
	fi
	printf '%s\n' $(( kb / 1024 ))
}

# Назначение: maxmemory по RAM сервера; пороги те же, что у redis_maxmemory в install.sh.
# Параметры: $1 — RAM MiB.
# Окно уникальности в сутки при около 120 байт на клик: 2gb держат около 17 млн кликов
# в сутки, 1gb — только около 9 млн при цели 14,4 млн, поэтому от 12000 MiB берётся 2gb.
# Результат: 512mb или 2gb на stdout.
# Ошибки: нет.
tier_maxmemory() {
	local mem=$1
	if (( mem < 12000 )); then
		printf '512mb\n'
	else
		printf '2gb\n'
	fi
}

# Назначение: лимит памяти контейнера по RAM сервера; пороги те же, что у redis_mem_limit в install.sh.
# Параметры: $1 — RAM MiB.
# Результат: 768m или 3g на stdout.
# Ошибки: нет.
tier_memory() {
	local mem=$1
	if (( mem < 12000 )); then
		printf '768m\n'
	else
		printf '3g\n'
	fi
}

# Назначение: последнее значение KEY= из env-файла.
# Параметры: $1 — файл, $2 — ключ.
# Результат: значение без кавычек на stdout; пусто, если файла или ключа нет.
# Ошибки: нет.
env_file_value() {
	local file=$1 key=$2 line val
	line=$(grep -E "^${key}=" "$file" 2>/dev/null | tail -n 1 || true)
	if [[ -z "$line" ]]; then
		return 0
	fi
	val=${line#*=}
	if [[ "$val" == \"*\" && "$val" == *\" ]] || [[ "$val" == \'*\' && "$val" == *\' ]]; then
		val=${val:1:${#val}-2}
	fi
	printf '%s' "$val"
}

# Назначение: подготовить пароль Redis для redis-cli. Значение уходит только в переменную
# REDISCLI_AUTH процесса; ни в аргументы, ни в вывод оно не попадает.
# Параметры: нет (EMTDS_REDIS_PASSWORD, затем KUZTDS_REDIS_PASSWORD, затем server.env).
# Результат: экспортирует REDISCLI_AUTH, если пароль задан.
# Ошибки: нет.
load_redis_password() {
	local pw=${EMTDS_REDIS_PASSWORD:-${KUZTDS_REDIS_PASSWORD:-}}
	if [[ -z "$pw" ]]; then
		pw=$(env_file_value "$REDIS_SERVER_ENV" EMTDS_REDIS_PASSWORD)
	fi
	if [[ -z "$pw" ]]; then
		pw=$(env_file_value "$REDIS_SERVER_ENV" KUZTDS_REDIS_PASSWORD)
	fi
	if [[ -n "$pw" ]]; then
		export REDISCLI_AUTH="$pw"
	fi
}

# Назначение: выполнить redis-cli в контейнере Redis.
# Параметры: аргументы redis-cli.
# Результат: вывод redis-cli.
# Ошибки: код docker exec.
rcli() {
	local args=(exec -i)
	if [[ -n "${REDISCLI_AUTH:-}" ]]; then
		args+=(-e REDISCLI_AUTH)
	fi
	docker "${args[@]}" "$REDIS_CONTAINER" redis-cli "$@"
}

# Назначение: текущий maxmemory Redis в байтах.
# Параметры: нет.
# Результат: число на stdout (0 — без лимита).
# Ошибки: Redis не ответил — код 1.
current_maxmemory() {
	local out
	out=$(rcli CONFIG GET maxmemory | tr -d '\r' | tail -n 1)
	if [[ ! "$out" =~ ^[0-9]+$ ]]; then
		return 1
	fi
	printf '%s\n' "$out"
}

# Назначение: used_memory Redis в байтах.
# Параметры: нет.
# Результат: число на stdout.
# Ошибки: Redis не ответил — код 1.
current_used_memory() {
	local out
	out=$(rcli INFO memory | tr -d '\r' | awk -F: '$1 == "used_memory" {print $2}')
	if [[ ! "$out" =~ ^[0-9]+$ ]]; then
		return 1
	fi
	printf '%s\n' "$out"
}

# Назначение: used_memory_rss Redis в байтах (память процесса с учётом фрагментации).
# Параметры: нет.
# Результат: число на stdout.
# Ошибки: Redis не ответил — код 1.
current_rss() {
	local out
	out=$(rcli INFO memory | tr -d '\r' | awk -F: '$1 == "used_memory_rss" {print $2}')
	if [[ ! "$out" =~ ^[0-9]+$ ]]; then
		return 1
	fi
	printf '%s\n' "$out"
}

# Назначение: потребление памяти контейнера по docker stats --no-stream.
# Параметры: нет.
# Результат: байты на stdout (из строки вида «1.5GiB / 3GiB» берётся левая часть).
# Ошибки: docker stats не ответил или формат не разобран — код 1.
stats_usage_bytes() {
	local line num unit mult
	line=$(docker stats --no-stream --format '{{.MemUsage}}' "$REDIS_CONTAINER" 2>/dev/null) || return 1
	line=${line%% /*}
	line=${line// /}
	if [[ ! "$line" =~ ^([0-9]+(\.[0-9]+)?)([A-Za-z]*)$ ]]; then
		return 1
	fi
	num=${BASH_REMATCH[1]}
	unit=${BASH_REMATCH[3],,}
	case $unit in
	b | '') mult=1 ;;
	kb) mult=1000 ;;
	kib) mult=1024 ;;
	mb) mult=1000000 ;;
	mib) mult=1048576 ;;
	gb) mult=1000000000 ;;
	gib) mult=1073741824 ;;
	*) return 1 ;;
	esac
	awk -v n="$num" -v m="$mult" 'BEGIN { printf "%d\n", n * m }'
}

# Назначение: maxmemory из command контейнера (то, что Redis получит после перезапуска процесса).
# Параметры: нет.
# Результат: байты на stdout; 0, если в command нет --maxmemory или значение не разобрано.
# Ошибки: нет.
command_maxmemory_bytes() {
	local raw bytes
	raw=$(docker inspect -f '{{json .Config.Cmd}}' "$REDIS_CONTAINER" 2>/dev/null | tr -d '[]"' | tr ',' '\n' | awk '$0 == "--maxmemory" { getline; print; exit }' || true)
	bytes=$(to_bytes "$raw" 2>/dev/null || true)
	printf '%s\n' "${bytes:-0}"
}

# Назначение: лимит памяти и memswap контейнера в байтах.
# Параметры: нет.
# Результат: «память memswap» на stdout (0 — без лимита).
# Ошибки: контейнера нет — код 1.
current_container_limits() {
	docker inspect -f '{{.HostConfig.Memory}} {{.HostConfig.MemorySwap}}' "$REDIS_CONTAINER"
}

# Назначение: байты в MiB для сообщений.
# Параметры: $1 — байты.
# Результат: «N MiB» или «без лимита» на stdout.
# Ошибки: нет.
human() {
	if (( $1 == 0 )); then
		printf 'без лимита\n'
	else
		printf '%s MiB\n' $(( $1 / 1048576 ))
	fi
}

# Назначение: разобрать аргументы командной строки.
# Параметры: аргументы скрипта.
# Результат: REDIS_CHECK_MODE, REDIS_WANT_MAXMEMORY, REDIS_WANT_MEMORY.
# Ошибки: неизвестный аргумент или флаг без значения — справка и код 2.
parse_args() {
	while (( $# > 0 )); do
		case $1 in
		--check) REDIS_CHECK_MODE=1 ;;
		--maxmemory | --memory)
			if (( $# < 2 )); then
				usage
				exit 2
			fi
			if [[ $1 == --maxmemory ]]; then
				REDIS_WANT_MAXMEMORY=$2
			else
				REDIS_WANT_MEMORY=$2
			fi
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		*)
			usage
			exit 2
			;;
		esac
		shift
	done
}

# Назначение: определить цели и показать текущие значения.
# Параметры: нет.
# Результат: переменные want_max, want_mem, cur_max, cur_mem, cur_swap, used, rss, cmd_max; строки состояния на stdout.
# Ошибки: цель не разобрана, лимит контейнера ниже maxmemory с запасом, контейнер или Redis недоступны — код 1.
inspect_state() {
	local mib
	if [[ -z "$REDIS_WANT_MAXMEMORY" || -z "$REDIS_WANT_MEMORY" ]]; then
		mib=$(host_mem_mib) || {
			redis_msg "не удалось определить RAM сервера: задайте --maxmemory и --memory" >&2
			return 1
		}
		[[ -n "$REDIS_WANT_MAXMEMORY" ]] || REDIS_WANT_MAXMEMORY=$(tier_maxmemory "$mib")
		[[ -n "$REDIS_WANT_MEMORY" ]] || REDIS_WANT_MEMORY=$(tier_memory "$mib")
	fi
	want_max=$(to_bytes "$REDIS_WANT_MAXMEMORY") || {
		redis_msg "неверный размер maxmemory: ${REDIS_WANT_MAXMEMORY}" >&2
		return 1
	}
	want_mem=$(to_bytes "$REDIS_WANT_MEMORY") || {
		redis_msg "неверный размер лимита контейнера: ${REDIS_WANT_MEMORY}" >&2
		return 1
	}
	if (( want_mem * 100 < want_max * (100 + REDIS_HEADROOM_PERCENT) )); then
		redis_msg "лимит контейнера ${REDIS_WANT_MEMORY} меньше maxmemory ${REDIS_WANT_MAXMEMORY} с запасом ${REDIS_HEADROOM_PERCENT}%" >&2
		return 1
	fi
	if ! docker inspect "$REDIS_CONTAINER" >/dev/null 2>&1; then
		redis_msg "контейнер ${REDIS_CONTAINER} не найден" >&2
		return 1
	fi
	load_redis_password
	cur_max=$(current_maxmemory) || {
		redis_msg "Redis в ${REDIS_CONTAINER} не ответил на CONFIG GET (проверьте пароль и что контейнер запущен)" >&2
		return 1
	}
	used=$(current_used_memory) || {
		redis_msg "Redis в ${REDIS_CONTAINER} не отдал INFO memory" >&2
		return 1
	}
	rss=$(current_rss) || {
		redis_msg "Redis в ${REDIS_CONTAINER} не отдал used_memory_rss" >&2
		return 1
	}
	read -r cur_mem cur_swap <<<"$(current_container_limits)"
	cmd_max=$(command_maxmemory_bytes)
	redis_msg "контейнер ${REDIS_CONTAINER}"
	redis_msg "maxmemory: сейчас $(human "$cur_max"), цель $(human "$want_max")"
	if (( cmd_max == 0 )); then
		redis_msg "maxmemory в command контейнера: не найден"
	else
		redis_msg "maxmemory в command контейнера: $(human "$cmd_max")"
	fi
	redis_msg "лимит памяти контейнера: сейчас $(human "$cur_mem"), цель $(human "$want_mem")"
	redis_msg "memswap контейнера: сейчас $(human "$cur_swap"), цель $(human "$want_mem") (без swap)"
	redis_msg "used_memory: $(human "$used"), used_memory_rss: $(human "$rss")"
}

# Назначение: объяснить срок жизни значений и показать расхождение с command контейнера.
# Параметры: $1 — maxmemory, который действует сейчас (в режиме записи — цель).
# Результат: строки на stdout. CONFIG SET maxmemory действует до перезапуска процесса Redis,
# docker update сохраняется.
# Ошибки: нет.
report_lifetime() {
	local effective=$1
	redis_msg "CONFIG SET maxmemory действует до перезапуска процесса Redis: рестарт контейнера или перезагрузка сервера вернут значение из command контейнера; лимиты после docker update сохраняются"
	if (( cmd_max != 0 && cmd_max != effective )); then
		redis_msg "расхождение: maxmemory сейчас $(human "$effective"), в command контейнера $(human "$cmd_max"); после перезапуска Redis вернётся $(human "$cmd_max")"
		redis_msg "повторите этот скрипт после перезагрузки сервера; постоянное решение — пересоздание контейнера Redis с новым command, только с согласия пользователя"
	fi
}

# Назначение: проверить, что лимит контейнера можно снижать (или впервые ставить).
# Параметры: нет (переменные inspect_state).
# Результат: код 0, если цель с запасом выше потребления.
# Ошибки: docker stats не отдал данные или цель не выше max(RSS Redis, docker stats) с запасом
# REDIS_SHRINK_HEADROOM_PERCENT — сообщение на stderr и код 1.
guard_container_limit() {
	local usage peak
	usage=$(stats_usage_bytes) || {
		redis_msg "docker stats не отдал потребление контейнера: снижать лимит памяти небезопасно, отказ" >&2
		return 1
	}
	peak=$usage
	if (( rss > peak )); then
		peak=$rss
	fi
	if (( want_mem * 100 < peak * (100 + REDIS_SHRINK_HEADROOM_PERCENT) )); then
		redis_msg "лимит контейнера $(human "$want_mem") ниже потребления $(human "$peak") (RSS Redis $(human "$rss"), docker stats $(human "$usage")) с запасом ${REDIS_SHRINK_HEADROOM_PERCENT}%: отказ" >&2
		return 1
	fi
}

# Назначение: выставить лимит памяти и memswap контейнера без пересоздания.
# Параметры: нет (переменные inspect_state).
# Результат: docker update и строка на stdout.
# Ошибки: docker update не выполнился — сообщение на stderr и код 1.
set_container_limit() {
	if ! docker update --memory "$want_mem" --memory-swap "$want_mem" "$REDIS_CONTAINER" >/dev/null; then
		redis_msg "docker update не выполнился" >&2
		return 1
	fi
	redis_msg "лимит памяти контейнера: $(human "$want_mem")"
}

# Назначение: выставить maxmemory Redis.
# Параметры: нет (переменные inspect_state).
# Результат: CONFIG SET и строка на stdout.
# Ошибки: CONFIG SET не выполнился — сообщение на stderr и код 1.
set_maxmemory() {
	if ! rcli CONFIG SET maxmemory "$want_max" >/dev/null; then
		redis_msg "CONFIG SET maxmemory не выполнился" >&2
		return 1
	fi
	redis_msg "maxmemory: $(human "$want_max")"
}

# Назначение: применить цели без пересоздания контейнера.
# Параметры: нет (переменные inspect_state).
# Результат: CONFIG SET maxmemory и docker update, только если значение отличается. При росте
# maxmemory лимит контейнера поднимается первым, при снижении maxmemory снижается первым.
# Перед снижением лимита контейнера цель сверяется с RSS Redis и docker stats с запасом 20%.
# Ошибки: maxmemory ниже занятой памяти Redis или лимит ниже потребления с запасом — отказ с кодом 1 без записи;
# сбой второго шага — «применено частично, повторите» и код 1.
apply_state() {
	local need_max=0 need_mem=0 done_any=0
	(( cur_max != want_max )) && need_max=1
	if (( cur_mem != want_mem || cur_swap != want_mem )); then
		need_mem=1
	fi
	if (( need_max == 0 && need_mem == 0 )); then
		redis_msg "значения уже совпадают с целью, менять нечего"
		report_lifetime "$cur_max"
		return 0
	fi
	if (( need_max == 1 && want_max < used )); then
		redis_msg "maxmemory $(human "$want_max") ниже занятой памяти $(human "$used"): запись в Redis встанет, отказ" >&2
		return 1
	fi
	if (( need_mem == 1 && (cur_mem == 0 || want_mem < cur_mem) )); then
		guard_container_limit || return 1
	fi
	if (( want_max > cur_max && need_mem == 1 )); then
		if ! set_container_limit; then
			partial_notice 0
			return 1
		fi
		done_any=1
		need_mem=0
	fi
	if (( need_max == 1 )); then
		if ! set_maxmemory; then
			partial_notice "$done_any"
			return 1
		fi
		done_any=1
	fi
	if (( need_mem == 1 )); then
		if ! set_container_limit; then
			partial_notice "$done_any"
			return 1
		fi
	fi
	redis_msg "готово"
	report_lifetime "$want_max"
}

# Назначение: сообщить о неполном применении после сбоя второго шага.
# Параметры: $1 — 1, если хотя бы один шаг уже выполнен.
# Результат: строка на stderr: при 1 «применено частично, повторите», иначе «ничего не применено».
# Ошибки: нет.
partial_notice() {
	if (( $1 == 1 )); then
		redis_msg "применено частично, повторите запуск скрипта: выполненные шаги он пропустит" >&2
	else
		redis_msg "ничего не применено, повторите запуск скрипта" >&2
	fi
}

# Назначение: точка входа.
# Параметры: аргументы скрипта.
# Результат: код 0 при успехе или в режиме --check.
# Ошибки: см. inspect_state и apply_state.
main() {
	local want_max want_mem cur_max cur_mem cur_swap used rss cmd_max
	parse_args "$@"
	inspect_state
	if (( REDIS_CHECK_MODE == 1 )); then
		if (( cur_max == want_max && cur_mem == want_mem && cur_swap == want_mem )); then
			redis_msg "--check: изменения не нужны"
		else
			redis_msg "--check: изменения нужны, запись не выполнялась"
		fi
		report_lifetime "$cur_max"
		return 0
	fi
	apply_state
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
	main "$@"
fi
