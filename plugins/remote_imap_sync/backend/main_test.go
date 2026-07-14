package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/imapclient"
	"rolltop/backend/plugins"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

var testMasterKey = []byte("0123456789abcdef0123456789abcdef")

type testAPIHost struct {
	plugins.APIHost
}

func (testAPIHost) MasterKey() []byte { return testMasterKey }

type routeAPIHost struct {
	st     *store.Store
	userID int64
}

func (h routeAPIHost) Store() any                                 { return h.st }
func (h routeAPIHost) MasterKey() []byte                          { return testMasterKey }
func (h routeAPIHost) PluginEnabled(context.Context, string) bool { return true }
func (h routeAPIHost) RequireAPIAuth(http.ResponseWriter, *http.Request) (plugins.CurrentUser, bool) {
	return plugins.CurrentUser{UserID: h.userID}, h.userID > 0
}
func (h routeAPIHost) LoginUserID(http.ResponseWriter, *http.Request, int64) error { return nil }
func (h routeAPIHost) VerifyCSRF(http.ResponseWriter, *http.Request) bool          { return true }
func (h routeAPIHost) DecodeJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	if err := json.NewDecoder(r.Body).Decode(value); err != nil {
		h.WriteAPIError(w, http.StatusBadRequest, "invalid JSON")
		return false
	}
	return true
}
func (routeAPIHost) WriteJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func (routeAPIHost) WriteAPIError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
func (h routeAPIHost) ServerError(w http.ResponseWriter, err error) {
	h.WriteAPIError(w, http.StatusInternalServerError, err.Error())
}

type backendFixture struct {
	store        *store.Store
	db           *sql.DB
	owner        store.User
	other        store.User
	ownerAccount store.MailAccount
	otherAccount store.MailAccount
	ownerMailbox store.Mailbox
	otherMailbox store.Mailbox
}

