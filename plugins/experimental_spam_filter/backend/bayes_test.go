package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"rolltop/backend/plugins"
	rollstore "rolltop/backend/store"
	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

func TestPersonalBayesTokenizationIsBoundedDocumentPresence(t *testing.T) {
	base := spammodel.Message{
		Subject:  "Limited offer",
		Body:     "remove",
		From:     `Sender <sender@Example.test>`,
		To:       []string{"recipient@example.test"},
		MIMEType: "text/plain; charset=utf-8",
	}
	repeated := base
	repeated.Body = strings.Repeat("remove ", 20_000)

	want := TokenizePersonalBayesMessage(base)
	got := TokenizePersonalBayesMessage(repeated)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repeated body changed document-presence tokens: got=%d want=%d", len(got), len(want))
	}
	seen := make(map[[32]byte]bool, len(got))
	removeCount := 0
	for _, token := range got {
		if seen[token.Hash] {
			t.Fatalf("duplicate token hash %x", token.Hash)
		}
		seen[token.Hash] = true
		if token.Feature == "body:word:remove" {
			removeCount++
		}
		if token.Hash != hashPersonalBayesFeature(token.Feature) {
			t.Fatalf("token hash does not match schema-bound feature %q", token.Feature)
		}
	}
	if removeCount != 1 {
		t.Fatalf("body remove token count = %d, want one document-presence token", removeCount)
	}

	var large strings.Builder
	for index := 0; index < 20_000; index++ {
		fmt.Fprintf(&large, "word%05d ", index)
	}
	bounded := TokenizePersonalBayesMessage(spammodel.Message{Body: large.String()})
	if len(bounded) == 0 || len(bounded) > personalBayesMaxUniqueTokens {
		t.Fatalf("bounded token count = %d, want 1..%d", len(bounded), personalBayesMaxUniqueTokens)
	}
}

func TestPersonalBayesLearnRelabelUnlearnLifecycle(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	stored := createSpamTestMessage(t, ctx, st, "bayes@example.test", "Bayes lifecycle", 1)
	message := spammodel.Message{
		Subject: "Exclusive offer",
		Body:    strings.Repeat("winner remove ", 50),
		From:    "promo@example.test",
	}

	// The Tx form participates in a larger caller-owned transaction.
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LearnPersonalBayesTx(ctx, tx, stored.UserID, stored.ID, "spam", message); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var stateRows int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_experimental_spam_bayes_state WHERE user_id = ?`, stored.UserID).Scan(&stateRows); err != nil {
		t.Fatal(err)
	}
	if stateRows != 0 {
		t.Fatalf("caller rollback retained %d Bayes state rows", stateRows)
	}

	learned, err := LearnPersonalBayes(ctx, st.DB(), stored.UserID, stored.ID, "spam", message)
	if err != nil {
		t.Fatal(err)
	}
	if !learned.Changed || learned.SpamMessages != 1 || learned.HamMessages != 0 || learned.Label != "spam" {
		t.Fatalf("first learn = %+v", learned)
	}
	duplicate, err := LearnPersonalBayes(ctx, st.DB(), stored.UserID, stored.ID, "spam", message)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Changed || duplicate.SpamMessages != 1 || duplicate.HamMessages != 0 {
		t.Fatalf("duplicate learn was not idempotent: %+v", duplicate)
	}

	relabeled, err := LearnPersonalBayes(ctx, st.DB(), stored.UserID, stored.ID, "ham", message)
	if err != nil {
		t.Fatal(err)
	}
	if !relabeled.Changed || relabeled.PreviousLabel != "spam" || relabeled.SpamMessages != 0 || relabeled.HamMessages != 1 {
		t.Fatalf("opposite relabel = %+v", relabeled)
	}
	rows, err := st.DB().QueryContext(ctx, `SELECT spam_messages, ham_messages
		FROM plugin_experimental_spam_bayes_tokens WHERE user_id = ?`, stored.UserID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	counted := 0
	for rows.Next() {
		var spamCount, hamCount int64
		if err := rows.Scan(&spamCount, &hamCount); err != nil {
			t.Fatal(err)
		}
		if spamCount != 0 || hamCount != 1 {
			t.Fatalf("relabeled token counts = spam:%d ham:%d", spamCount, hamCount)
		}
		counted++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if counted == 0 {
		t.Fatal("relabel left no token rows")
	}

	if _, err := UnlearnPersonalBayes(ctx, st.DB(), stored.UserID, stored.ID, "spam", message); !errors.Is(err, ErrPersonalBayesLabelMismatch) {
		t.Fatalf("wrong-label unlearn error = %v, want label mismatch", err)
	}
	unlearned, err := UnlearnPersonalBayes(ctx, st.DB(), stored.UserID, stored.ID, "ham", message)
	if err != nil {
		t.Fatal(err)
	}
	if !unlearned.Changed || unlearned.PreviousLabel != "ham" || unlearned.SpamMessages != 0 || unlearned.HamMessages != 0 || unlearned.StoredTokens != 0 {
		t.Fatalf("unlearn = %+v", unlearned)
	}
	var tokenRows, learnRows, spamTotal, hamTotal int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_experimental_spam_bayes_tokens WHERE user_id = ?`, stored.UserID).Scan(&tokenRows); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT COALESCE(SUM(spam_messages), 0), COALESCE(SUM(ham_messages), 0)
		FROM plugin_experimental_spam_bayes_tokens WHERE user_id = ?`, stored.UserID).Scan(&spamTotal, &hamTotal); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_experimental_spam_bayes_learns WHERE user_id = ?`, stored.UserID).Scan(&learnRows); err != nil {
		t.Fatal(err)
	}
	if tokenRows != 0 || learnRows != 0 || spamTotal != 0 || hamTotal != 0 {
		t.Fatalf("unlearn vocabulary=%d learns=%d counts=%d/%d", tokenRows, learnRows, spamTotal, hamTotal)
	}
}

