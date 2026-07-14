package main

import (
	"math"
	"testing"
	"time"

	"rolltop/backend/plugins"
)

func TestRecentReadReputationRequiresDiverseExactSenderTemplateHistory(t *testing.T) {
	now := time.Date(2026, 7, 14, 18, 0, 0, 0, time.UTC)
	hits := []plugins.SimilarMessageResult{
		reputationTestHit(1, `Newsletter <NEWS@example.test>`, "thread-1", now, 0),
		reputationTestHit(2, `news@example.test`, "thread-2", now, 5),
		reputationTestHit(3, `"Weekly News" <news@EXAMPLE.TEST>`, "thread-3", now, 10),
		reputationTestHit(4, `<news@example.test>`, "thread-4", now, 15),
	}

	result := scoreRecentReadReputation(recentReadReputationInput{
		CurrentFrom: "Current Newsletter <news@example.test>",
		Hits:        hits,
		Now:         now,
	})
	if result.GenericSupport <= 0 || result.GenericSupport > 1 {
		t.Fatalf("generic support = %f, want (0,1]", result.GenericSupport)
	}
	if result.ExactSenderTemplateSupport <= 0 || result.ExactSenderTemplateSupport > 1 {
		t.Fatalf("exact sender/template support = %f, want (0,1]", result.ExactSenderTemplateSupport)
	}
	if result.LogOddsAdjustment >= 0 || result.LogOddsAdjustment < -reputationMaxLogOddsAdjustment {
		t.Fatalf("log-odds adjustment = %f, want bounded negative adjustment", result.LogOddsAdjustment)
	}
	if len(result.Neighbors) != 4 {
		t.Fatalf("neighbors = %d, want 4", len(result.Neighbors))
	}
	for _, neighbor := range result.Neighbors {
		if neighbor.Label != "read_sender_template" {
			t.Fatalf("neighbor label = %q, want exact sender/template evidence", neighbor.Label)
		}
	}
}

func TestRecentReadReputationAsWeMoveModerateOverlapActivatesExactSenderChannel(t *testing.T) {
	now := time.Date(2026, 7, 14, 18, 0, 0, 0, time.UTC)
	hits := make([]plugins.SimilarMessageResult, 0, 4)
	for index, ageDays := range []int{0, 5, 10, 15} {
		hit := reputationTestHit(int64(index+1), `AsWeMove <dispatch@aswemove.example>`, "aswemove-thread-"+string(rune('a'+index)), now, ageDays)
		hit.MatchedTermCount = 3
		hit.MatchedTerms = []string{"movement", "weekly", "practice"}
		hit.MatchedFields = []string{plugins.SimilarityFieldSubject, plugins.SimilarityFieldBody}
		hit.WeightedTermCoverage = 0.4
		hits = append(hits, hit)
	}

	result := scoreRecentReadReputation(recentReadReputationInput{
		CurrentFrom: `As We Move <DISPATCH@ASWEMOVE.EXAMPLE>`,
		Hits:        hits,
		Now:         now,
	})
	if result.ExactSenderTemplateSupport <= 0 {
		t.Fatalf("AsWeMove exact sender/template support = %f, want moderate repeated overlap to activate it", result.ExactSenderTemplateSupport)
	}
	if result.LogOddsAdjustment >= 0 || result.LogOddsAdjustment < -reputationMaxLogOddsAdjustment {
		t.Fatalf("AsWeMove log-odds adjustment = %f, want bounded negative adjustment", result.LogOddsAdjustment)
	}
	// The checked-in scorecard's AsWeMove regression case is 0.432297654560482
	// before personalization. Four independent recent reads from the exact
	// sender/template must move that representative message below the 0.35
	// medium-risk display boundary.
	personalized := probabilityFromLogOdds(probabilityLogOdds(0.432297654560482) + result.LogOddsAdjustment)
	if personalized >= lowRiskBoundary {
		t.Fatalf("AsWeMove personalized probability = %f, want low risk below %f", personalized, lowRiskBoundary)
	}
}

