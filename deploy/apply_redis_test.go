package deploy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// redisBash возвращает путь к bash или пропускает тест, если bash нет в PATH.
func redisBash(t *testing.T) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	return bash
}

// shellFunc вырезает из src тело функции name (от «name() {» до первой строки «}»).
func shellFunc(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, name+"() {")
	if start < 0 {
		t.Fatalf("не найдена функция %s", name)
	}
	end := strings.Index(src[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("не найден конец функции %s", name)
	}
	return src[start : start+end+3]
}

// TestApplyRedisScriptGuards статически проверяет apply-redis.sh: режим --check, команды без
// пересоздания контейнера, пароль только через REDISCLI_AUTH, без запрещённых команд.
func TestApplyRedisScriptGuards(t *testing.T) {
	data, err := os.ReadFile("apply-redis.sh")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("\r")) {
		t.Fatal("apply-redis.sh содержит CRLF")
	}
	s := string(data)
	if !strings.HasPrefix(s, "#!/usr/bin/env bash\n") {
		t.Fatal("первая строка должна быть #!/usr/bin/env bash")
	}
	for _, want := range []string{
		"set -euo pipefail",
		"--check",
		"CONFIG SET maxmemory",
		"docker update --memory",
		"--memory-swap",
		"REDISCLI_AUTH",
		`if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("apply-redis.sh: нет %q", want)
		}
	}
	for _, bad := range []string{
		"compose", "docker restart", "docker stop", "docker kill", "docker rm", "docker run",
		"volume", "prune", "FLUSH", "CONFIG REWRITE", "set -x", "empataso", "requirepass", "redis-cli -a ",
	} {
		if strings.Contains(s, bad) {
			t.Fatalf("apply-redis.sh содержит запрещённый фрагмент %q", bad)
		}
	}
	leak := regexp.MustCompile(`(?m)^\s*(echo|printf)\b[^\n]*\$\{?(pw|REDISCLI_AUTH)\b`)
	if loc := leak.FindString(s); loc != "" {
		t.Fatalf("apply-redis.sh печатает пароль: %q", loc)
	}
	if out, err := exec.Command(redisBash(t), "-n", "apply-redis.sh").CombinedOutput(); err != nil {
		t.Fatalf("bash -n apply-redis.sh: %v\n%s", err, out)
	}
}

// TestApplyRedisToBytes проверяет разбор размеров Redis.
func TestApplyRedisToBytes(t *testing.T) {
	bash := redisBash(t)
	cases := []struct {
		in   string
		want string
		fail bool
	}{
		{"512mb", "536870912", false},
		{"1GB", "1073741824", false},
		{"2gb", "2147483648", false},
		{"768m", "805306368", false},
		{"1536m", "1610612736", false},
		{"3g", "3221225472", false},
		{"4k", "4096", false},
		{"1000", "1000", false},
		{"", "", true},
		{"0", "", true},
		{"0gb", "", true},
		{"abc", "", true},
		{"1.5g", "", true},
		{"-1g", "", true},
		{"2tb", "", true},
	}
	for _, tc := range cases {
		cmd := exec.Command(bash, "-c", "source apply-redis.sh; to_bytes \"$1\"", "bash", tc.in)
		out, err := cmd.Output()
		if tc.fail {
			if err == nil {
				t.Errorf("to_bytes(%q) = %q, ожидали ошибку", tc.in, out)
			}
			continue
		}
		if err != nil || strings.TrimSpace(string(out)) != tc.want {
			t.Errorf("to_bytes(%q) = %q, %v; хотели %s", tc.in, out, err, tc.want)
		}
	}
}

// TestApplyRedisTiersMatchInstall: пороги и значения apply-redis.sh те же, что у install.sh,
// и окно в сутки на крупном тире получает 2gb.
func TestApplyRedisTiersMatchInstall(t *testing.T) {
	bash := redisBash(t)
	installData, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	applyData, err := os.ReadFile("apply-redis.sh")
	if err != nil {
		t.Fatal(err)
	}
	install, apply := string(installData), string(applyData)
	funcs := shellFunc(t, install, "redis_maxmemory") + shellFunc(t, install, "redis_mem_limit") +
		shellFunc(t, apply, "tier_maxmemory") + shellFunc(t, apply, "tier_memory")
	want := map[int][2]string{
		7000: {"512mb", "768m"}, 11999: {"512mb", "768m"},
		12000: {"2gb", "3g"}, 27999: {"2gb", "3g"},
		28000: {"2gb", "3g"}, 64000: {"2gb", "3g"},
	}
	for mem, w := range want {
		m := strconv.Itoa(mem)
		script := funcs + `printf '%s %s %s %s' "$(redis_maxmemory ` + m + `)" "$(redis_mem_limit ` + m + `)" "$(tier_maxmemory ` + m + `)" "$(tier_memory ` + m + `)"`
		out, err := exec.Command(bash, "-c", script).Output()
		if err != nil {
			t.Fatalf("mem=%d: %v", mem, err)
		}
		if got, exp := string(out), w[0]+" "+w[1]+" "+w[0]+" "+w[1]; got != exp {
			t.Errorf("mem=%d: install и apply-redis разошлись: %q, ожидали %q", mem, got, exp)
		}
	}
}

// fakeDocker поднимает заглушку docker в PATH: состояние Redis и контейнера лежит в файлах,
// каждый вызов пишется в calls.log. Возвращает каталог состояния и каталог с заглушкой.
func fakeDocker(t *testing.T, maxmemory, mem, swap, used string) (state, bin string) {
	t.Helper()
	root := t.TempDir()
	state, bin = filepath.Join(root, "state"), filepath.Join(root, "bin")
	for _, d := range []string{state, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, v := range map[string]string{
		"maxmemory": maxmemory, "mem": mem, "swap": swap, "used": used, "rss": used,
		"stats": "1MiB / 3GiB",
		"cmd":   `["redis-server","--save","","--maxmemory","512mb","--maxmemory-policy","noeviction"]`,
	} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stub := `#!/bin/bash
d="$STUB_DIR"
echo "$*" >> "$d/calls.log"
case "$1" in
inspect)
	[[ -e "$d/missing" ]] && exit 1
	if [[ "$2" == "-f" && "$3" == *Config.Cmd* ]]; then cat "$d/cmd"; elif [[ "$2" == "-f" ]]; then echo "$(cat "$d/mem") $(cat "$d/swap")"; fi
	;;
stats)
	[[ -e "$d/stats_fail" ]] && exit 1
	cat "$d/stats"
	;;