func TestPersonalBayesRelabelAndUnlearnUsePersistedTokens(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	stored := createSpamTestMessage(t, ctx, st, "changed@example.test", "Changed", 1)
	original := spammodel.Message{Subject: "Original", Body: "origonly message content"}
	if _, err := LearnPersonalBayes(ctx, st.DB(), stored.UserID, stored.ID, "spam", original); err != nil {
		t.Fatal(err)
	}
	changed := original
	changed.Body = "newonly different content"
	if _, err := LearnPersonalBayes(ctx, st.DB(), stored.UserID, stored.ID, "ham", changed); err != nil {
		t.Fatalf("relabel after stored body changed: %v", err)
	}
	originalToken := hashPersonalBayesFeature("body:word:origonly")
	changedToken := hashPersonalBayesFeature("body:word:newonly")
	var originalSpam, originalHam int64
	if err := st.DB().QueryRowContext(ctx, `SELECT spam_messages, ham_messages
		FROM plugin_experimental_spam_bayes_tokens
		WHERE user_id = ? AND token_hash = ?`, stored.UserID, originalToken[:]).Scan(&originalSpam, &originalHam); err != nil {
		t.Fatal(err)
	}
	if originalSpam != 0 || originalHam != 1 {
		t.Fatalf("persisted original token counts = %d/%d, want 0/1", originalSpam, originalHam)
	}
	var changedRows int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*)
		FROM plugin_experimental_spam_bayes_tokens
		WHERE user_id = ? AND token_hash = ?`, stored.UserID, changedToken[:]).Scan(&changedRows); err != nil {
		t.Fatal(err)
	}
	if changedRows != 0 {
		t.Fatalf("relabel retokenized mutable message body into %d rows", changedRows)
	}
	if _, err := UnlearnPersonalBayes(ctx, st.DB(), stored.UserID, stored.ID, "ham", spammodel.Message{Body: "mutated again"}); err != nil {
		t.Fatalf("unlearn after stored body changed: %v", err)
	}
	var spamMessages, hamMessages, tokenTotal, documentRows, learnedTokenRows int64
	if err := st.DB().QueryRowContext(ctx, `SELECT spam_messages, ham_messages
		FROM plugin_experimental_spam_bayes_state WHERE user_id = ?`, stored.UserID).Scan(&spamMessages, &hamMessages); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT COALESCE(SUM(spam_messages + ham_messages), 0)
		FROM plugin_experimental_spam_bayes_tokens WHERE user_id = ?`, stored.UserID).Scan(&tokenTotal); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*)
		FROM plugin_experimental_spam_bayes_documents WHERE user_id = ?`, stored.UserID).Scan(&documentRows); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*)
		FROM plugin_experimental_spam_bayes_learn_tokens WHERE user_id = ?`, stored.UserID).Scan(&learnedTokenRows); err != nil {
		t.Fatal(err)
	}
	if spamMessages != 0 || hamMessages != 0 || tokenTotal != 0 || documentRows != 0 || learnedTokenRows != 0 {
		t.Fatalf("persisted-token unlearn left spam=%d ham=%d token-total=%d documents=%d learned-tokens=%d", spamMessages, hamMessages, tokenTotal, documentRows, learnedTokenRows)
	}
}

