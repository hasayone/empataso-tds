package deploy

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
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
	data, err := os.ReadFile(filepath.Join("nginx", "emtds-engine.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, frag := range []string{
		"default_server",
		"upstream emtds_engine {",
		"server __ENGINE_UPSTREAM__;",
		"keepalive 32;",
		"proxy_pass http://emtds_engine;",
		`proxy_set_header Connection "";`,
		"proxy_set_header X-Forwarded-For $remote_addr",
		"proxy_set_header CF-Connecting-IP $emtds_cf_ip",
		"proxy_set_header CF-IPCountry $emtds_cf_country",
		"real_ip_header CF-Connecting-IP",
		"server_name _",
	} {
		if !strings.Contains(s, frag) {
			t.Fatalf("emtds-engine.conf: нет %q", frag)
		}
	}
	if strings.Contains(s, "$proxy_add_x_forwarded_for") {
		t.Fatal("emtds-engine.conf: не должно быть $proxy_add_x_forwarded_for")
	}
	if !strings.Contains(s, "location ^~ /internal/") || !strings.Contains(s, "return 404") {
		t.Fatal("emtds-engine.conf: ожидали location ^~ /internal/ { return 404; }")
	}
	internalIdx := strings.Index(s, "location ^~ /internal/")
	rootLocIdx := strings.Index(s, "\n    location / {")
	if internalIdx < 0 || rootLocIdx < 0 || internalIdx > rootLocIdx {
		t.Fatal("emtds-engine.conf: /internal/ должен быть до location /")
	}
}

// TestNginxAdminTemplate проверяет vhost админки по публичному IP.
func TestNginxAdminTemplate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("nginx", "emtds.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "server_name __PUBLIC_IP__") {
		t.Fatal("emtds.conf: ожидали server_name __PUBLIC_IP__")
	}
	if strings.Contains(s, "default_server") {
		t.Fatal("emtds.conf: не должно быть default_server")
	}
	for _, frag := range []string{"upstream emtds_admin {", "server 127.0.0.1:8090;", "keepalive 16;", "proxy_pass http://emtds_admin;", `proxy_set_header Connection "";`} {
		if !strings.Contains(s, frag) {
			t.Fatalf("emtds.conf: нет %q", frag)
		}
	}
	if !strings.Contains(s, "proxy_set_header X-Forwarded-For $remote_addr") {
		t.Fatal("emtds.conf: X-Forwarded-For должен быть $remote_addr")
	}
	if !strings.Contains(s, `proxy_set_header CF-Connecting-IP ""`) {
		t.Fatal("emtds.conf: CF-Connecting-IP должен очищаться")
	}
	if strings.Contains(s, "$proxy_add_x_forwarded_for") {
		t.Fatal("emtds.conf: не должно быть $proxy_add_x_forwarded_for")
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	pbAt := strings.Index(s, "location = /postback {")
	rootAt := strings.Index(s, "\n    location / {")
	if pbAt < 0 || rootAt < pbAt {
		t.Fatal("emtds.conf: ожидали location = /postback до location /")
	}
	pb := s[pbAt:rootAt]
	for _, frag := range []string{
		"proxy_pass http://emtds_engine;",
		`proxy_set_header Connection "";`,
		"proxy_set_header Host $host;",
		"proxy_set_header X-Forwarded-For $remote_addr;",
		"proxy_set_header X-Forwarded-Proto https;",
		"proxy_set_header CF-Connecting-IP $emtds_cf_ip;",
		"proxy_set_header CF-IPCountry $emtds_cf_country;",
		"include /etc/nginx/emtds/cloudflare-realip.conf;",
		"real_ip_header CF-Connecting-IP;",
		"proxy_read_timeout 60s;",
	} {
		if !strings.Contains(pb, frag) {
			t.Fatalf("emtds.conf /postback: нет %q", frag)
		}
	}
	if strings.Contains(s[rootAt:], "emtds_engine") {
		t.Fatal("emtds.conf: location / должен идти в админку, не в движок")
	}
	if regexp.MustCompile(`(?m)^\s*upstream\s+emtds_engine`).MatchString(s) {
		t.Fatal("emtds.conf: upstream emtds_engine объявляется в emtds-engine.conf")
	}
}

// TestNginxAdminHostTemplate проверяет vhost admin.* через Cloudflare.
func TestNginxAdminHostTemplate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("nginx", "emtds-admin-host.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, frag := range []string{
		`server_name ~^admin\.`,
		"if ($emtds_from_cf = 0)",
		"return 444",
		"client_max_body_size 64m",
		"proxy_pass http://emtds_admin;",
		"location = /postback",
		"proxy_pass http://emtds_engine;",
		"proxy_set_header X-Forwarded-For $remote_addr",
		`proxy_set_header CF-Connecting-IP ""`,
		`proxy_set_header CF-IPCountry ""`,
		"proxy_read_timeout 100s",
		"real_ip_header CF-Connecting-IP",
		"/etc/nginx/emtds/cloudflare-realip.conf",
		"emtds-engine.conf",
		"$emtds_from_cf",
	} {
		if !strings.Contains(s, frag) {
			t.Fatalf("emtds-admin-host.conf: нет %q", frag)
		}
	}
	if strings.Contains(s, "$proxy_add_x_forwarded_for") {
		t.Fatal("emtds-admin-host.conf: не должно быть $proxy_add_x_forwarded_for")
	}
	if regexp.MustCompile(`(?m)^\s*upstream\s`).MatchString(s) {
		t.Fatal("emtds-admin-host.conf: upstream объявляются в emtds.conf и emtds-engine.conf")
	}
	if n := strings.Count(s, `proxy_set_header Connection "";`); n != 2 {
		t.Fatalf("emtds-admin-host.conf: Connection \"\" в %d location, ждали 2", n)
	}
}

// TestNginxUpstreamsKeepalive проверяет, что каждый upstream объявлен ровно в одном
// шаблоне, у каждого proxy_pass на него есть keepalive-заголовок и прямых адресов не осталось.
func TestNginxUpstreamsKeepalive(t *testing.T) {
	declared := map[string]int{}
	upstreamRe := regexp.MustCompile(`(?m)^upstream\s+(\S+)\s*\{`)
	for _, name := range []string{"emtds.conf", "emtds-admin-host.conf", "emtds-engine.conf"} {
		data, err := os.ReadFile(filepath.Join("nginx", name))
		if err != nil {
			t.Fatal(err)
		}
		s := strings.ReplaceAll(string(data), "\r\n", "\n")
		for _, m := range upstreamRe.FindAllStringSubmatch(s, -1) {
			declared[m[1]]++
		}
		if regexp.MustCompile(`proxy_pass http://(127\.0\.0\.1|__ENGINE_UPSTREAM__)`).MatchString(s) {
			t.Fatalf("%s: proxy_pass мимо upstream с keepalive", name)
		}
		passes := strings.Count(s, "proxy_pass ")
		if got := strings.Count(s, `proxy_set_header Connection "";`); got != passes {
			t.Fatalf("%s: proxy_pass %d, Connection \"\" %d", name, passes, got)
		}
		if strings.Contains(s, "$http_upgrade") {
			t.Fatalf("%s: websocket upgrade не используется", name)
		}
	}
	if declared["emtds_engine"] != 1 || declared["emtds_admin"] != 1 || len(declared) != 2 {
		t.Fatalf("upstream объявлены %v, ждали emtds_engine и emtds_admin по одному разу", declared)
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
		"EMTDS_ADMIN_TRUSTED_PROXIES",
		"emtds-admin-host.conf",
		"ensure_env_key_nonempty",
		"admin_trusted_proxies=",
		"emtds-http.conf",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("apply-edge.sh: нет %q", want)
		}
	}
	if strings.Contains(s, "conf.d/*.conf") {
		t.Fatal("apply-edge.sh: не должен массово трогать conf.d")
	}
	if matched, _ := regexp.MatchString(`listen\s+80\b`, s); matched {
		t.Fatal("apply-edge.sh: порт 80 описывается только шаблоном nginx/emtds-http.conf")
	}
}

// TestNginxHTTPTemplate проверяет шаблон порта 80: только редирект на https того же хоста, без
// default_server (чужой сервер с ним не конфликтует) и без проксирования.
func TestNginxHTTPTemplate(t *testing.T) {
	s := readEdgeFile(t, filepath.Join("nginx", "emtds-http.conf"))
	for _, frag := range []string{"listen 80;", "listen [::]:80;", "server_name _;", "return 301 https://$host$request_uri;"} {
		if !strings.Contains(s, frag) {
			t.Fatalf("emtds-http.conf: нет %q", frag)
		}
	}
	for _, bad := range []string{"default_server", "proxy_pass", "__", "listen 443"} {
		if strings.Contains(s, bad) {
			t.Fatalf("emtds-http.conf: не ждали %q", bad)
		}
	}
}

