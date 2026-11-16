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

// TestMigrateSelfTest проверяет bash -n и migrate-emtds.sh --self-test.
func TestMigrateSelfTest(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "migrate-emtds.sh")
	if out, err := exec.Command(bash, "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("bash -n migrate-emtds.sh: %v\n%s", err, out)
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

// TestMigrateForbiddenCommands запрещает в migrate-emtds.sh остановку стека, удаление томов и CRLF.
func TestMigrateForbiddenCommands(t *testing.T) {
	data, err := os.ReadFile("migrate-emtds.sh")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("\r")) {
		t.Fatal("migrate-emtds.sh содержит CRLF (\\r)")
	}
	lines := strings.Split(string(data), "\n")
	if lines[0] != "#!/usr/bin/env bash" {
		t.Fatalf("первая строка должна быть #!/usr/bin/env bash, получено: %q", lines[0])
	}
	forbidden := []string{
		"compose down",
		"volume rm",
		"docker rm",
		"system prune",
		"network prune",
		"--force-recreate",
		"empataso",
		"set -x",
		"deploy-clickhouse",
		"deploy-redis",
		"-p deploy",
	}
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") || strings.Contains(line, "msg ") || strings.Contains(line, "die ") {
			continue
		}
		for _, frag := range forbidden {
			if strings.Contains(strings.ToLower(line), frag) {
				t.Fatalf("строка %d содержит запрещённый фрагмент %q: %q", i+1, frag, line)
			}
		}
	}
	s := string(data)
	for _, want := range []string{
		"set -euo pipefail",
		"--dry-run",
		"--rollback",
		`up -d --no-deps "${APP_SERVICES[@]}"`,
		"emtds.migrated-from",
		"volume_fingerprint",
		"deploy/apply-edge.sh",
		"systemctl disable --now kuztds-watchdog.timer",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("migrate-emtds.sh: нет %q", want)
		}
	}
	if !regexp.MustCompile(`APP_SERVICES=\(engine admin php cron\)`).MatchString(s) {
		t.Fatal("migrate-emtds.sh: новый проект поднимает только engine, admin, php и cron")
	}
}

