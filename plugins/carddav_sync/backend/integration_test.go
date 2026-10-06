package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/plugins"
	"rolltop/backend/store"
	_ "rolltop/plugins/catalog"
)

const testCard = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:card-a\r\nFN:Remote Person\r\nEMAIL:person@example.test\r\nEND:VCARD\r\n"
const testKey = "0123456789abcdef0123456789abcdef"

type cardTestHost struct {
	plugins.BackendStartHost
	st *store.Store
}

func (h cardTestHost) Store() any                                 { return h.st }
func (h cardTestHost) MasterKey() []byte                          { return []byte(testKey) }
func (h cardTestHost) PluginEnabled(context.Context, string) bool { return true }

type cardAPIHost struct {
	plugins.APIHost
	st *store.Store
}

func (h cardAPIHost) Store() any        { return h.st }
func (h cardAPIHost) MasterKey() []byte { return []byte(testKey) }

func cardTestStore(t *testing.T) (*store.Store, *sql.DB, store.User, store.User) {
	t.Helper()
	st, err := store.OpenServer(filepath.Join(t.TempDir(), "system.db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	owner, err := st.CreateUser(context.Background(), "owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(context.Background(), "other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := st.UserDB(context.Background(), owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	return st, db, owner, other
}
func saveTestRoutine(t *testing.T, db *sql.DB, userID int64, base string) routine {
	t.Helper()
	encrypted, err := mmcrypto.EncryptString([]byte(testKey), "test-password")
	if err != nil {
		t.Fatal(err)
	}
	item, err := persistRoutine(context.Background(), db, userID, routine{Name: "Book", Enabled: true, ServerURL: base, Username: "owner", EncryptedPassword: encrypted, AddressbookURL: base + "/book/", PollIntervalMin: 15})
	if err != nil {
		t.Fatal(err)
	}
	return item
}
func multi(body, token string) string {
	return `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav">` + body + `<d:sync-token>` + xmlEscape(token) + `</d:sync-token></d:multistatus>`
}
func member(href, etag, card string) string {
	data := ""
	if card != "" {
		data = `<c:address-data>` + xmlEscape(card) + `</c:address-data>`
	}
	return `<d:response><d:href>` + xmlEscape(href) + `</d:href><d:propstat><d:prop><d:getetag>` + xmlEscape(etag) + `</d:getetag>` + data + `</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`
}
func davXML(w http.ResponseWriter, body string) { w.WriteHeader(207); fmt.Fprint(w, body) }

func TestDiscoveryAndCredentialBoundaries(t *testing.T) {
	foreignCalls := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignCalls++; w.WriteHeader(200) }))
	defer foreign.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Errorf("redirect changed method to %s", r.Method)
		}
		u, p, ok := r.BasicAuth()
		if !ok || u != "owner" || p != "test-password" {
			t.Error("missing DAV credentials")
		}
		switch r.URL.Path {
		case "/dav":
			http.Redirect(w, r, "/principal-discovery", 302)
		case "/principal-discovery":
			davXML(w, multi(`<d:response><d:href>/</d:href><d:propstat><d:prop><d:current-user-principal><d:href>/principal/</d:href></d:current-user-principal></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, ""))
		case "/principal/":
			davXML(w, multi(`<d:response><d:href>/principal/</d:href><d:propstat><d:prop><c:addressbook-home-set><d:href>/books/</d:href></c:addressbook-home-set></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, ""))
		case "/books/":
			davXML(w, multi(`<d:response><d:href>personal/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/><c:addressbook/></d:resourcetype><d:displayname>Personal</d:displayname></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, ""))
		case "/redirect":
			http.Redirect(w, r, foreign.URL, 307)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := newCardDAVClient(server.URL+"/dav", carddavCredentials{"owner", "test-password"})
	if err != nil {
		t.Fatal(err)
	}
	books, err := client.DiscoverAddressBooks(context.Background())
	if err != nil || len(books) != 1 || books[0].URL != server.URL+"/books/personal/" {
		t.Fatalf("discovery: %+v %v", books, err)
	}
	if _, err := client.doFollowRedirect(context.Background(), "PROPFIND", server.URL+"/redirect", "", "", nil); err == nil {
		t.Fatal("foreign redirect accepted")
	}
	if _, err := client.ListHrefs(context.Background(), foreign.URL); err == nil {
		t.Fatal("foreign book accepted")
	}
	if foreignCalls != 0 {
		t.Fatal("credentials reached foreign server")
	}
	for _, raw := range []string{"http://127.evil.example.test", "https://user:secret@example.test", "ftp://localhost/book"} {
		if _, err := newCardDAVClient(raw, carddavCredentials{"owner", "password"}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	private := &url.Error{Op: "REPORT", URL: "https://host/private-token?password=secret", Err: errors.New("upstream failure")}
	if strings.Contains(sanitizeCardDAVError(private).Error(), "secret") || strings.Contains(sanitizeCardDAVError(private).Error(), "private-token") {
		t.Fatal("transport error leaked URL")
	}
}

func TestSyncRetriesWithoutLosingContactsOrToken(t *testing.T) {
	ctx := context.Background()
	mode := "initial"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "sync-collection") {
			if mode == "initial" && !strings.Contains(string(body), "<d:sync-token></d:sync-token>") {
				t.Error("initial sync omitted empty token")
			}
			if mode == "deleted" {
				davXML(w, multi(`<d:response><d:href>/book/a.vcf</d:href><d:status>HTTP/1.1 404 Not Found</d:status></d:response>`, "token-3"))
				return
			}
			token := "token-1"
			if mode != "initial" {
				token = "token-2"
			}
			davXML(w, multi(member("/book/a.vcf", token, ""), token))
			return
		}
		switch mode {
		case "failed":
			davXML(w, multi(`<d:response><d:href>/book/a.vcf</d:href><d:propstat><d:prop/><d:status>HTTP/1.1 500 Internal Server Error</d:status></d:propstat></d:response>`, ""))
		case "missing":
			davXML(w, multi("", ""))
		default:
			davXML(w, multi(member("/book/a.vcf", "token-1", testCard), ""))
		}
	}))
	defer server.Close()
	st, db, owner, other := cardTestStore(t)
	item := saveTestRoutine(t, db, owner.ID, server.URL)
	worker := newRoutineWorker(ctx, cardTestHost{st: st}, st, item)
	defer worker.Stop()
	if err := worker.runOnce("manual"); err != nil {
		t.Fatal(err)
	}
	contacts, err := st.ListContactsForUser(ctx, owner.ID, "", 20)
	if err != nil || len(contacts) != 1 {
		t.Fatalf("contacts: %+v %v", contacts, err)
	}
	contact := contacts[0]
	contact.Notes = "Local notes"
	if _, err := st.UpdateContact(ctx, owner.ID, contact.ID, contact); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"failed", "missing"} {
		mode = failure
		if err := worker.runOnce("manual"); err == nil {
			t.Fatal("partial fetch succeeded")
		}
		current, _ := getRoutine(ctx, db, owner.ID, item.ID)
		if current.SyncToken != "token-1" {
			t.Fatalf("token advanced: %q", current.SyncToken)
		}
		if _, err := st.GetContactForUser(ctx, owner.ID, contact.ID); err != nil {
			t.Fatal("contact lost on partial fetch")
		}
	}
	mode = "deleted"
	if err := worker.runOnce("manual"); err != nil {
		t.Fatal(err)
	}
	kept, err := st.GetContactForUser(ctx, owner.ID, contact.ID)
	if err != nil || kept.Notes != "Local notes" {
		t.Fatalf("edited contact lost: %+v %v", kept, err)
	}
	mappings, err := listMappings(ctx, db, owner.ID, item.ID)
	if err != nil || len(mappings) != 0 {
		t.Fatalf("deleted card still linked: %v %v", mappings, err)
	}
	if _, err := getRoutine(ctx, db, other.ID, item.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("routine crossed tenants")
	}
	otherContacts, err := st.ListContactsForUser(ctx, other.ID, "", 20)
	if err != nil || len(otherContacts) != 0 {
		t.Fatal("contacts crossed tenants")
	}
}

func TestFullFetchFailureAndETagSkip(t *testing.T) {
	ctx := context.Background()
	mode := "changed"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			etag := "new"
			if mode == "unchanged" {
				etag = "old"
			}
			body := member("/book/a.vcf", etag, "")
			if mode == "gone" {
				body = ""
			}
			davXML(w, multi(body, ""))
			return
		}
		requests++
		davXML(w, multi(`<d:response><d:href>/book/a.vcf</d:href><d:propstat><d:prop/><d:status>HTTP/1.1 500 Error</d:status></d:propstat></d:response>`, ""))
	}))
	defer server.Close()
	st, db, user, _ := cardTestStore(t)
	item := saveTestRoutine(t, db, user.ID, server.URL)
	contact, err := st.CreateContact(ctx, user.ID, store.Contact{DisplayName: "Local"})
	if err != nil {
		t.Fatal(err)
	}
	if err := upsertMapping(ctx, db, user.ID, item.ID, mappingRow{VCardUID: "a", Href: server.URL + "/book/a.vcf", ETag: "old", ContactID: contact.ID, OwnsContact: true}); err != nil {
		t.Fatal(err)
	}
	client, _ := newCardDAVClient(server.URL, carddavCredentials{"owner", "password"})
	worker := &routineWorker{}
	if _, _, err := worker.fullFetch(ctx, client, db, item); err == nil {
		t.Fatal("failed multiget became successful deletion")
	}
	mode = "unchanged"
	requests = 0
	if changes, _, err := worker.fullFetch(ctx, client, db, item); err != nil || len(changes) != 0 || requests != 0 {
		t.Fatalf("unchanged cards downloaded: %+v %v %d", changes, err, requests)
	}
	mode = "gone"
	if changes, _, err := worker.fullFetch(ctx, client, db, item); err != nil || len(changes) != 1 || !changes[0].Deleted {
		t.Fatalf("missing mapping not reconciled: %+v %v", changes, err)
	}
}

func TestExpiredTokenAndAuthenticationAreDistinct(t *testing.T) {
	mode := "expired"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		if mode == "expired" {
			fmt.Fprint(w, `<d:error xmlns:d="DAV:"><d:valid-sync-token/></d:error>`)
		}
	}))
	defer server.Close()
	client, _ := newCardDAVClient(server.URL, carddavCredentials{"owner", "password"})
	_, _, err := client.SyncAddressBook(context.Background(), server.URL+"/book/", "old")
	if !isInvalidSyncToken(err) || isCardDAVAuthenticationError(err) {
		t.Fatalf("expired token = %v", err)
	}
	mode = "auth"
	_, _, err = client.SyncAddressBook(context.Background(), server.URL+"/book/", "old")
	if isInvalidSyncToken(err) || !isCardDAVAuthenticationError(err) {
		t.Fatalf("authentication = %v", err)
	}
	st, db, user, _ := cardTestStore(t)
	item := saveTestRoutine(t, db, user.ID, server.URL)
	worker := newRoutineWorker(context.Background(), cardTestHost{st: st}, st, item)
	defer worker.Stop()
	if err := worker.runOnce("manual"); !isCardDAVAuthenticationError(err) {
		t.Fatalf("worker auth = %v", err)
	}
	current, _ := getRoutine(context.Background(), db, user.ID, item.ID)
	if current.Enabled || current.State != "needs_credentials" {
		t.Fatalf("routine not suspended: %+v", current)
	}
}

func TestRoutineChangeIsAtomicAndScoped(t *testing.T) {
	ctx := context.Background()
	st, db, user, other := cardTestStore(t)
	item := saveTestRoutine(t, db, user.ID, "https://dav.example.test")
	contact, err := st.CreateContact(ctx, user.ID, store.Contact{DisplayName: "Kept"})
	if err != nil {
		t.Fatal(err)
	}
	if err := upsertMapping(ctx, db, user.ID, item.ID, mappingRow{VCardUID: "a", ContactID: contact.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugin_carddav_sync_routines SET sync_token='original' WHERE id=?`, item.ID); err != nil {
		t.Fatal(err)
	}
	item, _ = getRoutine(ctx, db, user.ID, item.ID)
	backend := &carddavSyncBackend{}
	input := routineInput{Name: "Edited", Enabled: true, ServerURL: item.ServerURL, Username: item.Username, AddressbookURL: item.AddressbookURL, PollIntervalMin: 15}
	input.ServerURL = "https://other.example.test"
	if _, err := backend.prepareRoutine(ctx, cardAPIHost{st: st}, db, user.ID, item.ID, input); err == nil {
		t.Fatal("saved password reused on another host")
	}
	input.ServerURL = item.ServerURL
	input.AddressbookURL = "https://foreign.example.test/book/"
	if _, err := backend.prepareRoutine(ctx, cardAPIHost{st: st}, db, user.ID, item.ID, input); err == nil {
		t.Fatal("foreign book accepted")
	}
	input.AddressbookURL = item.ServerURL + "/new-book/"
	input.Password = "replacement"
	prepared, err := backend.prepareRoutine(ctx, cardAPIHost{st: st}, db, user.ID, item.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if current, _ := getRoutine(ctx, db, user.ID, item.ID); current.SyncToken != "original" {
		t.Fatal("validation mutated token")
	}
	if _, err := getMapping(ctx, db, user.ID, item.ID, "a"); err != nil {
		t.Fatal("validation deleted mapping")
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_settings BEFORE UPDATE ON plugin_carddav_sync_routines BEGIN SELECT RAISE(FAIL,'storage failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := persistRoutine(ctx, db, user.ID, prepared); err == nil {
		t.Fatal("expected storage failure")
	}
	if _, err := getMapping(ctx, db, user.ID, item.ID, "a"); err != nil {
		t.Fatal("failed save lost mappings")
	}
	if _, err := db.Exec(`DROP TRIGGER reject_settings`); err != nil {
		t.Fatal(err)
	}
	saved, err := persistRoutine(ctx, db, user.ID, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if saved.SyncToken != "" {
		t.Fatal("new source kept old token")
	}
	if _, err := getMapping(ctx, db, user.ID, item.ID, "a"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("new source kept old mapping")
	}
	if _, err := st.GetContactForUser(ctx, user.ID, contact.ID); err != nil {
		t.Fatal("source change removed local contact")
	}
	if _, err := backend.prepareRoutine(ctx, cardAPIHost{st: st}, db, other.ID, item.ID, input); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign routine editable")
	}
	view, err := presentRoutine(ctx, db, saved)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "replacement") || strings.Contains(string(encoded), saved.EncryptedPassword) {
		t.Fatal("API exposed password")
	}
}

func TestPaginatedSyncFetchesBodiesBeforeCommittingToken(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "sync-collection") {
			pages++
			if pages == 1 {
				davXML(w, multi(member("/book/a.vcf", "a", "")+`<d:response><d:href>/book/</d:href><d:status>HTTP/1.1 507 Insufficient Storage</d:status></d:response>`, "page-1"))
			} else {
				if !strings.Contains(string(body), "<d:sync-token>page-1</d:sync-token>") {
					t.Error("next page did not use intermediate token")
				}
				davXML(w, multi(member("/book/b.vcf", "b", ""), "page-2"))
			}
		} else {
			href := "/book/a.vcf"
			if strings.Contains(string(body), "b.vcf") {
				href = "/book/b.vcf"
			}
			davXML(w, multi(member(href, "etag", testCard), ""))
		}
	}))
	defer server.Close()
	client, _ := newCardDAVClient(server.URL, carddavCredentials{"owner", "password"})
	changes, token, err := client.SyncAddressBook(context.Background(), server.URL+"/book/", "")
	if err != nil || token != "page-2" || len(changes) != 2 || pages != 2 {
		t.Fatalf("paginated sync: %d %q %d %v", len(changes), token, pages, err)
	}
	for _, change := range changes {
		if len(change.VCard) == 0 {
			t.Fatal("sync returned ETag without card body")
		}
	}
}