func newBackendFixture(t *testing.T) backendFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	owner, err := st.CreateUser(ctx, "owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, "other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	ownerAccount, err := st.CreateMailAccount(ctx, store.MailAccount{
		UserID: owner.ID, Email: owner.Email, Label: "Owner mail", Host: "imap.mxroute.test",
		Port: 993, Username: owner.Email, EncryptedPassword: "encrypted-owner", UseTLS: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherAccount, err := st.CreateMailAccount(ctx, store.MailAccount{
		UserID: other.ID, Email: other.Email, Label: "Other mail", Host: "imap.other.test",
		Port: 993, Username: other.Email, EncryptedPassword: "encrypted-other", UseTLS: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ownerMailbox, err := st.GetOrCreateMailbox(ctx, owner.ID, ownerAccount.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	otherMailbox, err := st.GetOrCreateMailbox(ctx, other.ID, otherAccount.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	db, err := st.UserDB(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	migrationFiles, err := filepath.Glob(filepath.Join("..", "migrations", "user", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(migrationFiles) == 0 {
		t.Fatal("remote IMAP sync user migrations were not found")
	}
	for _, migrationFile := range migrationFiles {
		migration, err := os.ReadFile(migrationFile)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(migrationFile), err)
		}
	}
	return backendFixture{
		store: st, db: db, owner: owner, other: other,
		ownerAccount: ownerAccount, otherAccount: otherAccount,
		ownerMailbox: ownerMailbox, otherMailbox: otherMailbox,
	}
}

func (f backendFixture) inputForOwner(password string) routineInput {
	enabled := true
	return routineInput{
		Name: "Gmail inbox", Enabled: &enabled, AfterDate: "2025-01-02",
		Source: sourceInput{Provider: "gmail", Host: "imap.gmail.com", Port: 993,
			Security: "tls", Username: "owner@gmail.test", Password: password, Mailbox: "INBOX"},
		Destination: destinationInput{AccountID: f.ownerAccount.ID, MailboxID: f.ownerMailbox.ID},
	}
}

func TestPrepareRoutineEncryptsPasswordAndScopesDestination(t *testing.T) {
	fixture := newBackendFixture(t)
	ctx := context.Background()
	backend := &remoteIMAPSyncBackend{}
	host := testAPIHost{}
	input := fixture.inputForOwner("abcd efgh ijkl mnop")

	item, err := backend.prepareRoutine(ctx, host, fixture.store, fixture.db, fixture.owner.ID, 0, input)
	if err != nil {
		t.Fatal(err)
	}
	if item.EncryptedSourcePassword == input.Source.Password || strings.Contains(item.EncryptedSourcePassword, "abcdefghijklmnop") {
		t.Fatal("source password was stored in plaintext")
	}
	plaintext, err := mmcrypto.DecryptString(testMasterKey, item.EncryptedSourcePassword)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "abcdefghijklmnop" {
		t.Fatalf("decrypted Gmail app password = %q", plaintext)
	}
	saved, err := persistRoutine(ctx, fixture.db, item)
	if err != nil {
		t.Fatal(err)
	}
	view, err := presentRoutine(ctx, fixture.store, fixture.db, saved)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), plaintext) || strings.Contains(string(encoded), saved.EncryptedSourcePassword) {
		t.Fatalf("routine API view exposed credential material: %s", encoded)
	}
	if !view.Source.HasPassword {
		t.Fatal("routine API view did not report that a password is configured")
	}

	input.Destination = destinationInput{AccountID: fixture.otherAccount.ID, MailboxID: fixture.otherMailbox.ID}
	if _, err := backend.prepareRoutine(ctx, host, fixture.store, fixture.db, fixture.owner.ID, 0, input); err == nil {
		t.Fatal("cross-user destination was accepted")
	}
}

func TestPrepareRoutineRequiresPasswordWhenConnectionChanges(t *testing.T) {
	fixture := newBackendFixture(t)
	ctx := context.Background()
	backend := &remoteIMAPSyncBackend{}
	host := testAPIHost{}
	item, err := backend.prepareRoutine(ctx, host, fixture.store, fixture.db, fixture.owner.ID, 0, fixture.inputForOwner("app-password"))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := persistRoutine(ctx, fixture.db, item)
	if err != nil {
		t.Fatal(err)
	}

	update := fixture.inputForOwner("")
	update.Source.Mailbox = "Archive"
	mailboxOnly, err := backend.prepareRoutine(ctx, host, fixture.store, fixture.db, fixture.owner.ID, saved.ID, update)
	if err != nil {
		t.Fatalf("mailbox-only update should preserve the stored credential: %v", err)
	}
	if mailboxOnly.EncryptedSourcePassword != saved.EncryptedSourcePassword {
		t.Fatal("mailbox-only update replaced the stored credential")
	}

	update.Source.Host = "capture-password.example.test"
	if _, err := backend.prepareRoutine(ctx, host, fixture.store, fixture.db, fixture.owner.ID, saved.ID, update); err == nil || !strings.Contains(err.Error(), "password is required") {
		t.Fatalf("connection change without a password error = %v", err)
	}
	if _, err := backend.prepareRoutine(ctx, host, fixture.store, fixture.db, fixture.other.ID, saved.ID, update); err == nil {
		t.Fatal("another user updated the owner's routine")
	}
}

func TestSourceDiscoveryCannotSendStoredPasswordToDifferentEndpoint(t *testing.T) {
	fixture := newBackendFixture(t)
	ctx := context.Background()
	backend := &remoteIMAPSyncBackend{}
	host := testAPIHost{}
	item, err := backend.prepareRoutine(ctx, host, fixture.store, fixture.db, fixture.owner.ID, 0, fixture.inputForOwner("saved-password"))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := persistRoutine(ctx, fixture.db, item)
	if err != nil {
		t.Fatal(err)
	}

	account, err := backend.sourceAccountForDiscover(ctx, host, fixture.db, fixture.owner.ID, discoverInput{RoutineID: saved.ID})
	if err != nil {
		t.Fatal(err)
	}
	if account.Host != saved.SourceHost || account.Username != saved.SourceUsername || account.EncryptedPassword != saved.EncryptedSourcePassword {
		t.Fatalf("stored discovery account = %+v", account)
	}
	_, err = backend.sourceAccountForDiscover(ctx, host, fixture.db, fixture.owner.ID, discoverInput{
		RoutineID: saved.ID,
		Source: sourceInput{Host: "capture-password.example.test", Port: saved.SourcePort,
			Username: saved.SourceUsername, Security: "tls"},
	})
	if err == nil || !strings.Contains(err.Error(), "password is required") {
		t.Fatalf("stored credential endpoint substitution error = %v", err)
	}
	if _, err := backend.sourceAccountForDiscover(ctx, host, fixture.db, fixture.other.ID, discoverInput{RoutineID: saved.ID}); err == nil {
		t.Fatal("another user discovered folders with the owner's credential")
	}
}

func TestRoutineLedgerIsTenantScopedAndIdempotent(t *testing.T) {
	fixture := newBackendFixture(t)
	ctx := context.Background()
	backend := &remoteIMAPSyncBackend{}
	host := testAPIHost{}
	item, err := backend.prepareRoutine(ctx, host, fixture.store, fixture.db, fixture.owner.ID, 0, fixture.inputForOwner("password"))
	if err != nil {
		t.Fatal(err)
	}
	item, err = persistRoutine(ctx, fixture.db, item)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := getRoutine(ctx, fixture.db, fixture.other.ID, item.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-user routine read error = %v", err)
	}
	if err := setRoutineEnabled(ctx, fixture.db, fixture.other.ID, item.ID, false); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-user routine update error = %v", err)
	}
	if err := deleteRoutine(ctx, fixture.db, fixture.other.ID, item.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-user routine delete error = %v", err)
	}

	if err := recordHandledMessage(ctx, fixture.db, item, 77, 10, "fingerprint-a", "marker-a", 100, "transferred"); err != nil {
		t.Fatal(err)
	}
	if err := recordHandledMessage(ctx, fixture.db, item, 77, 10, "fingerprint-a", "marker-a", 100, "skipped"); err != nil {
		t.Fatal(err)
	}
	if err := recordHandledMessage(ctx, fixture.db, item, 77, 11, "fingerprint-a", "marker-b", 0, "skipped"); err != nil {
		t.Fatal(err)
	}
	updated, err := getRoutine(ctx, fixture.db, fixture.owner.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.TransferredTotal != 1 || updated.SkippedTotal != 1 || updated.LastSourceUID != 11 {
		t.Fatalf("routine counters/checkpoint = transferred %d skipped %d uid %d", updated.TransferredTotal, updated.SkippedTotal, updated.LastSourceUID)
	}
	var messages int
	if err := fixture.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_remote_imap_sync_messages WHERE user_id = ? AND routine_id = ?`, fixture.owner.ID, item.ID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if messages != 2 {
		t.Fatalf("ledger rows = %d, want 2", messages)
	}
	handled, err := messageAlreadyHandled(ctx, fixture.db, fixture.other.ID, item.ID, 77, 10, "fingerprint-a")
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("another user observed the owner's handled-message ledger")
	}

	crossTenantItem := item
	crossTenantItem.UserID = fixture.other.ID
	if err := recordHandledMessage(ctx, fixture.db, crossTenantItem, 77, 12, "fingerprint-b", "marker-c", 0, "skipped"); err == nil {
		t.Fatal("cross-user ledger insert referencing the owner's routine succeeded")
	}
}

func TestHandledMessageIdentityReconcilesFingerprintOccurrences(t *testing.T) {
	fixture := newBackendFixture(t)
	ctx := context.Background()
	backend := &remoteIMAPSyncBackend{}
	item, err := backend.prepareRoutine(ctx, testAPIHost{}, fixture.store, fixture.db,
		fixture.owner.ID, 0, fixture.inputForOwner("password"))
	if err != nil {
		t.Fatal(err)
	}
	item, err = persistRoutine(ctx, fixture.db, item)
	if err != nil {
		t.Fatal(err)
	}
	if err := recordHandledMessage(ctx, fixture.db, item, 77, 10, "same-fingerprint", "marker-a", 100, "transferred"); err != nil {
		t.Fatal(err)
	}
	if err := recordHandledMessage(ctx, fixture.db, item, 77, 11, "same-fingerprint", "marker-b", 101, "transferred"); err != nil {
		t.Fatal(err)
	}

	handled, err := messageAlreadyHandled(ctx, fixture.db, item.UserID, item.ID, 77, 10, "same-fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("the exact source UID was not recognized as handled")
	}
	handled, err = messageAlreadyHandled(ctx, fixture.db, item.UserID, item.ID, 77, 12, "same-fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("a third byte-identical message in the same UIDVALIDITY epoch was collapsed")
	}
	handled, err = messageAlreadyHandled(ctx, fixture.db, item.UserID, item.ID, 78, 1, "same-fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("the first known occurrence after a UIDVALIDITY reset was not reconciled")
	}
	if err := recordHandledMessage(ctx, fixture.db, item, 78, 1, "same-fingerprint", "marker-c", 0, "skipped"); err != nil {
		t.Fatal(err)
	}
	handled, err = messageAlreadyHandled(ctx, fixture.db, item.UserID, item.ID, 78, 2, "same-fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("the second known occurrence after a UIDVALIDITY reset was not reconciled")
	}
	if err := recordHandledMessage(ctx, fixture.db, item, 78, 2, "same-fingerprint", "marker-d", 0, "skipped"); err != nil {
		t.Fatal(err)
	}
	handled, err = messageAlreadyHandled(ctx, fixture.db, item.UserID, item.ID, 78, 3, "same-fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("a new third occurrence after a UIDVALIDITY reset was collapsed")
	}
}

func TestSameIMAPEndpointIdentityIsCaseInsensitive(t *testing.T) {
	item := routine{
		SourceHost: " IMAP.MXROUTE.TEST ", SourcePort: 993,
		SourceUsername: "OWNER@EXAMPLE.TEST", SourceUseTLS: true, SourceMailbox: " inbox ",
	}
	account := store.MailAccount{
		Host: "imap.mxroute.test", Port: 993, Username: "owner@example.test", UseTLS: true,
	}
	if !sameIMAPEndpoint(item, account) {
		t.Fatal("case-only endpoint differences bypassed same-account protection")
	}
}

func TestSourceMessageAlreadyHandledRejectsRoutineOwnMarker(t *testing.T) {
	fixture := newBackendFixture(t)
	ctx := context.Background()
	backend := &remoteIMAPSyncBackend{}
	item, err := backend.prepareRoutine(ctx, testAPIHost{}, fixture.store, fixture.db,
		fixture.owner.ID, 0, fixture.inputForOwner("password"))
	if err != nil {
		t.Fatal(err)
	}
	item, err = persistRoutine(ctx, fixture.db, item)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := imapclient.MessageSyncMarker(item.MarkerSecret, 12, 34)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("Subject: loop\r\nX-Rolltop-Sync-ID: " + marker + "\r\n\r\nbody")
	message := syncer.FetchedMessage{UID: 99, Raw: raw}
	handled, err := sourceMessageAlreadyHandled(ctx, fixture.db, item, 88, message, messageFingerprint(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("a message previously appended by this routine was eligible for another APPEND")
	}
}

func TestRoutineRoutesUseAuthenticatedUserScope(t *testing.T) {
	fixture := newBackendFixture(t)
	ctx := context.Background()
	backend := &remoteIMAPSyncBackend{}
	prepareHost := testAPIHost{}
	otherInput := fixture.inputForOwner("other-password")
	otherInput.Name = "Other Gmail"
	otherInput.Source.Username = "other@gmail.test"
	otherInput.Destination = destinationInput{AccountID: fixture.otherAccount.ID, MailboxID: fixture.otherMailbox.ID}
	otherRoutine, err := backend.prepareRoutine(ctx, prepareHost, fixture.store, fixture.db, fixture.other.ID, 0, otherInput)
	if err != nil {
		t.Fatal(err)
	}
	otherRoutine, err = persistRoutine(ctx, fixture.db, otherRoutine)
	if err != nil {
		t.Fatal(err)
	}

	host := routeAPIHost{st: fixture.store, userID: fixture.owner.ID}
	request := httptest.NewRequest(http.MethodGet, "/api/plugins/remote_imap_sync/routines", nil)
	recorder := httptest.NewRecorder()
	backend.handleAPI(host, apiPath+"/routines", recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "other@gmail.test") || strings.Contains(body, fixture.other.Email) {
		t.Fatalf("owner list exposed another user's data: %s", body)
	}

	input := fixture.inputForOwner("owner-password")
	encoded, err := json.Marshal(map[string]any{
		"user_id": fixture.other.ID,
		"name":    input.Name, "enabled": true, "source": input.Source,
		"destination": input.Destination, "after_date": input.AfterDate,
	})
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/plugins/remote_imap_sync/routines", bytes.NewReader(encoded))
	recorder = httptest.NewRecorder()
	backend.handleAPI(host, apiPath+"/routines", recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	ownerRoutines, err := listRoutines(ctx, fixture.db, fixture.owner.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ownerRoutines) != 1 || ownerRoutines[0].UserID != fixture.owner.ID {
		t.Fatalf("owner routines after user_id injection = %+v", ownerRoutines)
	}
	otherRoutines, err := listRoutines(ctx, fixture.db, fixture.other.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherRoutines) != 1 || otherRoutines[0].ID != otherRoutine.ID {
		t.Fatalf("other user's routines changed = %+v", otherRoutines)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/plugins/remote_imap_sync/routines/1/runs", nil)
	recorder = httptest.NewRecorder()
	backend.handleAPI(host, apiPath+"/routines/"+strconv.FormatInt(otherRoutine.ID, 10)+"/runs", recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-user run history status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestRoutineMutationStopsWorkerBeforePersistence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	worker := &routineWorker{ctx: ctx, cancel: cancel, triggers: make(chan string, 1)}
	worker.wg.Add(1)
	go func() {
		defer worker.wg.Done()
		<-ctx.Done()
	}()
	key := workerKey{userID: 4, routineID: 9}
	manager := &routineManager{
		wake: make(chan struct{}, 1), workers: map[workerKey]*routineWorker{key: worker},
		pending: map[workerKey]string{key: "manual"},
	}
	if err := manager.MutateRoutine(key.userID, key.routineID, func() error {
		if ctx.Err() == nil {
			t.Fatal("persistence callback ran before the worker stopped")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	_, workerPresent := manager.workers[key]
	_, pendingPresent := manager.pending[key]
	manager.mu.Unlock()
	if workerPresent || pendingPresent {
		t.Fatal("routine worker or pending trigger remained after mutation")
	}
}
