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
	"strings"
	"time"

	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

const (
	MetadataSchemaVersion = "rolltop-spam-scorecard-metadata-v2"
	ReportSchemaVersion   = "rolltop-spam-scorecard-report-v2"
	ModelName             = "Rolltop named-rule scorecard"
	TrainingCorpusName    = "Apache SpamAssassin public corpus used for Rolltop rule mass-check and score fitting; not a SpamAssassin model"
	maxModelBytes         = 32 << 10
	maxInferenceNanos     = 10_000_000
	maxAllocatedBytes     = 1 << 20
	maximumValidationFPR  = 0.02
	minimumHeldOutRecall  = 0.60
	maximumHeldOutFPR     = 0.03
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
	Perceptron           PerceptronConfig `json:"perceptron"`
	MinimumRuleSupport   int              `json:"minimum_rule_support"`
	MaximumValidationFPR float64          `json:"maximum_validation_false_positive_rate"`
	Calibration          string           `json:"calibration"`
}

type GoldenInference struct {
	Name               string            `json:"name"`
	Message            spammodel.Message `json:"message"`
	Probability        float64           `json:"probability"`
	MinimumProbability float64           `json:"minimum_probability"`
	MaximumProbability float64           `json:"maximum_probability"`
	Tolerance          float64           `json:"tolerance"`
}

