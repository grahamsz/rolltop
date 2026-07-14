package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"math"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

const (
	// PersonalBayesTokenSchema is persisted with every tenant's learned state.
	// Changing token semantics requires rebuilding counts from explicit labels.
	PersonalBayesTokenSchema = "rolltop-personal-bayes-tokens-v1"

	PersonalBayesMinimumSpamMessages = int64(200)
	PersonalBayesMinimumHamMessages  = int64(200)

	PersonalBayesSourceExplicit  = "explicit"
	PersonalBayesSourceAutomatic = "automatic"

	personalBayesMaxBodyBytes    = 64 << 10
	personalBayesMaxSubjectBytes = 4 << 10
	personalBayesMaxUniqueTokens = 8192
	// SpamAssassin bounds its Bayes database and expires old entries. Rolltop
	// applies both a per-document admission limit and hard per-user limits so
	// explicit learning cannot grow SQLite without bound.
	personalBayesMaxLearnedTokensPerMessage = 2048
	personalBayesMaxStoredTokens            = 150_000
	personalBayesMaxLearnedMessages         = 2_000
	personalBayesMaxTokenRunes              = 15
	personalBayesLongTokenRunes             = 7
	personalBayesMaxSignificant             = 150
	personalBayesMaxEvidence                = 12
	personalBayesTokenQueryBatch            = 200
	personalBayesMinProbStrength            = 0.346
	personalBayesRobinsonX                  = 0.538
	personalBayesRobinsonStrength           = 0.030
	personalBayesDefaultProbability         = 0.5
)

var (
	ErrPersonalBayesInvalidLabel    = errors.New("personal Bayes label must be spam or ham")
	ErrPersonalBayesMessageNotOwned = errors.New("personal Bayes message is not owned by user")
	ErrPersonalBayesSchemaMismatch  = errors.New("personal Bayes token schema is incompatible")
	ErrPersonalBayesLabelMismatch   = errors.New("personal Bayes learned label does not match")
	ErrPersonalBayesInconsistent    = errors.New("personal Bayes counts are inconsistent")
	ErrPersonalBayesInvalidSource   = errors.New("personal Bayes source must be explicit or automatic")
	ErrPersonalBayesFingerprint     = errors.New("personal Bayes fingerprint does not match message")
	ErrPersonalBayesLimitExceeded   = errors.New("personal Bayes learned document limit exceeded")
)

// PersonalBayesToken is an in-memory token. Only Hash is stored in SQLite;
// Feature is retained transiently to produce bounded runtime evidence.
type PersonalBayesToken struct {
	Hash    [sha256.Size]byte
	Feature string
}

type PersonalBayesEvidence struct {
	Category     string  `json:"category"`
	TokenID      string  `json:"token_id"`
	Probability  float64 `json:"probability"`
	SpamMessages int64   `json:"spam_messages"`
	HamMessages  int64   `json:"ham_messages"`
}

type PersonalBayesScore struct {
	Ready         bool                    `json:"ready"`
	Probability   float64                 `json:"probability"`
	SpamMessages  int64                   `json:"spam_messages"`
	HamMessages   int64                   `json:"ham_messages"`
	StoredTokens  int64                   `json:"stored_tokens"`
	TokensUsed    int                     `json:"tokens_used"`
	Evidence      []PersonalBayesEvidence `json:"evidence,omitempty"`
	FeatureSchema string                  `json:"feature_schema"`
}

type PersonalBayesLearnResult struct {
	Changed       bool   `json:"changed"`
	PreviousLabel string `json:"previous_label,omitempty"`
	Label         string `json:"label,omitempty"`
	SpamMessages  int64  `json:"spam_messages"`
	HamMessages   int64  `json:"ham_messages"`
	StoredTokens  int64  `json:"stored_tokens"`
}

// PersonalBayesTrainingDocument is a body-independent training identity plus
// the transient message content needed to derive its bounded token set. The
// Fingerprint must be generated with PersonalBayesFingerprint.
type PersonalBayesTrainingDocument struct {
	Fingerprint [sha256.Size]byte
	Label       string
	Message     spammodel.Message
}

type PersonalBayesLabelCounts struct {
	Spam int64 `json:"spam"`
	Ham  int64 `json:"ham"`
}

// PersonalBayesCounts attributes effective unique fingerprints by source.
// Explicit labels always take precedence, so overridden automatic labels are
// intentionally absent from Automatic and from the Effective total.
type PersonalBayesCounts struct {
	Effective PersonalBayesLabelCounts `json:"effective"`
	Explicit  PersonalBayesLabelCounts `json:"explicit"`
	Automatic PersonalBayesLabelCounts `json:"automatic"`
	Ready     bool                     `json:"ready"`
}

type PersonalBayesSnapshotResult struct {
	PersonalBayesLearnResult
	Accepted PersonalBayesLabelCounts `json:"accepted"`
	Counts   PersonalBayesCounts      `json:"counts"`
}

type personalBayesTokenCounts struct {
	spam int64
	ham  int64
}

type personalBayesRowsQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type personalBayesQueryer interface {
	personalBayesRowsQueryer
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type personalBayesCandidate struct {
	token       PersonalBayesToken
	probability float64
	strength    float64
	spam        int64
	ham         int64
}

type personalBayesLearnedMessage struct {
	fingerprint [sha256.Size]byte
	messageID   int64
	label       string
	source      string
	originKey   string
	schema      string
}

type personalBayesCanonical struct {
	messageID int64
	label     string
	source    string
	originKey string
}

// TokenizePersonalBayesMessage extracts a deterministic, bounded set of
// document-presence tokens. Repeating a word within a message never increases
// its learned count.
func TokenizePersonalBayesMessage(message spammodel.Message) []PersonalBayesToken {
	seen := make(map[[sha256.Size]byte]struct{}, 1024)
	tokens := make([]PersonalBayesToken, 0, 1024)
	add := func(feature string) bool {
		if len(tokens) >= personalBayesMaxUniqueTokens {
			return false
		}
		feature = boundPersonalBayesFeature(feature)
		if feature == "" {
			return true
		}
		digest := hashPersonalBayesFeature(feature)
		if _, exists := seen[digest]; exists {
			return true
		}
		seen[digest] = struct{}{}
		tokens = append(tokens, PersonalBayesToken{Hash: digest, Feature: feature})
		return true
	}

	addPersonalBayesAddress(add, "from", message.From)
	for _, recipient := range message.To {
		if !addPersonalBayesAddress(add, "to", recipient) {
			break
		}
	}
	if mimeType := normalizePersonalBayesMIME(message.MIMEType); mimeType != "" {
		add("mime:" + mimeType)
	}
	if message.HTML || strings.Contains(strings.ToLower(message.MIMEType), "html") {
		add("structure:html")
	}
	attachmentTypes := append([]string(nil), message.AttachmentTypes...)
	sort.Strings(attachmentTypes)
	for _, attachmentType := range attachmentTypes {
		if normalized := normalizePersonalBayesMIME(attachmentType); normalized != "" {
			if !add("attachment:" + normalized) {
				break
			}
		}
	}

	body := truncatePersonalBayesUTF8(message.Body, personalBayesMaxBodyBytes)
	for _, host := range spammodel.URLHosts(message.Subject+" "+body, 64) {
		if !add("url-host:" + strings.ToLower(host)) {
			break
		}
	}
	addPersonalBayesText(add, "subject", truncatePersonalBayesUTF8(message.Subject, personalBayesMaxSubjectBytes))
	addPersonalBayesText(add, "body", body)

	sort.Slice(tokens, func(i, j int) bool {
		return bytes.Compare(tokens[i].Hash[:], tokens[j].Hash[:]) < 0
	})
	return tokens
}

// LearnPersonalBayes records one explicit, tenant-owned document. Repeating
// the same label is idempotent; an opposite explicit label atomically reverses
// the old counts before applying the new label.
func LearnPersonalBayes(ctx context.Context, db *sql.DB, userID, messageID int64, label string, message spammodel.Message) (PersonalBayesLearnResult, error) {
	if db == nil || userID <= 0 || messageID <= 0 {
		return PersonalBayesLearnResult{}, ErrPersonalBayesMessageNotOwned
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := LearnPersonalBayesTx(ctx, tx, userID, messageID, label, message)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	return result, nil
}

// LearnPersonalBayesTx performs the same mutation inside a caller-owned
// transaction. It lets feedback and Bayes state commit or roll back together.
func LearnPersonalBayesTx(ctx context.Context, tx *sql.Tx, userID, messageID int64, label string, message spammodel.Message) (PersonalBayesLearnResult, error) {
	if tx == nil || userID <= 0 || messageID <= 0 {
		return PersonalBayesLearnResult{}, ErrPersonalBayesMessageNotOwned
	}
	label = strings.ToLower(strings.TrimSpace(label))
	if !validPersonalBayesLabel(label) {
		return PersonalBayesLearnResult{}, ErrPersonalBayesInvalidLabel
	}
	now := time.Now().UTC().UnixNano()

	if err := requirePersonalBayesMessageOwner(ctx, tx, userID, messageID); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if err := ensurePersonalBayesState(ctx, tx, userID, now); err != nil {
		return PersonalBayesLearnResult{}, err
	}

	previous, found, err := personalBayesLearnedByMessageID(ctx, tx, userID, messageID)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if found && previous.schema != PersonalBayesTokenSchema {
		return PersonalBayesLearnResult{}, ErrPersonalBayesSchemaMismatch
	}
	if found {
		tokens, err := loadPersonalBayesDocumentTokens(ctx, tx, userID, previous.fingerprint)
		if err != nil {
			return PersonalBayesLearnResult{}, err
		}
		before, beforeFound, err := personalBayesCanonicalMessage(ctx, tx, userID, previous.fingerprint)
		if err != nil {
			return PersonalBayesLearnResult{}, err
		}
		if previous.label == label && beforeFound && before.source == PersonalBayesSourceExplicit && before.originKey == previous.originKey {
			result, resultErr := personalBayesLearnResult(ctx, tx, userID)
			if resultErr != nil {
				return PersonalBayesLearnResult{}, resultErr
			}
			result.Label = label
			return result, nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE plugin_experimental_spam_bayes_labels
			SET label = ?, updated_at = ?
			WHERE user_id = ? AND message_fingerprint = ? AND source = 'explicit' AND origin_key = ?`,
			label, now, userID, previous.fingerprint[:], previous.originKey); err != nil {
			return PersonalBayesLearnResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE plugin_experimental_spam_bayes_learns
			SET label = ?, source = 'explicit', updated_at = ? WHERE user_id = ? AND message_id = ?`,
			label, now, userID, messageID); err != nil {
			return PersonalBayesLearnResult{}, err
		}
		after, afterFound, err := personalBayesCanonicalMessage(ctx, tx, userID, previous.fingerprint)
		if err != nil {
			return PersonalBayesLearnResult{}, err
		}
		if err := changePersonalBayesCanonical(ctx, tx, userID, tokens, before.label, beforeFound, after.label, afterFound, now); err != nil {
			return PersonalBayesLearnResult{}, err
		}
	} else {
		fingerprint := fingerprintPersonalBayesMessage(message)
		tokens, err := ensurePersonalBayesDocument(ctx, tx, userID, fingerprint, TokenizePersonalBayesMessage(message), now)
		if err != nil {
			return PersonalBayesLearnResult{}, err
		}
		before, beforeFound, err := personalBayesCanonicalMessage(ctx, tx, userID, fingerprint)
		if err != nil {
			return PersonalBayesLearnResult{}, err
		}
		originKey := personalBayesMessageOriginKey(messageID)
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_experimental_spam_bayes_labels
			(user_id, message_fingerprint, source, origin_key, message_id, label, token_schema, learned_at, updated_at)
			VALUES (?, ?, 'explicit', ?, ?, ?, ?, ?, ?)`,
			userID, fingerprint[:], originKey, messageID, label, PersonalBayesTokenSchema, now, now); err != nil {
			return PersonalBayesLearnResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_experimental_spam_bayes_learns
			(user_id, message_id, message_fingerprint, label, source, token_schema, learned_at, updated_at)
			VALUES (?, ?, ?, ?, 'explicit', ?, ?, ?)`,
			userID, messageID, fingerprint[:], label, PersonalBayesTokenSchema, now, now); err != nil {
			return PersonalBayesLearnResult{}, err
		}
		after, afterFound, err := personalBayesCanonicalMessage(ctx, tx, userID, fingerprint)
		if err != nil {
			return PersonalBayesLearnResult{}, err
		}
		if err := changePersonalBayesCanonical(ctx, tx, userID, tokens, before.label, beforeFound, after.label, afterFound, now); err != nil {
			return PersonalBayesLearnResult{}, err
		}
	}
	if err := expirePersonalBayesLearns(ctx, tx, userID, personalBayesMaxLearnedMessages, now); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	fingerprint := fingerprintPersonalBayesMessage(message)
	if previous.fingerprint != ([sha256.Size]byte{}) {
		fingerprint = previous.fingerprint
	}
	if tokens, tokenErr := loadPersonalBayesDocumentTokens(ctx, tx, userID, fingerprint); tokenErr != nil {
		return PersonalBayesLearnResult{}, tokenErr
	} else if err := topUpPersonalBayesDocument(ctx, tx, userID, fingerprint, tokens, now); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if err := refreshPersonalBayesTokenCount(ctx, tx, userID, now); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	result, err := personalBayesLearnResult(ctx, tx, userID)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	result.Changed = true
	result.PreviousLabel = previous.label
	result.Label = label
	return result, nil
}

// LearnPersonalBayesFingerprint learns content that may not have a local
// messages row. Fingerprint must have been computed from message with
// PersonalBayesFingerprint. A source-specific label is upserted; explicit
// labels take precedence over automatic labels for the same fingerprint.
func LearnPersonalBayesFingerprint(ctx context.Context, db *sql.DB, userID int64, source, label string, fingerprint [sha256.Size]byte, message spammodel.Message) (PersonalBayesLearnResult, error) {
	if db == nil || userID <= 0 {
		return PersonalBayesLearnResult{}, ErrPersonalBayesMessageNotOwned
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := LearnPersonalBayesFingerprintTx(ctx, tx, userID, source, label, fingerprint, message)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	return result, nil
}

// LearnPersonalBayesFingerprintTx is the caller-owned transaction form of
// LearnPersonalBayesFingerprint.
func LearnPersonalBayesFingerprintTx(ctx context.Context, tx *sql.Tx, userID int64, source, label string, fingerprint [sha256.Size]byte, message spammodel.Message) (PersonalBayesLearnResult, error) {
	if tx == nil || userID <= 0 {
		return PersonalBayesLearnResult{}, ErrPersonalBayesMessageNotOwned
	}
	source = strings.ToLower(strings.TrimSpace(source))
	label = strings.ToLower(strings.TrimSpace(label))
	if !validPersonalBayesSource(source) {
		return PersonalBayesLearnResult{}, ErrPersonalBayesInvalidSource
	}
	if !validPersonalBayesLabel(label) {
		return PersonalBayesLearnResult{}, ErrPersonalBayesInvalidLabel
	}
	if fingerprint != fingerprintPersonalBayesMessage(message) {
		return PersonalBayesLearnResult{}, ErrPersonalBayesFingerprint
	}
	now := time.Now().UTC().UnixNano()
	if err := ensurePersonalBayesState(ctx, tx, userID, now); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	return learnPersonalBayesFingerprintTx(ctx, tx, userID, source, personalBayesFingerprintOriginKey(source), label, fingerprint, message, now, true)
}

// ClearPersonalBayesFingerprint removes one source-specific fingerprint label.
// If an explicit label is cleared, an automatic label for the same fingerprint
// becomes effective again. expectedLabel protects against stale callers.
func ClearPersonalBayesFingerprint(ctx context.Context, db *sql.DB, userID int64, source string, fingerprint [sha256.Size]byte, expectedLabel string) (PersonalBayesLearnResult, error) {
	if db == nil || userID <= 0 {
		return PersonalBayesLearnResult{}, ErrPersonalBayesMessageNotOwned
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := ClearPersonalBayesFingerprintTx(ctx, tx, userID, source, fingerprint, expectedLabel)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	return result, nil
}

func ClearPersonalBayesFingerprintTx(ctx context.Context, tx *sql.Tx, userID int64, source string, fingerprint [sha256.Size]byte, expectedLabel string) (PersonalBayesLearnResult, error) {
	if tx == nil || userID <= 0 {
		return PersonalBayesLearnResult{}, ErrPersonalBayesMessageNotOwned
	}
	source = strings.ToLower(strings.TrimSpace(source))
	expectedLabel = strings.ToLower(strings.TrimSpace(expectedLabel))
	if !validPersonalBayesSource(source) {
		return PersonalBayesLearnResult{}, ErrPersonalBayesInvalidSource
	}
	if expectedLabel != "" && !validPersonalBayesLabel(expectedLabel) {
		return PersonalBayesLearnResult{}, ErrPersonalBayesInvalidLabel
	}
	now := time.Now().UTC().UnixNano()
	if err := ensurePersonalBayesState(ctx, tx, userID, now); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	return clearPersonalBayesLabelTx(ctx, tx, userID, source, personalBayesFingerprintOriginKey(source), fingerprint, expectedLabel, now)
}

// UnlearnPersonalBayes reverses a previously learned explicit document. The
// expected label prevents callers from decrementing the wrong class.
func UnlearnPersonalBayes(ctx context.Context, db *sql.DB, userID, messageID int64, label string, message spammodel.Message) (PersonalBayesLearnResult, error) {
	if db == nil || userID <= 0 || messageID <= 0 {
		return PersonalBayesLearnResult{}, ErrPersonalBayesMessageNotOwned
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := UnlearnPersonalBayesTx(ctx, tx, userID, messageID, label, message)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	return result, nil
}

// UnlearnPersonalBayesTx reverses a learned message inside a caller-owned
// transaction so clearing feedback and counts can be one atomic operation.
func UnlearnPersonalBayesTx(ctx context.Context, tx *sql.Tx, userID, messageID int64, label string, _ spammodel.Message) (PersonalBayesLearnResult, error) {
	if tx == nil || userID <= 0 || messageID <= 0 {
		return PersonalBayesLearnResult{}, ErrPersonalBayesMessageNotOwned
	}
	label = strings.ToLower(strings.TrimSpace(label))
	if !validPersonalBayesLabel(label) {
		return PersonalBayesLearnResult{}, ErrPersonalBayesInvalidLabel
	}

	now := time.Now().UTC().UnixNano()

	if err := requirePersonalBayesMessageOwner(ctx, tx, userID, messageID); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	stored, found, err := personalBayesLearnedByMessageID(ctx, tx, userID, messageID)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if !found {
		result, resultErr := personalBayesLearnResult(ctx, tx, userID)
		if errors.Is(resultErr, sql.ErrNoRows) {
			result = PersonalBayesLearnResult{}
			resultErr = nil
		}
		if resultErr != nil {
			return PersonalBayesLearnResult{}, resultErr
		}
		return result, nil
	}
	if stored.schema != PersonalBayesTokenSchema {
		return PersonalBayesLearnResult{}, ErrPersonalBayesSchemaMismatch
	}
	if stored.label != label {
		return PersonalBayesLearnResult{}, ErrPersonalBayesLabelMismatch
	}
	if err := requirePersonalBayesSchema(ctx, tx, userID); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	tokens, err := loadPersonalBayesDocumentTokens(ctx, tx, userID, stored.fingerprint)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	before, beforeFound, err := personalBayesCanonicalMessage(ctx, tx, userID, stored.fingerprint)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_experimental_spam_bayes_labels
		WHERE user_id = ? AND message_fingerprint = ? AND source = 'explicit' AND origin_key = ?`,
		userID, stored.fingerprint[:], stored.originKey); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_experimental_spam_bayes_learns
		WHERE user_id = ? AND message_id = ?`, userID, messageID); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	after, afterFound, err := personalBayesCanonicalMessage(ctx, tx, userID, stored.fingerprint)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if err := changePersonalBayesCanonical(ctx, tx, userID, tokens, before.label, beforeFound, after.label, afterFound, now); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if !afterFound {
		if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_experimental_spam_bayes_documents
			WHERE user_id = ? AND message_fingerprint = ?`, userID, stored.fingerprint[:]); err != nil {
			return PersonalBayesLearnResult{}, err
		}
	}
	if err := refreshPersonalBayesTokenCount(ctx, tx, userID, now); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	result, err := personalBayesLearnResult(ctx, tx, userID)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	result.Changed = true
	result.PreviousLabel = stored.label
	return result, nil
}

// ReplaceAutomaticPersonalBayesSnapshot atomically replaces every automatic
// label for a tenant. Explicit labels are retained and remain authoritative.
// A validation error or storage failure leaves the previous snapshot intact.
func ReplaceAutomaticPersonalBayesSnapshot(ctx context.Context, db *sql.DB, userID int64, documents []PersonalBayesTrainingDocument) (PersonalBayesSnapshotResult, error) {
	if db == nil || userID <= 0 {
		return PersonalBayesSnapshotResult{}, ErrPersonalBayesMessageNotOwned
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return PersonalBayesSnapshotResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := ReplaceAutomaticPersonalBayesSnapshotTx(ctx, tx, userID, documents)
	if err != nil {
		return PersonalBayesSnapshotResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersonalBayesSnapshotResult{}, err
	}
	return result, nil
}

func ReplaceAutomaticPersonalBayesSnapshotTx(ctx context.Context, tx *sql.Tx, userID int64, documents []PersonalBayesTrainingDocument) (PersonalBayesSnapshotResult, error) {
	if tx == nil || userID <= 0 {
		return PersonalBayesSnapshotResult{}, ErrPersonalBayesMessageNotOwned
	}
	prepared := make(map[[sha256.Size]byte]PersonalBayesTrainingDocument, len(documents))
	for _, document := range documents {
		document.Label = strings.ToLower(strings.TrimSpace(document.Label))
		if !validPersonalBayesLabel(document.Label) {
			return PersonalBayesSnapshotResult{}, ErrPersonalBayesInvalidLabel
		}
		if document.Fingerprint != fingerprintPersonalBayesMessage(document.Message) {
			return PersonalBayesSnapshotResult{}, ErrPersonalBayesFingerprint
		}
		if previous, exists := prepared[document.Fingerprint]; exists && previous.Label != document.Label {
			return PersonalBayesSnapshotResult{}, ErrPersonalBayesLabelMismatch
		}
		prepared[document.Fingerprint] = document
	}
	if len(prepared) > personalBayesMaxLearnedMessages {
		return PersonalBayesSnapshotResult{}, ErrPersonalBayesLimitExceeded
	}
	now := time.Now().UTC().UnixNano()
	if err := ensurePersonalBayesState(ctx, tx, userID, now); err != nil {
		return PersonalBayesSnapshotResult{}, err
	}
	var explicitCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_experimental_spam_bayes_labels
		WHERE user_id = ? AND source = 'explicit'`, userID).Scan(&explicitCount); err != nil {
		return PersonalBayesSnapshotResult{}, err
	}
	if explicitCount+len(prepared) > personalBayesMaxLearnedMessages {
		return PersonalBayesSnapshotResult{}, ErrPersonalBayesLimitExceeded
	}

	existing, err := automaticPersonalBayesLabels(ctx, tx, userID)
	if err != nil {
		return PersonalBayesSnapshotResult{}, err
	}
	accepted := personalBayesAcceptedCounts(prepared)
	if equalPersonalBayesSnapshot(existing, prepared) {
		learn, resultErr := personalBayesLearnResult(ctx, tx, userID)
		if resultErr != nil {
			return PersonalBayesSnapshotResult{}, resultErr
		}
		counts, resultErr := getPersonalBayesCounts(ctx, tx, userID)
		return PersonalBayesSnapshotResult{PersonalBayesLearnResult: learn, Accepted: accepted, Counts: counts}, resultErr
	}
	for _, label := range existing {
		if _, err := clearPersonalBayesLabelTx(ctx, tx, userID, PersonalBayesSourceAutomatic, label.originKey, label.fingerprint, "", now); err != nil {
			return PersonalBayesSnapshotResult{}, err
		}
	}
	fingerprints := make([][sha256.Size]byte, 0, len(prepared))
	for fingerprint := range prepared {
		fingerprints = append(fingerprints, fingerprint)
	}
	sort.Slice(fingerprints, func(i, j int) bool {
		return bytes.Compare(fingerprints[i][:], fingerprints[j][:]) < 0
	})
	for _, fingerprint := range fingerprints {
		document := prepared[fingerprint]
		if _, err := learnPersonalBayesFingerprintTx(ctx, tx, userID, PersonalBayesSourceAutomatic, personalBayesFingerprintOriginKey(PersonalBayesSourceAutomatic), document.Label, fingerprint, document.Message, now, false); err != nil {
			return PersonalBayesSnapshotResult{}, err
		}
	}
	if err := refreshPersonalBayesTokenCount(ctx, tx, userID, now); err != nil {
		return PersonalBayesSnapshotResult{}, err
	}
	result, err := personalBayesLearnResult(ctx, tx, userID)
	if err != nil {
		return PersonalBayesSnapshotResult{}, err
	}
	result.Changed = true
	counts, err := getPersonalBayesCounts(ctx, tx, userID)
	if err != nil {
		return PersonalBayesSnapshotResult{}, err
	}
	return PersonalBayesSnapshotResult{PersonalBayesLearnResult: result, Accepted: accepted, Counts: counts}, nil
}

func ResetAutomaticPersonalBayes(ctx context.Context, db *sql.DB, userID int64) (PersonalBayesSnapshotResult, error) {
	return ReplaceAutomaticPersonalBayesSnapshot(ctx, db, userID, nil)
}

func ResetAutomaticPersonalBayesTx(ctx context.Context, tx *sql.Tx, userID int64) (PersonalBayesSnapshotResult, error) {
	return ReplaceAutomaticPersonalBayesSnapshotTx(ctx, tx, userID, nil)
}

// ScorePersonalBayes scores a message using only the authenticated tenant's
// learned document counts. Until both classes contain 200 messages it returns
// a neutral probability with Ready false.
func ScorePersonalBayes(ctx context.Context, db *sql.DB, userID int64, message spammodel.Message) (PersonalBayesScore, error) {
	score := PersonalBayesScore{
		Probability:   personalBayesDefaultProbability,
		FeatureSchema: PersonalBayesTokenSchema,
	}
	if db == nil || userID <= 0 {
		return score, ErrPersonalBayesMessageNotOwned
	}
	var schema string
	err := db.QueryRowContext(ctx, `SELECT token_schema, spam_messages, ham_messages, token_count
		FROM plugin_experimental_spam_bayes_state WHERE user_id = ?`, userID).
		Scan(&schema, &score.SpamMessages, &score.HamMessages, &score.StoredTokens)
	if errors.Is(err, sql.ErrNoRows) {
		return score, nil
	}
	if err != nil {
		return score, err
	}
	if schema != PersonalBayesTokenSchema {
		return score, ErrPersonalBayesSchemaMismatch
	}
	score.Ready = score.SpamMessages >= PersonalBayesMinimumSpamMessages &&
		score.HamMessages >= PersonalBayesMinimumHamMessages
	if !score.Ready {
		return score, nil
	}

	tokens := TokenizePersonalBayesMessage(message)
	counts, err := loadPersonalBayesTokenCounts(ctx, db, userID, tokens)
	if err != nil {
		return score, err
	}
	candidates := make([]personalBayesCandidate, 0, len(counts))
	for _, token := range tokens {
		count, exists := counts[token.Hash]
		if !exists || count.spam+count.ham <= 0 {
			continue
		}
		probability, ok := personalBayesTokenProbability(count.spam, count.ham, score.SpamMessages, score.HamMessages)
		if !ok {
			continue
		}
		candidates = append(candidates, personalBayesCandidate{
			token: token, probability: probability,
			strength: math.Abs(probability - 0.5), spam: count.spam, ham: count.ham,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].strength == candidates[j].strength {
			return bytes.Compare(candidates[i].token.Hash[:], candidates[j].token.Hash[:]) < 0
		}
		return candidates[i].strength > candidates[j].strength
	})
	if len(candidates) > personalBayesMaxSignificant {
		candidates = candidates[:personalBayesMaxSignificant]
	}
	selected := candidates[:0]
	for _, candidate := range candidates {
		if candidate.strength >= personalBayesMinProbStrength {
			selected = append(selected, candidate)
		}
	}
	if len(selected) == 0 {
		return score, nil
	}
	probabilities := make([]float64, len(selected))
	for index, candidate := range selected {
		probabilities[index] = candidate.probability
	}
	score.Probability = combinePersonalBayesProbabilities(score.SpamMessages, score.HamMessages, probabilities)
	score.TokensUsed = len(selected)
	evidenceCount := len(selected)
	if evidenceCount > personalBayesMaxEvidence {
		evidenceCount = personalBayesMaxEvidence
	}
	score.Evidence = make([]PersonalBayesEvidence, 0, evidenceCount)
	for _, candidate := range selected[:evidenceCount] {
		score.Evidence = append(score.Evidence, PersonalBayesEvidence{
			Category: personalBayesEvidenceCategory(candidate.token.Feature),
			TokenID:  hex.EncodeToString(candidate.token.Hash[:6]), Probability: candidate.probability,
			SpamMessages: candidate.spam, HamMessages: candidate.ham,
		})
	}
	return score, nil
}

// GetPersonalBayesCounts returns source-specific unique fingerprint counts and
// the effective counts used by readiness/scoring.
func GetPersonalBayesCounts(ctx context.Context, db *sql.DB, userID int64) (PersonalBayesCounts, error) {
	if db == nil || userID <= 0 {
		return PersonalBayesCounts{}, ErrPersonalBayesMessageNotOwned
	}
	return getPersonalBayesCounts(ctx, db, userID)
}

func getPersonalBayesCounts(ctx context.Context, queryer personalBayesQueryer, userID int64) (PersonalBayesCounts, error) {
	var counts PersonalBayesCounts
	var schema string
	err := queryer.QueryRowContext(ctx, `SELECT token_schema, spam_messages, ham_messages
		FROM plugin_experimental_spam_bayes_state WHERE user_id = ?`, userID).
		Scan(&schema, &counts.Effective.Spam, &counts.Effective.Ham)
	if errors.Is(err, sql.ErrNoRows) {
		return counts, nil
	}
	if err != nil {
		return counts, err
	}
	if schema != PersonalBayesTokenSchema {
		return counts, ErrPersonalBayesSchemaMismatch
	}
	rows, err := queryer.QueryContext(ctx, `SELECT message_fingerprint, source, label, token_schema
		FROM plugin_experimental_spam_bayes_labels
		WHERE user_id = ?
		ORDER BY message_fingerprint,
		         CASE source WHEN 'explicit' THEN 1 ELSE 0 END DESC,
		         updated_at DESC, origin_key DESC`, userID)
	if err != nil {
		return counts, err
	}
	defer rows.Close()
	var previous [sha256.Size]byte
	havePrevious := false
	for rows.Next() {
		var raw []byte
		var source, label, rowSchema string
		if err := rows.Scan(&raw, &source, &label, &rowSchema); err != nil {
			return counts, err
		}
		if len(raw) != sha256.Size {
			return counts, ErrPersonalBayesInconsistent
		}
		if rowSchema != PersonalBayesTokenSchema {
			return counts, ErrPersonalBayesSchemaMismatch
		}
		var fingerprint [sha256.Size]byte
		copy(fingerprint[:], raw)
		if !havePrevious || fingerprint != previous {
			previous = fingerprint
			havePrevious = true
			switch source {
			case PersonalBayesSourceExplicit:
				incrementPersonalBayesLabelCount(&counts.Explicit, label)
			case PersonalBayesSourceAutomatic:
				incrementPersonalBayesLabelCount(&counts.Automatic, label)
			default:
				return counts, ErrPersonalBayesInvalidSource
			}
		}
	}
	if err := rows.Err(); err != nil {
		return counts, err
	}
	counts.Ready = counts.Effective.Spam >= PersonalBayesMinimumSpamMessages &&
		counts.Effective.Ham >= PersonalBayesMinimumHamMessages
	return counts, nil
}

func incrementPersonalBayesLabelCount(counts *PersonalBayesLabelCounts, label string) {
	if label == "spam" {
		counts.Spam++
	} else if label == "ham" {
		counts.Ham++
	}
}

func addPersonalBayesText(add func(string) bool, field, value string) {
	var token []rune
	tokenLength := 0
	flush := func() bool {
		defer func() {
			token = token[:0]
			tokenLength = 0
		}()
		if tokenLength < 3 {
			return true
		}
		value := string(token)
		if tokenLength > personalBayesMaxTokenRunes {
			limit := personalBayesLongTokenRunes
			if len(token) < limit {
				limit = len(token)
			}
			value = "sk:" + string(token[:limit])
		}
		return add(field + ":word:" + value)
	}
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			tokenLength++
			if len(token) < personalBayesMaxTokenRunes {
				token = append(token, r)
			}
			continue
		}
		if !flush() {
			return
		}
	}
	_ = flush()
}

func addPersonalBayesAddress(add func(string) bool, field, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	if parsed, err := mail.ParseAddress(value); err == nil {
		value = parsed.Address
	}
	value = strings.ToLower(strings.TrimSpace(value))
	separator := strings.LastIndexByte(value, '@')
	if separator <= 0 || separator == len(value)-1 {
		return true
	}
	local := strings.Trim(value[:separator], " <>\t\r\n")
	domain := strings.Trim(value[separator+1:], " <>\t\r\n.")
	if local != "" && !add(field+":address:"+local+"@"+domain) {
		return false
	}
	return domain == "" || add(field+":domain:"+domain)
}

func normalizePersonalBayesMIME(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if separator := strings.IndexByte(value, ';'); separator >= 0 {
		value = value[:separator]
	}
	return strings.TrimSpace(value)
}

func boundPersonalBayesFeature(feature string) string {
	feature = strings.TrimSpace(strings.ToLower(feature))
	if feature == "" {
		return ""
	}
	runes := []rune(feature)
	if len(runes) > 96 {
		feature = string(runes[:96])
	}
	return feature
}

func hashPersonalBayesFeature(feature string) [sha256.Size]byte {
	return sha256.Sum256([]byte(PersonalBayesTokenSchema + "\x00" + feature))
}

// PersonalBayesFingerprint returns the durable, schema-stable content identity
// used to deduplicate personal training documents. Raw message content is not
// persisted as part of the fingerprint label.
func PersonalBayesFingerprint(message spammodel.Message) [sha256.Size]byte {
	return fingerprintPersonalBayesMessage(message)
}

func fingerprintPersonalBayesMessage(message spammodel.Message) [sha256.Size]byte {
	hasher := sha256.New()
	writePersonalBayesFingerprintPart(hasher, "rolltop-personal-bayes-message-v1")
	writePersonalBayesFingerprintPart(hasher, message.Subject)
	writePersonalBayesFingerprintPart(hasher, message.Body)
	writePersonalBayesFingerprintPart(hasher, message.From)
	recipients := append([]string(nil), message.To...)
	sort.Strings(recipients)
	for _, recipient := range recipients {
		writePersonalBayesFingerprintPart(hasher, recipient)
	}
	writePersonalBayesFingerprintPart(hasher, normalizePersonalBayesMIME(message.MIMEType))
	attachments := append([]string(nil), message.AttachmentTypes...)
	sort.Strings(attachments)
	for _, attachment := range attachments {
		writePersonalBayesFingerprintPart(hasher, normalizePersonalBayesMIME(attachment))
	}
	if message.HTML {
		writePersonalBayesFingerprintPart(hasher, "html")
	} else {
		writePersonalBayesFingerprintPart(hasher, "plain")
	}
	var result [sha256.Size]byte
	copy(result[:], hasher.Sum(nil))
	return result
}

func writePersonalBayesFingerprintPart(hasher hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = hasher.Write(size[:])
	_, _ = hasher.Write([]byte(value))
}

func truncatePersonalBayesUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func validPersonalBayesLabel(label string) bool {
	return label == "spam" || label == "ham"
}

func validPersonalBayesSource(source string) bool {
	return source == PersonalBayesSourceExplicit || source == PersonalBayesSourceAutomatic
}

func personalBayesMessageOriginKey(messageID int64) string {
	return "message:" + strconv.FormatInt(messageID, 10)
}

func personalBayesFingerprintOriginKey(source string) string {
	if source == PersonalBayesSourceAutomatic {
		return "snapshot"
	}
	return "fingerprint"
}

func personalBayesLabelDeltas(label string, direction int64) (int64, int64) {
	if label == "spam" {
		return direction, 0
	}
	return 0, direction
}

func requirePersonalBayesMessageOwner(ctx context.Context, tx *sql.Tx, userID, messageID int64) error {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM messages WHERE user_id = ? AND id = ?`, userID, messageID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPersonalBayesMessageNotOwned
	}
	return err
}

func ensurePersonalBayesState(ctx context.Context, tx *sql.Tx, userID, now int64) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_experimental_spam_bayes_state
		(user_id, token_schema, spam_messages, ham_messages, token_count, updated_at)
		VALUES (?, ?, 0, 0, 0, ?)
		ON CONFLICT(user_id) DO NOTHING`, userID, PersonalBayesTokenSchema, now); err != nil {
		return err
	}
	return requirePersonalBayesSchema(ctx, tx, userID)
}

