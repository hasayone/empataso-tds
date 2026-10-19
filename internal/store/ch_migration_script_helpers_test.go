package store

import (
	"fmt"
	"regexp"
	"strings"
)

// splitCHMigration splits a migration file into executable statements.
// Comment lines and blank lines are skipped. A semicolon ends a statement.
func splitCHMigration(body string) []string {
	var out []string
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "--") {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		if strings.HasSuffix(trim, ";") {
			out = append(out, b.String())
			b.Reset()
		}
	}
	if tail := strings.TrimSpace(b.String()); tail != "" {
		out = append(out, tail)
	}
	return out
}

// substituteMigrationDB rewrites production database names to the integration db.
func substituteMigrationDB(db, script string) string {
	s := strings.ReplaceAll(script, "kuztds.", db+".")
	s = strings.ReplaceAll(s, "DATABASE IF NOT EXISTS kuztds", "DATABASE IF NOT EXISTS "+db)
	return s
}

// validateMigrationScript checks every statement before execution.
// Only CREATE DATABASE, CREATE TABLE and ALTER with ADD/MODIFY COLUMN or MODIFY TTL
// on the given db are allowed.
func validateMigrationScript(db, script string) error {
	body := substituteMigrationDB(db, script)
	for _, stmt := range splitCHMigration(body) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if err := validateMigrationStmt(db, stmt); err != nil {
			return fmt.Errorf("%w (%s)", err, migrationStmtPrefix(stmt, 120))
		}
	}
	return nil
}

func migrationStmtPrefix(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var migrationForbidden = regexp.MustCompile(`(?i)(^|\s)(USE|DROP|TRUNCATE|INSERT|DELETE|RENAME|OPTIMIZE|ATTACH|DETACH)\s`)

func validateMigrationStmt(db, stmt string) error {
	if migrationForbidden.FindStringIndex(stmt) != nil {
		return fmt.Errorf("forbidden statement keyword")
	}
	if regexp.MustCompile(`(?i)ALTER\s+TABLE\b[^;]*\bDELETE\b`).MatchString(stmt) {
		return fmt.Errorf("ALTER DELETE is not allowed in migrations")
	}
	if err := rejectForeignQualifiers(db, stmt); err != nil {
		return err
	}
	dbQual := migrationDBQualifier(db)
	flat := migrationCollapseWS(stmt)
	if regexp.MustCompile(`(?is)^CREATE\s+DATABASE\s+IF\s+NOT\s+EXISTS\s+` + dbQual + `\s*;?\s*$`).MatchString(flat) {
		return nil
	}
	if regexp.MustCompile(`(?is)^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+` + dbQual + `\.`).MatchString(stmt) {
		return nil
	}
	alterRe := regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+` + dbQual + `\.([a-zA-Z_][a-zA-Z0-9_]*)\s+(.*)$`)
	m := alterRe.FindStringSubmatch(stmt)
	if m == nil {
		return fmt.Errorf("statement not in migration allowlist")
	}
	return validateAlterMigrationBody(m[2])
}

func migrationDBQualifier(db string) string {
	esc := regexp.QuoteMeta(db)
	return `(?:` + esc + `|` + "`" + esc + "`" + `)`
}

func migrationCollapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func rejectForeignQualifiers(db, stmt string) error {
	if regexp.MustCompile(`(?i)\bdefault\.`).MatchString(stmt) {
		return fmt.Errorf("references default database")
	}
	if regexp.MustCompile("(?i)`kuztds`\\.").MatchString(stmt) {
		return fmt.Errorf("references production database kuztds")
	}
	if regexp.MustCompile(`(?i)\bkuztds\.(?:events|conversions|postbacks|postback_log)\b`).MatchString(stmt) {
		return fmt.Errorf("references production database kuztds")
	}
	if regexp.MustCompile(`(?i)\bKUZTDS\.`).MatchString(stmt) && !strings.EqualFold(db, "kuztds") {
		return fmt.Errorf("references foreign database qualifier")
	}
	if regexp.MustCompile(`(?i)ALTER\s+TABLE\s+(events|conversions|postbacks|postback_log)\b`).MatchString(stmt) {
		return fmt.Errorf("unqualified ALTER TABLE")
	}
	if regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(events|conversions|postbacks|postback_log)\b`).MatchString(stmt) {
		return fmt.Errorf("unqualified CREATE TABLE")
	}
	_ = db
	return nil
}

func validateAlterMigrationBody(body string) error {
	b := strings.TrimSpace(body)
	if b == "" {
		return fmt.Errorf("empty ALTER body")
	}
	if !regexp.MustCompile(`(?is)^(ADD\s+COLUMN|MODIFY\s+COLUMN|MODIFY\s+TTL)\b`).MatchString(b) {
		return fmt.Errorf("ALTER may only ADD COLUMN, MODIFY COLUMN or MODIFY TTL")
	}
	if regexp.MustCompile(`(?i)\b(DROP|DELETE|RENAME|ATTACH|DETACH|UPDATE)\b`).MatchString(b) {
		return fmt.Errorf("forbidden ALTER operation")
	}
	return nil
}
