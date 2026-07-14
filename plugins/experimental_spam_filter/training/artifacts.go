package training

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"time"

	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

const (
	MetadataSchemaVersion = "rolltop-spam-model-metadata-v1"
	ReportSchemaVersion   = "rolltop-spam-benchmark-v1"
	maxModelBytes         = 2 << 20
	maxInferenceNanos     = 50_000_000
	maxAllocatedBytes     = 8 << 20
)

type ArtifactPaths struct {
	Model    string
	Metadata string
	Report   string
}

type DataCounts struct {
	Total          int `json:"total"`
	ParseSkipped   int `json:"parse_skipped"`
	Spam           int `json:"spam"`
	Ham            int `json:"ham"`
	Train          int `json:"train"`
	Validation     int `json:"validation"`
	Test           int `json:"test"`
	TrainSpam      int `json:"train_spam"`
	ValidationSpam int `json:"validation_spam"`
	TestSpam       int `json:"test_spam"`
}

type Hyperparameters struct {
	Dimension uint32  `json:"dimension"`
	Epochs    int     `json:"epochs"`
	Alpha     float64 `json:"alpha"`
	Beta      float64 `json:"beta"`
	L1        float64 `json:"l1"`
	L2        float64 `json:"l2"`
}

type GoldenInference struct {
	Name        string            `json:"name"`
	Message     spammodel.Message `json:"message"`
	Probability float64           `json:"probability"`
	Tolerance   float64           `json:"tolerance"`
}

type Metadata struct {
	SchemaVersion       string            `json:"schema_version"`
	ModelVersion        string            `json:"model_version"`
	BinaryFormatVersion uint16            `json:"binary_format_version"`
	FeatureSchema       string            `json:"feature_schema"`
	Dimension           uint32            `json:"dimension"`
	TrainerVersion      string            `json:"trainer_version"`
	Seed                int64             `json:"seed"`
	ModelSHA256         string            `json:"model_sha256"`
	ReportSHA256        string            `json:"report_sha256"`
	MediumThreshold     float64           `json:"medium_threshold"`
	HighThreshold       float64           `json:"high_threshold"`
	Corpus              []CorpusSpec      `json:"corpus"`
	Counts              DataCounts        `json:"counts"`
	Hyperparameters     Hyperparameters   `json:"hyperparameters"`
	Goldens             []GoldenInference `json:"golden_inference"`
}

type ConfidenceInterval struct {
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
}

type RuntimeBenchmark struct {
	Messages                 int   `json:"messages"`
	InferenceNanosecondsEach int64 `json:"inference_nanoseconds_per_message"`
	AllocatedBytesEach       int64 `json:"allocated_bytes_per_message"`
	ModelBytes               int   `json:"model_bytes"`
}

type QualityGate struct {
	Name   string  `json:"name"`
	Value  float64 `json:"value"`
	Limit  float64 `json:"limit"`
	Passed bool    `json:"passed"`
}

type BenchmarkReport struct {
	SchemaVersion     string             `json:"schema_version"`
	ModelSHA256       string             `json:"model_sha256"`
	Counts            DataCounts         `json:"counts"`
	Logistic          Metrics            `json:"logistic"`
	NaiveBayes        Metrics            `json:"naive_bayes"`
	NaiveBayesRecipe  string             `json:"naive_bayes_recipe"`
	FPRDifference95CI ConfidenceInterval `json:"fpr_difference_95_ci"`
	Runtime           RuntimeBenchmark   `json:"runtime"`
	QualityGates      []QualityGate      `json:"quality_gates"`
}

