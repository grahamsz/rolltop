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

	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

// Verify is intentionally offline: it reads only the three checked-in artifact
// files and checks their format, provenance, quality gates, and golden inference.
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
		return errors.New("model metadata is incompatible with runtime features")
	}
	if metadata.Dimension != spammodel.DefaultDimension || metadata.Hyperparameters.Dimension != metadata.Dimension {
		return errors.New("model metadata dimension mismatch")
	}
	if metadata.TrainerVersion != TrainerVersion || metadata.Seed != TrainingSeed {
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
	if metadata.Counts.ParseSkipped < 0 {
		return errors.New("dataset parse-skipped count is invalid")
	}
	if metadata.Counts.TrainSpam <= 0 || metadata.Counts.TestSpam <= 0 || metadata.Counts.TestSpam >= metadata.Counts.Test {
		return errors.New("dataset splits do not contain both labels")
	}
	if len(modelData) > maxModelBytes || report.Runtime.ModelBytes != len(modelData) {
		return errors.New("model size benchmark is invalid")
	}
	if err := verifyMetrics(report.Logistic); err != nil {
		return fmt.Errorf("logistic metrics: %w", err)
	}
	if err := verifyMetrics(report.NaiveBayes); err != nil {
		return fmt.Errorf("naive Bayes metrics: %w", err)
	}
	if report.Logistic.PRAUC < report.NaiveBayes.PRAUC || report.FPRDifference95CI.Upper >= 0 {
		return errors.New("model does not satisfy the Bayesian comparison gate")
	}
	if report.NaiveBayesRecipe != NaiveBayesRecipe {
		return errors.New("naive Bayes baseline recipe mismatch")
	}
	if report.Runtime.Messages <= 0 || report.Runtime.InferenceNanosecondsEach <= 0 ||
		report.Runtime.InferenceNanosecondsEach > maxInferenceNanos || report.Runtime.AllocatedBytesEach < 0 ||
		report.Runtime.AllocatedBytesEach > maxAllocatedBytes {
		return errors.New("runtime benchmark exceeds declared limits")
	}
	if len(report.QualityGates) != 5 {
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
	if classifier.ModelVersion() != metadata.ModelVersion || classifier.Dimension() != metadata.Dimension {
		return errors.New("loaded classifier metadata mismatch")
	}
	fixed := fixedGoldenMessages()
	if len(metadata.Goldens) != len(fixed) {
		return errors.New("golden inference set is incomplete")
	}
	for index, expected := range fixed {
		golden := metadata.Goldens[index]
		if golden.Name != expected.Name || !reflect.DeepEqual(golden.Message, expected.Message) || golden.Tolerance <= 0 || golden.Tolerance > 1e-6 {
			return fmt.Errorf("golden inference %d definition mismatch", index)
		}
		score, err := classifier.Classify(golden.Message)
		if err != nil {
			return err
		}
		if math.Abs(score.Probability-golden.Probability) > golden.Tolerance {
			return fmt.Errorf("golden inference %q mismatch: got %.12f, want %.12f", golden.Name, score.Probability, golden.Probability)
		}
	}
	fmt.Fprintf(output, "verified %s (%s), %d bytes, PR-AUC %.6f\n", paths.Model, metadata.ModelVersion, len(modelData), report.Logistic.PRAUC)
	return nil
}

func verifyMetrics(metrics Metrics) error {
	for name, value := range map[string]float64{
		"pr_auc":      metrics.PRAUC,
		"roc_auc":     metrics.ROCAUC,
		"brier":       metrics.Brier,
		"calibration": metrics.CalibrationError,
		"recall":      metrics.RecallTarget,
		"fpr":         metrics.FalsePositiveRate,
		"threshold":   metrics.Threshold,
	} {
		if !finiteMetric(value) {
			return fmt.Errorf("%s is outside [0,1]", name)
		}
	}
	if metrics.RecallTarget != 0.90 {
		return errors.New("unexpected recall target")
	}
	return nil
}
