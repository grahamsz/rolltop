package store

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestMailboxGenerationArrivalFloorMigrationPreservesUnprovenMessages(t *testing.T) {
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
		uidvalidity INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE messages (
		id INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL,
		account_id INTEGER NOT NULL,
		mailbox_id INTEGER NOT NULL,
		uid_validity INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE mailbox_generation_rebuilds (
		id INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL,
		account_id INTEGER NOT NULL,
		mailbox_id INTEGER NOT NULL
	);
	INSERT INTO mailboxes(id, user_id, account_id, uidvalidity) VALUES
		(1, 10, 101, 777),
		(2, 10, 101, 0),
		(3, 20, 202, 888),
		(4, 10, 101, 999);
	INSERT INTO messages(id, user_id, account_id, mailbox_id, uid_validity) VALUES
		(11, 10, 101, 1, 0),
		(12, 10, 101, 1, 555),
		(13, 10, 101, 2, 0),
		(14, 10, 101, 4, 0),
		(15, 10, 999, 1, 0),
		(16, 20, 202, 3, 0),
		(17, 10, 101, 3, 0);
	INSERT INTO mailbox_generation_rebuilds(id, user_id, account_id, mailbox_id)
		VALUES (1, 10, 101, 4)`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range userMailboxGenerationArrivalFloorMigrationSet().Statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	want := map[int64]int64{
		11: 0,
		12: 555,
		13: 0,
		14: 0,
		15: 0,
		16: 0,
		17: 0,
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
		if validity != want[id] {
			t.Fatalf("message %d uid_validity=%d, want %d", id, validity, want[id])
		}
		delete(want, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(want) != 0 {
		t.Fatalf("messages not checked: %v", want)
	}
	var arrivalUIDFloor int64
	if err := db.QueryRowContext(ctx, `SELECT arrival_uid_floor
		FROM mailbox_generation_rebuilds WHERE id = 1`).Scan(&arrivalUIDFloor); err != nil {
		t.Fatal(err)
	}
	if arrivalUIDFloor != 0 {
		t.Fatalf("existing rebuild arrival_uid_floor=%d, want 0", arrivalUIDFloor)
	}
}
