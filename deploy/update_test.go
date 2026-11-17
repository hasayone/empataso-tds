package deploy

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/cron"
)

// TestUpdateSyntax проверяет синтаксис bash, shebang и отсутствие CRLF.
func TestUpdateSyntax(t *testing.T) {
	bash := requireBash(t)
	data, err := os.ReadFile("update.sh")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("\r")) {
		t.Fatal("update.sh содержит CR")
	}
	if !bytes.HasPrefix(data, []byte("#!/usr/bin/env bash\n")) {
		t.Fatalf("первая строка должна быть #!/usr/bin/env bash")
	}
	if !bytes.Contains(data, []byte("set -Eeuo pipefail")) || !bytes.Contains(data, []byte("umask 077")) {
		t.Fatal("нет set -Eeuo pipefail или umask 077")
	}
	if !bytes.HasSuffix(bytes.TrimRight(data, "\n"), []byte(`{ main "$@"; exit $?; }`)) {
		t.Fatal("последняя строка должна быть { main \"$@\"; exit $?; }")
	}
	if out, err := exec.Command(bash, "-n", "update.sh").CombinedOutput(); err != nil {
		t.Fatalf("bash -n update.sh: %v\n%s", err, out)
	}
}

// TestUpdateSelfTest проверяет --self-test без Docker и сети.
func TestUpdateSelfTest(t *testing.T) {
	bash := requireBash(t)
	out, err := exec.Command(bash, "update.sh", "--self-test").CombinedOutput()
	if err != nil {
		t.Fatalf("--self-test: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "self-test ok") {
		t.Fatalf("нет self-test ok:\n%s", out)
	}
}

// TestUpdateForbiddenSubstrings проверяет отсутствие запрещённых фрагментов, включая комментарии.
func TestUpdateForbiddenSubstrings(t *testing.T) {
	data, err := os.ReadFile("update.sh")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"compose down", "volume rm", "docker rm ", "system prune", "network prune", "image prune",
		"set -x", "empataso", "-p emtds-infra", "--project-directory", "docker.sock", "privileged",
	}
	for i, line := range strings.Split(string(data), "\n") {
		low := strings.ToLower(line)
		for _, frag := range forbidden {
			if strings.Contains(low, frag) {
				t.Errorf("строка %d содержит %q: %q", i+1, frag, line)
			}
		}
		for _, verb := range []string{"up", "restart", "stop"} {
			for _, svc := range []string{"clickhouse", "redis", "php"} {
				re := regexp.MustCompile(`\b` + verb + `\b.*\b` + svc + `\b|\b` + svc + `\b.*\b` + verb + `\b`)
				if strings.Contains(low, "compose") && re.MatchString(low) {
					t.Errorf("строка %d: %s для %s: %q", i+1, verb, svc, line)
				}
			}
		}
	}
}

// TestUpdateRequiredSubstrings проверяет обязательные фрагменты и сервисы в compose up.
func TestUpdateRequiredSubstrings(t *testing.T) {
	data, err := os.ReadFile("update.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{"up -d --no-build --force-recreate --no-deps", "--network none", "--user 10001:10001", "flock -n"} {
		if !strings.Contains(s, want) {
			t.Errorf("нет обязательного фрагмента %q", want)
		}
	}
	upAny := regexp.MustCompile(`\bup -d\b`)
	upRe := regexp.MustCompile(`up -d --no-build --force-recreate --no-deps ("\$svc"|engine|admin|cron)( |$)`)
	for i, line := range strings.Split(s, "\n") {
		if upAny.MatchString(line) && !upRe.MatchString(line) {
			t.Errorf("строка %d: up -d вне разрешённой формы: %q", i+1, line)
		}
	}
	runRe := regexp.MustCompile(`docker run --rm( -i)? --network none --user 10001:10001`)
	for i, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "docker run") && !strings.HasPrefix(strings.TrimSpace(line), "#") && !runRe.MatchString(line) {
			t.Errorf("строка %d: docker run без --network none --user 10001:10001: %q", i+1, line)
		}
	}
}

// TestUpdateHelpExits проверяет, что --help выходит с кодом 0 и описывает команды.
func TestUpdateHelpExits(t *testing.T) {
	bash := requireBash(t)
	out, err := exec.Command(bash, "update.sh", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("--help: %v\n%s", err, out)
	}
	for _, want := range []string{"--check", "--channel", "--rollback", "--install", "--timer", "--unsigned", "<owner>"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("--help без %q", want)
		}
	}
}

// TestUpdateCheckCommandV проверяет наличие утилит, которые тесты не подменяют.
func TestUpdateCheckCommandV(t *testing.T) {
	for _, name := range []string{"bash", "git", "ssh-keygen", "flock", "tar", "dd", "timeout"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Errorf("нет %s в тест-образе: %v", name, err)
		}
	}
}

// requireBash возвращает путь к bash или пропускает тест.
func requireBash(t *testing.T) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден")
	}
	return bash
}

// testEnv — песочница сценарного прогона: репозитории, фейковые бинари, каталог тома и журнал.
type testEnv struct {
	Sandbox   string
	RepoDir   string
	TDSDir    string
	FakeBin   string
	VolDir    string
	FakeState string
	Journal   string
	StateDir  string
	VarDir    string
	SignKey   string
	Script    string
}

// newTestEnv создаёт песочницу с репозиторием из одного коммита и фейковыми docker, nginx, curl, systemctl.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	requireBash(t)
	sbx := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	te := &testEnv{
		Sandbox:   sbx,
		RepoDir:   filepath.Join(sbx, "repo.git"),
		TDSDir:    filepath.Join(sbx, "tds"),
		FakeBin:   filepath.Join(sbx, "bin"),
		VolDir:    filepath.Join(sbx, "vol"),
		FakeState: filepath.Join(sbx, "fakestate"),
		Journal:   filepath.Join(sbx, "docker.journal"),
		StateDir:  filepath.Join(sbx, "etc", "emtds"),
		VarDir:    filepath.Join(sbx, "var", "lib", "emtds-update"),
		SignKey:   filepath.Join(sbx, "sign_key"),
		Script:    filepath.Join(wd, "update.sh"),
	}
	for _, d := range []string{te.FakeBin, te.VolDir, te.FakeState, te.StateDir, te.VarDir, te.TDSDir,
		filepath.Join(sbx, "run"), filepath.Join(sbx, "proc"), filepath.Join(sbx, "var", "lib", "docker")} {
		mustMkdir(t, d)
	}
	mustWrite(t, filepath.Join(sbx, "proc", "meminfo"), "MemTotal:        8192000 kB\nMemAvailable:    4096000 kB\n", 0o644)
	mustWrite(t, filepath.Join(te.StateDir, "server.env"), "EMTDS_ENGINE_PUBLISH=127.0.0.1:18080:8080\nEMTDS_ADMIN_PUBLISH=127.0.0.1:8090:8090\nEMTDS_SERVER_TOKEN=servertoken777\n", 0o600)
	mustWrite(t, filepath.Join(te.StateDir, "compose.app.yml"), "services: {}\n", 0o600)
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "signer", "-f", te.SignKey).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	te.writeFakes(t)
	te.initRepo(t)
	return te
}

// mustMkdir создаёт каталог с родителями.
func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// mustWrite записывает файл с родительскими каталогами.
func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// readFile возвращает содержимое файла или пустую строку.
func readFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// git выполняет git в рабочем дереве TDSDir и возвращает вывод без пробелов по краям.
func (te *testEnv) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", te.TDSDir, "-c", "safe.directory=" + te.TDSDir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// initRepo создаёт bare-репозиторий origin и рабочее дерево с первым коммитом.
func (te *testEnv) initRepo(t *testing.T) {
	t.Helper()
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", te.RepoDir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	te.git(t, "init", "-q", "-b", "main")
	te.git(t, "config", "user.email", "signer@example.com")
	te.git(t, "config", "user.name", "Signer")
	te.git(t, "config", "gpg.format", "ssh")
	te.git(t, "config", "user.signingKey", te.SignKey)
	te.git(t, "remote", "add", "origin", te.RepoDir)
	script, err := os.ReadFile(te.Script)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(te.TDSDir, "deploy", "update.sh"), string(script), 0o755)
	mustWrite(t, filepath.Join(te.TDSDir, "deploy", "apply-edge.sh"), fakeApplyEdgeScript, 0o755)
	mustWrite(t, filepath.Join(te.TDSDir, "deploy", "docker-compose.app.yml"), "name: emtds-app\nservices: {}\n", 0o644)
	mustWrite(t, filepath.Join(te.TDSDir, ".gitignore"), ".env\n", 0o644)
	mustWrite(t, filepath.Join(te.TDSDir, ".env"), "CLICKHOUSE_PASSWORD=supersecretpassword123\nSHORT=abc\n", 0o600)
	te.commit(t, "init", false, nil)
	te.git(t, "push", "-q", "-u", "origin", "main")
}

