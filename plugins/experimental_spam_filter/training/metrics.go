package training

import (
	"errors"
	"math"
	"math/rand"
	"sort"
)

type Metrics struct {
	PRAUC             float64 `json:"pr_auc"`
	ROCAUC            float64 `json:"roc_auc"`
	Brier             float64 `json:"brier_score"`
	CalibrationError  float64 `json:"calibration_error"`
	RecallTarget      float64 `json:"recall_target"`
	FalsePositiveRate float64 `json:"false_positive_rate_at_recall"`
	Threshold         float64 `json:"threshold_at_recall"`
}

type scoredLabel struct {
	Probability float64
	Ranking     float64
	Spam        bool
}

func calculateMetrics(probabilities []float64, labels []bool) (Metrics, error) {
	return calculateMetricsWithRanking(probabilities, probabilities, labels)
}

func calculateMetricsWithRanking(probabilities, rankings []float64, labels []bool) (Metrics, error) {
	if len(probabilities) == 0 || len(probabilities) != len(labels) || len(rankings) != len(labels) {
		return Metrics{}, errors.New("metrics require equally sized non-empty inputs")
	}
	items := make([]scoredLabel, len(labels))
	positive, negative := 0, 0
	var brier float64
	for index := range labels {
		probability := math.Max(0, math.Min(1, probabilities[index]))
		items[index] = scoredLabel{Probability: probability, Ranking: rankings[index], Spam: labels[index]}
		target := 0.0
		if labels[index] {
			target = 1
			positive++
		} else {
			negative++
		}
		difference := probability - target
		brier += difference * difference
	}
	if positive == 0 || negative == 0 {
		return Metrics{}, errors.New("metrics require positive and negative examples")
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Ranking > items[j].Ranking })

	truePositive, falsePositive := 0, 0
	averagePrecision := 0.0
	rocArea := 0.0
	previousTPR, previousFPR := 0.0, 0.0
	threshold := 0.0
	fprAtRecall := 1.0
	foundTarget := false
	for index := 0; index < len(items); {
		end := index
		groupPositive, groupNegative := 0, 0
		for end < len(items) && items[end].Ranking == items[index].Ranking {
			if items[end].Spam {
				groupPositive++
			} else {
				groupNegative++
			}
			end++
		}
		truePositive += groupPositive
		falsePositive += groupNegative
		if groupPositive > 0 {
			averagePrecision += float64(groupPositive) * float64(truePositive) / float64(truePositive+falsePositive)
		}
		tpr := float64(truePositive) / float64(positive)
		fpr := float64(falsePositive) / float64(negative)
		rocArea += (fpr - previousFPR) * (tpr + previousTPR) / 2
		previousTPR, previousFPR = tpr, fpr
		if !foundTarget && tpr >= 0.90 {
			threshold = items[index].Probability
			fprAtRecall = fpr
			foundTarget = true
		}
		index = end
	}

	return Metrics{
		PRAUC:             averagePrecision / float64(positive),
		ROCAUC:            rocArea,
		Brier:             brier / float64(len(labels)),
		CalibrationError:  expectedCalibrationError(probabilities, labels, 10),
		RecallTarget:      0.90,
		FalsePositiveRate: fprAtRecall,
		Threshold:         threshold,
	}, nil
}

func expectedCalibrationError(probabilities []float64, labels []bool, bins int) float64 {
	counts := make([]int, bins)
	probabilitySums := make([]float64, bins)
	targetSums := make([]float64, bins)
	for index, probability := range probabilities {
		bin := int(probability * float64(bins))
		if bin >= bins {
			bin = bins - 1
		}
		if bin < 0 {
			bin = 0
		}
		counts[bin]++
		probabilitySums[bin] += probability
		if labels[index] {
			targetSums[bin]++
		}
	}
	var result float64
	for bin := range counts {
		if counts[bin] == 0 {
			continue
		}
		meanProbability := probabilitySums[bin] / float64(counts[bin])
		meanTarget := targetSums[bin] / float64(counts[bin])
		result += float64(counts[bin]) / float64(len(labels)) * math.Abs(meanProbability-meanTarget)
	}
	return result
}

func bootstrapFPRDifference(logisticProbabilities, logisticRankings, bayesProbabilities, bayesRankings []float64, labels []bool, iterations int) (float64, float64, error) {
	if len(labels) == 0 || len(logisticProbabilities) != len(labels) || len(logisticRankings) != len(labels) ||
		len(bayesProbabilities) != len(labels) || len(bayesRankings) != len(labels) {
		return 0, 0, errors.New("invalid bootstrap inputs")
	}
	random := rand.New(rand.NewSource(TrainingSeed + 97))
	differences := make([]float64, 0, iterations)
	for iteration := 0; iteration < iterations; iteration++ {
		logisticSample := make([]float64, len(labels))
		logisticRankingSample := make([]float64, len(labels))
		bayesSample := make([]float64, len(labels))
		bayesRankingSample := make([]float64, len(labels))
		labelSample := make([]bool, len(labels))
		for index := range labels {
			selected := random.Intn(len(labels))
			logisticSample[index] = logisticProbabilities[selected]
			logisticRankingSample[index] = logisticRankings[selected]
			bayesSample[index] = bayesProbabilities[selected]
			bayesRankingSample[index] = bayesRankings[selected]
			labelSample[index] = labels[selected]
		}
		logisticMetrics, logisticErr := calculateMetricsWithRanking(logisticSample, logisticRankingSample, labelSample)
		bayesMetrics, bayesErr := calculateMetricsWithRanking(bayesSample, bayesRankingSample, labelSample)
		if logisticErr != nil || bayesErr != nil {
			continue
		}
		differences = append(differences, logisticMetrics.FalsePositiveRate-bayesMetrics.FalsePositiveRate)
	}
	if len(differences) < iterations*9/10 {
		return 0, 0, errors.New("too many invalid bootstrap samples")
	}
	sort.Float64s(differences)
	lowerIndex := int(float64(len(differences)-1) * 0.025)
	upperIndex := int(math.Ceil(float64(len(differences)-1) * 0.975))
	return differences[lowerIndex], differences[upperIndex], nil
}
