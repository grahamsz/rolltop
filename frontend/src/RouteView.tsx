import type { LocationState, Toast } from "./appTypes";
import type { Bootstrap, Mailbox, SyncRun, User } from "./types";
import { MailView, SearchView } from "./features/mail/MailViews";
import { ThreadView } from "./features/mail/ThreadView";
import { ComposePage } from "./features/compose/ComposeViews";
import { ContactsView } from "./features/contacts/ContactsView";
import { SettingsView, AdminUsersView, SyncRunView } from "./features/settings/SettingsViews";

export function RouteView({
  csrf,
  user,
  mailboxes,
  latestSyncRun,
  activeSyncRuns,
  enabledPlugins,
  location,
  navigate,
  hiddenMessageIDs,
  openCompose,
  refreshChrome,
  addToast
}: {
  csrf: string;
  user: User;
  mailboxes: Mailbox[];
  latestSyncRun: SyncRun | null;
  activeSyncRuns: SyncRun[];
  enabledPlugins: string[];
  location: LocationState;
  navigate: (url: string) => void;
  hiddenMessageIDs: Set<number>;
  openCompose: (query?: string) => void;
  refreshChrome: () => Promise<Bootstrap | null>;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  if (location.path === "/search" || location.path.startsWith("/search/")) {
    return <SearchView csrf={csrf} location={location} navigate={navigate} hiddenMessageIDs={hiddenMessageIDs} datePrefs={user} addToast={addToast} />;
  }
  if (location.path.startsWith("/messages/")) {
    return (
      <ThreadView
        csrf={csrf}
        datePrefs={user}
        location={location}
        navigate={navigate}
        mailboxes={mailboxes}
        enabledPlugins={enabledPlugins}
        refreshChrome={refreshChrome}
        openCompose={openCompose}
        addToast={addToast}
      />
    );
  }
  if (location.path === "/compose") {
    return <ComposePage csrf={csrf} location={location} navigate={navigate} addToast={addToast} />;
  }
  if (location.path === "/contacts") {
    return <ContactsView csrf={csrf} addToast={addToast} />;
  }
  if (location.path === "/settings/account" || location.path.startsWith("/settings/account/")) {
    return <SettingsView csrf={csrf} user={user} mailboxes={mailboxes} activeSyncRuns={activeSyncRuns} location={location} navigate={navigate} refreshChrome={refreshChrome} addToast={addToast} />;
  }
  if (location.path === "/admin/users" && user.is_admin) {
    return <AdminUsersView csrf={csrf} refreshChrome={refreshChrome} addToast={addToast} />;
  }
  if (location.path.startsWith("/sync-runs/")) {
    return <SyncRunView location={location} navigate={navigate} datePrefs={user} />;
  }
  return (
    <MailView
      csrf={csrf}
      datePrefs={user}
      location={location}
      navigate={navigate}
      hiddenMessageIDs={hiddenMessageIDs}
      mailboxes={mailboxes}
      latestSyncRun={latestSyncRun}
      activeSyncRuns={activeSyncRuns}
      refreshChrome={refreshChrome}
      addToast={addToast}
    />
  );
}
