package main

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/mail"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

const (
	maxBodyBytes          = 64 * 1024
	maxStoredSignals      = 6
	maxStoredMatchedTerms = 8
	recentReadWindow      = 90 * 24 * time.Hour
)

func (p *spamFilterPlugin) ClassifyMessage(ctx context.Context, host plugins.MessageClassificationHost, input plugins.MessageClassificationInput) error {
	if input.UserID <= 0 || input.MessageID <= 0 {
		return errors.New("classification requires a tenant-owned message")
	}
	st, db, err := pluginUserDB(ctx, host, input.UserID)
	if err != nil {
		return err
	}
	// Re-resolve ownership rather than trusting an ID supplied by a hook caller.
	if _, err := st.GetMessageEnvelopeForUser(ctx, input.UserID, input.MessageID); err != nil {
		return err
	}
	_, err = p.classifyAndSave(ctx, host, db, input, contentCoverage(input))
	return err
}

func contentCoverage(input plugins.MessageClassificationInput) string {
	if input.IsEncrypted {
		return "encrypted_metadata"
	}
	if strings.TrimSpace(input.BodyText) == "" {
		return "metadata"
	}
	if input.BodyTruncated || len(input.BodyText) > maxBodyBytes {
		return "preview"
	}
	return "full"
}

func (p *spamFilterPlugin) classifyAndSave(ctx context.Context, host plugins.MessageClassificationHost, db *sql.DB, input plugins.MessageClassificationInput, coverage string) (classificationRecord, error) {
	classifier, _, _ := p.model()
	if classifier == nil {
		return classificationRecord{}, plugins.ErrUnsupported
	}

	message := modelMessage(input)
	score, err := classifier.Classify(message)
	if err != nil {
		return classificationRecord{}, err
	}
	base := clampProbability(score.Probability)
	explanation := contributionEvidence(score.Contributions)
	terms := similarityTerms(input)

	labeledProbability := 0.5
	labeledCount := 0
	personalized := base
	spamIDs, hamIDs, labels, err := labeledCandidates(ctx, db, input.UserID)
	if err != nil {
		return classificationRecord{}, err
	}
	if len(labels) > 0 && len(terms) > 0 {
		candidateIDs := append(append(make([]int64, 0, len(spamIDs)+len(hamIDs)), spamIDs...), hamIDs...)
		hits, similarityErr := host.SimilarMessages(ctx, input.UserID, plugins.SimilarMessagesRequest{
			CandidateMessageIDs: candidateIDs,
			CurrentMessageID:    input.MessageID,
			Terms:               terms,
			Limit:               plugins.MaxSimilarityResults,
		})
		if similarityErr == nil {
			labeledProbability, labeledCount, explanation.LabeledNeighbors = labeledNeighborScore(hits, labels)
			if labeledCount >= 3 {
				lambda := math.Min(0.4, 0.1*float64(labeledCount))
				personalized = base*(1-lambda) + labeledProbability*lambda
			}
		}
	}

	recentSupport := 0.0
	if len(terms) > 0 {
		hits, similarityErr := host.SimilarMessages(ctx, input.UserID, plugins.SimilarMessagesRequest{
			RecentRead: &plugins.RecentReadCandidates{
				Since: time.Now().UTC().Add(-recentReadWindow),
				Limit: plugins.MaxSimilarityRecentReadCandidates,
			},
			CurrentMessageID:  input.MessageID,
			ExcludeMessageIDs: spamIDs,
			Terms:             terms,
			Limit:             plugins.MaxSimilarityResults,
		})
		if similarityErr == nil {
			spamHits, feedbackErr := spamLabeledMessageIDs(ctx, db, input.UserID, similarityHitIDs(hits))
			if feedbackErr == nil {
				hits = filterSpamHits(hits, spamHits)
				recentSupport, explanation.RecentReadNeighbors = recentReadScore(hits, time.Now().UTC())
			}
		}
	}

	finalProbability := clampProbability(personalized * (1 - 0.15*recentSupport))
	record := classificationRecord{
		MessageID:                  input.MessageID,
		ModelVersion:               score.ModelVersion,
		BaseProbability:            base,
		LabeledNeighborProbability: labeledProbability,
		LabeledNeighborCount:       labeledCount,
		RecentReadSupport:          recentSupport,
		FinalProbability:           finalProbability,
		RiskBand:                   riskBand(finalProbability),
		DisplayBand:                riskBand(finalProbability),
		ContentCoverage:            coverage,
		Explanation:                explanation,
	}
	if err := saveClassification(ctx, db, input.UserID, record); err != nil {
		return classificationRecord{}, err
	}
	return record, nil
}

func similarityHitIDs(hits []plugins.SimilarMessageResult) []int64 {
	ids := make([]int64, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.MessageID)
	}
	return ids
}

