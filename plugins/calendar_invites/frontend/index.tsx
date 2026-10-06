// File overview: Runtime settings UI for calendar invites. The page lists
// detected METHOD:REQUEST invitations with Accept/Tentative/Decline buttons
// (the RSVP travels through the host outbox), plus an optional CalDAV
// section to push invitations onto a calendar. Routes mount at
// /settings/account/plugins/calendar-invites.

import { useCallback, useEffect, useState } from "react";
import type { FormEvent } from "react";
import type { Toast } from "../../../frontend/src/appTypes";
import { Icon } from "../../../frontend/src/components/Icon";
import { SettingsEmpty, SettingsError, SettingsLoading, SettingsPage } from "../../../frontend/src/features/settings/SettingsUI";
import type { AccountSettingsRuntimePlugin } from "../../../frontend/src/plugins/runtime";
import type { Mailbox, User } from "../../../frontend/src/types";
import "./styles.css";

const apiBase = "/api/plugins/calendar_invites";

type SettingsContext = {
  csrf: string;
  user: User;
  mailboxes: Mailbox[];
  navigate: (url: string) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
};

type Invite = {
  id: number;
  message_id: number;
  ics_uid: string;
  sequence: number;
  summary: string;
  dtstart: string;
  dtend: string;
  organizer_address: string;
  organizer_name: string;
  attendee_address: string;
  attendee_name: string;
  status: string;
  has_ics: boolean;
  created_at: number;
  updated_at: number;
};

type CalDAVConfig = {
  server_url: string;
  username: string;
  has_password: boolean;
  calendar_url: string;
  enabled: boolean;
  last_error: string;
};

type CalendarOption = {
  url: string;
  display_name: string;
};

const partStats = [
  { value: "ACCEPTED", label: "Accept", className: "accept" },
  { value: "TENTATIVE", label: "Tentative", className: "tentative" },
  { value: "DECLINED", label: "Decline", className: "decline" }
];

function numberOr(value: unknown, fallback = 0): number {
  const n = typeof value === "string" ? Number(value) : value;
  return typeof n === "number" && Number.isFinite(n) ? n : fallback;
}

function normalizeInvite(value: any): Invite {
  return {
    id: numberOr(value.id),
    message_id: numberOr(value.message_id),
    ics_uid: String(value.ics_uid || ""),
    sequence: numberOr(value.sequence),
    summary: String(value.summary || ""),
    dtstart: String(value.dtstart || ""),
    dtend: String(value.dtend || ""),
    organizer_address: String(value.organizer_address || ""),
    organizer_name: String(value.organizer_name || ""),
    attendee_address: String(value.attendee_address || ""),
    attendee_name: String(value.attendee_name || ""),
    status: String(value.status || "pending"),
    has_ics: Boolean(value.has_ics),
    created_at: numberOr(value.created_at),
    updated_at: numberOr(value.updated_at)
  };
}

function normalizeConfig(value: any): CalDAVConfig {
  return {
    server_url: String(value.server_url || ""),
    username: String(value.username || ""),
    has_password: Boolean(value.has_password),
    calendar_url: String(value.calendar_url || ""),
    enabled: Boolean(value.enabled),
    last_error: String(value.last_error || "")
  };
}

function statusLabel(status: string): string {
  switch (status) {
    case "accepted": return "Accepted";
    case "tentative": return "Tentative";
    case "declined": return "Declined";
    default: return "Pending";
  }
}

function formatWhen(invite: Invite): string {
  if (!invite.dtstart) return "";
  if (invite.dtend) return `${invite.dtstart} to ${invite.dtend}`;
  return invite.dtstart;
}

