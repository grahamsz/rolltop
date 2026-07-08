// File overview: Tests for BIMI plugin normalization and lookup behavior.

package bimi

import (
	"context"
	"database/sql"
	"net"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestDomainFromAddress(t *testing.T) {
	got := DomainFromAddress(`Brand Team <news@example.com>`)
	if got != "example.com" {
		t.Fatalf("domain = %q", got)
	}
}

func TestParseBIMITXT(t *testing.T) {
	fields := parseBIMITXT("v=BIMI1; l=https://brand.example/logo.svg; a=https://brand.example/vmc.pem")
	if fields["v"] != "BIMI1" || fields["l"] != "https://brand.example/logo.svg" {
		t.Fatalf("fields = %#v", fields)
	}
}

func TestValidateSVGRejectsActiveContent(t *testing.T) {
	if err := validateSVG(`<svg viewBox="0 0 1 1"><path d="M0 0h1v1z"/></svg>`); err != nil {
		t.Fatalf("safe svg rejected: %v", err)
	}
	if err := validateSVG(`<svg><script>alert(1)</script></svg>`); err == nil {
		t.Fatal("expected script-bearing svg to be rejected")
	}
}

func TestPublicIPRejectsPrivateAddresses(t *testing.T) {
	if publicIP(net.ParseIP("127.0.0.1")) {
		t.Fatal("loopback was treated as public")
	}
	if publicIP(net.ParseIP("10.0.0.1")) {
		t.Fatal("private IP was treated as public")
	}
	if !publicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public IP was rejected")
	}
}

func TestGetIconMetaReturnsScopedMetadata(t *testing.T) {
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
	if err := UpsertIcon(ctx, db, Icon{
		UserID:    7,
		Domain:    "example.com",
		LogoURL:   "https://example.com/logo.svg",
		SVG:       `<svg viewBox="0 0 1 1"><path d="M0 0h1v1z"/></svg>`,
		Status:    "ok",
		FetchedAt: now,
		ExpiresAt: now.Add(time.Hour),
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	meta, err := GetIconMeta(ctx, db, 7, "EXAMPLE.com")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Domain != "example.com" || meta.Status != "ok" || !meta.HasSVG {
		t.Fatalf("meta = %+v", meta)
	}
	if _, err := GetIconMeta(ctx, db, 8, "example.com"); err != sql.ErrNoRows {
		t.Fatalf("other user err = %v, want %v", err, sql.ErrNoRows)
	}
}
