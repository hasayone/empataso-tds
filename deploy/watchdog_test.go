package deploy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWatchdogSelfTest проверяет bash -n и watchdog.sh --self-test.
func TestWatchdogSelfTest(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "watchdog.sh")
	if out, err := exec.Command(bash, "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("bash -n watchdog.sh: %v\n%s", err, out)
	}
	cmd := exec.Command(bash, script, "--self-test")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("self-test завершился с ошибкой: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "self-test ok") {
		t.Fatalf("ожидали self-test ok в выводе:\n%s", out)
	}
}

// TestWatchdogForbiddenCommands запрещает опасные фрагменты и CRLF в watchdog.sh.
func TestWatchdogForbiddenCommands(t *testing.T) {
	data, err := os.ReadFile("watchdog.sh")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("\r")) {
		t.Fatal("watchdog.sh содержит CRLF (\\r)")
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || lines[0] != "#!/usr/bin/env bash" {
		t.Fatalf("первая строка должна быть #!/usr/bin/env bash, получено: %q", lines[0])
	}
	forbidden := []string{
		"compose down",
		"--force-recreate",
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
	s := string(data)
	for _, want := range []string{
		"OnUnitActiveSec=60s",
		"kuztds-watchdog.timer",
		"up -d --no-deps",
		"не restart (keitaro job)",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("watchdog.sh: нет %q", want)
		}
	}
	if strings.Contains(s, "php") && strings.Contains(s, "force-recreate") {
		t.Fatal("watchdog.sh не должен force-recreate php")
	}
}

// TestInstallWatchdogHook проверяет, что установщик включает сторож и не спорит с его юнитами.
func TestInstallWatchdogHook(t *testing.T) {
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, `bash "${OUR_DEPLOY_DIR}/watchdog.sh" --install`) {
		t.Fatal("install.sh: ожидали watchdog.sh --install")
	}
	if !strings.Contains(s, `grep -v '^kuztds-watchdog\.'`) {
		t.Fatal("install.sh: юниты kuztds-watchdog не должны блокировать установку")
	}
}
