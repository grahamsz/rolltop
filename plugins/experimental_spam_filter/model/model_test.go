package model

import (
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func testMessage(subject, body string) Message {
	return Message{Subject: subject, Body: body, From: "sender@example.org", To: []string{"reader@example.org"}, MIMEType: "text/plain"}
}

func TestNamedRuleExtractionDeterministicAndBounded(t *testing.T) {
	if RuleCount() == 0 || RuleCount() > 128 {
		t.Fatalf("rule count = %d", RuleCount())
	}
	message := testMessage("URGENT: claim your FREE cash prize now", "Click here to claim your prize. This limited time offer expires today!")
	first, err := ExtractFeatures(message, DefaultDimension)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExtractFeatures(message, DefaultDimension)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("rule extraction is not deterministic")
	}
	if len(first) == 0 {
		t.Fatal("expected named rule hits")
	}
	definitions := RuleDefinitions()
	seenNames := make(map[string]bool)
	for _, definition := range definitions {
		if definition.Index >= 128 || definition.Name == "" || definition.Description == "" || seenNames[definition.Name] {
			t.Fatalf("invalid rule definition: %#v", definition)
		}
		seenNames[definition.Name] = true
	}
	for _, feature := range first {
		if feature.Index >= uint32(len(definitions)) || feature.Name != definitions[feature.Index].Name || feature.Value <= 0 ||
			math.IsNaN(feature.Value) || math.IsInf(feature.Value, 0) {
			t.Fatalf("invalid named rule hit: %#v", feature)
		}
	}
}

func TestNoHashedOrSubstringFeaturesForAsWeMoveAndMovie(t *testing.T) {
	base := testMessage("AsWeMove member update", "AsWeMove no action is required; manage preferences at https://account.example.org/preferences")
	repeated := base
	repeated.Body = strings.Repeat("AsWeMove ", 64) + "no action is required; manage preferences at https://account.example.org/preferences"
	first, err := ExtractFeatures(base, DefaultDimension)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExtractFeatures(repeated, DefaultDimension)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeating an unlisted token changed rules:\nfirst=%#v\nsecond=%#v", first, second)
	}
	rules := RuleDefinitions()
	for _, message := range []Message{base, repeated, testMessage("Movie screening moved to Friday", "The community movie screening moved to Friday. Please review the room change before the meeting.")} {
		hits, err := ExtractFeatures(message, DefaultDimension)
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range hits {
			lower := strings.ToLower(hit.Name)
			if strings.HasPrefix(lower, "char:") || strings.HasPrefix(lower, "word:") || lower == "mov" || lower == "emo" {
				t.Fatalf("data-derived fragment leaked into rule hits: %q", hit.Name)
			}
			if rules[hit.Index].Polarity == SpamRule {
				t.Fatalf("benign AsWeMove/movie text hit spam rule %q", hit.Name)
			}
		}
	}
}

func TestExtractFeaturesConcurrent(t *testing.T) {
	message := testMessage("Free offer", "Click here for a free gift. Act now.")
	want, err := ExtractFeatures(message, DefaultDimension)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errors := make(chan string, 32)
	for worker := 0; worker < 32; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				got, extractErr := ExtractFeatures(message, DefaultDimension)
				if extractErr != nil {
					errors <- extractErr.Error()
					return
				}
				if !reflect.DeepEqual(got, want) {
					errors <- "concurrent extraction differed"
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errors)
	for message := range errors {
		t.Fatal(message)
	}
}

func TestBinaryRoundTripAndDescribedContribution(t *testing.T) {
	message := testMessage("Free offer", "Click here for a free gift")
	features, err := ExtractFeatures(message, DefaultDimension)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) == 0 {
		t.Fatal("expected a feature")
	}
	weights := make([]float32, DefaultDimension)
	weights[features[0].Index] = .75
	classifier, err := New(weights, -.2, 1.1, .03, "test")
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
	want, err := classifier.Classify(message)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loaded.Classify(message)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.Probability-want.Probability) > 1e-12 {
		t.Fatalf("probability mismatch: got %v, want %v", got.Probability, want.Probability)
	}
	if len(got.Contributions) == 0 || got.Contributions[0].Description == "" {
		t.Fatal("runtime contribution is missing the authored rule description")
	}
}

func TestInvalidDimension(t *testing.T) {
	if _, err := ExtractFeatures(Message{}, 64); err == nil {
		t.Fatal("expected undersized dimension error")
	}
	if _, err := ExtractFeatures(Message{}, 1000); err == nil {
		t.Fatal("expected non-power-of-two dimension error")
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
	if classifier.ModelName() != "Rolltop named-rule scorecard" || !strings.Contains(classifier.TrainingCorpus(), "not a SpamAssassin model") {
		t.Fatalf("untruthful embedded identity: name=%q corpus=%q", classifier.ModelName(), classifier.TrainingCorpus())
	}
	if classifier.FeatureSchemaVersion() != FeatureSchema {
		t.Fatalf("feature schema mismatch: %q", classifier.FeatureSchemaVersion())
	}
}
