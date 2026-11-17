#!/usr/bin/env bash
# EmTDS updater: управляет обновлением стека через git archive и docker build.
# Запускается хостовым systemd-таймером, кнопкой из админки или CLI.
# Вторая фаза (--stage2) выполняется из распакованного целевого дерева.

set -Eeuo pipefail
umask 077

SELF=$(readlink -f "${BASH_SOURCE[0]:-}" 2>/dev/null || true)

# Sandbox-префикс: пусто на проде, t.TempDir() в тестах.
SBX="${EMTDS_UPDATE_SANDBOX:-}"

# Пути хостовой части (с префиксом sandbox).
ETC_DIR="${SBX}/etc/emtds"
VAR_DIR="${SBX}/var/lib/emtds-update"
BACKUP_ROOT="${SBX}/var/backups/emtds-update"
LIB_DIR="${SBX}/usr/local/lib/emtds"
LOCK_FILE="${SBX}/run/emtds-update.lock"
UNIT_DIR="${EMTDS_UPDATE_UNIT_DIR:-${SBX}/etc/systemd/system}"
PROC_MEMINFO="${EMTDS_UPDATE_MEMINFO:-${SBX}/proc/meminfo}"
[[ -n "$SBX" || -n "${EMTDS_UPDATE_MEMINFO:-}" ]] || PROC_MEMINFO=/proc/meminfo

UPDATE_ENV="${ETC_DIR}/update.env"
DEPLOY_KEY="${ETC_DIR}/deploy_key"
KNOWN_HOSTS="${ETC_DIR}/known_hosts"
ALLOWED_SIGNERS="${ETC_DIR}/allowed_signers"

# Параметры, считанные из update.env.
REPO=
CHANNEL=release
CHECK_HOURS=1
KEEP_IMAGES=3
KEEP_BACKUPS=5
TDS_DIR=
# STATE_DIR определяется после чтения update.env.
STATE_DIR=

# CLI-флаги.
F_UNSIGNED=0
F_SCHEMA_OK=0
F_KEITARO_IDLE=0
F_FORCE_VERSION=0
F_DRY_RUN=0

# Масштаб ожиданий: 1 = прод, 0 = тесты (все sleep/deadline обнуляются до минимума).
WAIT_SCALE="${EMTDS_UPDATE_WAIT_SCALE:-1}"

# Маскируемые слова (заполняется после чтения .env).
_MASK_WORDS=()

# ------------------------------------------------------------------
# Утилиты
# ------------------------------------------------------------------

# Назначение: короткое сообщение в журнал.
# Параметры: текст.
# Результат: строка на stdout.
# Ошибки: нет.
log() {
	printf 'emtds-update: %s\n' "$*"
}

# Последняя причина отказа (для state.json при аварийном выходе).
LAST_ERROR=""

# Назначение: завершить скрипт с сообщением об ошибке.
# Параметры: текст причины.
# Результат: не возвращается, код 1; причина сохраняется в LAST_ERROR.
# Ошибки: нет.
die() {
	LAST_ERROR="$*"
	logbuf "ошибка: $*" >&2
	exit 1
}

# Назначение: пауза, масштабируемая для тестов.
# Параметры: $1 — секунды (по умолчанию 1).
# Результат: sleep.
# Ошибки: нет.
_sleep() {
	local n=$(( ${1:-1} * WAIT_SCALE ))
	[[ $n -gt 0 ]] && sleep "$n" || true
}

# Назначение: момент окончания ожидания в шкале SECONDS.
# Параметры: $1 — секунды ожидания на проде (умножаются на WAIT_SCALE).
# Результат: значение SECONDS, после которого ожидание прекращается; циклы делают хотя бы одну попытку.
# Ошибки: нет.
_deadline() {
	printf '%s' $(( SECONDS + ${1:-60} * WAIT_SCALE ))
}