// commit добавляет файлы и создаёт коммит (подписанный SSH-ключом, если sign).
func (te *testEnv) commit(t *testing.T, msg string, sign bool, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		mustWrite(t, filepath.Join(te.TDSDir, name), content, 0o644)
	}
	te.git(t, "add", "-A")
	if sign {
		te.git(t, "commit", "-q", "-S", "-m", msg)
	} else {
		te.git(t, "commit", "-q", "--no-gpg-sign", "-m", msg)
	}
	return te.git(t, "rev-parse", "HEAD")
}

// tag создаёт аннотированный тег (подписанный, если sign) и пушит его в origin.
func (te *testEnv) tag(t *testing.T, name, sha string, sign bool) {
	t.Helper()
	if sign {
		te.git(t, "tag", "-s", "-m", "release "+name, name, sha)
	} else {
		te.git(t, "tag", "-a", "-m", "release "+name, name, sha)
	}
	te.git(t, "push", "-q", "origin", "refs/tags/"+name)
}

// advance публикует в origin/main новый коммит и возвращает рабочее дерево на прежний HEAD.
func (te *testEnv) advance(t *testing.T, sign bool, files map[string]string) (prev, target string) {
	t.Helper()
	prev = te.git(t, "rev-parse", "HEAD")
	if files == nil {
		files = map[string]string{fmt.Sprintf("f%d.txt", time.Now().UnixNano()): "x\n"}
	}
	target = te.commit(t, "feat: next", sign, files)
	te.git(t, "push", "-q", "-f", "origin", "HEAD:main")
	te.git(t, "checkout", "-q", "--detach", prev)
	return prev, target
}

// writeUpdateEnv записывает update.env песочницы.
func (te *testEnv) writeUpdateEnv(t *testing.T, channel string) {
	t.Helper()
	mustWrite(t, filepath.Join(te.StateDir, "update.env"), fmt.Sprintf(
		"# test\nEMTDS_UPDATE_REPO=%s\nEMTDS_UPDATE_CHANNEL=%s\nEMTDS_UPDATE_CHECK_HOURS=1\nEMTDS_UPDATE_KEEP_IMAGES=3\nEMTDS_UPDATE_KEEP_BACKUPS=2\nTDS_DIR=%s\n",
		te.RepoDir, channel, te.TDSDir), 0o600)
}

// writeAllowedSigners включает проверку подписи ключом песочницы.
func (te *testEnv) writeAllowedSigners(t *testing.T) {
	t.Helper()
	pub := strings.TrimSpace(readFile(te.SignKey + ".pub"))
	mustWrite(t, filepath.Join(te.StateDir, "allowed_signers"), "signer@example.com namespaces=\"git\" "+pub+"\n", 0o600)
}

// env возвращает окружение запуска update.sh в песочнице с дополнительными переменными.
func (te *testEnv) env(extra ...string) []string {
	env := []string{
		"EMTDS_UPDATE_SANDBOX=" + te.Sandbox,
		"EMTDS_UPDATE_WAIT_SCALE=0",
		"EMTDS_INSTALL_STATE_DIR=" + te.StateDir,
		"PATH=" + te.FakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + te.Sandbox,
		"LANG=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"FAKE_DOCKER_JOURNAL=" + te.Journal,
		"FAKE_DOCKER_STATE=" + te.FakeState,
		"FAKE_DOCKER_VOLDIR=" + te.VolDir,
		"FAKE_TDS_DIR=" + te.TDSDir,
	}
	return append(env, extra...)
}

// run запускает update.sh и возвращает объединённый вывод и код выхода.
func (te *testEnv) run(t *testing.T, extra []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{te.Script}, args...)...)
	cmd.Env = te.env(extra...)
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &ee):
		return string(out), ee.ExitCode()
	default:
		t.Fatalf("запуск update.sh: %v", err)
		return "", -1
	}
}

// mustRun запускает update.sh и требует код 0.
func (te *testEnv) mustRun(t *testing.T, extra []string, args ...string) string {
	t.Helper()
	out, code := te.run(t, extra, args...)
	if code != 0 {
		t.Fatalf("update.sh %v: код %d\n%s\nжурнал:\n%s", args, code, out, readFile(te.Journal))
	}
	return out
}

// mustFail запускает update.sh и требует ненулевой код и фрагмент в выводе.
func (te *testEnv) mustFail(t *testing.T, want string, extra []string, args ...string) string {
	t.Helper()
	out, code := te.run(t, extra, args...)
	if code == 0 {
		t.Fatalf("update.sh %v: ожидали отказ, код 0\n%s", args, out)
	}
	if want != "" && !strings.Contains(out, want) {
		t.Fatalf("update.sh %v: нет %q в выводе:\n%s", args, want, out)
	}
	return out
}

// journal возвращает журнал вызовов фейковых бинарей.
func (te *testEnv) journal() string {
	return readFile(te.Journal)
}

// assertOrder проверяет, что шаги встречаются в журнале в заданном порядке.
func (te *testEnv) assertOrder(t *testing.T, steps ...string) {
	t.Helper()
	j := te.journal()
	pos := 0
	for _, s := range steps {
		i := strings.Index(j[pos:], s)
		if i < 0 {
			t.Fatalf("журнал: нет %q после позиции %d\n%s", s, pos, j)
		}
		pos += i + len(s)
	}
}

// count возвращает число строк журнала, начинающихся с prefix.
func (te *testEnv) count(prefix string) int {
	n := 0
	for _, l := range strings.Split(te.journal(), "\n") {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// state читает state.json тома как cron.StateFile.
func (te *testEnv) state(t *testing.T) cron.StateFile {
	t.Helper()
	var sf cron.StateFile
	if err := cron.ReadUpdateJSON(filepath.Join(te.VolDir, "update", "state.json"), cron.UpdateMaxJSON, &sf); err != nil {
		t.Fatalf("state.json: %v", err)
	}
	return sf
}

// check читает check.json тома как cron.CheckFile.
func (te *testEnv) check(t *testing.T) cron.CheckFile {
	t.Helper()
	var cf cron.CheckFile
	if err := cron.ReadUpdateJSON(filepath.Join(te.VolDir, "update", "check.json"), cron.UpdateMaxJSON, &cf); err != nil {
		t.Fatalf("check.json: %v", err)
	}
	return cf
}

// runLogs возвращает склеенные полные логи прогонов <var>/runs/*.log.
func (te *testEnv) runLogs(t *testing.T) string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(te.VarDir, "runs", "*.log"))
	var sb strings.Builder
	for _, f := range files {
		sb.WriteString(readFile(f))
	}
	return sb.String()
}

// writeFakes пишет фейковые бинари; ionice и nice — только если их нет в образе.
func (te *testEnv) writeFakes(t *testing.T) {
	t.Helper()
	fakes := map[string]string{
		"docker":      fakeDockerScript,
		"systemctl":   "#!/usr/bin/env bash\necho \"SYSTEMCTL $*\" >> \"$FAKE_DOCKER_JOURNAL\"\n",
		"nginx":       "#!/usr/bin/env bash\nexit 0\n",
		"curl":        "#!/usr/bin/env bash\necho \"CURL $*\" >> \"$FAKE_DOCKER_JOURNAL\"\n",
		"ssh-keyscan": "#!/usr/bin/env bash\necho \"SSH_KEYSCAN $*\" >> \"$FAKE_DOCKER_JOURNAL\"\nprintf 'github.com %s\\n' \"$(cat \"$FAKE_KEYSCAN_PUB\")\"\n",
	}
	for _, name := range []string{"ionice", "nice"} {
		if _, err := exec.LookPath(name); err != nil {
			fakes[name] = "#!/usr/bin/env bash\nwhile [[ \"${1:-}\" == -* ]]; do case \"$1\" in -n|-c) shift 2;; *) shift;; esac; done\nexec \"$@\"\n"
		}
	}
	for name, content := range fakes {
		mustWrite(t, filepath.Join(te.FakeBin, name), content, 0o755)
	}
}

// fakeApplyEdgeScript заменяет apply-edge.sh в тестовом репозитории: пишет HEAD своего дерева в журнал.
const fakeApplyEdgeScript = `#!/usr/bin/env bash
d=$(cd "$(dirname "$0")/.." && pwd)
printf 'APPLY_EDGE head=%s\n' "$(git -C "$d" -c safe.directory="$d" rev-parse HEAD)" >> "$FAKE_DOCKER_JOURNAL"
[[ -z "${FAKE_APPLY_EDGE_FAIL:-}" ]]
`

