package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestInboxArrivalMigrationLeavesLegacyMessageUIDValidityUnproven(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE messages (
		id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, mailbox_id INTEGER NOT NULL
	); INSERT INTO messages(id, user_id, mailbox_id) VALUES (11, 10, 1), (12, 20, 2)`); err != nil {
		t.Fatal(err)
	}
	var addUIDValidity string
	for _, statement := range userInboxArrivalClassificationMigrationSet().Statements {
		if strings.HasPrefix(strings.TrimSpace(statement), "ALTER TABLE messages ADD COLUMN uid_validity") {
			addUIDValidity = statement
			break
		}
	}
	if addUIDValidity == "" {
		t.Fatal("UIDVALIDITY migration statement is missing")
	}
	if _, err := db.ExecContext(ctx, addUIDValidity); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT id, uid_validity FROM messages ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, validity int64
		if err := rows.Scan(&id, &validity); err != nil {
			t.Fatal(err)
		}
		if validity != 0 {
			t.Fatalf("legacy message %d uid_validity=%d, want unproven 0", id, validity)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