func requirePersonalBayesSchema(ctx context.Context, tx *sql.Tx, userID int64) error {
	var schema string
	if err := tx.QueryRowContext(ctx, `SELECT token_schema FROM plugin_experimental_spam_bayes_state
		WHERE user_id = ?`, userID).Scan(&schema); err != nil {
		return err
	}
	if schema != PersonalBayesTokenSchema {
		return ErrPersonalBayesSchemaMismatch
	}
	return nil
}

func personalBayesLearnedByMessageID(ctx context.Context, tx *sql.Tx, userID, messageID int64) (personalBayesLearnedMessage, bool, error) {
	var learned personalBayesLearnedMessage
	var fingerprint []byte
	err := tx.QueryRowContext(ctx, `SELECT message_fingerprint, message_id, label, source, origin_key, token_schema
		FROM plugin_experimental_spam_bayes_labels
		WHERE user_id = ? AND source = 'explicit' AND message_id = ?`, userID, messageID).
		Scan(&fingerprint, &learned.messageID, &learned.label, &learned.source, &learned.originKey, &learned.schema)
	if errors.Is(err, sql.ErrNoRows) {
		return personalBayesLearnedMessage{}, false, nil
	}
	if err != nil {
		return personalBayesLearnedMessage{}, false, err
	}
	if len(fingerprint) != sha256.Size {
		return personalBayesLearnedMessage{}, false, ErrPersonalBayesInconsistent
	}
	copy(learned.fingerprint[:], fingerprint)
	return learned, true, nil
}

