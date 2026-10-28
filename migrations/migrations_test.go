// Package migrations contains the reviewed, versioned Goose SQL migrations
// that are the schema authority for the application (STACK_DECISIONS §6,
// PLATFORM_DECISIONS §D).
//
// The tests in this file are deliberately lint-style structural guards. They do
// not execute SQL: real migration, constraint and upgrade testing against a
// disposable PostgreSQL 18.x instance is the persistence-adapter/next-task
// work. What they can guarantee without a database is that every migration is
// well formed at the section level, that foreign keys point at tables that
// exist, that money columns stay integer minor units with the domain
// MaxMinorUnits bound, and that the journal stays append-only in shape.
package migrations

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	gooseUp   = "-- +goose Up"
	gooseDown = "-- +goose Down"

	// maxMinorUnitsLiteral mirrors domain.MaxMinorUnits (10^15). Keeping the
	// literal here makes an accidental schema change fail the lint test.
	maxMinorUnitsLiteral = "1000000000000000"
)

// migrationFile is one .sql file split into its Goose sections.
type migrationFile struct {
	name string
	up   string
	down string
}

// loadMigrations reads every .sql file in the migrations directory, sorted by
// filename so Goose version order matches the test order.
func loadMigrations(t *testing.T) []migrationFile {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read migrations directory: %v", err)
	}

	var files []migrationFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		content, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		raw := string(content)
		upIndex := strings.Index(raw, gooseUp)
		downIndex := strings.Index(raw, gooseDown)
		if upIndex < 0 || downIndex < 0 {
			// Section checks are a separate test so this helper stays usable
			// for diagnostics; return the file with empty sections instead.
			files = append(files, migrationFile{name: entry.Name()})
			continue
		}
		if downIndex < upIndex {
			t.Fatalf("%s: %q appears before %q", entry.Name(), gooseDown, gooseUp)
		}
		files = append(files, migrationFile{
			name: entry.Name(),
			up:   raw[upIndex+len(gooseUp) : downIndex],
			down: raw[downIndex+len(gooseDown):],
		})
	}

	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	if len(files) == 0 {
		t.Fatal("no .sql migration files found")
	}
	return files
}

