package main

import (
	"time"

	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

const (
	pluginID = "experimental_spam_filter"
	apiPath  = "plugins/experimental_spam_filter"

	feedbackSpam = "spam"
	feedbackHam  = "ham"

	bandLow    = "low"
	bandMedium = "medium"
	bandHigh   = "high"

	lowRiskBoundary  = spammodel.DefaultMediumThreshold
	highRiskBoundary = spammodel.DefaultHighThreshold
)

type signalEvidence struct {
	Feature      string  `json:"feature"`
	Description  string  `json:"description,omitempty"`
	Contribution float64 `json:"contribution"`
}

type neighborEvidence struct {
	MessageID        int64    `json:"message_id"`
	Label            string   `json:"label,omitempty"`
	Score            float64  `json:"score"`
	WeightedCoverage float64  `json:"weighted_coverage"`
	Date             int64    `json:"date,omitempty"`
	From             string   `json:"from,omitempty"`
	MatchedTerms     []string `json:"matched_terms,omitempty"`
}

type classificationExplanation struct {
	PositiveSignals             []signalEvidence          `json:"positive_signals,omitempty"`
	NegativeSignals             []signalEvidence          `json:"negative_signals,omitempty"`
	LabeledNeighbors            []neighborEvidence        `json:"labeled_neighbors,omitempty"`
	RecentReadNeighbors         []neighborEvidence        `json:"recent_read_neighbors,omitempty"`
	LabeledLogOddsAdjustment    float64                   `json:"labeled_log_odds_adjustment,omitempty"`
	GenericReadSupport          float64                   `json:"generic_read_support,omitempty"`
	ExactSenderTemplateSupport  float64                   `json:"exact_sender_template_support,omitempty"`
	ReputationLogOddsAdjustment float64                   `json:"reputation_log_odds_adjustment,omitempty"`
	PersonalBayes               *personalBayesExplanation `json:"personal_bayes,omitempty"`
}

type personalBayesExplanation struct {
	Ready             bool    `json:"ready"`
	Probability       float64 `json:"probability"`
	SpamMessages      int64   `json:"spam_messages"`
	HamMessages       int64   `json:"ham_messages"`
	StoredTokens      int64   `json:"stored_tokens"`
	TokensUsed        int     `json:"tokens_used"`
	Bucket            string  `json:"bucket,omitempty"`
	LogOddsAdjustment float64 `json:"log_odds_adjustment,omitempty"`
}

type classificationRecord struct {
	MessageID                  int64                     `json:"message_id"`
	ModelVersion               string                    `json:"model_version"`
	ModelName                  string                    `json:"model_name,omitempty"`
	TrainingCorpus             string                    `json:"training_corpus,omitempty"`
	BaseProbability            float64                   `json:"base_probability"`
	LabeledNeighborProbability float64                   `json:"labeled_neighbor_probability"`
	LabeledNeighborCount       int                       `json:"labeled_neighbor_count"`
	RecentReadSupport          float64                   `json:"recent_read_support"`
	FinalProbability           float64                   `json:"final_probability"`
	RiskBand                   string                    `json:"risk_band"`
	DisplayBand                string                    `json:"display_band"`
	ContentCoverage            string                    `json:"content_coverage"`
	Explanation                classificationExplanation `json:"explanation"`
	Feedback                   string                    `json:"feedback,omitempty"`
	Stale                      bool                      `json:"stale"`
	ClassifiedAt               int64                     `json:"classified_at"`
	UpdatedAt                  int64                     `json:"updated_at"`
}

type backfillRecord struct {
	ModelVersion  string `json:"model_version"`
	Status        string `json:"status"`
	Requested     int    `json:"requested"`
	Processed     int    `json:"processed"`
	Failed        int    `json:"failed"`
	LastMessageID int64  `json:"last_message_id"`
	LastError     string `json:"last_error,omitempty"`
	StartedAt     int64  `json:"started_at"`
	UpdatedAt     int64  `json:"updated_at"`
	CompletedAt   int64  `json:"completed_at"`
}

type bootstrapRecord struct {
	Status         string `json:"status"`
	CutoffAt       int64  `json:"cutoff_at"`
	CandidateSpam  int    `json:"candidate_spam"`
	CandidateHam   int    `json:"candidate_ham"`
	Examined       int    `json:"examined"`
	AcceptedSpam   int    `json:"accepted_spam"`
	AcceptedHam    int    `json:"accepted_ham"`
	Rejected       int    `json:"rejected"`
	CurrentMailbox string `json:"current_mailbox,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	StartedAt      int64  `json:"started_at"`
	UpdatedAt      int64  `json:"updated_at"`
	CompletedAt    int64  `json:"completed_at"`
}

type bootstrapMailboxSelection struct {
	AccountID      int64 `json:"account_id"`
	InboxMailboxID int64 `json:"inbox_mailbox_id"`
	JunkMailboxID  int64 `json:"junk_mailbox_id"`
}

type bootstrapPreviewMailbox struct {
	AccountID      int64  `json:"account_id"`
	AccountLabel   string `json:"account_label"`
	InboxMailboxID int64  `json:"inbox_mailbox_id"`
	InboxName      string `json:"inbox_name"`
	JunkMailboxID  int64  `json:"junk_mailbox_id"`
	JunkName       string `json:"junk_name"`
	SpamCandidates int    `json:"spam_candidates"`
	HamCandidates  int    `json:"ham_candidates"`
}

type bootstrapPreview struct {
	CutoffAt int64                     `json:"cutoff_at"`
	Accounts []bootstrapPreviewMailbox `json:"accounts"`
	Spam     int                       `json:"spam_candidates"`
	Ham      int                       `json:"ham_candidates"`
}

type pluginStatus struct {
	ModelAvailable     bool            `json:"model_available"`
	ModelVersion       string          `json:"model_version"`
	ModelName          string          `json:"model_name,omitempty"`
	TrainingCorpus     string          `json:"training_corpus,omitempty"`
	ModelError         string          `json:"model_error,omitempty"`
	Classified         int             `json:"classified"`
	LowRisk            int             `json:"low_risk"`
	MediumRisk         int             `json:"medium_risk"`
	HighRisk           int             `json:"high_risk"`
	Stale              int             `json:"stale"`
	SpamFeedback       int             `json:"spam_feedback"`
	HamFeedback        int             `json:"ham_feedback"`
	BayesReady         bool            `json:"bayes_ready"`
	BayesSpamLearned   int64           `json:"bayes_spam_learned"`
	BayesHamLearned    int64           `json:"bayes_ham_learned"`
	BayesExplicitSpam  int64           `json:"bayes_explicit_spam"`
	BayesExplicitHam   int64           `json:"bayes_explicit_ham"`
	BayesAutomaticSpam int64           `json:"bayes_automatic_spam"`
	BayesAutomaticHam  int64           `json:"bayes_automatic_ham"`
	Backfill           backfillRecord  `json:"backfill"`
	Bootstrap          bootstrapRecord `json:"bootstrap"`
}

type classificationResult struct {
	Record classificationRecord
}

func riskBand(probability float64) string {
	probability = clampProbability(probability)
	switch {
	case probability >= highRiskBoundary:
		return bandHigh
	case probability >= lowRiskBoundary:
		return bandMedium
	default:
		return bandLow
	}
}

func displayedBand(record classificationRecord) string {
	switch record.Feedback {
	case feedbackSpam:
		return bandHigh
	case feedbackHam:
		return bandLow
	default:
		return record.RiskBand
	}
}

func clampProbability(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func unixOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().Unix()
}
