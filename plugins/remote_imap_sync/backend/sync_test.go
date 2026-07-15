package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

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

func TestShouldYieldRemoteSyncCadence(t *testing.T) {
	tests := []struct {
		name           string
		scanned, total int64
		want           bool
	}{
		{name: "empty", scanned: 0, total: 100, want: false},
		{name: "before first chunk", scanned: 24, total: 100, want: false},
		{name: "first full chunk with more", scanned: 25, total: 100, want: true},
		{name: "between chunks", scanned: 49, total: 100, want: false},
		{name: "second full chunk with more", scanned: 50, total: 100, want: true},
		{name: "exact final chunk", scanned: 25, total: 25, want: false},
		{name: "final chunk", scanned: 100, total: 100, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldYieldRemoteSync(tt.scanned, tt.total); got != tt.want {
				t.Fatalf("shouldYieldRemoteSync(%d, %d) = %v, want %v", tt.scanned, tt.total, got, tt.want)
			}
		})
	}
}

func TestWaitForRemoteSyncChunkHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err := waitForRemoteSyncChunk(ctx, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForRemoteSyncChunk() error = %v, want context canceled", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("canceled wait took %s", elapsed)
	}
}

func TestWaitForRemoteSyncChunkCompletesDelay(t *testing.T) {
	if err := waitForRemoteSyncChunk(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("waitForRemoteSyncChunk() error = %v", err)
	}
}
