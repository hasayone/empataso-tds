package deploy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
		"${EMTDS_CH_HTTP_PUBLISH:-${KUZTDS_CH_HTTP_PUBLISH:-127.0.0.1:8123:8123}}",
		"${EMTDS_CH_NATIVE_PUBLISH:-${KUZTDS_CH_NATIVE_PUBLISH:-127.0.0.1:9000:9000}}",
		"${EMTDS_REDIS_PUBLISH:-${KUZTDS_REDIS_PUBLISH:-127.0.0.1:6379:6379}}",
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
	if !strings.Contains(as, "${EMTDS_ADMIN_PUBLISH:-${KUZTDS_ADMIN_PUBLISH:-127.0.0.1:8090:8090}}") {
		t.Fatal("docker-compose.app.yml: нет EMTDS_ADMIN_PUBLISH с loopback-умолчанием")
	}
	if !strings.Contains(as, "${EMTDS_ENGINE_PUBLISH:-${KUZTDS_ENGINE_PUBLISH:-127.0.0.1:8080:8080}}") {
		t.Fatal("docker-compose.app.yml: порт engine должен публиковаться через EMTDS_ENGINE_PUBLISH с умолчанием 127.0.0.1:8080:8080")
	}
	if !strings.Contains(as, "EMTDS_TRUSTED_PROXIES: ${EMTDS_TRUSTED_PROXIES:-${KUZTDS_TRUSTED_PROXIES:-}}") {
		t.Fatal("docker-compose.app.yml: нет EMTDS_TRUSTED_PROXIES у engine")
	}
	if !strings.Contains(as, "EMTDS_PUBLIC_IP: ${EMTDS_PUBLIC_IP:-${KUZTDS_PUBLIC_IP:-}}") {
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
	if strings.Index(s, `update.sh" "${update_args[@]}"`) < strings.Index(s, `watchdog.sh" --install`) {
		t.Fatal("install.sh: update.sh --install должен идти после watchdog.sh --install")
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
