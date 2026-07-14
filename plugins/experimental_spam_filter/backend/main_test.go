package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

func TestRiskBandsAndContentCoverage(t *testing.T) {
	if got := riskBand(0.3499); got != bandLow {
		t.Fatalf("riskBand below low boundary = %q", got)
	}
	if got := riskBand(0.35); got != bandMedium {
		t.Fatalf("riskBand at medium boundary = %q", got)
	}
	if got := riskBand(0.80); got != bandHigh {
		t.Fatalf("riskBand at high boundary = %q", got)
	}

	tests := []struct {
		name  string
		input plugins.MessageClassificationInput
		want  string
	}{
		{name: "encrypted", input: plugins.MessageClassificationInput{BodyText: "secret", IsEncrypted: true}, want: "encrypted_metadata"},
		{name: "metadata", input: plugins.MessageClassificationInput{}, want: "metadata"},
		{name: "preview", input: plugins.MessageClassificationInput{BodyText: "preview", BodyTruncated: true}, want: "preview"},
		{name: "full", input: plugins.MessageClassificationInput{BodyText: "complete"}, want: "full"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := contentCoverage(test.input); got != test.want {
				t.Fatalf("contentCoverage = %q, want %q", got, test.want)
			}
		})
	}
}

func TestEncryptedBackfillSuppressesBodyDerivedEvidence(t *testing.T) {
	message := store.MessageRecord{
		ID: 9, UserID: 3, Subject: "Encrypted note", FromAddr: "sender@example.test",
		BodyText: "unique-private-body-token", BodyHTML: "<p>unique-private-html-token</p>", IsEncrypted: true,
	}
	input := classificationInputFromStored(message, []store.Attachment{{Filename: "private.pdf", ContentType: "application/pdf"}})
	if input.BodyText != "" || input.HasHTML || len(input.Attachments) != 0 {
		t.Fatalf("encrypted classification input retained content: %+v", input)
	}
	if body := modelMessage(input).Body; body != "" {
		t.Fatalf("encrypted model body = %q, want empty", body)
	}
	for _, term := range similarityTerms(input) {
		if term.Field == plugins.SimilarityFieldBody {
			t.Fatalf("encrypted input generated body similarity term %+v", term)
		}
	}
}

func TestNeighborEvidenceBlendingRules(t *testing.T) {
	hits := []plugins.SimilarMessageResult{
		{MessageID: 1, Score: 10, WeightedTermCoverage: .8},
		{MessageID: 2, Score: 8, WeightedTermCoverage: .7},
		{MessageID: 3, Score: 7, WeightedTermCoverage: .6},
	}
	probability, count, evidence := labeledNeighborScore(hits, map[int64]string{1: feedbackSpam, 2: feedbackSpam, 3: feedbackHam})
	if count != 3 || len(evidence) != 3 {
		t.Fatalf("labeled neighbors count=%d evidence=%d, want 3", count, len(evidence))
	}
	if probability <= .5 || probability > 1 {
		t.Fatalf("labeled probability = %f, want spam-leaning bounded score", probability)
	}

	now := time.Now().UTC()
	readSupport, readEvidence := recentReadScore([]plugins.SimilarMessageResult{
		{MessageID: 4, Score: 9, MatchedTermCount: 3, WeightedTermCoverage: .7, Date: now.Add(-24 * time.Hour)},
		{MessageID: 5, Score: 9, MatchedTermCount: 2, WeightedTermCoverage: .9, Date: now.Add(-24 * time.Hour)},
		{MessageID: 6, Score: 9, MatchedTermCount: 4, WeightedTermCoverage: .2, Date: now.Add(-24 * time.Hour)},
		{MessageID: 7, Score: 9, MatchedTermCount: 4, WeightedTermCoverage: .9, Date: now.Add(-91 * 24 * time.Hour)},
	}, now)
	if len(readEvidence) != 1 {
		t.Fatalf("recent read evidence = %d, want only qualifying hit", len(readEvidence))
	}
	if readSupport <= 0 || readSupport > 1 {
		t.Fatalf("recent read support = %f, want (0,1]", readSupport)
	}
	base := .9
	reduced := base * (1 - .15*readSupport)
	if reduced < base*.85 || reduced > base {
		t.Fatalf("recent-read reduction %f from %f exceeds 15%% cap", reduced, base)
	}
}