// TestEdgeHTTPListenerFiles: порт 80 занимают listen 80, адрес без порта, [::]:80 с параметрами;
// не занимают 443, 8080, unix-сокет, закомментированный listen, свои conf.d/emtds*.conf и файлы из
// аргументов. Без дампа nginx -T порт считается занятым.
func TestEdgeHTTPListenerFiles(t *testing.T) {
	root := t.TempDir()
	dump := filepath.Join(root, "dump.txt")
	files := []struct{ name, body string }{
		{"/etc/nginx/nginx.conf", "http {\n    include /etc/nginx/conf.d/*.conf;\n}\n"},
		{"/x/a.conf", "server { listen 443 ssl; }\n"},
		{"/x/b.conf", "server {\n    listen 8080;\n}\n"},
		{"/x/c.conf", "server {\n    listen 127.0.0.1;\n}\n"},
		{"/x/d.conf", "server {\n\tlisten [::]:80 ipv6only=on;\n}\n"},
		{"/x/e.conf", "server {\n    # listen 80;\n    listen 443;\n}\n"},
		{"/x/f.conf", "server {\n    listen unix:/run/x.sock;\n}\n"},
		{"/x/stock.conf", "server {\n    listen       80;\n}\n"},
		{root + "/conf.d/emtds-http.conf", "server {\n    listen 80;\n}\n"},
	}
	var b strings.Builder
	for _, f := range files {
		b.WriteString("# configuration file " + f.name + ":\n" + f.body + "\n")
	}
	mustWrite(t, dump, b.String(), 0o644)
	env := append(edgeStubEnv(t), "STUB_NGINX_DUMP="+dump)
	out := edgeSourced(t, "EDGE_CONF_D=$2/conf.d\nedge_http_listener_files /x/stock.conf | LC_ALL=C sort", env, root)
	if got := strings.Fields(out); strings.Join(got, " ") != "/x/c.conf /x/d.conf" {
		t.Fatalf("слушают порт 80: %v, ждали /x/c.conf /x/d.conf", got)
	}
	if got := strings.TrimSpace(edgeSourced(t, "edge_http_listener_files", edgeStubEnv(t))); got != "nginx -T" {
		t.Fatalf("без дампа: %q, ждали nginx -T", got)
	}
}

// stubDpkgQueryScript имитирует dpkg-query для одного файла пакета STUB_STOCK с md5 STUB_STOCK_MD5.
const stubDpkgQueryScript = `case "$1" in
-S) if [ "$2" = "$STUB_STOCK" ]; then echo "nginx: $2"; exit 0; fi; exit 1;;
-W) printf ' %s %s\n' "$STUB_STOCK" "$STUB_STOCK_MD5";;
*) exit 1;;
esac
`

// stockWelcome — стандартный conf.d/default.conf пакета nginx.org в сокращении.
const stockWelcome = "server {\n    listen       80;\n    server_name  localhost;\n}\n"

// md5Hex возвращает md5 строки s в шестнадцатеричном виде, как его пишет dpkg.
func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestEdgeStockDefault: неизменённый файл пакета узнаётся по md5 из dpkg, изменённый и чужой — нет,
// символьная ссылка на файл пакета разыменовывается.
func TestEdgeStockDefault(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stock := filepath.Join(root, "default.conf")
	mustWrite(t, stock, stockWelcome, 0o644)
	link := filepath.Join(root, "enabled")
	if err := os.Symlink(stock, link); err != nil {
		t.Skipf("символьные ссылки недоступны: %v", err)
	}
	other := filepath.Join(root, "other.conf")
	mustWrite(t, other, stockWelcome, 0o644)
	bin := filepath.Join(t.TempDir(), "bin")
	writeStub(t, bin, "dpkg-query", stubDpkgQueryScript)
	env := []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "STUB_STOCK=" + stock, "STUB_STOCK_MD5=" + md5Hex(stockWelcome)}
	body := `for f in "$2" "$3" "$4" "$5"; do if edge_stock_default "$f"; then echo yes; else echo no; fi; done`
	if got := strings.Fields(edgeSourced(t, body, env, stock, link, other, filepath.Join(root, "missing"))); strings.Join(got, " ") != "yes yes no no" {
		t.Fatalf("неизменённый, ссылка, чужой, нет файла: %v", got)
	}
	mustWrite(t, stock, stockWelcome+"# edited\n", 0o644)
	if got := strings.TrimSpace(edgeSourced(t, body, env, stock, link, other, other)); !strings.HasPrefix(got, "no\nno") {
		t.Fatalf("изменённый файл пакета считается стандартным: %q", got)
	}
}

// TestApplyEdgeHTTPRedirect: если порт 80 держит только неизменённая страница пакета, apply ставит
// conf.d/emtds-http.conf и убирает страницу в бэкап; повторный запуск ничего не ломает; при сбое nginx -t
// страница возвращается, а emtds-http.conf удаляется. Чужой сервер на порту 80 оставляет всё как есть.
func TestApplyEdgeHTTPRedirect(t *testing.T) {
	f := newEdgeApplyFixture(t, ubuntuNginxConf)
	root, err := filepath.EvalSymlinks(f.root)
	if err != nil {
		t.Fatal(err)
	}
	f.root = root
	stock := filepath.Join(root, "conf.d", "default.conf")
	f.stub(t, "dpkg-query", stubDpkgQueryScript)
	dump := filepath.Join(root, "dump.txt")
	withStock := "# configuration file " + filepath.Join(root, "nginx.conf") + ":\nhttp {\n}\n# configuration file " + stock + ":\n" + stockWelcome +
		"# configuration file " + filepath.Join(root, "conf.d", "emtds.conf") + ":\nserver {\n    listen 443 ssl;\n}\n"
	env := []string{
		"STUB_NGINX_DUMP=" + dump,
		"STUB_STOCK=" + stock,
		"STUB_STOCK_MD5=" + md5Hex(stockWelcome),
		"EMTDS_NGINX_STOCK_DEFAULTS=" + stock + " " + filepath.Join(root, "sites-enabled", "default"),
	}
	httpConf := filepath.Join(root, "conf.d", "emtds-http.conf")
	exists := func(path string) bool {
		_, err := os.Lstat(path)
		return err == nil
	}

	mustWrite(t, stock, stockWelcome, 0o644)
	mustWrite(t, dump, withStock, 0o644)
	_, errOut, code := f.run(t, append([]string{"STUB_NGINX_T_RC=1"}, env...))
	if code == 0 {
		t.Fatalf("ждали ошибку nginx -t, stderr:\n%s", errOut)
	}
	if readEdgeFile(t, stock) != stockWelcome || exists(httpConf) {
		t.Fatalf("после отката страница пакета не вернулась или emtds-http.conf остался:\n%s", errOut)
	}

	out, errOut, code := f.run(t, env)
	if code != 0 {
		t.Fatalf("apply: код %d\n%s", code, errOut)
	}
	if exists(stock) || readEdgeFile(t, httpConf) != readEdgeFile(t, filepath.Join("nginx", "emtds-http.conf")) {
		t.Fatalf("страница пакета не отключена или emtds-http.conf не поставлен:\n%s\n%s", out, errOut)
	}
	saved, _ := filepath.Glob(filepath.Join(root, "backup", "*", "stock", "*default.conf"))
	if len(saved) != 1 || readEdgeFile(t, saved[0]) != stockWelcome || !strings.Contains(out, "отключён стандартный") {
		t.Fatalf("копия страницы пакета в бэкапе: %v\n%s", saved, out)
	}

	mustWrite(t, dump, "# configuration file "+filepath.Join(root, "nginx.conf")+":\nhttp {\n}\n# configuration file "+httpConf+":\nserver {\n    listen 80;\n}\n", 0o644)
	if _, errOut, code := f.run(t, env); code != 0 || !exists(httpConf) {
		t.Fatalf("повторный apply: код %d, emtds-http.conf есть: %v\n%s", code, exists(httpConf), errOut)
	}

	busy := newEdgeApplyFixture(t, ubuntuNginxConf)
	broot, err := filepath.EvalSymlinks(busy.root)
	if err != nil {
		t.Fatal(err)
	}
	busy.root = broot
	busy.stub(t, "dpkg-query", stubDpkgQueryScript)
	bstock := filepath.Join(broot, "conf.d", "default.conf")
	mustWrite(t, bstock, stockWelcome, 0o644)
	mustWrite(t, filepath.Join(broot, "conf.d", "emtds-http.conf"), "old redirect\n", 0o644)
	bdump := filepath.Join(broot, "dump.txt")
	mustWrite(t, bdump, "# configuration file /etc/nginx/nginx.conf:\nserver {\n\tserver_name localhost;\n\tlisten 80;\n}\n# configuration file "+bstock+":\n"+stockWelcome, 0o644)
	benv := []string{"STUB_NGINX_DUMP=" + bdump, "STUB_STOCK=" + bstock, "STUB_STOCK_MD5=" + md5Hex(stockWelcome), "EMTDS_NGINX_STOCK_DEFAULTS=" + bstock}
	out, errOut, code = busy.run(t, benv)
	if code != 0 {
		t.Fatalf("apply при занятом порту 80: код %d\n%s", code, errOut)
	}
	if readEdgeFile(t, bstock) != stockWelcome || exists(filepath.Join(broot, "conf.d", "emtds-http.conf")) || !strings.Contains(out, "порт 80 занят (/etc/nginx/nginx.conf)") {
		t.Fatalf("при чужом сервере на порту 80 страница пакета должна остаться, а emtds-http.conf уйти:\n%s\n%s", out, errOut)
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
	content := "EMTDS_PUBLIC_IP=203.0.113.55\nEMTDS_TRUSTED_PROXIES=172.18.0.1/32\nEMTDS_ADMIN_TRUSTED_PROXIES=172.18.0.1/32\nEMTDS_ENGINE_PUBLISH=127.0.0.1:18080:8080\n"
	if err := os.WriteFile(tmpEnv, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, filepath.Join(dir, "apply-edge.sh"), "--check")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"EMTDS_SERVER_ENV="+tmpEnv,
		"EMTDS_INSTALL_STATE_DIR=/srv/emtds-state",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--check: %v\n%s", err, out)
	}
	outS := string(out)
	if !strings.Contains(outS, "203.0.113.55") {
		t.Fatalf("--check: нет подставленного IP в выводе:\n%s", out)
	}
	if strings.Contains(outS, "__STATE_DIR__") || !strings.Contains(outS, "ssl_certificate /srv/emtds-state/tls/ip.crt;") {
		t.Fatalf("--check: путь сертификата не взят из каталога состояния:\n%s", out)
	}
	if !strings.Contains(outS, "server 127.0.0.1:18080;") {
		t.Fatalf("--check: engine upstream не отрендерен:\n%s", out)
	}
	if strings.Contains(outS, "__PUBLIC_IP__") || strings.Contains(outS, "__ENGINE_UPSTREAM__") {
		t.Fatalf("--check: остались плейсхолдеры:\n%s", out)
	}
	if !strings.Contains(outS, "admin_trusted_proxies=172.18.0.1/32") {
		t.Fatalf("--check: нет admin_trusted_proxies в выводе:\n%s", out)
	}
	for _, block := range []string{
		"===== emtds.conf =====",
		"===== emtds-admin-host.conf =====",
		"===== emtds-engine.conf =====",
	} {
		if !strings.Contains(outS, block) {
			t.Fatalf("--check: нет блока %q:\n%s", block, out)
		}
	}
	if !strings.Contains(outS, "server_name ~^admin\\.") {
		t.Fatalf("--check: admin-host не отрендерен:\n%s", out)
	}
	if !strings.Contains(outS, "server 127.0.0.1:8090;") {
		t.Fatalf("--check: admin upstream 8090 не в выводе:\n%s", out)
	}
	ipAt := strings.Index(outS, "===== emtds.conf =====")
	adminAt := strings.Index(outS, "===== emtds-admin-host.conf =====")
	engineAt := strings.Index(outS, "===== emtds-engine.conf =====")
	if ipAt < 0 || adminAt < ipAt {
		t.Fatalf("--check: нет блока emtds.conf перед admin-host:\n%s", out)
	}
	ipHost := outS[ipAt:adminAt]
	if !strings.Contains(ipHost, "server_name 203.0.113.55;") || !strings.Contains(ipHost, "location = /postback") || !strings.Contains(ipHost, "proxy_pass http://emtds_engine;") {
		t.Fatalf("--check: на IP-vhost /postback не проксируется на движок:\n%s", ipHost)
	}
	if adminAt < 0 || engineAt < adminAt {
		t.Fatalf("--check: нет блока admin-host перед engine:\n%s", out)
	}
	adminHost := outS[adminAt:engineAt]
	if !strings.Contains(adminHost, "location = /postback") || !strings.Contains(adminHost, "proxy_pass http://emtds_engine;") || !strings.Contains(outS[engineAt:], "server 127.0.0.1:18080;") {
		t.Fatalf("--check: /postback не проксируется на движок:\n%s", adminHost)
	}
}

