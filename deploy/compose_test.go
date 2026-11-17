package deploy

import (
	"os"
	"strings"
	"testing"
)

// TestComposeAppSecurity проверяет настройки безопасности в deploy/docker-compose.app.yml:
// ограниченное монтирование тома для php, заголовки Apache, переменные постбека и loopback-порты.
func TestComposeAppSecurity(t *testing.T) {
	data, err := os.ReadFile("docker-compose.app.yml")
	if err != nil {
		t.Fatal(err)
	}
	// Нормализуем CRLF для надёжного поиска по строкам.
	s := strings.ReplaceAll(string(data), "\r\n", "\n")

	// Границы секций по отступу верхнего уровня сервисов.
	engineIdx := strings.Index(s, "\n  engine:\n")
	phpIdx := strings.Index(s, "\n  php:\n")
	adminIdx := strings.Index(s, "\n  admin:\n")
	cronIdx := strings.Index(s, "\n  cron:\n")
	if engineIdx < 0 || phpIdx < 0 || adminIdx < 0 || cronIdx < 0 {
		t.Fatal("docker-compose.app.yml: не найдены секции engine/php/admin/cron")
	}

	engineSection := s[engineIdx:phpIdx]
	phpSection := s[phpIdx:adminIdx]
	adminSection := s[adminIdx:cronIdx]

	// --- php: длинная запись тома с subpath ---
	for _, want := range []string{
		"type: volume",
		"source: emtds_state",
		"target: /var/lib/emtds/landings",
		"read_only: true",
		"subpath: landings",
	} {
		if !strings.Contains(phpSection, want) {
			t.Fatalf("php: нет %q", want)
		}
	}

	// php не должен монтировать весь том /var/lib/emtds.
	if strings.Contains(phpSection, "emtds_state:/var/lib/emtds") {
		t.Fatal("php: короткая запись тома emtds_state:/var/lib/emtds запрещена")
	}
	if strings.Contains(phpSection, "target: /var/lib/emtds\n") {
		t.Fatal("php: target /var/lib/emtds без подкаталога запрещён")
	}

	// --- php: sed по security.conf ---
	if !strings.Contains(phpSection, "ServerTokens Prod") {
		t.Fatal("php: в command нет ServerTokens Prod")
	}
	if !strings.Contains(phpSection, "ServerSignature Off") {
		t.Fatal("php: в command нет ServerSignature Off")
	}
	if !strings.Contains(phpSection, "security.conf") {
		t.Fatal("php: в command нет обращения к security.conf")
	}

	// --- engine: переменные постбека ---
	for _, want := range []string{
		"EMTDS_POSTBACK_SECRET: ${EMTDS_POSTBACK_SECRET:-}",
		"EMTDS_POSTBACK_AUTH: ${EMTDS_POSTBACK_AUTH:-}",
	} {
		if !strings.Contains(engineSection, want) {
			t.Fatalf("engine: нет %q", want)
		}
	}

	// --- loopback по умолчанию ---
	if !strings.Contains(engineSection, "127.0.0.1:8080:8080") {
		t.Fatal("engine: порт должен публиковаться на 127.0.0.1 по умолчанию")
	}
	if !strings.Contains(adminSection, "127.0.0.1:8090:8090") {
		t.Fatal("admin: порт должен публиковаться на 127.0.0.1 по умолчанию")
	}

	// --- php depends_on engine ---
	if !strings.Contains(phpSection, "depends_on:") || !strings.Contains(phpSection, "service_healthy") {
		t.Fatal("php: нет depends_on engine service_healthy")
	}
}

// TestComposeInfraLoopback проверяет loopback-умолчания в deploy/docker-compose.yml.
func TestComposeInfraLoopback(t *testing.T) {
	data, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	for _, want := range []string{
		"127.0.0.1:8123:8123",
		"127.0.0.1:9000:9000",
		"127.0.0.1:6379:6379",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("docker-compose.yml: нет loopback-умолчания %q", want)
		}
	}
}

// TestComposeInfraNetworkName проверяет, что проект инфраструктуры называется emtds-infra,
// а приложение подключается к его сети emtds-infra_default.
func TestComposeInfraNetworkName(t *testing.T) {
	infra, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ReplaceAll(string(infra), "\r\n", "\n"), "\nname: emtds-infra\n") {
		t.Fatal("docker-compose.yml: нет name: emtds-infra")
	}
	app, err := os.ReadFile("docker-compose.app.yml")
	if err != nil {
		t.Fatal(err)
	}
	as := string(app)
	if !strings.Contains(as, "name: emtds-infra_default") {
		t.Fatal("docker-compose.app.yml: внешняя сеть должна называться emtds-infra_default")
	}
	if strings.Contains(as, "deploy_default") {
		t.Fatal("docker-compose.app.yml: сеть deploy_default не используется")
	}
}

// TestComposeAdminSetupTokenAndInbox проверяет, что токен первого входа передаётся только admin,
// а каталог запросов обновления задан одним фиксированным путём хоста без переопределения переменной.
func TestComposeAdminSetupTokenAndInbox(t *testing.T) {
	data, err := os.ReadFile("docker-compose.app.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	adminIdx := strings.Index(s, "\n  admin:\n")
	cronIdx := strings.Index(s, "\n  cron:\n")
	if adminIdx < 0 || cronIdx < adminIdx {
		t.Fatal("docker-compose.app.yml: не найдены секции admin и cron")
	}
	admin := s[adminIdx:cronIdx]
	if !strings.Contains(admin, "EMTDS_SETUP_TOKEN: ${EMTDS_SETUP_TOKEN:-}") {
		t.Fatal("admin: нет EMTDS_SETUP_TOKEN: ${EMTDS_SETUP_TOKEN:-}")
	}
	if rest := s[:adminIdx] + s[cronIdx:]; strings.Contains(rest, "EMTDS_SETUP_TOKEN") {
		t.Fatal("токен первого входа нужен только admin")
	}
	if !strings.Contains(admin, "      - /var/lib/emtds-update/inbox:/var/lib/emtds/update-inbox\n") {
		t.Fatal("admin: inbox должен монтироваться с фиксированного пути /var/lib/emtds-update/inbox")
	}
	if strings.Contains(s, "EMTDS_UPDATE_INBOX:-") || strings.Count(s, "emtds-update/inbox") != 1 {
		t.Fatal("путь inbox переопределяется переменной или задан не один раз")
	}
}
