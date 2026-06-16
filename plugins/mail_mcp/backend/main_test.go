package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

func TestMailMessageGetRequiresUserOwnedMessage(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	first := createTestMessage(t, ctx, db, "first@example.test", "First message", 1)
	second := createTestMessage(t, ctx, db, "second@example.test", "Second message", 2)

	if _, err := mailMessageGet(ctx, db, first.UserID, messageID(first.ID), "metadata"); err != nil {
		t.Fatalf("own message lookup failed: %v", err)
	}
	if _, err := mailMessageGet(ctx, db, first.UserID, messageID(second.ID), "metadata"); !store.IsNotFound(err) {
		t.Fatalf("cross-user message lookup err = %v, want not found", err)
	}
}

func TestListMessagesRequiresUserOwnedMailbox(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	first := createTestMessage(t, ctx, db, "first@example.test", "First message", 1)
	second := createTestMessage(t, ctx, db, "second@example.test", "Second message", 2)

	messages, _, err := listMessages(ctx, db, first.UserID, listArgs{LabelIDs: []string{mailLabelID(first.MailboxID)}}, false)
	if err != nil {
		t.Fatalf("own mailbox list failed: %v", err)
	}
	if len(messages) != 1 || messages[0].ID != first.ID {
		t.Fatalf("own mailbox messages = %+v, want only %d", messages, first.ID)
	}
	if _, _, err := listMessages(ctx, db, first.UserID, listArgs{LabelIDs: []string{mailLabelID(second.MailboxID)}}, false); !store.IsNotFound(err) {
		t.Fatalf("cross-user mailbox list err = %v, want not found", err)
	}
}

func TestBearerUserIDRequiresIssuedAccessToken(t *testing.T) {
	p := &mailMCPPlugin{access: map[string]oauthToken{}}
	req := httptest.NewRequest("POST", "/api/plugins/mail_mcp/mcp", nil)
	req.Header.Set("Authorization", "Bearer missing")
	if _, ok := p.bearerUserID(req); ok {
		t.Fatal("missing token authenticated")
	}

	p.access[codeHash("valid-token")] = oauthToken{UserID: 42, ClientID: "test", Scope: "mail.readonly", ExpiresAt: time.Now().Add(time.Minute)}
	req.Header.Set("Authorization", "Bearer valid-token")
	userID, ok := p.bearerUserID(req)
	if !ok || userID != 42 {
		t.Fatalf("valid token userID=%d ok=%t, want 42 true", userID, ok)
	}
}