// TestApplyEdgeIgnoresRemovedPrefix не читает ключи server.env с удалённым префиксом: адрес берётся из curl и пишется как EMTDS_PUBLIC_IP.
func TestApplyEdgeIgnoresRemovedPrefix(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	tmpEnv := filepath.Join(tmp, "server.env")
	removed := "KU" + "ZTDS_"
	content := removed + "PUBLIC_IP=203.0.113.77\nEMTDS_TRUSTED_PROXIES=172.18.0.1/32\nEMTDS_ADMIN_TRUSTED_PROXIES=172.18.0.1/32\n" + removed + "ENGINE_PUBLISH=127.0.0.1:18081:8080\n"
	if err := os.WriteFile(tmpEnv, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	curlDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(curlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeCurl(t, curlDir, "198.51.100.20")
	cmd := exec.Command(bash, filepath.Join(dir, "apply-edge.sh"), "--check")
	cmd.Dir = dir
	cmd.Env = edgeEnvWithoutPublicIP("EMTDS_SERVER_ENV="+tmpEnv, "PATH="+curlDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--check: %v\n%s", err, out)
	}
	outS := string(out)
	if !strings.Contains(outS, "public_ip=198.51.100.20") || strings.Contains(outS, "203.0.113.77") || strings.Contains(outS, "18081") {
		t.Fatalf("--check: ключ с удалённым префиксом прочитан:\n%s", out)
	}
	after, err := os.ReadFile(tmpEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "EMTDS_PUBLIC_IP=198.51.100.20") {
		t.Fatalf("EMTDS_PUBLIC_IP не записан:\n%s", after)
	}
}

// TestApplyEdgeEmptyAdminTrustedProxiesFilled дописывает пустой EMTDS_ADMIN_TRUSTED_PROXIES из engine-ключа.
func TestApplyEdgeEmptyAdminTrustedProxiesFilled(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmpEnv := filepath.Join(t.TempDir(), "server.env")
	content := "EMTDS_PUBLIC_IP=203.0.113.55\nEMTDS_TRUSTED_PROXIES=172.18.0.1/32\nEMTDS_ADMIN_TRUSTED_PROXIES=\nEMTDS_ENGINE_PUBLISH=127.0.0.1:18080:8080\n"
	if err := os.WriteFile(tmpEnv, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, filepath.Join(dir, "apply-edge.sh"), "--check")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "EMTDS_SERVER_ENV="+tmpEnv)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--check: %v\n%s", err, out)
	}
	after, err := os.ReadFile(tmpEnv)
	if err != nil {
		t.Fatal(err)
	}
	afterS := string(after)
	if !strings.Contains(afterS, "EMTDS_ADMIN_TRUSTED_PROXIES=172.18.0.1/32") {
		t.Fatalf("ожидали заполнение admin trusted proxies, файл:\n%s", afterS)
	}
	if strings.Contains(afterS, "EMTDS_ADMIN_TRUSTED_PROXIES=\n") {
		t.Fatalf("пустой EMTDS_ADMIN_TRUSTED_PROXIES не исправлен:\n%s", afterS)
	}
}