type Metadata struct {
	SchemaVersion       string            `json:"schema_version"`
	ModelVersion        string            `json:"model_version"`
	ModelName           string            `json:"model_name"`
	TrainingCorpus      string            `json:"training_corpus"`
	BinaryFormatVersion uint16            `json:"binary_format_version"`
	FeatureSchema       string            `json:"feature_schema"`
	Dimension           uint32            `json:"dimension"`
	RuleCount           int               `json:"rule_count"`
	RuleManifestSHA256  string            `json:"rule_manifest_sha256"`
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

type runtimeSmokeResult struct {
	messages                 int
	inferenceNanosecondsEach int64
	allocatedBytesEach       int64
}

type QualityGate struct {
	Name   string  `json:"name"`
	Value  float64 `json:"value"`
	Limit  float64 `json:"limit"`
	Passed bool    `json:"passed"`
}

type DisplayThresholds struct {
	Medium         float64              `json:"medium"`
	High           float64              `json:"high"`
	Note           string               `json:"note"`
	MediumOrHigher ThresholdPerformance `json:"medium_or_higher_held_out"`
	HighRisk       ThresholdPerformance `json:"high_risk_held_out"`
}

type ThresholdPerformance struct {
	Threshold         float64 `json:"threshold"`
	Recall            float64 `json:"recall"`
	FalsePositiveRate float64 `json:"false_positive_rate"`
	TruePositives     int     `json:"true_positives"`
	FalsePositives    int     `json:"false_positives"`
	Spam              int     `json:"spam"`
	Ham               int     `json:"ham"`
}

type BenchmarkReport struct {
	SchemaVersion      string            `json:"schema_version"`
	ModelName          string            `json:"model_name"`
	Method             string            `json:"method"`
	CorpusUse          string            `json:"corpus_use"`
	Limitations        []string          `json:"limitations"`
	ModelSHA256        string            `json:"model_sha256"`
	RuleManifestSHA256 string            `json:"rule_manifest_sha256"`
	ModelBytes         int               `json:"model_bytes"`
	Counts             DataCounts        `json:"counts"`
	HeldOut            Metrics           `json:"held_out_metrics"`
	OperatingPoint     OperatingPoint    `json:"validation_selected_operating_point"`
	DisplayThresholds  DisplayThresholds `json:"runtime_display_thresholds"`
	RuleAudit          []RuleAudit       `json:"rule_frequency_audit"`
	QualityGates       []QualityGate     `json:"quality_gates"`
}

func Train(cacheDir string, paths ArtifactPaths, output io.Writer) error {
	fmt.Fprintln(output, "loading and deduplicating corpus")
	samples, parseSkipped, err := loadCorpus(cacheDir)
	if err != nil {
		return err
	}
	splits := splitSamples(samples)
	counts := countData(samples, splits, parseSkipped)
	if counts.Spam == 0 || counts.Ham == 0 || len(splits.Train) == 0 || len(splits.Validation) == 0 || len(splits.Test) == 0 {
		return errors.New("corpus did not produce usable train/validation/test splits")
	}
	if parseSkipped > 0 {
		fmt.Fprintf(output, "skipped %d messages rejected by the production MIME parser\n", parseSkipped)
	}
	fmt.Fprintf(output, "mass-checking %d messages against %d named rules\n", len(samples), spammodel.RuleCount())
	trainHits, err := massCheckSamples(splits.Train)
	if err != nil {
		return err
	}
	validationHits, err := massCheckSamples(splits.Validation)
	if err != nil {
		return err
	}
	audits, ranges, err := auditRuleHits(trainHits)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "fitting bounded perceptron on %d rule-hit vectors\n", len(trainHits))
	weights, bias, err := trainPerceptron(trainHits, ranges, DefaultPerceptronConfig)
	if err != nil {
		return err
	}
	calibrationA, calibrationB, err := fitCalibration(weights, bias, validationHits)
	if err != nil {
		return err
	}
	classifier, err := spammodel.New(weights, bias, calibrationA, calibrationB, "training")
	if err != nil {
		return err
	}
	validationProbabilities, validationLabels, err := scoreClassifier(classifier, splits.Validation)
	if err != nil {
		return err
	}
	testProbabilities, testLabels, err := scoreClassifier(classifier, splits.Test)
	if err != nil {
		return err
	}
	operatingPoint, err := validationSelectedOperatingPoint(
		validationProbabilities, validationLabels, testProbabilities, testLabels, maximumValidationFPR,
	)
	if err != nil {
		return err
	}
	mediumDisplay, err := thresholdPerformance(testProbabilities, testLabels, spammodel.DefaultMediumThreshold)
	if err != nil {
		return err
	}
	highDisplay, err := thresholdPerformance(testProbabilities, testLabels, spammodel.DefaultHighThreshold)
	if err != nil {
		return err
	}
	if err := classifier.SetThresholds(spammodel.DefaultMediumThreshold, spammodel.DefaultHighThreshold); err != nil {
		return err
	}
	heldOutMetrics, err := calculateMetrics(testProbabilities, testLabels)
	if err != nil {
		return err
	}
	for index := range audits {
		audits[index].FittedScore = float64(weights[index])
	}
	ruleManifestSHA, err := ruleManifestDigest()
	if err != nil {
		return err
	}
	modelData, err := classifier.MarshalBinary()
	if err != nil {
		return err
	}
	modelDigest := sha256.Sum256(modelData)
	modelSHA := hex.EncodeToString(modelDigest[:])
	modelVersion := "rolltop-rule-scorecard-v1-" + modelSHA[:12]
	classifier, err = spammodel.New(weights, bias, calibrationA, calibrationB, modelVersion)
	if err != nil {
		return err
	}
	if err := classifier.SetThresholds(spammodel.DefaultMediumThreshold, spammodel.DefaultHighThreshold); err != nil {
		return err
	}
	runtimeSmoke, err := benchmarkRuntime(classifier, splits.Test)
	if err != nil {
		return err
	}
	if runtimeSmoke.inferenceNanosecondsEach > maxInferenceNanos || runtimeSmoke.allocatedBytesEach > maxAllocatedBytes {
		return fmt.Errorf("local runtime smoke check failed: %d ns/message, %d allocated bytes/message",
			runtimeSmoke.inferenceNanosecondsEach, runtimeSmoke.allocatedBytesEach)
	}
	fmt.Fprintf(output, "local runtime smoke (%d messages; not serialized): %d ns/message, %d allocated bytes/message\n",
		runtimeSmoke.messages, runtimeSmoke.inferenceNanosecondsEach, runtimeSmoke.allocatedBytesEach)
	goldens, behaviorGates, err := evaluateGoldenMessages(classifier, operatingPoint.Threshold)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "validation threshold %.6f: held-out recall %.3f, false-positive rate %.3f\n",
		operatingPoint.Threshold, operatingPoint.TestRecall, operatingPoint.TestFalsePositiveRate)
	for _, golden := range goldens {
		fmt.Fprintf(output, "golden %s: %.6f\n", golden.Name, golden.Probability)
	}
	enabledRules := 0
	for _, audit := range audits {
		if audit.Enabled {
			enabledRules++
		}
	}
	gates := []QualityGate{
		{Name: "held_out_recall", Value: operatingPoint.TestRecall, Limit: minimumHeldOutRecall, Passed: operatingPoint.TestRecall >= minimumHeldOutRecall},
		{Name: "held_out_false_positive_rate", Value: operatingPoint.TestFalsePositiveRate, Limit: maximumHeldOutFPR, Passed: operatingPoint.TestFalsePositiveRate <= maximumHeldOutFPR},
		{Name: "validation_false_positive_rate", Value: operatingPoint.ValidationFalsePositiveRate, Limit: maximumValidationFPR, Passed: operatingPoint.ValidationFalsePositiveRate <= maximumValidationFPR},
		{Name: "named_rule_count", Value: float64(spammodel.RuleCount()), Limit: 128, Passed: spammodel.RuleCount() <= 128},
		{Name: "enabled_rule_count", Value: float64(enabledRules), Limit: 12, Passed: enabledRules >= 12},
		{Name: "model_size_bytes", Value: float64(len(modelData)), Limit: maxModelBytes, Passed: len(modelData) <= maxModelBytes},
	}
	gates = append(gates, behaviorGates...)
	for _, gate := range gates {
		if !gate.Passed {
			return fmt.Errorf("quality gate %q failed: value %.8f, limit %.8f", gate.Name, gate.Value, gate.Limit)
		}
	}
	report := BenchmarkReport{
		SchemaVersion: ReportSchemaVersion,
		ModelName:     ModelName,
		Method:        "Deterministic bounded sigmoid perceptron fitted to sparse hits from a fixed authored rule table",
		CorpusUse:     "The public corpus labels are used only to mass-check Rolltop rules and fit their scores; this artifact is not Apache SpamAssassin or a SpamAssassin model.",
		Limitations: []string{
			"The 2002-2005 public corpus is a historical regression dataset and is not representative of all modern or tenant-specific mail.",
			"Held-out corpus metrics are regression measurements, not a guarantee of production spam or ham error rates.",
			"Rules and fitted scores are Rolltop-specific and are not compatible with Apache SpamAssassin.",
		},
		ModelSHA256: modelSHA, RuleManifestSHA256: ruleManifestSHA, ModelBytes: len(modelData),
		Counts: counts, HeldOut: heldOutMetrics, OperatingPoint: operatingPoint,
		DisplayThresholds: DisplayThresholds{
			Medium: spammodel.DefaultMediumThreshold, High: spammodel.DefaultHighThreshold,
			Note:           "Fixed conservative UI bands; distinct from the validation-selected benchmark operating threshold.",
			MediumOrHigher: mediumDisplay, HighRisk: highDisplay,
		},
		RuleAudit: audits, QualityGates: gates,
	}
	reportData, err := marshalIndented(report)
	if err != nil {
		return err
	}
	reportDigest := sha256.Sum256(reportData)
	metadata := Metadata{
		SchemaVersion: MetadataSchemaVersion, ModelVersion: modelVersion, ModelName: ModelName,
		TrainingCorpus: TrainingCorpusName, BinaryFormatVersion: spammodel.BinaryFormatVersion,
		FeatureSchema: spammodel.FeatureSchema, Dimension: spammodel.DefaultDimension,
		RuleCount: spammodel.RuleCount(), RuleManifestSHA256: ruleManifestSHA,
		TrainerVersion: TrainerVersion, Seed: TrainingSeed, ModelSHA256: modelSHA,
		ReportSHA256: hex.EncodeToString(reportDigest[:]), MediumThreshold: spammodel.DefaultMediumThreshold,
		HighThreshold: spammodel.DefaultHighThreshold, Corpus: append([]CorpusSpec(nil), CorpusSpecs...), Counts: counts,
		Hyperparameters: Hyperparameters{
			Perceptron: DefaultPerceptronConfig, MinimumRuleSupport: minRuleSupport,
			MaximumValidationFPR: maximumValidationFPR, Calibration: "two-parameter sigmoid fit on validation rule scores",
		},
		Goldens: goldens,
	}
	metadataData, err := marshalIndented(metadata)
	if err != nil {
		return err
	}
	if err := writeArtifactSet(paths, modelData, metadataData, reportData); err != nil {
		return err
	}
	fmt.Fprintf(output, "wrote %s (%s)\n", paths.Model, modelVersion)
	fmt.Fprintf(output, "held-out recall %.3f, false-positive rate %.3f at validation threshold %.6f\n",
		operatingPoint.TestRecall, operatingPoint.TestFalsePositiveRate, operatingPoint.Threshold)
	return nil
}