// fakeDockerScript эмулирует docker: образы и тег emtds:local в FAKE_DOCKER_STATE, том — каталог FAKE_DOCKER_VOLDIR.
// docker run без --network none и --user 10001:10001 и compose up вне разрешённой формы завершаются кодом 97/98.
const fakeDockerScript = `#!/usr/bin/env bash
set -uo pipefail
J="$FAKE_DOCKER_JOURNAL"
S="$FAKE_DOCKER_STATE"
V="$FAKE_DOCKER_VOLDIR"
printf 'DOCKER %s\n' "$*" >> "$J"
has_image() { [[ "$1" == local ]] || grep -qxF "$1" "$S/images" 2>/dev/null; }
add_image() { has_image "$1" || printf '%s\n' "$1" >> "$S/images"; }
local_tag() { cat "$S/local" 2>/dev/null || printf 'init'; }
up_count() { cat "$S/upcount" 2>/dev/null || printf '0'; }
infra_down() {
  [[ -n "${FAKE_DOCKER_FAIL_INFRA:-}" ]] && return 0
  [[ -n "${FAKE_INFRA_DOWN_AFTER_UP:-}" && "$(up_count)" -gt 0 ]]
}
health() {
  if infra_down; then echo unhealthy; return; fi
  if [[ -n "${FAKE_UNHEALTHY_IMAGE:-}" && "$(local_tag)" == "$FAKE_UNHEALTHY_IMAGE" ]]; then echo unhealthy; return; fi
  echo healthy
}
cmd=${1:-}
shift || true
case "$cmd" in
  info) echo '{}' ;;
  inspect)
    fmt=""; target=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --format|-f) fmt=$2; shift 2;;
        *) target=$1; shift;;
      esac
    done
    case "$fmt" in
      "") if [[ "$target" == emtds:* ]]; then has_image "${target#emtds:}" || exit 1; fi; echo '[]';;
      *Mounts*) echo emtds-app_emtds_state;;
      *State.Running*) echo true;;
      *Health*) health;;
      *StartedAt*) echo 2026-11-16T10:00:00Z;;
      *) echo "sha256:$(local_tag)";;
    esac
    ;;
  run)
    all=" $* "
    if [[ "$all" != *" --network none "* || "$all" != *" --user 10001:10001 "* ]]; then
      echo "DOCKER_RUN_INSECURE $*" >> "$J"; exit 97
    fi
    entry=""; script=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --entrypoint) entry=$2; shift 2;;
        -c) script=$2; shift 2;;
        *) shift;;
      esac
    done
    if [[ "$entry" == */emtds-cron ]]; then
      printf 'heartbeat_age=%s\nbusy=%s\nversion=v0\ncommit=%s\n' "${FAKE_CRON_AGE:-5}" "${FAKE_CRON_BUSY:-0}" \
        "$(git -C "$FAKE_TDS_DIR" -c safe.directory="$FAKE_TDS_DIR" rev-parse HEAD 2>/dev/null)"
      exit 0
    fi
    mod=${script//\/v\//$V/}
    mod=${mod//\/v /$V }
    if [[ -n "${FAKE_DOCKER_FAIL_VOLRM:-}" && "$mod" == *"rm -f"* ]]; then
      echo "VOLRM_FAILED" >> "$J"; exit 1
    fi
    sh -c "$mod"
    rc=$?
    if [[ -n "${FAKE_ADMIN_READY:-}" && ! -L "$V/update" && -f "$V/update/pending" && ! -e "$V/update/ready.json" ]]; then
      printf '{"id":"%s","keitaro_idle":true,"at":"2026-11-16T10:00:00Z"}' "$(head -c 16 "$V/update/pending")" > "$V/update/ready.json"
    fi
    exit $rc
    ;;
  build)
    tag=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        -t) tag=$2; shift 2;;
        *) shift;;
      esac
    done
    echo "DOCKER_BUILD $tag" >> "$J"
    [[ -n "${FAKE_BUILD_ECHO:-}" ]] && echo "$FAKE_BUILD_ECHO"
    if [[ -n "${FAKE_DOCKER_FAIL_BUILD:-}" ]]; then echo "build failed" >&2; exit 1; fi
    add_image "${tag#emtds:}"
    ;;
  tag)
    echo "DOCKER_TAG $1 $2" >> "$J"
    has_image "${1#emtds:}" || exit 1
    add_image "${2#emtds:}"
    if [[ "$2" == emtds:local ]]; then printf '%s' "${1#emtds:}" > "$S/local"; fi
    ;;
  compose)
    envs=0; files=0
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --env-file) envs=$((envs + 1)); shift 2;;
        -f) files=$((files + 1)); shift 2;;
        *) break;;
      esac
    done
    if [[ $envs -ne 2 || $files -ne 2 ]]; then echo "COMPOSE_BAD_FLAGS $*" >> "$J"; exit 98; fi
    sub=$1; shift
    case "$sub" in
      config)
        [[ "${1:-}" == -q ]] && exit 0
        host='        host_ip: 127.0.0.1'
        [[ -n "${FAKE_COMPOSE_PUBLIC:-}" ]] && host=''
        printf 'services:\n  engine:\n    ports:\n      - mode: ingress\n%s\n        target: 8080\n        published: "18080"\n        protocol: tcp\n' "$host"
        ;;
      up)
        svc=${*: -1}
        if [[ "$*" != "-d --no-build --force-recreate --no-deps $svc" || ! "$svc" =~ ^(engine|admin|cron)$ ]]; then
          echo "COMPOSE_BAD_UP $*" >> "$J"; exit 98
        fi
        echo "DOCKER_COMPOSE_UP $svc image=$(local_tag)" >> "$J"
        echo $(( $(up_count) + 1 )) > "$S/upcount"
        ;;
    esac
    ;;
  exec)
    infra_down && exit 1
    if [[ "$*" == *redis-cli* ]]; then echo PONG; else echo Ok; fi
    ;;
  logs) printf 'clickhouse connected\nredis connected\nevents_lost=3\n' ;;
  ps)
    echo abc123
    ;;
  image)
    case "${1:-}" in
      ls) tac "$S/images" 2>/dev/null || true;;
      rm) echo "DOCKER_IMAGE_RM ${2:-}" >> "$J";;
    esac
    ;;
esac
exit 0
`

// TestUpdateScenarioA: успешное обновление main на коммит вперёд.
func TestUpdateScenarioA(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	prev, target := te.advance(t, false, nil)
	t12 := target[:12]

	te.mustRun(t, nil, "--channel", "--keitaro-idle", "--unsigned")

	te.assertOrder(t,
		"DOCKER_TAG emtds:local emtds:"+prev[:12],
		"DOCKER_BUILD emtds:"+t12,
		"APPLY_EDGE head="+target,
		"DOCKER_TAG emtds:"+t12+" emtds:local",
		"DOCKER_COMPOSE_UP engine image="+t12,
		"DOCKER_COMPOSE_UP admin image="+t12,
		"DOCKER_COMPOSE_UP cron image="+t12,
	)
	if j := te.journal(); strings.Contains(j, "DOCKER_RUN_INSECURE") || strings.Contains(j, "COMPOSE_BAD") {
		t.Fatalf("небезопасный docker run или compose:\n%s", j)
	}
	if got := te.git(t, "rev-parse", "HEAD"); got != target {
		t.Fatalf("HEAD=%s, ожидали %s", got, target)
	}

	sf := te.state(t)
	if sf.Schema != 1 || sf.Result != cron.StateOK || sf.Action != "apply" || sf.From != prev || sf.To != target {
		t.Fatalf("state.json: %+v", sf)
	}
	if sf.FinishedAt == nil || sf.Phase != "done" || sf.EventsLost != 3 || sf.CronLagging || !cron.ValidUpdateID(sf.ID) {
		t.Fatalf("state.json: %+v", sf)
	}
	for _, p := range sf.Phases {
		if !p.OK {
			t.Fatalf("фаза %s не ok", p.Name)
		}
	}
	cf := te.check(t)
	if cf.Schema != 1 || cf.Target.Commit != target || cf.Current.Commit != prev || cf.Ahead != 1 || len(cf.Commits) != 1 || cf.Signed {
		t.Fatalf("check.json: %+v", cf)
	}

	hist := readFile(filepath.Join(te.VarDir, "history"))
	if !regexp.MustCompile(`(?m)^\d+ ` + sf.ID + ` apply ` + prev + ` ` + target + ` ok $`).MatchString(hist) {
		t.Fatalf("history: %q", hist)
	}
	logPath := filepath.Join(te.VarDir, "runs", sf.ID+".log")
	if fi, err := os.Stat(logPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("runs/%s.log: %v %v", sf.ID, fi, err)
	}
	if readFile(filepath.Join(te.Sandbox, "usr", "local", "lib", "emtds", "update.sh")) != readFile(te.Script) {
		t.Fatal("копия скрипта не установлена в /usr/local/lib/emtds")
	}
	backups, _ := filepath.Glob(filepath.Join(te.Sandbox, "var", "backups", "emtds-update", "*-"+sf.ID, "volume.tar.gz"))
	if len(backups) != 1 {
		t.Fatalf("нет снимка тома в бэкапе: %v", backups)
	}
	if _, err := os.Stat(filepath.Join(te.VarDir, "build", target)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("каталог сборки не удалён: %v", err)
	}

	te.mustRun(t, nil, "--timer")
	var af cron.AgentFile
	if err := cron.ReadUpdateJSON(filepath.Join(te.VolDir, "update", "agent.json"), cron.UpdateMaxJSON, &af); err != nil {
		t.Fatalf("agent.json: %v", err)
	}
	if af.Schema != 1 || af.Channel != "main" || !af.DockerOK || !af.Inbox || af.Signing || len(af.ScriptVersion) != 12 {
		t.Fatalf("agent.json: %+v", af)
	}

	out := te.mustRun(t, nil, "--channel", "--keitaro-idle", "--unsigned")
	if !strings.Contains(out, "уже актуально") {
		t.Fatalf("повторный прогон: %s", out)
	}
}

