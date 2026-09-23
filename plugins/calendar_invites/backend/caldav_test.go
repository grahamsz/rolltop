package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func principalXML() string {
	return `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:multistatus xmlns:d="DAV:">` +
		`<d:response><d:href>/principal/user1</d:href>` +
		`<d:propstat><d:prop><d:current-user-principal><d:href>/principal/user1</d:href></d:current-user-principal></d:prop>` +
		`<d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`
}

func homeSetXML() string {
	return `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">` +
		`<d:response><d:href>/principal/user1</d:href>` +
		`<d:propstat><d:prop><cal:calendar-home-set><d:href>/calendars/user1/</d:href></cal:calendar-home-set></d:prop>` +
		`<d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`
}

func calendarListXML() string {
	return `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">` +
		`<d:response><d:href>/calendars/user1/personal/</d:href>` +
		`<d:propstat><d:prop><d:displayname>Personal</d:displayname>` +
		`<d:resourcetype><d:collection/><cal:calendar/></d:resourcetype></d:prop>` +
		`<d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>` +
		`<d:response><d:href>/calendars/user1/contacts/</d:href>` +
		`<d:propstat><d:prop><d:displayname>Contacts</d:displayname>` +
		`<d:resourcetype><d:collection/><card:addressbook xmlns:card="urn:ietf:params:xml:ns:carddav"/></d:resourcetype></d:prop>` +
		`<d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>` +
		`</d:multistatus>`
}

func testCalDAVServer(t *testing.T) (*httptest.Server, *strings.Builder) {
	t.Helper()
	putBody := &strings.Builder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/caldav", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(207)
		_, _ = w.Write([]byte(principalXML()))
	})
	mux.HandleFunc("/principal/user1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(207)
		_, _ = w.Write([]byte(homeSetXML()))
	})
	mux.HandleFunc("/calendars/user1/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			body, _ := io.ReadAll(r.Body)
			putBody.Write(body)
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "text/calendar") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(207)
		_, _ = w.Write([]byte(calendarListXML()))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, putBody
}

func TestCalDAVDiscovery(t *testing.T) {
	server, _ := testCalDAVServer(t)
	client, err := newCalDAVClient(server.URL, caldavCredentials{Username: "user1", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cals, err := client.DiscoverCalendars(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cals) != 1 {
		t.Fatalf("discovered %d calendars, want 1 (addressbook must be filtered out)", len(cals))
	}
	if cals[0].DisplayName != "Personal" {
		t.Errorf("calendar name = %q", cals[0].DisplayName)
	}
	if !strings.HasSuffix(cals[0].URL, "/calendars/user1/personal/") {
		t.Errorf("calendar URL = %q", cals[0].URL)
	}
}

func TestCalDAVPutEvent(t *testing.T) {
	server, putBody := testCalDAVServer(t)
	client, err := newCalDAVClient(server.URL, caldavCredentials{Username: "user1", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	ics := []byte("BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:evt-1\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	if err := client.PutEvent(context.Background(), server.URL+"/calendars/user1/personal/", "evt-1", ics); err != nil {
		t.Fatal(err)
	}
	if got := putBody.String(); got != string(ics) {
		t.Errorf("PUT body = %q", got)
	}
}

func TestCalDAVRequiresHTTPSOffLoopback(t *testing.T) {
	if _, err := newCalDAVClient("http://cal.example.com", caldavCredentials{Username: "u", Password: "p"}); err == nil {
		t.Errorf("plain HTTP to a remote host must be rejected")
	}
	if _, err := newCalDAVClient("http://127.0.0.1:8080", caldavCredentials{Username: "u", Password: "p"}); err != nil {
		t.Errorf("plain HTTP to loopback must be allowed: %v", err)
	}
}

func TestCalDAVNeedsCredentials(t *testing.T) {
	if _, err := newCalDAVClient("https://cal.example.com", caldavCredentials{Username: "", Password: "p"}); err == nil {
		t.Errorf("empty username must be rejected")
	}
	if _, err := newCalDAVClient("", caldavCredentials{Username: "u", Password: "p"}); err == nil {
		t.Errorf("empty server URL must be rejected")
	}
}
