// File overview: Runtime API and lifecycle for calendar invites. The backend
// implements IncomingMessageHook to detect METHOD:REQUEST invitations during
// import, and exposes protected routes to list invites, send RSVP replies
// through the host outbox, and push events to a CalDAV calendar. RSVP
// detection is best-effort: invite parsing never fails the import it runs in.

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"rolltop/backend/mailparse"
	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

const (
	apiPath  = "plugins/calendar_invites"
	pluginID = "calendar_invites"
)

type calendarInvitesBackend struct {
	mu     sync.Mutex
	routes []plugins.ProtectedAPIRouteHandle
	host   plugins.BackendStartHost
}

// RolltopPlugin is loaded by the runtime Go plugin host.
func RolltopPlugin() plugins.BackendPlugin {
	return &calendarInvitesBackend{}
}

func (*calendarInvitesBackend) ID() string { return pluginID }

func (p *calendarInvitesBackend) Start(host plugins.BackendStartHost) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
	handle, err := host.RegisterProtectedAPI(p.ID(), plugins.ProtectedAPIRoute{
		Path: apiPath, Prefix: true, Handle: p.handleAPI,
	})
	if err != nil {
		return err
	}
	p.routes = append(p.routes, handle)
	p.host = host
	return nil
}

func (p *calendarInvitesBackend) Stop(plugins.BackendStartHost) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
	return nil
}

func (p *calendarInvitesBackend) stopLocked() {
	p.routes = nil
	p.host = nil
}

// ImportIncomingMessage implements plugins.IncomingMessageHook. It MIME-walks
// the raw message, extracts text/calendar parts and .ics attachments, and
// records METHOD:REQUEST invitations. Errors are swallowed: invite detection
// must never break mail import.
func (p *calendarInvitesBackend) ImportIncomingMessage(ctx context.Context, host plugins.BackendHost, userID int64, raw []byte, _ string) error {
	if !host.PluginEnabled(ctx, pluginID) {
		return nil
	}
	st, ok := host.Store().(*store.Store)
	if !ok || st == nil {
		return nil
	}
	db, err := st.UserDB(ctx, userID)
	if err != nil {
		return nil
	}
	parsed, err := mailparse.Parse(raw)
	if err != nil {
		return nil
	}
	for _, file := range parsed.Files {
		mediaType, _, _ := mime.ParseMediaType(file.ContentType)
		isCalendar := strings.EqualFold(strings.TrimSpace(mediaType), "text/calendar") ||
			strings.HasSuffix(strings.ToLower(strings.TrimSpace(file.Filename)), ".ics")
		if !isCalendar || len(file.Data) == 0 {
			continue
		}
		for _, ev := range parseICSEvents(file.Data) {
			if !isInviteMethod(ev.Method) || ev.UID == "" {
				continue
			}
			matched := matchImportAttendee(ctx, st, userID, ev)
			// Best effort: a failed upsert must not fail the import.
			_ = upsertInvite(ctx, db, userID, 0, ev, matched)
		}
	}
	return nil
}

// matchImportAttendee picks the attendee entry belonging to the user, if any,
// so the invite list can show who the invitation is for. RSVP re-matches
// anyway, so a miss here is harmless.
func matchImportAttendee(ctx context.Context, st *store.Store, userID int64, ev icsEvent) icsAttendee {
	identities, err := st.ListMailIdentitiesForUser(ctx, userID)
	if err != nil {
		return icsAttendee{}
	}
	known := map[string]bool{}
	for _, identity := range identities {
		if addr := normalizeAddr(identity.Email); addr != "" {
			known[addr] = true
		}
	}
	for _, att := range ev.Attendees {
		if att.Address != "" && known[normalizeAddr(att.Address)] {
			return att
		}
	}
	return icsAttendee{}
}