// TestUpdateScenarioB: провал сборки — без checkout, retag и up.
func TestUpdateScenarioB(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	prev, _ := te.advance(t, false, nil)

	te.mustFail(t, "docker build", []string{"FAKE_DOCKER_FAIL_BUILD=1"}, "--channel", "--keitaro-idle", "--unsigned")

	j := te.journal()
	if strings.Contains(j, "APPLY_EDGE") || strings.Contains(j, "DOCKER_COMPOSE_UP") || strings.Contains(j, " emtds:local\n") {
		t.Fatalf("после провала сборки были изменения:\n%s", j)
	}
	if got := te.git(t, "rev-parse", "HEAD"); got != prev {
		t.Fatalf("HEAD сдвинулся: %s", got)
	}
	sf := te.state(t)
	if sf.Result != cron.StateFailed || sf.Phase != "done" || !strings.Contains(sf.Reason, "docker build") || sf.FinishedAt == nil {
		t.Fatalf("state.json: %+v", sf)
	}
	if !strings.Contains(readFile(filepath.Join(te.VarDir, "history")), " failed") {
		t.Fatal("history без failed")
	}
}

// TestUpdateScenarioC: провал health второй фазы — откат на прежний образ и дерево.
func TestUpdateScenarioC(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	prev, target := te.advance(t, false, nil)

	te.mustFail(t, "", []string{"FAKE_UNHEALTHY_IMAGE=" + target[:12]}, "--channel", "--keitaro-idle", "--unsigned")

	te.assertOrder(t,
		"DOCKER_TAG emtds:"+target[:12]+" emtds:local",
		"DOCKER_COMPOSE_UP engine image="+target[:12],
		"DOCKER_TAG emtds:"+prev[:12]+" emtds:local",
		"APPLY_EDGE head="+prev,
		"DOCKER_COMPOSE_UP engine image="+prev[:12],
		"DOCKER_COMPOSE_UP admin image="+prev[:12],
		"DOCKER_COMPOSE_UP cron image="+prev[:12],
	)
	if got := te.git(t, "rev-parse", "HEAD"); got != prev {
		t.Fatalf("HEAD после отката %s, ожидали %s", got, prev)
	}
	sf := te.state(t)
	if sf.Result != cron.StateRolledBack || sf.To != target || sf.FinishedAt == nil {
		t.Fatalf("state.json: %+v", sf)
	}
}

// TestUpdateScenarioD: CH/Redis упали после пересоздания — failed без отката.
func TestUpdateScenarioD(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	prev, target := te.advance(t, false, nil)

	te.mustFail(t, "", []string{"FAKE_INFRA_DOWN_AFTER_UP=1"}, "--channel", "--keitaro-idle", "--unsigned")

	if n := te.count("DOCKER_COMPOSE_UP"); n != 1 {
		t.Fatalf("ожидали один compose up (engine), получили %d\n%s", n, te.journal())
	}
	if strings.Contains(te.journal(), "DOCKER_TAG emtds:"+prev[:12]+" emtds:local") {
		t.Fatal("при упавшей инфраструктуре выполнен откат")
	}
	sf := te.state(t)
	if sf.Result != cron.StateFailed || !strings.Contains(sf.Reason, "инфраструктура") || sf.To != target {
		t.Fatalf("state.json: %+v", sf)
	}

	te2 := newTestEnv(t)
	te2.writeUpdateEnv(t, "main")
	te2.advance(t, false, nil)
	te2.mustFail(t, "инфраструктура", []string{"FAKE_DOCKER_FAIL_INFRA=1"}, "--channel", "--keitaro-idle", "--unsigned")
	if strings.Contains(te2.journal(), "DOCKER_BUILD") {
		t.Fatal("сборка при недоступной инфраструктуре")
	}
}

// TestUpdateScenarioE: отказ на не-потомка HEAD и понижение.
func TestUpdateScenarioE(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	first := te.git(t, "rev-parse", "HEAD")
	te.commit(t, "local only", false, map[string]string{"local.txt": "x\n"})

	te.mustFail(t, "не является потомком HEAD", nil, "--channel", "--keitaro-idle", "--unsigned")
	te.mustFail(t, "не является потомком HEAD", nil, "--to", first, "--keitaro-idle", "--unsigned")
	te.mustFail(t, "интерактивного терминала", nil, "--to", first, "--force-version", "--keitaro-idle", "--unsigned")
	te.mustFail(t, "недопустимый ref", nil, "--to", "main", "--keitaro-idle", "--unsigned")
	if strings.Contains(te.journal(), "DOCKER_BUILD") {
		t.Fatal("сборка при отказе по цели")
	}
}

// TestUpdateScenarioF: неподписанная цель — отказ без --unsigned, успех с ним; подписанная — без флага.
func TestUpdateScenarioF(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	te.writeAllowedSigners(t)
	te.advance(t, false, nil)

	te.mustFail(t, "--unsigned", nil, "--channel", "--keitaro-idle")
	if strings.Contains(te.journal(), "DOCKER_BUILD") {
		t.Fatal("сборка неподписанной цели без --unsigned")
	}
	te.mustRun(t, nil, "--channel", "--keitaro-idle", "--unsigned")
	if sf := te.state(t); sf.Result != cron.StateOK {
		t.Fatalf("state.json: %+v", sf)
	}

	_, signed := te.advance(t, true, nil)
	te.mustRun(t, nil, "--channel", "--keitaro-idle")
	if cf := te.check(t); !cf.Signed || cf.Target.Commit != signed {
		t.Fatalf("check.json: %+v", cf)
	}
}

// TestUpdateScenarioG: release выбирает максимальный semver, тег вне main — отказ.
func TestUpdateScenarioG(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "release")
	base := te.git(t, "rev-parse", "HEAD")
	sha10 := te.commit(t, "v1.10.0", false, map[string]string{"a.txt": "a\n"})
	sha9 := te.commit(t, "v1.9.0", false, map[string]string{"b.txt": "b\n"})
	te.git(t, "push", "-q", "origin", "HEAD:main")
	te.tag(t, "v1.10.0", sha10, false)
	te.tag(t, "v1.9.0", sha9, false)
	te.git(t, "checkout", "-q", "--detach", base)

	te.mustRun(t, nil, "--channel", "--keitaro-idle", "--unsigned")
	if sf := te.state(t); sf.Result != cron.StateOK || sf.To != sha10 {
		t.Fatalf("ожидали v1.10.0 (%s): %+v", sha10, sf)
	}
	if cf := te.check(t); cf.Target.Tag != "v1.10.0" || cf.Target.Commit != sha10 {
		t.Fatalf("check.json: %+v", cf)
	}

	te.git(t, "checkout", "-q", "-b", "side", sha10)
	side := te.commit(t, "side", false, map[string]string{"side.txt": "s\n"})
	te.tag(t, "v2.0.0", side, false)
	te.git(t, "checkout", "-q", "--detach", sha10)
	te.mustFail(t, "не является предком origin/main", nil, "--channel", "--keitaro-idle", "--unsigned")
}

// TestUpdateScenarioH: ручные шаги без --schema-ok — отказ, с ним — прогон.
func TestUpdateScenarioH(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	te.advance(t, false, map[string]string{"migrations/clickhouse/0099_new.sql": "SELECT 1;\n"})

	te.mustFail(t, "--schema-ok", nil, "--channel", "--keitaro-idle", "--unsigned")
	if strings.Contains(te.journal(), "DOCKER_BUILD") {
		t.Fatal("сборка без --schema-ok")
	}
	if cf := te.check(t); len(cf.ManualSteps) != 1 || cf.ManualSteps[0] != "migrations/clickhouse/0099_new.sql" {
		t.Fatalf("check.json manual_steps: %+v", cf.ManualSteps)
	}
	te.mustRun(t, nil, "--channel", "--keitaro-idle", "--unsigned", "--schema-ok")
}

// TestUpdateScenarioI: update/ на томе — симлинк наружу; запись и удаление через него не проходят.
func TestUpdateScenarioI(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	te.advance(t, false, nil)
	outside := filepath.Join(te.Sandbox, "outside")
	mustWrite(t, filepath.Join(outside, "pending"), "keep\n", 0o644)
	if err := os.Remove(filepath.Join(te.VolDir, "update")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(te.VolDir, "update")); err != nil {
		t.Fatal(err)
	}

	te.mustFail(t, "", nil, "--check")
	te.mustFail(t, "", nil, "--channel", "--keitaro-idle", "--unsigned")

	entries, _ := os.ReadDir(outside)
	if len(entries) != 1 || readFile(filepath.Join(outside, "pending")) != "keep\n" {
		t.Fatalf("запись или удаление через симлинк: %v", entries)
	}
	if strings.Contains(te.journal(), "DOCKER_BUILD") {
		t.Fatal("прогон продолжился при симлинке update/")
	}
}