func personalBayesCanonicalMessage(ctx context.Context, tx *sql.Tx, userID int64, fingerprint [sha256.Size]byte) (personalBayesCanonical, bool, error) {
	var canonical personalBayesCanonical
	var messageID sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT message_id, label, source, origin_key
		FROM plugin_experimental_spam_bayes_labels
		WHERE user_id = ? AND message_fingerprint = ?
		ORDER BY CASE source WHEN 'explicit' THEN 1 ELSE 0 END DESC,
		         updated_at DESC, origin_key DESC LIMIT 1`, userID, fingerprint[:]).
		Scan(&messageID, &canonical.label, &canonical.source, &canonical.originKey)
	if errors.Is(err, sql.ErrNoRows) {
		return personalBayesCanonical{}, false, nil
	}
	if messageID.Valid {
		canonical.messageID = messageID.Int64
	}
	return canonical, err == nil, err
}

func learnPersonalBayesFingerprintTx(ctx context.Context, tx *sql.Tx, userID int64, source, originKey, label string, fingerprint [sha256.Size]byte, message spammodel.Message, now int64, expire bool) (PersonalBayesLearnResult, error) {
	var previousLabel, schema string
	err := tx.QueryRowContext(ctx, `SELECT label, token_schema
		FROM plugin_experimental_spam_bayes_labels
		WHERE user_id = ? AND message_fingerprint = ? AND source = ? AND origin_key = ?`,
		userID, fingerprint[:], source, originKey).Scan(&previousLabel, &schema)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return PersonalBayesLearnResult{}, err
	}
	if found && schema != PersonalBayesTokenSchema {
		return PersonalBayesLearnResult{}, ErrPersonalBayesSchemaMismatch
	}
	tokens, err := ensurePersonalBayesDocument(ctx, tx, userID, fingerprint, TokenizePersonalBayesMessage(message), now)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	before, beforeFound, err := personalBayesCanonicalMessage(ctx, tx, userID, fingerprint)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if found && previousLabel == label {
		result, resultErr := personalBayesLearnResult(ctx, tx, userID)
		if resultErr != nil {
			return PersonalBayesLearnResult{}, resultErr
		}
		result.Label = label
		return result, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_experimental_spam_bayes_labels
		(user_id, message_fingerprint, source, origin_key, message_id, label, token_schema, learned_at, updated_at)
		VALUES (?, ?, ?, ?, NULL, ?, ?, ?, ?)
		ON CONFLICT(user_id, message_fingerprint, source, origin_key) DO UPDATE SET
			label = excluded.label, token_schema = excluded.token_schema, updated_at = excluded.updated_at`,
		userID, fingerprint[:], source, originKey, label, PersonalBayesTokenSchema, now, now); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	after, afterFound, err := personalBayesCanonicalMessage(ctx, tx, userID, fingerprint)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if err := changePersonalBayesCanonical(ctx, tx, userID, tokens, before.label, beforeFound, after.label, afterFound, now); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if expire {
		if err := expirePersonalBayesLearns(ctx, tx, userID, personalBayesMaxLearnedMessages, now); err != nil {
			return PersonalBayesLearnResult{}, err
		}
	}
	if err := topUpPersonalBayesDocument(ctx, tx, userID, fingerprint, tokens, now); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if err := refreshPersonalBayesTokenCount(ctx, tx, userID, now); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	result, err := personalBayesLearnResult(ctx, tx, userID)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	result.Changed = true
	result.PreviousLabel = previousLabel
	result.Label = label
	return result, nil
}