func filterSpamHits(hits []plugins.SimilarMessageResult, spamIDs map[int64]bool) []plugins.SimilarMessageResult {
	if len(spamIDs) == 0 {
		return hits
	}
	out := make([]plugins.SimilarMessageResult, 0, len(hits))
	for _, hit := range hits {
		if !spamIDs[hit.MessageID] {
			out = append(out, hit)
		}
	}
	return out
}

func modelMessage(input plugins.MessageClassificationInput) spammodel.Message {
	attachmentTypes := make([]string, 0, len(input.Attachments))
	for _, attachment := range input.Attachments {
		if value := strings.ToLower(strings.TrimSpace(attachment.ContentType)); value != "" {
			attachmentTypes = append(attachmentTypes, value)
		}
	}
	mimeType := "text/plain"
	if input.HasHTML {
		mimeType = "text/html"
	}
	return spammodel.Message{
		Subject:         input.Subject,
		Body:            boundedText(classificationBody(input), maxBodyBytes),
		From:            input.From,
		To:              recipientAddresses(input.To, input.CC),
		MIMEType:        mimeType,
		AttachmentTypes: attachmentTypes,
		HTML:            input.HasHTML && !input.IsEncrypted,
	}
}

func labeledCandidates(ctx context.Context, db *sql.DB, userID int64) ([]int64, []int64, map[int64]string, error) {
	spamIDs, err := recentFeedbackIDs(ctx, db, userID, feedbackSpam, 1000)
	if err != nil {
		return nil, nil, nil, err
	}
	hamIDs, err := recentFeedbackIDs(ctx, db, userID, feedbackHam, 1000)
	if err != nil {
		return nil, nil, nil, err
	}
	labels := make(map[int64]string, len(spamIDs)+len(hamIDs))
	for _, id := range spamIDs {
		labels[id] = feedbackSpam
	}
	for _, id := range hamIDs {
		labels[id] = feedbackHam
	}
	return spamIDs, hamIDs, labels, nil
}

func labeledNeighborScore(hits []plugins.SimilarMessageResult, labels map[int64]string) (float64, int, []neighborEvidence) {
	maxScore := 0.0
	for _, hit := range hits {
		if hit.Score > maxScore {
			maxScore = hit.Score
		}
	}
	if maxScore <= 0 {
		return 0.5, 0, nil
	}
	weightedSpam := 1.0
	totalWeight := 2.0
	count := 0
	evidence := make([]neighborEvidence, 0, len(hits))
	for _, hit := range hits {
		label := labels[hit.MessageID]
		if label == "" || hit.Score <= 0 {
			continue
		}
		coverage := clampProbability(hit.WeightedTermCoverage)
		weight := (hit.Score / maxScore) * math.Max(0.05, coverage)
		if weight <= 0 {
			continue
		}
		if label == feedbackSpam {
			weightedSpam += weight
		}
		totalWeight += weight
		count++
		evidence = append(evidence, neighborFromHit(hit, label))
	}
	return clampProbability(weightedSpam / totalWeight), count, evidence
}

func recentReadScore(hits []plugins.SimilarMessageResult, now time.Time) (float64, []neighborEvidence) {
	weightSum := 0.0
	evidence := make([]neighborEvidence, 0, len(hits))
	for _, hit := range hits {
		coverage := clampProbability(hit.WeightedTermCoverage)
		if hit.MatchedTermCount < 3 || coverage < 0.25 || hit.Date.IsZero() {
			continue
		}
		age := now.Sub(hit.Date)
		if age < 0 {
			age = 0
		}
		if age > recentReadWindow {
			continue
		}
		decay := math.Pow(0.5, age.Hours()/(30*24))
		weightSum += coverage * decay
		evidence = append(evidence, neighborFromHit(hit, "read"))
	}
	return clampProbability(1 - math.Exp(-weightSum/2)), evidence
}

func neighborFromHit(hit plugins.SimilarMessageResult, label string) neighborEvidence {
	terms := append([]string(nil), hit.MatchedTerms...)
	if len(terms) > maxStoredMatchedTerms {
		terms = terms[:maxStoredMatchedTerms]
	}
	for index := range terms {
		terms[index] = safeFeature(terms[index])
	}
	return neighborEvidence{
		MessageID:        hit.MessageID,
		Label:            label,
		Score:            hit.Score,
		WeightedCoverage: clampProbability(hit.WeightedTermCoverage),
		Date:             unixOrZero(hit.Date),
		From:             safeFeature(hit.From),
		MatchedTerms:     terms,
	}
}

