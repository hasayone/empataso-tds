package deploy

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestInstallSelfTest проверяет, что install.sh --self-test завершается успешно.
func TestInstallSelfTest(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "install.sh")
	if out, err := exec.Command(bash, "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("bash -n install.sh: %v\n%s", err, out)
	}
	cmd := exec.Command(bash, script, "--self-test")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "EMTDS_INSTALL_STATE_DIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("self-test завершился с ошибкой: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "self-test ok") {
		t.Fatalf("ожидали self-test ok в выводе:\n%s", out)
	}
}

// TestInstallForbiddenCommands запрещает опасные фрагменты и CRLF в install.sh.
func TestInstallForbiddenCommands(t *testing.T) {
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("\r")) {
		t.Fatal("install.sh содержит CRLF (\\r)")
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || lines[0] != "#!/usr/bin/env bash" {
		t.Fatalf("первая строка должна быть #!/usr/bin/env bash, получено: %q", lines[0])
	}
	forbidden := []string{
		"compose down",
		" down",
		"volume rm",
		"system prune",
		"network prune",
		"empataso",
		"set -x",
		"daemon.json",
		"restart docker",
		"--project-directory",
	}
	for i, line := range lines {
		for _, frag := range forbidden {
			if strings.Contains(line, frag) {
				t.Fatalf("строка %d содержит запрещённый фрагмент %q: %q", i+1, frag, line)
			}
		}
	}
	timeoutFn := regexp.MustCompile(`timeout\s+\d+\s+(app_compose|infra_compose)`)
	if timeoutFn.Match(data) {
		t.Fatal("install.sh: timeout вызывает shell-функцию compose вместо массива argv")
	}
}

// TestInstallComposePortDefaults проверяет переменные публикации портов в compose-файлах.
func TestInstallComposePortDefaults(t *testing.T) {
	infra, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(infra)
	for _, want := range []string{
		"${EMTDS_CH_HTTP_PUBLISH:-127.0.0.1:8123:8123}",
		"${EMTDS_CH_NATIVE_PUBLISH:-127.0.0.1:9000:9000}",
		"${EMTDS_REDIS_PUBLISH:-127.0.0.1:6379:6379}",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("docker-compose.yml: нет %q", want)
		}
	}
	if strings.Count(s, "restart: unless-stopped") < 2 {
		t.Fatal("docker-compose.yml: у clickhouse и redis должен быть restart: unless-stopped")
	}
	install, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	src := string(install)
	start := strings.Index(src, "render_infra_override() {")
	end := strings.Index(src, "render_app_override() {")
	if start < 0 || end < start {
		t.Fatal("install.sh: не найден render_infra_override")
	}
	if strings.Contains(src[start:end], "restart:") {
		t.Fatal("render_infra_override не должен задавать restart")
	}
	app, err := os.ReadFile("docker-compose.app.yml")
	if err != nil {
		t.Fatal(err)
	}
	as := string(app)
	if !strings.Contains(as, "${EMTDS_ADMIN_PUBLISH:-127.0.0.1:8090:8090}") {
		t.Fatal("docker-compose.app.yml: нет EMTDS_ADMIN_PUBLISH с loopback-умолчанием")
	}
	if !strings.Contains(as, "${EMTDS_ENGINE_PUBLISH:-127.0.0.1:8080:8080}") {
		t.Fatal("docker-compose.app.yml: порт engine должен публиковаться через EMTDS_ENGINE_PUBLISH с умолчанием 127.0.0.1:8080:8080")
	}
	if !strings.Contains(as, "EMTDS_TRUSTED_PROXIES: ${EMTDS_TRUSTED_PROXIES:-}") {
		t.Fatal("docker-compose.app.yml: нет EMTDS_TRUSTED_PROXIES у engine")
	}
	if !strings.Contains(as, "EMTDS_PUBLIC_IP: ${EMTDS_PUBLIC_IP:-}") {
		t.Fatal("docker-compose.app.yml: нет EMTDS_PUBLIC_IP у admin")
	}
	if n := strings.Count(as, "GOMEMLIMIT: ${EMTDS_GOMEMLIMIT:-}"); n != 2 {
		t.Fatalf("docker-compose.app.yml: GOMEMLIMIT должен быть у engine и admin, найдено %d", n)
	}
}

