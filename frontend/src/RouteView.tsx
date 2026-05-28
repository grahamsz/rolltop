// File overview: Small route switch for the single-page app. It translates the parsed location
// into feature views while passing only the shared state each view needs.

import type { LocationState, PGPUnlockState, Toast } from "./appTypes";
import type { Bootstrap, Mailbox, SyncRun, ThemeDefinition, User } from "./types";
import type { ClientSidePGPPlugin } from "../../plugins/client_side_pgp/frontend/types";
import { MailView, SearchView } from "./features/mail/MailViews";
import { ThreadView } from "./features/mail/ThreadView";
import { ComposePage } from "./features/compose/ComposeViews";
import { ContactsView } from "./features/contacts/ContactsView";
import { SettingsView, AdminUsersView, SyncRunView } from "./features/settings/SettingsViews";
import type { RuntimePlugins } from "./plugins/runtime";

/**
 * RouteView is the app's manual router. Each branch maps one URL family to a
 * feature view and passes shared chrome state downward without letting features
 * import App-level bootstrap or navigation state directly.
 */
export function RouteView({
  csrf,
  user,
  mailboxes,
  latestSyncRun,
  activeSyncRuns,
  enabledPlugins,
  availableThemes,
  location,
  navigate,
  hiddenMessageIDs,
  openCompose,
  refreshChrome,
  runtimePlugins,
  pgpPlugin,
  pgpUnlock,
  openPGPUnlock,
  addToast
}: {
  csrf: string;
  user: User;
  mailboxes: Mailbox[];
  latestSyncRun: SyncRun | null;
  activeSyncRuns: SyncRun[];
  enabledPlugins: string[];
  availableThemes: ThemeDefinition[];
  location: LocationState;
  navigate: (url: string) => void;
  hiddenMessageIDs: Set<number>;
  openCompose: (query?: string) => void;
  refreshChrome: () => Promise<Bootstrap | null>;
  runtimePlugins: RuntimePlugins;
  pgpPlugin?: ClientSidePGPPlugin;
  pgpUnlock: PGPUnlockState;
  openPGPUnlock: (identityID?: number, onUnlocked?: (state: PGPUnlockState) => void, recipientKeyIDs?: string[]) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const pgpEnabled = enabledPlugins.includes("client_side_pgp") && Boolean(pgpPlugin);
  if (location.path === "/search" || location.path.startsWith("/search/")) {
    return <SearchView csrf={csrf} location={location} navigate={navigate} hiddenMessageIDs={hiddenMessageIDs} datePrefs={user} activeSyncRuns={activeSyncRuns} messageSecurityPlugins={runtimePlugins.all} addToast={addToast} />;
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
        messageSecurityPlugins={runtimePlugins.all}
        pgpPlugin={pgpPlugin}
        pgpUnlock={pgpUnlock}
        openPGPUnlock={openPGPUnlock}
        addToast={addToast}
      />
    );
  }
  if (location.path === "/compose") {
    return <ComposePage csrf={csrf} location={location} navigate={navigate} pgpEnabled={pgpEnabled} pgpPlugin={pgpPlugin} pgpUnlock={pgpUnlock} openPGPUnlock={openPGPUnlock} addToast={addToast} />;
  }
  if (location.path === "/contacts") {
    return <ContactsView csrf={csrf} pgpEnabled={pgpEnabled} pgpPlugin={pgpPlugin} addToast={addToast} />;
  }
  if (location.path === "/settings/account" || location.path.startsWith("/settings/account/")) {
    return <SettingsView csrf={csrf} user={user} mailboxes={mailboxes} activeSyncRuns={activeSyncRuns} availableThemes={availableThemes} location={location} navigate={navigate} refreshChrome={refreshChrome} pgpEnabled={pgpEnabled} pgpPlugin={pgpPlugin} addToast={addToast} />;
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
      messageSecurityPlugins={runtimePlugins.all}
    />
  );
}
