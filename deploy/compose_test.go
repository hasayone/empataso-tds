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
