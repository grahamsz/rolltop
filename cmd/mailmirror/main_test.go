package main

import (
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
	if !strings.Contains(res.Body.String(), "MailMirror") {
		t.Fatalf("startup body did not contain MailMirror branding")
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