// writeRequest кладёт файл запроса кнопки в inbox.
func (te *testEnv) writeRequest(t *testing.T, content string) {
	t.Helper()
	mustWrite(t, filepath.Join(te.VarDir, "inbox", "request"), content, 0o600)
}

// request формирует запрос кнопки через cron.FormatUpdateRequest.
func request(t *testing.T, action, target, id string, at int64) string {
	t.Helper()
	data, err := cron.FormatUpdateRequest(cron.UpdateRequest{Action: action, Target: target, ID: id, User: "admin", At: at})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// assertRefused проверяет state.json result=refused с кодом и id запроса.
func (te *testEnv) assertRefused(t *testing.T, id, code string) {
	t.Helper()
	sf := te.state(t)
	if sf.Result != cron.StateRefused || sf.ID != id || sf.Reason != code {
		t.Fatalf("ожидали refused %s/%s: %+v", id, code, sf)
	}
}

// TestUpdateScenarioJ: запросы кнопки — отказы по формату, повтору, цели и подписи; валидный apply выполняется.
func TestUpdateScenarioJ(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	te.writeAllowedSigners(t)
	_, target := te.advance(t, true, nil)
	ready := []string{"FAKE_ADMIN_READY=1"}
	req := filepath.Join(te.VarDir, "inbox", "request")
	const marker = "MARKER-CONTENT-7f3a"
	now := time.Now().Unix()

	assertGone := func(t *testing.T) {
		t.Helper()
		if _, err := os.Lstat(req); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("request не удалён: %v", err)
		}
	}
	assertNoMarker := func(t *testing.T, out string) {
		t.Helper()
		for name, s := range map[string]string{"stdout": out, "log.txt": readFile(filepath.Join(te.VolDir, "update", "log.txt")), "runs": te.runLogs(t), "journal": te.journal()} {
			if strings.Contains(s, marker) {
				t.Fatalf("содержимое запроса попало в %s", name)
			}
		}
	}

	t.Run("symlink", func(t *testing.T) {
		mustMkdir(t, filepath.Dir(req))
		outside := filepath.Join(te.Sandbox, "req-outside")
		mustWrite(t, outside, marker+"\n", 0o644)
		if err := os.Symlink(outside, req); err != nil {
			t.Fatal(err)
		}
		out := te.mustRun(t, ready, "--timer")
		assertGone(t)
		assertNoMarker(t, out)
		if readFile(outside) != marker+"\n" || !strings.Contains(out, "не обычный файл") {
			t.Fatalf("симлинк: %s", out)
		}
	})
	t.Run("too_large", func(t *testing.T) {
		te.writeRequest(t, strings.Repeat(marker, 100))
		out := te.mustRun(t, ready, "--timer")
		assertGone(t)
		assertNoMarker(t, out)
		if !strings.Contains(out, "больше 1024") {
			t.Fatalf("размер: %s", out)
		}
	})
	t.Run("garbage", func(t *testing.T) {
		te.writeRequest(t, "action=apply\n"+marker+"\n")
		out := te.mustRun(t, ready, "--timer")
		assertGone(t)
		assertNoMarker(t, out)
		if !strings.Contains(out, "запрос отклонён: формат") {
			t.Fatalf("мусор: %s", out)
		}
	})
	t.Run("foreign_sha", func(t *testing.T) {
		te.writeRequest(t, request(t, "apply", strings.Repeat("ab", 20), "00000000000000a1", now))
		te.mustRun(t, ready, "--timer")
		te.assertRefused(t, "00000000000000a1", "target_mismatch")
	})
	t.Run("replay", func(t *testing.T) {
		te.writeRequest(t, request(t, "apply", target, "00000000000000a1", now))
		te.mustRun(t, ready, "--timer")
		te.assertRefused(t, "00000000000000a1", "replay")
	})
	t.Run("stale", func(t *testing.T) {
		te.writeRequest(t, request(t, "apply", target, "00000000000000a2", now-3600))
		te.mustRun(t, ready, "--timer")
		te.assertRefused(t, "00000000000000a2", "stale")
	})
	if strings.Contains(te.journal(), "DOCKER_BUILD") {
		t.Fatal("сборка по отклонённому запросу")
	}
	t.Run("valid_apply", func(t *testing.T) {
		te.writeRequest(t, request(t, "apply", target, "00000000000000a3", now))
		te.mustRun(t, ready, "--timer")
		sf := te.state(t)
		if sf.Result != cron.StateOK || sf.To != target || sf.User != "admin" || sf.Action != "apply" {
			t.Fatalf("state.json: %+v", sf)
		}
		if got := te.git(t, "rev-parse", "HEAD"); got != target {
			t.Fatalf("HEAD=%s", got)
		}
		if _, err := os.Stat(filepath.Join(te.VolDir, "update", "pending")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("pending не снят после прогона")
		}
	})
	used := readFile(filepath.Join(te.VarDir, "used_ids"))
	for _, id := range []string{"00000000000000a1", "00000000000000a2", "00000000000000a3"} {
		if !strings.Contains(used, id+"\n") {
			t.Fatalf("used_ids без %s: %q", id, used)
		}
	}
}

// TestUpdateScenarioJUnsigned: запрос кнопки на неподписанную цель отклоняется даже при совпадении с --check.
func TestUpdateScenarioJUnsigned(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	te.writeAllowedSigners(t)
	_, target := te.advance(t, false, nil)
	te.writeRequest(t, request(t, "apply", target, "00000000000000b1", time.Now().Unix()))
	te.mustRun(t, []string{"FAKE_ADMIN_READY=1"}, "--timer")
	te.assertRefused(t, "00000000000000b1", "unsigned")
	if strings.Contains(te.journal(), "DOCKER_BUILD") {
		t.Fatal("сборка неподписанной цели по кнопке")
	}
}

// TestUpdateKeitaroGate: без ответа admin прогон отклоняется и pending снимается; с ready.json идёт дальше.
func TestUpdateKeitaroGate(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	te.advance(t, false, nil)
	te.mustFail(t, "ready.json", nil, "--channel", "--unsigned")
	if strings.Contains(te.journal(), "DOCKER_COMPOSE_UP") {
		t.Fatal("compose up без ответа гейта")
	}
	if _, err := os.Stat(filepath.Join(te.VolDir, "update", "pending")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("pending не снят после отказа гейта")
	}
	te.mustRun(t, []string{"FAKE_ADMIN_READY=1"}, "--channel", "--unsigned")
	if sf := te.state(t); sf.Result != cron.StateOK {
		t.Fatalf("state.json: %+v", sf)
	}
}

// TestUpdateScenarioK: --rollback на версию из history; без истории или образа — отказ.
func TestUpdateScenarioK(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	te.mustFail(t, "history", nil, "--rollback", "--keitaro-idle")

	prev, target := te.advance(t, false, nil)
	te.mustRun(t, nil, "--channel", "--keitaro-idle", "--unsigned")
	builds := te.count("DOCKER_BUILD")
	te.mustRun(t, nil, "--rollback", "--keitaro-idle")

	if got := te.git(t, "rev-parse", "HEAD"); got != prev {
		t.Fatalf("HEAD после отката %s, ожидали %s", got, prev)
	}
	if te.count("DOCKER_BUILD") != builds {
		t.Fatal("откат пересобирал образ")
	}
	sf := te.state(t)
	if sf.Action != "rollback" || sf.Result != cron.StateOK || sf.From != target || sf.To != prev {
		t.Fatalf("state.json: %+v", sf)
	}
	te.assertOrder(t, "DOCKER_TAG emtds:"+prev[:12]+" emtds:local", "DOCKER_COMPOSE_UP engine image="+prev[:12])

	te2 := newTestEnv(t)
	te2.writeUpdateEnv(t, "main")
	p2, t2 := te2.advance(t, false, nil)
	te2.git(t, "checkout", "-q", "--detach", t2)
	mustWrite(t, filepath.Join(te2.VarDir, "history"), fmt.Sprintf("1700000000 0123456789abcdef apply %s %s ok \n", p2, t2), 0o600)
	te2.mustFail(t, "образ emtds:"+p2[:12], nil, "--rollback", "--keitaro-idle")
}

// TestUpdateScenarioL: при занятом lock --timer выходит 0 без действий, --channel — с ошибкой.
func TestUpdateScenarioL(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	lock := filepath.Join(te.Sandbox, "run", "emtds-update.lock")
	holder := exec.Command("flock", "-x", lock, "sleep", "30")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = holder.Process.Kill()
		_ = holder.Wait()
	}()
	deadline := time.Now().Add(5 * time.Second)
	for exec.Command("flock", "-n", lock, "true").Run() == nil {
		if time.Now().After(deadline) {
			t.Fatal("lock не захвачен держателем")
		}
		time.Sleep(20 * time.Millisecond)
	}

	te.mustRun(t, nil, "--timer")
	te.mustFail(t, "заблокировано", nil, "--channel", "--keitaro-idle", "--unsigned")
	if j := te.journal(); j != "" {
		t.Fatalf("при занятом lock были вызовы:\n%s", j)
	}
}

