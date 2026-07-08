package gravatar

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestGetImageMetaReturnsScopedMetadata(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range Migrations()[0].Statements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	hash := Hash("sender@example.com")
	if err := UpsertImage(ctx, db, Image{
		UserID:      7,
		EmailHash:   hash,
		ContentType: "image/png",
		Image:       []byte{1, 2, 3, 4},
		Status:      "ok",
		FetchedAt:   now,
		ExpiresAt:   now.Add(time.Hour),
		UpdatedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}
	meta, err := GetImageMeta(ctx, db, 7, hash)
	if err != nil {
		t.Fatal(err)
	}
	if meta.EmailHash != hash || meta.ContentType != "image/png" || meta.Status != "ok" || !meta.HasImage {
		t.Fatalf("meta = %+v", meta)
	}
	if _, err := GetImageMeta(ctx, db, 8, hash); err != sql.ErrNoRows {
		t.Fatalf("other user err = %v, want %v", err, sql.ErrNoRows)
	}
}