// stripLineComments removes SQL line comments. Every migration string literal
// in this schema is comment-free, so a simple "--" scan is sufficient for the
// structural checks below.
func stripLineComments(sql string) string {
	var b strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if index := strings.Index(line, "--"); index >= 0 {
			line = line[:index]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

var (
	createTableRe  = regexp.MustCompile(`(?mi)^CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)\s*\(`)
	referencesRe   = regexp.MustCompile(`(?i)\bREFERENCES\s+([a-z_][a-z0-9_]*)\s*\(`)
	dropTableRe    = regexp.MustCompile(`(?mi)^DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)
	moneyColumnRe  = regexp.MustCompile(`(?mi)^\s*([a-z_][a-z0-9_]*_minor)\s+([a-z]+)`)
	householdColRe = regexp.MustCompile(`(?mi)^\s*household_id\s+uuid\b`)
)

// tableNames returns the created table names in statement order.
func tableNames(sql string) []string {
	matches := createTableRe.FindAllStringSubmatch(stripLineComments(sql), -1)
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match[1])
	}
	return names
}

// tableBody returns the parenthesized body of CREATE TABLE name, or "" when no
// such table is defined.
func tableBody(sql, name string) string {
	clean := stripLineComments(sql)
	re := regexp.MustCompile(`(?mi)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` + regexp.QuoteMeta(name) + `\s*\(`)
	location := re.FindStringIndex(clean)
	if location == nil {
		return ""
	}
	start := location[1] - 1 // position of the opening "("
	depth := 0
	for i := start; i < len(clean); i++ {
		switch clean[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return clean[start+1 : i]
			}
		}
	}
	return ""
}

// columnSection returns the column-definition prefix of a table body, stopping
// at the first table-level constraint. The schema lists all columns before its
// table constraints, so this avoids mistaking CHECK expression lines for
// column definitions.
func columnSection(body string) string {
	tableConstraintRe := regexp.MustCompile(`(?mi)^\s*(?:CONSTRAINT|CHECK|PRIMARY\s+KEY|UNIQUE)\b`)
	if location := tableConstraintRe.FindStringIndex(body); location != nil {
		return body[:location[0]]
	}
	return body
}

// checkExpressions returns every CHECK (...) expression's contents, including
// nested parentheses.
func checkExpressions(sql string) []string {
	clean := stripLineComments(sql)
	var expressions []string
	for offset := 0; offset < len(clean); {
		index := strings.Index(strings.ToUpper(clean[offset:]), "CHECK")
		if index < 0 {
			break
		}
		position := offset + index
		// Require a word boundary so a column or table name containing "check"
		// cannot be mistaken for a constraint keyword.
		beforeOK := position == 0 || !isIdentifierByte(clean[position-1])
		cursor := position + len("CHECK")
		for cursor < len(clean) && (clean[cursor] == ' ' || clean[cursor] == '\t' || clean[cursor] == '\n' || clean[cursor] == '\r') {
			cursor++
		}
		if !beforeOK || cursor >= len(clean) || clean[cursor] != '(' {
			offset = position + len("CHECK")
			continue
		}
		end := matchingParen(clean, cursor)
		if end < 0 {
			break
		}
		expressions = append(expressions, clean[cursor+1:end])
		offset = end + 1
	}
	return expressions
}

// matchingParen returns the index of the ")" matching the "(" at start, or -1.
func matchingParen(sql string, start int) int {
	depth := 0
	for i := start; i < len(sql); i++ {
		switch sql[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func isIdentifierByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// TestEveryFileHasGooseSections guards the migration file format itself.
func TestEveryFileHasGooseSections(t *testing.T) {
	for _, file := range loadMigrations(t) {
		if file.up == "" {
			t.Errorf("%s: missing %q section or empty Up body", file.name, gooseUp)
		}
		if strings.TrimSpace(file.down) == "" {
			t.Errorf("%s: missing %q section or empty Down body", file.name, gooseDown)
		}
		if strings.Count(file.up+file.down, gooseUp) > 0 || strings.Count(file.up+file.down, gooseDown) > 0 {
			t.Errorf("%s: Goose section markers must appear exactly once each", file.name)
		}
	}
}

// TestForeignKeysReferenceKnownTables ensures every REFERENCES target is a
// table created in the same or an earlier migration file. This is the
// no-database equivalent of applying the migration chain in order.
func TestForeignKeysReferenceKnownTables(t *testing.T) {
	known := make(map[string]int) // table -> file index
	for index, file := range loadMigrations(t) {
		up := stripLineComments(file.up)
		for _, name := range tableNames(file.up) {
			if previous, exists := known[name]; exists {
				t.Errorf("%s: table %q already created in migration file index %d", file.name, name, previous)
			}
			known[name] = index
		}
		for _, match := range referencesRe.FindAllStringSubmatch(up, -1) {
			target := match[1]
			targetIndex, exists := known[target]
			if !exists {
				t.Errorf("%s: foreign key references unknown table %q", file.name, target)
				continue
			}
			if targetIndex > index {
				t.Errorf("%s: foreign key references %q from a later file", file.name, target)
			}
		}
	}
}

// TestDownDropsEveryTable protects the test-teardown path from drifting behind
// the Up section.
func TestDownDropsEveryTable(t *testing.T) {
	var upTables, downTables []string
	for _, file := range loadMigrations(t) {
		upTables = append(upTables, tableNames(file.up)...)
		for _, match := range dropTableRe.FindAllStringSubmatch(stripLineComments(file.down), -1) {
			downTables = append(downTables, match[1])
		}
	}
	dropped := make(map[string]bool, len(downTables))
	for _, name := range downTables {
		dropped[name] = true
	}
	for _, name := range upTables {
		if !dropped[name] {
			t.Errorf("table %q is created but never dropped in a Down section", name)
		}
	}
}

// TestEveryHouseholdOwnedTableCarriesHouseholdID reinforces the isolation
// decision in STACK_DECISIONS §3. households is the household itself and users
// is the global authentication identity, so neither carries household_id.
func TestEveryHouseholdOwnedTableCarriesHouseholdID(t *testing.T) {
	for _, file := range loadMigrations(t) {
		for _, name := range tableNames(file.up) {
			if name == "households" || name == "users" {
				continue
			}
			body := tableBody(file.up, name)
			if !householdColRe.MatchString(body) {
				t.Errorf("%s: table %q is household-owned but has no household_id uuid column", file.name, name)
			}
		}
	}
}

// TestMoneyColumnsAreIntegerMinorUnits enforces Q8 at the schema level: money
// columns are BIGINT minor units, never floating point or NUMERIC, and each
// money column has a CHECK carrying the 10^15 domain bound.
func TestMoneyColumnsAreIntegerMinorUnits(t *testing.T) {
	numericTypeRe := regexp.MustCompile(`(?i)\b(numeric|decimal|real|double\s+precision|float\d*)\b`)

	for _, file := range loadMigrations(t) {
		up := stripLineComments(file.up)
		if match := numericTypeRe.FindString(up); match != "" {
			t.Errorf("%s: non-integer SQL type %q is not allowed for money", file.name, match)
		}

		for _, name := range tableNames(file.up) {
			body := tableBody(file.up, name)
			checks := strings.Join(checkExpressions(body), "\n")
			for _, match := range moneyColumnRe.FindAllStringSubmatch(columnSection(body), -1) {
				column, columnType := match[1], strings.ToLower(match[2])
				if columnType != "bigint" {
					t.Errorf("%s.%s: money column type is %q, want bigint minor units", name, column, columnType)
				}
				if !strings.Contains(checks, column) || !strings.Contains(checks, maxMinorUnitsLiteral) {
					t.Errorf("%s.%s: no CHECK expression enforces the %s minor-unit bound", name, column, maxMinorUnitsLiteral)
				}
			}
		}
	}
}

// TestJournalSchemaIsAppendOnly checks the journal-side tables have no
// updated_at column; committed rows have no update path by design.
func TestJournalSchemaIsAppendOnly(t *testing.T) {
	appendOnlyTables := []string{
		"journal_operations",
		"account_postings",
		"envelope_postings",
		"reservation_changes",
		"related_links",
	}

	for _, file := range loadMigrations(t) {
		updatedAtRe := regexp.MustCompile(`(?mi)^\s*updated_at\b`)
		for _, name := range appendOnlyTables {
			body := tableBody(file.up, name)
			if body == "" {
				continue
			}
			if updatedAtRe.MatchString(body) {
				t.Errorf("%s: append-only table %q must not have updated_at", file.name, name)
			}
		}
	}
}

// TestIdempotencyKeysAreUnique verifies the database-level idempotency
// guarantee exists on both the journal and the idempotency-record table.
func TestIdempotencyKeysAreUnique(t *testing.T) {
	journalUniqueRe := regexp.MustCompile(`(?is)UNIQUE\s*\(\s*household_id\s*,\s*idempotency_key\s*\)`)
	recordsPrimaryRe := regexp.MustCompile(`(?is)PRIMARY\s+KEY\s*\(\s*household_id\s*,\s*idempotency_key\s*\)`)

	for _, file := range loadMigrations(t) {
		journal := tableBody(file.up, "journal_operations")
		if journal == "" {
			t.Errorf("%s: journal_operations table not found", file.name)
		} else if !journalUniqueRe.MatchString(journal) {
			t.Errorf("%s: journal_operations must have UNIQUE (household_id, idempotency_key)", file.name)
		}

		records := tableBody(file.up, "idempotency_records")
		if records == "" {
			t.Errorf("%s: idempotency_records table not found", file.name)
		} else if !recordsPrimaryRe.MatchString(records) {
			t.Errorf("%s: idempotency_records must have PRIMARY KEY (household_id, idempotency_key)", file.name)
		}
	}
}
