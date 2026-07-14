package training

import (
	"errors"
	"math"
	"math/rand"
	"sort"

	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

const (
	TrainerVersion   = "rolltop-ftrl-v2"
	TrainingSeed     = int64(20260713)
	trainingEpochs   = 4
	NaiveBayesRecipe = "multinomial Laplace(alpha=1), unsigned feature hashing, observed training vocabulary, same bounded feature families"
)

type splitSet struct {
	Train      []sample
	Validation []sample
	Test       []sample
}

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

type ftrl struct {
	alpha float64
	beta  float64
	l1    float64
	l2    float64
	z     []float64
	n     []float64
	biasZ float64
	biasN float64
}

func newFTRL(dimension uint32) *ftrl {
	return &ftrl{
		alpha: 0.08,
		beta:  1,
		l1:    0.08,
		l2:    0.8,
		z:     make([]float64, dimension),
		n:     make([]float64, dimension),
	}
}

func (learner *ftrl) weight(index uint32) float64 {
	z := learner.z[index]
	if math.Abs(z) <= learner.l1 {
		return 0
	}
	return -(z - math.Copysign(learner.l1, z)) /
		((learner.beta+math.Sqrt(learner.n[index]))/learner.alpha + learner.l2)
}

func (learner *ftrl) biasWeight() float64 {
	return -learner.biasZ / ((learner.beta+math.Sqrt(learner.biasN))/learner.alpha + learner.l2)
}

func (learner *ftrl) predict(features []spammodel.Feature) float64 {
	logit := learner.biasWeight()
	for _, feature := range features {
		logit += learner.weight(feature.Index) * feature.Value
	}
	return logistic(logit)
}

func (learner *ftrl) update(features []spammodel.Feature, target, classWeight float64) {
	prediction := learner.predict(features)
	baseGradient := (prediction - target) * classWeight
	for _, feature := range features {
		gradient := baseGradient * feature.Value
		weight := learner.weight(feature.Index)
		sigma := (math.Sqrt(learner.n[feature.Index]+gradient*gradient) - math.Sqrt(learner.n[feature.Index])) / learner.alpha
		learner.z[feature.Index] += gradient - sigma*weight
		learner.n[feature.Index] += gradient * gradient
	}
	biasWeight := learner.biasWeight()
	biasSigma := (math.Sqrt(learner.biasN+baseGradient*baseGradient) - math.Sqrt(learner.biasN)) / learner.alpha
	learner.biasZ += baseGradient - biasSigma*biasWeight
	learner.biasN += baseGradient * baseGradient
}

func trainLogistic(samples []sample, dimension uint32) ([]float32, float64, error) {
	if len(samples) == 0 {
		return nil, 0, errors.New("training set is empty")
	}
	spamCount := 0
	for _, item := range samples {
		if item.Spam {
			spamCount++
		}
	}
	hamCount := len(samples) - spamCount
	if spamCount == 0 || hamCount == 0 {
		return nil, 0, errors.New("training set must contain spam and ham")
	}
	spamWeight := float64(len(samples)) / (2 * float64(spamCount))
	hamWeight := float64(len(samples)) / (2 * float64(hamCount))
	learner := newFTRL(dimension)
	order := make([]int, len(samples))
	for index := range order {
		order[index] = index
	}
	random := rand.New(rand.NewSource(TrainingSeed))
	for epoch := 0; epoch < trainingEpochs; epoch++ {
		random.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for _, index := range order {
			item := samples[index]
			features, err := spammodel.ExtractFeatures(item.Message, dimension)
			if err != nil {
				return nil, 0, err
			}
			target := 0.0
			weight := hamWeight
			if item.Spam {
				target = 1
				weight = spamWeight
			}
			learner.update(features, target, weight)
		}
	}
	weights := make([]float32, dimension)
	for index := range weights {
		weights[index] = float32(learner.weight(uint32(index)))
	}
	return weights, learner.biasWeight(), nil
}

func fitCalibration(classifier *spammodel.Classifier, validation []sample) (float64, float64, error) {
	if len(validation) == 0 {
		return 1, 0, errors.New("validation set is empty")
	}
	logits := make([]float64, len(validation))
	targets := make([]float64, len(validation))
	for index, item := range validation {
		score, err := classifier.Classify(item.Message)
		if err != nil {
			return 0, 0, err
		}
		probability := math.Max(1e-9, math.Min(1-1e-9, score.Probability))
		logits[index] = math.Log(probability / (1 - probability))
		if item.Spam {
			targets[index] = 1
		}
	}
	a, b := 1.0, 0.0
	for iteration := 0; iteration < 800; iteration++ {
		var gradientA, gradientB float64
		for index, logit := range logits {
			errorValue := logistic(a*logit+b) - targets[index]
			gradientA += errorValue * logit
			gradientB += errorValue
		}
		rate := 0.08 / math.Sqrt(float64(iteration+1))
		a -= rate * gradientA / float64(len(logits))
		b -= rate * gradientB / float64(len(logits))
		a = math.Max(0.05, math.Min(5, a))
		b = math.Max(-8, math.Min(8, b))
	}
	return a, b, nil
}

type naiveBayes struct {
	spamCounts []float64
	hamCounts  []float64
	active     []bool
	vocabulary int
	spamTotal  float64
	hamTotal   float64
	spamDocs   int
	hamDocs    int
}

func trainNaiveBayes(samples []sample, dimension uint32) (*naiveBayes, error) {
	classifier := &naiveBayes{
		spamCounts: make([]float64, dimension),
		hamCounts:  make([]float64, dimension),
		active:     make([]bool, dimension),
	}
	for _, item := range samples {
		features, err := spammodel.ExtractCountFeatures(item.Message, dimension)
		if err != nil {
			return nil, err
		}
		if item.Spam {
			classifier.spamDocs++
		} else {
			classifier.hamDocs++
		}
		for _, feature := range features {
			value := feature.Value
			if !classifier.active[feature.Index] {
				classifier.active[feature.Index] = true
				classifier.vocabulary++
			}
			if item.Spam {
				classifier.spamCounts[feature.Index] += value
				classifier.spamTotal += value
			} else {
				classifier.hamCounts[feature.Index] += value
				classifier.hamTotal += value
			}
		}
	}
	if classifier.spamDocs == 0 || classifier.hamDocs == 0 || classifier.vocabulary == 0 {
		return nil, errors.New("naive Bayes requires spam and ham")
	}
	return classifier, nil
}

func (classifier *naiveBayes) predict(features []spammodel.Feature) (float64, float64) {
	vocabulary := float64(classifier.vocabulary)
	totalDocs := float64(classifier.spamDocs + classifier.hamDocs)
	spamLog := math.Log(float64(classifier.spamDocs) / totalDocs)
	hamLog := math.Log(float64(classifier.hamDocs) / totalDocs)
	for _, feature := range features {
		if !classifier.active[feature.Index] {
			continue
		}
		value := feature.Value
		spamLog += value * math.Log((classifier.spamCounts[feature.Index]+1)/(classifier.spamTotal+vocabulary))
		hamLog += value * math.Log((classifier.hamCounts[feature.Index]+1)/(classifier.hamTotal+vocabulary))
	}
	logOdds := spamLog - hamLog
	return logistic(logOdds), logOdds
}

func logistic(value float64) float64 {
	if value >= 0 {
		exponential := math.Exp(-value)
		return 1 / (1 + exponential)
	}
	exponential := math.Exp(value)
	return exponential / (1 + exponential)
}
