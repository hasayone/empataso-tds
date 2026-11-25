package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// edgeSyncBody переопределяет пути на каталог $2, подменяет проверку root и чтение тома (копия
// каталога STUB_CERTS вместо docker run) и запускает main --sync-certs.
const edgeSyncBody = `EDGE_CONF_D=$2/conf.d
EDGE_NGINX_EMTDS_DIR=$2/nginx-emtds
EDGE_BACKUP_ROOT=$2/backup
EDGE_NGINX_CONF=$2/nginx.conf
edge_is_root() { return 0; }
edge_export_certs() { cp -a "$STUB_CERTS"/. "$2"/; }
main --sync-certs
`

// writeDomainCert создаёт dir/name/fullchain.pem и privkey.pem: самоподписанный сертификат P-256 на
// 90 дней с SAN DNS:name. Возвращает каталог домена.
func writeDomainCert(t *testing.T, dir, name string) string {
	t.Helper()
	requireOpenSSL(t)
	d := filepath.Join(dir, name)
	mustMkdir(t, d)
	out, err := exec.Command("openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
		"-nodes", "-sha256", "-days", "90", "-subj", "/CN="+name, "-addext", "subjectAltName=DNS:"+name,
		"-keyout", filepath.Join(d, "privkey.pem"), "-out", filepath.Join(d, "fullchain.pem")).CombinedOutput()
	if err != nil {
		t.Fatalf("openssl req %s: %v\n%s", name, err, out)
	}
	return d
}

// reloads считает перезагрузки nginx в журнале заглушки systemctl.
func (f *edgeApplyFixture) reloads(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, "stub.log"))
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "reload nginx")
}

// certBackups возвращает каталоги бэкапов --sync-certs.
func (f *edgeApplyFixture) certBackups(t *testing.T) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(f.root, "backup", "*-certs"))
	return m
}

// installedDomains возвращает имена каталогов в state/tls/domains.
func (f *edgeApplyFixture) installedDomains(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.root, "state", "tls", "domains"))
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}