func (p *calendarInvitesBackend) handleAPI(host plugins.APIHost, path string, w http.ResponseWriter, r *http.Request) {
	current, ok := host.RequireAPIAuth(w, r)
	if !ok {
		return
	}
	st, ok := host.Store().(*store.Store)
	if !ok || st == nil {
		host.WriteAPIError(w, http.StatusServiceUnavailable, "Calendar invites is not available")
		return
	}
	db, err := st.UserDB(r.Context(), current.UserID)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(path, apiPath), "/")
	switch {
	case rest == "invites" && r.Method == http.MethodGet:
		p.apiListInvites(host, db, current.UserID, w, r)
	case strings.HasPrefix(rest, "invites/"):
		p.apiInviteAction(host, st, db, current.UserID, rest, w, r)
	case rest == "caldav/config" && r.Method == http.MethodGet:
		p.apiGetCalDAVConfig(host, db, current.UserID, w, r)
	case rest == "caldav/config" && r.Method == http.MethodPost:
		p.apiSaveCalDAVConfig(host, db, current.UserID, w, r)
	case rest == "caldav/discover" && r.Method == http.MethodPost:
		p.apiDiscoverCalendars(host, db, current.UserID, w, r)
	case rest == "caldav/test" && r.Method == http.MethodPost:
		p.apiTestCalDAV(host, db, current.UserID, w, r)
	default:
		host.WriteAPIError(w, http.StatusNotFound, "Calendar invites route not found")
	}
}

func (p *calendarInvitesBackend) apiListInvites(host plugins.APIHost, db *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	invites, err := listInvites(r.Context(), db, userID)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	if invites == nil {
		invites = []inviteRecord{}
	}
	host.WriteJSON(w, map[string]any{"invites": invites})
}

func (p *calendarInvitesBackend) apiInviteAction(host plugins.APIHost, st *store.Store, db *sql.DB, userID int64, rest string, w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 3 || parts[0] != "invites" {
		host.WriteAPIError(w, http.StatusNotFound, "invite route not found")
		return
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		host.WriteAPIError(w, http.StatusBadRequest, "invalid invite id")
		return
	}
	switch {
	case parts[2] == "rsvp" && r.Method == http.MethodPost:
		p.apiRSVP(host, st, db, userID, id, w, r)
	case parts[2] == "push" && r.Method == http.MethodPost:
		p.apiPush(host, db, userID, id, w, r)
	default:
		host.WriteAPIError(w, http.StatusNotFound, "invite route not found")
	}
}

type rsvpInput struct {
	PartStat string `json:"partstat"`
	Sequence *int   `json:"sequence"`
}

func (p *calendarInvitesBackend) apiRSVP(host plugins.APIHost, st *store.Store, db *sql.DB, userID, inviteID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	var in rsvpInput
	if !host.DecodeJSON(w, r, &in) {
		return
	}
	wantSequence := -1
	if in.Sequence != nil {
		wantSequence = *in.Sequence
	}
	err := sendRSVP(r.Context(), st, db, userID, inviteID, in.PartStat, wantSequence)
	switch {
	case err == nil:
		host.WriteJSON(w, map[string]any{"ok": true})
	case errors.Is(err, errInviteNotFound):
		host.WriteAPIError(w, http.StatusNotFound, "invite not found")
	case errors.Is(err, errStaleSequence):
		host.WriteAPIError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errNoMatchingIdentity):
		host.WriteAPIError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		host.ServerError(w, err)
	}
}

