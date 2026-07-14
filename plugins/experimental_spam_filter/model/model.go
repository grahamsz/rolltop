// Package model contains Rolltop's native spam feature extractor and compact
// logistic classifier. It has no network, storage, or sidecar dependencies.
package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
)

const (
	BinaryFormatVersion = uint16(1)
	DefaultDimension    = uint32(1 << 17)
	maxContributions    = 12
)

var (
	binaryMagic         = [8]byte{'R', 'T', 'S', 'P', 'A', 'M', '1', 0}
	ErrInvalidDimension = errors.New("spam model dimension must be a non-zero power of two")
)

type RiskBand string

const (
	RiskLow    RiskBand = "low"
	RiskMedium RiskBand = "medium"
	RiskHigh   RiskBand = "high"
)

type Contribution struct {
	Feature string  `json:"feature"`
	Value   float64 `json:"value"`
	Weight  float64 `json:"weight"`
	Impact  float64 `json:"impact"`
}

type Score struct {
	Probability   float64        `json:"probability"`
	Band          RiskBand       `json:"band"`
	ModelVersion  string         `json:"model_version"`
	FeatureSchema string         `json:"feature_schema"`
	Contributions []Contribution `json:"contributions,omitempty"`
}

type Classifier struct {
	dimension       uint32
	bias            float64
	calibrationA    float64
	calibrationB    float64
	mediumThreshold float64
	highThreshold   float64
	weights         []float32
	modelVersion    string
	featureSchema   string
}

type artifactMetadata struct {
	ModelVersion    string  `json:"model_version"`
	FeatureSchema   string  `json:"feature_schema"`
	Dimension       uint32  `json:"dimension"`
	ModelSHA256     string  `json:"model_sha256"`
	MediumThreshold float64 `json:"medium_threshold"`
	HighThreshold   float64 `json:"high_threshold"`
}

// New constructs a classifier from trained weights. It is primarily used by
// the offline trainer; production callers should use LoadEmbedded.
func New(weights []float32, bias, calibrationA, calibrationB float64, modelVersion string) (*Classifier, error) {
	dimension := uint32(len(weights))
	if dimension == 0 || dimension&(dimension-1) != 0 {
		return nil, ErrInvalidDimension
	}
	if !finite(bias) || !finite(calibrationA) || !finite(calibrationB) || calibrationA <= 0 {
		return nil, errors.New("spam model contains invalid parameters")
	}
	return &Classifier{
		dimension:       dimension,
		bias:            bias,
		calibrationA:    calibrationA,
		calibrationB:    calibrationB,
		mediumThreshold: 0.35,
		highThreshold:   0.80,
		weights:         append([]float32(nil), weights...),
		modelVersion:    modelVersion,
		featureSchema:   FeatureSchema,
	}, nil
}

func (c *Classifier) ModelVersion() string { return c.modelVersion }

func (c *Classifier) FeatureSchemaVersion() string { return c.featureSchema }

func (c *Classifier) Dimension() uint32 { return c.dimension }

func (c *Classifier) Classify(message Message) (Score, error) {
	if c == nil || len(c.weights) == 0 {
		return Score{}, errors.New("spam classifier is not loaded")
	}
	features, err := ExtractFeatures(message, c.dimension)
	if err != nil {
		return Score{}, err
	}
	logit := c.bias
	contributions := make([]Contribution, 0, len(features))
	for _, feature := range features {
		weight := float64(c.weights[feature.Index])
		impact := weight * feature.Value
		logit += impact
		if impact != 0 {
			contributions = append(contributions, Contribution{
				Feature: feature.Name,
				Value:   feature.Value,
				Weight:  weight,
				Impact:  impact,
			})
		}
	}
	probability := sigmoid(c.calibrationA*logit + c.calibrationB)
	sort.SliceStable(contributions, func(i, j int) bool {
		return math.Abs(contributions[i].Impact) > math.Abs(contributions[j].Impact)
	})
	if len(contributions) > maxContributions {
		contributions = contributions[:maxContributions]
	}
	band := RiskLow
	if probability >= c.highThreshold {
		band = RiskHigh
	} else if probability >= c.mediumThreshold {
		band = RiskMedium
	}
	return Score{
		Probability:   probability,
		Band:          band,
		ModelVersion:  c.modelVersion,
		FeatureSchema: c.featureSchema,
		Contributions: contributions,
	}, nil
}