func TestPersonalBayesTenantIsolation(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	first := createSpamTestMessage(t, ctx, st, "first-bayes@example.test", "First Bayes", 1)
	second := createSpamTestMessage(t, ctx, st, "second-bayes@example.test", "Second Bayes", 2)
	message := spammodel.Message{Subject: "shared", Body: "shared tenant token", From: "sender@example.test"}
	if _, err := LearnPersonalBayes(ctx, st.DB(), first.UserID, first.ID, "spam", message); err != nil {
		t.Fatal(err)
	}
	if _, err := LearnPersonalBayes(ctx, st.DB(), second.UserID, second.ID, "ham", message); err != nil {
		t.Fatal(err)
	}

	token := hashPersonalBayesFeature("body:word:shared")
	for _, test := range []struct {
		userID            int64
		wantSpam, wantHam int64
	}{
		{userID: first.UserID, wantSpam: 1, wantHam: 0},
		{userID: second.UserID, wantSpam: 0, wantHam: 1},
	} {
		var spamCount, hamCount int64
		if err := st.DB().QueryRowContext(ctx, `SELECT spam_messages, ham_messages
			FROM plugin_experimental_spam_bayes_tokens
			WHERE user_id = ? AND token_hash = ?`, test.userID, token[:]).Scan(&spamCount, &hamCount); err != nil {
			t.Fatal(err)
		}
		if spamCount != test.wantSpam || hamCount != test.wantHam {
			t.Fatalf("user %d token counts spam=%d ham=%d, want %d/%d", test.userID, spamCount, hamCount, test.wantSpam, test.wantHam)
		}
	}
	if _, err := LearnPersonalBayes(ctx, st.DB(), first.UserID, second.ID, "spam", message); !errors.Is(err, ErrPersonalBayesMessageNotOwned) {
		t.Fatalf("cross-tenant learn error = %v, want not owned", err)
	}

	var firstLearns, secondLearns int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_experimental_spam_bayes_learns WHERE user_id = ?`, first.UserID).Scan(&firstLearns); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_experimental_spam_bayes_learns WHERE user_id = ?`, second.UserID).Scan(&secondLearns); err != nil {
		t.Fatal(err)
	}
	if firstLearns != 1 || secondLearns != 1 {
		t.Fatalf("tenant learn rows changed: first=%d second=%d", firstLearns, secondLearns)
	}
}

func TestPersonalBayesScoreReadinessCapAndPrivateEvidence(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	stored := createSpamTestMessage(t, ctx, st, "score@example.test", "Score", 1)

	var body strings.Builder
	for index := 0; index < 220; index++ {
		fmt.Fprintf(&body, "secretword%03d ", index)
	}
	message := spammodel.Message{Body: body.String()}
	tokens := TokenizePersonalBayesMessage(message)
	if len(tokens) < personalBayesMaxSignificant {
		t.Fatalf("test message has %d tokens, want at least %d", len(tokens), personalBayesMaxSignificant)
	}
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO plugin_experimental_spam_bayes_state
		(user_id, token_schema, spam_messages, ham_messages, token_count, updated_at)
		VALUES (?, ?, 199, 200, 0, 1)`, stored.UserID, PersonalBayesTokenSchema); err != nil {
		t.Fatal(err)
	}
	notReady, err := ScorePersonalBayes(ctx, st.DB(), stored.UserID, message)
	if err != nil {
		t.Fatal(err)
	}
	if notReady.Ready || notReady.Probability != .5 || notReady.TokensUsed != 0 {
		t.Fatalf("pre-activation score = %+v", notReady)
	}

	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO plugin_experimental_spam_bayes_tokens
		(user_id, token_hash, spam_messages, ham_messages, last_seen) VALUES (?, ?, 200, 0, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range tokens {
		if _, err := statement.ExecContext(ctx, stored.UserID, token.Hash[:]); err != nil {
			statement.Close()
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE plugin_experimental_spam_bayes_state
		SET spam_messages = 200, token_count = ? WHERE user_id = ?`, len(tokens), stored.UserID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	score, err := ScorePersonalBayes(ctx, st.DB(), stored.UserID, message)
	if err != nil {
		t.Fatal(err)
	}
	if !score.Ready || score.Probability <= .99 {
		t.Fatalf("activated score = %+v, want strong spam probability", score)
	}
	if score.TokensUsed != personalBayesMaxSignificant {
		t.Fatalf("tokens used = %d, want strongest-token cap %d", score.TokensUsed, personalBayesMaxSignificant)
	}
	if len(score.Evidence) != personalBayesMaxEvidence {
		t.Fatalf("evidence count = %d, want %d", len(score.Evidence), personalBayesMaxEvidence)
	}
	for _, evidence := range score.Evidence {
		if evidence.Category != "body-word" || len(evidence.TokenID) != 12 {
			t.Fatalf("private evidence = %+v", evidence)
		}
		if strings.Contains(evidence.TokenID, "secretword") {
			t.Fatalf("evidence leaked raw token: %+v", evidence)
		}
	}
	var hashType string
	var hashLength int
	if err := st.DB().QueryRowContext(ctx, `SELECT typeof(token_hash), length(token_hash)
		FROM plugin_experimental_spam_bayes_tokens WHERE user_id = ? LIMIT 1`, stored.UserID).Scan(&hashType, &hashLength); err != nil {
		t.Fatal(err)
	}
	if hashType != "blob" || hashLength != 32 {
		t.Fatalf("stored token type=%q length=%d, want 32-byte blob", hashType, hashLength)
	}
}