func (p *calendarInvitesBackend) apiPush(host plugins.APIHost, db *sql.DB, userID, inviteID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	invite, err := getInvite(r.Context(), db, userID, inviteID)
	if err != nil {
		if errors.Is(err, errInviteNotFound) {
			host.WriteAPIError(w, http.StatusNotFound, "invite not found")
			return
		}
		host.ServerError(w, err)
		return
	}
	cfg, password, err := loadCalDAVSecrets(r.Context(), db, userID, host.MasterKey())
	if err != nil {
		host.WriteAPIError(w, http.StatusBadRequest, "CalDAV is not configured: "+err.Error())
		return
	}
	if !cfg.Enabled {
		host.WriteAPIError(w, http.StatusBadRequest, "CalDAV push is not enabled")
		return
	}
	if strings.TrimSpace(cfg.CalendarURL) == "" {
		host.WriteAPIError(w, http.StatusBadRequest, "no CalDAV calendar selected")
		return
	}
	raw, err := inviteRawICS(r.Context(), db, userID, inviteID)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	client, err := newCalDAVClient(cfg.ServerURL, caldavCredentials{Username: cfg.Username, Password: password})
	if err != nil {
		host.WriteAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := client.PutEvent(r.Context(), cfg.CalendarURL, invite.ICSUID, raw); err != nil {
		_ = setCalDAVError(r.Context(), db, userID, err.Error())
		host.WriteAPIError(w, http.StatusBadGateway, "calendar upload failed: "+err.Error())
		return
	}
	_ = setCalDAVError(r.Context(), db, userID, "")
	host.WriteJSON(w, map[string]any{"ok": true})
}

func (p *calendarInvitesBackend) apiGetCalDAVConfig(host plugins.APIHost, db *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	cfg, err := getCalDAVConfig(r.Context(), db, userID)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	host.WriteJSON(w, map[string]any{"config": cfg})
}

type caldavConfigInput struct {
	ServerURL   string `json:"server_url"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	CalendarURL string `json:"calendar_url"`
	Enabled     bool   `json:"enabled"`
}

func (p *calendarInvitesBackend) apiSaveCalDAVConfig(host plugins.APIHost, db *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	var in caldavConfigInput
	if !host.DecodeJSON(w, r, &in) {
		return
	}
	cfg := caldavConfig{
		ServerURL:   strings.TrimSpace(in.ServerURL),
		Username:    strings.TrimSpace(in.Username),
		CalendarURL: strings.TrimSpace(in.CalendarURL),
		Enabled:     in.Enabled,
	}
	if cfg.Enabled && (cfg.ServerURL == "" || cfg.Username == "") {
		host.WriteAPIError(w, http.StatusBadRequest, "server URL and username are required to enable CalDAV push")
		return
	}
	if err := saveCalDAVConfig(r.Context(), db, userID, host.MasterKey(), cfg, in.Password); err != nil {
		host.ServerError(w, err)
		return
	}
	updated, err := getCalDAVConfig(r.Context(), db, userID)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	host.WriteJSON(w, map[string]any{"ok": true, "config": updated})
}

type caldavDiscoverInput struct {
	ServerURL string `json:"server_url"`
	Username  string `json:"username"`
	Password  string `json:"password"`
}

type calendarView struct {
	URL         string `json:"url"`
	DisplayName string `json:"display_name"`
}

// discoverCalendarsWith is shared by the discover and test endpoints: when the
// password field is blank it falls back to the stored encrypted password.
func (p *calendarInvitesBackend) discoverCalendarsWith(ctx context.Context, host plugins.APIHost, db *sql.DB, userID int64, in caldavDiscoverInput) ([]calendar, error) {
	password := in.Password
	serverURL := strings.TrimSpace(in.ServerURL)
	username := strings.TrimSpace(in.Username)
	if password == "" {
		stored, storedPassword, err := loadCalDAVSecrets(ctx, db, userID, host.MasterKey())
		if err != nil {
			return nil, fmt.Errorf("no password supplied and none is stored")
		}
		if serverURL == "" {
			serverURL = stored.ServerURL
		}
		if username == "" {
			username = stored.Username
		}
		password = storedPassword
	}
	client, err := newCalDAVClient(serverURL, caldavCredentials{Username: username, Password: password})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, caldavRequestTimeout)
	defer cancel()
	return client.DiscoverCalendars(ctx)
}

func (p *calendarInvitesBackend) apiDiscoverCalendars(host plugins.APIHost, db *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	var in caldavDiscoverInput
	if !host.DecodeJSON(w, r, &in) {
		return
	}
	cals, err := p.discoverCalendarsWith(r.Context(), host, db, userID, in)
	if err != nil {
		host.WriteAPIError(w, http.StatusBadGateway, "CalDAV discovery failed: "+err.Error())
		return
	}
	views := make([]calendarView, 0, len(cals))
	for _, cal := range cals {
		views = append(views, calendarView{URL: cal.URL, DisplayName: cal.DisplayName})
	}
	host.WriteJSON(w, map[string]any{"calendars": views})
}

func (p *calendarInvitesBackend) apiTestCalDAV(host plugins.APIHost, db *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	var in caldavDiscoverInput
	if !host.DecodeJSON(w, r, &in) {
		return
	}
	cals, err := p.discoverCalendarsWith(r.Context(), host, db, userID, in)
	if err != nil {
		_ = setCalDAVError(r.Context(), db, userID, err.Error())
		host.WriteAPIError(w, http.StatusBadGateway, "CalDAV test failed: "+err.Error())
		return
	}
	_ = setCalDAVError(r.Context(), db, userID, "")
	host.WriteJSON(w, map[string]any{"ok": true, "calendars": len(cals)})
}