func Train(cacheDir string, paths ArtifactPaths, output io.Writer) error {
	fmt.Fprintln(output, "loading and deduplicating corpus")
	samples, parseSkipped, err := loadCorpus(cacheDir)
	if err != nil {
		return err
	}
	splits := splitSamples(samples)
	counts := countData(samples, splits, parseSkipped)
	if parseSkipped > 0 {
		fmt.Fprintf(output, "skipped %d corpus messages rejected by the production MIME parser\n", parseSkipped)
	}
	if counts.Spam == 0 || counts.Ham == 0 || len(splits.Validation) == 0 || len(splits.Test) == 0 {
		return errors.New("corpus did not produce usable train/validation/test splits")
	}
	fmt.Fprintf(output, "training FTRL logistic model on %d messages\n", len(splits.Train))
	weights, bias, err := trainLogistic(splits.Train, spammodel.DefaultDimension)
	if err != nil {
		return err
	}
	uncalibrated, err := spammodel.New(weights, bias, 1, 0, "training")
	if err != nil {
		return err
	}
	calibrationA, calibrationB, err := fitCalibration(uncalibrated, splits.Validation)
	if err != nil {
		return err
	}
	classifier, err := spammodel.New(weights, bias, calibrationA, calibrationB, "training")
	if err != nil {
		return err
	}
	fmt.Fprintln(output, "training multinomial naive Bayes baseline")
	bayes, err := trainNaiveBayes(splits.Train, spammodel.DefaultDimension)
	if err != nil {
		return err
	}

	logisticProbabilities, labels, err := scoreLogistic(classifier, splits.Test)
	if err != nil {
		return err
	}
	bayesProbabilities, bayesRankings, err := scoreBayes(bayes, splits.Test, spammodel.DefaultDimension)
	if err != nil {
		return err
	}
	logisticMetrics, err := calculateMetrics(logisticProbabilities, labels)
	if err != nil {
		return err
	}
	bayesMetrics, err := calculateMetricsWithRanking(bayesProbabilities, bayesRankings, labels)
	if err != nil {
		return err
	}
	ciLower, ciUpper, err := bootstrapFPRDifference(
		logisticProbabilities, logisticProbabilities, bayesProbabilities, bayesRankings, labels, 400,
	)
	if err != nil {
		return err
	}

	modelData, err := classifier.MarshalBinary()
	if err != nil {
		return err
	}
	modelDigest := sha256.Sum256(modelData)
	modelSHA := hex.EncodeToString(modelDigest[:])
	modelVersion := "spamassassin-20030228-" + modelSHA[:12]
	classifier, err = spammodel.New(weights, bias, calibrationA, calibrationB, modelVersion)
	if err != nil {
		return err
	}
	runtimeBenchmark, err := benchmarkRuntime(classifier, splits.Test, len(modelData))
	if err != nil {
		return err
	}
	gates := []QualityGate{
		{Name: "pr_auc_not_below_naive_bayes", Value: logisticMetrics.PRAUC - bayesMetrics.PRAUC, Limit: 0, Passed: logisticMetrics.PRAUC >= bayesMetrics.PRAUC},
		{Name: "fpr_difference_95ci_upper_below_zero", Value: ciUpper, Limit: 0, Passed: ciUpper < 0},
		{Name: "model_size_bytes", Value: float64(runtimeBenchmark.ModelBytes), Limit: maxModelBytes, Passed: runtimeBenchmark.ModelBytes <= maxModelBytes},
		{Name: "inference_nanoseconds_per_message", Value: float64(runtimeBenchmark.InferenceNanosecondsEach), Limit: maxInferenceNanos, Passed: runtimeBenchmark.InferenceNanosecondsEach <= maxInferenceNanos},
		{Name: "allocated_bytes_per_message", Value: float64(runtimeBenchmark.AllocatedBytesEach), Limit: maxAllocatedBytes, Passed: runtimeBenchmark.AllocatedBytesEach <= maxAllocatedBytes},
	}
	for _, gate := range gates {
		if !gate.Passed {
			return fmt.Errorf("quality gate %q failed: value %.8f, limit %.8f", gate.Name, gate.Value, gate.Limit)
		}
	}
	report := BenchmarkReport{
		SchemaVersion:     ReportSchemaVersion,
		ModelSHA256:       modelSHA,
		Counts:            counts,
		Logistic:          logisticMetrics,
		NaiveBayes:        bayesMetrics,
		NaiveBayesRecipe:  NaiveBayesRecipe,
		FPRDifference95CI: ConfidenceInterval{Lower: ciLower, Upper: ciUpper},
		Runtime:           runtimeBenchmark,
		QualityGates:      gates,
	}
	reportData, err := marshalIndented(report)
	if err != nil {
		return err
	}
	reportDigest := sha256.Sum256(reportData)
	metadata := Metadata{
		SchemaVersion:       MetadataSchemaVersion,
		ModelVersion:        modelVersion,
		BinaryFormatVersion: spammodel.BinaryFormatVersion,
		FeatureSchema:       spammodel.FeatureSchema,
		Dimension:           spammodel.DefaultDimension,
		TrainerVersion:      TrainerVersion,
		Seed:                TrainingSeed,
		ModelSHA256:         modelSHA,
		ReportSHA256:        hex.EncodeToString(reportDigest[:]),
		MediumThreshold:     0.35,
		HighThreshold:       0.80,
		Corpus:              append([]CorpusSpec(nil), CorpusSpecs...),
		Counts:              counts,
		Hyperparameters: Hyperparameters{
			Dimension: spammodel.DefaultDimension,
			Epochs:    trainingEpochs,
			Alpha:     0.08,
			Beta:      1,
			L1:        0.08,
			L2:        0.8,
		},
	}
	for _, golden := range fixedGoldenMessages() {
		score, err := classifier.Classify(golden.Message)
		if err != nil {
			return err
		}
		golden.Probability = score.Probability
		golden.Tolerance = 1e-9
		metadata.Goldens = append(metadata.Goldens, golden)
	}
	metadataData, err := marshalIndented(metadata)
	if err != nil {
		return err
	}
	if err := writeArtifactSet(paths, modelData, metadataData, reportData); err != nil {
		return err
	}
	fmt.Fprintf(output, "wrote %s (%s)\n", paths.Model, modelVersion)
	fmt.Fprintf(output, "PR-AUC logistic %.6f, naive Bayes %.6f; FPR@90%% %.6f vs %.6f\n",
		logisticMetrics.PRAUC, bayesMetrics.PRAUC, logisticMetrics.FalsePositiveRate, bayesMetrics.FalsePositiveRate)
	return nil
}

