package jira

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The repo's DB rules (root CLAUDE.md) are hard requirements: no foreign keys
// or cascades anywhere, and every CONCURRENTLY index build must be the only
// statement in its migration file. This guard pins them for the jira_* block
// so a later story cannot regress the conventions silently.

func jiraMigrationFiles(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	files := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if !strings.Contains(name, "_jira_") || !strings.HasSuffix(name, ".sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		files[name] = string(body)
	}
	if len(files) == 0 {
		t.Fatal("no jira migrations found; expected the 202+ block")
	}
	return files
}

func TestJiraMigrationsHaveNoForeignKeysOrCascades(t *testing.T) {
	banned := regexp.MustCompile(`(?i)\b(FOREIGN\s+KEY|REFERENCES|ON\s+DELETE\s+CASCADE|ON\s+UPDATE\s+CASCADE)\b`)
	comment := regexp.MustCompile(`(?m)--[^\n]*`)
	for name, body := range jiraMigrationFiles(t) {
		sqlOnly := comment.ReplaceAllString(body, "")
		if loc := banned.FindString(sqlOnly); loc != "" {
			t.Errorf("%s contains banned construct %q (repo DB rules: no FKs/cascades)", name, loc)
		}
	}
}

func TestJiraMigrationsUpDownPairs(t *testing.T) {
	files := jiraMigrationFiles(t)
	for name := range files {
		var counterpart string
		switch {
		case strings.HasSuffix(name, ".up.sql"):
			counterpart = strings.TrimSuffix(name, ".up.sql") + ".down.sql"
		case strings.HasSuffix(name, ".down.sql"):
			counterpart = strings.TrimSuffix(name, ".down.sql") + ".up.sql"
		default:
			t.Errorf("%s is neither .up.sql nor .down.sql", name)
			continue
		}
		if _, ok := files[counterpart]; !ok {
			t.Errorf("%s has no counterpart %s", name, counterpart)
		}
	}
}

func TestJiraConcurrentIndexMigrationsAreSingleStatement(t *testing.T) {
	comment := regexp.MustCompile(`(?m)^\s*--[^\n]*$`)
	for name, body := range jiraMigrationFiles(t) {
		if !strings.Contains(strings.ToUpper(body), "CONCURRENTLY") {
			continue
		}
		stripped := strings.TrimSpace(comment.ReplaceAllString(body, ""))
		// One trailing semicolon = one statement.
		if got := strings.Count(stripped, ";"); got != 1 {
			t.Errorf("%s uses CONCURRENTLY but contains %d statements; must be exactly one", name, got)
		}
	}
}

func TestJiraMigrationNumbersStartAt202AndAreUnique(t *testing.T) {
	prefix := regexp.MustCompile(`^(\d+)_`)
	seen := map[string]string{}
	for name := range jiraMigrationFiles(t) {
		m := prefix.FindStringSubmatch(name)
		if m == nil {
			t.Errorf("%s lacks the numeric NNN_ prefix", name)
			continue
		}
		if m[1] < "202" {
			t.Errorf("%s uses number %s below the reserved 202+ block", name, m[1])
		}
		stem := strings.TrimSuffix(strings.TrimSuffix(name, ".up.sql"), ".down.sql")
		if prev, ok := seen[m[1]]; ok && prev != stem {
			t.Errorf("numeric prefix %s used by both %s and %s (post-148 numbers must be unique)", m[1], prev, stem)
		}
		seen[m[1]] = stem
	}
}