// TestApplyEdgeSyncCerts: --sync-certs ставит выпущенный сертификат домена в каталог состояния и
// отдаёт его nginx по имени; без изменений ничего не пишет и не перезагружает; плохие имена, чужие
// пары и ссылки не принимает; недописанная пара при продлении оставляет прежнюю; убранный домен
// уходит; отказ nginx -t откатывает всё и не повторяется час; без engine-site.conf отказывает.
func TestApplyEdgeSyncCerts(t *testing.T) {
	requireOpenSSL(t)
	f := newEdgeApplyFixture(t, ubuntuNginxConf)
	if _, errOut, code := f.run(t, nil); code != 0 {
		t.Fatalf("полный apply: код %d\n%s", code, errOut)
	}
	sniConf := filepath.Join(f.root, "conf.d", "emtds-sni.conf")
	if s := readEdgeFile(t, sniConf); strings.Contains(s, "server {") {
		t.Fatalf("без сертификатов emtds-sni.conf должен быть пустым:\n%s", s)
	}
	domains := filepath.Join(f.root, "state", "tls", "domains")
	src := filepath.Join(f.root, "volume-live")
	writeDomainCert(t, src, "direct.example")
	env := append(append([]string{}, f.env...), "STUB_CERTS="+src, "EMTDS_STATE_VOLUME_NAME=emtds-app_emtds_state")
	sync := func(extra ...string) (string, string, int) {
		t.Helper()
		return edgeRun(t, edgeSyncBody, append(append([]string{}, env...), extra...), f.root)
	}

	base := f.reloads(t)
	out, errOut, code := sync()
	if code != 0 {
		t.Fatalf("sync: код %d\n%s\n%s", code, out, errOut)
	}
	sni := readEdgeFile(t, sniConf)
	wantCrt := "ssl_certificate " + filepath.Join(f.root, "state", "tls", "domains", "direct.example", "fullchain.pem") + ";"
	if !strings.Contains(sni, "server_name direct.example;") || !strings.Contains(sni, wantCrt) || !strings.Contains(sni, "include /etc/nginx/emtds/engine-site.conf;") || strings.Contains(sni, "__") {
		t.Fatalf("emtds-sni.conf:\n%s", sni)
	}
	if readEdgeFile(t, filepath.Join(domains, "direct.example", "privkey.pem")) != readEdgeFile(t, filepath.Join(src, "direct.example", "privkey.pem")) {
		t.Fatal("ключ не скопирован")
	}
	if info, err := os.Stat(filepath.Join(domains, "direct.example", "privkey.pem")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("права ключа %v %v", info, err)
	}
	if f.reloads(t) != base+1 || len(f.certBackups(t)) != 1 || !strings.Contains(out, "сертификаты доменов: direct.example") {
		t.Fatalf("перезагрузки %d (база %d), бэкапы %v\n%s", f.reloads(t), base, f.certBackups(t), out)
	}
	if strings.Contains(out+errOut, "PRIVATE KEY") {
		t.Fatal("ключ попал в вывод")
	}

	if out, errOut, code := sync(); code != 0 || out != "" || f.reloads(t) != base+1 || len(f.certBackups(t)) != 1 {
		t.Fatalf("повтор без изменений: код %d, вывод %q %q, перезагрузки %d", code, out, errOut, f.reloads(t))
	}

	writeDomainCert(t, src, "admin.direct.example")
	bad := writeDomainCert(t, src, "bad.example")
	other := writeDomainCert(t, t.TempDir(), "x.example")
	mustWrite(t, filepath.Join(bad, "privkey.pem"), readEdgeFile(t, filepath.Join(other, "privkey.pem")), 0o600)
	if err := os.Symlink(filepath.Join(src, "direct.example"), filepath.Join(src, "link.example")); err != nil {
		t.Fatal(err)
	}
	if _, _, code := sync(); code != 0 || f.reloads(t) != base+1 || f.installedDomains(t) != "direct.example" {
		t.Fatalf("плохие сертификаты изменили набор: код %d, %s", code, f.installedDomains(t))
	}

	mustWrite(t, filepath.Join(src, "direct.example", "privkey.pem"), readEdgeFile(t, filepath.Join(other, "privkey.pem")), 0o600)
	kept := readEdgeFile(t, filepath.Join(domains, "direct.example", "fullchain.pem"))
	if _, _, code := sync(); code != 0 || f.reloads(t) != base+1 || readEdgeFile(t, filepath.Join(domains, "direct.example", "fullchain.pem")) != kept {
		t.Fatal("недописанная пара заменила рабочую")
	}

	for _, n := range []string{"direct.example", "admin.direct.example", "bad.example", "link.example"} {
		if err := os.RemoveAll(filepath.Join(src, n)); err != nil {
			t.Fatal(err)
		}
	}
	writeDomainCert(t, src, "other.example")
	if _, errOut, code := sync(); code != 0 || f.reloads(t) != base+2 || f.installedDomains(t) != "other.example" {
		t.Fatalf("замена набора: код %d, %s\n%s", code, f.installedDomains(t), errOut)
	}
	if sni := readEdgeFile(t, sniConf); strings.Contains(sni, "direct.example") || !strings.Contains(sni, "server_name other.example;") {
		t.Fatalf("emtds-sni.conf после замены:\n%s", sni)
	}

	writeDomainCert(t, src, "third.example")
	before := readEdgeFile(t, sniConf)
	if _, _, code := sync("STUB_NGINX_T_RC=1"); code == 0 {
		t.Fatal("ждали отказ при сбое nginx -t")
	}
	if f.installedDomains(t) != "other.example" || readEdgeFile(t, sniConf) != before || f.reloads(t) != base+2 {
		t.Fatalf("откат после nginx -t: %s\n%s", f.installedDomains(t), readEdgeFile(t, sniConf))
	}
	backups := len(f.certBackups(t))
	if _, _, code := sync(); code != 0 || f.reloads(t) != base+2 || len(f.certBackups(t)) != backups {
		t.Fatalf("отказанный набор повторён раньше часа: код %d", code)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(f.root, "state", "tls", "sync-refused"), old, old); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := sync(); code != 0 || f.installedDomains(t) != "other.example,third.example" || f.reloads(t) != base+3 {
		t.Fatalf("через час отказанный набор не повторён: код %d, %s\n%s", code, f.installedDomains(t), errOut)
	}
	if _, err := os.Stat(filepath.Join(f.root, "state", "tls", "sync-refused")); !os.IsNotExist(err) {
		t.Fatal("метка отказа осталась после успешной установки")
	}

	if _, errOut, code := f.run(t, nil); code != 0 {
		t.Fatalf("повторный полный apply: код %d\n%s", code, errOut)
	}
	if sni := readEdgeFile(t, sniConf); !strings.Contains(sni, "server_name other.example;") || !strings.Contains(sni, "server_name third.example;") {
		t.Fatalf("полный apply потерял vhost доменов:\n%s", sni)
	}

	if err := os.Remove(filepath.Join(f.root, "nginx-emtds", "engine-site.conf")); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := sync(); code != 1 || !strings.Contains(errOut, "engine-site.conf") {
		t.Fatalf("без engine-site.conf: код %d\n%s", code, errOut)
	}
}