func scoreClassifier(classifier *spammodel.Classifier, samples []sample) ([]float64, []bool, error) {
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

func thresholdPerformance(probabilities []float64, labels []bool, threshold float64) (ThresholdPerformance, error) {
	recall, falsePositiveRate, truePositives, falsePositives, spam, ham, err := evaluateThreshold(probabilities, labels, threshold)
	if err != nil {
		return ThresholdPerformance{}, err
	}
	return ThresholdPerformance{
		Threshold: threshold, Recall: recall, FalsePositiveRate: falsePositiveRate,
		TruePositives: truePositives, FalsePositives: falsePositives, Spam: spam, Ham: ham,
	}, nil
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

func benchmarkRuntime(classifier *spammodel.Classifier, samples []sample) (runtimeSmokeResult, error) {
	count := len(samples)
	if count > 100 {
		count = 100
	}
	if count == 0 {
		return runtimeSmokeResult{}, errors.New("cannot benchmark an empty test set")
	}
	for index := 0; index < count; index++ {
		if _, err := classifier.Classify(samples[index].Message); err != nil {
			return runtimeSmokeResult{}, err
		}
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	for index := 0; index < count; index++ {
		if _, err := classifier.Classify(samples[index].Message); err != nil {
			return runtimeSmokeResult{}, err
		}
	}
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	return runtimeSmokeResult{
		messages: count, inferenceNanosecondsEach: elapsed.Nanoseconds() / int64(count),
		allocatedBytesEach: int64(after.TotalAlloc-before.TotalAlloc) / int64(count),
	}, nil
}

func goldenDefinitions() []GoldenInference {
	return []GoldenInference{
		{Name: "obvious_spam", Message: spammodel.Message{Subject: "URGENT: claim your FREE cash prize now", Body: "Congratulations winner! Click https://cash-prizes.invalid/claim to receive prize money today. Act now; this limited time offer expires today.", From: "offers@bulk-mail.invalid", To: []string{"customer@example.com"}, MIMEType: "text/html", HTML: true}, MinimumProbability: .60, MaximumProbability: 1},
		{Name: "ordinary_ham", Message: spammodel.Message{Subject: "Notes from Tuesday's engineering meeting", Body: "Hi team, attached are the action items. Please review the database migration before Friday's meeting. Best regards, Alex", From: "alex@example.org", To: []string{"team@example.org"}, MIMEType: "text/plain"}, MaximumProbability: .40},
		{Name: "movie_move_ham", Message: spammodel.Message{Subject: "Movie screening moved to Friday", Body: "The community movie screening moved to Friday at 7 PM. Please review the room change before the meeting.", From: "events@example.org", To: []string{"team@example.org"}, MIMEType: "text/plain"}, MaximumProbability: .40},
		asWeMoveGolden("aswemove_1_ham", 1), asWeMoveGolden("aswemove_8_ham", 8),
		asWeMoveGolden("aswemove_32_ham", 32), asWeMoveGolden("aswemove_64_ham", 64),
	}
}

func asWeMoveGolden(name string, repetitions int) GoldenInference {
	return GoldenInference{Name: name, Message: spammodel.Message{
		Subject: "AsWeMove member update",
		Body:    strings.Repeat("AsWeMove ", repetitions) + "Thank you for being a member. Your order is being prepared and no action is required. View your receipt at https://account.example.org/orders/123 or manage preferences at https://account.example.org/preferences.",
		From:    "care@account.example.org", To: []string{"member@example.org"}, MIMEType: "text/html", HTML: true,
	}, MaximumProbability: .45}
}

func evaluateGoldenMessages(classifier *spammodel.Classifier, spamThreshold float64) ([]GoldenInference, []QualityGate, error) {
	definitions := goldenDefinitions()
	gates := make([]QualityGate, 0, len(definitions)+4)
	minimumAsWeMove, maximumAsWeMove := 1.0, 0.0
	for index := range definitions {
		score, err := classifier.Classify(definitions[index].Message)
		if err != nil {
			return nil, nil, err
		}
		definitions[index].Probability = score.Probability
		definitions[index].Tolerance = 1e-8
		if definitions[index].Name == "obvious_spam" {
			definitions[index].MinimumProbability = math.Max(definitions[index].MinimumProbability, spamThreshold)
		}
		if definitions[index].MinimumProbability > 0 {
			gates = append(gates, QualityGate{Name: "behavior_min_" + definitions[index].Name, Value: score.Probability, Limit: definitions[index].MinimumProbability, Passed: score.Probability >= definitions[index].MinimumProbability})
		}
		if definitions[index].MaximumProbability < 1 {
			gates = append(gates, QualityGate{Name: "behavior_max_" + definitions[index].Name, Value: score.Probability, Limit: definitions[index].MaximumProbability, Passed: score.Probability <= definitions[index].MaximumProbability})
		}
		if strings.HasPrefix(definitions[index].Name, "aswemove_") {
			minimumAsWeMove = math.Min(minimumAsWeMove, score.Probability)
			maximumAsWeMove = math.Max(maximumAsWeMove, score.Probability)
		}
	}
	gates = append(gates, QualityGate{Name: "behavior_aswemove_repetition_range", Value: maximumAsWeMove - minimumAsWeMove, Limit: .03, Passed: maximumAsWeMove-minimumAsWeMove <= .03})
	gates = append(gates,
		QualityGate{Name: "behavior_aswemove_below_spam_threshold", Value: maximumAsWeMove, Limit: spamThreshold, Passed: maximumAsWeMove < spamThreshold},
		QualityGate{Name: "behavior_movie_below_spam_threshold", Value: definitions[2].Probability, Limit: spamThreshold, Passed: definitions[2].Probability < spamThreshold},
	)
	return definitions, gates, nil
}

func ruleManifestDigest() (string, error) {
	data, err := json.Marshal(spammodel.RuleDefinitions())
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
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
	modelNext, metadataNext, reportNext := paths.Model+".next", paths.Metadata+".next", paths.Report+".next"
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
