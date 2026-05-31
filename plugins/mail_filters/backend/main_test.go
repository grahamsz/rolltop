package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestOlderThanClauseStripsAgeOperator(t *testing.T) {
	age, ok := olderThanClause("from:studio@example.test older_than:7d subject:Yoga")
	if !ok {
		t.Fatal("older_than clause not found")
	}
	if got := age.Duration.Hours() / 24; got != 7 {
		t.Fatalf("duration days = %v, want 7", got)
	}
	if age.QueryWithoutClause != "from:studio@example.test subject:Yoga" {
		t.Fatalf("query without clause = %q", age.QueryWithoutClause)
	}
}

func TestEvaluationListsSeparateManagementFromMessageAudit(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE plugin_mail_filter_rules (
		id INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		query TEXT NOT NULL,
		enabled INTEGER NOT NULL,
		scope_mode TEXT NOT NULL,
		actions_json TEXT NOT NULL,
		position INTEGER NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE TABLE messages (
		id INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL,
		subject TEXT NOT NULL,
		from_addr TEXT NOT NULL
	);
	CREATE TABLE plugin_mail_filter_evaluations (
		id INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL,
		rule_id INTEGER NOT NULL,
		message_id INTEGER NOT NULL,
		account_id INTEGER NOT NULL,
		mailbox_id INTEGER NOT NULL,
		phase TEXT NOT NULL,
		status TEXT NOT NULL,
		matched INTEGER NOT NULL,
		due_at INTEGER NOT NULL,
		evaluated_at INTEGER NOT NULL,
		terms_json TEXT NOT NULL,
		fields_json TEXT NOT NULL,
		actions_json TEXT NOT NULL,
		error TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	if _, err := db.Exec(`INSERT INTO plugin_mail_filter_rules (id, user_id, name, query, enabled, scope_mode, actions_json, position, created_at, updated_at) VALUES (10, 1, 'Yoga cleanup', 'older_than:7d yoga', 1, 'all_accounts', '{}', 0, ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO messages (id, user_id, subject, from_addr) VALUES (100, 1, 'Yoga booking', 'studio@example.test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_mail_filter_evaluations
		(id, user_id, rule_id, message_id, account_id, mailbox_id, phase, status, matched, due_at, evaluated_at, terms_json, fields_json, actions_json, error, created_at)
		VALUES
		(1, 1, 10, 100, 2, 3, 'backfill', 'not_matched', 0, 0, ?, '[]', '[]', '{}', '', ?),
		(2, 1, 10, 100, 2, 3, 'backfill', 'matched', 1, 0, ?, '[]', '[]', '{"move":"ok"}', '', ?)`, now-1, now-1, now, now); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	recent, err := listRecentEvaluations(ctx, db, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || !recent[0].Matched {
		t.Fatalf("recent evaluations = %+v, want only matched rows", recent)
	}
	messageRows, err := listMessageEvaluations(ctx, db, 1, 100, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(messageRows) != 2 {
		t.Fatalf("message evaluations len = %d, want 2", len(messageRows))
	}
}

func TestEnsureForwarderIDIsStableAndOpaque(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE plugin_mail_filter_forwarders (
		user_id INTEGER NOT NULL,
		account_id INTEGER NOT NULL,
		forwarder_id TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY(user_id, account_id)
	)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := ensureForwarderID(ctx, db, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ensureForwarderID(ctx, db, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("forwarder id changed: %q then %q", first, second)
	}
	if len(first) != len("rtf-")+32 {
		t.Fatalf("forwarder id = %q", first)
	}
}