// TestApplyEdgeEmptyTrustedProxiesFilled дописывает пустой EMTDS_TRUSTED_PROXIES тем же CIDR, что admin.
func TestApplyEdgeEmptyTrustedProxiesFilled(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmpEnv := filepath.Join(t.TempDir(), "server.env")
	content := "EMTDS_PUBLIC_IP=203.0.113.55\nEMTDS_TRUSTED_PROXIES=\nEMTDS_ADMIN_TRUSTED_PROXIES=10.11.12.13/32\nEMTDS_ENGINE_PUBLISH=127.0.0.1:18080:8080\n"
	if err := os.WriteFile(tmpEnv, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, filepath.Join(dir, "apply-edge.sh"), "--check")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "EMTDS_SERVER_ENV="+tmpEnv)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--check: %v\n%s", err, out)
	}
	after, err := os.ReadFile(tmpEnv)
	if err != nil {
		t.Fatal(err)
	}
	afterS := string(after)
	if !strings.Contains(afterS, "EMTDS_TRUSTED_PROXIES=10.11.12.13/32") {
		t.Fatalf("ожидали trusted из admin CIDR без docker, файл:\n%s", afterS)
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

// edgeEnvWithoutPublicIP is the process environment without EMTDS_PUBLIC_IP, plus extra.
// extra entries are appended as-is. The host value must not satisfy an empty server.env.
func edgeEnvWithoutPublicIP(extra ...string) []string {
	out := make([]string, 0, len(os.Environ())+len(extra))
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "EMTDS_PUBLIC_IP=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, extra...)
}

// writeFakeCurl puts an executable curl on dir that prints body and ignores arguments.
// The test uses it instead of the network. It returns dir, which belongs at the front of PATH.
func writeFakeCurl(t *testing.T, dir, body string) string {
	t.Helper()
	script := "#!/bin/sh\nprintf '%s\\n' '" + body + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestApplyEdgeFillsEmptyPublicIP читает пустой EMTDS_PUBLIC_IP через curl из PATH, без сети.
func TestApplyEdgeFillsEmptyPublicIP(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	tmpEnv := filepath.Join(tmp, "server.env")
	content := "EMTDS_TRUSTED_PROXIES=172.18.0.1/32\nEMTDS_ADMIN_TRUSTED_PROXIES=172.18.0.1/32\nEMTDS_ENGINE_PUBLISH=127.0.0.1:18080:8080\n"
	if err := os.WriteFile(tmpEnv, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	curlDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(curlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeCurl(t, curlDir, "198.51.100.20")
	cmd := exec.Command(bash, filepath.Join(dir, "apply-edge.sh"), "--check")
	cmd.Dir = dir
	cmd.Env = edgeEnvWithoutPublicIP("EMTDS_SERVER_ENV="+tmpEnv, "PATH="+curlDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--check: %v\n%s", err, out)
	}
	after, err := os.ReadFile(tmpEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "EMTDS_PUBLIC_IP=198.51.100.20") {
		t.Fatalf("пустой адрес не записан:\n%s", after)
	}
}

// TestApplyEdgeKeepsSetPublicIP не заменяет уже заданный адрес ответом curl.
func TestApplyEdgeKeepsSetPublicIP(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	tmpEnv := filepath.Join(tmp, "server.env")
	content := "EMTDS_PUBLIC_IP=203.0.113.55\nEMTDS_TRUSTED_PROXIES=172.18.0.1/32\nEMTDS_ADMIN_TRUSTED_PROXIES=172.18.0.1/32\nEMTDS_ENGINE_PUBLISH=127.0.0.1:18080:8080\n"
	if err := os.WriteFile(tmpEnv, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	curlDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(curlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeCurl(t, curlDir, "198.51.100.8")
	cmd := exec.Command(bash, filepath.Join(dir, "apply-edge.sh"), "--check")
	cmd.Dir = dir
	cmd.Env = edgeEnvWithoutPublicIP("EMTDS_SERVER_ENV="+tmpEnv, "PATH="+curlDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--check: %v\n%s", err, out)
	}
	after, err := os.ReadFile(tmpEnv)
	if err != nil {
		t.Fatal(err)
	}
	afterS := string(after)
	if !strings.Contains(afterS, "EMTDS_PUBLIC_IP=203.0.113.55") || strings.Contains(afterS, "198.51.100.8") {
		t.Fatalf("заданный адрес изменился:\n%s", afterS)
	}
}

// TestApplyEdgeRejectsBadPublicIP не пишет в файл ответ curl, который не является IPv4.
func TestApplyEdgeRejectsBadPublicIP(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	tmpEnv := filepath.Join(tmp, "server.env")
	content := "EMTDS_TRUSTED_PROXIES=172.18.0.1/32\nEMTDS_ADMIN_TRUSTED_PROXIES=172.18.0.1/32\nEMTDS_ENGINE_PUBLISH=127.0.0.1:18080:8080\n"
	if err := os.WriteFile(tmpEnv, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	curlDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(curlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeCurl(t, curlDir, "nope")
	cmd := exec.Command(bash, filepath.Join(dir, "apply-edge.sh"), "--check")
	cmd.Dir = dir
	cmd.Env = edgeEnvWithoutPublicIP("EMTDS_SERVER_ENV="+tmpEnv, "PATH="+curlDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("--check должен завершиться с ошибкой:\n%s", out)
	}
	after, err := os.ReadFile(tmpEnv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "EMTDS_PUBLIC_IP=") || strings.Contains(string(after), "nope") {
		t.Fatalf("мусор попал в файл:\n%s", after)
	}
}

// TestComposeEdgeEnv проверяет проброс EMTDS_PUBLIC_IP и EMTDS_TRUSTED_PROXIES в compose.
func TestComposeEdgeEnv(t *testing.T) {
	data, err := os.ReadFile("docker-compose.app.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "EMTDS_TRUSTED_PROXIES: ${EMTDS_TRUSTED_PROXIES:-}") {
		t.Fatal("docker-compose.app.yml: нет EMTDS_TRUSTED_PROXIES у engine")
	}
	if !strings.Contains(s, "EMTDS_PUBLIC_IP: ${EMTDS_PUBLIC_IP:-}") {
		t.Fatal("docker-compose.app.yml: нет EMTDS_PUBLIC_IP у admin")
	}
	if !strings.Contains(s, "EMTDS_ADMIN_TRUSTED_PROXIES: ${EMTDS_ADMIN_TRUSTED_PROXIES:-}") {
		t.Fatal("docker-compose.app.yml: нет EMTDS_ADMIN_TRUSTED_PROXIES у admin")
	}
}

// TestApplyEdgeInstallRestoreConfigs подключает apply-edge.sh через source и проверяет, что install_rendered
// кладёт в бэкап только emtds*.conf, не трогает чужие файлы, а restore_from_backup возвращает старое и удаляет новые emtds*.conf.
func TestApplyEdgeInstallRestoreConfigs(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	confD := filepath.Join(tmp, "conf.d")
	rendered := filepath.Join(tmp, "rendered")
	for _, name := range []string{"emtds.conf", "emtds-admin-host.conf", "emtds-engine.conf", "cloudflare-realip.conf", "cloudflare-geo.conf"} {
		write(filepath.Join(rendered, name), "new "+name+"\n")
	}
	write(filepath.Join(confD, "emtds.conf"), "old server_name\n")
	write(filepath.Join(confD, "other.conf"), "foreign\n")

	script := `set -euo pipefail
source "$1"
EDGE_CONF_D=$2/conf.d
EDGE_NGINX_EMTDS_DIR=$2/nginx-emtds
EDGE_BACKUP_ROOT=$2/backup
install_rendered "$2/rendered"
LC_ALL=C ls "$EDGE_CONF_D" | tr '\n' ' '; echo
LC_ALL=C ls "$EDGE_BACKUP_DIR/conf.d" | tr '\n' ' '; echo
restore_from_backup "$EDGE_BACKUP_DIR"
LC_ALL=C ls "$EDGE_CONF_D" | tr '\n' ' '; echo
cat "$EDGE_CONF_D/emtds.conf"
`
	cmd := exec.Command(bash, "-c", script, "bash", filepath.Join(dir, "apply-edge.sh"), tmp)
	cmd.Env = append(os.Environ(), "EMTDS_INSTALL_STATE_DIR="+filepath.Join(tmp, "state"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("source apply-edge.sh: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 4 {
		t.Fatalf("неожиданный вывод:\n%s", out)
	}
	if got, want := strings.Fields(lines[0]), []string{"emtds-admin-host.conf", "emtds-engine.conf", "emtds.conf", "other.conf"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("после установки в conf.d %v, ждали %v", got, want)
	}
	if got, want := strings.Fields(lines[1]), []string{"emtds.conf"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("в бэкапе conf.d %v, ждали %v", got, want)
	}
	if got, want := strings.Fields(lines[2]), []string{"emtds.conf", "other.conf"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("после отката в conf.d %v, ждали %v", got, want)
	}
	if lines[3] != "old server_name" {
		t.Fatalf("после отката emtds.conf не вернулся: %q", lines[3])
	}
}

// ubuntuNginxConf is a typical Ubuntu nginx.conf without worker_rlimit_nofile.
const ubuntuNginxConf = `user www-data;
worker_processes auto;
pid /run/nginx.pid;
error_log /var/log/nginx/error.log;
include /etc/nginx/modules-enabled/*.conf;

events {
	worker_connections 768;
	# multi_accept on;
}

http {
	sendfile on;
	include /etc/nginx/mime.types;
	include /etc/nginx/conf.d/*.conf;
	include /etc/nginx/sites-enabled/*;
}
`

// edgeSourced запускает body после source apply-edge.sh: $1 — путь к скрипту, $2... — args.
// env добавляется к окружению процесса. Возвращает stdout; при ненулевом коде тест падает.
func edgeSourced(t *testing.T, body string, env []string, args ...string) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	argv := append([]string{"-c", "set -euo pipefail\nsource \"$1\"\n" + body, "bash", filepath.Join(dir, "apply-edge.sh")}, args...)
	cmd := exec.Command(bash, argv...)
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("source apply-edge.sh: %v\n%s\n%s", err, out, stderr.String())
	}
	return string(out)
}

// writeEdgeFile пишет body в файл name во временном каталоге и возвращает путь.
func writeEdgeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// edgeRun запускает body после source apply-edge.sh так же, как edgeSourced, но не требует
// успешного завершения: возвращает stdout, stderr и код выхода.
func edgeRun(t *testing.T, body string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	argv := append([]string{"-c", "set -Eeuo pipefail\nsource \"$1\"\n" + body, "bash", filepath.Join(dir, "apply-edge.sh")}, args...)
	cmd := exec.Command(bash, argv...)
	cmd.Env = append(os.Environ(), env...)
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

// writeStub кладёт в dir исполняемый sh-скрипт name с телом script.
func writeStub(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// readEdgeFile возвращает содержимое файла path как строку.
func readEdgeFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// stubNginxScript имитирует nginx: -T печатает дамп из STUB_NGINX_DUMP (без переменной — код 1),
// -t завершается кодом STUB_NGINX_T_RC.
const stubNginxScript = `case "$1" in
-T)
	if [ -n "${STUB_NGINX_DUMP:-}" ]; then cat "$STUB_NGINX_DUMP"; exit 0; fi
	exit 1
	;;
-t)
	if [ "${STUB_NGINX_T_RC:-0}" != 0 ]; then echo "nginx: [emerg] stub" >&2; fi
	exit "${STUB_NGINX_T_RC:-0}"
	;;
esac
exit 0
`

// edgeStubEnv создаёт в каталоге с заглушками nginx и возвращает PATH с этим каталогом впереди.
// Без дампа (STUB_NGINX_DUMP) заглушка не поддерживает -T, и разбор идёт по include.
func edgeStubEnv(t *testing.T) []string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	writeStub(t, bin, "nginx", stubNginxScript)
	return []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
}

// TestRenderNginxEvents проверяет правку nginx.conf: нет директив, ниже и выше порога,
// комментарии, однострочный events, events без пробела, скобка на следующей строке,
// табы и пробелы, кавычки со скобками и #, многострочные и экранированные строки,
// worker_connections вне events; повторный рендер результата ничего не меняет.
func TestRenderNginxEvents(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []string
		notWant []string
	}{
		{
			name: "нет директив",
			in:   ubuntuNginxConf,
			want: []string{"\nworker_rlimit_nofile 16384;\nevents {\n\tworker_connections 4096;\n", "include /etc/nginx/modules-enabled/*.conf;"},
		},
		{
			name:    "ниже порога",
			in:      "worker_rlimit_nofile 1024;\nevents {\n\tworker_connections 768;\n}\nhttp {\n}\n",
			want:    []string{"worker_rlimit_nofile 16384;", "worker_connections 4096;"},
			notWant: []string{"1024", "768"},
		},
		{
			name:    "выше порога не трогать",
			in:      "worker_rlimit_nofile 65535;\nevents {\n\tworker_connections 10000;\n}\n",
			want:    []string{"worker_rlimit_nofile 65535;", "worker_connections 10000;"},
			notWant: []string{"16384", "4096"},
		},
		{
			name: "ровно порог",
			in:   "worker_rlimit_nofile 16384;\nevents {\n\tworker_connections 4096;\n}\n",
			want: []string{"worker_rlimit_nofile 16384;", "worker_connections 4096;"},
		},
		{
			name:    "connections выше порога, nofile нет",
			in:      "events {\n\tworker_connections 8192;\n}\n",
			want:    []string{"worker_rlimit_nofile 16384;\nevents {", "worker_connections 8192;"},
			notWant: []string{"4096"},
		},
		{
			name: "комментарии не считаются директивами",
			in:   "# worker_rlimit_nofile 99999;\nevents {\n\t# worker_connections 99999;\n}\n",
			want: []string{"worker_rlimit_nofile 16384;\nevents {", "    worker_connections 4096;\n}", "# worker_connections 99999;"},
		},
		{
			name: "nofile после events и connections в http",
			in:   "events {\n\tworker_connections 100;\n}\nworker_rlimit_nofile 2048;\nhttp {\n\tworker_connections 5;\n}\n",
			want: []string{"worker_rlimit_nofile 16384;", "worker_connections 4096;", "\tworker_connections 5;"},
		},
		{
			name: "однострочный events",
			in:   "events { worker_connections 512; }\n",
			want: []string{"events { worker_connections 4096; }", "worker_rlimit_nofile 16384;"},
		},
		{
			name: "однострочный events без connections",
			in:   "events { multi_accept on; }\n",
			want: []string{"events { worker_connections 4096; multi_accept on; }"},
		},
		{
			name: "events без worker_connections",
			in:   "events {\n\tmulti_accept on;\n}\n",
			want: []string{"\tmulti_accept on;\n    worker_connections 4096;\n}"},
		},
		{
			name: "events без пробела перед скобкой",
			in:   "events{\n\tworker_connections 100;\n}\n",
			want: []string{"worker_rlimit_nofile 16384;\nevents{", "\tworker_connections 4096;"},
		},
		{
			name: "скобка events на следующей строке",
			in:   "events\n{\n\tworker_connections 100;\n}\n",
			want: []string{"worker_rlimit_nofile 16384;\nevents\n{", "\tworker_connections 4096;"},
		},
		{
			name: "скобка на следующей строке без connections",
			in:   "events\n{\n}\n",
			want: []string{"worker_rlimit_nofile 16384;\nevents\n{\n    worker_connections 4096;\n}"},
		},
		{
			name: "табы и несколько пробелов между именем и значением",
			in:   "worker_rlimit_nofile\t 1024;\nevents {\n\tworker_connections\t\t  768 ;\n}\n",
			want: []string{"worker_rlimit_nofile\t 16384;", "worker_connections\t\t  4096 ;"},
		},
		{
			name: "кавычки со скобками, точкой с запятой и # внутри",
			in:   "http {\n\tlog_format main '{ # } worker_connections 5; \"';\n\tlog_format j \"} # {\";\n}\nevents {\n\tworker_connections 100;\n}\n",
			want: []string{"'{ # } worker_connections 5; \"';", "\"} # {\";", "\tworker_connections 4096;", "worker_rlimit_nofile 16384;\nevents {"},
		},
		{
			name: "многострочная строка со скобкой",
			in:   "http {\n\tlog_format main '{\n\t\t# }\n\t';\n}\nevents {\n\tworker_connections 100;\n}\n",
			want: []string{"'{\n\t\t# }\n\t';", "\tworker_connections 4096;"},
		},
		{
			name: "экранированная кавычка в строке",
			in:   "http {\n\tlog_format main 'it\\'s { not a block';\n}\nevents {\n}\n",
			want: []string{"'it\\'s { not a block';", "    worker_connections 4096;\n}"},
		},
		{
			name: "worker_connections в http не считается",
			in:   "http {\n\tworker_connections 99999;\n}\nevents {\n}\n",
			want: []string{"\tworker_connections 99999;", "    worker_connections 4096;\n}"},
		},
		{
			name: "комментарий сразу после точки с запятой",
			in:   "events {\n\tworker_connections 100;# 5000\n}\n",
			want: []string{"worker_connections 4096;# 5000"},
		},
	}
	nofileLine := regexp.MustCompile(`(?m)^worker_rlimit_nofile\s`)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := edgeSourced(t, `render_nginx_events "$2" 4096 16384`, nil, writeEdgeFile(t, "nginx.conf", tc.in))
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Fatalf("нет %q в результате:\n%s", w, out)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(out, w) {
					t.Fatalf("не ждали %q в результате:\n%s", w, out)
				}
			}
			if n := len(nofileLine.FindAllString(out, -1)); n != 1 {
				t.Fatalf("worker_rlimit_nofile в главном контексте %d раз:\n%s", n, out)
			}
			again := edgeSourced(t, `render_nginx_events "$2" 4096 16384`, nil, writeEdgeFile(t, "again.conf", out))
			if again != out {
				t.Fatalf("повторный рендер изменил результат:\n--- первый\n%s\n--- второй\n%s", out, again)
			}
		})
	}
}

// TestRenderNginxEventsRefuses: неоднозначный nginx.conf не правится, а завершается ошибкой
// с понятным сообщением и пустым stdout (второй экземпляр директивы не дописывается).
func TestRenderNginxEventsRefuses(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"нет events", "http {\n}\n", "блок events не найден"},
		{"два events", "events {\n}\nevents {\n}\n", "несколько блоков events"},
		{"connections дважды", "events {\n\tworker_connections 1;\n\tworker_connections 2;\n}\n", "worker_connections задан в events несколько раз"},
		{"nofile дважды", "worker_rlimit_nofile 1;\nworker_rlimit_nofile 2;\nevents {\n}\n", "worker_rlimit_nofile задан несколько раз"},
		{"connections не число", "events {\n\tworker_connections auto;\n}\n", "значение не число"},
		{"nofile не число", "worker_rlimit_nofile $x;\nevents {\n}\n", "значение не число"},
		{"скобка не закрыта", "events {\n", "не сбалансированы"},
		{"лишняя скобка", "}\nevents {\n}\n", "лишняя закрывающая скобка"},
		{"кавычка не закрыта", "events {\n}\nlog_format x 'abc;\n", "кавычка не закрыта"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut, code := edgeRun(t, `render_nginx_events "$2" 4096 16384`, nil, writeEdgeFile(t, "nginx.conf", tc.in))
			if code == 0 {
				t.Fatalf("ждали ошибку, вывод:\n%s", out)
			}
			if !strings.Contains(errOut, tc.want) {
				t.Fatalf("в stderr нет %q:\n%s", tc.want, errOut)
			}
			if out != "" {
				t.Fatalf("при ошибке stdout должен быть пуст:\n%s", out)
			}
		})
	}
}

// TestNginxForeignDirectiveFiles: директивы в подключаемом файле находятся по дампу nginx -T
// и, если nginx -T недоступен, по include главного контекста; пустой результат — без находок.
func TestNginxForeignDirectiveFiles(t *testing.T) {
	root := t.TempDir()
	conf := filepath.Join(root, "nginx.conf")
	body := "events {\n\tworker_connections 100;\n}\n"
	if err := os.WriteFile(conf, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(env ...string) string {
		out := edgeSourced(t, `EDGE_NGINX_CONF=$2
nginx_foreign_directive_files "$(readlink -f "$2")"`, env, conf)
		return strings.TrimSpace(out)
	}
	bin := filepath.Join(root, "bin")
	writeStub(t, bin, "nginx", stubNginxScript)
	path := "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")

	dumpOwn := filepath.Join(root, "dump-own.txt")
	if err := os.WriteFile(dumpOwn, []byte("# configuration file "+conf+":\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(path, "STUB_NGINX_DUMP="+dumpOwn); got != "" {
		t.Fatalf("дамп с одним файлом: ждали пусто, получили %q", got)
	}
	dumpForeign := filepath.Join(root, "dump-foreign.txt")
	if err := os.WriteFile(dumpForeign, []byte("# configuration file "+conf+":\n"+body+"# configuration file /etc/nginx/extra.conf:\nworker_connections 5;\n# worker_rlimit_nofile 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(path, "STUB_NGINX_DUMP="+dumpForeign); got != "/etc/nginx/extra.conf" {
		t.Fatalf("дамп с чужим файлом: получили %q", got)
	}

	withInclude := "include inc/*.conf;\n" + body
	if err := os.WriteFile(conf, []byte(withInclude), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "inc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inc", "a.conf"), []byte("types {\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(path); got != "" {
		t.Fatalf("include без директив: ждали пусто, получили %q", got)
	}
	hit := filepath.Join(root, "inc", "b.conf")
	if err := os.WriteFile(hit, []byte("worker_rlimit_nofile 100;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(path); got != hit {
		t.Fatalf("include с директивой: получили %q, ждали %q", got, hit)
	}
}

// TestTuneNginxEventsIdempotent правит файл на месте: первый вызов меняет, второй оставляет
// содержимое и время изменения; права 0640 сохраняются; временных файлов рядом не остаётся.
func TestTuneNginxEventsIdempotent(t *testing.T) {
	conf := writeEdgeFile(t, "nginx.conf", ubuntuNginxConf)
	if err := os.Chmod(conf, 0o640); err != nil {
		t.Fatal(err)
	}
	env := append(edgeStubEnv(t), "EMTDS_NGINX_CONF="+conf)
	edgeSourced(t, `tune_nginx_events`, env)
	first := readEdgeFile(t, conf)
	if !strings.Contains(first, "worker_connections 4096;") || !strings.Contains(first, "worker_rlimit_nofile 16384;") {
		t.Fatalf("правка не применена:\n%s", first)
	}
	stat1, err := os.Stat(conf)
	if err != nil {
		t.Fatal(err)
	}
	if stat1.Mode().Perm() != 0o640 {
		t.Fatalf("права после правки %v, ждали 0640", stat1.Mode().Perm())
	}
	edgeSourced(t, `tune_nginx_events`, env)
	if second := readEdgeFile(t, conf); second != first {
		t.Fatalf("повторный запуск изменил файл:\n%s", second)
	}
	stat2, err := os.Stat(conf)
	if err != nil {
		t.Fatal(err)
	}
	if !stat2.ModTime().Equal(stat1.ModTime()) {
		t.Fatal("повторный запуск перезаписал файл")
	}
	entries, err := os.ReadDir(filepath.Dir(conf))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("в каталоге остались лишние файлы: %v", entries)
	}
}

// edgeApplyFixture — каталог для запуска всего apply-edge.sh (main) с заглушками nginx,
// systemctl и подставными путями вместо /etc/nginx.
type edgeApplyFixture struct {
	root     string
	env      []string
	original string
}

// edgeApplyBody переопределяет пути на каталог $2, подменяет проверку root и запускает main с флагами $3...
const edgeApplyBody = `EDGE_CONF_D=$2/conf.d
EDGE_NGINX_EMTDS_DIR=$2/nginx-emtds
EDGE_BACKUP_ROOT=$2/backup
EDGE_NGINX_CONF=$2/nginx.conf
edge_is_root() { return 0; }
main "${@:3}"
`

// newEdgeApplyFixture готовит каталог: server.env, старый conf.d/emtds.conf, nginx.conf с правами 0640,
// заглушки nginx и systemctl (лог в STUB_LOG), пустой TMPDIR.
func newEdgeApplyFixture(t *testing.T, nginxConf string) *edgeApplyFixture {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	for _, d := range []string{"bin", "conf.d", "tmpdir"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"server.env":        "EMTDS_PUBLIC_IP=203.0.113.55\nEMTDS_TRUSTED_PROXIES=172.18.0.1/32\nEMTDS_ADMIN_TRUSTED_PROXIES=172.18.0.1/32\nEMTDS_ENGINE_PUBLISH=127.0.0.1:18080:8080\n",
		"conf.d/emtds.conf": "old emtds\n",
		"nginx.conf":        nginxConf,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(root, "nginx.conf"), 0o640); err != nil {
		t.Fatal(err)
	}
	writeStub(t, bin, "nginx", stubNginxScript)
	writeStub(t, bin, "systemctl", "echo \"systemctl $*\" >>\"$STUB_LOG\"\n")
	realInstall, err := exec.LookPath("install")
	if err != nil {
		t.Skip("install не найден в PATH")
	}
	return &edgeApplyFixture{
		root:     root,
		original: nginxConf,
		env: []string{
			"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
			"TMPDIR=" + filepath.Join(root, "tmpdir"),
			"EMTDS_SERVER_ENV=" + filepath.Join(root, "server.env"),
			"EMTDS_INSTALL_STATE_DIR=" + filepath.Join(root, "state"),
			"STUB_LOG=" + filepath.Join(root, "stub.log"),
			"STUB_NGINX_T_RC=0",
			"STUB_REAL_INSTALL=" + realInstall,
		},
	}
}

// run запускает main с флагами flags; extraEnv добавляется после базового окружения.
func (f *edgeApplyFixture) run(t *testing.T, extraEnv []string, flags ...string) (stdout, stderr string, code int) {
	t.Helper()
	return edgeRun(t, edgeApplyBody, append(append([]string{}, f.env...), extraEnv...), append([]string{f.root}, flags...)...)
}

// stub заменяет заглушку name в каталоге bin фикстуры.
func (f *edgeApplyFixture) stub(t *testing.T, name, script string) {
	t.Helper()
	writeStub(t, filepath.Join(f.root, "bin"), name, script)
}

// assertRolledBack проверяет полный откат: nginx.conf (по ссылке тоже) и права как были,
// conf.d/emtds.conf прежний, новых emtds-*.conf нет, каталога emtds нет, временных файлов нет.
func (f *edgeApplyFixture) assertRolledBack(t *testing.T, stderr string) {
	t.Helper()
	conf := filepath.Join(f.root, "nginx.conf")
	if got := readEdgeFile(t, conf); got != f.original {
		t.Fatalf("nginx.conf не восстановлен:\n%s\nstderr:\n%s", got, stderr)
	}
	if info, err := os.Stat(conf); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("права nginx.conf после отката: %v, %v", info, err)
	}
	if got := readEdgeFile(t, filepath.Join(f.root, "conf.d", "emtds.conf")); got != "old emtds\n" {
		t.Fatalf("conf.d/emtds.conf не восстановлен: %q", got)
	}
	for _, name := range []string{"emtds-admin-host.conf", "emtds-engine.conf"} {
		if _, err := os.Stat(filepath.Join(f.root, "conf.d", name)); err == nil {
			t.Fatalf("conf.d/%s остался после отката", name)
		}
	}
	if _, err := os.Stat(filepath.Join(f.root, "nginx-emtds")); err == nil {
		t.Fatal("каталог nginx-emtds остался после отката")
	}
	f.assertNoTempFiles(t)
	if log, err := os.ReadFile(filepath.Join(f.root, "stub.log")); err == nil && strings.Contains(string(log), "reload") {
		t.Fatalf("nginx перезагружен после сбоя: %s", log)
	}
}

// assertNoTempFiles проверяет, что рядом с nginx.conf нет *.emtds-new, а TMPDIR пуст.
func (f *edgeApplyFixture) assertNoTempFiles(t *testing.T) {
	t.Helper()
	dirs := []string{f.root, filepath.Join(f.root, "real")}
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".emtds-new") {
				t.Fatalf("остался временный файл %s", filepath.Join(d, e.Name()))
			}
		}
	}
	left, err := os.ReadDir(filepath.Join(f.root, "tmpdir"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("в TMPDIR остались файлы: %v", left)
	}
}

