package training

import (
	"errors"
	"math"
	"math/rand"
	"sort"

	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

const (
	TrainerVersion = "rolltop-named-rule-perceptron-v2"
	TrainingSeed   = int64(20260713)
	minRuleSupport = 5
)

type PerceptronConfig struct {
	Epochs        int     `json:"epochs"`
	LearningRate  float64 `json:"learning_rate"`
	HamPreference float64 `json:"ham_preference"`
	WeightDecay   float64 `json:"weight_decay"`
}

var DefaultPerceptronConfig = PerceptronConfig{
	Epochs:        30,
	LearningRate:  1.0,
	HamPreference: 0.5,
	WeightDecay:   0.995,
}

type splitSet struct {
	Train      []sample
	Validation []sample
	Test       []sample
}

// splitSamples is deterministic and stratified by dated archive. Message IDs
// in the public corpus include a content digest, so ordered slicing does not
// depend on filesystem traversal order.
func splitSamples(samples []sample) splitSet {
	bySource := make(map[string][]sample)
	for _, item := range samples {
		bySource[item.Source] = append(bySource[item.Source], item)
	}
	var sources []string
	for source := range bySource {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	var result splitSet
	for _, source := range sources {
		items := bySource[source]
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		trainEnd := len(items) * 70 / 100
		validationEnd := len(items) * 85 / 100
		if trainEnd == 0 && len(items) > 0 {
			trainEnd = 1
		}
		if validationEnd <= trainEnd && len(items) > trainEnd {
			validationEnd = trainEnd + 1
		}
		result.Train = append(result.Train, items[:trainEnd]...)
		result.Validation = append(result.Validation, items[trainEnd:validationEnd]...)
		result.Test = append(result.Test, items[validationEnd:]...)
	}
	return result
}

type hitSample struct {
	ID     string
	Source string
	Spam   bool
	Hits   []spammodel.Feature
}

func massCheckSamples(samples []sample) ([]hitSample, error) {
	result := make([]hitSample, len(samples))
	for index, item := range samples {
		hits, err := spammodel.ExtractFeatures(item.Message, spammodel.DefaultDimension)
		if err != nil {
			return nil, err
		}
		result[index] = hitSample{ID: item.ID, Source: item.Source, Spam: item.Spam, Hits: hits}
	}
	return result, nil
}

type RuleAudit struct {
	Index           uint32                 `json:"index"`
	Name            string                 `json:"name"`
	Description     string                 `json:"description"`
	Polarity        spammodel.RulePolarity `json:"polarity"`
	Count           bool                   `json:"count"`
	TrainHits       int                    `json:"train_messages_hit"`
	TrainSpamHits   int                    `json:"train_spam_messages_hit"`
	TrainHamHits    int                    `json:"train_ham_messages_hit"`
	TotalHitValue   float64                `json:"total_hit_value"`
	SpamHitRate     float64                `json:"spam_hit_rate"`
	HamHitRate      float64                `json:"ham_hit_rate"`
	SpamOverOverall float64                `json:"spam_over_overall"`
	Discrimination  float64                `json:"discrimination"`
	RangeLow        float64                `json:"range_low"`
	RangeHigh       float64                `json:"range_high"`
	Enabled         bool                   `json:"enabled"`
	DisabledReason  string                 `json:"disabled_reason,omitempty"`
	FittedScore     float64                `json:"fitted_score"`
}

type scoreRange struct {
	low  float64
	high float64
}

func auditRuleHits(samples []hitSample) ([]RuleAudit, []scoreRange, error) {
	definitions := spammodel.RuleDefinitions()
	if len(definitions) == 0 || len(definitions) > 128 {
		return nil, nil, errors.New("named-rule manifest must contain between 1 and 128 rules")
	}
	audits := make([]RuleAudit, len(definitions))
	ranges := make([]scoreRange, spammodel.DefaultDimension)
	spamMessages, hamMessages := 0, 0
	for _, item := range samples {
		if item.Spam {
			spamMessages++
		} else {
			hamMessages++
		}
		seen := make(map[uint32]bool, len(item.Hits))
		for _, hit := range item.Hits {
			if int(hit.Index) >= len(definitions) || hit.Name != definitions[hit.Index].Name || hit.Value <= 0 {
				return nil, nil, errors.New("mass-check produced an unknown or invalid rule hit")
			}
			audit := &audits[hit.Index]
			audit.TotalHitValue += hit.Value
			if seen[hit.Index] {
				continue
			}
			seen[hit.Index] = true
			audit.TrainHits++
			if item.Spam {
				audit.TrainSpamHits++
			} else {
				audit.TrainHamHits++
			}
		}
	}
	if spamMessages == 0 || hamMessages == 0 {
		return nil, nil, errors.New("rule audit requires both spam and ham")
	}
	for index, definition := range definitions {
		audit := &audits[index]
		audit.Index = definition.Index
		audit.Name = definition.Name
		audit.Description = definition.Description
		audit.Polarity = definition.Polarity
		audit.Count = definition.Count
		audit.SpamHitRate = float64(audit.TrainSpamHits) / float64(spamMessages)
		audit.HamHitRate = float64(audit.TrainHamHits) / float64(hamMessages)
		denominator := audit.SpamHitRate + audit.HamHitRate
		if denominator > 0 {
			audit.SpamOverOverall = audit.SpamHitRate / denominator
		}
		audit.Discrimination = math.Abs(audit.SpamHitRate - audit.HamHitRate)
		audit.Enabled = true
		switch {
		case audit.TrainHits < minRuleSupport:
			audit.Enabled = false
			audit.DisabledReason = "support below five training messages"
		case definition.Polarity == spammodel.SpamRule && audit.SpamHitRate <= audit.HamHitRate:
			audit.Enabled = false
			audit.DisabledReason = "observed hit rates contradict spam polarity"
		case definition.Polarity == spammodel.HamRule && audit.HamHitRate <= audit.SpamHitRate:
			audit.Enabled = false
			audit.DisabledReason = "observed hit rates contradict ham polarity"
		}
		if audit.Enabled {
			if definition.Polarity == spammodel.SpamRule {
				audit.RangeHigh = definition.MaxScore
			} else {
				audit.RangeLow = -definition.MaxScore
			}
			ranges[index] = scoreRange{low: audit.RangeLow, high: audit.RangeHigh}
		}
	}
	return audits, ranges, nil
}

func trainPerceptron(samples []hitSample, ranges []scoreRange, config PerceptronConfig) ([]float32, float64, error) {
	if len(samples) == 0 || len(ranges) != int(spammodel.DefaultDimension) {
		return nil, 0, errors.New("perceptron requires mass-check rows and complete score ranges")
	}
	if config.Epochs <= 0 || config.LearningRate <= 0 || config.HamPreference < 0 || config.WeightDecay <= 0 || config.WeightDecay > 1 {
		return nil, 0, errors.New("invalid perceptron configuration")
	}
	weights := make([]float64, spammodel.DefaultDimension)
	bias := -0.5
	order := make([]int, len(samples))
	for index := range order {
		order[index] = index
	}
	random := rand.New(rand.NewSource(TrainingSeed))
	for epoch := 0; epoch < config.Epochs; epoch++ {
		bias *= config.WeightDecay
		for index := range weights {
			weights[index] *= config.WeightDecay
		}
		random.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for _, sampleIndex := range order {
			item := samples[sampleIndex]
			hitMass := 0.0
			for _, hit := range item.Hits {
				hitMass += hit.Value
			}
			repetitions := 1
			if !item.Spam {
				repetitions += int(math.Min(32, hitMass*config.HamPreference))
			}
			for repetition := 0; repetition < repetitions; repetition++ {
				activation := bias
				for _, hit := range item.Hits {
					activation += weights[hit.Index] * hit.Value
				}
				prediction := logistic(activation)
				target := 0.0
				if item.Spam {
					target = 1
				}
				delta := config.LearningRate * prediction * (1 - prediction) * (target - prediction) / (hitMass + 1)
				bias += delta
				for _, hit := range item.Hits {
					index := int(hit.Index)
					weights[index] += delta * hit.Value
					if weights[index] < ranges[index].low {
						weights[index] = ranges[index].low
					}
					if weights[index] > ranges[index].high {
						weights[index] = ranges[index].high
					}
				}
			}
		}
	}
	result := make([]float32, len(weights))
	for index, value := range weights {
		result[index] = float32(value)
	}
	return result, bias, nil
}

func fitCalibration(weights []float32, bias float64, validation []hitSample) (float64, float64, error) {
	if len(validation) == 0 {
		return 0, 0, errors.New("validation set is empty")
	}
	logits := make([]float64, len(validation))
	targets := make([]float64, len(validation))
	for index, item := range validation {
		logit := bias
		for _, hit := range item.Hits {
			logit += float64(weights[hit.Index]) * hit.Value
		}
		logits[index] = logit
		if item.Spam {
			targets[index] = 1
		}
	}
	a, b := 1.0, 0.0
	for iteration := 0; iteration < 1000; iteration++ {
		gradientA, gradientB := 0.0, 0.0
		for index, logit := range logits {
			errorValue := logistic(a*logit+b) - targets[index]
			gradientA += errorValue * logit
			gradientB += errorValue
		}
		rate := 0.08 / math.Sqrt(float64(iteration+1))
		a -= rate * gradientA / float64(len(logits))
		b -= rate * gradientB / float64(len(logits))
		a = math.Max(0.05, math.Min(8, a))
		b = math.Max(-12, math.Min(12, b))
	}
	return a, b, nil
}

func logistic(value float64) float64 {
	if value >= 0 {
		exponential := math.Exp(-value)
		return 1 / (1 + exponential)
	}
	exponential := math.Exp(value)
	return exponential / (1 + exponential)
}