func TestRecentReadReputationDoesNotTrustBareFromOrRepeatedHistory(t *testing.T) {
	now := time.Date(2026, 7, 14, 18, 0, 0, 0, time.UTC)
	bareFromHits := make([]plugins.SimilarMessageResult, 0, 4)
	repeatedHits := make([]plugins.SimilarMessageResult, 0, 4)
	for index, ageDays := range []int{0, 5, 10, 15} {
		bare := reputationTestHit(int64(index+1), "news@example.test", "thread-bare-"+string(rune('a'+index)), now, ageDays)
		bare.MatchedFields = []string{plugins.SimilarityFieldFromDomain}
		bareFromHits = append(bareFromHits, bare)

		repeated := reputationTestHit(int64(index+10), "news@example.test", "one-thread", now, 0)
		repeatedHits = append(repeatedHits, repeated)
	}

	bareResult := scoreRecentReadReputation(recentReadReputationInput{
		CurrentFrom: "news@example.test",
		Hits:        bareFromHits,
		Now:         now,
	})
	if bareResult.ExactSenderTemplateSupport != 0 {
		t.Fatalf("bare From exact support = %f, want 0", bareResult.ExactSenderTemplateSupport)
	}

	repeatedResult := scoreRecentReadReputation(recentReadReputationInput{
		CurrentFrom: "news@example.test",
		Hits:        repeatedHits,
		Now:         now,
	})
	if repeatedResult.ExactSenderTemplateSupport != 0 {
		t.Fatalf("repeated history exact support = %f, want 0", repeatedResult.ExactSenderTemplateSupport)
	}
	if len(repeatedResult.Neighbors) != 1 {
		t.Fatalf("repeated history neighbors = %d, want one independent contribution", len(repeatedResult.Neighbors))
	}
}

func TestRecentReadReputationChecksDiversityAfterIndependence(t *testing.T) {
	now := time.Date(2026, 7, 14, 18, 0, 0, 0, time.UTC)
	hits := []plugins.SimilarMessageResult{
		reputationTestHit(1, "news@example.test", "thread-1", now, 0),
		reputationTestHit(2, "news@example.test", "thread-2", now, 0),
		reputationTestHit(3, "news@example.test", "thread-3", now, 0),
		reputationTestHit(4, "news@example.test", "thread-4", now, 0),
		reputationTestHit(5, "news@example.test", "thread-1", now, 5),
		reputationTestHit(6, "news@example.test", "thread-1", now, 10),
		reputationTestHit(7, "news@example.test", "thread-1", now, 15),
	}

	result := scoreRecentReadReputation(recentReadReputationInput{
		CurrentFrom: "News <news@example.test>",
		Hits:        hits,
		Now:         now,
	})
	if result.GenericSupport <= 0 {
		t.Fatalf("generic support = %f, want weak support from the one independent hit", result.GenericSupport)
	}
	if result.ExactSenderTemplateSupport != 0 {
		t.Fatalf("exact sender/template support = %f, want 0 after candidates collapse to one independent hit", result.ExactSenderTemplateSupport)
	}
	for _, neighbor := range result.Neighbors {
		if neighbor.Label == "read_sender_template" {
			t.Fatalf("non-diverse effective history was labeled exact sender/template evidence: %+v", neighbor)
		}
	}
}

func TestRecentReadReputationExplicitSpamVetoDisablesExactSenderChannel(t *testing.T) {
	now := time.Date(2026, 7, 14, 18, 0, 0, 0, time.UTC)
	hits := make([]plugins.SimilarMessageResult, 0, 5)
	for index, ageDays := range []int{0, 5, 10, 15, 20} {
		hits = append(hits, reputationTestHit(int64(index+1), "news@example.test", "thread-"+string(rune('a'+index)), now, ageDays))
	}

	result := scoreRecentReadReputation(recentReadReputationInput{
		CurrentFrom:        "News <news@example.test>",
		Hits:               hits,
		SpamVetoMessageIDs: map[int64]bool{hits[0].MessageID: true},
		Now:                now,
	})
	if result.ExactSenderTemplateSupport != 0 {
		t.Fatalf("vetoed exact support = %f, want 0", result.ExactSenderTemplateSupport)
	}
	if result.GenericSupport <= 0 {
		t.Fatalf("generic support = %f, want unrelated weak channel to remain available", result.GenericSupport)
	}
	for _, neighbor := range result.Neighbors {
		if neighbor.MessageID == hits[0].MessageID {
			t.Fatalf("spam-vetoed message returned as supporting evidence: %+v", neighbor)
		}
	}
}

