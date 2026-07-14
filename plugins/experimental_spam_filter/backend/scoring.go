package main

import "math"

const (
	maximumLabeledNeighborLogOdds = 0.9
	maximumBayesLogOdds           = 2.5
)

// probabilityLogOdds and probabilityFromLogOdds keep the independently
// sourced rule, Bayes, labeled-neighbor, and reputation stages additive. This
// mirrors SpamAssassin's scorecard shape better than repeatedly averaging or
// multiplying probabilities.
func probabilityLogOdds(probability float64) float64 {
	const epsilon = 1e-6
	probability = clampProbability(probability)
	probability = math.Max(epsilon, math.Min(1-epsilon, probability))
	return math.Log(probability / (1 - probability))
}

func probabilityFromLogOdds(logOdds float64) float64 {
	if logOdds >= 0 {
		exponential := math.Exp(-logOdds)
		return 1 / (1 + exponential)
	}
	exponential := math.Exp(logOdds)
	return exponential / (1 + exponential)
}

// labeledNeighborLogOddsAdjustment retains Bleve's useful explicit-label
// evidence without allowing one or two coincidental matches to move a score.
// The bounded adjustment is symmetric and intentionally weaker than mature
// personal Bayes evidence.
func labeledNeighborLogOddsAdjustment(probability float64, count int) float64 {
	if count < 3 {
		return 0
	}
	confidence := math.Min(1, float64(count-2)/6)
	centered := (clampProbability(probability) - 0.5) * 2
	return math.Max(-maximumLabeledNeighborLogOdds,
		math.Min(maximumLabeledNeighborLogOdds, centered*confidence*maximumLabeledNeighborLogOdds))
}

// personalBayesLogOddsAdjustment exposes Robinson/Fisher Bayes as discrete,
// explainable score buckets, like SpamAssassin's BAYES_* rules. Values in the
// broad uncertain middle are deliberately neutral.
func personalBayesLogOddsAdjustment(probability float64) (float64, string) {
	probability = clampProbability(probability)
	switch {
	case probability >= 0.99:
		return maximumBayesLogOdds, "PERSONAL_BAYES_99"
	case probability >= 0.95:
		return 1.75, "PERSONAL_BAYES_95"
	case probability >= 0.80:
		return 0.75, "PERSONAL_BAYES_80"
	case probability <= 0.01:
		return -maximumBayesLogOdds, "PERSONAL_BAYES_00"
	case probability <= 0.05:
		return -1.75, "PERSONAL_BAYES_05"
	case probability <= 0.20:
		return -0.75, "PERSONAL_BAYES_20"
	default:
		return 0, ""
	}
}