// TestApplyEdgeRollbackNginxTestFails: nginx -t падает после правки — откатываются conf.d и nginx.conf.
func TestApplyEdgeRollbackNginxTestFails(t *testing.T) {
	f := newEdgeApplyFixture(t, ubuntuNginxConf)
	_, errOut, code := f.run(t, []string{"STUB_NGINX_T_RC=1"}, "--tune-events")
	if code == 0 {
		t.Fatalf("ждали ошибку при падении nginx -t, stderr:\n%s", errOut)
	}
	f.assertRolledBack(t, errOut)
}

// TestApplyEdgeRollbackWriteFails: сбой записи nginx.conf (mv заменён заглушкой) — всё восстановлено,
// временных файлов не осталось.
func TestApplyEdgeRollbackWriteFails(t *testing.T) {
	f := newEdgeApplyFixture(t, ubuntuNginxConf)
	f.stub(t, "mv", "echo 'mv: stub failure' >&2\nexit 1\n")
	_, errOut, code := f.run(t, nil, "--tune-events")
	if code == 0 {
		t.Fatalf("ждали ошибку при сбое записи, stderr:\n%s", errOut)
	}
	if !strings.Contains(errOut, "не удалось записать") {
		t.Fatalf("нет сообщения о сбое записи:\n%s", errOut)
	}
	f.assertRolledBack(t, errOut)
}