// TestUpdateScenarioM: --install в песочнице: update.env без учётки, deploy key, юниты; отказы на учётку и чужой ключ github.com.
func TestUpdateScenarioM(t *testing.T) {
	te := newTestEnv(t)
	out := te.mustRun(t, nil, "--install", "--repo", "git@git.example.com:owner/emtds.git", "--channel", "main", "--tds-dir", te.TDSDir, "--no-systemctl")

	envFile := filepath.Join(te.StateDir, "update.env")
	envData := readFile(envFile)
	for _, want := range []string{"EMTDS_UPDATE_REPO=git@git.example.com:owner/emtds.git\n", "EMTDS_UPDATE_CHANNEL=main\n", "EMTDS_UPDATE_CHECK_HOURS=1\n", "TDS_DIR=" + te.TDSDir + "\n"} {
		if !strings.Contains(envData, want) {
			t.Fatalf("update.env без %q:\n%s", want, envData)
		}
	}
	for path, mode := range map[string]os.FileMode{envFile: 0o600, filepath.Join(te.StateDir, "deploy_key"): 0o600, filepath.Join(te.VarDir, "inbox"): 0o700} {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm() != mode {
			t.Fatalf("%s: %v %v", path, fi, err)
		}
	}
	pub := strings.TrimSpace(readFile(filepath.Join(te.StateDir, "deploy_key.pub")))
	if pub == "" || !strings.Contains(out, pub) || strings.Contains(out, "PRIVATE KEY") {
		t.Fatalf("вывод --install: ожидали .pub без приватного ключа:\n%s", out)
	}
	if !strings.Contains(out, "known_hosts") || !strings.Contains(out, "allowed_signers") || !strings.Contains(out, "подписи выключена") {
		t.Fatalf("нет подсказок known_hosts и allowed_signers или сообщения о выключенной подписи:\n%s", out)
	}
	unit := readFile(filepath.Join(te.Sandbox, "etc", "systemd", "system", "emtds-update.service"))
	if !strings.Contains(unit, "TimeoutStartSec=1800") || !strings.Contains(unit, "update.sh --timer") || !strings.Contains(unit, "Type=oneshot") {
		t.Fatalf("emtds-update.service:\n%s", unit)
	}
	if !strings.Contains(unit, "Environment=EMTDS_UPDATE_SANDBOX=") {
		t.Fatalf("emtds-update.service без Environment=EMTDS_UPDATE_SANDBOX=:\n%s", unit)
	}
	if timer := readFile(filepath.Join(te.Sandbox, "etc", "systemd", "system", "emtds-update.timer")); !strings.Contains(timer, "OnUnitActiveSec=60") {
		t.Fatalf("emtds-update.timer:\n%s", timer)
	}
	if strings.Contains(te.journal(), "SYSTEMCTL") {
		t.Fatal("systemctl вызван при --no-systemctl")
	}

	te.mustRun(t, nil, "--install", "--channel", "release")
	if envData := readFile(envFile); !strings.Contains(envData, "EMTDS_UPDATE_CHANNEL=release\n") || !strings.Contains(envData, "git@git.example.com:owner/emtds.git") {
		t.Fatalf("повторный --install затёр ключи:\n%s", envData)
	}
	if !strings.Contains(te.journal(), "SYSTEMCTL enable --now emtds-update.timer") {
		t.Fatalf("таймер не включён:\n%s", te.journal())
	}

	te2 := newTestEnv(t)
	te2.mustFail(t, "", nil, "--install", "--repo", "https://user:password@github.com/owner/emtds.git", "--no-systemctl")
	if _, err := os.Stat(filepath.Join(te2.StateDir, "update.env")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("update.env создан с учётными данными")
	}

	te3 := newTestEnv(t)
	te3.mustFail(t, "отпечаток github.com", []string{"FAKE_KEYSCAN_PUB=" + te3.SignKey + ".pub"},
		"--install", "--repo", "git@github.com:owner/emtds.git", "--no-systemctl")
	if _, err := os.Stat(filepath.Join(te3.StateDir, "known_hosts")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("known_hosts создан при несовпадении отпечатка")
	}
}

// TestUpdateScenarioN: значения из .env и server.env не попадают в вывод, log.txt и runs/*.log.
func TestUpdateScenarioN(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	te.advance(t, false, nil)
	out := te.mustRun(t, []string{"FAKE_BUILD_ECHO=step password=supersecretpassword123 token=servertoken777 short=abc"},
		"--channel", "--keitaro-idle", "--unsigned")

	logTxt := readFile(filepath.Join(te.VolDir, "update", "log.txt"))
	runs := te.runLogs(t)
	for name, s := range map[string]string{"stdout": out, "log.txt": logTxt, "runs": runs} {
		if strings.Contains(s, "supersecretpassword123") || strings.Contains(s, "servertoken777") {
			t.Fatalf("секрет в %s", name)
		}
		if !strings.Contains(s, "password=***") || !strings.Contains(s, "token=***") {
			t.Fatalf("%s без маскированной строки:\n%s", name, s)
		}
	}
}

// TestUpdateScenarioO: занятый cron не пересоздаётся, state.json с cron_lagging.
func TestUpdateScenarioO(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	te.advance(t, false, nil)
	te.mustRun(t, []string{"FAKE_CRON_BUSY=1"}, "--channel", "--keitaro-idle", "--unsigned")

	if strings.Contains(te.journal(), "DOCKER_COMPOSE_UP cron") {
		t.Fatal("занятый cron пересоздан")
	}
	if sf := te.state(t); sf.Result != cron.StateOK || !sf.CronLagging {
		t.Fatalf("state.json: %+v", sf)
	}
}

// TestUpdateInboxFIFO: FIFO и каталог в inbox не подвешивают таймер; FIFO удалён, каталог — нет.
func TestUpdateInboxFIFO(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")

	req := filepath.Join(te.VarDir, "inbox", "request")
	mustMkdir(t, filepath.Dir(req))

	t.Run("fifo", func(t *testing.T) {
		if err := syscall.Mkfifo(req, 0o600); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		start := time.Now()
		out, code := te.run(t, nil, "--timer")
		elapsed := time.Since(start)
		if elapsed > 15*time.Second {
			t.Fatalf("--timer завис на FIFO: %v", elapsed)
		}
		if code != 0 {
			t.Fatalf("--timer вернул %d:\n%s", code, out)
		}
		if _, err := os.Lstat(req); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("FIFO не удалён: %v", err)
		}
	})

	t.Run("dir", func(t *testing.T) {
		mustMkdir(t, req)
		start := time.Now()
		out, code := te.run(t, nil, "--timer")
		elapsed := time.Since(start)
		if elapsed > 15*time.Second {
			t.Fatalf("--timer завис на каталоге: %v", elapsed)
		}
		if code != 0 {
			t.Fatalf("--timer вернул %d:\n%s", code, out)
		}
		// rm -f не удаляет каталог — он должен остаться.
		if _, err := os.Stat(req); err != nil {
			t.Fatalf("каталог не должен быть удалён: %v", err)
		}
		os.Remove(req)
	})
}

// TestUpdateGitConfigSandbox: вредоносные core.hooksPath и gpg.ssh.program в .git/config
// не влияют на выполнение и проверку подписи.
func TestUpdateGitConfigSandbox(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	te.writeAllowedSigners(t)

	markerFile := filepath.Join(te.Sandbox, "hook_ran")
	hookDir := filepath.Join(te.Sandbox, "evil_hooks")
	mustMkdir(t, hookDir)
	mustWrite(t, filepath.Join(hookDir, "post-checkout"),
		"#!/usr/bin/env bash\ntouch '"+markerFile+"'\n", 0o755)

	fakeGpgProg := filepath.Join(te.Sandbox, "fake_gpg_prog")
	mustWrite(t, fakeGpgProg,
		"#!/usr/bin/env bash\necho 'Good ssh signature for signer@example.com'\necho 'signing key: signer@example.com'\nexit 0\n", 0o755)

	te.git(t, "config", "core.hooksPath", hookDir)
	te.git(t, "config", "gpg.ssh.program", fakeGpgProg)

	// Неподписанная цель: фальшивый gpg.ssh.program должен быть проигнорирован.
	te.advance(t, false, nil)
	te.mustFail(t, "--unsigned", nil, "--channel", "--keitaro-idle")
	if cf := te.check(t); cf.Signed {
		t.Fatal("check.json.Signed=true при неподписанной цели: фальшивый gpg.ssh.program применился")
	}

	// Восстановить gpg.ssh.program чтобы te.commit мог подписать коммит реальным ключом.
	// core.hooksPath с вредоносным hookDir остаётся — проверяем, что update.sh его игнорирует.
	te.git(t, "config", "gpg.ssh.program", "ssh-keygen")
	// Подписанная цель: проверить, что хук post-checkout не запускается при git checkout в stage2.
	_, signedTarget := te.advance(t, true, nil)
	// te.advance вызывает git checkout напрямую (не через git_cmd), что может создать маркер.
	// Удаляем его, чтобы проверить только поведение update.sh.
	os.Remove(markerFile)
	te.mustRun(t, nil, "--channel", "--keitaro-idle")
	if _, err := os.Stat(markerFile); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("хук post-checkout запустился в git_cmd checkout update.sh: %v", err)
	}
	if cf := te.check(t); !cf.Signed || cf.Target.Commit != signedTarget {
		t.Fatalf("check.json подписанной цели: %+v", cf)
	}
}

