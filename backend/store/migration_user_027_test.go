package store

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestMessageImportCompletionMigrationBackfillsExistingRows(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE mailboxes (
		id INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL,
		account_id INTEGER NOT NULL,
		uidvalidity INTEGER NOT NULL,
		last_uid INTEGER NOT NULL
	);
	CREATE TABLE messages (
		id INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL,
		account_id INTEGER NOT NULL,
		mailbox_id INTEGER NOT NULL,
		uid INTEGER NOT NULL,
		uid_validity INTEGER NOT NULL,
		attachment_indexed_at INTEGER NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	INSERT INTO mailboxes(id, user_id, account_id, uidvalidity, last_uid) VALUES
		(10, 1, 20, 7, 2),
		(11, 2, 21, 9, 5);
	INSERT INTO messages(id, user_id, account_id, mailbox_id, uid, uid_validity, attachment_indexed_at, created_at, updated_at) VALUES
		(1, 1, 20, 10, 1, 7, 100, 101, 102),
		(2, 1, 20, 10, 3, 7, 200, 201, 0),
		(3, 1, 20, 10, 2, 8, 300, 301, 302),
		(4, 2, 21, 11, 5, 9, 400, 401, 402),
		(5, 1, 20, 10, 2, 7, 0, 501, 502)`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range userMessageImportCompletionMigrationSet().Statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	want := map[int64]int64{1: 102, 2: 0, 3: 0, 4: 402, 5: 0}
	rows, err := db.QueryContext(ctx, `SELECT id, import_completed_at FROM messages ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, completedAt int64
		if err := rows.Scan(&id, &completedAt); err != nil {
			t.Fatal(err)
		}
		if completedAt != want[id] {
			t.Fatalf("message %d import_completed_at=%d, want %d", id, completedAt, want[id])
		}
		delete(want, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(want) != 0 {
		t.Fatalf("messages not checked: %v", want)
	}
}
