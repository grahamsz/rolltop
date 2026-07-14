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
	"reflect"
	"strings"

	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

// Verify is intentionally offline. It reads the checked-in artifacts, checks
// their provenance and rule manifest, and reruns only bounded golden inference.
func Verify(paths ArtifactPaths, output io.Writer) error {
	modelData, err := os.ReadFile(paths.Model)
	if err != nil {
		return err
	}
	metadataData, err := os.ReadFile(paths.Metadata)
	if err != nil {
		return err
	}
	reportData, err := os.ReadFile(paths.Report)
	if err != nil {
		return err
	}
	var metadata Metadata
	if err := json.Unmarshal(metadataData, &metadata); err != nil {
		return fmt.Errorf("decode metadata: %w", err)
	}
	var report BenchmarkReport
	if err := json.Unmarshal(reportData, &report); err != nil {
		return fmt.Errorf("decode benchmark report: %w", err)
	}
	if metadata.SchemaVersion != MetadataSchemaVersion || report.SchemaVersion != ReportSchemaVersion {
		return errors.New("artifact schema version is unsupported")
	}
	if metadata.BinaryFormatVersion != spammodel.BinaryFormatVersion || metadata.FeatureSchema != spammodel.FeatureSchema {
		return errors.New("model metadata is incompatible with runtime rules")
	}
	if metadata.ModelName != ModelName || metadata.TrainingCorpus != TrainingCorpusName || report.ModelName != ModelName {
		return errors.New("model identity or corpus description is not truthful")
	}
	if !strings.Contains(report.CorpusUse, "not Apache SpamAssassin") || !strings.Contains(report.Method, "authored rule table") {
		return errors.New("benchmark report does not describe the model boundary")
	}
	limitations := strings.Join(report.Limitations, " ")
	if len(report.Limitations) < 3 || !strings.Contains(limitations, "historical regression dataset") ||
		!strings.Contains(limitations, "not a guarantee") || !strings.Contains(limitations, "not compatible with Apache SpamAssassin") {
		return errors.New("benchmark report does not describe model limitations")
	}
	if metadata.Dimension != spammodel.DefaultDimension || metadata.RuleCount != spammodel.RuleCount() || metadata.RuleCount > 128 {
		return errors.New("named-rule dimensions are inconsistent")
	}
	manifestSHA, err := ruleManifestDigest()
	if err != nil {
		return err
	}
	if metadata.RuleManifestSHA256 != manifestSHA || report.RuleManifestSHA256 != manifestSHA {
		return errors.New("named-rule manifest checksum mismatch")
	}
	if metadata.TrainerVersion != TrainerVersion || metadata.Seed != TrainingSeed ||
		metadata.Hyperparameters.Perceptron != DefaultPerceptronConfig ||
		metadata.Hyperparameters.MinimumRuleSupport != minRuleSupport ||
		metadata.Hyperparameters.MaximumValidationFPR != maximumValidationFPR {
		return errors.New("model metadata trainer recipe mismatch")
	}
	if !sameCorpusSpecs(metadata.Corpus, CorpusSpecs) {
		return errors.New("model corpus provenance does not match pinned corpus")
	}
	modelDigest := sha256.Sum256(modelData)
	modelSHA := hex.EncodeToString(modelDigest[:])
	if modelSHA != metadata.ModelSHA256 || modelSHA != report.ModelSHA256 {
		return errors.New("model checksum mismatch")
	}
	reportDigest := sha256.Sum256(reportData)
	if hex.EncodeToString(reportDigest[:]) != metadata.ReportSHA256 {
		return errors.New("benchmark report checksum mismatch")
	}
	if report.Counts != metadata.Counts || metadata.Counts.Total != metadata.Counts.Spam+metadata.Counts.Ham ||
		metadata.Counts.Total != metadata.Counts.Train+metadata.Counts.Validation+metadata.Counts.Test {
		return errors.New("dataset counts are inconsistent")
	}
	if metadata.Counts.ParseSkipped < 0 || metadata.Counts.TrainSpam <= 0 || metadata.Counts.ValidationSpam <= 0 ||
		metadata.Counts.TestSpam <= 0 || metadata.Counts.TestSpam >= metadata.Counts.Test {
		return errors.New("dataset splits do not contain both labels")
	}
	if len(modelData) > maxModelBytes || report.ModelBytes != len(modelData) {
		return errors.New("model size benchmark is invalid")
	}
	if err := verifyMetrics(report.HeldOut); err != nil {
		return fmt.Errorf("held-out metrics: %w", err)
	}
	point := report.OperatingPoint
	if point.MaximumValidationFalsePositiveRate != maximumValidationFPR ||
		point.ValidationFalsePositiveRate > maximumValidationFPR || point.TestRecall < minimumHeldOutRecall ||
		point.TestFalsePositiveRate > maximumHeldOutFPR || point.TestTruePositives <= 0 || point.TestFalsePositives < 0 ||
		point.TestSpam != metadata.Counts.TestSpam || point.TestHam != metadata.Counts.Test-metadata.Counts.TestSpam {
		return errors.New("validation-selected operating point is invalid")
	}
	if metadata.MediumThreshold != spammodel.DefaultMediumThreshold || metadata.HighThreshold != spammodel.DefaultHighThreshold {
		return errors.New("scorecard thresholds are invalid")
	}
	if report.DisplayThresholds.Medium != metadata.MediumThreshold || report.DisplayThresholds.High != metadata.HighThreshold ||
		!strings.Contains(report.DisplayThresholds.Note, "distinct from") {
		return errors.New("runtime display thresholds are not distinguished from the benchmark operating point")
	}
	for name, performance := range map[string]ThresholdPerformance{
		"medium": report.DisplayThresholds.MediumOrHigher,
		"high":   report.DisplayThresholds.HighRisk,
	} {
		wantThreshold := metadata.MediumThreshold
		if name == "high" {
			wantThreshold = metadata.HighThreshold
		}
		if performance.Threshold != wantThreshold || !finiteMetric(performance.Recall) ||
			!finiteMetric(performance.FalsePositiveRate) || performance.TruePositives < 0 || performance.FalsePositives < 0 ||
			performance.Spam != metadata.Counts.TestSpam || performance.Ham != metadata.Counts.Test-metadata.Counts.TestSpam {
			return fmt.Errorf("%s display-threshold held-out performance is invalid", name)
		}
	}
	definitions := spammodel.RuleDefinitions()
	if len(report.RuleAudit) != len(definitions) {
		return errors.New("rule-frequency audit is incomplete")
	}
	for index, audit := range report.RuleAudit {
		definition := definitions[index]
		if audit.Index != definition.Index || audit.Name != definition.Name || audit.Description != definition.Description ||
			audit.Polarity != definition.Polarity || audit.Count != definition.Count || audit.TrainHits != audit.TrainSpamHits+audit.TrainHamHits {
			return fmt.Errorf("rule-frequency audit %d does not match the manifest", index)
		}
		if audit.FittedScore < audit.RangeLow-1e-6 || audit.FittedScore > audit.RangeHigh+1e-6 ||
			(definition.Polarity == spammodel.SpamRule && audit.FittedScore < -1e-8) ||
			(definition.Polarity == spammodel.HamRule && audit.FittedScore > 1e-8) {
			return fmt.Errorf("rule %q score is outside its authored range", audit.Name)
		}
		if !audit.Enabled && math.Abs(audit.FittedScore) > 1e-8 {
			return fmt.Errorf("disabled rule %q has a non-zero score", audit.Name)
		}
	}
	if len(report.QualityGates) < 12 {
		return errors.New("benchmark report quality gates are incomplete")
	}
	for _, gate := range report.QualityGates {
		if !gate.Passed || math.IsNaN(gate.Value) || math.IsInf(gate.Value, 0) {
			return fmt.Errorf("quality gate %q is not satisfied", gate.Name)
		}
	}
	classifier, err := spammodel.LoadFiles(paths.Model, paths.Metadata)
	if err != nil {
		return err
	}
	if classifier.ModelVersion() != metadata.ModelVersion || classifier.ModelName() != metadata.ModelName ||
		classifier.TrainingCorpus() != metadata.TrainingCorpus || classifier.Dimension() != metadata.Dimension {
		return errors.New("loaded classifier metadata mismatch")
	}
	fixed := goldenDefinitions()
	if len(metadata.Goldens) != len(fixed) {
		return errors.New("golden inference set is incomplete")
	}
	minimumAsWeMove, maximumAsWeMove := 1.0, 0.0
	for index, expected := range fixed {
		golden := metadata.Goldens[index]
		// obvious_spam's effective minimum is raised to the selected threshold.
		if expected.Name == "obvious_spam" {
			expected.MinimumProbability = math.Max(expected.MinimumProbability, report.OperatingPoint.Threshold)
		}
		if golden.Name != expected.Name || !reflect.DeepEqual(golden.Message, expected.Message) ||
			golden.MinimumProbability != expected.MinimumProbability || golden.MaximumProbability != expected.MaximumProbability ||
			golden.Tolerance <= 0 || golden.Tolerance > 1e-6 {
			return fmt.Errorf("golden inference %d definition mismatch", index)
		}
		score, err := classifier.Classify(golden.Message)
		if err != nil {
			return err
		}
		if math.Abs(score.Probability-golden.Probability) > golden.Tolerance {
			return fmt.Errorf("golden inference %q mismatch", golden.Name)
		}
		if score.Probability < golden.MinimumProbability || score.Probability > golden.MaximumProbability {
			return fmt.Errorf("golden inference %q violates behavior bounds", golden.Name)
		}
		if strings.HasPrefix(golden.Name, "aswemove_") {
			minimumAsWeMove = math.Min(minimumAsWeMove, golden.Probability)
			maximumAsWeMove = math.Max(maximumAsWeMove, golden.Probability)
		}
	}
	if maximumAsWeMove-minimumAsWeMove > .03 {
		return errors.New("AsWeMove repetition behavior regressed")
	}
	fmt.Fprintf(output, "verified %s (%s), %d named rules, held-out recall %.3f and FPR %.3f\n",
		paths.Model, metadata.ModelVersion, metadata.RuleCount, point.TestRecall, point.TestFalsePositiveRate)
	return nil
}

func verifyMetrics(metrics Metrics) error {
	for name, value := range map[string]float64{"pr_auc": metrics.PRAUC, "roc_auc": metrics.ROCAUC, "brier": metrics.Brier, "calibration": metrics.CalibrationError} {
		if !finiteMetric(value) {
			return fmt.Errorf("%s is outside [0,1]", name)
		}
	}
	return nil
}