// TestInstallAppOverrideGoMemLimit проверяет GOMEMLIMIT около 90% mem_limit в render_app_override.
func TestInstallAppOverrideGoMemLimit(t *testing.T) {
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	start := strings.Index(s, "render_app_override() {")
	if start < 0 {
		t.Fatal("install.sh: не найден render_app_override")
	}
	body := s[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	if n := strings.Count(body, `GOMEMLIMIT: \${EMTDS_GOMEMLIMIT:-${go_lim}}`); n != 2 {
		t.Fatalf("render_app_override: GOMEMLIMIT должен быть у engine и admin, найдено %d", n)
	}
	for _, want := range []string{"1g) printf '920MiB\\n'", "2g) printf '1840MiB\\n'"} {
		if !strings.Contains(s, want) {
			t.Fatalf("install.sh: app_gomemlimit без %q", want)
		}
	}
}

// TestInstallUpdateHook статически проверяет интеграцию агента обновлений в install.sh:
// фильтр юнитов, вызов update.sh --install, защиту от самоудаления при .git,
// build-args, канал по умолчанию release, отсутствие захардкоженного адреса репо.
func TestInstallUpdateHook(t *testing.T) {
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		`grep -Ev '^emtds-(watchdog|update)\.'`,
		`update_args=(--install "--channel" "${INSTALL_CHANNEL}" "--tds-dir" "${TDS_DIR}")`,
		`bash "${OUR_DEPLOY_DIR}/update.sh" "${update_args[@]}"`,
		"нет git-дерева, см. update.sh --adopt",
		`if can_self_delete "$SELF" && [[ ! -d "${TDS_DIR}/.git" ]]; then`,
		`INSTALL_CHANNEL="${EMTDS_INSTALL_CHANNEL:-release}"`,
		`git -c safe.directory="$TDS_DIR" -C "$TDS_DIR" describe --tags --always`,
		`git -c safe.directory="$TDS_DIR" -C "$TDS_DIR" rev-parse HEAD`,
		`git -c safe.directory="$TDS_DIR" -C "$TDS_DIR" show -s --format=%cI HEAD`,
		`export EMTDS_CHANNEL="$channel"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("install.sh: нет %q", want)
		}
	}
	if strings.Index(s, "export_build_args \"${INSTALL_CHANNEL}\"") > strings.Index(s, `"${APP_COMPOSE[@]}" build engine`) {
		t.Fatal("install.sh: build-args должны экспортироваться до сборки образа")
	}
	if strings.Index(s, `watchdog.sh" --install`) < strings.Index(s, `update.sh" "${update_args[@]}"`) {
		t.Fatal("install.sh: таймер watchdog включается после update.sh --install")
	}
	for _, bad := range []string{"empataso", "git@github.com:", "https://github.com/"} {
		if strings.Contains(s, bad) {
			t.Fatalf("install.sh: запрещённая подстрока %q", bad)
		}
	}
}

// TestInstallApplyEdgeHook проверяет source apply-edge, ensure_server_env_keys и пропуск без nginx.
func TestInstallApplyEdgeHook(t *testing.T) {
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, `source "${OUR_DEPLOY_DIR}/apply-edge.sh"`) {
		t.Fatal("install.sh: ожидали source apply-edge.sh")
	}
	if !strings.Contains(s, "ensure_server_env_keys") {
		t.Fatal("install.sh: ожидали ensure_server_env_keys")
	}
	if !strings.Contains(s, "command -v nginx") {
		t.Fatal("install.sh: ожидали проверку nginx перед apply-edge")
	}
	if strings.Contains(s, "trusted_proxies_cidr") {
		t.Fatal("install.sh: не должно быть дубликата trusted_proxies_cidr")
	}
	if regexp.MustCompile(`(?m)^valid_ipv4\(\)`).Match(data) {
		t.Fatal("install.sh: valid_ipv4 должен быть только в apply-edge.sh")
	}
}

// TestInstallRedisTiers проверяет тиры Redis под окно уникальности в сутки: 512mb до 12000 MiB
// и 2gb от 12000 MiB (1gb держит около 9 млн кликов в сутки при цели 14,4 млн), лимит контейнера в полтора раза выше maxmemory, переменную
// потолка у engine и admin в docker-compose.app.yml.
func TestInstallRedisTiers(t *testing.T) {
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	max := shellFunc(t, s, "redis_maxmemory")
	limit := shellFunc(t, s, "redis_mem_limit")
	for _, want := range []string{"mem < 12000", "printf '512mb\\n'", "printf '2gb\\n'"} {
		if !strings.Contains(max, want) {
			t.Fatalf("redis_maxmemory: нет %q", want)
		}
	}
	if strings.Contains(max, "1gb") || strings.Contains(max, "28000") {
		t.Fatal("redis_maxmemory: тира 1gb быть не должно, 2gb начинается с 12000 MiB")
	}
	for _, want := range []string{"mem < 12000", "printf '768m\\n'", "printf '3g\\n'"} {
		if !strings.Contains(limit, want) {
			t.Fatalf("redis_mem_limit: нет %q", want)
		}
	}
	for _, want := range []string{
		`assert_eq "$(redis_maxmemory 12000)" 2gb rmax12000`,
		`assert_eq "$(redis_mem_limit 12000)" 3g rlim12000`,
		`assert_eq "$(redis_maxmemory 11999)" 512mb rmax11999`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("self-test install.sh: нет %q", want)
		}
	}
	app, err := os.ReadFile("docker-compose.app.yml")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(app), "EMTDS_UNIQ_MAX_SECONDS: ${EMTDS_UNIQ_MAX_SECONDS:-86400}"); n != 2 {
		t.Fatalf("docker-compose.app.yml: потолок окна уникальности должен быть у engine и admin, найдено %d", n)
	}
}

// TestInstallNoPruneOrWipe запрещает в install.sh команды очистки Docker и удаление системных каталогов.
func TestInstallNoPruneOrWipe(t *testing.T) {
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"prune", "docker rm", "docker system", "rm -rf /opt", "rm -rf /etc"} {
		if strings.Contains(string(data), bad) {
			t.Fatalf("install.sh: запрещённая подстрока %q", bad)
		}
	}
}

// TestInstallMainOrder проверяет порядок main: аргументы, root, маркер установки, отказ на старой
// установке, затем режим загрузчика или основной сценарий; в режиме self-test до root дело не доходит.
func TestInstallMainOrder(t *testing.T) {
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	body := shellFunc(t, string(data), "main")
	prev := -1
	for _, step := range []string{
		`ORIG_ARGS=("$@")`,
		`parse_args "$@"`,
		`run_self_test`,
		`is_root ||`,
		`[[ -f "$MARKER" ]]`,
		`find_old_install`,
		`require_valid_repo`,
		`loader_mode_needed "$SELF" "$TDS_DIR"`,
		`run_loader`,
		`run_install`,
	} {
		at := strings.Index(body, step)
		if at < 0 {
			t.Fatalf("main: нет шага %q", step)
		}
		if at < prev {
			t.Fatalf("main: шаг %q стоит раньше предыдущего", step)
		}
		prev = at
	}
	if !strings.Contains(body, "${UPDATE_LIB_SCRIPT} --check") || !strings.Contains(body, "${UP_SCRIPT}") {
		t.Fatal("main: при маркере нужно печатать up.sh и update.sh --check")
	}
}

// TestInstallLoaderOrder проверяет порядок режима загрузчика: --repo, apt, полный клон без --depth,
// выбор версии, подпись только при флаге, атомарный rename, exec с исходными аргументами.
func TestInstallLoaderOrder(t *testing.T) {
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if strings.Contains(s, "--depth") {
		t.Fatal("install.sh: клон должен быть полным (fast-forward проверяется по истории)")
	}
	loader := shellFunc(t, s, "run_loader")
	prev := -1
	for _, step := range []string{
		`[[ "${EMTDS_INSTALL_LOADED:-}" != 1 ]]`,
		`[[ -n "$INSTALL_REPO" ]]`,
		`apt_install git ca-certificates curl`,
		`loader_sweep_stale`,
		`loader_reuse_clone`,
		`loader_clone`,
		`[[ -f "${INSTALL_DIR}/deploy/docker-compose.app.yml" ]]`,
		`EMTDS_INSTALL_LOADED=1 exec bash "${INSTALL_DIR}/deploy/install.sh" "${ORIG_ARGS[@]+"${ORIG_ARGS[@]}"}"`,
	} {
		at := strings.Index(loader, step)
		if at < 0 {
			t.Fatalf("run_loader: нет шага %q", step)
		}
		if at < prev {
			t.Fatalf("run_loader: шаг %q стоит раньше предыдущего", step)
		}
		prev = at
	}
	clone := shellFunc(t, s, "loader_clone")
	prev = -1
	for _, step := range []string{
		`loader_git clone --quiet --`,
		`loader_checkout "$tmp"`,
		`mv -T -- "$tmp" "$INSTALL_DIR"`,
	} {
		at := strings.Index(clone, step)
		if at < 0 {
			t.Fatalf("loader_clone: нет шага %q", step)
		}
		if at < prev {
			t.Fatalf("loader_clone: шаг %q стоит раньше предыдущего", step)
		}
		prev = at
	}
	checkout := shellFunc(t, s, "loader_checkout")
	prev = -1
	for _, step := range []string{
		`loader_pick_ref "$dir" "$INSTALL_CHANNEL"`,
		`checkout --quiet --detach "${ref}^{commit}"`,
		`loader_verify "$dir" "$ref" "$INSTALL_SIGNERS"`,
		`deploy/docker-compose.app.yml`,
	} {
		at := strings.Index(checkout, step)
		if at < 0 {
			t.Fatalf("loader_checkout: нет шага %q", step)
		}
		if at < prev {
			t.Fatalf("loader_checkout: шаг %q стоит раньше предыдущего", step)
		}
		prev = at
	}
	if n := strings.Count(s, "checkout --quiet --detach"); n != 1 {
		t.Fatalf("install.sh: переход на версию должен быть в одной функции, найдено %d", n)
	}
	reuse := shellFunc(t, s, "loader_reuse_clone")
	prev = -1
	for _, step := range []string{`remote get-url origin`, `status --porcelain`, `loader_fetch "$dir"`, `loader_checkout "$dir"`} {
		at := strings.Index(reuse, step)
		if at < 0 {
			t.Fatalf("loader_reuse_clone: нет шага %q", step)
		}
		if at < prev {
			t.Fatalf("loader_reuse_clone: шаг %q стоит раньше предыдущего", step)
		}
		prev = at
	}
	fetch := shellFunc(t, s, "loader_fetch")
	for _, want := range []string{`remote.origin.fetch=+refs/heads/*:refs/remotes/origin/*`, `fetch --quiet --tags --force origin`, "tag -d"} {
		if !strings.Contains(fetch, want) {
			t.Fatalf("loader_fetch: нет %q", want)
		}
	}
	git := shellFunc(t, s, "loader_git")
	for _, want := range []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "core.hooksPath=/dev/null",
		"core.fsmonitor=false", "core.sshCommand=", "gpg.program=gpg", "gpg.ssh.program=ssh-keygen", "protocol.ext.allow=never"} {
		if !strings.Contains(git, want) {
			t.Fatalf("loader_git: нет защитного параметра %q", want)
		}
	}
	verify := shellFunc(t, s, "loader_verify")
	for _, want := range []string{`refs/tags/*`, "verify-tag", "verify-commit HEAD", `"$head" != "$want"`} {
		if !strings.Contains(verify, want) {
			t.Fatalf("loader_verify: нет %q", want)
		}
	}
	pick := shellFunc(t, s, "loader_pick_ref")
	for _, want := range []string{"refs/remotes/origin/main", "sort -V", "нет тегов vX.Y.Z", "merge-base --is-ancestor", "не является предком origin/main"} {
		if !strings.Contains(pick, want) {
			t.Fatalf("loader_pick_ref: нет %q", want)
		}
	}
}

// TestInstallRunOrder проверяет run_install: замок первым, проверка коммита между build-args и сборкой,
// ключи подписи до сборки, nginx до apply-edge, замок отпущен перед агентом, сбой агента — ошибка.
func TestInstallRunOrder(t *testing.T) {
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	body := shellFunc(t, s, "run_install")
	if !strings.HasPrefix(strings.TrimSpace(strings.SplitN(body, "\n", 2)[1]), "acquire_run_lock") {
		t.Fatal("run_install: первым действием должен быть замок acquire_run_lock")
	}
	for _, want := range []string{`exec 9>"$LOCK_FILE"`, "flock -n 9", "/run/emtds-update.lock"} {
		if !strings.Contains(s, want) {
			t.Fatalf("install.sh: нет %q", want)
		}
	}
	prev := -1
	for _, step := range []string{
		`install_allowed_signers`,
		`export_build_args "${INSTALL_CHANNEL}"`,
		`require_build_commit || exit 1`,
		`"${APP_COMPOSE[@]}" build engine`,
		`ensure_nginx "$docker_distro"`,
		`apply-edge.sh" "$public_ip"`,
		`release_run_lock`,
		`update.sh" "${update_args[@]}"`,
		`date -Iseconds | write_atomic "$MARKER" 0644`,
		`watchdog.sh" --install`,
		`print_first_login "$public_ip"`,
	} {
		at := strings.Index(body, step)
		if at < 0 {
			t.Fatalf("run_install: нет шага %q", step)
		}
		if at < prev {
			t.Fatalf("run_install: шаг %q стоит раньше предыдущего", step)
		}
		prev = at
	}
	if !strings.Contains(body, `die "не удалось установить агент обновлений`) || strings.Contains(s, "предупреждение: не удалось установить агент") {
		t.Fatal("run_install: сбой update.sh --install должен быть ошибкой, а не предупреждением")
	}
	if strings.Contains(body, "BASH_SOURCE") || strings.Contains(body, `-f "$MARKER"`) {
		t.Fatal("run_install: проверки запуска и маркера принадлежат main")
	}
	ensure := shellFunc(t, s, "ensure_nginx")
	for _, want := range []string{"command -v nginx", `nginx_version_ok "$have"`, "nginx_foreign_sites", "remove_distro_nginx", `install_nginx_org "$distro" "$codename"`} {
		if !strings.Contains(ensure, want) {
			t.Fatalf("ensure_nginx: нет %q", want)
		}
	}
	if !strings.Contains(shellFunc(t, s, "install_nginx_org"), "apt_install nginx") {
		t.Fatal("install_nginx_org: ожидали apt_install nginx")
	}
	if !strings.Contains(s, "NGINX_MIN_VERSION=1.25.1") {
		t.Fatal("install.sh: минимальная версия nginx должна быть 1.25.1 (http2 on)")
	}
}

// TestInstallNginxOrgFingerprints: в install.sh зашиты ровно три отпечатка ключей nginx.org (40 hex-знаков),
// ключ проверяется до записи в keyring, источник apt — https://nginx.org/packages с signed-by, приоритет 900.
func TestInstallNginxOrgFingerprints(t *testing.T) {
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	m := regexp.MustCompile(`(?m)^NGINX_ORG_FINGERPRINTS="([^"]*)"$`).FindStringSubmatch(s)
	if m == nil {
		t.Fatal("install.sh: нет NGINX_ORG_FINGERPRINTS")
	}
	want := []string{"573BFD6B3D8FBC641079A6ABABF5BD827BD9BF62", "8540A6F18833A80E9C1653A42FD21310B49F6B46", "9E9BE90EACBCDE69FE9B204CBCDCD8A38D88A2B3"}
	if got := strings.Fields(m[1]); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("отпечатки nginx.org %v, ждали %v", got, want)
	}
	body := shellFunc(t, s, "install_nginx_org")
	verify := strings.Index(body, "nginx_key_trusted")
	dearmor := strings.Index(body, "--dearmor")
	if verify < 0 || dearmor < verify {
		t.Fatal("install_nginx_org: ключ должен проверяться до записи в keyring")
	}
	for _, frag := range []string{"https://nginx.org/keys/nginx_signing.key", "https://nginx.org/packages/%s %s nginx", "Pin: origin nginx.org", "Pin-Priority: 900", "systemctl enable --now nginx"} {
		if !strings.Contains(body, frag) {
			t.Fatalf("install_nginx_org: нет %q", frag)
		}
	}
}

// installSandbox — песочница запуска install.sh без root: подменённые корни путей, заглушки команд
// в PATH и журнал их вызовов.
type installSandbox struct {
	bash string
	root string
	bin  string
	log  string
	env  []string
}

// installStubs — заглушки команд, которые install.sh вызывает до и после клона.
var installStubs = map[string]string{
	"apt-get":   "echo \"apt-get $*\" >>\"$STUB_LOG\"\ncase \"$*\" in\n*\"install -y nginx\"*) printf '#!/bin/sh\\necho \"nginx version: nginx/%s\" >&2\\n' \"${STUB_NGINX_NEW:-1.30.6}\" >\"$STUB_BIN/nginx\"; chmod 0755 \"$STUB_BIN/nginx\";;\nesac\nexit \"${STUB_APT_RC:-0}\"\n",
	"gpg": `echo "gpg $*" >>"$STUB_LOG"
case "$*" in
*--show-keys*) printf 'pub:-:4096:1:ABABF5BD827BD9BF62:1:::-:::scSC::::::23::0:\nfpr:::::::::%s:\n' "${STUB_GPG_FPR:-573BFD6B3D8FBC641079A6ABABF5BD827BD9BF62}";;
*--dearmor*)
	while [ $# -gt 0 ]; do [ "$1" = -o ] && out=$2; shift; done
	echo keyring >"$out";;
esac
exit 0
`,
	"dpkg-query": "case \"$1\" in\n-W) printf '%b' \"${STUB_DPKG_PKGS:-}\";;\n*) exit 1;;\nesac\n",
	"nproc":     "echo 4\n",
	"df":        "printf 'Avail\\n 100G\\n'\n",
	"ss":        "exit 0\n",
	"curl":      "exit 0\n",
	"systemctl": "echo \"systemctl $*\" >>\"$STUB_LOG\"\nexit 0\n",
	"docker": `echo "docker $*" >>"$STUB_LOG"
case "$1" in
compose)
	case "$*" in
	*" build "*) exit "${STUB_BUILD_RC:-0}";;
	*"exec -T clickhouse"*) echo 1;;
	*"redis-cli ping"*) echo PONG;;
	esac
	exit 0;;
ps)
	[ "$2" = "-aq" ] && exit 0
	echo cid1;;
container|volume) exit 1;;
network)
	case "$*" in
	*IPAM*) printf 172.18.0.1;;
	*Labels*) echo emtds-infra;;
	esac;;
inspect)
	case "$*" in
	*StartedAt*) echo 2026-01-01T00:00:00Z;;
	*Running*) echo true;;
	esac;;
logs) printf 'clickhouse connected\nredis connected\n';;
esac
exit 0
`,
}

// newInstallSandbox создаёт песочницу: каталоги etc, run, bin с заглушками, meminfo и os-release Debian 12.
// Переменные EMTDS_* хоста в окружение запуска не попадают.
func newInstallSandbox(t *testing.T) *installSandbox {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sb := &installSandbox{bash: requireBash(t), root: root}
	sb.bin = filepath.Join(sb.root, "bin")
	sb.log = filepath.Join(sb.root, "stub.log")
	for _, d := range []string{"bin", "etc", "run"} {
		mustMkdir(t, filepath.Join(sb.root, d))
	}
	for name, script := range installStubs {
		writeStub(t, sb.bin, name, script)
	}
	mustWrite(t, filepath.Join(sb.root, "meminfo"), "MemTotal:       16777216 kB\n", 0o644)
	mustWrite(t, filepath.Join(sb.root, "os-release"), "ID=debian\nVERSION_CODENAME=bookworm\n", 0o644)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "EMTDS_") {
			sb.env = append(sb.env, e)
		}
	}
	sb.env = append(sb.env,
		"PATH="+sb.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_LOG="+sb.log,
		"STUB_BIN="+sb.bin,
		"EMTDS_INSTALL_SANDBOX=1",
		"EMTDS_INSTALL_STATE_DIR="+sb.path("etc"),
		"EMTDS_INSTALL_DIR="+sb.path("opt", "emtds"),
		"EMTDS_INSTALL_LOCK_FILE="+sb.path("run", "emtds-update.lock"),
		"EMTDS_INSTALL_MEMINFO="+sb.path("meminfo"),
		"EMTDS_INSTALL_OS_RELEASE="+sb.path("os-release"),
		"EMTDS_INSTALL_OLD_ETC_ROOT="+sb.path("etc"),
		"EMTDS_INSTALL_APT_ROOT="+sb.path("aptroot"),
		"EMTDS_PUBLIC_IP=203.0.113.9",
	)
	return sb
}

// path возвращает путь внутри песочницы.
func (sb *installSandbox) path(parts ...string) string {
	return filepath.Join(append([]string{sb.root}, parts...)...)
}

// run запускает bash script args; extra дописывается к окружению. Возвращает stdout, stderr и код выхода.
func (sb *installSandbox) run(t *testing.T, script string, extra []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(sb.bash, append([]string{script}, args...)...)
	cmd.Dir = sb.root
	cmd.Env = append(append([]string{}, sb.env...), extra...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	return o.String(), e.String(), code
}

// installGit выполняет git в каталоге dir с фиксированной личностью и без подписи; возвращает вывод без пробелов по краям.
func installGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=Tester", "-c", "user.email=tester@example.com", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// requireGit пропускает тест, если git нет в PATH.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git не найден в PATH")
	}
}

// installStubScript — содержимое deploy/install.sh в репозитории-источнике: печатает свои аргументы.
const installStubScript = "#!/bin/sh\necho \"EXEC-OK $*\"\necho \"LOADED=${EMTDS_INSTALL_LOADED:-}\"\n"

// newOrigin создаёт репозиторий-источник на ветке main с одним коммитом и возвращает его путь.
func (sb *installSandbox) newOrigin(t *testing.T) string {
	t.Helper()
	requireGit(t)
	repo := sb.path("origin")
	mustMkdir(t, repo)
	installGit(t, repo, "init", "-q", "-b", "main")
	mustWrite(t, filepath.Join(repo, "deploy", "install.sh"), installStubScript, 0o755)
	mustWrite(t, filepath.Join(repo, "deploy", "docker-compose.app.yml"), "name: emtds-app\n", 0o644)
	installGit(t, repo, "add", "-A")
	installGit(t, repo, "commit", "-q", "-m", "init")
	return repo
}

// commitMore добавляет в репозиторий коммит с новым файлом и возвращает его sha.
func commitMore(t *testing.T, repo, name string) string {
	t.Helper()
	mustWrite(t, filepath.Join(repo, name), name+"\n", 0o644)
	installGit(t, repo, "add", "-A")
	installGit(t, repo, "commit", "-q", "-m", name)
	return installGit(t, repo, "rev-parse", "HEAD")
}

// loaderScript кладёт копию install.sh вне deploy/ (так он запускается после curl) и возвращает путь.
func (sb *installSandbox) loaderScript(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	path := sb.path("dl", "emtds-install.sh")
	mustWrite(t, path, string(data), 0o644)
	return path
}

// logOf возвращает журнал вызовов заглушек (пустая строка, если вызовов не было).
func (sb *installSandbox) logOf() string {
	return readFile(sb.log)
}

// assertNoClone проверяет, что код не появился и временных клонов не осталось.
func (sb *installSandbox) assertNoClone(t *testing.T) {
	t.Helper()
	entries, _ := os.ReadDir(sb.path("opt"))
	if len(entries) != 0 {
		t.Fatalf("в %s остались %d записей: клона быть не должно", sb.path("opt"), len(entries))
	}
}

// TestInstallLoaderMarkerBeforeClone: при маркере установки загрузчик печатает up.sh и update.sh --check,
// выходит с кодом 0 и не доходит до --repo, apt и клона.
func TestInstallLoaderMarkerBeforeClone(t *testing.T) {
	sb := newInstallSandbox(t)
	mustWrite(t, sb.path("etc", "installed"), "2026-01-01\n", 0o644)
	script := sb.loaderScript(t)
	for _, args := range [][]string{nil, {"--repo", sb.path("origin")}} {
		out, errOut, code := sb.run(t, script, nil, args...)
		if code != 0 {
			t.Fatalf("маркер: код %d\n%s", code, errOut)
		}
		if !strings.Contains(out, sb.path("etc", "up.sh")) || !strings.Contains(out, "update.sh --check") {
			t.Fatalf("маркер: нет up.sh и update.sh --check:\n%s", out)
		}
	}
	if sb.logOf() != "" {
		t.Fatalf("при маркере вызывались команды:\n%s", sb.logOf())
	}
	sb.assertNoClone(t)
}

// TestInstallLoaderRefusals: без --repo, при каталоге старой установки и с неверным каналом загрузчик
// отказывает до apt и клона.
func TestInstallLoaderRefusals(t *testing.T) {
	sb := newInstallSandbox(t)
	script := sb.loaderScript(t)
	_, errOut, code := sb.run(t, script, nil)
	if code != 1 || !strings.Contains(errOut, "--repo") {
		t.Fatalf("без --repo: код %d\n%s", code, errOut)
	}
	_, errOut, code = sb.run(t, script, nil, "--repo", "-oops")
	if code != 1 || !strings.Contains(errOut, "недопустимый адрес") {
		t.Fatalf("--repo с дефисом: код %d\n%s", code, errOut)
	}
	_, errOut, code = sb.run(t, script, nil, "--repo", sb.path("origin"), "--channel", "dev")
	if code != 2 || !strings.Contains(errOut, "release или main") {
		t.Fatalf("неверный канал: код %d\n%s", code, errOut)
	}
	_, errOut, code = sb.run(t, script, nil, "--repo")
	if code != 2 {
		t.Fatalf("--repo без значения: код %d\n%s", code, errOut)
	}
	mustWrite(t, sb.path("etc", "oldtds", "installed"), "2026-01-01\n", 0o644)
	_, errOut, code = sb.run(t, script, nil, "--repo", sb.path("origin"))
	if code != 1 || !strings.Contains(errOut, "прежняя установка") {
		t.Fatalf("старая установка: код %d\n%s", code, errOut)
	}
	if sb.logOf() != "" {
		t.Fatalf("до отказа вызывались команды:\n%s", sb.logOf())
	}
	sb.assertNoClone(t)
}

// TestInstallLoaderRefusesNonRoot: без песочницы не от root загрузчик отказывает до всех шагов.
func TestInstallLoaderRefusesNonRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("тест запускается не от root")
	}
	sb := newInstallSandbox(t)
	_, errOut, code := sb.run(t, sb.loaderScript(t), []string{"EMTDS_INSTALL_SANDBOX="}, "--repo", sb.path("origin"))
	if code != 1 || !strings.Contains(errOut, "root") {
		t.Fatalf("не root: код %d\n%s", code, errOut)
	}
	if sb.logOf() != "" {
		t.Fatalf("до отказа вызывались команды:\n%s", sb.logOf())
	}
}

// TestInstallLoaderMainChannel: канал main — полный клон на detached HEAD origin/main, apt ставит
// git, ca-certificates и curl, клону передаются те же аргументы, временного клона не остаётся.
func TestInstallLoaderMainChannel(t *testing.T) {
	sb := newInstallSandbox(t)
	repo := sb.newOrigin(t)
	commitMore(t, repo, "second.txt")
	tip := commitMore(t, repo, "third.txt")
	out, errOut, code := sb.run(t, sb.loaderScript(t), nil, "--repo", repo, "--channel", "main", "--no-edge")
	if code != 0 {
		t.Fatalf("main: код %d\n%s\n%s", code, out, errOut)
	}
	if want := "EXEC-OK --repo " + repo + " --channel main --no-edge"; !strings.Contains(out, want) {
		t.Fatalf("клон получил другие аргументы, ждали %q:\n%s", want, out)
	}
	clone := sb.path("opt", "emtds")
	if got := installGit(t, clone, "rev-parse", "HEAD"); got != tip {
		t.Fatalf("HEAD клона %s, ждали %s", got, tip)
	}
	if got := installGit(t, clone, "rev-list", "--count", "HEAD"); got != "3" {
		t.Fatalf("история клона неполная: %s коммитов", got)
	}
	if got := installGit(t, clone, "rev-parse", "--is-shallow-repository"); got != "false" {
		t.Fatalf("клон поверхностный: %s", got)
	}
	cmd := exec.Command("git", "-C", clone, "symbolic-ref", "-q", "HEAD")
	if err := cmd.Run(); err == nil {
		t.Fatal("HEAD клона должен быть отсоединён")
	}
	if !strings.Contains(sb.logOf(), "apt-get -o DPkg::Lock::Timeout=300 install -y git ca-certificates curl") {
		t.Fatalf("apt не ставил git, ca-certificates и curl:\n%s", sb.logOf())
	}
	entries, err := os.ReadDir(sb.path("opt"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("в opt должен быть только клон: %v %v", entries, err)
	}
}

// TestInstallLoaderReleaseChannel: канал release выбирает максимальный тег vX.Y.Z по версии, а не по
// строке; теги другого вида игнорируются; при отсутствии тегов — отказ без следов.
func TestInstallLoaderReleaseChannel(t *testing.T) {
	sb := newInstallSandbox(t)
	repo := sb.newOrigin(t)
	script := sb.loaderScript(t)
	_, errOut, code := sb.run(t, script, nil, "--repo", repo, "--channel", "release")
	if code != 1 || !strings.Contains(errOut, "нет тегов vX.Y.Z") {
		t.Fatalf("release без тегов: код %d\n%s", code, errOut)
	}
	sb.assertNoClone(t)

	installGit(t, repo, "tag", "v1.9.9")
	want := commitMore(t, repo, "second.txt")
	installGit(t, repo, "tag", "-a", "-m", "release", "v1.10.0")
	installGit(t, repo, "tag", "v2.0")
	installGit(t, repo, "tag", "v1.11.0-rc1")
	commitMore(t, repo, "third.txt")
	out, errOut, code := sb.run(t, script, nil, "--repo", repo, "--channel", "release")
	if code != 0 || !strings.Contains(out, "EXEC-OK") {
		t.Fatalf("release: код %d\n%s\n%s", code, out, errOut)
	}
	if got := installGit(t, sb.path("opt", "emtds"), "rev-parse", "HEAD"); got != want {
		t.Fatalf("HEAD клона %s, ждали коммит тега v1.10.0 %s", got, want)
	}
}

// TestInstallLoaderExistingDir: готовый чистый клон с тем же origin используется как есть;
// грязное дерево, чужой origin и каталог без клона — отказ.
func TestInstallLoaderExistingDir(t *testing.T) {
	sb := newInstallSandbox(t)
	repo := sb.newOrigin(t)
	script := sb.loaderScript(t)
	args := []string{"--repo", repo, "--channel", "main"}
	if _, errOut, code := sb.run(t, script, nil, args...); code != 0 {
		t.Fatalf("первый запуск: код %d\n%s", code, errOut)
	}
	clone := sb.path("opt", "emtds")
	out, errOut, code := sb.run(t, script, nil, args...)
	if code != 0 || !strings.Contains(out, "EXEC-OK") {
		t.Fatalf("чистый клон не принят: код %d\n%s\n%s", code, out, errOut)
	}
	mustWrite(t, filepath.Join(clone, "local.txt"), "x\n", 0o644)
	if _, errOut, code := sb.run(t, script, nil, args...); code != 1 || !strings.Contains(errOut, "локальные изменения") {
		t.Fatalf("грязное дерево: код %d\n%s", code, errOut)
	}
	if err := os.Remove(filepath.Join(clone, "local.txt")); err != nil {
		t.Fatal(err)
	}
	installGit(t, clone, "remote", "set-url", "origin", sb.path("elsewhere"))
	if _, errOut, code := sb.run(t, script, nil, args...); code != 1 || !strings.Contains(errOut, "origin не совпадает") {
		t.Fatalf("чужой origin: код %d\n%s", code, errOut)
	}
	if err := os.RemoveAll(clone); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(clone, "file"), "x\n", 0o644)
	if _, errOut, code := sb.run(t, script, nil, args...); code != 1 || !strings.Contains(errOut, "не является клоном") {
		t.Fatalf("каталог без клона: код %d\n%s", code, errOut)
	}
}

// signedOrigin создаёт репозиторий-источник, чей единственный коммит подписан ключом SSH (если signed),
// и возвращает путь репозитория и файл allowed_signers. Тест пропускается, если подпись недоступна.
func (sb *installSandbox) signedOrigin(t *testing.T, signed bool) (repo, signers string) {
	t.Helper()
	requireGit(t)
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen не найден в PATH")
	}
	key := sb.path("sign_key")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "tester", "-f", key).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen: %v\n%s", err, out)
	}
	signers = sb.path("allowed_signers.src")
	mustWrite(t, signers, "tester@example.com namespaces=\"git\" "+strings.TrimSpace(readFile(key+".pub"))+"\n", 0o644)
	repo = sb.path("origin")
	mustMkdir(t, repo)
	installGit(t, repo, "init", "-q", "-b", "main")
	mustWrite(t, filepath.Join(repo, "deploy", "install.sh"), installStubScript, 0o755)
	mustWrite(t, filepath.Join(repo, "deploy", "docker-compose.app.yml"), "name: emtds-app\n", 0o644)
	installGit(t, repo, "add", "-A")
	flags := []string{"commit", "-q", "-m", "init", "--no-gpg-sign"}
	if signed {
		flags = []string{"-c", "gpg.format=ssh", "-c", "user.signingkey=" + key, "commit", "-q", "-S", "-m", "init"}
	}
	cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=Tester", "-c", "user.email=tester@example.com"}, flags...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git не умеет подписывать SSH: %v\n%s", err, out)
	}
	return repo, signers
}

// TestInstallLoaderSignature: с --allowed-signers загрузчик пускает подписанный коммит и отказывает
// на неподписанном и при нечитаемом файле; без флага подпись не проверяется. Каталог кода при отказе не появляется.
func TestInstallLoaderSignature(t *testing.T) {
	good := newInstallSandbox(t)
	repo, signers := good.signedOrigin(t, true)
	out, errOut, code := good.run(t, good.loaderScript(t), nil, "--repo", repo, "--channel", "main", "--allowed-signers", signers)
	if code != 0 || !strings.Contains(out, "EXEC-OK") {
		t.Fatalf("подписанный коммит: код %d\n%s\n%s", code, out, errOut)
	}
	if _, err := os.Stat(good.path("etc", "allowed_signers")); err == nil {
		t.Fatal("загрузчик не должен класть allowed_signers: это делает run_install")
	}

	bad := newInstallSandbox(t)
	repo, signers = bad.signedOrigin(t, false)
	script := bad.loaderScript(t)
	_, errOut, code = bad.run(t, script, nil, "--repo", repo, "--channel", "main", "--allowed-signers", signers)
	if code != 1 || !strings.Contains(errOut, "подпись коммита HEAD не прошла проверку") {
		t.Fatalf("неподписанный коммит: код %d\n%s", code, errOut)
	}
	bad.assertNoClone(t)
	_, errOut, code = bad.run(t, script, nil, "--repo", repo, "--channel", "main", "--allowed-signers", bad.path("missing"))
	if code != 1 || !strings.Contains(errOut, "не читается") {
		t.Fatalf("нет файла ключей: код %d\n%s", code, errOut)
	}
	bad.assertNoClone(t)
	if out, errOut, code = bad.run(t, script, nil, "--repo", repo, "--channel", "main"); code != 0 || !strings.Contains(out, "EXEC-OK") {
		t.Fatalf("без флага подпись не проверяется: код %d\n%s\n%s", code, out, errOut)
	}
}

// cloneSandbox раскладывает в песочнице каталог кода как после клона: deploy/install.sh, apply-edge.sh
// (его запуск как программы только пишет в журнал, функции при source остаются настоящими), заглушки
// watchdog.sh и update.sh, compose-файл и git-репозиторий. Если commit — в репозитории есть коммит.
// Возвращает каталог кода и путь к install.sh.
func (sb *installSandbox) cloneSandbox(t *testing.T, commit bool) (tds, script string) {
	t.Helper()
	requireGit(t)
	tds = sb.path("tds")
	for _, name := range []string{"install.sh", "apply-edge.sh"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		body := string(data)
		if name == "apply-edge.sh" {
			guard := "set -Eeuo pipefail\nif [[ \"${BASH_SOURCE[0]}\" == \"$0\" ]]; then echo \"apply-edge $*\" >>\"$STUB_LOG\"; exit 0; fi\n"
			if !strings.Contains(body, "set -Eeuo pipefail\n") {
				t.Fatal("apply-edge.sh: не найден set -Eeuo pipefail")
			}
			body = strings.Replace(body, "set -Eeuo pipefail\n", guard, 1)
		}
		mustWrite(t, filepath.Join(tds, "deploy", name), body, 0o755)
	}
	mustWrite(t, filepath.Join(tds, "deploy", "docker-compose.yml"), "name: emtds-infra\n", 0o644)
	mustWrite(t, filepath.Join(tds, "deploy", "docker-compose.app.yml"), "name: emtds-app\n", 0o644)
	mustWrite(t, filepath.Join(tds, "deploy", "watchdog.sh"), "#!/bin/sh\nif [ -f \"$EMTDS_INSTALL_STATE_DIR/installed\" ]; then m=yes; else m=no; fi\necho \"watchdog $* marker=$m\" >>\"$STUB_LOG\"\nexit \"${STUB_WATCHDOG_RC:-0}\"\n", 0o755)
	mustWrite(t, filepath.Join(tds, "deploy", "update.sh"), "#!/bin/sh\necho \"update $*\" >>\"$STUB_LOG\"\nflock -n \"$EMTDS_INSTALL_LOCK_FILE\" true || { echo 'замок занят' >&2; exit 9; }\nexit \"${STUB_UPDATE_RC:-0}\"\n", 0o755)
	mustWrite(t, filepath.Join(tds, ".gitignore"), ".env\n", 0o644)
	installGit(t, tds, "init", "-q", "-b", "main")
	if commit {
		installGit(t, tds, "add", "-A")
		installGit(t, tds, "commit", "-q", "-m", "init")
	}
	return tds, filepath.Join(tds, "deploy", "install.sh")
}

// stubNginx кладёт в PATH песочницы заглушку nginx подходящей версии: установщик её не трогает.
func (sb *installSandbox) stubNginx(t *testing.T) {
	t.Helper()
	sb.stubNginxVersion(t, "1.30.6")
}

// stubNginxVersion кладёт в PATH песочницы заглушку nginx, которая на -v сообщает версию version.
func (sb *installSandbox) stubNginxVersion(t *testing.T, version string) {
	t.Helper()
	writeStub(t, sb.bin, "nginx", "echo 'nginx version: nginx/"+version+"' >&2\n")
}

// fixtureToken — токен первого входа для фикстур: 32 строчных hex-знака, собранных при выполнении,
// чтобы в исходнике не было литерала, похожего на секрет.
var fixtureToken = strings.Repeat("ab", 16)

// setupToken читает EMTDS_SETUP_TOKEN из .env каталога кода.
func setupToken(t *testing.T, tds string) string {
	t.Helper()
	for _, line := range strings.Split(readFile(filepath.Join(tds, ".env")), "\n") {
		if v, ok := strings.CutPrefix(line, "EMTDS_SETUP_TOKEN="); ok {
			return v
		}
	}
	return ""
}

// requireOpenSSLAndFlock пропускает тест без openssl и flock: install.sh генерирует токен и берёт замок.
func requireOpenSSLAndFlock(t *testing.T) {
	t.Helper()
	requireOpenSSL(t)
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock не найден в PATH")
	}
}

// TestInstallRunFullFlow: полный прогон основного сценария на заглушках. Проверяет порядок шагов,
// аргументы агента (замок к этому моменту свободен), токен первого входа: он в .env с правами 0600,
// печатается на stdout ровно один раз и не попадает ни в один другой файл; без --allowed-signers
// файл ключей не создаётся.
func TestInstallRunFullFlow(t *testing.T) {
	requireOpenSSLAndFlock(t)
	sb := newInstallSandbox(t)
	sb.stubNginx(t)
	tds, script := sb.cloneSandbox(t, true)
	repo := "https://example.com/emtds.git"
	out, errOut, code := sb.run(t, script, nil, "--repo", repo, "--channel", "main")
	if code != 0 {
		t.Fatalf("прогон: код %d\n%s\n%s", code, out, errOut)
	}
	token := setupToken(t, tds)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(token) {
		t.Fatalf("токен первого входа %q не 32 hex-знака", token)
	}
	if info, err := os.Stat(filepath.Join(tds, ".env")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf(".env: права %v, %v", info, err)
	}
	if n := strings.Count(out, token); n != 1 || strings.Contains(errOut, token) {
		t.Fatalf("токен должен печататься на stdout один раз (stdout %d, stderr %v)", n, strings.Contains(errOut, token))
	}
	if !strings.Contains(out, "https://203.0.113.9/") {
		t.Fatalf("нет адреса входа:\n%s", out)
	}
	if _, err := os.Stat(sb.path("etc", "installed")); err != nil {
		t.Fatalf("маркер не создан: %v", err)
	}
	if _, err := os.Stat(sb.path("etc", "allowed_signers")); err == nil {
		t.Fatal("без --allowed-signers файл allowed_signers создаваться не должен")
	}
	log := sb.logOf()
	prev := -1
	for _, step := range []string{
		"build engine",
		"apply-edge 203.0.113.9",
		"update --install --channel main --tds-dir " + tds + " --repo " + repo,
		"watchdog --install marker=yes",
	} {
		at := strings.Index(log, step)
		if at < 0 {
			t.Fatalf("в журнале нет %q:\n%s", step, log)
		}
		if at < prev {
			t.Fatalf("шаг %q выполнен раньше предыдущего:\n%s", step, log)
		}
		prev = at
	}
	if strings.Contains(log, "install -y nginx") {
		t.Fatalf("nginx уже был, apt его ставить не должен:\n%s", log)
	}
	err := filepath.WalkDir(sb.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() && path != filepath.Join(tds, ".env") && strings.Contains(readFile(path), token) {
			t.Errorf("токен первого входа записан в файл %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestInstallRunSignersAndExistingToken: --allowed-signers кладёт файл ключей в каталог состояния с
// правами 0644; заданный в .env токен не меняется, пароль ClickHouse сохраняется.
func TestInstallRunSignersAndExistingToken(t *testing.T) {
	requireOpenSSLAndFlock(t)
	sb := newInstallSandbox(t)
	sb.stubNginx(t)
	tds, script := sb.cloneSandbox(t, true)
	mustWrite(t, filepath.Join(tds, ".env"), "CLICKHOUSE_PASSWORD=keepme\nEMTDS_SETUP_TOKEN="+fixtureToken, 0o600)
	signers := sb.path("keys.src")
	mustWrite(t, signers, "tester@example.com namespaces=\"git\" ssh-ed25519 AAAAexample\n", 0o600)
	out, errOut, code := sb.run(t, script, nil, "--repo", "https://example.com/emtds.git", "--allowed-signers", signers)
	if code != 0 {
		t.Fatalf("прогон: код %d\n%s\n%s", code, out, errOut)
	}
	dst := sb.path("etc", "allowed_signers")
	info, err := os.Stat(dst)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("allowed_signers: %v, %v", info, err)
	}
	if readFile(dst) != readFile(signers) {
		t.Fatal("allowed_signers отличается от переданного файла")
	}
	env := readFile(filepath.Join(tds, ".env"))
	if !strings.Contains(env, "CLICKHOUSE_PASSWORD=keepme\n") || setupToken(t, tds) != fixtureToken {
		t.Fatalf(".env изменён:\n%s", env)
	}
	if !strings.Contains(out, fixtureToken) {
		t.Fatalf("существующий токен не напечатан:\n%s", out)
	}
}

// TestInstallRunNoEdge: --no-edge не ставит nginx и не вызывает apply-edge.sh, вход — через ssh-туннель.
func TestInstallRunNoEdge(t *testing.T) {
	requireOpenSSLAndFlock(t)
	sb := newInstallSandbox(t)
	_, script := sb.cloneSandbox(t, true)
	out, errOut, code := sb.run(t, script, nil, "--repo", "https://example.com/emtds.git", "--no-edge")
	if code != 0 {
		t.Fatalf("прогон: код %d\n%s\n%s", code, out, errOut)
	}
	log := sb.logOf()
	if strings.Contains(log, "apply-edge") || strings.Contains(log, "install -y nginx") {
		t.Fatalf("--no-edge вызвал nginx или apply-edge:\n%s", log)
	}
	if !strings.Contains(out, "ssh -L 8090") || strings.Contains(out, "https://203.0.113.9/") {
		t.Fatalf("при --no-edge вход должен идти через туннель:\n%s", out)
	}
}

// TestInstallRunInstallsNginx: без nginx на Debian 12 ставится пакет nginx из nginx.org: ключ с
// проверенным отпечатком в keyring, источник https://nginx.org/packages/debian bookworm, приоритет 900,
// служба включена. На неподдерживаемой системе — отказ с подсказкой --no-edge. Тест пропускается,
// если nginx есть на хосте.
func TestInstallRunInstallsNginx(t *testing.T) {
	requireOpenSSLAndFlock(t)
	if _, err := exec.LookPath("nginx"); err == nil {
		t.Skip("на хосте установлен nginx")
	}
	sb := newInstallSandbox(t)
	_, script := sb.cloneSandbox(t, true)
	args := []string{"--repo", "https://example.com/emtds.git"}
	if out, errOut, code := sb.run(t, script, nil, args...); code != 0 || !strings.Contains(sb.logOf(), "install -y nginx") {
		t.Fatalf("nginx не установлен: код %d\n%s\n%s\n%s", code, out, errOut, sb.logOf())
	}
	apt := sb.path("aptroot")
	if got := readFile(filepath.Join(apt, "etc", "apt", "sources.list.d", "nginx.list")); got != "deb [signed-by=/usr/share/keyrings/nginx-archive-keyring.gpg] https://nginx.org/packages/debian bookworm nginx\n" {
		t.Fatalf("nginx.list: %q", got)
	}
	if got := readFile(filepath.Join(apt, "etc", "apt", "preferences.d", "99nginx")); !strings.Contains(got, "Pin: origin nginx.org\n") || !strings.Contains(got, "Pin-Priority: 900\n") {
		t.Fatalf("99nginx: %q", got)
	}
	if readFile(filepath.Join(apt, "usr", "share", "keyrings", "nginx-archive-keyring.gpg")) == "" {
		t.Fatal("keyring nginx.org не записан")
	}
	log := sb.logOf()
	if strings.Index(log, "--show-keys") > strings.Index(log, "--dearmor") || !strings.Contains(log, "systemctl enable --now nginx") {
		t.Fatalf("ключ должен проверяться до записи, служба — включаться:\n%s", log)
	}
	if strings.Index(log, "install -y nginx") > strings.Index(log, "apply-edge 203.0.113.9") {
		t.Fatalf("nginx должен ставиться до apply-edge:\n%s", log)
	}

	other := newInstallSandbox(t)
	mustWrite(t, other.path("os-release"), "ID=alpine\nVERSION_CODENAME=\n", 0o644)
	_, script = other.cloneSandbox(t, true)
	_, errOut, code := other.run(t, script, nil, args...)
	if code != 1 || !strings.Contains(errOut, "--no-edge") {
		t.Fatalf("неподдерживаемая система: код %d\n%s", code, errOut)
	}
}

// TestInstallRunRejectsForeignNginxKey: ключ nginx.org с чужим отпечатком останавливает установку до
// записи keyring и apt; маркера нет. Тест пропускается, если nginx есть на хосте.
func TestInstallRunRejectsForeignNginxKey(t *testing.T) {
	requireOpenSSLAndFlock(t)
	if _, err := exec.LookPath("nginx"); err == nil {
		t.Skip("на хосте установлен nginx")
	}
	sb := newInstallSandbox(t)
	_, script := sb.cloneSandbox(t, true)
	_, errOut, code := sb.run(t, script, []string{"STUB_GPG_FPR=0000000000000000000000000000000000000000"}, "--repo", "https://example.com/emtds.git")
	if code != 1 || !strings.Contains(errOut, "отпечатка") {
		t.Fatalf("чужой ключ: код %d\n%s", code, errOut)
	}
	log := sb.logOf()
	if strings.Contains(log, "--dearmor") || strings.Contains(log, "install -y nginx") || strings.Contains(log, "apply-edge") {
		t.Fatalf("после отказа ключ записан или nginx поставлен:\n%s", log)
	}
	if _, err := os.Stat(sb.path("aptroot", "etc", "apt", "sources.list.d", "nginx.list")); err == nil {
		t.Fatal("источник nginx.org записан после отказа")
	}
	if _, err := os.Stat(sb.path("etc", "installed")); err == nil {
		t.Fatal("маркер создан после отказа")
	}
}

// TestInstallRunKeepsModernNginx: nginx 1.25.1 и новее не переустанавливается: ни apt, ни gpg.
func TestInstallRunKeepsModernNginx(t *testing.T) {
	requireOpenSSLAndFlock(t)
	sb := newInstallSandbox(t)
	sb.stubNginxVersion(t, "1.25.1")
	_, script := sb.cloneSandbox(t, true)
	if out, errOut, code := sb.run(t, script, nil, "--repo", "https://example.com/emtds.git"); code != 0 {
		t.Fatalf("прогон: код %d\n%s\n%s", code, out, errOut)
	}
	log := sb.logOf()
	if strings.Contains(log, "install -y nginx") || strings.Contains(log, "purge") || strings.Contains(log, "gpg ") {
		t.Fatalf("подходящий nginx переустанавливался:\n%s", log)
	}
	if !strings.Contains(log, "apply-edge 203.0.113.9") {
		t.Fatalf("apply-edge не вызван:\n%s", log)
	}
}

// TestInstallRunUpgradesOldNginx: nginx старее 1.25.1 без чужих сайтов заменяется пакетом nginx.org:
// копия /etc/nginx в архиве, удаляются только установленные пакеты nginx*, затем ставится nginx.org.
// С чужим сайтом в sites-enabled — отказ с подсказкой --no-edge до удаления пакетов.
func TestInstallRunUpgradesOldNginx(t *testing.T) {
	requireOpenSSLAndFlock(t)
	sb := newInstallSandbox(t)
	sb.stubNginxVersion(t, "1.24.0")
	mustWrite(t, sb.path("aptroot", "etc", "nginx", "nginx.conf"), "events {}\n", 0o644)
	_, script := sb.cloneSandbox(t, true)
	env := []string{`STUB_DPKG_PKGS=nginx installed\nnginx-common installed\nnginx-core installed\nlibnginx-mod-stream config-files\n`}
	out, errOut, code := sb.run(t, script, env, "--repo", "https://example.com/emtds.git")
	if code != 0 {
		t.Fatalf("прогон: код %d\n%s\n%s", code, out, errOut)
	}
	log := sb.logOf()
	purge := strings.Index(log, "purge -y nginx nginx-common nginx-core\n")
	install := strings.Index(log, "install -y nginx\n")
	edge := strings.Index(log, "apply-edge 203.0.113.9")
	if purge < 0 || install < purge || edge < install {
		t.Fatalf("ждали purge старых пакетов, затем установку nginx.org и apply-edge:\n%s", log)
	}
	if strings.Contains(log, "libnginx-mod-stream") {
		t.Fatalf("неустановленный пакет попал в purge:\n%s", log)
	}
	if backups, _ := filepath.Glob(sb.path("etc", "nginx-backup", "etc-nginx-*.tar.gz")); len(backups) != 1 {
		t.Fatalf("нет архива /etc/nginx: %v", backups)
	}

	foreign := newInstallSandbox(t)
	foreign.stubNginxVersion(t, "1.24.0")
	mustWrite(t, foreign.path("aptroot", "etc", "nginx", "sites-enabled", "shop"), "server {}\n", 0o644)
	_, script = foreign.cloneSandbox(t, true)
	_, errOut, code = foreign.run(t, script, env, "--repo", "https://example.com/emtds.git")
	if code != 1 || !strings.Contains(errOut, "чужие сайты") || !strings.Contains(errOut, "shop") || !strings.Contains(errOut, "--no-edge") {
		t.Fatalf("старый nginx с чужим сайтом: код %d\n%s", code, errOut)
	}
	if log := foreign.logOf(); strings.Contains(log, "purge") || strings.Contains(log, "install -y nginx") {
		t.Fatalf("при чужих сайтах nginx не должен меняться:\n%s", log)
	}
}

// TestInstallRunRefusesEmptyCommit: git-дерево без коммита останавливает установку до сборки образа.
func TestInstallRunRefusesEmptyCommit(t *testing.T) {
	requireOpenSSLAndFlock(t)
	sb := newInstallSandbox(t)
	sb.stubNginx(t)
	_, script := sb.cloneSandbox(t, false)
	_, errOut, code := sb.run(t, script, nil, "--repo", "https://example.com/emtds.git")
	if code != 1 || !strings.Contains(errOut, "не удалось определить коммит") {
		t.Fatalf("пустой коммит: код %d\n%s", code, errOut)
	}
	if log := sb.logOf(); strings.Contains(log, " build ") || strings.Contains(log, "update --install") {
		t.Fatalf("после отказа выполнялись сборка или агент:\n%s", log)
	}
	if _, err := os.Stat(sb.path("etc", "installed")); err == nil {
		t.Fatal("маркер создан после отказа")
	}
}

// TestInstallRunUpdateFailureIsFatal: сбой update.sh --install (в том числе ensure_inbox_dir внутри него)
// останавливает установку: код 1, маркера нет.
func TestInstallRunUpdateFailureIsFatal(t *testing.T) {
	requireOpenSSLAndFlock(t)
	sb := newInstallSandbox(t)
	sb.stubNginx(t)
	_, script := sb.cloneSandbox(t, true)
	_, errOut, code := sb.run(t, script, []string{"STUB_UPDATE_RC=1"}, "--repo", "https://example.com/emtds.git")
	if code != 1 || !strings.Contains(errOut, "update.sh --install") {
		t.Fatalf("сбой агента: код %d\n%s", code, errOut)
	}
	if _, err := os.Stat(sb.path("etc", "installed")); err == nil {
		t.Fatal("маркер создан после сбоя агента")
	}
	if strings.Contains(sb.logOf(), "watchdog") {
		t.Fatalf("после сбоя агента сторож включён:\n%s", sb.logOf())
	}
}

// TestInstallRunLockHeld: пока замок /run/emtds-update.lock занят, установка отказывает до любых команд.
func TestInstallRunLockHeld(t *testing.T) {
	requireOpenSSLAndFlock(t)
	sb := newInstallSandbox(t)
	_, script := sb.cloneSandbox(t, true)
	lock := sb.path("run", "emtds-update.lock")
	holder := exec.Command("flock", "-x", lock, "sleep", "60")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = holder.Process.Kill()
		_ = holder.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for exec.Command("flock", "-n", lock, "true").Run() == nil {
		if time.Now().After(deadline) {
			t.Fatal("замок не занят держателем")
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, errOut, code := sb.run(t, script, nil, "--repo", "https://example.com/emtds.git")
	if code != 1 || !strings.Contains(errOut, "уже идёт установка") {
		t.Fatalf("занятый замок: код %d\n%s", code, errOut)
	}
	if sb.logOf() != "" {
		t.Fatalf("при занятом замке вызывались команды:\n%s", sb.logOf())
	}
}

// TestInstallRepoValidation: адрес --repo проверяется до apt и до печати адреса. Учётные данные в URL,
// http://, git://, file://, ftp://, пробелы и неполные адреса — отказ с кодом 1; сам адрес в вывод не
// попадает. То же правило действует в режиме клона и для EMTDS_INSTALL_REPO.
func TestInstallRepoValidation(t *testing.T) {
	sb := newInstallSandbox(t)
	script := sb.loaderScript(t)
	bad := []string{
		"https://user:secret@example.com/o/r.git",
		"https://token@example.com/o/r.git",
		"http://example.com/o/r.git",
		"git://example.com/o/r.git",
		"file://" + sb.path("origin"),
		"ftp://example.com/o/r.git",
		"git@example.com:owner/repo",
		"https://example.com",
		"https://example.com/o/r .git",
	}
	for _, repo := range bad {
		out, errOut, code := sb.run(t, script, nil, "--repo", repo)
		if code != 1 || !strings.Contains(errOut, "--repo: недопустимый адрес") {
			t.Fatalf("--repo %q: код %d\n%s", repo, code, errOut)
		}
		if strings.Contains(out+errOut, repo) || strings.Contains(out+errOut, "secret") {
			t.Fatalf("--repo %q: адрес попал в вывод:\n%s%s", repo, out, errOut)
		}
	}
	_, errOut, code := sb.run(t, script, []string{"EMTDS_INSTALL_REPO=https://user:secret@example.com/o/r.git"})
	if code != 1 || !strings.Contains(errOut, "недопустимый адрес") || strings.Contains(errOut, "secret") {
		t.Fatalf("EMTDS_INSTALL_REPO с учётными данными: код %d\n%s", code, errOut)
	}
	if sb.logOf() != "" {
		t.Fatalf("до отказа вызывались команды:\n%s", sb.logOf())
	}
	sb.assertNoClone(t)

	run := newInstallSandbox(t)
	_, cloneScript := run.cloneSandbox(t, true)
	_, errOut, code = run.run(t, cloneScript, nil, "--repo", "https://user:secret@example.com/o/r.git")
	if code != 1 || !strings.Contains(errOut, "недопустимый адрес") || strings.Contains(errOut, "secret") {
		t.Fatalf("режим клона: код %d\n%s", code, errOut)
	}
	if run.logOf() != "" {
		t.Fatalf("режим клона: до отказа вызывались команды:\n%s", run.logOf())
	}
}

// TestInstallLoaderReuseUpdates: существующий чистый клон перед запуском обновляется и переводится на
// версию канала так же, как свежий: main берёт новую верхушку, release — максимальный тег, а тег, снятый
// в origin, из кандидатов выпадает.
func TestInstallLoaderReuseUpdates(t *testing.T) {
	sb := newInstallSandbox(t)
	repo := sb.newOrigin(t)
	script := sb.loaderScript(t)
	clone := sb.path("opt", "emtds")
	args := []string{"--repo", repo, "--channel", "main"}
	if _, errOut, code := sb.run(t, script, nil, args...); code != 0 {
		t.Fatalf("первый запуск: код %d\n%s", code, errOut)
	}
	tip := commitMore(t, repo, "second.txt")
	out, errOut, code := sb.run(t, script, nil, args...)
	if code != 0 || !strings.Contains(out, "EXEC-OK") {
		t.Fatalf("повторный запуск: код %d\n%s\n%s", code, out, errOut)
	}
	if got := installGit(t, clone, "rev-parse", "HEAD"); got != tip {
		t.Fatalf("существующий клон не обновлён: HEAD %s, ждали %s", got, tip)
	}

	rel := newInstallSandbox(t)
	repo = rel.newOrigin(t)
	first := installGit(t, repo, "rev-parse", "HEAD")
	installGit(t, repo, "tag", "v1.0.0")
	script = rel.loaderScript(t)
	args = []string{"--repo", repo, "--channel", "release"}
	if _, errOut, code := rel.run(t, script, nil, args...); code != 0 {
		t.Fatalf("release, первый запуск: код %d\n%s", code, errOut)
	}
	second := commitMore(t, repo, "second.txt")
	installGit(t, repo, "tag", "v1.1.0")
	if _, errOut, code := rel.run(t, script, nil, args...); code != 0 {
		t.Fatalf("release, второй запуск: код %d\n%s", code, errOut)
	}
	clone = rel.path("opt", "emtds")
	if got := installGit(t, clone, "rev-parse", "HEAD"); got != second {
		t.Fatalf("release: HEAD %s, ждали коммит v1.1.0 %s", got, second)
	}
	installGit(t, repo, "tag", "-d", "v1.1.0")
	if _, errOut, code := rel.run(t, script, nil, args...); code != 0 {
		t.Fatalf("release, тег снят: код %d\n%s", code, errOut)
	}
	if got := installGit(t, clone, "rev-parse", "HEAD"); got != first {
		t.Fatalf("снятый тег остался кандидатом: HEAD %s, ждали коммит v1.0.0 %s", got, first)
	}
}

// TestInstallLoaderReuseSignature: подпись проверяется у того, что вытащено в существующий клон: новый
// неподписанный коммит origin при --allowed-signers отклоняется, и управление не передаётся.
func TestInstallLoaderReuseSignature(t *testing.T) {
	sb := newInstallSandbox(t)
	repo, signers := sb.signedOrigin(t, true)
	script := sb.loaderScript(t)
	args := []string{"--repo", repo, "--channel", "main", "--allowed-signers", signers}
	if out, errOut, code := sb.run(t, script, nil, args...); code != 0 || !strings.Contains(out, "EXEC-OK") {
		t.Fatalf("подписанный коммит: код %d\n%s\n%s", code, out, errOut)
	}
	commitMore(t, repo, "second.txt")
	for i := 0; i < 2; i++ {
		out, errOut, code := sb.run(t, script, nil, args...)
		if code != 1 || !strings.Contains(errOut, "подпись коммита HEAD не прошла проверку") || strings.Contains(out, "EXEC-OK") {
			t.Fatalf("неподписанный коммит в существующем клоне (прогон %d): код %d\n%s\n%s", i+1, code, out, errOut)
		}
	}
}

// signedTag ставит на HEAD репозитория repo аннотированный тег name; при signed он подписан ключом SSH
// песочницы. Тест пропускается, если git не умеет подписывать.
func (sb *installSandbox) signedTag(t *testing.T, repo, name string, signed bool) {
	t.Helper()
	args := []string{"-C", repo, "-c", "user.name=Tester", "-c", "user.email=tester@example.com"}
	if signed {
		args = append(args, "-c", "gpg.format=ssh", "-c", "user.signingkey="+sb.path("sign_key"), "tag", "-s", "-m", "rel", name)
	} else {
		args = append(args, "-c", "tag.gpgsign=false", "tag", "-a", "-m", "rel", name)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git не умеет ставить подписанный тег: %v\n%s", err, out)
	}
}

// TestInstallLoaderSignedRelease: в канале release проверяется подпись максимального тега и HEAD равен
// его коммиту: подписанный тег проходит, неподписанный более новый тег отклоняется и в свежем клоне,
// и в существующем.
func TestInstallLoaderSignedRelease(t *testing.T) {
	sb := newInstallSandbox(t)
	repo, signers := sb.signedOrigin(t, true)
	sb.signedTag(t, repo, "v1.0.0", true)
	want := installGit(t, repo, "rev-parse", "HEAD")
	script := sb.loaderScript(t)
	args := []string{"--repo", repo, "--channel", "release", "--allowed-signers", signers}
	out, errOut, code := sb.run(t, script, nil, args...)
	if code != 0 || !strings.Contains(out, "EXEC-OK") {
		t.Fatalf("подписанный тег: код %d\n%s\n%s", code, out, errOut)
	}
	if got := installGit(t, sb.path("opt", "emtds"), "rev-parse", "HEAD"); got != want {
		t.Fatalf("HEAD клона %s, ждали коммит тега %s", got, want)
	}
	commitMore(t, repo, "second.txt")
	sb.signedTag(t, repo, "v1.1.0", false)
	_, errOut, code = sb.run(t, script, nil, args...)
	if code != 1 || !strings.Contains(errOut, "подпись тега v1.1.0 не прошла проверку") {
		t.Fatalf("неподписанный тег в существующем клоне: код %d\n%s", code, errOut)
	}

	fresh := newInstallSandbox(t)
	repo, signers = fresh.signedOrigin(t, true)
	fresh.signedTag(t, repo, "v1.0.0", false)
	_, errOut, code = fresh.run(t, fresh.loaderScript(t), nil, "--repo", repo, "--channel", "release", "--allowed-signers", signers)
	if code != 1 || !strings.Contains(errOut, "подпись тега v1.0.0 не прошла проверку") {
		t.Fatalf("неподписанный тег в свежем клоне: код %d\n%s", code, errOut)
	}
	fresh.assertNoClone(t)
}

// TestInstallLoaderReleaseNotOnMain: максимальный тег вне истории origin/main отклоняется в канале release,
// клона не остаётся.
func TestInstallLoaderReleaseNotOnMain(t *testing.T) {
	sb := newInstallSandbox(t)
	repo := sb.newOrigin(t)
	installGit(t, repo, "checkout", "-q", "-b", "side")
	commitMore(t, repo, "side.txt")
	installGit(t, repo, "tag", "v1.0.0")
	installGit(t, repo, "checkout", "-q", "main")
	_, errOut, code := sb.run(t, sb.loaderScript(t), nil, "--repo", repo, "--channel", "release")
	if code != 1 || !strings.Contains(errOut, "не является предком origin/main") {
		t.Fatalf("тег вне main: код %d\n%s", code, errOut)
	}
	sb.assertNoClone(t)
}

// TestInstallLoaderLoopGuard: загрузчик передаёт клону EMTDS_INSTALL_LOADED=1, а повторный вход в режим
// загрузчика с этим признаком — отказ до apt и клона, а не бесконечный exec.
func TestInstallLoaderLoopGuard(t *testing.T) {
	sb := newInstallSandbox(t)
	repo := sb.newOrigin(t)
	script := sb.loaderScript(t)
	_, errOut, code := sb.run(t, script, []string{"EMTDS_INSTALL_LOADED=1"}, "--repo", repo, "--channel", "main")
	if code != 1 || !strings.Contains(errOut, "повторно") {
		t.Fatalf("повторный вход: код %d\n%s", code, errOut)
	}
	if sb.logOf() != "" {
		t.Fatalf("при повторном входе вызывались команды:\n%s", sb.logOf())
	}
	sb.assertNoClone(t)
	out, errOut, code := sb.run(t, script, nil, "--repo", repo, "--channel", "main")
	if code != 0 || !strings.Contains(out, "LOADED=1") {
		t.Fatalf("клон должен получить EMTDS_INSTALL_LOADED=1: код %d\n%s\n%s", code, out, errOut)
	}
}

// TestInstallLoaderRequiresComposeFile: клон без deploy/docker-compose.app.yml не получает управление:
// установщик клона снова ушёл бы в режим загрузчика. Отказ до exec, временного клона не остаётся.
func TestInstallLoaderRequiresComposeFile(t *testing.T) {
	sb := newInstallSandbox(t)
	repo := sb.newOrigin(t)
	installGit(t, repo, "rm", "-q", "deploy/docker-compose.app.yml")
	installGit(t, repo, "commit", "-q", "-m", "no compose")
	out, errOut, code := sb.run(t, sb.loaderScript(t), nil, "--repo", repo, "--channel", "main")
	if code != 1 || !strings.Contains(errOut, "docker-compose.app.yml") || strings.Contains(out, "EXEC-OK") {
		t.Fatalf("клон без compose-файла: код %d\n%s\n%s", code, out, errOut)
	}
	sb.assertNoClone(t)
}

// TestInstallLoaderIgnoresForeignApplyEdge: загрузчик вне дерева EmTDS не исполняет apply-edge.sh из
// соседнего каталога deploy/: ни при запуске из .../dl/ (каталог кода выше), ни из deploy/ без compose-файла.
func TestInstallLoaderIgnoresForeignApplyEdge(t *testing.T) {
	sb := newInstallSandbox(t)
	repo := sb.newOrigin(t)
	pwned := sb.path("pwned")
	mustWrite(t, sb.path("deploy", "apply-edge.sh"), "touch "+pwned+"\n", 0o755)
	if out, errOut, code := sb.run(t, sb.loaderScript(t), nil, "--repo", repo, "--channel", "main"); code != 0 {
		t.Fatalf("загрузчик из dl/: код %d\n%s\n%s", code, out, errOut)
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("загрузчик исполнил чужой deploy/apply-edge.sh (запуск из dl/)")
	}

	other := newInstallSandbox(t)
	repo = other.newOrigin(t)
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, other.path("x", "deploy", "install.sh"), string(data), 0o755)
	mustWrite(t, other.path("x", "deploy", "apply-edge.sh"), "touch "+other.path("pwned")+"\n", 0o755)
	if out, errOut, code := other.run(t, other.path("x", "deploy", "install.sh"), nil, "--repo", repo, "--channel", "main"); code != 0 {
		t.Fatalf("загрузчик из deploy/ без compose: код %d\n%s\n%s", code, out, errOut)
	}
	if _, err := os.Stat(other.path("pwned")); err == nil {
		t.Fatal("загрузчик исполнил apply-edge.sh из deploy/ без compose-файла")
	}
}

// TestInstallLoaderSweepsStaleClones: при старте загрузчик удаляет только давние остатки своих временных
// клонов (<INSTALL_DIR>.clone.XXXXXX с .git внутри, не ссылка); свежие, без .git, с другим именем и
// ссылки остаются.
func TestInstallLoaderSweepsStaleClones(t *testing.T) {
	sb := newInstallSandbox(t)
	repo := sb.newOrigin(t)
	opt := sb.path("opt")
	stale := filepath.Join(opt, "emtds.clone.AbCd12")
	keep := []string{
		filepath.Join(opt, "emtds.clone.Fresh1"),
		filepath.Join(opt, "emtds.clone.NoGit1"),
		filepath.Join(opt, "emtds.clone.abc"),
		filepath.Join(opt, "other.clone.AbCd12"),
	}
	for _, dir := range append([]string{stale}, keep...) {
		mustMkdir(t, dir)
		if !strings.HasSuffix(dir, "NoGit1") {
			mustMkdir(t, filepath.Join(dir, ".git"))
		}
	}
	target := sb.path("elsewhere")
	mustMkdir(t, filepath.Join(target, ".git"))
	link := filepath.Join(opt, "emtds.clone.Link12")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("символические ссылки недоступны: %v", err)
	}
	old := time.Now().Add(-3 * time.Hour)
	for _, dir := range append([]string{stale, target}, keep...) {
		if strings.HasSuffix(dir, "Fresh1") {
			continue
		}
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if out, errOut, code := sb.run(t, sb.loaderScript(t), nil, "--repo", repo, "--channel", "main"); code != 0 {
		t.Fatalf("загрузчик: код %d\n%s\n%s", code, out, errOut)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Fatal("давний временный клон не удалён")
	}
	for _, path := range append(keep, link, target) {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("загрузчик удалил то, что трогать нельзя: %s", path)
		}
	}
	if _, err := os.Stat(sb.path("opt", "emtds", "deploy", "install.sh")); err != nil {
		t.Fatalf("клон не создан: %v", err)
	}
}

// TestInstallLoaderCleansTmpOnFailure: при сбое клона временный каталог удаляется ловушкой EXIT.
func TestInstallLoaderCleansTmpOnFailure(t *testing.T) {
	sb := newInstallSandbox(t)
	_, errOut, code := sb.run(t, sb.loaderScript(t), nil, "--repo", sb.path("missing-origin"), "--channel", "main")
	if code != 1 || !strings.Contains(errOut, "не удалось клонировать") {
		t.Fatalf("клон несуществующего репозитория: код %d\n%s", code, errOut)
	}
	sb.assertNoClone(t)
}

// TestInstallRunReplacesBadSetupToken: токен первого входа, который не 32 строчных hex-знака (пустой, с
// пробелом, мусор, верхний регистр), заменяется новым; пароль ClickHouse остаётся, токен печатается.
func TestInstallRunReplacesBadSetupToken(t *testing.T) {
	requireOpenSSLAndFlock(t)
	good := fixtureToken
	for _, bad := range []string{"", " " + good, good + " ", "garbage", strings.ToUpper(good)} {
		sb := newInstallSandbox(t)
		sb.stubNginx(t)
		tds, script := sb.cloneSandbox(t, true)
		mustWrite(t, filepath.Join(tds, ".env"), "CLICKHOUSE_PASSWORD=keepme\nEMTDS_SETUP_TOKEN="+bad+"\n", 0o600)
		out, errOut, code := sb.run(t, script, nil, "--repo", "https://example.com/emtds.git")
		if code != 0 {
			t.Fatalf("токен %q: код %d\n%s\n%s", bad, code, out, errOut)
		}
		token := setupToken(t, tds)
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(token) || token == strings.TrimSpace(bad) {
			t.Fatalf("токен %q не заменён на новый: %q", bad, token)
		}
		if !strings.Contains(out, token) || !strings.Contains(readFile(filepath.Join(tds, ".env")), "CLICKHOUSE_PASSWORD=keepme\n") {
			t.Fatalf("токен %q: вывод или .env неверны:\n%s\n%s", bad, out, readFile(filepath.Join(tds, ".env")))
		}
	}
}

// TestInstallRunWatchdogAfterMarker: сбой включения сторожа не отменяет установку: маркер записан, токен
// первого входа напечатан, код выхода 1 и подсказка включить сторож вручную; сторож шёл после агента.
func TestInstallRunWatchdogAfterMarker(t *testing.T) {
	requireOpenSSLAndFlock(t)
	sb := newInstallSandbox(t)
	sb.stubNginx(t)
	tds, script := sb.cloneSandbox(t, true)
	out, errOut, code := sb.run(t, script, []string{"STUB_WATCHDOG_RC=1"}, "--repo", "https://example.com/emtds.git")
	if code != 1 || !strings.Contains(errOut, "watchdog.sh --install") {
		t.Fatalf("сбой сторожа: код %d\n%s", code, errOut)
	}
	if _, err := os.Stat(sb.path("etc", "installed")); err != nil {
		t.Fatalf("маркер не записан при сбое сторожа: %v", err)
	}
	if token := setupToken(t, tds); token == "" || !strings.Contains(out, token) {
		t.Fatalf("токен первого входа не напечатан:\n%s", out)
	}
	log := sb.logOf()
	if strings.Index(log, "update --install") > strings.Index(log, "watchdog --install marker=yes") || !strings.Contains(log, "watchdog --install marker=yes") {
		t.Fatalf("сторож должен включаться после агента и маркера:\n%s", log)
	}
}