function CalendarInvitesSettings({ csrf, navigate, addToast }: SettingsContext) {
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [invites, setInvites] = useState<Invite[]>([]);
  const [busyInvite, setBusyInvite] = useState<number | null>(null);
  const [inviteError, setInviteError] = useState<Record<number, string>>({});

  const [config, setConfig] = useState<CalDAVConfig | null>(null);
  const [serverUrl, setServerUrl] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [calendarUrl, setCalendarUrl] = useState("");
  const [enabled, setEnabled] = useState(false);
  const [calendars, setCalendars] = useState<CalendarOption[]>([]);
  const [discoverError, setDiscoverError] = useState("");
  const [configError, setConfigError] = useState("");
  const [busyConfig, setBusyConfig] = useState("");

  const request = useCallback(async (path: string, init: RequestInit = {}) => {
    const response = await fetch(`${apiBase}${path}`, {
      ...init,
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf, ...(init.headers || {}) }
    });
    let body: any = null;
    try {
      body = await response.json();
    } catch {
      body = null;
    }
    if (!response.ok) {
      const error = new Error(body?.error || `Request failed (${response.status})`) as Error & { status?: number };
      error.status = response.status;
      throw error;
    }
    return body;
  }, [csrf]);

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError("");
    try {
      const [inviteBody, configBody] = await Promise.all([
        request("/invites", { headers: { "X-CSRF-Token": csrf } }),
        request("/caldav/config", { headers: { "X-CSRF-Token": csrf } })
      ]);
      setInvites((inviteBody.invites || []).map(normalizeInvite));
      const cfg = normalizeConfig(configBody.config || {});
      setConfig(cfg);
      setServerUrl(cfg.server_url);
      setUsername(cfg.username);
      setCalendarUrl(cfg.calendar_url);
      setEnabled(cfg.enabled);
    } catch (error) {
      setLoadError(error instanceof Error ? error.message : "Could not load calendar invites.");
    } finally {
      setLoading(false);
    }
  }, [request, csrf]);

  useEffect(() => {
    void load();
  }, [load]);

  const rsvp = async (invite: Invite, partstat: string) => {
    setBusyInvite(invite.id);
    setInviteError((current) => ({ ...current, [invite.id]: "" }));
    try {
      await request(`/invites/${invite.id}/rsvp`, {
        method: "POST",
        body: JSON.stringify({ partstat, sequence: invite.sequence })
      });
      addToast(partstat === "ACCEPTED" ? "Invitation accepted" : partstat === "TENTATIVE" ? "Invitation tentatively accepted" : "Invitation declined", "success");
      await load();
    } catch (error) {
      const status = (error as Error & { status?: number }).status;
      if (status === 409) {
        setInviteError((current) => ({
          ...current,
          [invite.id]: "A newer version of this invitation arrived. The list was refreshed; answer again."
        }));
        await load();
      } else {
        setInviteError((current) => ({
          ...current,
          [invite.id]: error instanceof Error ? error.message : "RSVP failed."
        }));
      }
    } finally {
      setBusyInvite(null);
    }
  };

  const pushToCalendar = async (invite: Invite) => {
    setBusyInvite(invite.id);
    setInviteError((current) => ({ ...current, [invite.id]: "" }));
    try {
      await request(`/invites/${invite.id}/push`, { method: "POST" });
      addToast("Invitation pushed to your calendar", "success");
    } catch (error) {
      setInviteError((current) => ({
        ...current,
        [invite.id]: error instanceof Error ? error.message : "Push failed."
      }));
    } finally {
      setBusyInvite(null);
    }
  };

  const discover = async () => {
    setBusyConfig("discover");
    setDiscoverError("");
    try {
      const body = await request("/caldav/discover", {
        method: "POST",
        body: JSON.stringify({ server_url: serverUrl, username, password })
      });
      const options: CalendarOption[] = (body.calendars || []).map((cal: any) => ({
        url: String(cal.url || ""),
        display_name: String(cal.display_name || cal.url || "")
      }));
      setCalendars(options);
      if (options.length === 1) {
        setCalendarUrl(options[0].url);
      }
      if (options.length === 0) {
        setDiscoverError("No calendars found on this server.");
      }
    } catch (error) {
      setDiscoverError(error instanceof Error ? error.message : "Discovery failed.");
    } finally {
      setBusyConfig("");
    }
  };

  const saveConfig = async (event: FormEvent) => {
    event.preventDefault();
    setBusyConfig("save");
    setConfigError("");
    try {
      const body = await request("/caldav/config", {
        method: "POST",
        body: JSON.stringify({
          server_url: serverUrl,
          username,
          password,
          calendar_url: calendarUrl,
          enabled
        })
      });
      const cfg = normalizeConfig(body.config || {});
      setConfig(cfg);
      setPassword("");
      addToast("CalDAV settings saved", "success");
    } catch (error) {
      setConfigError(error instanceof Error ? error.message : "Could not save settings.");
    } finally {
      setBusyConfig("");
    }
  };

  const testConnection = async () => {
    setBusyConfig("test");
    setConfigError("");
    try {
      const body = await request("/caldav/test", {
        method: "POST",
        body: JSON.stringify({ server_url: serverUrl, username, password })
      });
      addToast(`Connected: ${body.calendars ?? 0} calendar(s) found`, "success");
      await load();
    } catch (error) {
      setConfigError(error instanceof Error ? error.message : "Connection test failed.");
      await load();
    } finally {
      setBusyConfig("");
    }
  };

  if (loading) {
    return (
      <SettingsPage title="Calendar invites" description="Answer meeting invitations and push them to your calendar." navigate={navigate}>
        <SettingsLoading label="Loading invitations..." />
      </SettingsPage>
    );
  }

  if (loadError) {
    return (
      <SettingsPage title="Calendar invites" description="Answer meeting invitations and push them to your calendar." navigate={navigate}>
        <SettingsError message={loadError} onRetry={() => void load()} />
      </SettingsPage>
    );
  }

  return (
    <SettingsPage
      title="Calendar invites"
      description="Meeting invitations detected in your mail. Replies are queued through your outbox."
      navigate={navigate}
    >
      <section className="calendar-invites-section">
        <div className="calendar-invites-section-head">
          <h2>Invitations</h2>
        </div>
        {invites.length === 0 ? (
          <SettingsEmpty
            icon="calendar"
            title="No invitations yet"
            description="Meeting invitations received while this plugin is enabled will show up here."
          />
        ) : (
          <ul className="calendar-invites-list">
            {invites.map((invite) => (
              <li key={invite.id} className="calendar-invites-invite">
                <div className="calendar-invites-invite-head">
                  <strong>{invite.summary || "(No subject)"}</strong>
                  <span className={`calendar-invites-status calendar-invites-status-${invite.status}`}>
                    {statusLabel(invite.status)}
                  </span>
                </div>
                {formatWhen(invite) ? (
                  <div className="calendar-invites-meta"><Icon name="clock" /> {formatWhen(invite)}</div>
                ) : null}
                {invite.organizer_address ? (
                  <div className="calendar-invites-meta">
                    From: {invite.organizer_name ? `${invite.organizer_name} <${invite.organizer_address}>` : invite.organizer_address}
                  </div>
                ) : null}
                {invite.attendee_address ? (
                  <div className="calendar-invites-meta">
                    To: {invite.attendee_name ? `${invite.attendee_name} <${invite.attendee_address}>` : invite.attendee_address}
                  </div>
                ) : null}
                {inviteError[invite.id] ? (
                  <p className="calendar-invites-error" role="alert">{inviteError[invite.id]}</p>
                ) : null}
                <div className="calendar-invites-actions">
                  {partStats.map((stat) => (
                    <button
                      key={stat.value}
                      type="button"
                      className={`calendar-invites-rsvp calendar-invites-rsvp-${stat.className}`}
                      disabled={busyInvite === invite.id}
                      onClick={() => void rsvp(invite, stat.value)}
                    >
                      {stat.label}
                    </button>
                  ))}
                  {config?.enabled && config.calendar_url ? (
                    <button
                      type="button"
                      className="secondary"
                      disabled={busyInvite === invite.id}
                      onClick={() => void pushToCalendar(invite)}
                    >
                      <Icon name="calendar" /> Push to calendar
                    </button>
                  ) : null}
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="calendar-invites-section">
        <div className="calendar-invites-section-head">
          <h2>CalDAV push</h2>
        </div>
        <p className="calendar-invites-hint">
          Optionally copy invitations to your CalDAV calendar.
          Your password is stored encrypted and used only to connect to your configured calendar server.
        </p>
        {config?.last_error ? (
          <p className="calendar-invites-error" role="alert">Last error: {config.last_error}</p>
        ) : null}
        {configError ? (
          <p className="calendar-invites-error" role="alert">{configError}</p>
        ) : null}
        <form onSubmit={saveConfig} className="calendar-invites-form">
          <label>
            <span>CalDAV server URL</span>
            <input
              type="url"
              value={serverUrl}
              onChange={(event) => setServerUrl(event.target.value)}
              placeholder="https://cloud.example.com/remote.php/dav"
              autoComplete="url"
            />
          </label>
          <label>
            <span>Username</span>
            <input
              type="text"
              value={username}
              onChange={(event) => setUsername(event.target.value)}
              autoComplete="username"
            />
          </label>
          <label>
            <span>Password {config?.has_password ? "(saved; leave blank to keep)" : ""}</span>
            <input
              type="password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              autoComplete="new-password"
              placeholder={config?.has_password ? "Saved" : ""}
            />
          </label>
          <label>
            <span>Calendar</span>
            <select value={calendarUrl} onChange={(event) => setCalendarUrl(event.target.value)}>
              <option value="">Choose a calendar</option>
              {calendars.map((cal) => (
                <option key={cal.url} value={cal.url}>{cal.display_name}</option>
              ))}
              {calendarUrl && !calendars.some((cal) => cal.url === calendarUrl) ? (
                <option value={calendarUrl}>{calendarUrl}</option>
              ) : null}
            </select>
          </label>
          {discoverError ? (
            <p className="calendar-invites-error" role="alert">{discoverError}</p>
          ) : null}
          <label className="calendar-invites-checkbox">
            <input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} />
            <span>Enable CalDAV push</span>
          </label>
          <div className="actions calendar-invites-form-actions">
            <button type="submit" disabled={busyConfig === "save"}>
              <Icon name="check" />{busyConfig === "save" ? "Saving..." : "Save"}
            </button>
            <button type="button" className="secondary" disabled={busyConfig === "discover"} onClick={() => void discover()}>
              <Icon name="search" />{busyConfig === "discover" ? "Discovering..." : "Discover calendars"}
            </button>
            <button type="button" className="secondary" disabled={busyConfig === "test"} onClick={() => void testConnection()}>
              <Icon name="send" />{busyConfig === "test" ? "Testing..." : "Test connection"}
            </button>
          </div>
        </form>
      </section>
    </SettingsPage>
  );
}

export default {
  accountSettingsRoutes: [
    {
      path: "/settings/account/plugins/calendar-invites",
      title: "Calendar invites",
      label: "Calendar invites",
      description: "Answer meeting invitations and push them to a CalDAV calendar.",
      icon: "calendar",
      section: "plugins",
      render: (context: SettingsContext) => <CalendarInvitesSettings {...context} />
    }
  ]
} satisfies AccountSettingsRuntimePlugin;