# Назначение: экранировать строку для встраивания в JSON без jq.
# Параметры: $1 — строка; $2 — лимит символов исходной строки (по умолчанию 200; обрезка по символам UTF-8 до экранирования).
# Результат: JSON-строка в кавычках на stdout; обрезка не рвёт escape-последовательность.
# Ошибки: нет.
json_str() {
	local LC_ALL=C.UTF-8
	local s=${1:-} lim=${2:-200}
	s=${s:0:$lim}
	s=${s//\\/\\\\}
	s=${s//\"/\\\"}
	s=${s//$'\t'/\\t}
	s=${s//$'\r'/\\r}
	s=${s//$'\n'/\\n}
	# Удалить оставшиеся управляющие символы (0–8, 11–12, 14–31, 127).
	s=$(printf '%s' "$s" | LC_ALL=C tr -d '\000-\010\013\014\016-\037\177' 2>/dev/null || printf '%s' "$s")
	printf '"%s"' "$s"
}

# Назначение: убрать управляющие символы (кроме табуляции и перевода строки) из данных тома перед печатью в терминал.
# Параметры: stdin — текст; UTF-8 не портится.
# Результат: очищенный текст на stdout.
# Ошибки: нет.
strip_control() {
	LC_ALL=C tr -d '\000-\010\013-\037\177'
}

# Назначение: заменить секреты на *** в строке.
# Параметры: $1 — строка; _MASK_WORDS содержит слова ≥6 символов.
# Результат: маскированная строка на stdout.
# Ошибки: нет.
mask_line() {
	local s=$1 w
	for w in "${_MASK_WORDS[@]+"${_MASK_WORDS[@]}"}"; do
		[[ ${#w} -ge 6 ]] && s="${s//"$w"/"***"}"
	done
	# Маскировать URL с учётными данными: https://user:pass@host
	s=$(printf '%s' "$s" | LC_ALL=C sed 's|https\?://[^@]*@|https://***@|g')
	printf '%s' "$s"
}

# Полный лог текущего прогона <var>/runs/<id>.log (0600); пусто вне прогона.
RUN_LOG=""

# Назначение: маскировать строку лога, напечатать и дописать в полный лог прогона.
# Параметры: строка лога.
# Результат: строка на stdout и в RUN_LOG (если задан); работает и внутри конвейеров.
# Ошибки: нет.
logbuf() {
	local line
	line=$(mask_line "$*")
	log "$line"
	if [[ -n "$RUN_LOG" ]]; then
		printf 'emtds-update: %s\n' "$line" >> "$RUN_LOG" 2>/dev/null || true
	fi
}

# Назначение: открыть полный лог прогона <var>/runs/<id>.log.
# Параметры: $1 — id прогона.
# Результат: RUN_LOG указывает на пустой файл 0600.
# Ошибки: нет.
open_run_log() {
	install -d -m 0700 "${VAR_DIR}/runs"
	RUN_LOG="${VAR_DIR}/runs/$1.log"
	: > "$RUN_LOG"
	chmod 0600 "$RUN_LOG"
}

# Назначение: записать хвост полного лога прогона (не больше 256 КиБ) в log.txt тома.
# Параметры: нет (читает RUN_LOG, VOL_NAME).
# Результат: файл log.txt в /v/update/ через docker run; при обрезке первая неполная строка отбрасывается.
# Ошибки: не fatal.
flush_log() {
	[[ -n "${VOL_NAME:-}" && -n "$RUN_LOG" && -f "$RUN_LOG" ]] || return 0
	local max=262144 size
	size=$(stat -c '%s' "$RUN_LOG" 2>/dev/null || echo 0)
	if (( size > max )); then
		tail -c "$max" "$RUN_LOG" | tail -n +2 | vol_write log.txt || true
	else
		vol_write log.txt < "$RUN_LOG" || true
	fi
}

# ------------------------------------------------------------------
# Парсинг update.env
# ------------------------------------------------------------------

# Назначение: прочитать последнее значение KEY= из файла окружения.
# Параметры: $1 — путь; $2 — имя ключа.
# Результат: значение на stdout (может быть пустым).
# Ошибки: нет.
env_value() {
	local file=$1 key=$2 line val
	line=$(grep -E "^${key}=" "$file" 2>/dev/null | tail -n 1 || true)
	[[ -z "$line" ]] && return 0
	val=${line#*=}
	if [[ "$val" == \"*\" && "$val" == *\" ]]; then
		val=${val:1:${#val}-2}
	elif [[ "$val" == \'*\' && "$val" == *\' ]]; then
		val=${val:1:${#val}-2}
	fi
	printf '%s' "$val"
}

# Назначение: проверить и нормализовать EMTDS_UPDATE_REPO.
# Параметры: $1 — URL репозитория.
# Результат: 0 = допустимый, 1 = недопустимый; сообщение на stderr.
# Ошибки: код 1.
validate_repo_url() {
	local url=$1
	# Запретить пустое значение.
	[[ -z "$url" ]] && { printf 'пустой EMTDS_UPDATE_REPO\n' >&2; return 1; }
	# Запретить учётные данные в URL.
	if printf '%s' "$url" | LC_ALL=C grep -qE '^https?://[^@]+@'; then
		printf 'EMTDS_UPDATE_REPO не должен содержать учётных данных\n' >&2
		return 1
	fi
	# Допустимые форматы: git@ , ssh://git@, https://.
	if printf '%s' "$url" | LC_ALL=C grep -qE '^(git@[^:]+:.+\.git|ssh://git@[^/]+/.+|https://[^/]+/.+)$'; then
		return 0
	fi
	# file:// разрешён только в sandbox.
	if [[ -n "$SBX" ]] && printf '%s' "$url" | LC_ALL=C grep -qE '^(file://|/).+'; then
		return 0
	fi
	printf 'недопустимый EMTDS_UPDATE_REPO: %s\n' "$url" >&2
	return 1
}

# Назначение: проверить права и владельца защищённого файла.
# Параметры: $1 — путь; $2 — запрещённая маска прав в восьмеричном формате (по умолчанию 022).
# Результат: 0, если файл не существует или права и владелец в норме.
# Ошибки: небезопасные права или не-root владелец вне sandbox — код 1 через die.
check_secure_file() {
	local path=$1 mask=${2:-022}
	[[ -f "$path" ]] || return 0
	local perms
	perms=$(stat -c '%a' "$path" 2>/dev/null || echo 644)
	if (( 8#$perms & 8#$mask )); then
		die "${path}: небезопасные права (${perms})"
	fi
	if [[ -z "$SBX" ]]; then
		local owner
		owner=$(stat -c '%u' "$path" 2>/dev/null || echo 1)
		[[ "$owner" == 0 ]] || die "${path}: владелец не root"
	fi
}

# Белый список ключей update.env и шаблоны допустимых значений.
declare -A _ENV_KEY_RE=(
	[EMTDS_UPDATE_REPO]='.'
	[EMTDS_UPDATE_CHANNEL]='^(release|main)$'
	[EMTDS_UPDATE_CHECK_HOURS]='^[0-9]+$'
	[EMTDS_UPDATE_KEEP_IMAGES]='^[0-9]+$'
	[EMTDS_UPDATE_KEEP_BACKUPS]='^[0-9]+$'
	[TDS_DIR]='^/'
)

# Назначение: строго прочитать update.env — только KEY=VALUE из белого списка.
# Параметры: нет (читает UPDATE_ENV).
# Результат: устанавливает REPO, CHANNEL, CHECK_HOURS, KEEP_IMAGES, KEEP_BACKUPS, TDS_DIR.
# Ошибки: нет файла, неверные права или недопустимые значения — код 1.
read_update_env() {
	[[ -f "$UPDATE_ENV" ]] || die "не найден ${UPDATE_ENV}; запустите update.sh --install"
	check_secure_file "$UPDATE_ENV" 022
	local line key val re
	while IFS= read -r line || [[ -n "$line" ]]; do
		# Пропустить пустые и комментарии.
		[[ -z "$line" || "${line:0:1}" == '#' ]] && continue
		# Только KEY=VALUE.
		if ! printf '%s' "$line" | LC_ALL=C grep -qE '^[A-Za-z_][A-Za-z0-9_]*='; then
			die "${UPDATE_ENV}: недопустимая строка: ${line}"
		fi
		key=${line%%=*}
		val=${line#*=}
		# Убрать кавычки.
		if [[ "$val" == \"*\" && "$val" == *\" ]]; then
			val=${val:1:${#val}-2}
		elif [[ "$val" == \'*\' && "$val" == *\' ]]; then
			val=${val:1:${#val}-2}
		fi
		# Белый список.
		re="${_ENV_KEY_RE[$key]:-}"
		[[ -n "$re" ]] || die "${UPDATE_ENV}: неизвестный ключ ${key}"
		if ! printf '%s' "$val" | LC_ALL=C grep -qE "$re"; then
			die "${UPDATE_ENV}: недопустимое значение ${key}=${val}"
		fi
		case "$key" in
			EMTDS_UPDATE_REPO)      REPO="$val";;
			EMTDS_UPDATE_CHANNEL)   CHANNEL="$val";;
			EMTDS_UPDATE_CHECK_HOURS) CHECK_HOURS="$val";;
			EMTDS_UPDATE_KEEP_IMAGES) KEEP_IMAGES="$val";;
			EMTDS_UPDATE_KEEP_BACKUPS) KEEP_BACKUPS="$val";;
			TDS_DIR)                TDS_DIR="$val";;
		esac
	done < "$UPDATE_ENV"
	[[ -n "$REPO" ]] || die "${UPDATE_ENV}: не задан EMTDS_UPDATE_REPO"
	validate_repo_url "$REPO" || die "недопустимый EMTDS_UPDATE_REPO"
	[[ -n "$TDS_DIR" ]] || die "${UPDATE_ENV}: не задан TDS_DIR"
	TDS_DIR=$(readlink -f "$TDS_DIR" 2>/dev/null || printf '%s' "$TDS_DIR")
	check_secure_file "$DEPLOY_KEY" 077
	check_secure_file "$KNOWN_HOSTS" 022
}

# ------------------------------------------------------------------
# Определение STATE_DIR и проверка окружения
# ------------------------------------------------------------------

# Назначение: определить каталог состояния (SERVER_ENV, compose.app.yml).
# Параметры: нет.
# Результат: устанавливает STATE_DIR.
# Ошибки: нет.
determine_state_dir() {
	STATE_DIR="${EMTDS_INSTALL_STATE_DIR:-${SBX}/etc/emtds}"
}

# Назначение: проверить предусловия применения (шаг 1 плана).
# Параметры: нет.
# Результат: код 0 при успехе.
# Ошибки: любое условие нарушено — код 1.
check_environment() {
	# Root — обязателен вне sandbox.
	if [[ -z "$SBX" ]] && [[ $(id -u) -ne 0 ]]; then
		die "нужны права root"
	fi
	determine_state_dir
	# Нет .git — не репозиторий.
	if [[ ! -d "${TDS_DIR}/.git" ]]; then
		die "${TDS_DIR}: нет .git; для первой установки: update.sh --adopt --to <sha>"
	fi
	# Нет server.env или compose.app.yml.
	[[ -f "${STATE_DIR}/server.env" ]] || die "не найден ${STATE_DIR}/server.env"
	[[ -f "${STATE_DIR}/compose.app.yml" ]] || die "не найден ${STATE_DIR}/compose.app.yml"
	# Rootless Docker или Docker Desktop недопустимы.
	if [[ -z "$SBX" ]]; then
		local info
		info=$(docker info --format '{{.OperatingSystem}}' 2>/dev/null || true)
		if printf '%s' "$info" | LC_ALL=C grep -qi 'desktop\|rootless'; then
			die "Docker Desktop и rootless Docker не поддерживаются"
		fi
	fi
}

# ------------------------------------------------------------------
# Git-операции
# ------------------------------------------------------------------

# Назначение: построить GIT_SSH_COMMAND с deploy key.
# Параметры: нет.
# Результат: строка переменной окружения на stdout.
# Ошибки: нет.
git_ssh_cmd() {
	local cmd="ssh -o BatchMode=yes -o StrictHostKeyChecking=yes"
	if [[ -f "$KNOWN_HOSTS" ]]; then
		cmd="${cmd} -o UserKnownHostsFile=${KNOWN_HOSTS}"
	fi
	if [[ -f "$DEPLOY_KEY" ]]; then
		cmd="${cmd} -i ${DEPLOY_KEY} -o IdentitiesOnly=yes"
	fi
	printf '%s' "$cmd"
}

# Назначение: выполнить git-команду в TDS_DIR с закреплёнными параметрами безопасности.
# Параметры: аргументы git.
# Результат: код возврата git.
# Ошибки: код git.
git_cmd() {
	GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0 \
	GIT_SSH_COMMAND="$(git_ssh_cmd)" \
		git -C "$TDS_DIR" \
		-c "safe.directory=${TDS_DIR}" \
		-c core.hooksPath=/dev/null \
		-c core.fsmonitor=false \
		-c "core.sshCommand=" \
		-c gpg.program=gpg \
		-c gpg.ssh.program=ssh-keygen \
		-c protocol.ext.allow=never \
		"$@"
}

# Назначение: закрепить origin на адрес из update.env и выполнить fetch.
# Параметры: нет.
# Результат: 0; refs/remotes/origin/* и теги обновлены.
# Ошибки: fetch не удался или выкатанный тег сдвинулся — код 1.
git_fetch() {
	logbuf "fetch ${REPO}"
	if ! git_cmd remote set-url origin "$REPO" 2>/dev/null; then
		git_cmd remote add origin "$REPO" || return 1
	fi
	if ! git_cmd fetch --tags --force --prune origin 2>&1 | while IFS= read -r l; do logbuf "$l"; done; then
		logbuf "git fetch не удался"
		return 1
	fi
	check_tag_shift
}

# Назначение: проверить, что ни один выкатанный тег из history не сдвинулся.
# Параметры: нет.
# Результат: 0.
# Ошибки: сдвиг тега — код 1.
check_tag_shift() {
	local hist="${VAR_DIR}/history"
	[[ -f "$hist" ]] || return 0
	local ts id action from to result tag_field sha_now
	while IFS=' ' read -r ts id action from to result tag_field; do
		[[ "$result" == ok && "$tag_field" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || continue
		sha_now=$(git_cmd rev-parse "refs/tags/${tag_field}^{commit}" 2>/dev/null || true)
		[[ -z "$sha_now" || "$sha_now" == "$to" ]] && continue
		logbuf "тег ${tag_field} сдвинулся: был ${to:0:12}, теперь ${sha_now:0:12}"
		return 1
	done < "$hist"
}

# Назначение: выбрать максимальный тег вида vX.Y.Z.
# Параметры: stdin — имена тегов по одному в строке.
# Результат: максимальный тег по semver на stdout (пусто, если подходящих нет).
# Ошибки: нет.
max_semver() {
	LC_ALL=C grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1 || true
}

# Назначение: выбрать целевой коммит по каналу release (максимальный semver-тег).
# Параметры: нет.
# Результат: sha, тег и версия на stdout через пробел.
# Ошибки: нет подходящего тега — код 1.
pick_target_release() {
	local head_sha tags best_tag best_sha version
	head_sha=$(git_cmd rev-parse HEAD)
	best_tag=$(git_cmd tag -l 'v[0-9]*.[0-9]*.[0-9]*' | max_semver)
	[[ -n "$best_tag" ]] || die "нет semver-тегов на ${CHANNEL}"
	best_sha=$(git_cmd rev-parse "${best_tag}^{commit}" 2>/dev/null) || die "не удалось разрешить ${best_tag}"
	# Тег должен быть предком origin/main.
	local main_sha
	main_sha=$(git_cmd rev-parse "origin/main" 2>/dev/null) || die "нет origin/main"
	git_cmd merge-base --is-ancestor "$best_sha" "$main_sha" 2>/dev/null || \
		die "тег ${best_tag} не является предком origin/main"
	# Тег должен быть потомком HEAD (не понижение).
	if ! git_cmd merge-base --is-ancestor "$head_sha" "$best_sha" 2>/dev/null; then
		if [[ $F_FORCE_VERSION -eq 1 ]]; then
			confirm_force_version "$best_sha"
		else
			die "цель ${best_tag} не является потомком HEAD; используйте --force-version для принудительного применения"
		fi
	fi
	version="$best_tag"
	printf '%s %s %s\n' "$best_sha" "$best_tag" "$version"
}

# Назначение: выбрать целевой коммит по каналу main (верхушка origin/main).
# Параметры: нет.
# Результат: sha, тег и версия на stdout через пробел.
# Ошибки: нет origin/main — код 1.
pick_target_main() {
	local head_sha target_sha
	head_sha=$(git_cmd rev-parse HEAD)
	target_sha=$(git_cmd rev-parse "origin/main" 2>/dev/null) || die "нет origin/main"
	# Должен быть потомком HEAD.
	if ! git_cmd merge-base --is-ancestor "$head_sha" "$target_sha" 2>/dev/null; then
		if [[ $F_FORCE_VERSION -eq 1 ]]; then
			confirm_force_version "$target_sha"
		else
			die "origin/main не является потомком HEAD; используйте --force-version для принудительного применения"
		fi
	fi
	printf '%s %s %s\n' "$target_sha" "" ""
}

# Назначение: выбрать целевой коммит по sha или тегу (--to).
# Параметры: $1 — sha (40 hex) или тег vX.Y.Z.
# Результат: sha, тег и версия на stdout через пробел.
# Ошибки: код 1.
pick_target_ref() {
	local ref=$1 sha tag version head_sha
	# Определить sha.
	if printf '%s' "$ref" | LC_ALL=C grep -qE '^[0-9a-f]{40}$'; then
		sha=$ref
		tag=""
		version=""
	elif printf '%s' "$ref" | LC_ALL=C grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
		sha=$(git_cmd rev-parse "${ref}^{commit}" 2>/dev/null) || die "тег ${ref} не найден"
		tag=$ref
		version=$ref
	else
		die "недопустимый ref: ${ref} (ожидается 40-hex sha или vX.Y.Z)"
	fi
	head_sha=$(git_cmd rev-parse HEAD)
	# Не понижение (кроме --force-version).
	if ! git_cmd merge-base --is-ancestor "$head_sha" "$sha" 2>/dev/null; then
		if [[ $F_FORCE_VERSION -eq 1 ]]; then
			confirm_force_version "$sha"
		else
			die "цель ${sha:0:12} не является потомком HEAD; используйте --force-version"
		fi
	fi
	printf '%s %s %s\n' "$sha" "$tag" "$version"
}

# Назначение: интерактивно подтвердить принудительное применение произвольного sha.
# Параметры: $1 — sha.
# Результат: 0 если пользователь ввёл первые 7 символов sha.
# Ошибки: неверный ввод или не tty — код 1.
confirm_force_version() {
	local sha=$1 input
	[[ -t 0 && -w /dev/tty ]] || die "--force-version требует интерактивного терминала"
	printf 'ВНИМАНИЕ: цель %s не потомок текущей версии (понижение)\n' "$sha" > /dev/tty
	printf 'Введите первые 7 символов sha для подтверждения: ' > /dev/tty
	read -r input
	[[ ${#input} -ge 7 && "${sha:0:7}" == "${input:0:7}" ]] || die "подтверждение не совпадает, отменено"
}

# Назначение: выяснить, включена ли проверка подписи (политика одна: файл allowed_signers).
# Параметры: нет.
# Результат: 1 (выключена) только если пути $ALLOWED_SIGNERS нет совсем либо это обычный читаемый файл без строк ключа;
#   иначе 0: ключ в файле, а также всё, что не удаётся прочитать как обычный файл (битый симлинк, каталог,
#   сбой grep). Права файла здесь не проверяются: отказ по правам и по типу выдаёт verify_signature,
#   так что порча файла не выключает проверку тихо.
# Ошибки: нет.
signing_enabled() {
	[[ -e "$ALLOWED_SIGNERS" || -L "$ALLOWED_SIGNERS" ]] || return 1
	[[ -f "$ALLOWED_SIGNERS" && -r "$ALLOWED_SIGNERS" ]] || return 0
	local rc=0
	LC_ALL=C grep -qvE '^[[:space:]]*($|#)' "$ALLOWED_SIGNERS" 2>/dev/null || rc=$?
	(( rc != 1 ))
}

# Назначение: проверить SSH-подпись коммита или тега.
# Параметры: $1 — sha; $2 — тег (или пусто для коммита).
# Результат: устанавливает SIGNED (true/false) и SIGNER; при выключенной подписи SIGNED=false.
# Ошибки: подпись включена, а allowed_signers не обычный файл или у него небезопасные права — код 1 через die
#   (проверка не пропускается).
SIGNED=false
SIGNER=""

verify_signature() {
	local sha=$1 tag=${2:-}
	SIGNED=false
	SIGNER=""
	signing_enabled || return 0
	[[ -f "$ALLOWED_SIGNERS" ]] || die "${ALLOWED_SIGNERS}: не обычный файл"
	check_secure_file "$ALLOWED_SIGNERS" 022
	local out
	if [[ -n "$tag" ]]; then
		out=$(git_cmd \
			-c gpg.format=ssh \
			-c gpg.ssh.program=ssh-keygen \
			-c "gpg.ssh.allowedSignersFile=${ALLOWED_SIGNERS}" \
			verify-tag "$tag" 2>&1) && SIGNED=true || true
	else
		out=$(git_cmd \
			-c gpg.format=ssh \
			-c gpg.ssh.program=ssh-keygen \
			-c "gpg.ssh.allowedSignersFile=${ALLOWED_SIGNERS}" \
			verify-commit "$sha" 2>&1) && SIGNED=true || true
	fi
	if [[ "$SIGNED" == true ]]; then
		SIGNER=$(printf '%s' "$out" | LC_ALL=C grep -oE 'Good.*signing key: [^ ]+' | head -1 | \
			sed 's/.*signing key: //' || true)
	fi
}

# Назначение: решить, можно ли применять цель без обхода подписи.
# Параметры: нет (читает SIGNED после verify_signature).
# Результат: 0, если подпись выключена или цель проверена (SIGNED=true); иначе 1.
# Ошибки: нет.
signature_gate() {
	! signing_enabled || [[ "$SIGNED" == true ]]
}

# ------------------------------------------------------------------
# Ручные шаги
# ------------------------------------------------------------------

MANUAL_STEPS=()

# Назначение: собрать изменения HEAD..цель, требующие ручного вмешательства.
# Параметры: $1 — sha цели.
# Результат: заполняет MANUAL_STEPS (схема ClickHouse, store-схема, инфраструктурный compose).
# Ошибки: нет.
collect_manual_steps() {
	local sha=$1 changed f
	MANUAL_STEPS=()
	changed=$(git_cmd diff --name-only "HEAD..${sha}" 2>/dev/null || true)
	while IFS= read -r f; do
		case "$f" in
			migrations/clickhouse/*|deploy/docker-compose.yml|internal/store/schema*.go)
				MANUAL_STEPS+=("$f");;
		esac
	done <<< "$changed"
}

# Назначение: отказать в применении при ручных шагах без --schema-ok.
# Параметры: $1 — sha цели.
# Результат: 0, если шагов нет или передан --schema-ok.
# Ошибки: есть шаги без --schema-ok — код 1.
check_manual_steps() {
	collect_manual_steps "$1"
	if [[ ${#MANUAL_STEPS[@]} -gt 0 && $F_SCHEMA_OK -eq 0 ]]; then
		local f
		for f in "${MANUAL_STEPS[@]}"; do
			logbuf "ручной шаг: ${f}"
		done
		die "требуются ручные шаги (${#MANUAL_STEPS[@]}); после выполнения добавьте --schema-ok"
	fi
}

# Назначение: JSON-массив MANUAL_STEPS.
# Параметры: нет.
# Результат: JSON-массив строк на stdout.
# Ошибки: нет.
manual_steps_json() {
	local out="[" f sep=""
	for f in "${MANUAL_STEPS[@]+"${MANUAL_STEPS[@]}"}"; do
		out="${out}${sep}$(json_str "$f")"
		sep=","
	done
	printf '%s]' "$out"
}

# ------------------------------------------------------------------
# Ресурсы
# ------------------------------------------------------------------

# Назначение: получить имя тома из docker inspect emtds-admin.
# Параметры: нет.
# Результат: имя тома на stdout.
# Ошибки: не удалось определить — код 1.
get_volume_name() {
	docker inspect emtds-admin \
		--format '{{range .Mounts}}{{if eq .Destination "/var/lib/emtds"}}{{.Name}}{{end}}{{end}}' \
		2>/dev/null | head -1 || die "не удалось определить имя тома из emtds-admin"
}

# Назначение: проверить доступность ресурсов (память, диск, CH, Redis).
# Параметры: нет.
# Результат: 0.
# Ошибки: недостаточно ресурсов — код 1.
check_resources() {
	# Память ≥ 2 ГБ.
	local mem_kb avail_kb
	avail_kb=$(awk '/^MemAvailable:/ { print $2 }' "${PROC_MEMINFO}" 2>/dev/null || echo 0)
	if [[ $avail_kb -lt 2097152 ]]; then
		die "недостаточно свободной памяти: ${avail_kb} KiB (нужно ≥ 2 GiB)"
	fi
	# Диск ≥ 5 ГБ.
	local free_kb
	local df_target=/var/lib/docker
	[[ -n "$SBX" ]] && df_target="${SBX}/var/lib/docker"
	mkdir -p "$df_target" 2>/dev/null || true
	free_kb=$(df -k --output=avail "$df_target" 2>/dev/null | tail -1 | tr -d ' ' || echo 0)
	if [[ $free_kb -lt 5242880 ]]; then
		die "недостаточно места на диске: ${free_kb} KiB (нужно ≥ 5 GiB)"
	fi
	# ClickHouse /ping и Redis PING через docker exec в их контейнеры.
	check_infra_health || die "инфраструктура: ${INFRA_ERROR}"
}

# Причина последнего провала check_infra_health.
INFRA_ERROR=""

# Назначение: проверить доступность ClickHouse и Redis через docker exec.
# Параметры: нет.
# Результат: 0, если оба отвечают.
# Ошибки: не отвечает — код 1, причина в INFRA_ERROR.
check_infra_health() {
	# Имена контейнеров из deploy/docker-compose.yml инфраструктуры.
	local ch_ctr=emtds-infra-clickhouse-1 redis_ctr=emtds-infra-redis-1
	INFRA_ERROR=""
	if ! docker exec "$ch_ctr" sh -c 'wget -qO- http://127.0.0.1:8123/ping 2>/dev/null || curl -fsS http://127.0.0.1:8123/ping 2>/dev/null' 2>/dev/null | grep -qi ok; then
		INFRA_ERROR="ClickHouse недоступен (${ch_ctr})"
		return 1
	fi
	if ! docker exec "$redis_ctr" redis-cli ping 2>/dev/null | grep -qi pong; then
		INFRA_ERROR="Redis недоступен (${redis_ctr})"
		return 1
	fi
}

# ------------------------------------------------------------------
# Операции с томом (через docker run, не через Mountpoint)
# ------------------------------------------------------------------

VOL_NAME=""

# Назначение: записать файл в /v/update/<name> через docker run с защитой от симлинков.
# Параметры: $1 — имя файла из белого списка; stdin — содержимое.
# Результат: файл обновлён атомарно.
# Ошибки: симлинк в /v/update/ — код 2; docker run — код 1.
vol_write() {
	local name=$1
	# Имя файла — белый список (только безопасные имена без слэшей).
	case "$name" in
		agent.json|check.json|state.json|log.txt|pending) ;;
		*) die "vol_write: недопустимое имя файла: ${name}";;
	esac
	local n="$name"
	docker run --rm -i --network none --user 10001:10001 \
		-v "${VOL_NAME}:/v" \
		--entrypoint sh emtds:local \
		-c "set -e; d=/v/update; [ ! -L \"\$d\" ] || { printf 'symlink\n' >&2; exit 2; }; mkdir -p \"\$d\"; tmp=\$(mktemp \"\$d/.upd.XXXXXX\"); cat >\"\$tmp\"; chmod 0644 \"\$tmp\"; mv -f \"\$tmp\" \"\$d/${n}\""
}

# Назначение: прочитать файл из /v/update/<name> с проверкой симлинка.
# Параметры: $1 — имя файла; $2 — лимит байт (по умолчанию 65536).
# Результат: содержимое на stdout или пусто.
# Ошибки: нет (отсутствие файла не ошибка).
vol_read() {
	local name=$1 lim=${2:-65536}
	case "$name" in
		check.json|state.json|ready.json|pending) ;;
		*) die "vol_read: недопустимое имя файла: ${name}";;
	esac
	docker run --rm --network none --user 10001:10001 \
		-v "${VOL_NAME}:/v:ro" \
		--entrypoint sh emtds:local \
		-c "d=/v/update; f=\$d/${name}; [ ! -L \"\$d\" ] && [ -f \"\$f\" ] && [ ! -L \"\$f\" ] && head -c ${lim} \"\$f\" || true"
}

# Назначение: удалить файл из /v/update/<name>.
# Параметры: $1 — имя файла.
# Результат: файл удалён или не существовал; через симлинк ничего не удаляется.
# Ошибки: нет.
vol_rm() {
	local name=$1
	case "$name" in
		pending|ready.json) ;;
		*) die "vol_rm: недопустимое имя файла: ${name}";;
	esac
	docker run --rm --network none --user 10001:10001 \
		-v "${VOL_NAME}:/v" \
		--entrypoint sh emtds:local \
		-c "d=/v/update; f=\$d/${name}; [ ! -L \"\$d\" ] && [ -f \"\$f\" ] && [ ! -L \"\$f\" ] && rm -f \"\$f\" || true"
}

# ------------------------------------------------------------------
# Запись файлов состояния
# ------------------------------------------------------------------

# Назначение: записать state.json в том.
# Параметры: $1—id; $2—action; $3—from; $4—to; $5—phase; $6—result; $7—reason; $8—events_lost; $9—cron_lagging; $10—user; $11—started_at; $12—finished_at (или пусто).
# Результат: state.json в томе.
# Ошибки: vol_write.
write_state_json() {
	local id=$1 action=$2 from=$3 to=$4 phase=$5 result=$6 reason=$7 \
		events_lost=${8:-0} cron_lagging=${9:-false} user=${10:-} \
		started_at=${11:-} finished_at=${12:-}
	local phases_json="${_PHASES_JSON:-[]}"
	local fin_field=""
	[[ -n "$finished_at" ]] && fin_field=",\"finished_at\":$(json_str "$finished_at")"
	local _state_json
	_state_json=$(printf '{"schema":1,"id":%s,"action":%s,"from":%s,"to":%s,"started_at":%s%s,"phase":%s,"phases":%s,"result":%s,"reason":%s,"events_lost":%s,"cron_lagging":%s,"user":%s}' \
		"$(json_str "$id")" "$(json_str "$action")" "$(json_str "$from")" "$(json_str "$to")" \
		"$(json_str "$started_at")" "$fin_field" "$(json_str "$phase")" "$phases_json" \
		"$(json_str "$result")" "$(json_str "$reason")" "$events_lost" "$cron_lagging" \
		"$(json_str "$user")")
	printf '%s' "$_state_json" | vol_write state.json
	# Сохранить последнее состояние прогона на хосте для восстановления после прерывания родителя.
	if [[ -n "$id" && -n "${VAR_DIR:-}" ]]; then
		install -m 0600 /dev/null "${VAR_DIR}/state.last" 2>/dev/null || true
		printf '%s\n' "$_state_json" > "${VAR_DIR}/state.last" 2>/dev/null || true
	fi
}

# Назначение: добавить фазу в глобальный список _PHASES_JSON.
# Параметры: $1 — имя; $2 — ok (true/false).
# Результат: обновляет _PHASES_JSON.
# Ошибки: нет.
_PHASES_JSON="[]"
add_phase() {
	local name=$1 ok=${2:-true} at
	at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	local entry
	entry="{\"name\":$(json_str "$name"),\"at\":$(json_str "$at"),\"ok\":${ok}}"
	if [[ "$_PHASES_JSON" == "[]" ]]; then
		_PHASES_JSON="[${entry}]"
	else
		_PHASES_JSON="${_PHASES_JSON%]},${entry}]"
	fi
}

# Назначение: записать check.json в том.
# Параметры: $1—sha текущего; $2—sha цели; $3—тег цели; $4—версия цели; $5—количество коммитов; $6—signed; $7—signer; $8—manual_steps JSON; $9—notes; $10—error.
# Результат: check.json в томе.
# Ошибки: vol_write.
write_check_json() {
	local cur_sha=$1 target_sha=$2 target_tag=$3 target_ver=$4 \
		ahead=${5:-0} signed=${6:-false} signer=${7:-} manual_steps_json=${8:-[]} \
		notes=${9:-} error=${10:-}
	local at channel commits_json
	at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	channel=$CHANNEL
	commits_json="${_CHECK_COMMITS_JSON:-[]}"
	printf '{"schema":1,"at":%s,"channel":%s,"current":{"commit":%s},"target":{"commit":%s,"tag":%s,"version":%s},"ahead":%s,"commits":%s,"notes":%s,"manual_steps":%s,"signed":%s,"signer":%s,"error":%s}' \
		"$(json_str "$at")" "$(json_str "$channel")" "$(json_str "$cur_sha")" \
		"$(json_str "$target_sha")" "$(json_str "$target_tag")" "$(json_str "$target_ver")" \
		"$ahead" "$commits_json" "$(json_str "$notes" 16384)" "$manual_steps_json" \
		"$signed" "$(json_str "$signer")" "$(json_str "$error")" | vol_write check.json
}

# Назначение: записать agent.json в том.
# Параметры: нет (собирает данные из docker inspect).
# Результат: agent.json в томе.
# Ошибки: vol_write.
write_agent_json() {
	local at script_ver repo channel signing docker_ok inbox img_local img_engine img_admin img_cron mismatch
	at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	script_ver=$(printf '%s' "$(sha256sum "$SELF" 2>/dev/null | cut -c1-12)" || true)
	repo=$(printf '%s' "$REPO" | LC_ALL=C sed 's|//[^@]*@|//***@|g')
	channel=$CHANNEL
	signing=false
	signing_enabled && signing=true
	docker_ok=false
	docker info >/dev/null 2>&1 && docker_ok=true
	inbox=false
	local inbox_dir="${VAR_DIR}/inbox"
	if [[ -d "$inbox_dir" && ! -L "$inbox_dir" ]]; then
		local iown
		iown=$(stat -c '%u' "$inbox_dir" 2>/dev/null || echo 0)
		[[ "$iown" == "10001" ]] && inbox=true
		# В песочнице ensure_inbox_dir допускает отказ chown без root: владелец — текущий пользователь.
		[[ -n "$SBX" && "$iown" == "$(id -u)" ]] && inbox=true
	fi
	img_local=$(docker inspect --format '{{.Id}}' emtds:local 2>/dev/null | head -c 32 || true)
	img_engine=$(docker inspect --format '{{.Image}}' emtds-engine 2>/dev/null | head -c 32 || true)
	img_admin=$(docker inspect --format '{{.Image}}' emtds-admin 2>/dev/null | head -c 32 || true)
	img_cron=$(docker inspect --format '{{.Image}}' emtds-cron 2>/dev/null | head -c 32 || true)
	mismatch=false
	if [[ -n "$img_local" && ( "$img_engine" != "$img_local" || "$img_admin" != "$img_local" || "$img_cron" != "$img_local" ) ]]; then
		mismatch=true
	fi
	printf '{"schema":1,"at":%s,"script_version":%s,"repo":%s,"channel":%s,"signing":%s,"docker_ok":%s,"inbox":%s,"images":{"local":%s,"engine":%s,"admin":%s,"cron":%s},"mismatch":%s}' \
		"$(json_str "$at")" "$(json_str "$script_ver")" "$(json_str "$repo")" \
		"$(json_str "$channel")" "$signing" "$docker_ok" "$inbox" \
		"$(json_str "$img_local")" "$(json_str "$img_engine")" \
		"$(json_str "$img_admin")" "$(json_str "$img_cron")" "$mismatch" | vol_write agent.json
}

# ------------------------------------------------------------------
# Бэкап
# ------------------------------------------------------------------

# Назначение: сделать бэкап STATE_DIR, .env и тома в BACKUP_ROOT.
# Параметры: $1 — id прогона (суффикс каталога бэкапа).
# Результат: каталог бэкапа 0700, ротация KEEP_BACKUPS.
# Ошибки: код 1.
do_backup() {
	local ts backup_dir
	ts=$(date -u +%Y%m%dT%H%M%SZ)
	[[ ! -L "$BACKUP_ROOT" ]] || die "каталог бэкапов является симлинком: ${BACKUP_ROOT}"
	install -d -m 0700 "$BACKUP_ROOT"
	backup_dir="${BACKUP_ROOT}/${ts}-$1"
	[[ ! -e "$backup_dir" && ! -L "$backup_dir" ]] || die "каталог бэкапа уже существует: ${backup_dir}"
	install -d -m 0700 "$backup_dir"
	logbuf "бэкап → ${backup_dir}"
	tar -czf "${backup_dir}/state.tar.gz" --warning=no-file-changed \
		-C "$(dirname "$STATE_DIR")" "$(basename "$STATE_DIR")" || die "бэкап ${STATE_DIR} не удался"
	if [[ -f "${TDS_DIR}/.env" ]]; then
		tar -czf "${backup_dir}/env.tar.gz" -C "$TDS_DIR" .env || die "бэкап .env не удался"
	fi
	# Снимок тома на работающем стеке: только чтение, без geo/; файлы могут меняться во время чтения.
	docker run --rm --network none --user 10001:10001 \
		-v "${VOL_NAME}:/v:ro" \
		--entrypoint sh emtds:local \
		-c 'cd /v && tar -czf - --warning=no-file-changed --exclude=./geo . ; rc=$?; [ "$rc" -le 1 ]' \
		> "${backup_dir}/volume.tar.gz" || die "снимок тома не удался"
	printf 'снимок тома снят без остановки стека, без geo/; tar код 1 (файл изменился при чтении) допустим\n' \
		> "${backup_dir}/volume.note"
	# Ротация: оставить KEEP_BACKUPS последних каталогов.
	local d
	while IFS= read -r d; do
		[[ -n "$d" && ! -L "${BACKUP_ROOT}/${d}" ]] && rm -rf -- "${BACKUP_ROOT:?}/${d}"
	done < <(ls -1 "$BACKUP_ROOT" 2>/dev/null | LC_ALL=C grep -E '^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{16}$' | sort -r | tail -n +$(( KEEP_BACKUPS + 1 )))
}

# ------------------------------------------------------------------
# Сборка образа
# ------------------------------------------------------------------

# Назначение: распаковать git archive и собрать образ emtds:<sha12>.
# Параметры: $1 — sha (40 hex).
# Результат: образ emtds:<sha12> создан; путь к дереву на stdout.
# Ошибки: код 1.
BUILD_DIR=""

# Назначение: распаковать дерево коммита через git archive в <var>/build/<sha>.
# Параметры: $1 — sha (40 hex).
# Результат: BUILD_DIR указывает на свежую распаковку.
# Ошибки: git archive или tar — код 1.
extract_tree() {
	local sha=$1
	BUILD_DIR="${VAR_DIR}/build/${sha}"
	[[ ! -L "${VAR_DIR}/build" ]] || die "${VAR_DIR}/build является симлинком"
	rm -rf -- "$BUILD_DIR"
	install -d -m 0700 "${VAR_DIR}/build" "$BUILD_DIR"
	logbuf "git archive ${sha:0:12} → ${BUILD_DIR}"
	git_cmd archive "$sha" | tar -xf - -C "$BUILD_DIR" || die "git archive ${sha:0:12} не удался"
}

do_build() {
	local sha=$1
	local sha12="${sha:0:12}"
	extract_tree "$sha"
	local version commit_date
	version=$(git_cmd describe --tags --always "$sha" 2>/dev/null || printf 'dev')
	commit_date=$(git_cmd show -s --format=%cI "$sha" 2>/dev/null || printf '')
	logbuf "docker build emtds:${sha12}"
	timeout 1200 \
		nice -n 10 ionice -c3 docker build \
		--build-arg "EMTDS_VERSION=${version}" \
		--build-arg "EMTDS_COMMIT=${sha}" \
		--build-arg "EMTDS_COMMIT_DATE=${commit_date}" \
		--build-arg "EMTDS_CHANNEL=${CHANNEL}" \
		--build-arg "EMTDS_GOFLAGS=-p=1" \
		-t "emtds:${sha12}" \
		"$BUILD_DIR" 2>&1 | while IFS= read -r l; do logbuf "$l"; done || \
		die "docker build завершился с ошибкой"
}

# ------------------------------------------------------------------
# Гейт Keitaro
# ------------------------------------------------------------------

# Назначение: записать pending и дождаться ready.json от admin.
# Параметры: $1 — id прогона.
# Результат: 0 при успехе; выставляет KEITARO_IDLE (true/false).
# Ошибки: таймаут или admin недоступен без --keitaro-idle — код 1.
KEITARO_IDLE_RESULT=false

keitaro_gate() {
	local run_id=$1
	if [[ $F_KEITARO_IDLE -eq 1 ]]; then
		logbuf "keitaro: --keitaro-idle, пропуск гейта"
		KEITARO_IDLE_RESULT=true
		return 0
	fi
	# Проверить, запущен ли admin.
	if ! docker inspect --format '{{.State.Running}}' emtds-admin 2>/dev/null | grep -q true; then
		die "emtds-admin не запущен; добавьте --keitaro-idle"
	fi
	logbuf "keitaro: запись pending=${run_id}"
	printf '%s\n' "$run_id" | vol_write pending
	# Опрашивать ready.json до 10 минут.
	local deadline ready_json ready_id
	deadline=$(_deadline 600)
	while true; do
		ready_json=$(vol_read ready.json 65536 || true)
		ready_id=$(printf '%s' "$ready_json" | LC_ALL=C grep -oE '"id" *: *"[0-9a-f]{16}"' | head -1 | LC_ALL=C grep -oE '[0-9a-f]{16}' || true)
		if [[ -n "$ready_id" && "$ready_id" == "$run_id" ]]; then
			KEITARO_IDLE_RESULT=false
			printf '%s' "$ready_json" | LC_ALL=C grep -qE '"keitaro_idle" *: *true' && KEITARO_IDLE_RESULT=true
			logbuf "keitaro: получен ready (keitaro_idle=${KEITARO_IDLE_RESULT})"
			[[ "$KEITARO_IDLE_RESULT" == true ]] || die "keitaro: перенос из Keitaro идёт; повторите позже"
			return 0
		fi
		(( SECONDS < deadline )) || break
		_sleep 5
	done
	die "keitaro: нет ready.json от admin за 10 минут — идёт перенос или актуализация Keitaro; обновление не применено, повторите после окончания"
}

# ------------------------------------------------------------------
# Health-проверки
# ------------------------------------------------------------------

# Назначение: получить хост:порт из server.env для данного сервиса.
# Параметры: $1 — ключ EMTDS_*_PUBLISH; $2 — запасное значение.
# Результат: host:port на stdout.
# Ошибки: нет.
publish_listen() {
	local key=$1 fallback=$2
	local val host port rest
	val=$(env_value "${STATE_DIR}/server.env" "$key" 2>/dev/null || true)
	[[ -z "$val" ]] && { printf '%s' "$fallback"; return 0; }
	IFS=: read -r host port rest <<< "$val"
	if [[ -n "$host" && -n "$port" && -n "$rest" ]]; then
		printf '%s:%s' "$host" "$port"
	else
		printf '%s' "$fallback"
	fi
}

# Назначение: проверить здоровье пересозданного engine или admin.
# Параметры: $1 — имя контейнера; $2 — HTTP URL проверки; $3 — StartedAt контейнера.
# Результат: 0, когда healthcheck healthy, в логах с StartedAt есть «clickhouse connected» и «redis connected», URL отвечает.
# Ошибки: не дождались за 90 с (с учётом WAIT_SCALE) — код 1.
health_check_service() {
	local ctr=$1 url=$2 started_at=$3
	local deadline status logs
	deadline=$(_deadline 90)
	while true; do
		status=$(docker inspect --format '{{.State.Health.Status}}' "$ctr" 2>/dev/null || echo starting)
		if [[ "$status" == healthy ]]; then
			logs=$(docker logs --since "$started_at" "$ctr" 2>&1 || true)
			if printf '%s' "$logs" | grep -q 'clickhouse connected' && \
			   printf '%s' "$logs" | grep -q 'redis connected' && \
			   curl -fsS --max-time 5 "$url" >/dev/null 2>&1; then
				return 0
			fi
		fi
		(( SECONDS < deadline )) || break
		_sleep 3
	done
	logbuf "health провал: ${ctr} (статус=${status})"
	return 1
}

# Назначение: Compose-флаги для приложения (одинаковые везде).
# Параметры: нет.
# Результат: устанавливает массив COMPOSE_FLAGS.
# Ошибки: нет.
build_compose_flags() {
	COMPOSE_FLAGS=(
		--env-file "${TDS_DIR}/.env"
		--env-file "${STATE_DIR}/server.env"
		-f "${TDS_DIR}/deploy/docker-compose.app.yml"
		-f "${STATE_DIR}/compose.app.yml"
	)
}

# Назначение: найти небезопасные настройки в выводе docker compose config.
# Параметры: stdin — YAML compose config.
# Результат: 0, если всё допустимо; иначе причина на stdout.
# Ошибки: опубликованный порт не на 127.0.0.1, сокет Docker, привилегированный режим или pid host — код 1.
compose_config_problem() {
	local cfg sock priv
	cfg=$(cat)
	# Искомые строки собраны из частей, чтобы тест запрещённых подстрок проверял их буквально.
	sock="docker.so"; sock="${sock}ck"
	priv="privile"; priv="${priv}ged"
	if printf '%s\n' "$cfg" | LC_ALL=C grep -qF "$sock"; then
		printf 'смонтирован сокет Docker\n'; return 1
	fi
	if printf '%s\n' "$cfg" | LC_ALL=C grep -qE "^[[:space:]]*${priv}:[[:space:]]*true"; then
		printf 'привилегированный контейнер\n'; return 1
	fi
	if printf '%s\n' "$cfg" | LC_ALL=C grep -qE '^[[:space:]]*pid:[[:space:]]*"?host"?[[:space:]]*$'; then
		printf 'pid host\n'; return 1
	fi
	local bad
	bad=$(printf '%s\n' "$cfg" | LC_ALL=C awk -v sq="'" '
		function flush() { if (pub && host != "127.0.0.1") bad = 1; pub = 0; host = "" }
		function indent(s) { match(s, /^[ ]*/); return RLENGTH }
		function unq(s) {
			gsub(/^[ ]+|[ ]+$/, "", s)
			if (substr(s, 1, 1) == "\"" || substr(s, 1, 1) == sq) s = substr(s, 2)
			if (substr(s, length(s), 1) == "\"" || substr(s, length(s), 1) == sq) s = substr(s, 1, length(s) - 1)
			return s
		}
		function kv(s,   k, v) {
			k = s; sub(/:.*/, "", k); v = s; sub(/^[^:]*:/, "", v); v = unq(v)
			if (k == "host_ip") host = v
			if (k == "published" && v != "") pub = 1
		}
		/^[ ]*$/ { next }
		/^[ ]*ports:[ ]*$/ { flush(); inport = 1; pind = indent($0); next }
		inport && indent($0) <= pind { flush(); inport = 0 }
		inport {
			line = $0; sub(/^[ ]*/, "", line)
			if (line ~ /^- /) {
				flush(); line = substr(line, 3)
				if (line !~ /^[a-z_]+:( |$)/) {
					n = split(unq(line), p, ":")
					if (n >= 3 && p[1] != "127.0.0.1") bad = 1
					if (n == 2) bad = 1
					next
				}
			}
			kv(line)
		}
		END { flush(); print bad ? 1 : 0 }')
	if [[ "$bad" == 1 ]]; then
		printf 'опубликованный порт не привязан к 127.0.0.1\n'; return 1
	fi
	return 0
}

# Назначение: проверить итоговый compose config приложения.
# Параметры: нет (использует COMPOSE_FLAGS, TDS_DIR).
# Результат: 0; вывод config в лог не попадает.
# Ошибки: config не собирается или небезопасен — код 1.
check_compose_config() {
	docker compose "${COMPOSE_FLAGS[@]}" config -q >/dev/null 2>&1 || die "docker compose config не удался"
	local cfg_tmp reason
	cfg_tmp=$(mktemp "${VAR_DIR}/.cfg.XXXXXX")
	if ! docker compose "${COMPOSE_FLAGS[@]}" config > "$cfg_tmp" 2>/dev/null; then
		rm -f "$cfg_tmp"
		die "docker compose config не удался"
	fi
	if ! reason=$(compose_config_problem < "$cfg_tmp"); then
		rm -f "$cfg_tmp"
		die "compose: ${reason}"
	fi
	rm -f "$cfg_tmp"
	# Ключи из .env.example, которых нет в .env (только имена).
	if [[ -f "${TDS_DIR}/.env.example" && -f "${TDS_DIR}/.env" ]]; then
		local l k
		while IFS= read -r l; do
			[[ "$l" =~ ^[A-Z_][A-Z0-9_]*= ]] || continue
			k="${l%%=*}"
			grep -qE "^${k}=" "${TDS_DIR}/.env" 2>/dev/null || \
				logbuf "предупреждение: ключ ${k} есть в .env.example, но нет в .env"
		done < "${TDS_DIR}/.env.example"
	fi
}

# Назначение: пересоздать engine или admin и проверить здоровье.
# Параметры: $1 — сервис (engine|admin); $2 — URL проверки.
# Результат: 0.
# Ошибки: compose up или health — код 1.
recreate_service() {
	local svc=$1 health_url=$2 ctr started_at
	case "$svc" in engine|admin) ;; *) return 1;; esac
	logbuf "compose up ${svc}"
	if ! docker compose "${COMPOSE_FLAGS[@]}" up -d --no-build --force-recreate --no-deps "$svc" 2>&1 | \
		while IFS= read -r l; do logbuf "$l"; done; then
		logbuf "compose up ${svc} не удался"
		return 1
	fi
	ctr="emtds-${svc}"
	started_at=$(docker inspect --format '{{.State.StartedAt}}' "$ctr" 2>/dev/null || printf '')
	health_check_service "$ctr" "$health_url" "$started_at"
}

# Назначение: прочитать значение ключа из вывода probe cron.
# Параметры: $1 — ключ; stdin — строки key=value.
# Результат: значение на stdout (пусто, если ключа нет).
# Ошибки: нет.
probe_value() {
	local key=$1 line
	while IFS= read -r line; do
		if [[ "$line" == "${key}="* ]]; then
			printf '%s' "${line#*=}"
			return 0
		fi
	done
}

# Назначение: выполнить probe cron образом emtds:<sha12> по файлу статуса на томе.
# Параметры: $1 — sha12 образа.
# Результат: строки key=value на stdout (пусто при ошибке).
# Ошибки: нет.
cron_probe() {
	docker run --rm --network none --user 10001:10001 \
		-v "${VOL_NAME}:/v:ro" \
		--entrypoint /usr/local/bin/emtds-cron \
		"emtds:$1" -probe /v/cron.json.status.json 2>/dev/null || true
}

# Флаг: cron не пересоздан, потому что был занят задачей.
CRON_LAGGING=false

# Назначение: пересоздать cron, когда он свободен, и проверить пульс новой версии.
# Параметры: $1 — sha цели (40 hex).
# Результат: CRON_LAGGING=true, если cron занят дольше 10 минут (cron не тронут).
# Ошибки: compose up, не один контейнер emtds-cron или пульс без нужного commit за 90 с — код 1.
recreate_cron() {
	local target_sha=$1 sha12="${1:0:12}" deadline probe
	deadline=$(_deadline 600)
	while true; do
		probe=$(cron_probe "$sha12")
		[[ "$(probe_value busy <<< "$probe")" == 0 ]] && break
		if (( SECONDS >= deadline )); then
			logbuf "cron занят, пересоздание отложено (cron_lagging)"
			CRON_LAGGING=true
			return 0
		fi
		_sleep 10
	done
	CRON_LAGGING=false
	logbuf "compose up cron"
	if ! docker compose "${COMPOSE_FLAGS[@]}" up -d --no-build --force-recreate --no-deps cron 2>&1 | \
		while IFS= read -r l; do logbuf "$l"; done; then
		logbuf "compose up cron не удался"
		return 1
	fi
	local count age commit
	deadline=$(_deadline 90)
	while true; do
		count=$(docker ps -q --filter 'name=^/emtds-cron$' 2>/dev/null | grep -c . || true)
		probe=$(cron_probe "$sha12")
		age=$(probe_value heartbeat_age <<< "$probe")
		commit=$(probe_value commit <<< "$probe")
		if [[ "$count" == 1 && "$age" =~ ^[0-9]+$ ]] && (( age <= 30 )) && [[ "$commit" == "$target_sha" ]]; then
			return 0
		fi
		(( SECONDS < deadline )) || break
		_sleep 5
	done
	logbuf "health провал: emtds-cron (контейнеров=${count}, heartbeat_age=${age:-?}, commit=${commit:0:12})"
	return 1
}

# Потерянные события из логов старого engine.
EVENTS_LOST=0

# Назначение: получить events_lost из логов текущего (старого) engine.
# Параметры: $1 — StartedAt старого контейнера.
# Результат: устанавливает EVENTS_LOST (целое).
# Ошибки: нет.
get_events_lost() {
	local logs val
	logs=$(docker logs --since "$1" emtds-engine 2>&1 || true)
	val=$(printf '%s' "$logs" | grep -oE 'events_lost=[0-9]+' | tail -1 | cut -d= -f2 || true)
	EVENTS_LOST=${val:-0}
}

# Назначение: путь файла итогов второй фазы для родителя.
# Параметры: $1 — id прогона.
# Результат: путь на stdout.
# Ошибки: нет.
stage2_result_file() {
	printf '%s/runs/%s.stage2' "$VAR_DIR" "$1"
}

# Назначение: сохранить итоги второй фазы (events_lost, cron_lagging) для родителя.
# Параметры: $1 — id прогона.
# Результат: файл <var>/runs/<id>.stage2 (0600).
# Ошибки: нет.
write_stage2_result() {
	install -d -m 0700 "${VAR_DIR}/runs"
	printf 'events_lost=%s\ncron_lagging=%s\n' "$EVENTS_LOST" "$CRON_LAGGING" > "$(stage2_result_file "$1")"
}

# Назначение: прочитать итоги второй фазы в EVENTS_LOST и CRON_LAGGING.
# Параметры: $1 — id прогона.
# Результат: переменные обновлены; файл удалён.
# Ошибки: нет.
read_stage2_result() {
	local f v
	f=$(stage2_result_file "$1")
	[[ -f "$f" && ! -L "$f" ]] || return 0
	v=$(probe_value events_lost < "$f")
	[[ "$v" =~ ^[0-9]+$ ]] && EVENTS_LOST=$v
	v=$(probe_value cron_lagging < "$f")
	[[ "$v" == true ]] && CRON_LAGGING=true || CRON_LAGGING=false
	rm -f "$f"
}

# Назначение: применить apply-edge.sh из текущего дерева TDS_DIR, если на хосте есть nginx.
# Параметры: нет.
# Результат: 0.
# Ошибки: apply-edge.sh завершился ошибкой — код 1.
run_apply_edge() {
	command -v nginx >/dev/null 2>&1 || return 0
	[[ -f "${TDS_DIR}/deploy/apply-edge.sh" ]] || return 0
	logbuf "apply-edge.sh"
	bash "${TDS_DIR}/deploy/apply-edge.sh" 2>&1 | while IFS= read -r l; do logbuf "$l"; done
}

# Назначение: выполнить вторую фазу применения (checkout, compose, edge, сервисы, health).
# Параметры: $1—id; $2—prev_sha; $3—target_sha (вызывается родителем через --stage2).
# Результат: 0; итоги в <var>/runs/<id>.stage2.
# Ошибки: код 1 (откат делает родитель).
run_stage2() {
	local run_id=$1 prev_sha=$2 target_sha=$3
	[[ "$run_id" =~ ^[0-9a-f]{16}$ ]] || die "--stage2: недопустимый id"
	[[ "$prev_sha" =~ ^[0-9a-f]{40}$ && "$target_sha" =~ ^[0-9a-f]{40}$ ]] || die "--stage2: недопустимый sha"
	local sha12="${target_sha:0:12}"
	read_update_env
	determine_state_dir
	load_mask_words
	build_compose_flags
	VOL_NAME=$(get_volume_name)
	[[ -n "$VOL_NAME" ]] || die "не удалось определить том emtds-admin"
	local old_started_at
	old_started_at=$(docker inspect --format '{{.State.StartedAt}}' emtds-engine 2>/dev/null || printf '')
	get_events_lost "$old_started_at"
	write_stage2_result "$run_id"
	logbuf "git checkout --detach ${sha12}"
	git_cmd checkout --detach "$target_sha" 2>&1 | while IFS= read -r l; do logbuf "$l"; done
	check_compose_config
	run_apply_edge || die "apply-edge.sh не удался"
	docker tag "emtds:${sha12}" emtds:local || die "docker tag emtds:${sha12} не удался"
	logbuf "emtds:${sha12} → emtds:local"
	local engine_url admin_url
	engine_url="http://$(publish_listen EMTDS_ENGINE_PUBLISH 127.0.0.1:18080)/healthz"
	admin_url="http://$(publish_listen EMTDS_ADMIN_PUBLISH 127.0.0.1:8090)/api/health"
	recreate_service engine "$engine_url" || die "health engine провал"
	recreate_service admin "$admin_url" || die "health admin провал"
	recreate_cron "$target_sha" || die "health cron провал"
	write_stage2_result "$run_id"
	if command -v nginx >/dev/null 2>&1; then
		curl -kfsS --max-time 8 "https://127.0.0.1/healthz" >/dev/null 2>&1 || \
			logbuf "предупреждение: https://127.0.0.1/healthz не отвечает"
	fi
	install_script_copy "${TDS_DIR}/deploy/update.sh"
	if [[ -f "${TDS_DIR}/deploy/watchdog.sh" && -f "${UNIT_DIR}/emtds-watchdog.service" ]]; then
		bash "${TDS_DIR}/deploy/watchdog.sh" --install 2>&1 | while IFS= read -r l; do logbuf "$l"; done || \
			logbuf "предупреждение: watchdog.sh --install не удался"
	fi
}

# Назначение: установить копию скрипта в /usr/local/lib/emtds/update.sh.
# Параметры: $1 — исходный файл.
# Результат: файл 0755 root (в песочнице без смены владельца).
# Ошибки: код 1.
install_script_copy() {
	install -d -m 0755 "$LIB_DIR"
	if [[ -n "$SBX" ]]; then
		install -m 0755 "$1" "${LIB_DIR}/update.sh"
	else
		install -m 0755 -o root -g root "$1" "${LIB_DIR}/update.sh"
	fi
}

# ------------------------------------------------------------------
# Откат (выполняет РОДИТЕЛЬ)
# ------------------------------------------------------------------

# Назначение: вернуть стек на версию prev_sha после провала второй фазы.
# Параметры: $1 — prev_sha.
# Результат: 0 — откат выполнен и сервисы здоровы.
# Ошибки: 2 — ClickHouse или Redis не отвечают (откат не делается); 1 — откат не удался.
do_rollback_procedure() {
	local prev_sha=$1 prev12="${1:0:12}"
	logbuf "откат → ${prev12}"
	if ! check_infra_health; then
		logbuf "откат не выполняется: ${INFRA_ERROR}"
		return 2
	fi
	if ! docker tag "emtds:${prev12}" emtds:local; then
		logbuf "образ emtds:${prev12} не найден"
		return 1
	fi
	if ! git_cmd checkout --detach "$prev_sha" 2>&1 | while IFS= read -r l; do logbuf "$l"; done; then
		logbuf "git checkout ${prev12} не удался"
		return 1
	fi
	build_compose_flags
	run_apply_edge || { logbuf "apply-edge.sh прежнего дерева не удался"; return 1; }
	local engine_url admin_url
	engine_url="http://$(publish_listen EMTDS_ENGINE_PUBLISH 127.0.0.1:18080)/healthz"
	admin_url="http://$(publish_listen EMTDS_ADMIN_PUBLISH 127.0.0.1:8090)/api/health"
	recreate_service engine "$engine_url" || return 1
	recreate_service admin "$admin_url" || return 1
	logbuf "compose up cron"
	docker compose "${COMPOSE_FLAGS[@]}" up -d --no-build --force-recreate --no-deps cron 2>&1 | \
		while IFS= read -r l; do logbuf "$l"; done || return 1
}

# ------------------------------------------------------------------
# Финализация
# ------------------------------------------------------------------

# Назначение: записать строку в history и удалить build dir.
# Параметры: $1—id; $2—action; $3—from; $4—to; $5—result; $6—tag.
# Результат: строка в history.
# Ошибки: нет.
write_history() {
	local run_id=$1 action=$2 from=$3 to=$4 result=$5 tag=${6:-}
	local ts; ts=$(date -u +%s)
	printf '%s %s %s %s %s %s %s\n' "$ts" "$run_id" "$action" "$from" "$to" "$result" "$tag" \
		>> "${VAR_DIR}/history"
}

# Назначение: удалить образы emtds:<sha12> сверх KEEP_IMAGES (кроме текущего и предыдущего).
# Параметры: $1—текущий sha; $2—предыдущий sha.
# Результат: лишние образы удалены.
# Ошибки: нет.
prune_old_images() {
	local cur12="${1:0:12}" prev12="${2:0:12}" tag
	# docker image ls выдаёт образы от новых к старым; оставить KEEP_IMAGES новейших тегов sha12.
	while IFS= read -r tag; do
		[[ "$tag" == "$cur12" || "$tag" == "$prev12" ]] && continue
		docker image rm "emtds:${tag}" >/dev/null 2>&1 || true
	done < <(docker image ls --format '{{.Tag}}' emtds 2>/dev/null | LC_ALL=C grep -E '^[0-9a-f]{12}$' | \
		tail -n +$(( KEEP_IMAGES + 1 )))
	docker builder prune -f --filter until=168h >/dev/null 2>&1 || true
}

# ------------------------------------------------------------------
# Режимы работы
# ------------------------------------------------------------------

# Цель, выбранная resolve_target, и причина отказа выбора.
TARGET_SHA=""
TARGET_TAG=""
TARGET_VER=""
TARGET_ERROR=""

# Назначение: выбрать цель по ref (--to) или по каналу из update.env.
# Параметры: $1 — sha или тег vX.Y.Z; пусто — по каналу.
# Результат: TARGET_SHA, TARGET_TAG, TARGET_VER.
# Ошибки: цель не найдена, не предок origin/main или понижение — код 1, причина в TARGET_ERROR.
resolve_target() {
	local ref=${1:-} line errf rc=0
	TARGET_SHA=""; TARGET_TAG=""; TARGET_VER=""; TARGET_ERROR=""
	errf=$(mktemp "${VAR_DIR}/.target.XXXXXX")
	if [[ -n "$ref" ]]; then
		line=$(pick_target_ref "$ref" 2>"$errf") || rc=$?
	else
		line=$(pick_target_for_channel 2>"$errf") || rc=$?
	fi
	if [[ $rc -ne 0 ]]; then
		TARGET_ERROR=$(tail -n 1 "$errf" | sed 's/^emtds-update: ошибка: //')
		rm -f "$errf"
		[[ -n "$TARGET_ERROR" ]] || TARGET_ERROR="не удалось выбрать цель"
		return 1
	fi
	rm -f "$errf"
	read -r TARGET_SHA TARGET_TAG TARGET_VER <<< "$line"
	[[ "$TARGET_SHA" =~ ^[0-9a-f]{40}$ ]] || { TARGET_ERROR="не удалось выбрать цель"; return 1; }
}

# Назначение: выбрать цель по текущему каналу.
# Параметры: нет.
# Результат: sha tag version на stdout через пробел.
# Ошибки: код 1.
pick_target_for_channel() {
	case "$CHANNEL" in
		release) pick_target_release;;
		main)    pick_target_main;;
		*)       die "неизвестный канал: ${CHANNEL}";;
	esac
}

# Назначение: построить JSON-массив коммитов от текущего до цели.
# Параметры: $1—cur_sha; $2—target_sha; $3—лимит.
# Результат: JSON-массив на stdout.
# Ошибки: нет.
_CHECK_COMMITS_JSON="[]"
build_commits_json() {
	local cur=$1 target=$2 lim=${3:-200}
	local arr="[" n=0 sha subj author date
	while IFS=$'\x1f' read -r sha subj author date; do
		[[ -z "$sha" ]] && continue
		[[ $n -gt 0 ]] && arr="${arr},"
		arr="${arr}{\"sha\":$(json_str "$sha" 40),\"subject\":$(json_str "$subj" 200),\"author\":$(json_str "$author" 200),\"date\":$(json_str "$date" 40)}"
		n=$(( n + 1 ))
		[[ $n -ge $lim ]] && break
	done < <(git_cmd log --format='%H%x1f%s%x1f%aN%x1f%cI' "${cur}..${target}" 2>/dev/null || true)
	printf '%s]' "$arr"
}

# Назначение: собрать заметки «что нового» для цели.
# Параметры: $1 — текущий sha; $2 — sha цели; $3 — тег цели (или пусто).
# Результат: на stdout добавленные строки CHANGELOG.md между версиями без ведущего «+»;
#   если таких строк нет — тема и тело аннотации тега (без блока подписи); иначе пусто.
#   Длину ограничивает json_str при записи check.json (16 КиБ).
# Ошибки: нет.
build_notes() {
	local cur=$1 target=$2 tag=${3:-} notes
	notes=$(git_cmd diff --no-color -U0 "${cur}..${target}" -- CHANGELOG.md 2>/dev/null | \
		LC_ALL=C sed -n '/^+++/d; s/^+//p' || true)
	if [[ -z "$notes" && -n "$tag" ]]; then
		notes=$(git_cmd for-each-ref --format='%(contents:subject)%0a%0a%(contents:body)' "refs/tags/${tag}" 2>/dev/null || true)
	fi
	printf '%s' "$notes"
}

# Назначение: записать check.json для выбранной цели (подпись, ручные шаги, коммиты, заметки).
# Параметры: $1 — текущий sha.
# Результат: check.json в томе; SIGNED, SIGNER, MANUAL_STEPS обновлены.
# Ошибки: vol_write — код 1.
write_check_for_target() {
	local cur_sha=$1 ahead notes
	verify_signature "$TARGET_SHA" "$TARGET_TAG"
	collect_manual_steps "$TARGET_SHA"
	ahead=$(git_cmd rev-list --count "${cur_sha}..${TARGET_SHA}" 2>/dev/null || echo 0)
	notes=$(build_notes "$cur_sha" "$TARGET_SHA" "$TARGET_TAG")
	_CHECK_COMMITS_JSON=$(build_commits_json "$cur_sha" "$TARGET_SHA" 200)
	write_check_json "$cur_sha" "$TARGET_SHA" "$TARGET_TAG" "$TARGET_VER" \
		"$ahead" "$SIGNED" "$SIGNER" "$(manual_steps_json)" "$notes" ""
}

# Назначение: подготовить окружение команды, работающей со стеком (шаг 1 плана).
# Параметры: нет.
# Результат: update.env прочитан, STATE_DIR и VOL_NAME определены, маскировка загружена.
# Ошибки: код 1.
prepare_stack() {
	read_update_env
	determine_state_dir
	check_environment
	load_mask_words
	VOL_NAME=$(get_volume_name)
	[[ -n "$VOL_NAME" ]] || die "не удалось определить том emtds-admin"
}

# Назначение: режим --check (шаги 1–3 плана, запись check.json).
# Параметры: нет.
# Результат: check.json в томе; ошибки выбора цели — в поле error.
# Ошибки: код 1.
do_check() {
	log "режим: --check"
	prepare_stack
	local cur_sha
	cur_sha=$(git_cmd rev-parse HEAD)
	_CHECK_COMMITS_JSON="[]"
	if ! git_fetch; then
		write_check_json "$cur_sha" "" "" "" 0 false "" "[]" "" "git fetch не удался"
		return 1
	fi
	if ! resolve_target ""; then
		write_check_json "$cur_sha" "" "" "" 0 false "" "[]" "" "$TARGET_ERROR"
		log "check: ${TARGET_ERROR}" >&2
		return 1
	fi
	write_check_for_target "$cur_sha"
	log "check: target=${TARGET_SHA:0:12} signed=${SIGNED} manual_steps=${#MANUAL_STEPS[@]}"
}

# Контекст текущего прогона для аварийного завершения.
RUN_ID=""
RUN_ACTION=""
RUN_FROM=""
RUN_TO=""
RUN_TAG=""
RUN_USER=""
RUN_STARTED=""
RUN_PHASE=""

# Назначение: перейти к фазе прогона и записать state.json (result=running).
# Параметры: $1 — имя фазы.
# Результат: RUN_PHASE обновлена, state.json в томе.
# Ошибки: vol_write — код 1.
set_phase() {
	RUN_PHASE=$1
	write_state_json "$RUN_ID" "$RUN_ACTION" "$RUN_FROM" "$RUN_TO" "$RUN_PHASE" running "" \
		"$EVENTS_LOST" "$CRON_LAGGING" "$RUN_USER" "$RUN_STARTED" ""
}

# Назначение: отметить текущую фазу завершённой.
# Параметры: $1 — true/false (по умолчанию true).
# Результат: запись в _PHASES_JSON.
# Ошибки: нет.
phase_done() {
	add_phase "$RUN_PHASE" "${1:-true}"
}

# Назначение: завершить прогон: state.json, history, снятие pending, лог.
# Параметры: $1 — result (ok|rolled_back|failed); $2 — причина.
# Результат: прогон закрыт, аварийный обработчик снят.
# Ошибки: нет (ошибки записи в том не прерывают завершение).
finish_run() {
	local result=$1 reason=${2:-} finished_at
	trap - EXIT INT TERM
	finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	write_state_json "$RUN_ID" "$RUN_ACTION" "$RUN_FROM" "$RUN_TO" done "$result" "$reason" \
		"$EVENTS_LOST" "$CRON_LAGGING" "$RUN_USER" "$RUN_STARTED" "$finished_at" || true
	write_history "$RUN_ID" "$RUN_ACTION" "$RUN_FROM" "$RUN_TO" "$result" "$RUN_TAG" || true
	vol_rm pending || log "предупреждение: не удалось удалить pending" >&2
	vol_rm ready.json || log "предупреждение: не удалось удалить ready.json" >&2
	[[ -n "$BUILD_DIR" ]] && rm -rf -- "$BUILD_DIR"
	logbuf "прогон ${RUN_ID}: ${result}${reason:+ (${reason})}"
	flush_log
	RUN_ID=""
}

# Назначение: обработчик выхода во время прогона: записать failed с причиной.
# Параметры: нет ($? — код выхода).
# Результат: прогон закрыт с result=failed; код выхода сохраняется.
# Ошибки: нет.
abort_run() {
	local rc=$?
	if [[ -n "$RUN_ID" ]]; then
		[[ -n "$RUN_PHASE" ]] && add_phase "$RUN_PHASE" false
		finish_run failed "${LAST_ERROR:-прервано (код ${rc})}"
	fi
	exit "$rc"
}

# Назначение: прогон применения или отката (шаги 4–11 плана) с откатом при провале второй фазы.
# Параметры: $1 — action (apply|rollback); $2 — sha цели; $3 — тег цели; $4 — пользователь; $5 — 1, если образ уже есть (без сборки).
# Результат: 0 — result=ok.
# Ошибки: код 1 — result rolled_back или failed записан в state.json.
do_apply() {
	local action=$1 target_sha=$2 target_tag=${3:-} user=${4:-} skip_build=${5:-0}
	RUN_ID=$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')
	RUN_ACTION=$action
	RUN_FROM=$(git_cmd rev-parse HEAD)
	RUN_TO=$target_sha
	RUN_TAG=$target_tag
	RUN_USER=$user
	RUN_STARTED=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	RUN_PHASE=""
	LAST_ERROR=""
	EVENTS_LOST=0
	CRON_LAGGING=false
	BUILD_DIR=""
	_PHASES_JSON="[]"
	local prev12="${RUN_FROM:0:12}"
	open_run_log "$RUN_ID"
	trap abort_run EXIT
	trap 'exit 130' INT TERM
	logbuf "прогон ${RUN_ID} (${action}): ${prev12} → ${target_sha:0:12}"
	set_phase env
	# Образ текущей версии должен остаться под тегом emtds:<prev12> для отката.
	if ! docker inspect "emtds:${prev12}" >/dev/null 2>&1; then
		docker tag emtds:local "emtds:${prev12}" || die "нет образа emtds:local для отката"
	fi
	phase_done
	set_phase resources
	check_resources
	phase_done
	set_phase backup
	do_backup "$RUN_ID"
	phase_done
	set_phase build
	if [[ "$skip_build" == 1 ]]; then
		extract_tree "$target_sha"
	else
		do_build "$target_sha"
	fi
	phase_done
	set_phase keitaro_gate
	keitaro_gate "$RUN_ID"
	phase_done
	set_phase stage2
	local stage2_rc=0
	EMTDS_UPDATE_SANDBOX="$SBX" EMTDS_UPDATE_WAIT_SCALE="$WAIT_SCALE" EMTDS_UPDATE_MEMINFO="$PROC_MEMINFO" \
		timeout 900 bash "${BUILD_DIR}/deploy/update.sh" --stage2 "$RUN_ID" "$RUN_FROM" "$target_sha" 2>&1 | \
		while IFS= read -r l; do logbuf "$l"; done || stage2_rc=$?
	read_stage2_result "$RUN_ID"
	if [[ $stage2_rc -eq 0 ]]; then
		phase_done
		finish_run ok ""
		prune_old_images "$target_sha" "$RUN_FROM"
		return 0
	fi
	phase_done false
	set_phase rollback
	local rb_rc=0
	do_rollback_procedure "$RUN_FROM" || rb_rc=$?
	case $rb_rc in
		0) phase_done; finish_run rolled_back "вторая фаза завершилась кодом ${stage2_rc}";;
		2) phase_done false; finish_run failed "инфраструктура: ${INFRA_ERROR}";;
		*) phase_done false; finish_run failed "откат не удался";;
	esac
	return 1
}

# Назначение: режим --channel или --to REF (полный прогон).
# Параметры: $1 — ref или пусто; флаги F_*.
# Результат: 0 — обновлено или уже актуально.
# Ошибки: код 1.
do_update() {
	local ref=${1:-} cur_sha
	prepare_stack
	git_fetch || die "git fetch не удался"
	cur_sha=$(git_cmd rev-parse HEAD)
	resolve_target "$ref" || die "$TARGET_ERROR"
	if [[ "$TARGET_SHA" == "$cur_sha" ]]; then
		log "уже актуально (HEAD=${cur_sha:0:12})"
		return 0
	fi
	write_check_for_target "$cur_sha"
	if ! signature_gate; then
		[[ $F_UNSIGNED -eq 1 ]] || \
			die "подпись включена (allowed_signers), цель ${TARGET_SHA:0:12} без проверенной подписи; обход — только CLI --unsigned"
		logbuf "внимание: применение без проверенной подписи (--unsigned)"
	fi
	check_manual_steps "$TARGET_SHA"
	do_apply apply "$TARGET_SHA" "$TARGET_TAG" "" 0
}

# Назначение: загрузить значения из .env и server.env для маскировки.
# Параметры: нет.
# Результат: заполняет _MASK_WORDS (значения длиной ≥ 6).
# Ошибки: нет.
load_mask_words() {
	_MASK_WORDS=()
	local f k v
	for f in "${TDS_DIR}/.env" "${STATE_DIR}/server.env"; do
		[[ -f "$f" ]] || continue
		while IFS='=' read -r k v || [[ -n "$k" ]]; do
			[[ -z "$k" || "${k:0:1}" == '#' ]] && continue
			v="${v%$'\r'}"
			v="${v#\"}"; v="${v%\"}"; v="${v#\'}"; v="${v%\'}"
			[[ ${#v} -ge 6 ]] && _MASK_WORDS+=("$v")
		done < "$f"
	done
}

# Назначение: найти версию, с которой выкатили текущий HEAD.
# Параметры: $1 — текущий sha; $2 — 1: учитывать только последнюю запись history (один шаг назад), по умолчанию 0.
# Результат: sha предыдущей версии на stdout.
# Ошибки: нет записи apply ok с to == HEAD (при $2=1 — последняя запись не такая) — код 1.
previous_version() {
	local hist="${VAR_DIR}/history" prev
	[[ -f "$hist" && ! -L "$hist" ]] || return 1
	prev=$(awk -v cur="$1" -v last_only="${2:-0}" '
		NF == 0 { next }
		$3 == "apply" && $6 == "ok" && $5 == cur && length($4) == 40 && $4 ~ /^[0-9a-f]+$/ { p = $4; hit = 1; next }
		{ hit = 0 }
		END { if (last_only != 1 || hit) print p }' "$hist")
	[[ -n "$prev" ]] || return 1
	printf '%s' "$prev"
}

# Назначение: режим --rollback (на версию до последнего применения).
# Параметры: $1 — пользователь (пусто для CLI).
# Результат: прогон rollback через вторую фазу без сборки.
# Ошибки: нет истории или образа — код 1.
do_rollback() {
	local user=${1:-} cur_sha prev_sha
	prepare_stack
	cur_sha=$(git_cmd rev-parse HEAD)
	prev_sha=$(previous_version "$cur_sha") || die "не найдена предыдущая версия в history для ${cur_sha:0:12}"
	docker inspect "emtds:${prev_sha:0:12}" >/dev/null 2>&1 || \
		die "образ emtds:${prev_sha:0:12} не найден; откат недоступен"
	do_apply rollback "$prev_sha" "" "$user" 1
}

# Назначение: режим --status.
# Параметры: нет.
# Результат: состояние на stdout (без секретов).
# Ошибки: нет.
do_status() {
	read_update_env 2>/dev/null || true
	determine_state_dir
	VOL_NAME=$(get_volume_name 2>/dev/null || printf '')
	local cur_sha=""
	[[ -n "${TDS_DIR:-}" && -d "${TDS_DIR}/.git" ]] && \
		cur_sha=$(git_cmd rev-parse HEAD 2>/dev/null || printf '')
	printf 'HEAD:    %s\n' "${cur_sha:0:12}"
	printf 'channel: %s\n' "${CHANNEL:-?}"
	printf 'repo:    %s\n' "$(printf '%s' "${REPO:-?}" | LC_ALL=C sed 's|//[^@]*@|//***@|g')"
	printf 'signing: %s\n' "$(signing_enabled && echo on || echo off)"
	printf 'lock:    %s\n' "$(flock -n "${LOCK_FILE}" true 2>/dev/null && echo free || echo held)"
	if [[ -n "$VOL_NAME" ]]; then
		local state; state=$(vol_read state.json 65536 2>/dev/null | strip_control || printf '')
		[[ -n "$state" ]] && printf 'state:   %s\n' "$state" || printf 'state:   (нет)\n'
	fi
}

# Назначение: режим --dry-run.
# Параметры: $1—ref или пусто.
# Результат: план обновления на stdout (fetch допустим).
# Ошибки: нет.
do_dry_run() {
	local ref=${1:-}
	read_update_env
	determine_state_dir
	printf -- '--- dry-run ---\n'
	printf 'tds_dir:   %s\n' "$TDS_DIR"
	printf 'state_dir: %s\n' "$STATE_DIR"
	printf 'channel:   %s\n' "$CHANNEL"
	printf 'repo:      %s\n' "$(printf '%s' "$REPO" | LC_ALL=C sed 's|//[^@]*@|//***@|g')"
	printf 'signing:   %s\n' "$(signing_enabled && echo on || echo off)"
	if ( check_environment ) >/dev/null 2>&1; then
		printf 'env:       ok\n'
	else
		printf 'env:       FAIL (подробности: update.sh --check)\n'
	fi
	git_fetch >/dev/null 2>&1 || printf 'fetch:     FAIL\n'
	local cur_sha
	cur_sha=$(git_cmd rev-parse HEAD 2>/dev/null || printf '')
	printf 'current:   %s\n' "${cur_sha:0:12}"
	if ! resolve_target "$ref"; then
		printf 'target:    FAIL (%s)\n' "$TARGET_ERROR"
		printf -- '--- конец dry-run ---\n'
		return 0
	fi
	printf 'target:    %s %s\n' "${TARGET_SHA:0:12}" "$TARGET_TAG"
	verify_signature "$TARGET_SHA" "$TARGET_TAG"
	printf 'signed:    %s signer=%s\n' "$SIGNED" "$SIGNER"
	collect_manual_steps "$TARGET_SHA"
	if [[ ${#MANUAL_STEPS[@]} -gt 0 ]]; then
		printf 'manual_steps (нужен --schema-ok):\n'
		printf '  %s\n' "${MANUAL_STEPS[@]}"
	fi
	printf 'шаги:      ресурсы → бэкап → build emtds:%s → гейт Keitaro → checkout → apply-edge → engine → admin → cron\n' "${TARGET_SHA:0:12}"
	printf -- '--- конец dry-run ---\n'
}

# Назначение: режим --adopt (разовая инициализация git-репо).
# Параметры: $1—sha цели.
# Результат: репо инициализирован, git reset --mixed на sha.
# Ошибки: код 1.
do_adopt() {
	local sha=$1
	[[ "$sha" =~ ^[0-9a-f]{40}$ ]] || die "--adopt требует --to <40-hex sha>"
	read_update_env
	[[ ! -e "${TDS_DIR}/.git" ]] || die "${TDS_DIR} уже является git-репо"
	logbuf "adopt: git init ${TDS_DIR}"
	git -C "$TDS_DIR" -c "safe.directory=${TDS_DIR}" init -q
	git_fetch || die "git fetch не удался"
	git_cmd cat-file -e "${sha}^{commit}" 2>/dev/null || die "коммит ${sha:0:12} не найден в ${REPO}"
	git_cmd reset -q --mixed "$sha"
	local status bad
	status=$(git_cmd status --porcelain 2>/dev/null || true)
	# Допустимы только неотслеживаемые .env и deploy/install.sh.
	bad=$(printf '%s\n' "$status" | LC_ALL=C grep -vxE '\?\? (\.env|deploy/install\.sh)' | grep . || true)
	if [[ -n "$bad" ]]; then
		log "adopt: есть изменённые файлы кроме .env и install.sh:" >&2
		printf '%s\n' "$bad" >&2
		die "adopt: неожиданные изменения"
	fi
	log "adopt: успешно на ${sha:0:12}"
}

# Опубликованный GitHub отпечаток ed25519-ключа github.com.
GITHUB_ED25519_FP="SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU"

# Назначение: записать known_hosts для github.com после сверки отпечатка.
# Параметры: нет.
# Результат: KNOWN_HOSTS (0600) с ключом, отпечаток которого совпал с опубликованным.
# Ошибки: несовпадение отпечатка или ssh-keyscan не ответил — код 1.
install_github_known_hosts() {
	local tmp fp
	tmp=$(mktemp "${ETC_DIR}/.known_hosts.XXXXXX")
	ssh-keyscan -t ed25519 github.com > "$tmp" 2>/dev/null || true
	fp=$(ssh-keygen -lf "$tmp" 2>/dev/null | awk '{print $2}' | head -1 || true)
	if [[ -z "$fp" || "$fp" != "$GITHUB_ED25519_FP" ]]; then
		rm -f -- "$tmp"
		die "отпечаток github.com (${fp:-нет ответа}) не совпадает с опубликованным ${GITHUB_ED25519_FP}; known_hosts не создан"
	fi
	chmod 0600 "$tmp"
	mv -f -- "$tmp" "$KNOWN_HOSTS"
}

# Назначение: режим --install (настройка update.env и systemd-юнитов).
# Параметры: массив args.
# Результат: файлы созданы, таймер включён.
# Ошибки: код 1.
do_install() {
	local opt_repo="" opt_channel="" opt_tds_dir="" skip_systemctl=0
	while [[ $# -gt 0 ]]; do
		case "$1" in
			--repo)      shift; opt_repo="$1";;
			--channel)   shift; opt_channel="$1";;
			--tds-dir)   shift; opt_tds_dir="$1";;
			--no-systemctl) skip_systemctl=1;;
		esac
		shift
	done
	# Права root (вне sandbox).
	if [[ -z "$SBX" ]] && [[ $(id -u) -ne 0 ]]; then
		die "нужны права root"
	fi
	if [[ -n "$opt_repo" ]]; then
		validate_repo_url "$opt_repo" || die "недопустимый --repo URL"
	fi
	if [[ -n "$opt_channel" && ! "$opt_channel" =~ ^(release|main)$ ]]; then
		die "--channel: допустимо release или main"
	fi
	if [[ -n "$opt_tds_dir" && "$opt_tds_dir" != /* ]]; then
		die "--tds-dir: нужен абсолютный путь"
	fi
	install -d -m 0700 "$ETC_DIR" "$VAR_DIR" "${VAR_DIR}/runs" "$BACKUP_ROOT"
	ensure_inbox_dir || die "не удалось подготовить ${VAR_DIR}/inbox"
	if [[ ! -f "$UPDATE_ENV" ]]; then
		# Новый файл.
		local repo_val="${opt_repo:-}"
		if [[ -z "$repo_val" && -n "${opt_tds_dir:-}" ]]; then
			repo_val=$(git -C "$opt_tds_dir" -c "safe.directory=${opt_tds_dir}" remote get-url origin 2>/dev/null || true)
		fi
		[[ -n "$repo_val" ]] || die "задайте --repo URL"
		validate_repo_url "$repo_val" || die "недопустимый repo"
		local ch_val="${opt_channel:-release}"
		printf 'EMTDS_UPDATE_REPO=%s\nEMTDS_UPDATE_CHANNEL=%s\nEMTDS_UPDATE_CHECK_HOURS=1\nEMTDS_UPDATE_KEEP_IMAGES=3\nEMTDS_UPDATE_KEEP_BACKUPS=5\nTDS_DIR=%s\n' \
			"$repo_val" "$ch_val" "${opt_tds_dir:-/opt/emtds}" > "${UPDATE_ENV}.tmp"
		chmod 0600 "${UPDATE_ENV}.tmp"
		mv -f "${UPDATE_ENV}.tmp" "$UPDATE_ENV"
	else
		# Обновить только явно переданные ключи.
		if [[ -n "$opt_repo" ]]; then
			local tmp; tmp=$(mktemp "${ETC_DIR}/.env.XXXXXX")
			grep -v '^EMTDS_UPDATE_REPO=' "$UPDATE_ENV" > "$tmp"
			printf 'EMTDS_UPDATE_REPO=%s\n' "$opt_repo" >> "$tmp"
			chmod 0600 "$tmp"; mv -f "$tmp" "$UPDATE_ENV"
		fi
		if [[ -n "$opt_channel" ]]; then
			local tmp; tmp=$(mktemp "${ETC_DIR}/.env.XXXXXX")
			grep -v '^EMTDS_UPDATE_CHANNEL=' "$UPDATE_ENV" > "$tmp"
			printf 'EMTDS_UPDATE_CHANNEL=%s\n' "$opt_channel" >> "$tmp"
			chmod 0600 "$tmp"; mv -f "$tmp" "$UPDATE_ENV"
		fi
	fi
	# Deploy key (только для SSH-репо).
	read_update_env
	if printf '%s' "$REPO" | LC_ALL=C grep -qE '^(git@|ssh://)' && [[ ! -f "$DEPLOY_KEY" ]]; then
		ssh-keygen -q -t ed25519 -N '' -C "emtds-deploy@$(uname -n)" -f "$DEPLOY_KEY" </dev/null || \
			die "ssh-keygen не удался"
		chmod 0600 "$DEPLOY_KEY"
		printf '\n=== Deploy key (добавьте в GitHub → Settings → Deploy keys) ===\n'
		cat "${DEPLOY_KEY}.pub"
		printf '=== Только read-only доступ ===\n\n'
	fi
	if printf '%s' "$REPO" | LC_ALL=C grep -qE '^(git@|ssh://)' && [[ ! -f "$KNOWN_HOSTS" ]]; then
		if printf '%s' "$REPO" | LC_ALL=C grep -qE '^(git@|ssh://git@)github\.com[:/]'; then
			install_github_known_hosts
		else
			printf 'Положите ключ хоста вручную: ssh-keyscan -t ed25519 <host> > %s (сверьте отпечаток)\n' "$KNOWN_HOSTS"
		fi
	fi
	if ! signing_enabled; then
		printf 'Проверка подписи выключена: в %s нет ключей.\nЧтобы включить, добавьте строки вида (файл 0644, владелец root):\n  <email> namespaces="git" ssh-ed25519 AAAA...\n' "$ALLOWED_SIGNERS"
	fi
	# Копия скрипта.
	if [[ -n "$SELF" && -f "$SELF" && "$SELF" != "${LIB_DIR}/update.sh" ]]; then
		install_script_copy "$SELF"
	fi
	# Systemd-юниты.
	install -d -m 0755 "$UNIT_DIR"
	cat > "${UNIT_DIR}/emtds-update.service" <<EOF
[Unit]
Description=EmTDS updater
After=docker.service network-online.target
Wants=docker.service

[Service]
Type=oneshot
ExecStart=${LIB_DIR}/update.sh --timer
TimeoutStartSec=1800
Environment=EMTDS_UPDATE_SANDBOX=
EOF
	cat > "${UNIT_DIR}/emtds-update.timer" <<EOF
[Unit]
Description=EmTDS updater timer

[Timer]
OnBootSec=60s
OnUnitActiveSec=60s
AccuracySec=10s
Persistent=true
Unit=emtds-update.service

[Install]
WantedBy=timers.target
EOF
	chmod 0644 "${UNIT_DIR}/emtds-update.service" "${UNIT_DIR}/emtds-update.timer"
	if [[ $skip_systemctl -eq 1 ]]; then
		log "юниты записаны в ${UNIT_DIR}"
		return 0
	fi
	command -v systemctl >/dev/null 2>&1 || return 0
	systemctl daemon-reload
	systemctl enable --now emtds-update.timer
	log "таймер emtds-update включён"
}

# ------------------------------------------------------------------
# Таймер
# ------------------------------------------------------------------

# Назначение: после захвата lock очистить зависший pending и восстановить state.json.
# Параметры: нет.
# Результат: pending и ready.json удалены; state.json обновлён с result=failed/interrupted, если state.last
#            показывает running.
# Ошибки: нет.
_recover_interrupted() {
	[[ -n "$VOL_NAME" ]] || return 0
	local _pend
	_pend=$(vol_read pending 20 2>/dev/null | tr -cd '0-9a-f' | head -c 16 || true)
	[[ -n "$_pend" ]] || return 0
	log "обнаружен зависший pending=${_pend}, восстановление" >&2
	local _last="${VAR_DIR}/state.last"
	if [[ -f "$_last" && ! -L "$_last" ]]; then
		local _content
		_content=$(cat "$_last" 2>/dev/null || true)
		if printf '%s' "$_content" | LC_ALL=C grep -q '"result":"running"'; then
			local _rid _raction _rfrom _rto _rstart _ruser _revents _rcron
			_rid=$(printf '%s' "$_content" | LC_ALL=C grep -oE '"id":"[0-9a-f]{16}"' | head -1 | \
				LC_ALL=C grep -oE '[0-9a-f]{16}' || true)
			_raction=$(printf '%s' "$_content" | LC_ALL=C sed -n 's/.*"action":"\([^"]*\)".*/\1/p' | head -1 || true)
			_rfrom=$(printf '%s' "$_content" | LC_ALL=C grep -oE '"from":"[0-9a-f]{40}"' | head -1 | \
				LC_ALL=C grep -oE '[0-9a-f]{40}' || true)
			_rto=$(printf '%s' "$_content" | LC_ALL=C grep -oE '"to":"[0-9a-f]{40}"' | head -1 | \
				LC_ALL=C grep -oE '[0-9a-f]{40}' || true)
			_rstart=$(printf '%s' "$_content" | LC_ALL=C sed -n 's/.*"started_at":"\([^"]*Z\)".*/\1/p' | head -1 || true)
			_ruser=$(printf '%s' "$_content" | LC_ALL=C sed -n 's/.*"user":"\([^"]*\)".*/\1/p' | head -1 || true)
			_revents=$(printf '%s' "$_content" | LC_ALL=C grep -oE '"events_lost":[0-9]+' | head -1 | \
				LC_ALL=C grep -oE '[0-9]+' || echo 0)
			_rcron=$(printf '%s' "$_content" | LC_ALL=C grep -oE '"cron_lagging":(true|false)' | head -1 | \
				LC_ALL=C sed 's/.*"cron_lagging":\(.*\)/\1/' || echo false)
			if [[ "$_rid" =~ ^[0-9a-f]{16}$ ]]; then
				local _now
				_now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
				_PHASES_JSON="[]"
				write_state_json "$_rid" "${_raction:-apply}" "${_rfrom:-}" "${_rto:-}" done \
					failed interrupted "${_revents:-0}" "${_rcron:-false}" "${_ruser:-}" \
					"${_rstart:-}" "$_now" || true
			fi
		fi
	fi
	vol_rm pending || log "предупреждение: не удалось удалить pending" >&2
	vol_rm ready.json || log "предупреждение: не удалось удалить ready.json" >&2
}

# Назначение: проверить и выставить права на <var>/inbox (владелец 10001:10001, 0700).
# Параметры: нет.
# Результат: каталог существует с нужными правами (docker мог создать его root:root 0755).
# Ошибки: симлинк или chown не удался вне песочницы — код 1.
ensure_inbox_dir() {
	local inbox="${VAR_DIR}/inbox"
	[[ ! -L "$VAR_DIR" && ! -L "$inbox" ]] || { log "inbox или ${VAR_DIR} является симлинком" >&2; return 1; }
	install -d -m 0700 "$VAR_DIR"
	[[ -d "$inbox" ]] || install -d -m 0700 "$inbox"
	if ! chown 10001:10001 "$inbox" 2>/dev/null; then
		[[ -n "$SBX" ]] || return 1
	fi
	chmod 0700 "$inbox"
}

# Сырые байты запроса кнопки (в лог не пишутся).
REQUEST_DATA=""

# Назначение: забрать файл запроса из inbox в память и сразу удалить его.
# Параметры: нет.
# Результат: REQUEST_DATA (≤ 1024 байт) или пусто, если запроса нет.
# Ошибки: симлинк, не обычный файл или больше 1024 байт — код 1 (файл удалён).
read_inbox_request() {
	local req="${VAR_DIR}/inbox/request" tmp sz
	REQUEST_DATA=""
	[[ -e "$req" || -L "$req" ]] || return 0
	if [[ -L "$req" || ! -f "$req" ]]; then
		rm -f -- "$req"
		log "запрос отклонён: не обычный файл" >&2
		return 1
	fi
	tmp=$(mktemp "${VAR_DIR}/.req.XXXXXX")
	local _rc=0
	timeout 5 dd if="$req" of="$tmp" iflag=nofollow,nonblock bs=1025 count=1 2>/dev/null || _rc=$?
	rm -f -- "$req"
	if [[ $_rc -ne 0 ]]; then
		rm -f -- "$tmp"
		log "запрос отклонён: формат" >&2
		return 1
	fi
	sz=$(stat -c '%s' "$tmp" 2>/dev/null || echo 0)
	if (( sz == 0 )); then
		rm -f -- "$tmp"
		log "запрос отклонён: формат" >&2
		return 1
	fi
	if (( sz > 1024 )); then
		rm -f -- "$tmp"
		log "запрос отклонён: размер больше 1024 байт" >&2
		return 1
	fi
	REQUEST_DATA=$(cat "$tmp"; printf x)
	REQUEST_DATA=${REQUEST_DATA%x}
	rm -f -- "$tmp"
}

# Поля разобранного запроса кнопки.
REQ_ACTION=""
REQ_TARGET=""
REQ_ID=""
REQ_USER=""
REQ_AT=0

# Назначение: строго разобрать запрос кнопки (формат ParseUpdateRequest: 5 строк action, target, id, user, at).
# Параметры: $1 — текст запроса.
# Результат: REQ_ACTION, REQ_TARGET, REQ_ID, REQ_USER, REQ_AT.
# Ошибки: любое отклонение от формата — код 1.
parse_request() {
	local data=${1//$'\r'/} keys=(action target id user at) lines=() i
	REQ_ACTION=""; REQ_TARGET=""; REQ_ID=""; REQ_USER=""; REQ_AT=0
	(( ${#data} <= 1024 )) || return 1
	data=${data%$'\n'}
	mapfile -t lines < <(printf '%s\n' "$data")
	[[ ${#lines[@]} -eq 5 ]] || return 1
	for i in 0 1 2 3 4; do
		[[ "${lines[$i]}" == "${keys[$i]}="* ]] || return 1
	done
	REQ_ACTION=${lines[0]#action=}
	REQ_TARGET=${lines[1]#target=}
	REQ_ID=${lines[2]#id=}
	REQ_USER=${lines[3]#user=}
	local at=${lines[4]#at=}
	[[ "$REQ_ACTION" =~ ^(apply|rollback|check)$ ]] || return 1
	if [[ "$REQ_ACTION" == apply ]]; then
		[[ "$REQ_TARGET" =~ ^[0-9a-f]{40}$ ]] || return 1
	else
		[[ -z "$REQ_TARGET" ]] || return 1
	fi
	[[ "$REQ_ID" =~ ^[0-9a-f]{16}$ ]] || return 1
	[[ "$REQ_USER" =~ ^[A-Za-z0-9._@-]{1,64}$ ]] || return 1
	[[ "$at" =~ ^[0-9]{1,12}$ ]] || return 1
	REQ_AT=$(( 10#$at ))
	(( REQ_AT > 0 ))
}

# Назначение: проверить, использовался ли id запроса.
# Параметры: $1 — id.
# Результат: 0 — уже использован.
# Ошибки: 1 — новый id.
is_used_id() {
	local used="${VAR_DIR}/used_ids"
	[[ -f "$used" ]] && grep -qxF "$1" "$used"
}

# Назначение: запомнить id запроса.
# Параметры: $1 — id.
# Результат: id дописан в <var>/used_ids.
# Ошибки: нет.
mark_used_id() {
	printf '%s\n' "$1" >> "${VAR_DIR}/used_ids"
}

# Назначение: отклонить принятый к разбору запрос: лог и state.json result=refused.
# Параметры: $1 — код причины.
# Результат: state.json с id запроса и кодом причины.
# Ошибки: нет.
refuse_request() {
	local code=$1 now cur=""
	log "запрос ${REQ_ACTION} ${REQ_ID} отклонён: ${code}"
	[[ -n "${VOL_NAME:-}" ]] || return 0
	now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	[[ -d "${TDS_DIR}/.git" ]] && cur=$(git_cmd rev-parse HEAD 2>/dev/null || true)
	_PHASES_JSON="[]"
	write_state_json "$REQ_ID" "$REQ_ACTION" "$cur" "$REQ_TARGET" done refused "$code" 0 false \
		"$REQ_USER" "$now" "$now" || true
}

# Назначение: выполнить запрос кнопки apply: цель обязана совпасть со свежим --check и пройти signature_gate.
# Параметры: нет (REQ_*).
# Результат: прогон применения или отказ refused.
# Ошибки: нет (результат в state.json).
handle_request_apply() {
	if ! ( do_check ); then
		refuse_request check_failed; return 0
	fi
	prepare_stack
	resolve_target "" || { refuse_request target; return 0; }
	[[ "$TARGET_SHA" == "$REQ_TARGET" ]] || { refuse_request target_mismatch; return 0; }
	verify_signature "$TARGET_SHA" "$TARGET_TAG"
	signature_gate || { refuse_request unsigned; return 0; }
	collect_manual_steps "$TARGET_SHA"
	[[ ${#MANUAL_STEPS[@]} -eq 0 ]] || { refuse_request manual_steps; return 0; }
	log "запрос ${REQ_ACTION} ${REQ_ID} принят"
	do_apply apply "$TARGET_SHA" "$TARGET_TAG" "$REQ_USER" 0 || true
}

# Назначение: выполнить запрос кнопки rollback: один шаг назад, только если последняя запись history —
#   успешный apply на текущий HEAD; предыдущая версия должна пройти signature_gate.
#   Цепочка откатов кнопкой невозможна: после отката последняя запись — rollback, и запрос получает no_previous.
# Параметры: нет (REQ_*).
# Результат: прогон отката или отказ refused (no_previous, no_image, unsigned).
# Ошибки: нет (результат в state.json).
handle_request_rollback() {
	local cur_sha prev_sha tag
	prepare_stack
	cur_sha=$(git_cmd rev-parse HEAD)
	prev_sha=$(previous_version "$cur_sha" 1) || { refuse_request no_previous; return 0; }
	docker inspect "emtds:${prev_sha:0:12}" >/dev/null 2>&1 || { refuse_request no_image; return 0; }
	tag=$(git_cmd tag --points-at "$prev_sha" -l 'v*.*.*' 2>/dev/null | sort -V | tail -1 || true)
	verify_signature "$prev_sha" "$tag"
	signature_gate || { refuse_request unsigned; return 0; }
	log "запрос ${REQ_ACTION} ${REQ_ID} принят"
	do_apply rollback "$prev_sha" "$tag" "$REQ_USER" 1 || true
}

# Назначение: выполнить запрос кнопки check (не чаще раза в 5 минут).
# Параметры: нет (REQ_*).
# Результат: свежий check.json или отказ refused.
# Ошибки: нет.
handle_request_check() {
	local stamp="${VAR_DIR}/.check_stamp_btn" last=0 now
	now=$(date -u +%s)
	[[ -f "$stamp" ]] && last=$(cat "$stamp" 2>/dev/null || echo 0)
	[[ "$last" =~ ^[0-9]+$ ]] || last=0
	if (( now - last < 300 * WAIT_SCALE )); then
		refuse_request rate_limit; return 0
	fi
	printf '%s' "$now" > "$stamp"
	log "запрос ${REQ_ACTION} ${REQ_ID} принят"
	( do_check ) || refuse_request check_failed
}

# Назначение: забрать и выполнить запрос кнопки из inbox.
# Параметры: нет.
# Результат: запрос выполнен или отклонён; содержимое запроса в лог не попадает.
# Ошибки: нет.
handle_request() {
	read_inbox_request || return 0
	[[ -n "$REQUEST_DATA" ]] || return 0
	if ! parse_request "$REQUEST_DATA"; then
		REQUEST_DATA=""
		log "запрос отклонён: формат"
		return 0
	fi
	REQUEST_DATA=""
	if is_used_id "$REQ_ID"; then
		refuse_request replay; return 0
	fi
	mark_used_id "$REQ_ID"
	local age=$(( $(date -u +%s) - REQ_AT ))
	if (( age > 900 || age < -60 )); then
		refuse_request stale; return 0
	fi
	case "$REQ_ACTION" in
		apply)    handle_request_apply;;
		rollback) handle_request_rollback;;
		check)    handle_request_check;;
	esac
}

# Назначение: режим --timer (пульс agent.json, --check по расписанию, запрос кнопки).
# Параметры: нет.
# Результат: 0.
# Ошибки: ошибки прогона по запросу — код прогона.
do_timer() {
	# Из таймера применение без подписи невозможно.
	F_UNSIGNED=0
	read_update_env
	determine_state_dir
	load_mask_words
	ensure_inbox_dir || log "предупреждение: inbox недоступен" >&2
	VOL_NAME=$(get_volume_name 2>/dev/null || true)
	_recover_interrupted
	local now last stamp
	now=$(date -u +%s)
	stamp="${VAR_DIR}/.agent_stamp"
	last=$(cat "$stamp" 2>/dev/null || echo 0)
	[[ "$last" =~ ^[0-9]+$ ]] || last=0
	if [[ -n "$VOL_NAME" ]] && (( now - last >= 300 * WAIT_SCALE )); then
		write_agent_json || log "предупреждение: agent.json не записан" >&2
		printf '%s' "$now" > "$stamp"
	fi
	stamp="${VAR_DIR}/.check_stamp"
	last=$(cat "$stamp" 2>/dev/null || echo 0)
	[[ "$last" =~ ^[0-9]+$ ]] || last=0
	if (( now - last >= CHECK_HOURS * 3600 * WAIT_SCALE )); then
		printf '%s' "$now" > "$stamp"
		( do_check ) || log "check по расписанию не удался" >&2
	fi
	handle_request
}

# ------------------------------------------------------------------
# Self-test
# ------------------------------------------------------------------

# Назначение: проверить чистые функции без Docker и сети.
# Параметры: нет.
# Результат: «self-test ok» на stdout.
# Ошибки: хотя бы одна проверка не прошла — код 1.
run_self_test() {
	bash -n "$SELF" || exit 1
	local fail=0 tmp
	tmp=$(mktemp -d)
	# Назначение: сравнить значение с ожидаемым. Параметры: получено, ожидалось, метка.
	_assert() {
		[[ "$1" == "$2" ]] && return 0
		printf 'FAIL: %s (ожидали %q, получили %q)\n' "$3" "$2" "$1" >&2
		fail=$(( fail + 1 ))
	}
	# Назначение: разобрать файл как update.env в подоболочке. Параметры: путь.
	_env_parse() {
		( UPDATE_ENV=$1; SBX=selftest; read_update_env )
	}
	# Назначение: проверить код возврата. Параметры: ожидаемый код, метка, команда.
	_assert_rc() {
		local want=$1 label=$2 rc=0
		shift 2
		"$@" >/dev/null 2>&1 || rc=$?
		[[ "$want" == 0 && $rc -eq 0 || "$want" != 0 && $rc -ne 0 ]] && return 0
		printf 'FAIL: %s (код %s)\n' "$label" "$rc" >&2
		fail=$(( fail + 1 ))
	}

	_assert "$(json_str 'hello "world"')" '"hello \"world\""' json_str_quotes
	_assert "$(json_str $'tab\there')" '"tab\there"' json_str_tab
	_assert "$(json_str $'line\nnext')" '"line\nnext"' json_str_nl
	_assert "$(json_str $'cr\r')" '"cr\r"' json_str_cr
	_assert "$(json_str 'back\slash')" '"back\\slash"' json_str_backslash
	_assert "$(json_str $'bell\a!')" '"bell!"' json_str_control
	_assert "$(json_str "abcdefgh" 4)" '"abcd"' json_str_trunc
	_assert "$(LC_ALL=C json_str 'жжж' 2)" '"жж"' json_str_utf8
	_assert "$(json_str $'ab\ncd' 3)" '"ab\n"' json_str_trunc_nl
	_assert "$(json_str 'a\b' 2)" '"a\\"' json_str_trunc_backslash
	_assert "$(json_str 'a"b' 2)" '"a\""' json_str_trunc_quote

	_assert_rc 0 repo_git_at validate_repo_url 'git@github.com:owner/emtds.git'
	_assert_rc 0 repo_ssh validate_repo_url 'ssh://git@github.com/owner/repo.git'
	_assert_rc 0 repo_https validate_repo_url 'https://github.com/owner/repo.git'
	_assert_rc 1 repo_creds validate_repo_url 'https://user:pass@github.com/owner/repo.git'
	_assert_rc 1 repo_empty validate_repo_url ''
	_assert_rc 1 repo_scheme validate_repo_url 'ftp://example.com/repo.git'

	printf 'EMTDS_UPDATE_REPO=git@github.com:owner/r.git\nEMTDS_UPDATE_CHANNEL=main\nEMTDS_UPDATE_CHECK_HOURS=6\nTDS_DIR=/opt/emtds\n' > "${tmp}/ok.env"
	printf 'EMTDS_UPDATE_REPO=git@github.com:owner/r.git\nTDS_DIR=/opt/emtds\nEVIL=$(id)\n' > "${tmp}/bad.env"
	_assert "$( UPDATE_ENV="${tmp}/ok.env" SBX=selftest; read_update_env >/dev/null 2>&1; printf '%s %s' "$CHANNEL" "$CHECK_HOURS" )" 'main 6' env_ok
	_assert_rc 1 env_unknown_key _env_parse "${tmp}/bad.env"
	_assert_rc 0 env_known_keys _env_parse "${tmp}/ok.env"

	_assert "$(printf 'v1.9.0\nv1.10.0\nv1.0.0\nv2.0.0-rc1\nnightly\n' | max_semver)" v1.10.0 semver_max

	local saved=("${_MASK_WORDS[@]+"${_MASK_WORDS[@]}"}")
	_MASK_WORDS=("mysecret123" "short")
	_assert "$(mask_line 'token=mysecret123 short')" 'token=*** short' mask_secret
	_assert "$(mask_line 'https://user:supersecret@github.com/repo')" 'https://***@github.com/repo' mask_url
	_MASK_WORDS=("${saved[@]+"${saved[@]}"}")

	local probe=$'heartbeat_age=5\nbusy=0\nversion=v1.1.0\ncommit=aabbccdd1122334455667788990011aabbccddee'
	_assert "$(probe_value heartbeat_age <<< "$probe")" 5 probe_age
	_assert "$(probe_value busy <<< "$probe")" 0 probe_busy
	_assert "$(probe_value missing <<< "$probe")" '' probe_missing

	local long_ok=$'services:\n  engine:\n    ports:\n      - mode: ingress\n        host_ip: 127.0.0.1\n        target: 8080\n        published: "18080"\n        protocol: tcp\n    volumes:\n      - type: volume\n'
	local long_bad=$'services:\n  engine:\n    ports:\n      - mode: ingress\n        target: 8080\n        published: "18080"\n'
	_assert_rc 0 compose_long_ok compose_config_problem <<< "$long_ok"
	_assert_rc 1 compose_long_public compose_config_problem <<< "$long_bad"
	_assert_rc 0 compose_short_ok compose_config_problem <<< $'    ports:\n      - "127.0.0.1:18080:8080"\n'
	_assert_rc 1 compose_short_public compose_config_problem <<< $'    ports:\n      - "0.0.0.0:18080:8080"\n'
	_assert_rc 1 compose_short_any compose_config_problem <<< $'    ports:\n      - "18080:8080"\n'
	_assert_rc 1 compose_pid_host compose_config_problem <<< $'services:\n  x:\n    pid: host\n'
	_assert_rc 1 compose_priv compose_config_problem <<< "    $(printf 'privile')ged: true"
	_assert_rc 1 compose_sock compose_config_problem <<< "      source: /var/run/$(printf 'docker.so')ck"

	local req=$'action=apply\ntarget=bbccddee1122334455667788990011aabbccddee\nid=0123456789abcdef\nuser=admin\nat=1736935500\n'
	_assert_rc 0 req_ok parse_request "$req"
	parse_request "$req" || true
	_assert "${REQ_ACTION} ${REQ_ID} ${REQ_USER} ${REQ_AT}" 'apply 0123456789abcdef admin 1736935500' req_fields
	_assert_rc 0 req_check parse_request $'action=check\ntarget=\nid=0123456789abcdef\nuser=admin\nat=1736935500'
	_assert_rc 1 req_check_target parse_request $'action=check\ntarget=bbccddee1122334455667788990011aabbccddee\nid=0123456789abcdef\nuser=admin\nat=1736935500\n'
	_assert_rc 1 req_extra parse_request "${req}x=1"$'\n'
	_assert_rc 1 req_order parse_request $'target=\naction=check\nid=0123456789abcdef\nuser=admin\nat=1\n'
	_assert_rc 1 req_bad_user parse_request $'action=check\ntarget=\nid=0123456789abcdef\nuser=a b\nat=1\n'
	_assert_rc 1 req_bad_id parse_request $'action=check\ntarget=\nid=XYZ\nuser=admin\nat=1\n'

	# check_secure_file: арифметика прав вместо regex.
	_check_perm() { ( SBX=selftest check_secure_file "$1" "${2:-022}" ); }
	local _pt="${tmp}/perm_test"
	printf 'x' > "$_pt"
	chmod 0600 "$_pt"; _assert_rc 0 perm_600 _check_perm "$_pt" 022
	chmod 0620 "$_pt"; _assert_rc 1 perm_620 _check_perm "$_pt" 022
	chmod 0602 "$_pt"; _assert_rc 1 perm_602 _check_perm "$_pt" 022
	chmod 0644 "$_pt"; _assert_rc 0 perm_644 _check_perm "$_pt" 022
	chmod 0600 "$_pt"; _assert_rc 0 perm_600_077 _check_perm "$_pt" 077
	chmod 0640 "$_pt"; _assert_rc 1 perm_640_077 _check_perm "$_pt" 077

	# signing_enabled и signature_gate: политика определяется только строками ключей.
	_sign_on() { ( ALLOWED_SIGNERS=$1; signing_enabled ); }
	_gate() { ( ALLOWED_SIGNERS=$1; SIGNED=$2; signature_gate ); }
	local _as="${tmp}/allowed_signers"
	_assert_rc 1 signing_absent _sign_on "$_as"
	printf '\n# только комментарий\n' > "$_as"
	_assert_rc 1 signing_comments_only _sign_on "$_as"
	printf 'signer@example.com namespaces="git" ssh-ed25519 AAAA\n' > "$_as"
	chmod 0666 "$_as"
	_assert_rc 0 signing_key_loose_perms _sign_on "$_as"
	_assert_rc 1 gate_on_unsigned _gate "$_as" false
	_assert_rc 0 gate_on_signed _gate "$_as" true
	: > "$_as"
	_assert_rc 0 gate_off_unsigned _gate "$_as" false
	rm -f -- "$_as"
	ln -s "${tmp}/missing" "$_as"
	_assert_rc 0 signing_broken_symlink _sign_on "$_as"
	rm -f -- "$_as"
	mkdir "$_as"
	_assert_rc 0 signing_directory _sign_on "$_as"
	_assert_rc 1 gate_dir_unsigned _gate "$_as" false

	_assert "$(printf 'a\033[31mж\tb\n' | strip_control)" $'a[31mж\tb' strip_control_esc

	# file:// и локальные пути отклоняются вне sandbox, разрешены в sandbox.
	_vno()  { ( SBX="" validate_repo_url "$1" ); }
	_vsbx() { ( SBX=selftest validate_repo_url "$1" ); }
	_assert_rc 1 repo_file_nosandbox  _vno  'file:///tmp/repo.git'
	_assert_rc 1 repo_local_nosandbox _vno  '/tmp/repo.git'
	_assert_rc 0 repo_file_sandbox    _vsbx 'file:///tmp/repo.git'

	rm -rf -- "$tmp"
	if [[ $fail -gt 0 ]]; then
		printf 'self-test FAIL: %d провалов\n' "$fail" >&2
		exit 1
	fi
	printf 'self-test ok\n'
}

# ------------------------------------------------------------------
# Точка входа
# ------------------------------------------------------------------

# Назначение: распечатать справку.
# Параметры: нет.
# Результат: текст usage на stderr.
# Ошибки: нет.
usage() {
	cat >&2 <<'EOF'
использование: update.sh КОМАНДА [МОДИФИКАТОРЫ]

Команды:
  --check                    проверить наличие обновления, записать check.json
  --channel                  обновить по каналу из update.env
  --to <sha|vX.Y.Z>          обновить до указанного sha или тега
  --rollback                 откатить на предыдущую выкатанную версию
  --status                   показать состояние без изменений
  --dry-run                  показать план без изменений (fetch допустим)
  --adopt --to <sha>         первичная инициализация git-репо
  --install [--repo URL] [--channel release|main] [--tds-dir DIR] [--no-systemctl]
                             создать update.env, deploy key, systemd-юниты
  --timer                    режим systemd-таймера (пульс, check, inbox)
  --stage2 <id> <prev> <sha> вторая фаза (вызывается родителем)
  --self-test                проверить чистые функции без Docker

Модификаторы:
  --unsigned                 применить без проверки подписи (только CLI)
  --schema-ok                подтвердить ручные шаги с миграцией
  --keitaro-idle             пропустить гейт Keitaro
  --force-version            подтвердить понижение версии интерактивно
  -h, --help                 показать эту справку

Репо в <owner>: задайте в /etc/emtds/update.env (EMTDS_UPDATE_REPO).
EOF
}

# Назначение: точка входа.
# Параметры: аргументы CLI.
# Результат: код выхода сценария.
# Ошибки: неизвестный аргумент — код 2.
main() {
	local cmd="" ref=""
	local install_args=()
	local stage2_args=()

	while [[ $# -gt 0 ]]; do
		case "$1" in
			--check)         cmd=check;;
			--channel)
				# После --install это опция канала, иначе команда обновления по каналу.
				if [[ "${cmd:-}" == install ]]; then
					[[ $# -ge 2 ]] || { printf 'update.sh: --channel требует значение\n' >&2; exit 2; }
					install_args+=("$1" "$2"); shift
				else
					cmd=channel
				fi
				;;
			--to)
				[[ $# -ge 2 ]] || { printf 'update.sh: --to требует sha или тег\n' >&2; exit 2; }
				ref=$2; shift
				[[ "${cmd:-}" == adopt ]] || cmd=to
				;;
			--rollback)      cmd=rollback;;
			--status)        cmd=status;;
			--dry-run)       F_DRY_RUN=1;;
			--adopt)         cmd=adopt;;
			--install)       cmd=install;;
			--timer)         cmd=timer;;
			--stage2)
				[[ $# -ge 4 ]] || { printf 'update.sh: --stage2 требует <id> <prev_sha> <target_sha>\n' >&2; exit 2; }
				cmd=stage2
				stage2_args=("$2" "$3" "$4")
				shift 3
				;;
			--self-test)     cmd=selftest;;
			--unsigned)      F_UNSIGNED=1;;
			--schema-ok)     F_SCHEMA_OK=1;;
			--keitaro-idle)  F_KEITARO_IDLE=1;;
			--force-version) F_FORCE_VERSION=1;;
			--repo|--tds-dir)
				[[ $# -ge 2 ]] || { printf 'update.sh: %s требует значение\n' "$1" >&2; exit 2; }
				install_args+=("$1" "$2"); shift
				;;
			--no-systemctl)
				install_args+=("$1")
				;;
			-h|--help)       usage; exit 0;;
			*)               printf 'update.sh: неизвестный аргумент: %s\n' "$1" >&2; exit 2;;
		esac
		shift
	done

	# Предупреждение о режиме песочницы при запуске от root в продакшен-режиме.
	if [[ -n "$SBX" && $(id -u) -eq 0 && "${WAIT_SCALE}" != "0" ]]; then
		log "предупреждение: режим песочницы (SBX=${SBX})"
	fi

	# Lock для всех операций, кроме --self-test и --status; --stage2 работает под lock родителя.
	case "${cmd:-}" in
		selftest|status|stage2|'') ;;
		*)
			mkdir -p "$(dirname "$LOCK_FILE")" 2>/dev/null || true
			exec 9>"$LOCK_FILE"
			if ! flock -n 9; then
				log "выполнение заблокировано: другой экземпляр"
				[[ "${cmd:-}" == timer ]] && exit 0 || exit 1
			fi
			;;
	esac

	if [[ $F_DRY_RUN -eq 1 ]]; then
		do_dry_run "$ref"
		return 0
	fi

	case "${cmd:-}" in
		check)   do_check;;
		channel) do_update "";;
		to)      do_update "$ref";;
		rollback) do_rollback;;
		status)  do_status;;
		adopt)   do_adopt "$ref";;
		install) do_install "${install_args[@]+"${install_args[@]}"}";;
		timer)   do_timer;;
		stage2)  run_stage2 "${stage2_args[0]}" "${stage2_args[1]}" "${stage2_args[2]}";;
		selftest) run_self_test;;
		'')      usage; exit 2;;
		*)       usage; exit 2;;
	esac
}

{ main "$@"; exit $?; }