func TestPersonalBayesMigrationLoadsThroughManifestParser(t *testing.T) {
	ctx := context.Background()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate bayes test source")
	}
	pluginRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	manifests, err := plugins.LoadManifests(pluginRoot)
	if err != nil {
		t.Fatal(err)
	}
	var selected []plugins.Manifest
	for _, manifest := range manifests {
		if manifest.ID == pluginID {
			selected = append(selected, manifest)
		}
	}
	if len(selected) != 1 {
		t.Fatalf("experimental spam-filter manifests = %d, want 1", len(selected))
	}
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	st, err := rollstore.OpenServerWithPluginManifests(filepath.Join(dataDir, "rolltop.db"), dataDir, selected, nil)
	if err != nil {
		t.Fatalf("open store through parsed manifest migrations: %v", err)
	}
	defer st.Close()
	user, err := st.CreateUser(ctx, "loader@example.test", "Loader", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	userDB, err := st.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatalf("apply parsed user migrations: %v", err)
	}
	for _, table := range []string{
		"plugin_experimental_spam_bayes_state",
		"plugin_experimental_spam_bayes_tokens",
		"plugin_experimental_spam_bayes_documents",
		"plugin_experimental_spam_bayes_learns",
		"plugin_experimental_spam_bayes_learn_tokens",
		"plugin_experimental_spam_bayes_labels",
		"plugin_experimental_spam_bootstraps",
		"plugin_experimental_spam_pending_move_labels",
	} {
		var count int
		if err := userDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
			WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("parsed migration table %s count = %d", table, count)
		}
	}
	var applied int
	if err := userDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_migrations
		WHERE plugin_id = ? AND migration_id IN (
			'002_create_personal_bayes',
			'003_create_personal_bayes_labels',
			'004_create_personal_bayes_bootstrap'
		)`, pluginID).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 3 {
		t.Fatalf("parsed personal Bayes migration records = %d, want 3", applied)
	}
}

func TestPersonalBayesV3MigrationPreservesV2LearnsAndAppliesPrecedence(t *testing.T) {
	ctx := context.Background()
	st, err := rollstore.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v2, err := os.ReadFile(filepath.Join("..", "migrations", "user", "002_create_personal_bayes.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(string(v2)); err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(ctx, "v2-migration@example.test", "V2 migration", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256([]byte("legacy fingerprint"))
	token := sha256.Sum256([]byte("legacy token"))
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO plugin_experimental_spam_bayes_state
		(user_id, token_schema, spam_messages, ham_messages, token_count, updated_at)
		VALUES (?, ?, 1, 0, 1, 2)`, user.ID, PersonalBayesTokenSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO plugin_experimental_spam_bayes_documents
		(user_id, message_fingerprint, token_schema, created_at, updated_at) VALUES (?, ?, ?, 1, 2)`,
		user.ID, fingerprint[:], PersonalBayesTokenSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO plugin_experimental_spam_bayes_learn_tokens
		(user_id, message_fingerprint, token_hash) VALUES (?, ?, ?)`, user.ID, fingerprint[:], token[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO plugin_experimental_spam_bayes_tokens
		(user_id, token_hash, spam_messages, ham_messages, last_seen) VALUES (?, ?, 1, 0, 2)`, user.ID, token[:]); err != nil {
		t.Fatal(err)
	}
	for _, learned := range []struct {
		messageID int64
		label     string
		source    string
		updatedAt int64
	}{
		{messageID: 10, label: "ham", source: "explicit", updatedAt: 1},
		{messageID: 11, label: "spam", source: "automatic", updatedAt: 2},
	} {
		if _, err := st.DB().ExecContext(ctx, `INSERT INTO plugin_experimental_spam_bayes_learns
			(user_id, message_id, message_fingerprint, label, source, token_schema, learned_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, user.ID, learned.messageID, fingerprint[:], learned.label,
			learned.source, PersonalBayesTokenSchema, learned.updatedAt, learned.updatedAt); err != nil {
			t.Fatal(err)
		}
	}
	v3, err := os.ReadFile(filepath.Join("..", "migrations", "user", "003_create_personal_bayes_labels.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(string(v3)); err != nil {
		t.Fatal(err)
	}
	assertPersonalBayesTotals(t, ctx, st.DB(), user.ID, 0, 1)
	var labels, tokenSpam, tokenHam int64
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_experimental_spam_bayes_labels
		WHERE user_id = ?`, user.ID).Scan(&labels); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT spam_messages, ham_messages
		FROM plugin_experimental_spam_bayes_tokens WHERE user_id = ? AND token_hash = ?`, user.ID, token[:]).Scan(&tokenSpam, &tokenHam); err != nil {
		t.Fatal(err)
	}
	if labels != 2 || tokenSpam != 0 || tokenHam != 1 {
		t.Fatalf("migrated labels=%d token counts=%d/%d, want 2 and 0/1", labels, tokenSpam, tokenHam)
	}
}