// TestApplyEdgeRollbackInstallFails: сбой внутри install_rendered (вложенная функция) тоже откатывается:
// без set -E ловушка ERR здесь не срабатывала.
func TestApplyEdgeRollbackInstallFails(t *testing.T) {
	f := newEdgeApplyFixture(t, ubuntuNginxConf)
	f.stub(t, "install", "case \"$*\" in *emtds-engine.conf*) echo 'install: stub failure' >&2; exit 1;; esac\nexec \"$STUB_REAL_INSTALL\" \"$@\"\n")
	_, errOut, code := f.run(t, nil, "--tune-events")
	if code == 0 {
		t.Fatalf("ждали ошибку при сбое install, stderr:\n%s", errOut)
	}
	f.assertRolledBack(t, errOut)
}

// TestApplyEdgeRefusesIncludedDirectives: директива в подключаемом файле останавливает apply
// до любых записей: ни бэкапа, ни новых конфигов, nginx.conf прежний.
func TestApplyEdgeRefusesIncludedDirectives(t *testing.T) {
	conf := "include inc/*.conf;\nevents {\n\tworker_connections 100;\n}\n"
	f := newEdgeApplyFixture(t, conf)
	if err := os.MkdirAll(filepath.Join(f.root, "inc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "inc", "a.conf"), []byte("worker_rlimit_nofile 100;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errOut, code := f.run(t, nil, "--tune-events")
	if code == 0 || !strings.Contains(errOut, "подключаемых файлах") || !strings.Contains(errOut, "a.conf") {
		t.Fatalf("ждали отказ из-за include, код %d, stderr:\n%s", code, errOut)
	}
	f.assertRolledBack(t, errOut)
	if _, err := os.Stat(filepath.Join(f.root, "backup")); err == nil {
		t.Fatal("отказ до записи не должен создавать бэкап")
	}
}

// TestApplyEdgeTuneEventsSuccess: успешный apply с --tune-events правит nginx.conf с сохранением прав,
// кладёт оригинал в бэкап, перезагружает nginx; повторный запуск nginx.conf не меняет.
func TestApplyEdgeTuneEventsSuccess(t *testing.T) {
	f := newEdgeApplyFixture(t, ubuntuNginxConf)
	conf := filepath.Join(f.root, "nginx.conf")
	if _, errOut, code := f.run(t, nil, "--tune-events"); code != 0 {
		t.Fatalf("apply: код %d, stderr:\n%s", code, errOut)
	}
	tuned := readEdgeFile(t, conf)
	if !strings.Contains(tuned, "worker_connections 4096;") || !strings.Contains(tuned, "worker_rlimit_nofile 16384;") {
		t.Fatalf("nginx.conf не правлен:\n%s", tuned)
	}
	if info, err := os.Stat(conf); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("права после правки: %v, %v", info, err)
	}
	backups, err := filepath.Glob(filepath.Join(f.root, "backup", "*", "nginx.conf"))
	if err != nil || len(backups) != 1 || readEdgeFile(t, backups[0]) != ubuntuNginxConf {
		t.Fatalf("в бэкапе нет оригинального nginx.conf: %v, %v", backups, err)
	}
	if log := readEdgeFile(t, filepath.Join(f.root, "stub.log")); !strings.Contains(log, "systemctl reload nginx") {
		t.Fatalf("nginx не перезагружен: %q", log)
	}
	if _, errOut, code := f.run(t, nil, "--tune-events"); code != 0 {
		t.Fatalf("повторный apply: код %d, stderr:\n%s", code, errOut)
	}
	if again := readEdgeFile(t, conf); again != tuned {
		t.Fatalf("повторный apply изменил nginx.conf:\n%s", again)
	}
	f.assertNoTempFiles(t)
}

// TestApplyEdgeWithoutTuneEventsKeepsNginxConf: без флага apply не читает и не меняет nginx.conf,
// не кладёт его в бэкап.
func TestApplyEdgeWithoutTuneEventsKeepsNginxConf(t *testing.T) {
	f := newEdgeApplyFixture(t, ubuntuNginxConf)
	conf := filepath.Join(f.root, "nginx.conf")
	before, err := os.Stat(conf)
	if err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := f.run(t, nil); code != 0 {
		t.Fatalf("apply без флага: код %d, stderr:\n%s", code, errOut)
	}
	if got := readEdgeFile(t, conf); got != ubuntuNginxConf {
		t.Fatalf("nginx.conf изменён без флага:\n%s", got)
	}
	after, err := os.Stat(conf)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("nginx.conf перезаписан без флага")
	}
	if backups, _ := filepath.Glob(filepath.Join(f.root, "backup", "*", "nginx.conf")); len(backups) != 0 {
		t.Fatalf("без флага nginx.conf не должен попадать в бэкап: %v", backups)
	}
}

