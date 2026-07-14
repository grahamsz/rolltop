package training

import (
	"errors"
	"math"
	"sort"
)

type Metrics struct {
	PRAUC            float64 `json:"pr_auc"`
	ROCAUC           float64 `json:"roc_auc"`
	Brier            float64 `json:"brier_score"`
	CalibrationError float64 `json:"calibration_error"`
}

type OperatingPoint struct {
	MaximumValidationFalsePositiveRate float64 `json:"maximum_validation_false_positive_rate"`
	Threshold                          float64 `json:"threshold_selected_on_validation"`
	ValidationRecall                   float64 `json:"validation_recall"`
	ValidationFalsePositiveRate        float64 `json:"validation_false_positive_rate"`
	TestRecall                         float64 `json:"test_recall"`
	TestFalsePositiveRate              float64 `json:"test_false_positive_rate"`
	TestTruePositives                  int     `json:"test_true_positives"`
	TestFalsePositives                 int     `json:"test_false_positives"`
	TestSpam                           int     `json:"test_spam"`
	TestHam                            int     `json:"test_ham"`
}

type scoredLabel struct {
	Probability float64
	Spam        bool
}

func calculateMetrics(probabilities []float64, labels []bool) (Metrics, error) {
	if len(probabilities) == 0 || len(probabilities) != len(labels) {
		return Metrics{}, errors.New("metrics require equally sized non-empty inputs")
	}
	items := make([]scoredLabel, len(labels))
	positive, negative := 0, 0
	brier := 0.0
	for index, label := range labels {
		probability := math.Max(0, math.Min(1, probabilities[index]))
		items[index] = scoredLabel{Probability: probability, Spam: label}
		target := 0.0
		if label {
			target = 1
			positive++
		} else {
			negative++
		}
		difference := probability - target
		brier += difference * difference
	}
	if positive == 0 || negative == 0 {
		return Metrics{}, errors.New("metrics require both labels")
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Probability > items[j].Probability })
	truePositive, falsePositive := 0, 0
	averagePrecision, rocArea := 0.0, 0.0
	previousTPR, previousFPR := 0.0, 0.0
	for index := 0; index < len(items); {
		end := index
		groupPositive, groupNegative := 0, 0
		for end < len(items) && items[end].Probability == items[index].Probability {
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
		index = end
	}
	return Metrics{
		PRAUC: averagePrecision / float64(positive), ROCAUC: rocArea,
		Brier: brier / float64(len(labels)), CalibrationError: expectedCalibrationError(probabilities, labels, 10),
	}, nil
}

// validationSelectedOperatingPoint picks the highest validation recall whose
// false-positive rate does not exceed the declared budget. Held-out test labels
// are evaluated only after the threshold is fixed.
func validationSelectedOperatingPoint(validationProbabilities []float64, validationLabels []bool, testProbabilities []float64, testLabels []bool, maximumValidationFPR float64) (OperatingPoint, error) {
	threshold, err := thresholdForFalsePositiveBudget(validationProbabilities, validationLabels, maximumValidationFPR)
	if err != nil {
		return OperatingPoint{}, err
	}
	validationRecall, validationFPR, _, _, _, _, err := evaluateThreshold(validationProbabilities, validationLabels, threshold)
	if err != nil {
		return OperatingPoint{}, err
	}
	testRecall, testFPR, truePositives, falsePositives, spam, ham, err := evaluateThreshold(testProbabilities, testLabels, threshold)
	if err != nil {
		return OperatingPoint{}, err
	}
	return OperatingPoint{
		MaximumValidationFalsePositiveRate: maximumValidationFPR,
		Threshold:                          threshold, ValidationRecall: validationRecall, ValidationFalsePositiveRate: validationFPR,
		TestRecall: testRecall, TestFalsePositiveRate: testFPR, TestTruePositives: truePositives,
		TestFalsePositives: falsePositives, TestSpam: spam, TestHam: ham,
	}, nil
}

func thresholdForFalsePositiveBudget(probabilities []float64, labels []bool, maximumFPR float64) (float64, error) {
	if len(probabilities) == 0 || len(probabilities) != len(labels) || maximumFPR < 0 || maximumFPR >= 1 {
		return 0, errors.New("threshold selection requires probabilities, labels, and a valid false-positive budget")
	}
	items := make([]scoredLabel, len(labels))
	positive, negative := 0, 0
	for index, label := range labels {
		probability := math.Max(0, math.Min(1, probabilities[index]))
		items[index] = scoredLabel{Probability: probability, Spam: label}
		if label {
			positive++
		} else {
			negative++
		}
	}
	if positive == 0 || negative == 0 {
		return 0, errors.New("threshold selection requires both labels")
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Probability > items[j].Probability })
	bestThreshold := math.Min(1, math.Nextafter(items[0].Probability, 1))
	bestRecall, bestFPR := 0.0, 0.0
	truePositive, falsePositive := 0, 0
	for index := 0; index < len(items); {
		end := index
		for end < len(items) && items[end].Probability == items[index].Probability {
			if items[end].Spam {
				truePositive++
			} else {
				falsePositive++
			}
			end++
		}
		recall := float64(truePositive) / float64(positive)
		fpr := float64(falsePositive) / float64(negative)
		if fpr <= maximumFPR && (recall > bestRecall || (recall == bestRecall && fpr < bestFPR)) {
			bestThreshold, bestRecall, bestFPR = items[index].Probability, recall, fpr
		}
		index = end
	}
	if bestThreshold <= 0 || bestThreshold >= 1 {
		return 0, errors.New("validation did not produce a usable threshold")
	}
	return bestThreshold, nil
}

func evaluateThreshold(probabilities []float64, labels []bool, threshold float64) (recall float64, falsePositiveRate float64, truePositive int, falsePositive int, positive int, negative int, err error) {
	if len(probabilities) == 0 || len(probabilities) != len(labels) || threshold <= 0 || threshold >= 1 {
		err = errors.New("threshold evaluation requires probabilities, labels, and a threshold inside (0,1)")
		return
	}
	for index, label := range labels {
		if label {
			positive++
		} else {
			negative++
		}
		if probabilities[index] < threshold {
			continue
		}
		if label {
			truePositive++
		} else {
			falsePositive++
		}
	}
	if positive == 0 || negative == 0 {
		err = errors.New("threshold evaluation requires both labels")
		return
	}
	recall = float64(truePositive) / float64(positive)
	falsePositiveRate = float64(falsePositive) / float64(negative)
	return
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
	result := 0.0
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