// TestUpdatePermChecks: update.env с правами 0620/0602 → отказ; 0600/0644 → успех.
func TestUpdatePermChecks(t *testing.T) {
	bash := requireBash(t)
	for _, tc := range []struct {
		mode   os.FileMode
		wantOK bool
	}{
		{0o600, true},
		{0o644, true},
		{0o620, false},
		{0o602, false},
	} {
		tc := tc
		t.Run(fmt.Sprintf("%04o", tc.mode), func(t *testing.T) {
			te := newTestEnv(t)
			envPath := filepath.Join(te.StateDir, "update.env")
			mustWrite(t, envPath,
				"EMTDS_UPDATE_REPO="+te.RepoDir+"\nEMTDS_UPDATE_CHANNEL=main\nEMTDS_UPDATE_CHECK_HOURS=1\nEMTDS_UPDATE_KEEP_IMAGES=3\nEMTDS_UPDATE_KEEP_BACKUPS=2\nTDS_DIR="+te.TDSDir+"\n",
				tc.mode)
			if err := os.Chmod(envPath, tc.mode); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bash, te.Script, "--dry-run")
			cmd.Env = te.env()
			out, err := cmd.CombinedOutput()
			ok := err == nil
			if ok != tc.wantOK {
				t.Fatalf("mode=%04o wantOK=%v gotOK=%v:\n%s", tc.mode, tc.wantOK, ok, out)
			}
		})
	}
}

// TestUpdateRecoverInterrupted: зависший pending + state.last с running →
// после --timer pending удалён, state.json показывает failed/interrupted.
func TestUpdateRecoverInterrupted(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")

	runID := "aabbccddeeff0011"
	prevSHA := te.git(t, "rev-parse", "HEAD")
	fakeSHA := strings.Repeat("a", 40)
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	stateLast := fmt.Sprintf(
		`{"schema":1,"id":%q,"action":"apply","from":%q,"to":%q,"started_at":%q,"phase":"build","phases":[],"result":"running","reason":"","events_lost":0,"cron_lagging":false,"user":""}`,
		runID, prevSHA, fakeSHA, now,
	)
	mustWrite(t, filepath.Join(te.VarDir, "state.last"), stateLast+"\n", 0o600)

	pendingDir := filepath.Join(te.VolDir, "update")
	mustMkdir(t, pendingDir)
	mustWrite(t, filepath.Join(pendingDir, "pending"), runID+"\n", 0o644)

	te.mustRun(t, nil, "--timer")

	if _, err := os.Stat(filepath.Join(pendingDir, "pending")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("pending не удалён после восстановления")
	}

	sf := te.state(t)
	if sf.Result != cron.StateFailed || sf.ID != runID || sf.Reason != "interrupted" || sf.FinishedAt == nil {
		t.Fatalf("state.json после восстановления: %+v", sf)
	}
}

// TestUpdateVolRmWarning: неудача vol_rm pending логируется предупреждением.
func TestUpdateVolRmWarning(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	te.advance(t, false, nil)
	out := te.mustRun(t, []string{"FAKE_DOCKER_FAIL_VOLRM=1"}, "--channel", "--keitaro-idle", "--unsigned")
	if !strings.Contains(out, "предупреждение: не удалось удалить pending") {
		t.Fatalf("нет предупреждения о неудалённом pending:\n%s", out)
	}
}

// TestUpdateValidateRepoURL: file:// и локальные пути в EMTDS_UPDATE_REPO отклоняются вне sandbox.
func TestUpdateValidateRepoURL(t *testing.T) {
	bash := requireBash(t)
	for _, url := range []string{"file:///tmp/repo.git", "/tmp/repo.git"} {
		url := url
		t.Run(url, func(t *testing.T) {
			te := newTestEnv(t)
			mustWrite(t, filepath.Join(te.StateDir, "update.env"),
				"EMTDS_UPDATE_REPO="+url+"\nEMTDS_UPDATE_CHANNEL=main\nEMTDS_UPDATE_CHECK_HOURS=1\nEMTDS_UPDATE_KEEP_IMAGES=3\nEMTDS_UPDATE_KEEP_BACKUPS=2\nTDS_DIR="+te.TDSDir+"\n",
				0o600)
			cmd := exec.Command(bash, te.Script, "--dry-run")
			env := append(te.env(), "EMTDS_UPDATE_SANDBOX=")
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("ожидали отказ для %s, но успех:\n%s", url, out)
			}
		})
	}
}

// TestUpdateComposePublicPort: опубликованный не на 127.0.0.1 порт останавливает вторую фазу до retag.
func TestUpdateComposePublicPort(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	prev, target := te.advance(t, false, nil)
	te.mustFail(t, "", []string{"FAKE_COMPOSE_PUBLIC=1"}, "--channel", "--keitaro-idle", "--unsigned")
	if strings.Contains(te.journal(), "DOCKER_TAG emtds:"+target[:12]+" emtds:local") {
		t.Fatal("retag при небезопасном compose")
	}
	if got := te.git(t, "rev-parse", "HEAD"); got != prev {
		t.Fatalf("HEAD=%s, ожидали откат на %s", got, prev)
	}
	if sf := te.state(t); sf.Result != cron.StateRolledBack {
		t.Fatalf("state.json: %+v", sf)
	}
}

// agent читает agent.json тома как cron.AgentFile.
func (te *testEnv) agent(t *testing.T) cron.AgentFile {
	t.Helper()
	var af cron.AgentFile
	if err := cron.ReadUpdateJSON(filepath.Join(te.VolDir, "update", "agent.json"), cron.UpdateMaxJSON, &af); err != nil {
		t.Fatalf("agent.json: %v", err)
	}
	return af
}

// TestUpdateNoSigningMain: без allowed_signers на канале main применение и откат идут без --unsigned
// и по кнопке, агент и --status сообщают «подпись выключена».
func TestUpdateNoSigningMain(t *testing.T) {
	ready := []string{"FAKE_ADMIN_READY=1"}

	t.Run("cli", func(t *testing.T) {
		te := newTestEnv(t)
		te.writeUpdateEnv(t, "main")
		_, target := te.advance(t, false, nil)
		te.mustRun(t, nil, "--channel", "--keitaro-idle")
		if sf := te.state(t); sf.Result != cron.StateOK || sf.To != target {
			t.Fatalf("state.json: %+v", sf)
		}
		if cf := te.check(t); cf.Signed || cf.Error != "" {
			t.Fatalf("check.json: %+v", cf)
		}
	})

	t.Run("button", func(t *testing.T) {
		te := newTestEnv(t)
		te.writeUpdateEnv(t, "main")
		prev, target := te.advance(t, false, nil)
		now := time.Now().Unix()

		te.writeRequest(t, request(t, "apply", target, "00000000000000c1", now))
		te.mustRun(t, ready, "--timer")
		if sf := te.state(t); sf.Result != cron.StateOK || sf.To != target || sf.Action != "apply" || sf.User != "admin" {
			t.Fatalf("apply по кнопке: %+v", sf)
		}
		if got := te.git(t, "rev-parse", "HEAD"); got != target {
			t.Fatalf("HEAD=%s, ожидали %s", got, target)
		}

		te.writeRequest(t, request(t, "rollback", "", "00000000000000c2", now))
		te.mustRun(t, ready, "--timer")
		if sf := te.state(t); sf.Result != cron.StateOK || sf.To != prev || sf.Action != "rollback" {
			t.Fatalf("rollback по кнопке: %+v", sf)
		}
		if got := te.git(t, "rev-parse", "HEAD"); got != prev {
			t.Fatalf("HEAD=%s, ожидали %s", got, prev)
		}

		if af := te.agent(t); af.Signing {
			t.Fatalf("agent.json: signing=true без allowed_signers: %+v", af)
		}
		if out := te.mustRun(t, nil, "--status"); !strings.Contains(out, "signing: off") {
			t.Fatalf("--status без «signing: off»:\n%s", out)
		}
	})

	t.Run("force_push", func(t *testing.T) {
		te := newTestEnv(t)
		te.writeUpdateEnv(t, "main")
		deployed := te.commit(t, "deployed", false, map[string]string{"deployed.txt": "d\n"})
		te.git(t, "push", "-q", "origin", "HEAD:main")
		te.git(t, "checkout", "-q", "-b", "rewritten", "HEAD~1")
		rewritten := te.commit(t, "rewritten", false, map[string]string{"rewritten.txt": "r\n"})
		te.git(t, "push", "-q", "-f", "origin", "rewritten:main")
		te.git(t, "checkout", "-q", "--detach", deployed)

		te.mustFail(t, "не является потомком HEAD", nil, "--check")
		if cf := te.check(t); cf.Error == "" {
			t.Fatalf("check.json без error при перезаписанном origin/main: %+v", cf)
		}
		te.mustFail(t, "не является потомком HEAD", nil, "--channel", "--keitaro-idle")

		te.writeRequest(t, request(t, "apply", rewritten, "00000000000000d1", time.Now().Unix()))
		te.mustRun(t, ready, "--timer")
		te.assertRefused(t, "00000000000000d1", "check_failed")
		if strings.Contains(te.journal(), "DOCKER_BUILD") {
			t.Fatal("сборка цели после force-push")
		}
		if got := te.git(t, "rev-parse", "HEAD"); got != deployed {
			t.Fatalf("HEAD=%s, ожидали %s", got, deployed)
		}
	})
}