exec)
	args=("$@")
	rest=()
	for i in "${!args[@]}"; do
		if [[ "${args[$i]}" == "-e" && "${args[$((i+1))]}" == "REDISCLI_AUTH" ]]; then echo "auth-passed:${REDISCLI_AUTH:+set}" >> "$d/calls.log"; fi
		if [[ "${args[$i]}" == "redis-cli" ]]; then rest=("${args[@]:$((i+1))}"); fi
	done
	case "${rest[0]} ${rest[1]}" in
	"CONFIG GET") printf 'maxmemory\n%s\n' "$(cat "$d/maxmemory")" ;;
	"CONFIG SET")
		[[ -e "$d/fail_set" ]] && exit 1
		echo "${rest[3]}" > "$d/maxmemory"
		;;
	"INFO memory") printf '# Memory\r\nused_memory:%s\r\nused_memory_rss:%s\r\nused_memory_human:1M\r\n' "$(cat "$d/used")" "$(cat "$d/rss")" ;;
	esac
	;;
update)
	[[ -e "$d/fail_update" ]] && exit 1
	echo "$3" > "$d/mem"
	echo "$5" > "$d/swap"
	;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	return state, bin
}

// runApplyRedis запускает apply-redis.sh с заглушкой docker и RAM сервера 32000 MiB.
// Возвращает объединённый вывод и код выхода.
func runApplyRedis(t *testing.T, state, bin string, args ...string) (string, int) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(t.TempDir(), "server.env")
	if err := os.WriteFile(envFile, []byte("EMTDS_REDIS_PASSWORD=s3cret-pass-xyz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(redisBash(t), append([]string{filepath.Join(dir, "apply-redis.sh")}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_DIR="+state,
		"EMTDS_HOST_MEM_MIB=32000",
		"EMTDS_REDIS_PASSWORD=",
		"KUZTDS_REDIS_PASSWORD=",
		"EMTDS_SERVER_ENV="+envFile,
	)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

// stateValue читает файл состояния заглушки.
func stateValue(t *testing.T, state, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(state, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// stateCalls возвращает вызовы docker, которые меняют состояние.
func stateCalls(t *testing.T, state string) []string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(state, "calls.log"))
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "update ") || strings.Contains(line, "CONFIG SET") {
			out = append(out, line)
		}
	}
	return out
}