func clearPersonalBayesLabelTx(ctx context.Context, tx *sql.Tx, userID int64, source, originKey string, fingerprint [sha256.Size]byte, expectedLabel string, now int64) (PersonalBayesLearnResult, error) {
	var storedLabel, schema string
	err := tx.QueryRowContext(ctx, `SELECT label, token_schema
		FROM plugin_experimental_spam_bayes_labels
		WHERE user_id = ? AND message_fingerprint = ? AND source = ? AND origin_key = ?`,
		userID, fingerprint[:], source, originKey).Scan(&storedLabel, &schema)
	if errors.Is(err, sql.ErrNoRows) {
		result, resultErr := personalBayesLearnResult(ctx, tx, userID)
		if errors.Is(resultErr, sql.ErrNoRows) {
			return PersonalBayesLearnResult{}, nil
		}
		return result, resultErr
	}
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if schema != PersonalBayesTokenSchema {
		return PersonalBayesLearnResult{}, ErrPersonalBayesSchemaMismatch
	}
	if expectedLabel != "" && storedLabel != expectedLabel {
		return PersonalBayesLearnResult{}, ErrPersonalBayesLabelMismatch
	}
	tokens, err := loadPersonalBayesDocumentTokens(ctx, tx, userID, fingerprint)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	before, beforeFound, err := personalBayesCanonicalMessage(ctx, tx, userID, fingerprint)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_experimental_spam_bayes_labels
		WHERE user_id = ? AND message_fingerprint = ? AND source = ? AND origin_key = ?`,
		userID, fingerprint[:], source, originKey); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if source == PersonalBayesSourceAutomatic {
		if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_experimental_spam_bayes_learns
			WHERE user_id = ? AND message_fingerprint = ? AND source = 'automatic'`, userID, fingerprint[:]); err != nil {
			return PersonalBayesLearnResult{}, err
		}
	}
	after, afterFound, err := personalBayesCanonicalMessage(ctx, tx, userID, fingerprint)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if err := changePersonalBayesCanonical(ctx, tx, userID, tokens, before.label, beforeFound, after.label, afterFound, now); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	if !afterFound {
		if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_experimental_spam_bayes_documents
			WHERE user_id = ? AND message_fingerprint = ?`, userID, fingerprint[:]); err != nil {
			return PersonalBayesLearnResult{}, err
		}
	}
	if err := refreshPersonalBayesTokenCount(ctx, tx, userID, now); err != nil {
		return PersonalBayesLearnResult{}, err
	}
	result, err := personalBayesLearnResult(ctx, tx, userID)
	if err != nil {
		return PersonalBayesLearnResult{}, err
	}
	result.Changed = true
	result.PreviousLabel = storedLabel
	if afterFound {
		result.Label = after.label
	}
	return result, nil
}

func automaticPersonalBayesLabels(ctx context.Context, tx *sql.Tx, userID int64) ([]personalBayesLearnedMessage, error) {
	rows, err := tx.QueryContext(ctx, `SELECT message_fingerprint, label, origin_key, token_schema
		FROM plugin_experimental_spam_bayes_labels
		WHERE user_id = ? AND source = 'automatic'
		ORDER BY message_fingerprint, origin_key`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var labels []personalBayesLearnedMessage
	for rows.Next() {
		var label personalBayesLearnedMessage
		var raw []byte
		if err := rows.Scan(&raw, &label.label, &label.originKey, &label.schema); err != nil {
			return nil, err
		}
		if len(raw) != sha256.Size {
			return nil, ErrPersonalBayesInconsistent
		}
		copy(label.fingerprint[:], raw)
		label.source = PersonalBayesSourceAutomatic
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

func equalPersonalBayesSnapshot(existing []personalBayesLearnedMessage, prepared map[[sha256.Size]byte]PersonalBayesTrainingDocument) bool {
	if len(existing) != len(prepared) {
		return false
	}
	for _, label := range existing {
		if label.originKey != personalBayesFingerprintOriginKey(PersonalBayesSourceAutomatic) || label.schema != PersonalBayesTokenSchema {
			return false
		}
		document, found := prepared[label.fingerprint]
		if !found || document.Label != label.label {
			return false
		}
	}
	return true
}

func personalBayesAcceptedCounts(prepared map[[sha256.Size]byte]PersonalBayesTrainingDocument) PersonalBayesLabelCounts {
	var counts PersonalBayesLabelCounts
	for _, document := range prepared {
		incrementPersonalBayesLabelCount(&counts, document.Label)
	}
	return counts
}

func ensurePersonalBayesDocument(ctx context.Context, tx *sql.Tx, userID int64, fingerprint [sha256.Size]byte, tokens []PersonalBayesToken, now int64) ([]PersonalBayesToken, error) {
	var schema string
	err := tx.QueryRowContext(ctx, `SELECT token_schema
		FROM plugin_experimental_spam_bayes_documents
		WHERE user_id = ? AND message_fingerprint = ?`, userID, fingerprint[:]).Scan(&schema)
	if err == nil {
		if schema != PersonalBayesTokenSchema {
			return nil, ErrPersonalBayesSchemaMismatch
		}
		return loadPersonalBayesDocumentTokens(ctx, tx, userID, fingerprint)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	if len(tokens) > personalBayesMaxLearnedTokensPerMessage {
		tokens = tokens[:personalBayesMaxLearnedTokensPerMessage]
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_experimental_spam_bayes_documents
		(user_id, message_fingerprint, token_schema, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`, userID, fingerprint[:], PersonalBayesTokenSchema, now, now); err != nil {
		return nil, err
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO plugin_experimental_spam_bayes_learn_tokens
		(user_id, message_fingerprint, token_hash) VALUES (?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer statement.Close()
	for _, token := range tokens {
		if _, err := statement.ExecContext(ctx, userID, fingerprint[:], token.Hash[:]); err != nil {
			return nil, err
		}
	}
	return tokens, nil
}

func loadPersonalBayesDocumentTokens(ctx context.Context, tx *sql.Tx, userID int64, fingerprint [sha256.Size]byte) ([]PersonalBayesToken, error) {
	rows, err := tx.QueryContext(ctx, `SELECT token_hash
		FROM plugin_experimental_spam_bayes_learn_tokens
		WHERE user_id = ? AND message_fingerprint = ?
		ORDER BY token_hash`, userID, fingerprint[:])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []PersonalBayesToken
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if len(raw) != sha256.Size {
			return nil, ErrPersonalBayesInconsistent
		}
		var token PersonalBayesToken
		copy(token.Hash[:], raw)
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func changePersonalBayesCanonical(ctx context.Context, tx *sql.Tx, userID int64, tokens []PersonalBayesToken, beforeLabel string, beforeFound bool, afterLabel string, afterFound bool, now int64) error {
	if beforeFound == afterFound && (!beforeFound || beforeLabel == afterLabel) {
		return nil
	}
	if !beforeFound && afterFound {
		if _, err := admitPersonalBayesTokens(ctx, tx, userID, tokens, now, personalBayesMaxStoredTokens); err != nil {
			return err
		}
	}
	spamDelta, hamDelta := int64(0), int64(0)
	if beforeFound {
		spam, ham := personalBayesLabelDeltas(beforeLabel, -1)
		spamDelta += spam
		hamDelta += ham
	}
	if afterFound {
		spam, ham := personalBayesLabelDeltas(afterLabel, 1)
		spamDelta += spam
		hamDelta += ham
	}
	if err := changePersonalBayesTokens(ctx, tx, userID, tokens, spamDelta, hamDelta, now); err != nil {
		return err
	}
	if err := changePersonalBayesTotals(ctx, tx, userID, spamDelta, hamDelta, now); err != nil {
		return err
	}
	if beforeFound && !afterFound {
		return reclaimPersonalBayesTokens(ctx, tx, userID, now)
	}
	return nil
}

func admitPersonalBayesTokens(ctx context.Context, tx *sql.Tx, userID int64, tokens []PersonalBayesToken, now int64, maxStored int) ([]PersonalBayesToken, error) {
	if len(tokens) == 0 || maxStored <= 0 {
		return nil, nil
	}
	var stored int
	if err := tx.QueryRowContext(ctx, `SELECT token_count
		FROM plugin_experimental_spam_bayes_state WHERE user_id = ?`, userID).Scan(&stored); err != nil {
		return nil, err
	}
	available := maxStored - stored
	if available <= 0 {
		return nil, nil
	}
	existing, err := loadPersonalBayesTokenCounts(ctx, tx, userID, tokens)
	if err != nil {
		return nil, err
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO plugin_experimental_spam_bayes_tokens
		(user_id, token_hash, spam_messages, ham_messages, last_seen)
		VALUES (?, ?, 0, 0, ?)
		ON CONFLICT(user_id, token_hash) DO NOTHING`)
	if err != nil {
		return nil, err
	}
	defer statement.Close()
	inserted := int64(0)
	admitted := make([]PersonalBayesToken, 0, available)
	for _, token := range tokens {
		if available <= 0 {
			break
		}
		if _, found := existing[token.Hash]; found {
			continue
		}
		result, err := statement.ExecContext(ctx, userID, token.Hash[:], now)
		if err != nil {
			return nil, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if rows > 0 {
			inserted += rows
			available -= int(rows)
			admitted = append(admitted, token)
		}
	}
	if inserted == 0 {
		return nil, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE plugin_experimental_spam_bayes_state
		SET token_count = token_count + ?, updated_at = ?
		WHERE user_id = ? AND token_count + ? <= ?`, inserted, now, userID, inserted, maxStored)
	if err != nil {
		return nil, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, ErrPersonalBayesInconsistent
	}
	return admitted, nil
}

func reclaimPersonalBayesTokens(ctx context.Context, tx *sql.Tx, userID, now int64) error {
	result, err := tx.ExecContext(ctx, `DELETE FROM plugin_experimental_spam_bayes_tokens
		WHERE user_id = ? AND spam_messages = 0 AND ham_messages = 0`, userID)
	if err != nil {
		return err
	}
	removed, err := result.RowsAffected()
	if err != nil || removed == 0 {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE plugin_experimental_spam_bayes_state
		SET token_count = token_count - ?, updated_at = ?
		WHERE user_id = ? AND token_count >= ?`, removed, now, userID, removed)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrPersonalBayesInconsistent
	}
	return nil
}

func topUpPersonalBayesDocument(ctx context.Context, tx *sql.Tx, userID int64, fingerprint [sha256.Size]byte, tokens []PersonalBayesToken, now int64) error {
	canonical, found, err := personalBayesCanonicalMessage(ctx, tx, userID, fingerprint)
	if err != nil || !found {
		return err
	}
	admitted, err := admitPersonalBayesTokens(ctx, tx, userID, tokens, now, personalBayesMaxStoredTokens)
	if err != nil || len(admitted) == 0 {
		return err
	}
	spamDelta, hamDelta := personalBayesLabelDeltas(canonical.label, 1)
	return changePersonalBayesTokens(ctx, tx, userID, admitted, spamDelta, hamDelta, now)
}

func expirePersonalBayesLearns(ctx context.Context, tx *sql.Tx, userID int64, keep int, now int64) error {
	if keep < 0 {
		keep = 0
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM plugin_experimental_spam_bayes_labels WHERE user_id = ?`, userID).Scan(&count); err != nil {
		return err
	}
	excess := count - keep
	if excess <= 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT message_fingerprint, source, origin_key, message_id
		FROM plugin_experimental_spam_bayes_labels
		WHERE user_id = ?
		ORDER BY CASE source WHEN 'automatic' THEN 0 ELSE 1 END,
		         updated_at ASC, origin_key ASC LIMIT ?`, userID, excess)
	if err != nil {
		return err
	}
	labels := make([]personalBayesLearnedMessage, 0, excess)
	for rows.Next() {
		var label personalBayesLearnedMessage
		var raw []byte
		var messageID sql.NullInt64
		if err := rows.Scan(&raw, &label.source, &label.originKey, &messageID); err != nil {
			rows.Close()
			return err
		}
		if len(raw) != sha256.Size {
			rows.Close()
			return ErrPersonalBayesInconsistent
		}
		copy(label.fingerprint[:], raw)
		if messageID.Valid {
			label.messageID = messageID.Int64
		}
		labels = append(labels, label)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, label := range labels {
		if err := expirePersonalBayesLabel(ctx, tx, userID, label, now); err != nil {
			return err
		}
	}
	return nil
}

func expirePersonalBayesLearn(ctx context.Context, tx *sql.Tx, userID, messageID, now int64) error {
	learned, found, err := personalBayesLearnedByMessageID(ctx, tx, userID, messageID)
	if err != nil || !found {
		return err
	}
	return expirePersonalBayesLabel(ctx, tx, userID, learned, now)
}

func expirePersonalBayesLabel(ctx context.Context, tx *sql.Tx, userID int64, learned personalBayesLearnedMessage, now int64) error {
	tokens, err := loadPersonalBayesDocumentTokens(ctx, tx, userID, learned.fingerprint)
	if err != nil {
		return err
	}
	before, beforeFound, err := personalBayesCanonicalMessage(ctx, tx, userID, learned.fingerprint)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_experimental_spam_bayes_labels
		WHERE user_id = ? AND message_fingerprint = ? AND source = ? AND origin_key = ?`,
		userID, learned.fingerprint[:], learned.source, learned.originKey); err != nil {
		return err
	}
	if learned.messageID > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_experimental_spam_bayes_learns
			WHERE user_id = ? AND message_id = ?`, userID, learned.messageID); err != nil {
			return err
		}
	}
	after, afterFound, err := personalBayesCanonicalMessage(ctx, tx, userID, learned.fingerprint)
	if err != nil {
		return err
	}
	if err := changePersonalBayesCanonical(ctx, tx, userID, tokens, before.label, beforeFound, after.label, afterFound, now); err != nil {
		return err
	}
	if !afterFound {
		_, err = tx.ExecContext(ctx, `DELETE FROM plugin_experimental_spam_bayes_documents
			WHERE user_id = ? AND message_fingerprint = ?`, userID, learned.fingerprint[:])
	}
	return err
}

func personalBayesEvidenceCategory(feature string) string {
	parts := strings.SplitN(feature, ":", 3)
	if len(parts) == 1 {
		return "other"
	}
	if len(parts) >= 3 && (parts[1] == "word" || parts[1] == "address" || parts[1] == "domain") {
		return parts[0] + "-" + parts[1]
	}
	return parts[0]
}

func changePersonalBayesTokens(ctx context.Context, tx *sql.Tx, userID int64, tokens []PersonalBayesToken, spamDelta, hamDelta, atime int64) error {
	if spamDelta == 0 && hamDelta == 0 {
		return nil
	}
	update, err := tx.PrepareContext(ctx, `UPDATE plugin_experimental_spam_bayes_tokens
		SET spam_messages = spam_messages + ?,
		    ham_messages = ham_messages + ?,
		    last_seen = MAX(last_seen, ?)
		WHERE user_id = ? AND token_hash = ?`)
	if err != nil {
		return err
	}
	defer update.Close()
	for _, token := range tokens {
		if _, err := update.ExecContext(ctx, spamDelta, hamDelta, atime, userID, token.Hash[:]); err != nil {
			return err
		}
	}
	return nil
}

func changePersonalBayesTotals(ctx context.Context, tx *sql.Tx, userID, spamDelta, hamDelta, now int64) error {
	result, err := tx.ExecContext(ctx, `UPDATE plugin_experimental_spam_bayes_state
		SET spam_messages = spam_messages + ?, ham_messages = ham_messages + ?, updated_at = ?
		WHERE user_id = ? AND spam_messages + ? >= 0 AND ham_messages + ? >= 0`,
		spamDelta, hamDelta, now, userID, spamDelta, hamDelta)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrPersonalBayesInconsistent
	}
	return nil
}

func refreshPersonalBayesTokenCount(ctx context.Context, tx *sql.Tx, userID, now int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE plugin_experimental_spam_bayes_state
		SET updated_at = ? WHERE user_id = ?`, now, userID)
	return err
}