// TestUpdateSigningLoosePerms: allowed_signers с ключом и небезопасными правами не выключает подпись:
// agent.json и --status сообщают «включена», проверка и запрос кнопки отклоняются.
func TestUpdateSigningLoosePerms(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	te.writeAllowedSigners(t)
	if err := os.Chmod(filepath.Join(te.StateDir, "allowed_signers"), 0o666); err != nil {
		t.Fatal(err)
	}
	_, target := te.advance(t, true, nil)

	te.mustFail(t, "небезопасные права", nil, "--check")
	te.writeRequest(t, request(t, "apply", target, "00000000000000e1", time.Now().Unix()))
	te.mustRun(t, []string{"FAKE_ADMIN_READY=1"}, "--timer")
	te.assertRefused(t, "00000000000000e1", "check_failed")
	if strings.Contains(te.journal(), "DOCKER_BUILD") {
		t.Fatal("сборка при небезопасных правах allowed_signers")
	}
	if af := te.agent(t); !af.Signing {
		t.Fatalf("agent.json: signing=false при файле с ключом: %+v", af)
	}
	if out := te.mustRun(t, nil, "--status"); !strings.Contains(out, "signing: on") {
		t.Fatalf("--status без «signing: on»:\n%s", out)
	}
}

// TestUpdatePinnedOrigin: перед fetch origin возвращается на адрес из update.env.
func TestUpdatePinnedOrigin(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	te.advance(t, false, nil)
	te.git(t, "remote", "set-url", "origin", filepath.Join(te.Sandbox, "evil.git"))

	te.mustRun(t, nil, "--check")
	if got := te.git(t, "remote", "get-url", "origin"); got != te.RepoDir {
		t.Fatalf("origin=%q, ожидали адрес из update.env %q", got, te.RepoDir)
	}
}

// commitChangelog публикует в origin/main коммит с CHANGELOG.md и оставляет рабочее дерево на нём.
func (te *testEnv) commitChangelog(t *testing.T, body string) {
	t.Helper()
	te.commit(t, "changelog", false, map[string]string{"CHANGELOG.md": body})
	te.git(t, "push", "-q", "origin", "HEAD:main")
}

// TestUpdateNotesFromChangelog: notes — добавленные строки CHANGELOG.md без «+» и заголовков diff.
func TestUpdateNotesFromChangelog(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	te.commitChangelog(t, "# Changelog\n\n## [Unreleased]\n- старая запись\n")
	te.advance(t, false, map[string]string{"CHANGELOG.md": "# Changelog\n\n## [Unreleased]\n- новая запись\n- старая запись\n"})

	te.mustRun(t, nil, "--check")
	if cf := te.check(t); cf.Notes != "- новая запись" {
		t.Fatalf("notes=%q", cf.Notes)
	}
}

// TestUpdateNotesFromTagWithoutSignature: без diff CHANGELOG.md notes — тема и тело аннотации тега, блока подписи нет.
func TestUpdateNotesFromTagWithoutSignature(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "release")
	_, target := te.advance(t, false, nil)
	te.git(t, "tag", "-s", "-m", "Тема релиза", "-m", "Тело релиза", "v1.2.3", target)
	te.git(t, "push", "-q", "origin", "refs/tags/v1.2.3")

	te.mustRun(t, nil, "--check")
	cf := te.check(t)
	if cf.Notes != "Тема релиза\n\nТело релиза" || strings.Contains(cf.Notes, "SIGNATURE") {
		t.Fatalf("notes=%q", cf.Notes)
	}
	if cf.Target.Tag != "v1.2.3" {
		t.Fatalf("check.json: %+v", cf)
	}
}

// TestUpdateNotesTruncationKeepsJSON: обрезка notes до 16 КиБ не рвёт escape-последовательность и JSON.
func TestUpdateNotesTruncationKeepsJSON(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	te.advance(t, false, map[string]string{"CHANGELOG.md": "a" + strings.Repeat(`"`, 20000) + "\n"})

	te.mustRun(t, nil, "--check")
	cf := te.check(t)
	if len(cf.Notes) != 16384 || !strings.HasPrefix(cf.Notes, `a"`) || strings.Trim(cf.Notes[1:], `"`) != "" {
		t.Fatalf("notes: длина %d", len(cf.Notes))
	}
}

// TestUpdateSigningUnreadablePolicy: битый симлинк или каталог на месте allowed_signers не выключают подпись:
// --status показывает «on», проверка и запрос кнопки отклоняются, сборки нет.
func TestUpdateSigningUnreadablePolicy(t *testing.T) {
	cases := map[string]func(t *testing.T, path string){
		"broken_symlink": func(t *testing.T, path string) {
			if err := os.Symlink(filepath.Join(filepath.Dir(path), "missing"), path); err != nil {
				t.Fatal(err)
			}
		},
		"directory": func(t *testing.T, path string) { mustMkdir(t, path) },
	}
	for name, plant := range cases {
		plant := plant
		t.Run(name, func(t *testing.T) {
			te := newTestEnv(t)
			te.writeUpdateEnv(t, "main")
			plant(t, filepath.Join(te.StateDir, "allowed_signers"))
			_, target := te.advance(t, false, nil)

			if out := te.mustRun(t, nil, "--status"); !strings.Contains(out, "signing: on") {
				t.Fatalf("--status без «signing: on»:\n%s", out)
			}
			te.mustFail(t, "не обычный файл", nil, "--check")
			te.mustFail(t, "", nil, "--channel", "--keitaro-idle")
			te.writeRequest(t, request(t, "apply", target, "00000000000000f1", time.Now().Unix()))
			te.mustRun(t, []string{"FAKE_ADMIN_READY=1"}, "--timer")
			te.assertRefused(t, "00000000000000f1", "check_failed")
			if strings.Contains(te.journal(), "DOCKER_BUILD") {
				t.Fatal("сборка неподписанной цели при нечитаемой политике подписи")
			}
			if af := te.agent(t); !af.Signing {
				t.Fatalf("agent.json: signing=false: %+v", af)
			}
		})
	}
}

// TestUpdateRollbackChain: кнопка откатывает ровно на один шаг; повторный откат отклоняется,
// старая запись apply A→B не уводит дальше.
func TestUpdateRollbackChain(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	ready := []string{"FAKE_ADMIN_READY=1"}
	_, b := te.advance(t, false, nil)
	te.mustRun(t, nil, "--channel", "--keitaro-idle")
	_, c := te.advance(t, false, nil)
	te.mustRun(t, nil, "--channel", "--keitaro-idle")
	if got := te.git(t, "rev-parse", "HEAD"); got != c {
		t.Fatalf("HEAD=%s, ожидали %s", got, c)
	}
	now := time.Now().Unix()

	te.writeRequest(t, request(t, "rollback", "", "0000000000000a01", now))
	te.mustRun(t, ready, "--timer")
	if sf := te.state(t); sf.Result != cron.StateOK || sf.Action != "rollback" || sf.To != b {
		t.Fatalf("первый откат: %+v", sf)
	}
	if got := te.git(t, "rev-parse", "HEAD"); got != b {
		t.Fatalf("HEAD=%s, ожидали %s", got, b)
	}

	ups := te.count("DOCKER_COMPOSE_UP engine")
	te.writeRequest(t, request(t, "rollback", "", "0000000000000a02", now))
	te.mustRun(t, ready, "--timer")
	te.assertRefused(t, "0000000000000a02", "no_previous")
	if got := te.git(t, "rev-parse", "HEAD"); got != b {
		t.Fatalf("после второго отката HEAD=%s, ожидали %s", got, b)
	}
	if te.count("DOCKER_COMPOSE_UP engine") != ups {
		t.Fatal("второй откат по кнопке пересоздал сервисы")
	}
}

// TestUpdateStatusStripsControl: управляющие символы из state.json тома не попадают в вывод --status.
func TestUpdateStatusStripsControl(t *testing.T) {
	te := newTestEnv(t)
	te.writeUpdateEnv(t, "main")
	mustWrite(t, filepath.Join(te.VolDir, "update", "state.json"), "{\"reason\":\"\x1b[31mred\x1b]0;title\x07 ж\"}", 0o644)

	out := te.mustRun(t, nil, "--status")
	if strings.ContainsAny(out, "\x1b\x07") || !strings.Contains(out, "red") || !strings.Contains(out, "ж") {
		t.Fatalf("--status: %q", out)
	}
}