func contributionEvidence(contributions []spammodel.Contribution) classificationExplanation {
	positive := make([]signalEvidence, 0, len(contributions))
	negative := make([]signalEvidence, 0, len(contributions))
	for _, contribution := range contributions {
		item := signalEvidence{Feature: safeFeature(contribution.Feature), Contribution: contribution.Impact}
		if item.Feature == "" || item.Contribution == 0 {
			continue
		}
		if item.Contribution > 0 {
			positive = append(positive, item)
		} else {
			negative = append(negative, item)
		}
	}
	sort.SliceStable(positive, func(i, j int) bool { return math.Abs(positive[i].Contribution) > math.Abs(positive[j].Contribution) })
	sort.SliceStable(negative, func(i, j int) bool { return math.Abs(negative[i].Contribution) > math.Abs(negative[j].Contribution) })
	if len(positive) > maxStoredSignals {
		positive = positive[:maxStoredSignals]
	}
	if len(negative) > maxStoredSignals {
		negative = negative[:maxStoredSignals]
	}
	return classificationExplanation{PositiveSignals: positive, NegativeSignals: negative}
}

func similarityTerms(input plugins.MessageClassificationInput) []plugins.SimilarityTerm {
	type weightedToken struct {
		text   string
		count  int
		weight float64
		field  string
	}
	var terms []plugins.SimilarityTerm
	seen := map[string]bool{}
	appendTokens := func(field, text string, weight float64, limit int) {
		for _, token := range tokenize(text) {
			key := field + "\x00" + token
			if seen[key] {
				continue
			}
			seen[key] = true
			terms = append(terms, plugins.SimilarityTerm{Field: field, Text: token, Weight: weight})
			if limit > 0 && len(terms) >= limit {
				return
			}
		}
	}
	appendTokens(plugins.SimilarityFieldSubject, input.Subject, 3, 8)
	if domain := senderDomain(input.From); domain != "" && len(terms) < plugins.MaxSimilarityTerms {
		terms = append(terms, plugins.SimilarityTerm{Field: plugins.SimilarityFieldFromDomain, Text: domain, Weight: 2.25})
	}

	counts := map[string]int{}
	for _, token := range tokenize(boundedText(classificationBody(input), 16*1024)) {
		if !seen[plugins.SimilarityFieldBody+"\x00"+token] {
			counts[token]++
		}
	}
	body := make([]weightedToken, 0, len(counts))
	for token, count := range counts {
		body = append(body, weightedToken{text: token, count: count, weight: 1 + math.Min(0.5, math.Log1p(float64(count))/5), field: plugins.SimilarityFieldBody})
	}
	sort.SliceStable(body, func(i, j int) bool {
		if body[i].count != body[j].count {
			return body[i].count > body[j].count
		}
		return body[i].text < body[j].text
	})
	for _, token := range body {
		if len(terms) >= plugins.MaxSimilarityTerms {
			break
		}
		terms = append(terms, plugins.SimilarityTerm{Field: token.field, Text: token.text, Weight: token.weight})
	}
	return terms
}

func classificationBody(input plugins.MessageClassificationInput) string {
	if input.IsEncrypted {
		return ""
	}
	return input.BodyText
}

var similarityStopWords = map[string]bool{
	"and": true, "are": true, "but": true, "for": true, "from": true, "has": true,
	"have": true, "not": true, "that": true, "the": true, "this": true, "was": true,
	"will": true, "with": true, "you": true, "your": true,
}

func tokenize(value string) []string {
	raw := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '@' && r != '.' && r != '-'
	})
	out := make([]string, 0, len(raw))
	for _, token := range raw {
		token = strings.Trim(token, ".-")
		if len(token) < 3 || similarityStopWords[token] {
			continue
		}
		out = append(out, token)
	}
	return out
}

func senderDomain(value string) string {
	address := strings.TrimSpace(value)
	if parsed, err := mail.ParseAddress(address); err == nil {
		address = parsed.Address
	}
	if index := strings.LastIndex(address, "@"); index >= 0 && index+1 < len(address) {
		return strings.ToLower(strings.Trim(strings.TrimSpace(address[index+1:]), ">"))
	}
	return ""
}

func recipientAddresses(values ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, value := range values {
		parsed, err := mail.ParseAddressList(value)
		if err == nil {
			for _, address := range parsed {
				clean := strings.ToLower(strings.TrimSpace(address.Address))
				if clean != "" && !seen[clean] {
					seen[clean] = true
					out = append(out, clean)
				}
			}
			continue
		}
		for _, address := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' }) {
			clean := strings.ToLower(strings.TrimSpace(address))
			if clean != "" && !seen[clean] {
				seen[clean] = true
				out = append(out, clean)
			}
		}
	}
	return out
}

func boundedText(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func safeFeature(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(value))
	if len(value) > 120 {
		value = boundedText(value, 120)
	}
	return value
}

func pluginUserDB(ctx context.Context, host plugins.BackendHost, userID int64) (*store.Store, *sql.DB, error) {
	st, ok := host.Store().(*store.Store)
	if !ok || st == nil {
		return nil, nil, errors.New("spam filter store is not available")
	}
	db, err := st.UserDB(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	return st, db, nil
}
