package model

import (
	"math"
	"reflect"
	"testing"
)

func TestExtractFeaturesDeterministicAndBounded(t *testing.T) {
	message := Message{
		Subject:         "Meeting notes and FREE prize",
		Body:            "Review https://example.org/path before Friday.",
		From:            "Sender <sender@example.org>",
		To:              []string{"one@example.org", "two@example.org"},
		MIMEType:        "text/html; charset=utf-8",
		AttachmentTypes: []string{"application/pdf"},
		HTML:            true,
	}
	first, err := ExtractFeatures(message, 1024)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExtractFeatures(message, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("feature extraction is not deterministic")
	}
	if len(first) == 0 {
		t.Fatal("expected extracted features")
	}
	for _, feature := range first {
		if feature.Index >= 1024 || math.IsNaN(feature.Value) || math.IsInf(feature.Value, 0) {
			t.Fatalf("invalid feature: %#v", feature)
		}
	}
}

func TestBinaryRoundTrip(t *testing.T) {
	weights := make([]float32, 1024)
	features, err := ExtractFeatures(Message{Subject: "free prize"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	weights[features[0].Index] = 0.75
	classifier, err := New(weights, -0.2, 1.1, 0.03, "test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := classifier.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := classifier.Classify(Message{Subject: "free prize"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := loaded.Classify(Message{Subject: "free prize"})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.Probability-want.Probability) > 1e-12 {
		t.Fatalf("probability mismatch: got %v, want %v", got.Probability, want.Probability)
	}
}

func TestInvalidDimension(t *testing.T) {
	if _, err := ExtractFeatures(Message{}, 1000); err == nil {
		t.Fatal("expected invalid dimension error")
	}
}

func TestLoadEmbedded(t *testing.T) {
	classifier, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	if classifier.ModelVersion() == "" || classifier.ModelVersion() == "unknown" {
		t.Fatalf("missing embedded model version: %q", classifier.ModelVersion())
	}
	if classifier.FeatureSchemaVersion() != FeatureSchema {
		t.Fatalf("feature schema mismatch: %q", classifier.FeatureSchemaVersion())
	}
}