func TestStorageAndAnnotationsAreTenantScoped(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	first := createSpamTestMessage(t, ctx, st, "first@example.test", "First", 1)
	second := createSpamTestMessage(t, ctx, st, "second@example.test", "Second", 2)
	db := st.DB()
	p := &spamFilterPlugin{}
	_, version, errText := p.model()
	if errText != "" {
		t.Fatalf("load embedded model: %s", errText)
	}
	for _, item := range []struct {
		userID, messageID int64
		probability       float64
	}{
		{first.UserID, first.ID, .91},
		{second.UserID, second.ID, .12},
	} {
		if err := saveClassification(ctx, db, item.userID, classificationRecord{
			MessageID: item.messageID, ModelVersion: version, BaseProbability: item.probability,
			LabeledNeighborProbability: .5, FinalProbability: item.probability,
		}); err != nil {
			t.Fatal(err)
		}
	}

	items, err := listClassifications(ctx, db, first.UserID, []int64{first.ID, second.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[first.ID].MessageID != first.ID {
		t.Fatalf("tenant-scoped classifications = %+v", items)
	}
	if err := setFeedback(ctx, db, first.UserID, second.ID, feedbackSpam); err == nil {
		t.Fatal("cross-tenant feedback insert unexpectedly succeeded")
	} else if !store.IsNotFound(err) {
		t.Fatalf("cross-tenant feedback error = %v, want not found", err)
	}
	if err := saveClassification(ctx, db, first.UserID, classificationRecord{
		MessageID: second.ID, ModelVersion: version, BaseProbability: .5,
		LabeledNeighborProbability: .5, FinalProbability: .5,
	}); !store.IsNotFound(err) {
		t.Fatalf("cross-tenant classification error = %v, want not found", err)
	}
	if err := setFeedback(ctx, db, first.UserID, first.ID, feedbackSpam); err != nil {
		t.Fatal(err)
	}

	host := &spamTestHost{st: st, userID: first.UserID, csrf: true}
	annotations, err := p.MessageAnnotations(ctx, host, first.UserID, []int64{first.ID, second.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(annotations) != 1 || len(annotations[first.ID]) != 1 {
		t.Fatalf("annotations = %+v, want only first user's message", annotations)
	}
	annotation := annotations[first.ID][0]
	if annotation.Level != bandHigh || !strings.Contains(annotation.Summary, "marked") {
		t.Fatalf("feedback annotation = %+v", annotation)
	}
	if _, found := annotation.Metadata["final_probability"]; found {
		t.Fatal("compact annotation exposed raw probability")
	}
}

func TestRecentReadPostFilterRejectsAnyExplicitSpamHit(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	message := createSpamTestMessage(t, ctx, st, "spam-hit@example.test", "Spam hit", 1)
	if err := setFeedback(ctx, st.DB(), message.UserID, message.ID, feedbackSpam); err != nil {
		t.Fatal(err)
	}
	spamIDs, err := spamLabeledMessageIDs(ctx, st.DB(), message.UserID, []int64{message.ID})
	if err != nil {
		t.Fatal(err)
	}
	hits := filterSpamHits([]plugins.SimilarMessageResult{{MessageID: message.ID, Score: 10}}, spamIDs)
	if len(hits) != 0 {
		t.Fatalf("explicitly spam-labeled recent-read hit was retained: %+v", hits)
	}
}

func TestFeedbackOnlyAnnotationsAreTenantScoped(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	first := createSpamTestMessage(t, ctx, st, "feedback-only@example.test", "Feedback only", 1)
	second := createSpamTestMessage(t, ctx, st, "other-feedback@example.test", "Other user", 2)
	if err := setFeedback(ctx, st.DB(), first.UserID, first.ID, feedbackSpam); err != nil {
		t.Fatal(err)
	}
	if err := setFeedback(ctx, st.DB(), second.UserID, second.ID, feedbackHam); err != nil {
		t.Fatal(err)
	}
	p := &spamFilterPlugin{}
	host := &spamTestHost{st: st, userID: first.UserID}
	annotations, err := p.MessageAnnotations(ctx, host, first.UserID, []int64{first.ID, second.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(annotations) != 1 || len(annotations[first.ID]) != 1 {
		t.Fatalf("feedback-only annotations = %+v, want only owned message", annotations)
	}
	annotation := annotations[first.ID][0]
	if annotation.Level != bandHigh || annotation.Metadata["feedback"] != feedbackSpam {
		t.Fatalf("feedback-only annotation = %+v", annotation)
	}
	if _, exists := annotation.Metadata["model_version"]; exists {
		t.Fatalf("feedback-only annotation unexpectedly contains model metadata: %+v", annotation.Metadata)
	}
}

func TestStatusReconcilesInterruptedBackfill(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	message := createSpamTestMessage(t, ctx, st, "orphan@example.test", "Orphan", 1)
	if err := startBackfillRecord(ctx, st.DB(), message.UserID, "model", 100); err != nil {
		t.Fatal(err)
	}
	p := &spamFilterPlugin{}
	host := &spamTestHost{st: st, userID: message.UserID, csrf: true}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	p.apiStatus(host, st.DB(), message.UserID, response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	record, err := getBackfill(ctx, st.DB(), message.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "cancelled" {
		t.Fatalf("orphaned backfill status = %q, want cancelled", record.Status)
	}
}

func TestBackfillNotificationRequiresSavedClassification(t *testing.T) {
	host := &spamTestHost{}
	notifyBackfillChange(host, 42, false)
	if len(host.notified) != 0 {
		t.Fatalf("unchanged backfill notifications = %v, want none", host.notified)
	}
	notifyBackfillChange(host, 42, true)
	if len(host.notified) != 1 || host.notified[0] != 42 {
		t.Fatalf("changed backfill notifications = %v, want [42]", host.notified)
	}
}

func TestClassificationLazilyLoadsModelAndPersistsBoundedEvidence(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	message := createSpamTestMessage(t, ctx, st, "classify@example.test", "Limited offer", 1)
	host := &spamTestHost{
		st: st,
		similarities: [][]plugins.SimilarMessageResult{{
			{MessageID: message.ID + 10, Score: 5, MatchedTermCount: 3, WeightedTermCoverage: .7, Date: time.Now().Add(-time.Hour), From: "known@example.test"},
		}},
	}
	p := &spamFilterPlugin{}
	input := plugins.MessageClassificationInput{
		UserID: message.UserID, MessageID: message.ID, Subject: message.Subject,
		From: message.FromAddr, To: message.ToAddr, BodyText: strings.Repeat("special offer ", 80),
		BodyTruncated: true,
	}
	if err := p.ClassifyMessage(ctx, host, input); err != nil {
		t.Fatal(err)
	}
	record, err := getClassification(ctx, st.DB(), message.UserID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.ModelVersion == "" || record.ContentCoverage != "preview" {
		t.Fatalf("classification = %+v", record)
	}
	if len(host.requests) != 1 || host.requests[0].RecentRead == nil {
		t.Fatalf("similarity requests = %+v, want recent-read request", host.requests)
	}
	if len(host.requests[0].Terms) > plugins.MaxSimilarityTerms {
		t.Fatalf("similarity term count = %d", len(host.requests[0].Terms))
	}
	var explanation string
	if err := st.DB().QueryRowContext(ctx, `SELECT explanation_json FROM plugin_experimental_spam_classifications WHERE user_id = ? AND message_id = ?`, message.UserID, message.ID).Scan(&explanation); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(explanation, input.BodyText) {
		t.Fatal("classification persisted a raw body copy")
	}
}

func TestFeedbackAPIUsesSessionTenantAndRequiresCSRF(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	first := createSpamTestMessage(t, ctx, st, "api-first@example.test", "First", 1)
	second := createSpamTestMessage(t, ctx, st, "api-second@example.test", "Second", 2)
	p := &spamFilterPlugin{}

	host := &spamTestHost{st: st, userID: first.UserID, csrf: false}
	body := bytes.NewBufferString(`{"label":"spam"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/plugins/experimental_spam_filter/messages/1/feedback", body)
	response := httptest.NewRecorder()
	p.handleAPI(host, apiPath+"/messages/"+strconv.FormatInt(first.ID, 10)+"/feedback", response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d, want 403", response.Code)
	}
	if label, err := getFeedback(ctx, st.DB(), first.UserID, first.ID); err != nil || label != "" {
		t.Fatalf("feedback after rejected request label=%q err=%v", label, err)
	}

	host.csrf = true
	body = bytes.NewBufferString(`{"label":"spam"}`)
	request = httptest.NewRequest(http.MethodPost, "/api/plugins/experimental_spam_filter/messages/x/feedback", body)
	response = httptest.NewRecorder()
	p.handleAPI(host, apiPath+"/messages/"+strconv.FormatInt(second.ID, 10)+"/feedback", response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant feedback status = %d, want 404; body=%s", response.Code, response.Body.String())
	}
	if label, err := getFeedback(ctx, st.DB(), second.UserID, second.ID); err != nil || label != "" {
		t.Fatalf("second user feedback label=%q err=%v", label, err)
	}
}

func TestBackfillPrioritizesMissingAndStaleClassifications(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	first := createSpamTestMessage(t, ctx, st, "backfill@example.test", "First", 1)
	second := createSpamTestMessageForUser(t, ctx, st, first.UserID, "Second", 2)
	third := createSpamTestMessageForUser(t, ctx, st, first.UserID, "Third", 3)
	for _, record := range []classificationRecord{
		{MessageID: second.ID, ModelVersion: "old", BaseProbability: .5, LabeledNeighborProbability: .5, FinalProbability: .5},
		{MessageID: third.ID, ModelVersion: "current", BaseProbability: .5, LabeledNeighborProbability: .5, FinalProbability: .5},
	} {
		if err := saveClassification(ctx, st.DB(), first.UserID, record); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := backfillMessageIDs(ctx, st.DB(), first.UserID, "current", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != second.ID || ids[1] != first.ID {
		t.Fatalf("backfill priority = %v, want stale %d then missing %d", ids, second.ID, first.ID)
	}
}

func newSpamTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	raw, err := os.ReadFile(filepath.Join("..", "migrations", "user", "001_create_experimental_spam_filter.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	return st
}

func createSpamTestMessage(t *testing.T, ctx context.Context, st *store.Store, email, subject string, uid uint32) store.MessageRecord {
	t.Helper()
	user, err := st.CreateUser(ctx, email, email, "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	return createSpamTestMessageForUser(t, ctx, st, user.ID, subject, uid)
}

func createSpamTestMessageForUser(t *testing.T, ctx context.Context, st *store.Store, userID int64, subject string, uid uint32) store.MessageRecord {
	t.Helper()
	email := "user-" + strconv.FormatInt(userID, 10) + "@example.test"
	account, err := st.CreateMailAccount(ctx, store.MailAccount{
		UserID: userID, Email: email, Host: "imap.example.test", Port: 993,
		Username: email, EncryptedPassword: "encrypted-test-value", UseTLS: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := st.GetOrCreateMailbox(ctx, userID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := st.CreateBlob(ctx, store.BlobRecord{UserID: userID, Kind: "message", Path: filepath.Join("messages", subject+".eml"), SHA256: subject, Size: 64})
	if err != nil {
		t.Fatal(err)
	}
	message, err := st.CreateMessage(ctx, store.CreateMessage{
		UserID: userID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		MessageIDHeader: "<" + strconv.FormatUint(uint64(uid), 10) + "@example.test>",
		Subject:         subject, FromAddr: "sender@example.test", ToAddr: email,
		Date: time.Now().UTC(), InternalDate: time.Now().UTC(), UID: uid, Size: 64,
		BlobPath: blob.Path, BodyText: "stored preview for " + subject,
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

type spamTestHost struct {
	st           *store.Store
	userID       int64
	csrf         bool
	similarities [][]plugins.SimilarMessageResult
	requests     []plugins.SimilarMessagesRequest
	notified     []int64
}

func (h *spamTestHost) Store() any                               { return h.st }
func (*spamTestHost) MasterKey() []byte                          { return []byte("test") }
func (*spamTestHost) PluginEnabled(context.Context, string) bool { return true }
func (h *spamTestHost) SimilarMessages(_ context.Context, _ int64, request plugins.SimilarMessagesRequest) ([]plugins.SimilarMessageResult, error) {
	h.requests = append(h.requests, request)
	if len(h.similarities) == 0 {
		return nil, nil
	}
	result := h.similarities[0]
	h.similarities = h.similarities[1:]
	return result, nil
}
func (h *spamTestHost) RequireAPIAuth(http.ResponseWriter, *http.Request) (plugins.CurrentUser, bool) {
	return plugins.CurrentUser{UserID: h.userID}, true
}
func (*spamTestHost) LoginUserID(http.ResponseWriter, *http.Request, int64) error { return nil }
func (h *spamTestHost) VerifyCSRF(w http.ResponseWriter, _ *http.Request) bool {
	if h.csrf {
		return true
	}
	h.WriteAPIError(w, http.StatusForbidden, "invalid CSRF token")
	return false
}
func (h *spamTestHost) DecodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	if err := json.NewDecoder(r.Body).Decode(destination); err != nil {
		h.WriteAPIError(w, http.StatusBadRequest, "invalid JSON")
		return false
	}
	return true
}
func (*spamTestHost) WriteJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func (*spamTestHost) WriteAPIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
func (h *spamTestHost) ServerError(w http.ResponseWriter, err error) {
	h.WriteAPIError(w, http.StatusInternalServerError, err.Error())
}
func (h *spamTestHost) NotifyUserChanged(userID int64) { h.notified = append(h.notified, userID) }

var _ plugins.MessageClassificationHost = (*spamTestHost)(nil)
var _ plugins.APIHost = (*spamTestHost)(nil)
var _ plugins.UserChangeHost = (*spamTestHost)(nil)