// TestApplyEdgeSyncCertsWithoutAdmin: без контейнера emtds-admin (том не найден) синхронизация
// ничего не делает и не считается ошибкой.
func TestApplyEdgeSyncCertsWithoutAdmin(t *testing.T) {
	requireOpenSSL(t)
	f := newEdgeApplyFixture(t, ubuntuNginxConf)
	if _, errOut, code := f.run(t, nil); code != 0 {
		t.Fatalf("полный apply: код %d\n%s", code, errOut)
	}
	f.stub(t, "docker", "exit 1\n")
	base := f.reloads(t)
	body := strings.Replace(edgeSyncBody, "edge_export_certs() { cp -a \"$STUB_CERTS\"/. \"$2\"/; }\n", "", 1)
	if out, errOut, code := edgeRun(t, body, f.env, f.root); code != 0 || out != "" || f.reloads(t) != base {
		t.Fatalf("без admin: код %d %q %q", code, out, errOut)
	}
}

// TestEdgeExportScript: скрипт чтения тома копирует только обычные файлы пар, режет размер и не
// идёт по ссылкам: ни по каталогу домена, ни по файлу, ни по самому каталогу live.
func TestEdgeExportScript(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "v", "tls", "live")
	out := filepath.Join(root, "out")
	mustMkdir(t, out)
	mustWrite(t, filepath.Join(live, "good.example", "fullchain.pem"), "CRT\n", 0o644)
	mustWrite(t, filepath.Join(live, "good.example", "privkey.pem"), "KEY\n", 0o600)
	mustWrite(t, filepath.Join(live, "big.example", "fullchain.pem"), strings.Repeat("x", 100000), 0o644)
	mustWrite(t, filepath.Join(live, "big.example", "privkey.pem"), "KEY\n", 0o600)
	mustWrite(t, filepath.Join(live, "half.example", "fullchain.pem"), "CRT\n", 0o644)
	mustWrite(t, filepath.Join(live, "linkfile.example", "privkey.pem"), "KEY\n", 0o600)
	secret := filepath.Join(root, "secret")
	mustWrite(t, secret, "SECRET\n", 0o600)
	if err := os.Symlink(secret, filepath.Join(live, "linkfile.example", "fullchain.pem")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(live, "good.example"), filepath.Join(live, "linkdir.example")); err != nil {
		t.Fatal(err)
	}
	edgeSourced(t, `sh -c "$EDGE_EXPORT_SCRIPT" sh "$2" "$3"`, nil, live, out)
	if got := strings.Join(dirNames(t, out), ","); got != "big.example,good.example" {
		t.Fatalf("скопированы %s, ждали big.example,good.example", got)
	}
	if info, err := os.Stat(filepath.Join(out, "big.example", "fullchain.pem")); err != nil || info.Size() != 65536 {
		t.Fatalf("размер не ограничен: %v %v", info, err)
	}

	linked := filepath.Join(root, "v2", "tls", "live")
	mustMkdir(t, filepath.Dir(linked))
	if err := os.Symlink(live, linked); err != nil {
		t.Fatal(err)
	}
	out2 := filepath.Join(root, "out2")
	mustMkdir(t, out2)
	edgeSourced(t, `sh -c "$EDGE_EXPORT_SCRIPT" sh "$2" "$3"`, nil, linked, out2)
	if got := dirNames(t, out2); len(got) != 0 {
		t.Fatalf("каталог live по ссылке прочитан: %v", got)
	}
}

// TestEdgeCertNameOK: правило имени то же, что certNameOK в admin: домен из двух и более меток,
// верхняя метка с буквы, без admin., без порта, IP и звёздочки.
func TestEdgeCertNameOK(t *testing.T) {
	cases := map[string]bool{
		"example.com": true, "a.b.example.com": true, "xn--80ak6aa92e.com": true, "my-site.io": true,
		"admin.example.com": false, "203.0.113.10": false, "example.com:8443": false, "localhost": false,
		"*.example.com": false, "Example.com": false, "-bad.com": false,
	}
	var script strings.Builder
	for name := range cases {
		script.WriteString("if edge_cert_name_ok '" + name + "'; then echo '" + name + " yes'; else echo '" + name + " no'; fi\n")
	}
	out := edgeSourced(t, script.String(), nil)
	for name, want := range cases {
		line := name + " no"
		if want {
			line = name + " yes"
		}
		if !strings.Contains(out, line+"\n") {
			t.Fatalf("edge_cert_name_ok %q: ждали %q\n%s", name, line, out)
		}
	}
}
