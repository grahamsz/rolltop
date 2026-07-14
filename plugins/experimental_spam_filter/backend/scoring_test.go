package main

import (
	"math"
	"testing"
)

func TestProbabilityLogOddsRoundTrip(t *testing.T) {
	for _, probability := range []float64{0.001, 0.1, 0.5, 0.9, 0.999} {
		got := probabilityFromLogOdds(probabilityLogOdds(probability))
		if math.Abs(got-probability) > 1e-12 {
			t.Fatalf("round trip %v = %v", probability, got)
		}
	}
}

func TestPersonalBayesUsesNeutralDeadZoneAndBoundedBuckets(t *testing.T) {
	if adjustment, rule := personalBayesLogOddsAdjustment(0.5); adjustment != 0 || rule != "" {
		t.Fatalf("neutral Bayes adjustment = %v %q", adjustment, rule)
	}
	spam, spamRule := personalBayesLogOddsAdjustment(0.999)
	ham, hamRule := personalBayesLogOddsAdjustment(0.001)
	if spam != -ham || spam > maximumBayesLogOdds || spamRule != "PERSONAL_BAYES_99" || hamRule != "PERSONAL_BAYES_00" {
		t.Fatalf("extreme Bayes buckets = spam %v/%q ham %v/%q", spam, spamRule, ham, hamRule)
	}
}

func TestLabeledNeighborAdjustmentRequiresThreeAndIsBounded(t *testing.T) {
	if got := labeledNeighborLogOddsAdjustment(1, 2); got != 0 {
		t.Fatalf("two-neighbor adjustment = %v", got)
	}
	for _, probability := range []float64{0, 1} {
		got := labeledNeighborLogOddsAdjustment(probability, 100)
		if math.Abs(got) > maximumLabeledNeighborLogOdds {
			t.Fatalf("unbounded neighbor adjustment = %v", got)
		}
	}
}