func scoreLogistic(classifier *spammodel.Classifier, samples []sample) ([]float64, []bool, error) {
	probabilities := make([]float64, len(samples))
	labels := make([]bool, len(samples))
	for index, item := range samples {
		score, err := classifier.Classify(item.Message)
		if err != nil {
			return nil, nil, err
		}
		probabilities[index] = score.Probability
		labels[index] = item.Spam
	}
	return probabilities, labels, nil
}

func scoreBayes(classifier *naiveBayes, samples []sample, dimension uint32) ([]float64, []float64, error) {
	probabilities := make([]float64, len(samples))
	rankings := make([]float64, len(samples))
	for index, item := range samples {
		features, err := spammodel.ExtractCountFeatures(item.Message, dimension)
		if err != nil {
			return nil, nil, err
		}
		probabilities[index], rankings[index] = classifier.predict(features)
	}
	return probabilities, rankings, nil
}

func countData(samples []sample, splits splitSet, parseSkipped int) DataCounts {
	result := DataCounts{Total: len(samples), ParseSkipped: parseSkipped, Train: len(splits.Train), Validation: len(splits.Validation), Test: len(splits.Test)}
	for _, item := range samples {
		if item.Spam {
			result.Spam++
		} else {
			result.Ham++
		}
	}
	for _, item := range splits.Train {
		if item.Spam {
			result.TrainSpam++
		}
	}
	for _, item := range splits.Validation {
		if item.Spam {
			result.ValidationSpam++
		}
	}
	for _, item := range splits.Test {
		if item.Spam {
			result.TestSpam++
		}
	}
	return result
}

func benchmarkRuntime(classifier *spammodel.Classifier, samples []sample, modelBytes int) (RuntimeBenchmark, error) {
	count := len(samples)
	if count > 100 {
		count = 100
	}
	if count == 0 {
		return RuntimeBenchmark{}, errors.New("cannot benchmark an empty test set")
	}
	for index := 0; index < count; index++ {
		if _, err := classifier.Classify(samples[index].Message); err != nil {
			return RuntimeBenchmark{}, err
		}
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	for index := 0; index < count; index++ {
		if _, err := classifier.Classify(samples[index].Message); err != nil {
			return RuntimeBenchmark{}, err
		}
	}
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	return RuntimeBenchmark{
		Messages:                 count,
		InferenceNanosecondsEach: elapsed.Nanoseconds() / int64(count),
		AllocatedBytesEach:       int64(after.TotalAlloc-before.TotalAlloc) / int64(count),
		ModelBytes:               modelBytes,
	}, nil
}

func fixedGoldenMessages() []GoldenInference {
	return []GoldenInference{
		{
			Name: "obvious_spam",
			Message: spammodel.Message{
				Subject:  "URGENT: claim your FREE cash prize now",
				Body:     "Congratulations winner! Click https://cash-prizes.invalid/claim to receive money today. Limited time offer.",
				From:     "offers@bulk-mail.invalid",
				To:       []string{"customer@example.com"},
				MIMEType: "text/html",
				HTML:     true,
			},
		},
		{
			Name: "ordinary_ham",
			Message: spammodel.Message{
				Subject:  "Notes from Tuesday's engineering meeting",
				Body:     "Hi team, attached are the action items. Please review the database migration before Friday's meeting.",
				From:     "alex@example.org",
				To:       []string{"team@example.org"},
				MIMEType: "text/plain",
			},
		},
		{
			Name: "newsletter",
			Message: spammodel.Message{
				Subject:  "Your weekly project newsletter",
				Body:     "This week: release notes, community events, and documentation updates. Manage preferences at https://news.example.org/preferences",
				From:     "newsletter@news.example.org",
				To:       []string{"reader@example.com"},
				MIMEType: "text/html",
				HTML:     true,
			},
		},
	}
}

func marshalIndented(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func writeArtifactSet(paths ArtifactPaths, modelData, metadataData, reportData []byte) error {
	if paths.Model == "" || paths.Metadata == "" || paths.Report == "" {
		return errors.New("model, metadata, and report paths are required")
	}
	for _, path := range []string{paths.Model, paths.Metadata, paths.Report} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	}
	modelNext := paths.Model + ".next"
	metadataNext := paths.Metadata + ".next"
	reportNext := paths.Report + ".next"
	defer os.Remove(modelNext)
	defer os.Remove(metadataNext)
	defer os.Remove(reportNext)
	if err := atomicWrite(modelNext, modelData, 0o644); err != nil {
		return err
	}
	if err := atomicWrite(reportNext, reportData, 0o644); err != nil {
		return err
	}
	if err := atomicWrite(metadataNext, metadataData, 0o644); err != nil {
		return err
	}
	if err := os.Rename(modelNext, paths.Model); err != nil {
		return err
	}
	if err := os.Rename(reportNext, paths.Report); err != nil {
		return err
	}
	return os.Rename(metadataNext, paths.Metadata)
}

func finiteMetric(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}
