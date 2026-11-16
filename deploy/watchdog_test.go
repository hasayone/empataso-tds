package deploy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		"emtds-watchdog.timer",
		"emtds-update.lock",
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

// TestWatchdogUpdateLock проверяет: при занятом lock update.sh watchdog завершается 0
// и не вызывает docker (фейковый docker в PATH ведёт журнал вызовов).
func TestWatchdogUpdateLock(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	lockFile := filepath.Join(tmp, "emtds-update.lock")
	dockerLog := filepath.Join(tmp, "docker.log")

	fakeBin := filepath.Join(tmp, "docker")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\necho docker-called >> "+dockerLog+"\n"), 0755); err != nil {
		t.Fatal(err)
	}

	holdScript := "exec 9>" + lockFile + "; flock -x 9; echo LOCKED; sleep 60"
	holdCmd := exec.Command(bash, "-c", holdScript)
	pr, pw, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	holdCmd.Stdout = pw
	if err := holdCmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		t.Fatal(err)
	}
	defer holdCmd.Wait()
	defer holdCmd.Process.Kill()
	pw.Close()

	done := make(chan struct{})
	go func() {
		buf := make([]byte, 10)
		_, _ = pr.Read(buf)
		pr.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("фоновый bash не сообщил LOCKED за 5 секунд")
	}

	script := filepath.Join(dir, "watchdog.sh")
	cmd := exec.Command(bash, script)
	cmd.Env = append(os.Environ(),
		"PATH="+tmp+":"+os.Getenv("PATH"),
		"EMTDS_UPDATE_LOCK="+lockFile,
	)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("watchdog должен выйти 0 при занятом update lock, err=%v\n%s", runErr, out)
	}
	if !strings.Contains(string(out), "update.sh") {
		t.Fatalf("watchdog должен сообщить о прогоне update.sh:\n%s", out)
	}
	if _, statErr := os.Stat(dockerLog); statErr == nil {
		t.Fatal("watchdog вызвал docker при занятом update lock")
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
	if !strings.Contains(s, `grep -Ev '^emtds-(watchdog|update)\.'`) {
		t.Fatal("install.sh: юниты emtds-watchdog и emtds-update не должны блокировать установку")
	}
}