func TestPersonalBayesDuplicateFingerprintUsesLatestExplicitLabel(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	first := createSpamTestMessage(t, ctx, st, "dedupe@example.test", "Duplicate first", 1)
	second := createSpamTestMessageForUser(t, ctx, st, first.UserID, "Duplicate second", 2)
	message := spammodel.Message{Subject: "same content", Body: "identical fingerprint payload"}

	if _, err := LearnPersonalBayes(ctx, st.DB(), second.UserID, second.ID, "spam", message); err != nil {
		t.Fatal(err)
	}
	if _, err := LearnPersonalBayes(ctx, st.DB(), first.UserID, first.ID, "ham", message); err != nil {
		t.Fatal(err)
	}
	assertPersonalBayesTotals(t, ctx, st.DB(), first.UserID, 0, 1)
	var documents, learns int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_experimental_spam_bayes_documents WHERE user_id = ?`, first.UserID).Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_experimental_spam_bayes_learns WHERE user_id = ?`, first.UserID).Scan(&learns); err != nil {
		t.Fatal(err)
	}
	if documents != 1 || learns != 2 {
		t.Fatalf("duplicate fingerprint documents=%d learns=%d, want 1/2", documents, learns)
	}

	// Reasserting the older membership makes that explicit feedback newest,
	// without learning a second copy of the document.
	if result, err := LearnPersonalBayes(ctx, st.DB(), second.UserID, second.ID, "spam", spammodel.Message{Body: "now mutable"}); err != nil {
		t.Fatal(err)
	} else if !result.Changed {
		t.Fatalf("reasserted noncanonical duplicate = %+v, want effective change", result)
	}
	assertPersonalBayesTotals(t, ctx, st.DB(), first.UserID, 1, 0)
	if _, err := UnlearnPersonalBayes(ctx, st.DB(), second.UserID, second.ID, "spam", spammodel.Message{}); err != nil {
		t.Fatal(err)
	}
	assertPersonalBayesTotals(t, ctx, st.DB(), first.UserID, 0, 1)
	if _, err := UnlearnPersonalBayes(ctx, st.DB(), first.UserID, first.ID, "ham", spammodel.Message{}); err != nil {
		t.Fatal(err)
	}
	assertPersonalBayesTotals(t, ctx, st.DB(), first.UserID, 0, 0)
}