// TestApplyRedisCheckChangesNothing: --check показывает значения и не пишет ни CONFIG SET, ни docker update.
func TestApplyRedisCheckChangesNothing(t *testing.T) {
	state, bin := fakeDocker(t, "1073741824", "1610612736", "1610612736", "100000000")
	out, code := runApplyRedis(t, state, bin, "--check")
	if code != 0 {
		t.Fatalf("--check: код %d\n%s", code, out)
	}
	for _, want := range []string{
		"maxmemory: сейчас 1024 MiB, цель 2048 MiB",
		"изменения нужны, запись не выполнялась",
		"maxmemory в command контейнера: 512 MiB",
		"до перезапуска процесса Redis",
		"расхождение: maxmemory сейчас 1024 MiB, в command контейнера 512 MiB",
		"повторите этот скрипт после перезагрузки сервера",
		"только с согласия пользователя",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("--check: нет %q в выводе:\n%s", want, out)
		}
	}
	if calls := stateCalls(t, state); len(calls) != 0 {
		t.Fatalf("--check записал: %v", calls)
	}
	if strings.Contains(out, "s3cret-pass-xyz") {
		t.Fatal("пароль попал в вывод")
	}
}

// TestApplyRedisAppliesOnceInOrder: при росте сначала лимит контейнера, потом maxmemory;
// повторный запуск ничего не пишет; пароль не попадает ни в вывод, ни в аргументы docker.
func TestApplyRedisAppliesOnceInOrder(t *testing.T) {
	state, bin := fakeDocker(t, "1073741824", "1610612736", "1610612736", "100000000")
	out, code := runApplyRedis(t, state, bin)
	if code != 0 {
		t.Fatalf("apply: код %d\n%s", code, out)
	}
	calls := stateCalls(t, state)
	if len(calls) != 2 ||
		!strings.HasPrefix(calls[0], "update --memory 3221225472 --memory-swap 3221225472 ") ||
		!strings.Contains(calls[1], "CONFIG SET maxmemory 2147483648") {
		t.Fatalf("порядок или значения записи неверны: %v", calls)
	}
	if v := stateValue(t, state, "maxmemory"); v != "2147483648" {
		t.Errorf("maxmemory = %s", v)
	}
	if stateValue(t, state, "mem") != "3221225472" || stateValue(t, state, "swap") != "3221225472" {
		t.Errorf("лимит контейнера не применён: mem=%s swap=%s", stateValue(t, state, "mem"), stateValue(t, state, "swap"))
	}
	log, _ := os.ReadFile(filepath.Join(state, "calls.log"))
	if strings.Contains(out, "s3cret-pass-xyz") || bytes.Contains(log, []byte("s3cret-pass-xyz")) {
		t.Fatal("пароль попал в вывод или в аргументы docker")
	}
	if !bytes.Contains(log, []byte("auth-passed:set")) {
		t.Error("пароль из server.env не передан через REDISCLI_AUTH")
	}

	before := len(stateCalls(t, state))
	out, code = runApplyRedis(t, state, bin)
	if code != 0 || !strings.Contains(out, "менять нечего") {
		t.Fatalf("повторный запуск: код %d\n%s", code, out)
	}
	if after := len(stateCalls(t, state)); after != before {
		t.Fatalf("повторный запуск записал ещё %d раз(а)", after-before)
	}
}

// TestApplyRedisRefusesBelowUsed: maxmemory ниже занятой памяти не ставится, состояние не меняется.
func TestApplyRedisRefusesBelowUsed(t *testing.T) {
	state, bin := fakeDocker(t, "2147483648", "3221225472", "3221225472", "1610612736")
	out, code := runApplyRedis(t, state, bin, "--maxmemory", "1gb", "--memory", "1536m")
	if code == 0 || !strings.Contains(out, "ниже занятой памяти") {
		t.Fatalf("ожидали отказ, код %d\n%s", code, out)
	}
	if calls := stateCalls(t, state); len(calls) != 0 {
		t.Fatalf("отказ всё равно записал: %v", calls)
	}
}

