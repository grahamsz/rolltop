package imapclient

import (
	"errors"
	"testing"
	"time"
)

func TestStopIdleSessionStopsCleanly(t *testing.T) {
	stop := make(chan struct{})
	done := make(chan error, 1)
	terminated := false
	go func() {
		<-stop
		done <- nil
	}()

	if err := stopIdleSession(stop, done, func() error {
		terminated = true
		return nil
	}, time.Second); err != nil {
		t.Fatalf("stopIdleSession error = %v", err)
	}
	if terminated {
		t.Fatalf("terminate called for clean IDLE stop")
	}
}

func TestStopIdleSessionTerminatesStuckIdle(t *testing.T) {
	stop := make(chan struct{})
	done := make(chan error)
	terminated := false

	err := stopIdleSession(stop, done, func() error {
		terminated = true
		return nil
	}, 10*time.Millisecond)
	if !errors.Is(err, errIdleStopTimeout) {
		t.Fatalf("stopIdleSession error = %v, want errIdleStopTimeout", err)
	}
	if !terminated {
		t.Fatalf("terminate was not called for stuck IDLE stop")
	}
	select {
	case <-stop:
	default:
		t.Fatalf("stop channel was not closed")
	}
}
