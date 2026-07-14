package main

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

const (
	reputationRecentWindow          = 90 * 24 * time.Hour
	reputationDecayHalfLife         = 30 * 24 * time.Hour
	reputationFutureClockSkew       = 24 * time.Hour
	reputationMinimumHistorySpan    = 14 * 24 * time.Hour
	reputationMinimumDistinctDays   = 4
	reputationMinimumDistinctThread = 4
	reputationMaxNeighbors          = 8

	reputationGenericLogOddsWeight = 0.55
	reputationExactLogOddsWeight   = 1.25
	reputationMaxLogOddsAdjustment = 1.8
)

// recentReadReputationInput contains all state used by the pure reputation
// scorer. SpamVetoMessageIDs must come from explicit user feedback; model
// predictions must never populate it.
type recentReadReputationInput struct {
	CurrentFrom        string
	Hits               []plugins.SimilarMessageResult
	SpamVetoMessageIDs map[int64]bool
	Now                time.Time
}

// recentReadReputationResult is a bounded, TxRep-like personalization signal.
// LogOddsAdjustment is intended to be added to the model's log odds and is
// therefore non-positive: read history may cautiously reduce spam risk, but it
// cannot turn model output into an automatically learned reputation record.
type recentReadReputationResult struct {
	GenericSupport             float64
	ExactSenderTemplateSupport float64
	LogOddsAdjustment          float64
	Neighbors                  []neighborEvidence
}

type recentReadReputationCandidate struct {
	hit                 plugins.SimilarMessageResult
	date                time.Time
	day                 string
	thread              string
	weight              float64
	exactSenderTemplate bool
}

// scoreRecentReadReputation combines generic similarity with a more demanding
// exact-sender/template channel. Exact From equality is necessary but never
// sufficient: the hit must also contain a substantive body/template match and
// come from diverse days and threads. Vetoed messages never contribute, and a
// vetoed hit from the exact canonical sender disables the stronger channel.
func scoreRecentReadReputation(input recentReadReputationInput) recentReadReputationResult {
	now := input.Now.UTC()
	if now.IsZero() {
		return recentReadReputationResult{}
	}

	currentSender := store.SenderIdentity(input.CurrentFrom)
	candidates := make([]recentReadReputationCandidate, 0, len(input.Hits))
	exactSenderVeto := false
	for _, hit := range input.Hits {
		hitSender := store.SenderIdentity(hit.From)
		if input.SpamVetoMessageIDs[hit.MessageID] {
			if currentSender != "" && hitSender == currentSender {
				exactSenderVeto = true
			}
			continue
		}
		candidate, ok := reputationCandidateFromHit(hit, now)
		if !ok {
			continue
		}
		candidate.exactSenderTemplate = currentSender != "" &&
			hitSender == currentSender && reputationTemplateMatch(hit)
		candidates = append(candidates, candidate)
	}

	genericCandidates := independentReputationCandidates(candidates)
	genericSupport := reputationSupport(genericCandidates)

	exactSupport := 0.0
	var exactCandidates []recentReadReputationCandidate
	if !exactSenderVeto {
		// Enforce the diversity minimum on evidence that can independently
		// contribute, not on a raw set that later collapses by day or thread.
		exactCandidates = independentReputationCandidates(exactReputationCandidates(candidates))
		if reputationHistoryIsDiverse(exactCandidates) {
			exactSupport = reputationSupport(exactCandidates)
		} else {
			exactCandidates = nil
		}
	}

	return recentReadReputationResult{
		GenericSupport:             genericSupport,
		ExactSenderTemplateSupport: exactSupport,
		LogOddsAdjustment:          boundedReputationLogOddsAdjustment(genericSupport, exactSupport),
		Neighbors:                  reputationNeighborEvidence(genericCandidates, exactCandidates),
	}
}

func reputationCandidateFromHit(hit plugins.SimilarMessageResult, now time.Time) (recentReadReputationCandidate, bool) {
	coverage := reputationUnit(hit.WeightedTermCoverage)
	if hit.MessageID <= 0 || hit.Score <= 0 || math.IsNaN(hit.Score) || math.IsInf(hit.Score, 0) ||
		hit.MatchedTermCount < 3 || coverage < 0.25 || hit.Date.IsZero() {
		return recentReadReputationCandidate{}, false
	}
	date := hit.Date.UTC()
	if date.After(now.Add(reputationFutureClockSkew)) {
		return recentReadReputationCandidate{}, false
	}
	age := now.Sub(date)
	if age < 0 {
		age = 0
	}
	if age > reputationRecentWindow {
		return recentReadReputationCandidate{}, false
	}
	decay := math.Pow(0.5, float64(age)/float64(reputationDecayHalfLife))
	thread := strings.TrimSpace(hit.ThreadKey)
	if thread == "" {
		thread = "message:" + strconv.FormatInt(hit.MessageID, 10)
	}
	return recentReadReputationCandidate{
		hit:    hit,
		date:   date,
		day:    date.Format("2006-01-02"),
		thread: thread,
		weight: coverage * decay,
	}, true
}

