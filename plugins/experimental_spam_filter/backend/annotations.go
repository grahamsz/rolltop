package main

import (
	"context"
	"strings"

	"rolltop/backend/plugins"
)

func (p *spamFilterPlugin) MessageAnnotations(ctx context.Context, host plugins.BackendHost, userID int64, messageIDs []int64) (map[int64][]plugins.MessageAnnotation, error) {
	_, db, err := pluginUserDB(ctx, host, userID)
	if err != nil {
		return nil, err
	}
	records, err := listClassifications(ctx, db, userID, messageIDs)
	if err != nil {
		return nil, err
	}
	feedback, err := listFeedback(ctx, db, userID, messageIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]plugins.MessageAnnotation, len(records)+len(feedback))
	_, currentVersion, _ := p.model()
	for messageID, label := range feedback {
		band := bandLow
		summary := "You marked this message as not spam."
		if label == feedbackSpam {
			band = bandHigh
			summary = "You marked this message as spam."
		}
		out[messageID] = []plugins.MessageAnnotation{{
			PluginID: pluginID,
			Kind:     "spam-risk",
			Label:    strings.ToUpper(band[:1]) + band[1:] + " spam risk",
			Level:    band,
			Summary:  summary,
			Metadata: map[string]string{"feedback": label},
		}}
	}
	for messageID, record := range records {
		if record.Feedback != "" {
			continue
		}
		// Stale scores are withheld until the bounded backfill recomputes them;
		// explicit feedback remains in its own table and survives the upgrade.
		if currentVersion == "" || record.ModelVersion != currentVersion {
			continue
		}
		band := displayedBand(record)
		annotation := plugins.MessageAnnotation{
			PluginID: pluginID,
			Kind:     "spam-risk",
			Label:    strings.ToUpper(band[:1]) + band[1:] + " spam risk",
			Level:    band,
			Summary:  annotationSummary(record),
			Metadata: map[string]string{
				"model_version":    record.ModelVersion,
				"content_coverage": record.ContentCoverage,
				"feedback":         record.Feedback,
			},
		}
		out[messageID] = []plugins.MessageAnnotation{annotation}
	}
	return out, nil
}

func annotationSummary(record classificationRecord) string {
	switch record.Feedback {
	case feedbackSpam:
		return "You marked this message as spam."
	case feedbackHam:
		return "You marked this message as not spam."
	default:
		parts := []string{strings.ToUpper(record.RiskBand[:1]) + record.RiskBand[1:] + " risk from the checked-in named-rule scorecard"}
		if record.LabeledNeighborCount > 0 {
			parts = append(parts, "explicitly labeled similar mail")
		}
		if record.RecentReadSupport > 0 {
			if record.Explanation.ExactSenderTemplateSupport > 0 {
				parts = append(parts, "recent exact-sender template evidence")
			} else {
				parts = append(parts, "generic recent-read evidence")
			}
		}
		return strings.Join(parts, ", ") + "."
	}
}