// TestApplyRedisBadInput: неверный аргумент, лимит контейнера без запаса и отсутствующий контейнер.
func TestApplyRedisBadInput(t *testing.T) {
	state, bin := fakeDocker(t, "1073741824", "1610612736", "1610612736", "100000000")
	if _, code := runApplyRedis(t, state, bin, "--bogus"); code != 2 {
		t.Errorf("неизвестный аргумент: код %d, ожидали 2", code)
	}
	if out, code := runApplyRedis(t, state, bin, "--maxmemory", "2gb", "--memory", "2gb"); code == 0 || !strings.Contains(out, "с запасом") {
		t.Errorf("лимит без запаса принят: код %d\n%s", code, out)
	}
	if err := os.WriteFile(filepath.Join(state, "missing"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := runApplyRedis(t, state, bin); code == 0 || !strings.Contains(out, "не найден") {
		t.Errorf("отсутствующий контейнер: код %d\n%s", code, out)
	}
	if calls := stateCalls(t, state); len(calls) != 0 {
		t.Fatalf("при ошибке было записано: %v", calls)
	}
}

// setStub записывает файл состояния заглушки docker; файлы stats_fail, fail_set и fail_update
// служат флагами сбоя, их значение не важно.
func setStub(t *testing.T, state, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(state, name), []byte(value+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestApplyRedisLoweringChecksUsage: лимит контейнера не снижается ниже потребления с запасом 20%
// ни по docker stats, ни по RSS Redis, ни при недоступном docker stats; безопасное снижение идёт
// сначала maxmemory, потом лимит.
func TestApplyRedisLoweringChecksUsage(t *testing.T) {
	lower := []string{"--maxmemory", "1gb", "--memory", "1536m"}
	cases := []struct {
		name  string
		stats string
		rss   string
		fail  bool
		want  string
	}{
		{"docker stats выше цели", "1.4GiB / 3GiB", "100000000", false, "ниже потребления"},
		{"RSS выше цели", "100MiB / 3GiB", "1503238554", false, "ниже потребления"},
		{"docker stats недоступен", "", "100000000", true, "docker stats не отдал"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, bin := fakeDocker(t, "2147483648", "3221225472", "3221225472", "100000000")
			setStub(t, state, "stats", tc.stats)
			setStub(t, state, "rss", tc.rss)
			if tc.fail {
				setStub(t, state, "stats_fail", "")
			}
			out, code := runApplyRedis(t, state, bin, lower...)
			if code == 0 || !strings.Contains(out, tc.want) || !strings.Contains(out, "отказ") {
				t.Fatalf("ожидали отказ с %q, код %d\n%s", tc.want, code, out)
			}
			if calls := stateCalls(t, state); len(calls) != 0 {
				t.Fatalf("отказ всё равно записал: %v", calls)
			}
		})
	}

	state, bin := fakeDocker(t, "2147483648", "3221225472", "3221225472", "100000000")
	setStub(t, state, "stats", "500MiB / 3GiB")
	setStub(t, state, "rss", "524288000")
	out, code := runApplyRedis(t, state, bin, lower...)
	if code != 0 {
		t.Fatalf("безопасное снижение: код %d\n%s", code, out)
	}
	calls := stateCalls(t, state)
	if len(calls) != 2 || !strings.Contains(calls[0], "CONFIG SET maxmemory 1073741824") ||
		!strings.HasPrefix(calls[1], "update --memory 1610612736 ") {
		t.Fatalf("порядок снижения неверен: %v", calls)
	}
}

// TestApplyRedisPartialApply: сбой второго шага даёт «применено частично, повторите» и ненулевой
// код, повторный запуск доделывает только недостающее; сбой первого шага ничего не применяет.
func TestApplyRedisPartialApply(t *testing.T) {
	state, bin := fakeDocker(t, "1073741824", "1610612736", "1610612736", "100000000")
	setStub(t, state, "fail_set", "")
	out, code := runApplyRedis(t, state, bin)
	if code == 0 || !strings.Contains(out, "применено частично, повторите") {
		t.Fatalf("ожидали частичное применение, код %d\n%s", code, out)
	}
	if stateValue(t, state, "mem") != "3221225472" || stateValue(t, state, "maxmemory") != "1073741824" {
		t.Fatalf("состояние после сбоя: mem=%s maxmemory=%s", stateValue(t, state, "mem"), stateValue(t, state, "maxmemory"))
	}
	if err := os.Remove(filepath.Join(state, "fail_set")); err != nil {
		t.Fatal(err)
	}
	before := len(stateCalls(t, state))
	if out, code = runApplyRedis(t, state, bin); code != 0 {
		t.Fatalf("повтор: код %d\n%s", code, out)
	}
	if after := stateCalls(t, state); len(after)-before != 1 || !strings.Contains(after[len(after)-1], "CONFIG SET maxmemory 2147483648") {
		t.Fatalf("повтор должен был записать только maxmemory: %v", after[before:])
	}

	state, bin = fakeDocker(t, "1073741824", "1610612736", "1610612736", "100000000")
	setStub(t, state, "fail_update", "")
	out, code = runApplyRedis(t, state, bin)
	if code == 0 || !strings.Contains(out, "ничего не применено") || strings.Contains(out, "применено частично") {
		t.Fatalf("сбой первого шага: код %d\n%s", code, out)
	}
	if stateValue(t, state, "maxmemory") != "1073741824" {
		t.Fatal("maxmemory изменился после сбоя первого шага")
	}
}