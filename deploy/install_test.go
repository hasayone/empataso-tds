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
	cmd.Env = append(os.Environ(), "KUZTDS_INSTALL_STATE_DIR="+t.TempDir())
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
		"${KUZTDS_CH_HTTP_PUBLISH:-8123:8123}",
		"${KUZTDS_CH_NATIVE_PUBLISH:-9000:9000}",
		"${KUZTDS_REDIS_PUBLISH:-6379:6379}",
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
	if !strings.Contains(as, "${KUZTDS_ADMIN_PUBLISH:-8090:8090}") {
		t.Fatal("docker-compose.app.yml: нет KUZTDS_ADMIN_PUBLISH")
	}
	if !strings.Contains(as, "${KUZTDS_ENGINE_PUBLISH:-8080:8080}") {
		t.Fatal("docker-compose.app.yml: порт engine должен публиковаться через KUZTDS_ENGINE_PUBLISH с умолчанием 8080:8080")
	}
	if !strings.Contains(as, "KUZTDS_TRUSTED_PROXIES: ${KUZTDS_TRUSTED_PROXIES:-}") {
		t.Fatal("docker-compose.app.yml: нет KUZTDS_TRUSTED_PROXIES у engine")
	}
	if !strings.Contains(as, "KUZTDS_PUBLIC_IP: ${KUZTDS_PUBLIC_IP:-}") {
		t.Fatal("docker-compose.app.yml: нет KUZTDS_PUBLIC_IP у admin")
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