// migrateScript читает migrate-emtds.sh.
func migrateScript(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("migrate-emtds.sh")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// migrateFuncBody возвращает тело функции shell от «name() {» до первой строки «}».
func migrateFuncBody(t *testing.T, script, name string) string {
	t.Helper()
	start := strings.Index(script, "\n"+name+"() {\n")
	if start < 0 {
		t.Fatalf("migrate-emtds.sh: нет функции %s", name)
	}
	body := script[start:]
	end := strings.Index(body, "\n}\n")
	if end < 0 {
		t.Fatalf("migrate-emtds.sh: функция %s не закрыта", name)
	}
	return body[:end]
}

// runMigrate запускает migrate-emtds.sh в bash с каталогами во временной папке.
// args — аргументы сценария. Возвращает объединённый вывод и ошибку запуска.
func runMigrate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash не найден в PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	cmd := exec.Command(bash, append([]string{filepath.Join(dir, "migrate-emtds.sh")}, args...)...)
	cmd.Env = append(os.Environ(),
		"MIGRATE_OLD_TDS="+filepath.Join(tmp, "old-tds"),
		"MIGRATE_NEW_TDS="+filepath.Join(tmp, "new-tds"),
		"MIGRATE_OLD_STATE="+filepath.Join(tmp, "old-state"),
		"MIGRATE_NEW_STATE="+filepath.Join(tmp, "new-state"),
		"MIGRATE_BACKUP_ROOT="+filepath.Join(tmp, "backup"),
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestMigrateBuildBeforeStop проверяет порядок: образ собирается до остановки старого приложения,
// тома копируются после остановки, новое приложение стартует из готового образа.
func TestMigrateBuildBeforeStop(t *testing.T) {
	s := migrateScript(t)
	body := migrateFuncBody(t, s, "run_migrate")
	prev := -1
	for _, step := range []string{"require_keitaro_idle", "backup_config", "rewrite_configs", "build_new_image", "stop_old_app", "copy_volumes", "start_new_app", "apply_nginx"} {
		at := strings.Index(body, "\t"+step)
		if at < 0 {
			t.Fatalf("run_migrate: нет шага %s", step)
		}
		if at < prev {
			t.Fatalf("run_migrate: шаг %s стоит раньше предыдущего", step)
		}
		prev = at
	}
	if strings.Contains(migrateFuncBody(t, s, "start_new_app"), "build") {
		t.Fatal("start_new_app не должен собирать образ: сборка идёт до остановки старого приложения")
	}
	if !strings.Contains(migrateFuncBody(t, s, "build_new_image"), "app_compose build engine") {
		t.Fatal("build_new_image: нет сборки engine")
	}
}

// TestMigrateExplicitVolumes проверяет, что копируется явный список томов, а не всё с префиксом kuztds.
func TestMigrateExplicitVolumes(t *testing.T) {
	s := migrateScript(t)
	if !strings.Contains(s, "STATE_VOLUMES=(kuztds-app_kuztds_state)") {
		t.Fatal("нет явного списка томов STATE_VOLUMES")
	}
	compose, err := os.ReadFile("docker-compose.app.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "emtds_state:") {
		t.Fatal("docker-compose.app.yml: нет тома emtds_state, список томов миграции надо сверить")
	}
	body := migrateFuncBody(t, s, "copy_volumes")
	if strings.Contains(body, "docker volume ls") || strings.Contains(body, "grep -E '^kuztds") {
		t.Fatal("copy_volumes не должен перебирать все тома по префиксу")
	}
	if !strings.Contains(body, `for src in "${STATE_VOLUMES[@]}"`) {
		t.Fatal("copy_volumes должен идти по STATE_VOLUMES")
	}
}

// TestMigrateEnginePublish проверяет, что адрес healthz движка берётся из server.env, а не зашит.
func TestMigrateEnginePublish(t *testing.T) {
	s := migrateScript(t)
	if strings.Contains(migrateFuncBody(t, s, "start_new_app"), "127.0.0.1:18080") {
		t.Fatal("start_new_app: адрес движка зашит, нужен engine_health_url")
	}
	if !strings.Contains(migrateFuncBody(t, s, "engine_health_url"), "env_value_compat") ||
		!strings.Contains(s, "ENGINE_PUBLISH") || !strings.Contains(s, "127.0.0.1:18080)") {
		t.Fatal("engine_health_url должен читать ENGINE_PUBLISH с запасным 127.0.0.1:18080")
	}
}

// TestMigrateKeitaroGate: боевой запуск без --keitaro-idle отказывает, --dry-run только напоминает.
func TestMigrateKeitaroGate(t *testing.T) {
	out, err := runMigrate(t)
	if err == nil || !strings.Contains(out, "--keitaro-idle") || !strings.Contains(out, "/api/keitaro/job") {
		t.Fatalf("запуск без --keitaro-idle должен отказать с подсказкой, err=%v:\n%s", err, out)
	}
	out, _ = runMigrate(t, "--dry-run")
	if !strings.Contains(out, "--keitaro-idle") || strings.Contains(out, "ошибка: перенос Keitaro не проверен") {
		t.Fatalf("--dry-run не должен требовать флаг, только напоминать:\n%s", out)
	}
}

// TestMigrateRollbackDisablesUpdateTimer проверяет, что run_rollback отключает emtds-update.timer
// тем же способом, что и emtds-watchdog.timer.
func TestMigrateRollbackDisablesUpdateTimer(t *testing.T) {
	s := migrateScript(t)
	body := migrateFuncBody(t, s, "run_rollback")
	if !strings.Contains(body, "emtds-update.timer") {
		t.Fatal("run_rollback: нет отключения emtds-update.timer")
	}
	if !strings.Contains(body, "systemctl disable --now emtds-update.timer") {
		t.Fatal("run_rollback: emtds-update.timer должен отключаться через systemctl disable --now")
	}
}

// TestMigrateRejectsMixedModes: --dry-run несовместим с --rollback и --self-test, неизвестный флаг даёт код 2.
func TestMigrateRejectsMixedModes(t *testing.T) {
	for _, args := range [][]string{{"--dry-run", "--rollback"}, {"--dry-run", "--self-test"}, {"--bogus"}} {
		out, err := runMigrate(t, args...)
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 2 {
			t.Fatalf("%v: ждали код 2, err=%v:\n%s", args, err, out)
		}
	}
}
