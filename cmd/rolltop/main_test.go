package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStartupGateServesStartupHTMLForAppRoutes(t *testing.T) {
	gate := &startupGate{state: newStartupState()}
	req := httptest.NewRequest(http.MethodGet, "/mailbox/97/p3", nil)
	res := httptest.NewRecorder()

	gate.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if !strings.Contains(res.Body.String(), "rolltop") {
		t.Fatalf("startup body did not contain rolltop branding")
	}
}

func TestStartupGateKeepsAPIUnavailableUntilReady(t *testing.T) {
	gate := &startupGate{state: newStartupState()}
	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	res := httptest.NewRecorder()

	gate.ServeHTTP(res, req)

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusServiceUnavailable)
	}
}

func TestStartupHTMLShowsFailureMessage(t *testing.T) {
	state := newStartupState()
	state.fail(errors.New("ROLLTOP_MASTER_KEY is required"))
	rec := httptest.NewRecorder()

	writeStartupHTML(rec, state.snapshotCopy())

	body := rec.Body.String()
	if !strings.Contains(body, "Startup failed") {
		t.Fatalf("startup body did not contain failure phase")
	}
	if !strings.Contains(body, "ROLLTOP_MASTER_KEY is required") {
		t.Fatalf("startup body did not contain startup error")
	}
}
