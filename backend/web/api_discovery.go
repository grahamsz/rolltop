package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"rolltop/backend/maildiscovery"
)

type mailSettingsDiscoverer interface {
	Discover(context.Context, string) (maildiscovery.Result, error)
}

// Bound simultaneous lookups across users; one request already fans out to
// several public providers. Nothing is persisted or shared between requests.
var mailDiscoverySlots = make(chan struct{}, 4)

func (s *Server) apiDiscoverMailSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, ok := s.requireAPIAuth(w, r); !ok {
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		writeAPIError(w, http.StatusBadRequest, "Expected only an email address.")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeAPIError(w, http.StatusBadRequest, "Invalid discovery request.")
		return
	}
	select {
	case mailDiscoverySlots <- struct{}{}:
		defer func() { <-mailDiscoverySlots }()
	default:
		writeAPIError(w, http.StatusTooManyRequests, "Server discovery is busy. Try again shortly.")
		return
	}
	result, err := s.mailDiscovery.Discover(r.Context(), in.Email)
	if errors.Is(err, maildiscovery.ErrInvalidEmail) {
		writeAPIError(w, http.StatusBadRequest, "Enter a valid email address. Use the ASCII (punycode) form for an international domain.")
		return
	}
	if err != nil {
		// Do not log errors carrying submitted addresses or provider responses.
		writeAPIError(w, http.StatusBadGateway, "Could not discover server settings. Enter them manually or try again.")
		return
	}
	writeJSON(w, result)
}