func reputationTemplateMatch(hit plugins.SimilarMessageResult) bool {
	coverage := reputationUnit(hit.WeightedTermCoverage)
	if hit.MatchedTermCount < 3 || coverage < 0.35 || !reputationHasField(hit.MatchedFields, plugins.SimilarityFieldBody) {
		return false
	}
	return hit.MatchedTermCount >= 5 || reputationHasField(hit.MatchedFields, plugins.SimilarityFieldSubject)
}

func reputationHasField(fields []string, target string) bool {
	for _, field := range fields {
		if field == target {
			return true
		}
	}
	return false
}

func exactReputationCandidates(candidates []recentReadReputationCandidate) []recentReadReputationCandidate {
	out := make([]recentReadReputationCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.exactSenderTemplate {
			out = append(out, candidate)
		}
	}
	return out
}

func reputationHistoryIsDiverse(candidates []recentReadReputationCandidate) bool {
	if len(candidates) < reputationMinimumDistinctDays || len(candidates) < reputationMinimumDistinctThread {
		return false
	}
	days := make(map[string]bool, len(candidates))
	threads := make(map[string]bool, len(candidates))
	oldest := candidates[0].date
	newest := candidates[0].date
	for _, candidate := range candidates {
		days[candidate.day] = true
		threads[candidate.thread] = true
		if candidate.date.Before(oldest) {
			oldest = candidate.date
		}
		if candidate.date.After(newest) {
			newest = candidate.date
		}
	}
	return len(days) >= reputationMinimumDistinctDays &&
		len(threads) >= reputationMinimumDistinctThread &&
		newest.Sub(oldest) >= reputationMinimumHistorySpan
}

// independentReputationCandidates prevents a busy thread or a burst of mail
// on one day from manufacturing reputation. The strongest hit wins for each
// day and thread, making the result independent of Bleve's input ordering.
func independentReputationCandidates(input []recentReadReputationCandidate) []recentReadReputationCandidate {
	candidates := append([]recentReadReputationCandidate(nil), input...)
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].weight != candidates[j].weight {
			return candidates[i].weight > candidates[j].weight
		}
		if !candidates[i].date.Equal(candidates[j].date) {
			return candidates[i].date.After(candidates[j].date)
		}
		return candidates[i].hit.MessageID < candidates[j].hit.MessageID
	})
	seenDays := make(map[string]bool, len(candidates))
	seenThreads := make(map[string]bool, len(candidates))
	out := make([]recentReadReputationCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if seenDays[candidate.day] || seenThreads[candidate.thread] {
			continue
		}
		seenDays[candidate.day] = true
		seenThreads[candidate.thread] = true
		out = append(out, candidate)
	}
	return out
}

func reputationSupport(candidates []recentReadReputationCandidate) float64 {
	weightSum := 0.0
	for _, candidate := range candidates {
		weightSum += candidate.weight
	}
	return reputationUnit(1 - math.Exp(-weightSum/2))
}

func boundedReputationLogOddsAdjustment(genericSupport, exactSupport float64) float64 {
	shift := reputationGenericLogOddsWeight*reputationUnit(genericSupport) +
		reputationExactLogOddsWeight*reputationUnit(exactSupport)
	return -math.Min(reputationMaxLogOddsAdjustment, shift)
}

func reputationNeighborEvidence(generic, exact []recentReadReputationCandidate) []neighborEvidence {
	exactIDs := make(map[int64]bool, len(exact))
	for _, candidate := range exact {
		exactIDs[candidate.hit.MessageID] = true
	}
	seen := make(map[int64]bool, len(generic)+len(exact))
	out := make([]neighborEvidence, 0, min(reputationMaxNeighbors, len(generic)+len(exact)))
	appendCandidate := func(candidate recentReadReputationCandidate) {
		if len(out) >= reputationMaxNeighbors || seen[candidate.hit.MessageID] {
			return
		}
		seen[candidate.hit.MessageID] = true
		label := "read"
		if exactIDs[candidate.hit.MessageID] {
			label = "read_sender_template"
		}
		out = append(out, neighborFromHit(candidate.hit, label))
	}
	for _, candidate := range exact {
		appendCandidate(candidate)
	}
	for _, candidate := range generic {
		appendCandidate(candidate)
	}
	return out
}

func reputationUnit(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	if value >= 1 {
		return 1
	}
	return value
}