func TestGroupedVCardPropertiesAndQuotedParameters(t *testing.T) {
	raw := "BEGIN:VCARD\r\nUID:grouped\r\nFN:Sample\r\nitem1.EMAIL;TYPE=HOME;X-LABEL=\"home: primary; address\":person@example.test\r\nNOTE:Keep two\r\n  spaces\r\nEND:VCARD\r\n"
	cards := parseVCards([]byte(raw))
	if len(cards) != 1 || len(cards[0].Contact.Emails) != 1 || cards[0].Contact.Emails[0].Email != "person@example.test" || cards[0].Contact.Notes != "Keep two spaces" {
		t.Fatalf("grouped vCard: %+v", cards)
	}
}

func TestRoutineMutationWaitsForActiveSyncToStop(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	st, db, user, _ := cardTestStore(t)
	item := saveTestRoutine(t, db, user.ID, server.URL)
	host := cardTestHost{st: st}
	manager := newRoutineManager(host, st)
	defer manager.Stop()
	worker := newRoutineWorker(manager.ctx, host, st, item)
	manager.workers[workerKey{userID: user.ID, routineID: item.ID}] = worker
	worker.Start()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("sync did not start")
	}
	if err := manager.MutateRoutine(user.ID, item.ID, func() error {
		run, err := latestRun(context.Background(), db, user.ID, item.ID)
		if err != nil {
			return err
		}
		if run == nil || run.Status != "canceled" {
			return fmt.Errorf("mutation ran before sync stopped: %+v", run)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