func TestPersonalBayesFingerprintSourcesPrecedenceAndReveal(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	stored := createSpamTestMessage(t, ctx, st, "source-aware@example.test", "Source aware", 1)
	message := spammodel.Message{
		Subject: "Remote-only training document",
		Body:    "durable fetched message content",
		From:    "sender@example.test",
	}
	fingerprint := PersonalBayesFingerprint(message)

	automatic, err := LearnPersonalBayesFingerprint(ctx, st.DB(), stored.UserID, PersonalBayesSourceAutomatic, "spam", fingerprint, message)
	if err != nil {
		t.Fatal(err)
	}
	if !automatic.Changed || automatic.SpamMessages != 1 || automatic.HamMessages != 0 {
		t.Fatalf("automatic learn = %+v", automatic)
	}
	explicit, err := LearnPersonalBayesFingerprint(ctx, st.DB(), stored.UserID, PersonalBayesSourceExplicit, "ham", fingerprint, message)
	if err != nil {
		t.Fatal(err)
	}
	if !explicit.Changed || explicit.PreviousLabel != "" || explicit.SpamMessages != 0 || explicit.HamMessages != 1 {
		t.Fatalf("explicit override = %+v", explicit)
	}
	counts, err := GetPersonalBayesCounts(ctx, st.DB(), stored.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Effective != (PersonalBayesLabelCounts{Ham: 1}) ||
		counts.Explicit != (PersonalBayesLabelCounts{Ham: 1}) ||
		counts.Automatic != (PersonalBayesLabelCounts{}) {
		t.Fatalf("source-aware counts = %+v", counts)
	}

	revealed, err := ClearPersonalBayesFingerprint(ctx, st.DB(), stored.UserID, PersonalBayesSourceExplicit, fingerprint, "ham")
	if err != nil {
		t.Fatal(err)
	}
	if !revealed.Changed || revealed.PreviousLabel != "ham" || revealed.Label != "spam" || revealed.SpamMessages != 1 || revealed.HamMessages != 0 {
		t.Fatalf("clear explicit reveal automatic = %+v", revealed)
	}
	counts, err = GetPersonalBayesCounts(ctx, st.DB(), stored.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Automatic != (PersonalBayesLabelCounts{Spam: 1}) || counts.Explicit != (PersonalBayesLabelCounts{}) {
		t.Fatalf("revealed source counts = %+v", counts)
	}
	cleared, err := ClearPersonalBayesFingerprint(ctx, st.DB(), stored.UserID, PersonalBayesSourceAutomatic, fingerprint, "spam")
	if err != nil {
		t.Fatal(err)
	}
	if !cleared.Changed || cleared.SpamMessages != 0 || cleared.HamMessages != 0 || cleared.StoredTokens != 0 {
		t.Fatalf("clear automatic = %+v", cleared)
	}
}

func TestPersonalBayesAutomaticSnapshotIsAtomicIdempotentAndResettable(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	stored := createSpamTestMessage(t, ctx, st, "snapshot@example.test", "Snapshot", 1)
	spamMessage := spammodel.Message{Subject: "junk", Body: "snapshot spam payload"}
	hamMessage := spammodel.Message{Subject: "wanted", Body: "snapshot ham payload"}
	documents := []PersonalBayesTrainingDocument{
		{Fingerprint: PersonalBayesFingerprint(spamMessage), Label: "spam", Message: spamMessage},
		{Fingerprint: PersonalBayesFingerprint(hamMessage), Label: "ham", Message: hamMessage},
	}
	created, err := ReplaceAutomaticPersonalBayesSnapshot(ctx, st.DB(), stored.UserID, documents)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Changed || created.SpamMessages != 1 || created.HamMessages != 1 || created.Accepted != (PersonalBayesLabelCounts{Spam: 1, Ham: 1}) {
		t.Fatalf("created snapshot = %+v", created)
	}
	repeated, err := ReplaceAutomaticPersonalBayesSnapshot(ctx, st.DB(), stored.UserID, documents)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Changed || repeated.SpamMessages != 1 || repeated.HamMessages != 1 || repeated.Counts.Automatic != (PersonalBayesLabelCounts{Spam: 1, Ham: 1}) {
		t.Fatalf("repeated snapshot = %+v", repeated)
	}

	invalid := append([]PersonalBayesTrainingDocument(nil), documents...)
	invalid[0].Fingerprint = sha256.Sum256([]byte("not the message fingerprint"))
	if _, err := ReplaceAutomaticPersonalBayesSnapshot(ctx, st.DB(), stored.UserID, invalid); !errors.Is(err, ErrPersonalBayesFingerprint) {
		t.Fatalf("invalid snapshot error = %v, want fingerprint mismatch", err)
	}
	assertPersonalBayesTotals(t, ctx, st.DB(), stored.UserID, 1, 1)

	if _, err := LearnPersonalBayesFingerprint(ctx, st.DB(), stored.UserID, PersonalBayesSourceExplicit, "ham", documents[0].Fingerprint, spamMessage); err != nil {
		t.Fatal(err)
	}
	assertPersonalBayesTotals(t, ctx, st.DB(), stored.UserID, 0, 2)
	reset, err := ResetAutomaticPersonalBayes(ctx, st.DB(), stored.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if !reset.Changed || reset.SpamMessages != 0 || reset.HamMessages != 1 {
		t.Fatalf("reset automatic preserving explicit = %+v", reset)
	}
	counts, err := GetPersonalBayesCounts(ctx, st.DB(), stored.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Explicit.Ham != 1 || counts.Automatic.Spam != 0 || counts.Automatic.Ham != 0 || counts.Effective.Ham != 1 {
		t.Fatalf("post-reset counts = %+v", counts)
	}
}

func TestPersonalBayesAutomaticResetIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	first := createSpamTestMessage(t, ctx, st, "reset-first@example.test", "First", 1)
	second := createSpamTestMessage(t, ctx, st, "reset-second@example.test", "Second", 2)
	firstMessage := spammodel.Message{Subject: "first automatic", Body: "first automatic spam"}
	secondMessage := spammodel.Message{Subject: "second automatic", Body: "second automatic ham"}
	if _, err := ReplaceAutomaticPersonalBayesSnapshot(ctx, st.DB(), first.UserID, []PersonalBayesTrainingDocument{{
		Fingerprint: PersonalBayesFingerprint(firstMessage), Label: feedbackSpam, Message: firstMessage,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplaceAutomaticPersonalBayesSnapshot(ctx, st.DB(), second.UserID, []PersonalBayesTrainingDocument{{
		Fingerprint: PersonalBayesFingerprint(secondMessage), Label: feedbackHam, Message: secondMessage,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ResetAutomaticPersonalBayes(ctx, st.DB(), first.UserID); err != nil {
		t.Fatal(err)
	}
	firstCounts, err := GetPersonalBayesCounts(ctx, st.DB(), first.UserID)
	if err != nil {
		t.Fatal(err)
	}
	secondCounts, err := GetPersonalBayesCounts(ctx, st.DB(), second.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if firstCounts.Effective != (PersonalBayesLabelCounts{}) || firstCounts.Automatic != (PersonalBayesLabelCounts{}) {
		t.Fatalf("reset first counts = %+v", firstCounts)
	}
	if secondCounts.Effective != (PersonalBayesLabelCounts{Ham: 1}) || secondCounts.Automatic != (PersonalBayesLabelCounts{Ham: 1}) {
		t.Fatalf("other tenant counts after reset = %+v", secondCounts)
	}
}

func TestPersonalBayesExpiryPreservesFeedbackAndCanRelearn(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	first := createSpamTestMessage(t, ctx, st, "expiry@example.test", "Expiry first", 1)
	second := createSpamTestMessageForUser(t, ctx, st, first.UserID, "Expiry second", 2)
	third := createSpamTestMessageForUser(t, ctx, st, first.UserID, "Expiry third", 3)
	for index, stored := range []rollstore.MessageRecord{first, second, third} {
		message := spammodel.Message{Body: fmt.Sprintf("expiry unique payload %d", index)}
		if _, err := LearnPersonalBayes(ctx, st.DB(), stored.UserID, stored.ID, "spam", message); err != nil {
			t.Fatal(err)
		}
		if err := setFeedback(ctx, st.DB(), stored.UserID, stored.ID, "spam"); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := expirePersonalBayesLearns(ctx, tx, first.UserID, 2, 99); err != nil {
		t.Fatal(err)
	}
	if err := refreshPersonalBayesTokenCount(ctx, tx, first.UserID, 99); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertPersonalBayesTotals(t, ctx, st.DB(), first.UserID, 2, 0)
	if label, err := getFeedback(ctx, st.DB(), first.UserID, first.ID); err != nil || label != "spam" {
		t.Fatalf("expired membership feedback = %q error=%v", label, err)
	}
	if result, err := UnlearnPersonalBayes(ctx, st.DB(), first.UserID, first.ID, "spam", spammodel.Message{}); err != nil {
		t.Fatal(err)
	} else if result.Changed {
		t.Fatalf("clearing expired membership changed Bayes counts: %+v", result)
	}
	if err := clearFeedback(ctx, st.DB(), first.UserID, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := LearnPersonalBayes(ctx, st.DB(), first.UserID, first.ID, "ham", spammodel.Message{Body: "relearned after expiry"}); err != nil {
		t.Fatal(err)
	}
	assertPersonalBayesTotals(t, ctx, st.DB(), first.UserID, 2, 1)
}

func TestPersonalBayesMessageDeleteLeavesPrunableLearn(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	stored := createSpamTestMessage(t, ctx, st, "delete@example.test", "Delete learned", 1)
	if _, err := LearnPersonalBayes(ctx, st.DB(), stored.UserID, stored.ID, "spam", spammodel.Message{Body: "stored hashes survive deletion"}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteMessageForUser(ctx, stored.UserID, stored.ID); err != nil {
		t.Fatalf("delete learned local message: %v", err)
	}
	assertPersonalBayesTotals(t, ctx, st.DB(), stored.UserID, 1, 0)
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := expirePersonalBayesLearns(ctx, tx, stored.UserID, 0, 100); err != nil {
		t.Fatal(err)
	}
	if err := refreshPersonalBayesTokenCount(ctx, tx, stored.UserID, 100); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertPersonalBayesTotals(t, ctx, st.DB(), stored.UserID, 0, 0)
}

func TestPersonalBayesTokenGrowthBoundsAndRenews(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	first := createSpamTestMessage(t, ctx, st, "bounds@example.test", "Bounds first", 1)
	second := createSpamTestMessageForUser(t, ctx, st, first.UserID, "Bounds second", 2)
	largeMessage := func(prefix string) spammodel.Message {
		var body strings.Builder
		for index := 0; index < 10_000; index++ {
			fmt.Fprintf(&body, "%s%05d ", prefix, index)
		}
		return spammodel.Message{Body: body.String()}
	}
	firstMessage := largeMessage("firstword")
	if _, err := LearnPersonalBayes(ctx, st.DB(), first.UserID, first.ID, "spam", firstMessage); err != nil {
		t.Fatal(err)
	}
	var learnedTokens, storedTokens int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_experimental_spam_bayes_learn_tokens WHERE user_id = ?`, first.UserID).Scan(&learnedTokens); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT token_count FROM plugin_experimental_spam_bayes_state WHERE user_id = ?`, first.UserID).Scan(&storedTokens); err != nil {
		t.Fatal(err)
	}
	if learnedTokens != personalBayesMaxLearnedTokensPerMessage || storedTokens != personalBayesMaxLearnedTokensPerMessage {
		t.Fatalf("per-message learned=%d stored=%d, want %d", learnedTokens, storedTokens, personalBayesMaxLearnedTokensPerMessage)
	}
	secondTokens := TokenizePersonalBayesMessage(largeMessage("secondword"))
	if len(secondTokens) > personalBayesMaxLearnedTokensPerMessage {
		secondTokens = secondTokens[:personalBayesMaxLearnedTokensPerMessage]
	}
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := admitPersonalBayesTokens(ctx, tx, first.UserID, secondTokens, 1, storedTokens)
	if err != nil {
		t.Fatal(err)
	}
	if len(admitted) != 0 {
		t.Fatalf("full vocabulary admitted %d new tokens", len(admitted))
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := UnlearnPersonalBayes(ctx, st.DB(), first.UserID, first.ID, "spam", spammodel.Message{}); err != nil {
		t.Fatal(err)
	}
	if _, err := LearnPersonalBayes(ctx, st.DB(), second.UserID, second.ID, "ham", largeMessage("secondword")); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT token_count FROM plugin_experimental_spam_bayes_state WHERE user_id = ?`, first.UserID).Scan(&storedTokens); err != nil {
		t.Fatal(err)
	}
	if storedTokens != personalBayesMaxLearnedTokensPerMessage || storedTokens > personalBayesMaxStoredTokens {
		t.Fatalf("renewed vocabulary = %d", storedTokens)
	}
}

func assertPersonalBayesTotals(t *testing.T, ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, userID, wantSpam, wantHam int64) {
	t.Helper()
	var spamMessages, hamMessages int64
	if err := db.QueryRowContext(ctx, `SELECT spam_messages, ham_messages
		FROM plugin_experimental_spam_bayes_state WHERE user_id = ?`, userID).Scan(&spamMessages, &hamMessages); err != nil {
		t.Fatal(err)
	}
	if spamMessages != wantSpam || hamMessages != wantHam {
		t.Fatalf("Bayes totals = %d/%d, want %d/%d", spamMessages, hamMessages, wantSpam, wantHam)
	}
}

func TestPersonalBayesChiSquareCombiner(t *testing.T) {
	spam := combinePersonalBayesProbabilities(200, 200, []float64{.99})
	ham := combinePersonalBayesProbabilities(200, 200, []float64{.01})
	neutral := combinePersonalBayesProbabilities(200, 200, []float64{.5, .5})
	if math.Abs(spam-.745) > .001 {
		t.Fatalf("one .99 token combined to %.6f, want approximately .745", spam)
	}
	if math.Abs(ham-.255) > .001 {
		t.Fatalf("one .01 token combined to %.6f, want approximately .255", ham)
	}
	if math.Abs(neutral-.5) > 1e-12 {
		t.Fatalf("neutral tokens combined to %.12f, want .5", neutral)
	}
}