func personalBayesLearnResult(ctx context.Context, tx *sql.Tx, userID int64) (PersonalBayesLearnResult, error) {
	var result PersonalBayesLearnResult
	err := tx.QueryRowContext(ctx, `SELECT spam_messages, ham_messages, token_count
		FROM plugin_experimental_spam_bayes_state WHERE user_id = ?`, userID).
		Scan(&result.SpamMessages, &result.HamMessages, &result.StoredTokens)
	return result, err
}

func loadPersonalBayesTokenCounts(ctx context.Context, queryer personalBayesRowsQueryer, userID int64, tokens []PersonalBayesToken) (map[[sha256.Size]byte]personalBayesTokenCounts, error) {
	counts := make(map[[sha256.Size]byte]personalBayesTokenCounts, len(tokens))
	for start := 0; start < len(tokens); start += personalBayesTokenQueryBatch {
		end := start + personalBayesTokenQueryBatch
		if end > len(tokens) {
			end = len(tokens)
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", end-start), ",")
		query := `SELECT token_hash, spam_messages, ham_messages
			FROM plugin_experimental_spam_bayes_tokens
			WHERE user_id = ? AND token_hash IN (` + placeholders + `)`
		arguments := make([]any, 0, end-start+1)
		arguments = append(arguments, userID)
		for _, token := range tokens[start:end] {
			arguments = append(arguments, token.Hash[:])
		}
		rows, err := queryer.QueryContext(ctx, query, arguments...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var raw []byte
			var count personalBayesTokenCounts
			if err := rows.Scan(&raw, &count.spam, &count.ham); err != nil {
				rows.Close()
				return nil, err
			}
			if len(raw) != sha256.Size {
				rows.Close()
				return nil, ErrPersonalBayesInconsistent
			}
			var digest [sha256.Size]byte
			copy(digest[:], raw)
			counts[digest] = count
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return counts, nil
}

func personalBayesTokenProbability(spamCount, hamCount, spamMessages, hamMessages int64) (float64, bool) {
	if spamMessages <= 0 || hamMessages <= 0 || spamCount+hamCount <= 0 {
		return 0, false
	}
	spam := float64(spamCount)
	ham := float64(hamCount)
	totalSpam := float64(spamMessages)
	totalHam := float64(hamMessages)
	denominator := ham*totalSpam + spam*totalHam
	if denominator <= 0 {
		return 0, false
	}
	raw := (spam * totalHam) / denominator
	observations := spam + ham
	probability := (personalBayesRobinsonStrength*personalBayesRobinsonX + observations*raw) /
		(personalBayesRobinsonStrength + observations)
	return clampPersonalBayesUnit(probability), true
}

func combinePersonalBayesProbabilities(spamMessages, hamMessages int64, probabilities []float64) float64 {
	if spamMessages <= 0 || hamMessages <= 0 || len(probabilities) == 0 {
		return personalBayesDefaultProbability
	}
	total := float64(spamMessages + hamMessages)
	logSpamProduct := math.Log(float64(spamMessages) / total)
	logHamProduct := math.Log(float64(hamMessages) / total)
	for _, probability := range probabilities {
		probability = math.Max(1e-15, math.Min(1-1e-15, probability))
		logSpamProduct += math.Log1p(-probability)
		logHamProduct += math.Log(probability)
	}
	halfDegrees := len(probabilities)
	spamEvidence := 1 - personalBayesChi2Q(-2*logSpamProduct, halfDegrees)
	hamEvidence := 1 - personalBayesChi2Q(-2*logHamProduct, halfDegrees)
	return clampPersonalBayesUnit((spamEvidence - hamEvidence + 1) / 2)
}

func personalBayesChi2Q(value float64, halfDegrees int) float64 {
	if value < 0 || halfDegrees <= 0 || math.IsNaN(value) {
		return 1
	}
	m := value / 2
	term := math.Exp(-m)
	sum := term
	for index := 1; index < halfDegrees; index++ {
		term *= m / float64(index)
		sum += term
	}
	if math.IsNaN(sum) {
		return 0
	}
	return clampPersonalBayesUnit(sum)
}

func clampPersonalBayesUnit(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