// MarshalBinary emits a stable little-endian artifact. The model version and
// threshold policy live in model.json so weights can be checksummed directly.
func (c *Classifier) MarshalBinary() ([]byte, error) {
	if c == nil || uint32(len(c.weights)) != c.dimension {
		return nil, errors.New("invalid spam classifier")
	}
	buffer := bytes.NewBuffer(make([]byte, 0, 48+len(c.weights)*4))
	buffer.Write(binaryMagic[:])
	for _, value := range []any{
		BinaryFormatVersion,
		uint16(0),
		c.dimension,
		c.bias,
		c.calibrationA,
		c.calibrationB,
	} {
		if err := binary.Write(buffer, binary.LittleEndian, value); err != nil {
			return nil, err
		}
	}
	for _, weight := range c.weights {
		if err := binary.Write(buffer, binary.LittleEndian, weight); err != nil {
			return nil, err
		}
	}
	return buffer.Bytes(), nil
}

func Load(data []byte) (*Classifier, error) {
	reader := bytes.NewReader(data)
	var magic [8]byte
	if _, err := reader.Read(magic[:]); err != nil || magic != binaryMagic {
		return nil, errors.New("invalid spam model magic")
	}
	var version, reserved uint16
	var dimension uint32
	var bias, calibrationA, calibrationB float64
	for _, target := range []any{&version, &reserved, &dimension, &bias, &calibrationA, &calibrationB} {
		if err := binary.Read(reader, binary.LittleEndian, target); err != nil {
			return nil, fmt.Errorf("read spam model header: %w", err)
		}
	}
	if version != BinaryFormatVersion {
		return nil, fmt.Errorf("unsupported spam model format %d", version)
	}
	if reserved != 0 || dimension == 0 || dimension&(dimension-1) != 0 {
		return nil, errors.New("invalid spam model header")
	}
	if reader.Len() != int(dimension)*4 {
		return nil, fmt.Errorf("spam model weight length mismatch: got %d bytes", reader.Len())
	}
	weights := make([]float32, dimension)
	if err := binary.Read(reader, binary.LittleEndian, weights); err != nil {
		return nil, fmt.Errorf("read spam model weights: %w", err)
	}
	classifier, err := New(weights, bias, calibrationA, calibrationB, "unknown")
	if err != nil {
		return nil, err
	}
	for _, weight := range weights {
		if math.IsNaN(float64(weight)) || math.IsInf(float64(weight), 0) {
			return nil, errors.New("spam model contains non-finite weight")
		}
	}
	return classifier, nil
}

func LoadFiles(modelPath, metadataPath string) (*Classifier, error) {
	modelData, err := os.ReadFile(modelPath)
	if err != nil {
		return nil, err
	}
	metadataData, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, err
	}
	return loadWithMetadata(modelData, metadataData)
}

func loadWithMetadata(modelData, metadataData []byte) (*Classifier, error) {
	classifier, err := Load(modelData)
	if err != nil {
		return nil, err
	}
	var metadata artifactMetadata
	if err := json.Unmarshal(metadataData, &metadata); err != nil {
		return nil, fmt.Errorf("decode spam model metadata: %w", err)
	}
	if metadata.ModelVersion == "" || metadata.FeatureSchema != FeatureSchema {
		return nil, errors.New("spam model metadata is incompatible")
	}
	digest := sha256.Sum256(modelData)
	if metadata.Dimension != classifier.dimension || metadata.ModelSHA256 != hex.EncodeToString(digest[:]) {
		return nil, errors.New("spam model metadata checksum or dimension mismatch")
	}
	if metadata.MediumThreshold <= 0 || metadata.HighThreshold >= 1 || metadata.MediumThreshold >= metadata.HighThreshold {
		return nil, errors.New("spam model metadata has invalid thresholds")
	}
	classifier.modelVersion = metadata.ModelVersion
	classifier.featureSchema = metadata.FeatureSchema
	classifier.mediumThreshold = metadata.MediumThreshold
	classifier.highThreshold = metadata.HighThreshold
	return classifier, nil
}

func sigmoid(value float64) float64 {
	if value >= 0 {
		exp := math.Exp(-value)
		return 1 / (1 + exp)
	}
	exp := math.Exp(value)
	return exp / (1 + exp)
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
