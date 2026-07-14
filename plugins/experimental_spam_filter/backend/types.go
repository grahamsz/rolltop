package main

import "time"

const (
	pluginID = "experimental_spam_filter"
	apiPath  = "plugins/experimental_spam_filter"

	feedbackSpam = "spam"
	feedbackHam  = "ham"

	bandLow    = "low"
	bandMedium = "medium"
	bandHigh   = "high"

	lowRiskBoundary  = 0.35
	highRiskBoundary = 0.80
)

type signalEvidence struct {
	Feature      string  `json:"feature"`
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
	PositiveSignals     []signalEvidence   `json:"positive_signals,omitempty"`
	NegativeSignals     []signalEvidence   `json:"negative_signals,omitempty"`
	LabeledNeighbors    []neighborEvidence `json:"labeled_neighbors,omitempty"`
	RecentReadNeighbors []neighborEvidence `json:"recent_read_neighbors,omitempty"`
}

type classificationRecord struct {
	MessageID                  int64                     `json:"message_id"`
	ModelVersion               string                    `json:"model_version"`
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

type pluginStatus struct {
	ModelAvailable bool           `json:"model_available"`
	ModelVersion   string         `json:"model_version"`
	ModelError     string         `json:"model_error,omitempty"`
	Classified     int            `json:"classified"`
	LowRisk        int            `json:"low_risk"`
	MediumRisk     int            `json:"medium_risk"`
	HighRisk       int            `json:"high_risk"`
	Stale          int            `json:"stale"`
	SpamFeedback   int            `json:"spam_feedback"`
	HamFeedback    int            `json:"ham_feedback"`
	Backfill       backfillRecord `json:"backfill"`
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