func TestRecentReadReputationRejectsUnsafeDatesAndBoundsAdjustment(t *testing.T) {
	now := time.Date(2026, 7, 14, 18, 0, 0, 0, time.UTC)
	old := reputationTestHit(1, "news@example.test", "old", now, 91)
	future := reputationTestHit(2, "news@example.test", "future", now, -2)
	result := scoreRecentReadReputation(recentReadReputationInput{
		CurrentFrom: "news@example.test",
		Hits:        []plugins.SimilarMessageResult{old, future},
		Now:         now,
	})
	if result.GenericSupport != 0 || result.ExactSenderTemplateSupport != 0 || result.LogOddsAdjustment != 0 || len(result.Neighbors) != 0 {
		t.Fatalf("unsafe-date result = %+v, want no reputation evidence", result)
	}

	if got := boundedReputationLogOddsAdjustment(10, 10); got != -reputationMaxLogOddsAdjustment {
		t.Fatalf("capped adjustment = %f, want %f", got, -reputationMaxLogOddsAdjustment)
	}
	if got := boundedReputationLogOddsAdjustment(math.NaN(), math.Inf(1)); got != 0 {
		t.Fatalf("non-finite adjustment = %f, want 0", got)
	}
}

func TestRecentReadReputationIsInputOrderIndependentAndBoundsEvidence(t *testing.T) {
	now := time.Date(2026, 7, 14, 18, 0, 0, 0, time.UTC)
	hits := make([]plugins.SimilarMessageResult, 0, 12)
	for index := 0; index < 12; index++ {
		hit := reputationTestHit(int64(index+1), "news@example.test", "thread-"+string(rune('a'+index)), now, index*3)
		hit.WeightedTermCoverage = 0.5 + float64(index)/100
		hits = append(hits, hit)
	}
	first := scoreRecentReadReputation(recentReadReputationInput{CurrentFrom: "news@example.test", Hits: hits, Now: now})
	for left, right := 0, len(hits)-1; left < right; left, right = left+1, right-1 {
		hits[left], hits[right] = hits[right], hits[left]
	}
	second := scoreRecentReadReputation(recentReadReputationInput{CurrentFrom: "news@example.test", Hits: hits, Now: now})
	if math.Abs(first.GenericSupport-second.GenericSupport) > 1e-12 ||
		math.Abs(first.ExactSenderTemplateSupport-second.ExactSenderTemplateSupport) > 1e-12 ||
		math.Abs(first.LogOddsAdjustment-second.LogOddsAdjustment) > 1e-12 {
		t.Fatalf("order changed result: first=%+v second=%+v", first, second)
	}
	if len(first.Neighbors) != reputationMaxNeighbors || len(second.Neighbors) != reputationMaxNeighbors {
		t.Fatalf("neighbor bounds = %d/%d, want %d", len(first.Neighbors), len(second.Neighbors), reputationMaxNeighbors)
	}
}

func reputationTestHit(id int64, from, thread string, now time.Time, ageDays int) plugins.SimilarMessageResult {
	return plugins.SimilarMessageResult{
		MessageID:            id,
		Score:                10,
		MatchedTerms:         []string{"weekly", "camera", "darkroom", "photos"},
		MatchedTermCount:     4,
		MatchedFields:        []string{plugins.SimilarityFieldSubject, plugins.SimilarityFieldBody},
		WeightedTermCoverage: 0.8,
		Date:                 now.Add(-time.Duration(ageDays) * 24 * time.Hour),
		From:                 from,
		ThreadKey:            thread,
	}
}