// TestApplyEdgeNginxConfSymlink: nginx.conf — символьная ссылка. Правится целевой файл, ссылка остаётся,
// при откате цель возвращается с прежними правами, бэкап — обычный файл с содержимым цели.
func TestApplyEdgeNginxConfSymlink(t *testing.T) {
	f := newEdgeApplyFixture(t, ubuntuNginxConf)
	link := filepath.Join(f.root, "nginx.conf")
	target := filepath.Join(f.root, "real", "nginx.real.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(link, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("символьные ссылки недоступны: %v", err)
	}
	isLink := func() {
		t.Helper()
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("nginx.conf перестал быть ссылкой: %v, %v", info, err)
		}
	}

	_, errOut, code := f.run(t, []string{"STUB_NGINX_T_RC=1"}, "--tune-events")
	if code == 0 {
		t.Fatalf("ждали ошибку при падении nginx -t, stderr:\n%s", errOut)
	}
	isLink()
	f.assertRolledBack(t, errOut)

	if err := os.RemoveAll(filepath.Join(f.root, "backup")); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := f.run(t, nil, "--tune-events"); code != 0 {
		t.Fatalf("apply через ссылку: код %d, stderr:\n%s", code, errOut)
	}
	isLink()
	if got := readEdgeFile(t, target); !strings.Contains(got, "worker_connections 4096;") {
		t.Fatalf("целевой файл не правлен:\n%s", got)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("права целевого файла: %v, %v", info, err)
	}
	backups, err := filepath.Glob(filepath.Join(f.root, "backup", "*", "nginx.conf"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("бэкапы nginx.conf: %v, %v", backups, err)
	}
	if info, err := os.Lstat(backups[0]); err != nil || info.Mode()&os.ModeSymlink != 0 || readEdgeFile(t, backups[0]) != ubuntuNginxConf {
		t.Fatalf("бэкап должен быть копией содержимого цели: %v, %v", info, err)
	}
	f.assertNoTempFiles(t)
}

// TestApplyEdgeCheckReportsNginxEvents: --check печатает значения с учётом контекста (connections только из events,
// nofile только из главного контекста), будущую правку и отказ; nginx.conf не меняется.
func TestApplyEdgeCheckReportsNginxEvents(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	tmpEnv := filepath.Join(tmp, "server.env")
	content := "EMTDS_PUBLIC_IP=203.0.113.55\nEMTDS_TRUSTED_PROXIES=172.18.0.1/32\nEMTDS_ADMIN_TRUSTED_PROXIES=172.18.0.1/32\nEMTDS_ENGINE_PUBLISH=127.0.0.1:18080:8080\n"
	if err := os.WriteFile(tmpEnv, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	stubPath := edgeStubEnv(t)[0]
	run := func(name, conf string) (string, string) {
		t.Helper()
		confPath := filepath.Join(tmp, name)
		if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bash, filepath.Join(dir, "apply-edge.sh"), "--check", "--tune-events")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "EMTDS_SERVER_ENV="+tmpEnv, "EMTDS_NGINX_CONF="+confPath, stubPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("--check --tune-events: %v\n%s", err, out)
		}
		return string(out), confPath
	}

	cases := []struct {
		name, conf string
		want       []string
	}{
		{
			name: "ubuntu.conf",
			conf: ubuntuNginxConf,
			want: []string{
				"check mode: nginx.conf worker_connections=768 worker_rlimit_nofile=не задано",
				"--tune-events изменит worker_connections 768 -> 4096, worker_rlimit_nofile не задано -> 16384",
			},
		},
		{
			name: "http.conf",
			conf: "http {\n\tworker_connections 99999;\n\tlog_format x '{ worker_rlimit_nofile 7; }';\n}\nevents {\n}\n",
			want: []string{
				"check mode: nginx.conf worker_connections=не задано worker_rlimit_nofile=не задано",
				"--tune-events изменит worker_connections не задано -> 4096",
			},
		},
		{
			name: "big.conf",
			conf: "worker_rlimit_nofile 65535;\nevents {\n\tworker_connections 10000;\n}\n",
			want: []string{
				"worker_connections=10000 worker_rlimit_nofile=65535",
				"--tune-events ничего не изменит",
			},
		},
		{
			name: "broken.conf",
			conf: "http {\n}\n",
			want: []string{"блок events не найден", "--tune-events остановится"},
		},
		{
			name: "include.conf",
			conf: "include " + filepath.Join(tmp, "inc", "*.conf") + ";\nevents {\n\tworker_connections 100;\n}\n",
			want: []string{"worker_connections=100", "заданы в подключаемых файлах", "hit.conf", "--tune-events остановится"},
		},
	}
	if err := os.MkdirAll(filepath.Join(tmp, "inc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "inc", "hit.conf"), []byte("worker_rlimit_nofile 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, confPath := run(tc.name, tc.conf)
			for _, frag := range tc.want {
				if !strings.Contains(out, frag) {
					t.Fatalf("--check: нет %q:\n%s", frag, out)
				}
			}
			if after := readEdgeFile(t, confPath); after != tc.conf {
				t.Fatalf("--check изменил nginx.conf:\n%s", after)
			}
		})
	}
	cmd := exec.Command(bash, filepath.Join(dir, "apply-edge.sh"), "--check")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "EMTDS_SERVER_ENV="+tmpEnv, "EMTDS_NGINX_CONF="+filepath.Join(tmp, "missing.conf"))
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "не найден") {
		t.Fatalf("--check без nginx.conf: %v\n%s", err, out)
	}
}

// TestApplyEdgeTuneEventsUsage: справка описывает --tune-events, неизвестный флаг даёт код 2.
func TestApplyEdgeTuneEventsUsage(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bash, filepath.Join(dir, "apply-edge.sh"), "--help").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--tune-events") || !strings.Contains(string(out), "--check") {
		t.Fatalf("справка: %v\n%s", err, out)
	}
	err = exec.Command(bash, filepath.Join(dir, "apply-edge.sh"), "--no-such-flag").Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		t.Fatalf("неизвестный флаг: ждали код 2, получили %v", err)
	}
}

// requireOpenSSL пропускает тест, если openssl нет в PATH.
func requireOpenSSL(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl не найден в PATH")
	}
}

// opensslOut запускает openssl с аргументами args и возвращает stdout; код выхода возвращается отдельно.
func opensslOut(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("openssl", args...)
	var o bytes.Buffer
	cmd.Stdout = &o
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatal(err)
		}
		return o.String(), ee.ExitCode()
	}
	return o.String(), 0
}

// TestApplyEdgeEnsureIPCertCreates: без файлов ensure_ip_cert создаёт пару: ключ 0600, сертификат 0644,
// SAN на нужный IP, срок около 10 лет, ключ подходит к сертификату.
func TestApplyEdgeEnsureIPCertCreates(t *testing.T) {
	requireOpenSSL(t)
	state := t.TempDir()
	edgeSourced(t, "ensure_ip_cert 203.0.113.55\n", []string{"EMTDS_INSTALL_STATE_DIR=" + state})
	crt := filepath.Join(state, "tls", "ip.crt")
	key := filepath.Join(state, "tls", "ip.key")
	for path, want := range map[string]os.FileMode{key: 0o600, crt: 0o644} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("файл не создан: %v", err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("права %s: %v, ждали %v", path, info.Mode().Perm(), want)
		}
	}
	text, code := opensslOut(t, "x509", "-in", crt, "-noout", "-text")
	if code != 0 || !strings.Contains(text, "IP Address:203.0.113.55") {
		t.Fatalf("в сертификате нет SAN IP (код %d):\n%s", code, text)
	}
	if _, code := opensslOut(t, "x509", "-in", crt, "-noout", "-checkend", "315000000"); code != 0 {
		t.Fatal("сертификат истекает раньше, чем через 10 лет минус несколько дней")
	}
	if _, code := opensslOut(t, "x509", "-in", crt, "-noout", "-checkend", "316000000"); code == 0 {
		t.Fatal("срок сертификата больше 10 лет")
	}
	certKey, _ := opensslOut(t, "x509", "-in", crt, "-noout", "-pubkey")
	privKey, _ := opensslOut(t, "pkey", "-in", key, "-pubout")
	if certKey == "" || certKey != privKey {
		t.Fatal("ключ не подходит к сертификату")
	}
}