func TestGrantRevokeClearsActiveTokens(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createGrantTable(t, ctx, db)
	user, err := db.CreateUser(ctx, "grant@example.test", "Grant", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := upsertMailMCPGrant(ctx, db, user.ID, "chatgpt", "mail.readonly", "https://chat.openai.com/callback")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := mailMCPGrantActive(ctx, db, user.ID, grant.ID); err != nil || !ok {
		t.Fatalf("active=%t err=%v, want active grant", ok, err)
	}
	p := &mailMCPPlugin{
		access:  map[string]oauthToken{codeHash("access"): {UserID: user.ID, GrantID: grant.ID, ClientID: "chatgpt", Scope: "mail.readonly", ExpiresAt: time.Now().Add(time.Hour)}},
		refresh: map[string]oauthToken{codeHash("refresh"): {UserID: user.ID, GrantID: grant.ID, ClientID: "chatgpt", Scope: "mail.readonly", ExpiresAt: time.Now().Add(time.Hour)}},
	}
	if err := revokeMailMCPGrant(ctx, db, user.ID, grant.ID); err != nil {
		t.Fatal(err)
	}
	p.revokeTokens(user.ID, grant.ID)
	if ok, err := mailMCPGrantActive(ctx, db, user.ID, grant.ID); err != nil || ok {
		t.Fatalf("active=%t err=%v, want revoked grant", ok, err)
	}
	if len(p.access) != 0 || len(p.refresh) != 0 {
		t.Fatalf("tokens after revoke access=%d refresh=%d, want none", len(p.access), len(p.refresh))
	}
}

func TestAuthorizationServerMetadataUsesRequestBaseURL(t *testing.T) {
	p := &mailMCPPlugin{}
	req := httptest.NewRequest("GET", "/api/plugins/mail_mcp/.well-known/oauth-authorization-server", nil)
	req.Host = "rolltop.example.test"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()

	p.authorizationServerMetadata(testAPIHost{}, "", rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["issuer"] != "https://rolltop.example.test/api/plugins/mail_mcp" {
		t.Fatalf("issuer = %v", body["issuer"])
	}
	if body["authorization_endpoint"] != "https://rolltop.example.test/api/plugins/mail_mcp/oauth/authorize" {
		t.Fatalf("authorization_endpoint = %v", body["authorization_endpoint"])
	}
	if body["registration_endpoint"] != "https://rolltop.example.test/api/plugins/mail_mcp/oauth/register" {
		t.Fatalf("registration_endpoint = %v", body["registration_endpoint"])
	}
}

func TestRegisterClientReturnsPublicClientID(t *testing.T) {
	p := &mailMCPPlugin{}
	req := httptest.NewRequest("POST", "/api/plugins/mail_mcp/oauth/register", strings.NewReader(`{
		"client_name": "ChatGPT",
		"redirect_uris": ["https://chat.openai.com/aip/oauth/callback"],
		"token_endpoint_auth_method": "none"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	p.registerClient(testAPIHost{}, "", rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	clientID, _ := body["client_id"].(string)
	if !strings.HasPrefix(clientID, "mail-mcp-") {
		t.Fatalf("client_id = %q", clientID)
	}
	if body["token_endpoint_auth_method"] != "none" {
		t.Fatalf("token_endpoint_auth_method = %v", body["token_endpoint_auth_method"])
	}
}

func TestValidPKCEVerifierS256(t *testing.T) {
	verifier := "pkce-verifier"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	if !validPKCEVerifier(verifier, challenge, "S256") {
		t.Fatal("S256 verifier was rejected")
	}
	if validPKCEVerifier("wrong", challenge, "S256") {
		t.Fatal("wrong verifier was accepted")
	}
}

func TestMCPAuthenticateHeaderIncludesResourceMetadata(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/plugins/mail_mcp/mcp", nil)
	req.Host = "rolltop.example.test"
	req.Header.Set("X-Forwarded-Proto", "https")

	header := mailMCPAuthenticateHeader(req, "invalid_token")

	if !strings.Contains(header, `resource_metadata="https://rolltop.example.test/api/plugins/mail_mcp/.well-known/oauth-protected-resource"`) {
		t.Fatalf("WWW-Authenticate header missing resource metadata: %s", header)
	}
	if !strings.Contains(header, `error="invalid_token"`) {
		t.Fatalf("WWW-Authenticate header missing error: %s", header)
	}
}

func TestConsentTokenValidation(t *testing.T) {
	rawURL := "/api/plugins/mail_mcp/oauth/authorize?response_type=code&client_id=mail-mcp-3_j23z1tMQTM_ixPjDSDJh6yje33nej-l-dTrD45s04&redirect_uri=https%3A%2F%2Fchatgpt.com%2Fconnector%2Foauth%2Fr1-r3wGxJrBd&scope=mail.readonly&code_challenge=kb8ZNqBN70AFtilmuQTRg1XsBaqiybACBqNTRn-Y5uM&code_challenge_method=S256&resource=https%3A%2F%2Fmail.theparkerstewarts.com%2Fapi%2Fplugins%2Fmail_mcp%2Fmcp&state=oauth_s_6a31d2a5e2888191a48771d5522ead45&ui_locales=en-US"
	host := testAPIHost{}
	getReq := httptest.NewRequest("GET", rawURL, nil)
	token, err := newConsentToken(host, getReq)
	if err != nil {
		t.Fatal(err)
	}

	postReq := httptest.NewRequest("POST", rawURL, nil)
	postReq.Form = map[string][]string{"consent_token": {token}}
	if !validConsentToken(host, postReq) {
		t.Fatal("matching consent token was rejected")
	}

	tamperedReq := httptest.NewRequest("POST", rawURL+"&scope=other", nil)
	tamperedReq.Form = map[string][]string{"consent_token": {token}}
	if validConsentToken(host, tamperedReq) {
		t.Fatal("consent token was accepted for a different OAuth query")
	}
}

func TestListMessagesSupportsDateQuery(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user, account, mailbox, blob := createTestMailbox(t, ctx, db, "range@example.test")
	_ = createMailboxMessage(t, ctx, db, user.ID, account.ID, mailbox.ID, blob.ID, "Old message", 1, time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC))
	inRange := createMailboxMessage(t, ctx, db, user.ID, account.ID, mailbox.ID, blob.ID, "Range message", 2, time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC))
	_ = createMailboxMessage(t, ctx, db, user.ID, account.ID, mailbox.ID, blob.ID, "New message", 3, time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC))

	messages, _, err := listMessages(ctx, db, user.ID, listArgs{Q: "after:2026/6/16 before:2026/6/17", MaxResults: 20}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != inRange.ID {
		t.Fatalf("messages = %+v, want only %d", messages, inRange.ID)
	}
}

func createTestMessage(t *testing.T, ctx context.Context, db *store.Store, email, subject string, uid uint32) store.MessageRecord {
	return createTestMessageAt(t, ctx, db, email, subject, uid, time.Now())
}

func createTestMessageAt(t *testing.T, ctx context.Context, db *store.Store, email, subject string, uid uint32, date time.Time) store.MessageRecord {
	t.Helper()
	user, account, mailbox, blob := createTestMailbox(t, ctx, db, email)
	return createMailboxMessage(t, ctx, db, user.ID, account.ID, mailbox.ID, blob.ID, subject, uid, date)
}

func createTestMailbox(t *testing.T, ctx context.Context, db *store.Store, email string) (store.User, store.MailAccount, store.Mailbox, store.BlobRecord) {
	t.Helper()
	user, err := db.CreateUser(ctx, email, email, "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID:            user.ID,
		Email:             email,
		Host:              "imap.example.test",
		Port:              993,
		Username:          email,
		EncryptedPassword: "secret",
		UseTLS:            true,
		Mailbox:           store.DefaultMailboxPattern,
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: user.ID,
		Kind:   "message",
		Path:   filepath.Join("accounts", "1", "mailboxes", "INBOX", email+".eml"),
		SHA256: email,
		Size:   64,
	})
	if err != nil {
		t.Fatal(err)
	}
	return user, account, mailbox, blob
}

func createGrantTable(t *testing.T, ctx context.Context, db *store.Store) {
	t.Helper()
	_, err := db.DB().ExecContext(ctx, `CREATE TABLE IF NOT EXISTS plugin_mail_mcp_grants (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		client_id TEXT NOT NULL DEFAULT '',
		scope TEXT NOT NULL DEFAULT '',
		redirect_uri TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		last_used_at INTEGER NOT NULL DEFAULT 0,
		revoked_at INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.DB().ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_plugin_mail_mcp_grants_user_client_scope ON plugin_mail_mcp_grants(user_id, client_id, scope)`)
	if err != nil {
		t.Fatal(err)
	}
}

func createMailboxMessage(t *testing.T, ctx context.Context, db *store.Store, userID, accountID, mailboxID, blobID int64, subject string, uid uint32, date time.Time) store.MessageRecord {
	t.Helper()
	msg, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID:          userID,
		AccountID:       accountID,
		MailboxID:       mailboxID,
		BlobID:          blobID,
		MessageIDHeader: "<" + subject + "@example.test>",
		Subject:         subject,
		FromAddr:        "sender@example.test",
		ToAddr:          "recipient@example.test",
		Date:            date,
		InternalDate:    date,
		UID:             uid,
		Size:            64,
		BlobPath:        filepath.Join("accounts", "1", "mailboxes", "INBOX", subject+".eml"),
		BodyText:        "body for " + subject,
	})
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

type testAPIHost struct {
	plugins.APIHost
}

func (testAPIHost) MasterKey() []byte {
	return []byte("test master key")
}

func (testAPIHost) WriteJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
