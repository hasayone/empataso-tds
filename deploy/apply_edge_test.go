package deploy

import (
	"bufio"
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestCloudflareIPsValidCIDR проверяет, что cloudflare-ips.txt содержит только валидные CIDR.
func TestCloudflareIPsValidCIDR(t *testing.T) {
	path := filepath.Join("nginx", "cloudflare-ips.txt")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.ContainsAny(line, " \t") {
			t.Fatalf("строка %d: лишние пробелы в %q", lineNo, line)
		}
		ip, n, err := net.ParseCIDR(line)
		if err != nil {
			t.Fatalf("строка %d: невалидный CIDR %q: %v", lineNo, line, err)
		}
		if ip.String() != strings.Split(line, "/")[0] {
			// ParseCIDR нормализует адрес; для списка CF достаточно успешного parse.
			_ = n
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestNginxEngineTemplate проверяет шаблон default_server для движка.
func TestNginxEngineTemplate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("nginx", "kuztds-engine.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, frag := range []string{
		"default_server",
		"proxy_pass http://__ENGINE_UPSTREAM__",
		"proxy_set_header X-Forwarded-For $remote_addr",
		"real_ip_header CF-Connecting-IP",
		"server_name _",
	} {
		if !strings.Contains(s, frag) {
			t.Fatalf("kuztds-engine.conf: нет %q", frag)
		}
	}
	if strings.Contains(s, "$proxy_add_x_forwarded_for") {
		t.Fatal("kuztds-engine.conf: не должно быть $proxy_add_x_forwarded_for")
	}
}

// TestNginxAdminTemplate проверяет vhost админки по публичному IP.
func TestNginxAdminTemplate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("nginx", "kuztds.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "server_name __PUBLIC_IP__") {
		t.Fatal("kuztds.conf: ожидали server_name __PUBLIC_IP__")
	}
	if strings.Contains(s, "default_server") {
		t.Fatal("kuztds.conf: не должно быть default_server")
	}
}

// TestApplyEdgeScriptGuards проверяет apply-edge.sh на nginx -t, backup и отсутствие правок :80.
func TestApplyEdgeScriptGuards(t *testing.T) {
	data, err := os.ReadFile("apply-edge.sh")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("\r")) {
		t.Fatal("apply-edge.sh содержит CRLF")
	}
	s := string(data)
	for _, want := range []string{
		"nginx -t",
		"nginx-backup",
		"restore_from_backup",
		"--check",
		`if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then`,
		"EDGE_NGINX_DIR_EXISTED",
		"EDGE_BACKUP_DIR=$backup_dir",
		"edge_msg",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("apply-edge.sh: нет %q", want)
		}
	}
	if strings.Contains(s, "conf.d/*.conf") {
		t.Fatal("apply-edge.sh: не должен массово трогать conf.d")
	}
	if matched, _ := regexp.MatchString(`listen\s+80\b`, s); matched {
		t.Fatal("apply-edge.sh: не должен трогать порт 80")
	}
}

// TestApplyEdgeCheckMode рендерит конфиги без записи на диск (--check).
func TestApplyEdgeCheckMode(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	if out, err := exec.Command(bash, "-n", "apply-edge.sh").CombinedOutput(); err != nil {
		t.Fatalf("bash -n apply-edge.sh: %v\n%s", err, out)
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmpEnv := filepath.Join(t.TempDir(), "server.env")
	content := "KUZTDS_PUBLIC_IP=203.0.113.55\nKUZTDS_TRUSTED_PROXIES=172.18.0.1/32\nKUZTDS_ENGINE_PUBLISH=127.0.0.1:18080:8080\n"
	if err := os.WriteFile(tmpEnv, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, filepath.Join(dir, "apply-edge.sh"), "--check")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"KUZTDS_SERVER_ENV="+tmpEnv,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--check: %v\n%s", err, out)
	}
	outS := string(out)
	if !strings.Contains(outS, "203.0.113.55") {
		t.Fatalf("--check: нет подставленного IP в выводе:\n%s", out)
	}
	if !strings.Contains(outS, "proxy_pass http://127.0.0.1:18080") {
		t.Fatalf("--check: engine upstream не отрендерен:\n%s", out)
	}
	if strings.Contains(outS, "__PUBLIC_IP__") || strings.Contains(outS, "__ENGINE_UPSTREAM__") {
		t.Fatalf("--check: остались плейсхолдеры:\n%s", out)
	}
}

// TestApplyEdgeSourceSafeGlobals проверяет, что apply-edge.sh не затирает имена install.sh при source.
func TestApplyEdgeSourceSafeGlobals(t *testing.T) {
	data, err := os.ReadFile("apply-edge.sh")
	if err != nil {
		t.Fatal(err)
	}
	topAssign := regexp.MustCompile(`^[A-Z_][A-Z0-9_]*=`)
	funcMsg := regexp.MustCompile(`^msg\(\)`)
	for i, line := range strings.Split(string(data), "\n") {
		if topAssign.MatchString(line) {
			name := strings.SplitN(line, "=", 2)[0]
			if !strings.HasPrefix(name, "EDGE_") {
				t.Fatalf("строка %d: top-level %s без префикса EDGE_", i+1, name)
			}
		}
		if funcMsg.MatchString(strings.TrimSpace(line)) {
			t.Fatalf("строка %d: apply-edge.sh не должен определять msg()", i+1)
		}
	}
}

// TestComposeEdgeEnv проверяет проброс KUZTDS_PUBLIC_IP и KUZTDS_TRUSTED_PROXIES в compose.
func TestComposeEdgeEnv(t *testing.T) {
	data, err := os.ReadFile("docker-compose.app.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "KUZTDS_TRUSTED_PROXIES: ${KUZTDS_TRUSTED_PROXIES:-}") {
		t.Fatal("docker-compose.app.yml: нет KUZTDS_TRUSTED_PROXIES у engine")
	}
	if !strings.Contains(s, "KUZTDS_PUBLIC_IP: ${KUZTDS_PUBLIC_IP:-}") {
		t.Fatal("docker-compose.app.yml: нет KUZTDS_PUBLIC_IP у admin")
	}
}