// writeCertPair создаёт в каталоге dir настоящую пару ip.crt и ip.key (самоподписанный сертификат на
// 30 дней для адреса ip) и возвращает пути сертификата и ключа.
func writeCertPair(t *testing.T, dir, ip string) (crt, key string) {
	t.Helper()
	requireOpenSSL(t)
	mustMkdir(t, dir)
	crt, key = filepath.Join(dir, "ip.crt"), filepath.Join(dir, "ip.key")
	out, err := exec.Command("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-sha256", "-days", "30",
		"-subj", "/CN="+ip, "-keyout", key, "-out", crt).CombinedOutput()
	if err != nil {
		t.Fatalf("openssl req: %v\n%s", err, out)
	}
	for path, mode := range map[string]os.FileMode{crt: 0o644, key: 0o600} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	return crt, key
}

// dirNames возвращает отсортированные имена записей каталога dir (включая скрытые).
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// assertCertPair проверяет, что в каталоге tls лежит ровно пара ip.crt и ip.key нужных прав и ключ
// подходит к сертификату: временных файлов не осталось.
func assertCertPair(t *testing.T, tls string) {
	t.Helper()
	if got := strings.Join(dirNames(t, tls), ","); got != "ip.crt,ip.key" {
		t.Fatalf("в %s ждали только ip.crt и ip.key, найдено: %s", tls, got)
	}
	for name, want := range map[string]os.FileMode{"ip.key": 0o600, "ip.crt": 0o644} {
		info, err := os.Stat(filepath.Join(tls, name))
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("%s: права %v (%v), ждали %v", name, info, err, want)
		}
	}
	certKey, code := opensslOut(t, "x509", "-in", filepath.Join(tls, "ip.crt"), "-noout", "-pubkey")
	privKey, _ := opensslOut(t, "pkey", "-in", filepath.Join(tls, "ip.key"), "-pubout")
	if code != 0 || certKey == "" || certKey != privKey {
		t.Fatal("ключ не подходит к сертификату или сертификат не читается как x509")
	}
}

// TestApplyEdgeEnsureIPCertKeepsExisting: валидная пара не пересоздаётся (байты те же, временных файлов
// нет), но права ключа приводятся к 0600.
func TestApplyEdgeEnsureIPCertKeepsExisting(t *testing.T) {
	state := t.TempDir()
	tls := filepath.Join(state, "tls")
	crt, key := writeCertPair(t, tls, "203.0.113.55")
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	crtBefore, keyBefore := readEdgeFile(t, crt), readEdgeFile(t, key)
	edgeSourced(t, "ensure_ip_cert 203.0.113.55\n", []string{"EMTDS_INSTALL_STATE_DIR=" + state})
	if readEdgeFile(t, crt) != crtBefore || readEdgeFile(t, key) != keyBefore {
		t.Fatal("валидная пара изменена")
	}
	assertCertPair(t, tls)
}

// TestApplyEdgeEnsureIPCertRecreatesBroken: пара, которую нельзя использовать, создаётся заново целиком:
// пустой или битый сертификат, пустой или потерянный ключ, потерянный сертификат, ключ от другого сертификата.
func TestApplyEdgeEnsureIPCertRecreatesBroken(t *testing.T) {
	requireOpenSSL(t)
	cases := []struct {
		name  string
		spoil func(t *testing.T, crt, key string)
	}{
		{"пустой сертификат", func(t *testing.T, crt, _ string) { mustWrite(t, crt, "", 0o644) }},
		{"битый сертификат", func(t *testing.T, crt, _ string) { mustWrite(t, crt, "CRT-SENTINEL\n", 0o644) }},
		{"пустой ключ", func(t *testing.T, _, key string) { mustWrite(t, key, "", 0o600) }},
		{"нет ключа", func(t *testing.T, _, key string) { _ = os.Remove(key) }},
		{"нет сертификата", func(t *testing.T, crt, _ string) { _ = os.Remove(crt) }},
		{"чужой ключ", func(t *testing.T, _, key string) {
			_, other := writeCertPair(t, filepath.Join(t.TempDir(), "other"), "203.0.113.99")
			mustWrite(t, key, readEdgeFile(t, other), 0o600)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			tls := filepath.Join(state, "tls")
			crt, key := writeCertPair(t, tls, "203.0.113.55")
			tc.spoil(t, crt, key)
			edgeSourced(t, "ensure_ip_cert 203.0.113.55\n", []string{"EMTDS_INSTALL_STATE_DIR=" + state})
			assertCertPair(t, tls)
			if text, code := opensslOut(t, "x509", "-in", crt, "-noout", "-text"); code != 0 || !strings.Contains(text, "IP Address:203.0.113.55") {
				t.Fatalf("новый сертификат без SAN IP (код %d):\n%s", code, text)
			}
		})
	}
}

// opensslReqStub кладёт в PATH заглушку openssl: команда req пишет мусор в файлы -keyout и -out и
// выходит с кодом STUB_REQ_RC, остальные команды выполняет настоящий openssl. Возвращает окружение запуска.
func opensslReqStub(t *testing.T, state string) []string {
	t.Helper()
	requireOpenSSL(t)
	realSSL, err := exec.LookPath("openssl")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "bin")
	writeStub(t, bin, "openssl", `if [ "$1" = req ]; then
	while [ $# -gt 0 ]; do
		case "$1" in -keyout) k=$2;; -out) c=$2;; esac
		shift
	done
	echo partial >"$k"
	echo partial >"$c"
	exit "${STUB_REQ_RC:-0}"
fi
exec `+realSSL+` "$@"
`)
	return []string{
		"EMTDS_INSTALL_STATE_DIR=" + state,
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
}

// TestApplyEdgeEnsureIPCertFails: сбой openssl и «успешный» openssl с неверным результатом дают код 1,
// не оставляют временных файлов и не трогают прежние файлы (замена атомарна).
func TestApplyEdgeEnsureIPCertFails(t *testing.T) {
	state := t.TempDir()
	tls := filepath.Join(state, "tls")
	env := opensslReqStub(t, state)
	_, errOut, code := edgeRun(t, "ensure_ip_cert 203.0.113.55\n", append(env, "STUB_REQ_RC=1"))
	if code != 1 || !strings.Contains(errOut, "не удалось создать самоподписанный сертификат") {
		t.Fatalf("сбой openssl: код %d\n%s", code, errOut)
	}
	if names := dirNames(t, tls); len(names) != 0 {
		t.Fatalf("после сбоя в %s остались файлы: %v", tls, names)
	}

	mustWrite(t, filepath.Join(tls, "ip.crt"), "OLD-CRT\n", 0o644)
	mustWrite(t, filepath.Join(tls, "ip.key"), "OLD-KEY\n", 0o600)
	_, errOut, code = edgeRun(t, "ensure_ip_cert 203.0.113.55\n", append(env, "STUB_REQ_RC=0"))
	if code != 1 || !strings.Contains(errOut, "не удалось создать самоподписанный сертификат") {
		t.Fatalf("openssl с неверным результатом: код %d\n%s", code, errOut)
	}
	if readEdgeFile(t, filepath.Join(tls, "ip.crt")) != "OLD-CRT\n" || readEdgeFile(t, filepath.Join(tls, "ip.key")) != "OLD-KEY\n" {
		t.Fatal("прежние файлы изменены при неудачной замене")
	}
	if got := strings.Join(dirNames(t, tls), ","); got != "ip.crt,ip.key" {
		t.Fatalf("остались временные файлы: %s", got)
	}
}

// TestApplyEdgeEnsureIPCertMoveOrder: готовые временные файлы переименовываются сначала ключ, затем сертификат.
func TestApplyEdgeEnsureIPCertMoveOrder(t *testing.T) {
	data, err := os.ReadFile("apply-edge.sh")
	if err != nil {
		t.Fatal(err)
	}
	body := shellFunc(t, string(data), "ensure_ip_cert")
	key := strings.Index(body, `mv -f "$tmp_key" "$key"`)
	crt := strings.Index(body, `mv -f "$tmp_crt" "$crt"`)
	if key < 0 || crt < 0 || key > crt {
		t.Fatalf("ensure_ip_cert: ключ должен переименовываться раньше сертификата (%d, %d)", key, crt)
	}
	if strings.Contains(body, `-keyout "$key"`) || strings.Contains(body, `-out "$crt"`) {
		t.Fatal("ensure_ip_cert: openssl не должен писать сразу в рабочие файлы")
	}
}

// TestApplyEdgeCertBeforeNginxCheck: полный запуск создаёт сертификат до nginx -t, --check ничего не пишет.
func TestApplyEdgeCertBeforeNginxCheck(t *testing.T) {
	requireOpenSSL(t)
	checkOnly := newEdgeApplyFixture(t, ubuntuNginxConf)
	if _, errOut, code := checkOnly.run(t, nil, "--check"); code != 0 {
		t.Fatalf("--check: код %d\n%s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(checkOnly.root, "state", "tls")); err == nil {
		t.Fatal("--check создал каталог сертификата")
	}
	f := newEdgeApplyFixture(t, ubuntuNginxConf)
	f.stub(t, "nginx", "[ -f \"$STUB_CERT\" ] && [ -f \"$STUB_KEY\" ] || { echo 'нет сертификата до nginx -t' >&2; exit 1; }\nexit 0\n")
	env := []string{
		"STUB_CERT=" + filepath.Join(f.root, "state", "tls", "ip.crt"),
		"STUB_KEY=" + filepath.Join(f.root, "state", "tls", "ip.key"),
	}
	if _, errOut, code := f.run(t, env); code != 0 {
		t.Fatalf("apply: код %d\n%s", code, errOut)
	}
}
