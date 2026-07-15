package main

import "testing"

func TestShouldWriteRunProgressCadence(t *testing.T) {
	tests := []struct {
		name    string
		scanned int64
		want    bool
	}{
		{name: "empty checkpoint", scanned: 0, want: false},
		{name: "first checkpoint", scanned: 1, want: true},
		{name: "second checkpoint", scanned: 2, want: false},
		{name: "before interval", scanned: 24, want: false},
		{name: "interval checkpoint", scanned: 25, want: true},
		{name: "after interval", scanned: 26, want: false},
		{name: "second interval checkpoint", scanned: 50, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldWriteRunProgress(tt.scanned); got != tt.want {
				t.Fatalf("shouldWriteRunProgress(%d) = %v, want %v", tt.scanned, got, tt.want)
			}
		})
	}
}

func TestRunProgressCadenceKeepsFinalPosition(t *testing.T) {
	for total := int64(1); total <= 100; total++ {
		lastWritten := int64(0)
		writes := 0
		for scanned := int64(1); scanned <= total; scanned++ {
			if shouldWriteRunProgress(scanned) {
				lastWritten = scanned
				writes++
			}
		}
		if runProgressNeedsFlush(total, lastWritten) {
			lastWritten = total
			writes++
		}
		if lastWritten != total {
			t.Fatalf("total %d left last persisted position at %d", total, lastWritten)
		}
		wantMaxWrites := int(total/runProgressWriteEvery) + 2
		if writes > wantMaxWrites {
			t.Fatalf("total %d produced %d writes, want at most %d", total, writes, wantMaxWrites)
		}
	}
}

func TestRunProgressFailureFlushesSinceLastCheckpoint(t *testing.T) {
	tests := []struct {
		name      string
		scanned   int64
		persisted int64
		want      bool
	}{
		{name: "no messages", scanned: 0, persisted: 0, want: false},
		{name: "first checkpoint persisted", scanned: 1, persisted: 1, want: false},
		{name: "between checkpoints", scanned: 24, persisted: 1, want: true},
		{name: "checkpoint write failed", scanned: 25, persisted: 1, want: true},
		{name: "checkpoint persisted", scanned: 25, persisted: 25, want: false},
		{name: "later partial batch", scanned: 49, persisted: 25, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runProgressNeedsFlush(tt.scanned, tt.persisted); got != tt.want {
				t.Fatalf("runProgressNeedsFlush(%d, %d) = %v, want %v", tt.scanned, tt.persisted, got, tt.want)
			}
		})
	}
}
