// File overview: Small route switch for the single-page app. It translates the parsed location
// into feature views while passing only the shared state each view needs.

import type React from "react";
import type { LocationState, SecurityUnlockState, Toast } from "./appTypes";
import type { Bootstrap, Mailbox, SyncRun, ThemeDefinition, User } from "./types";
import { MailView, SearchView, SnoozedView } from "./features/mail/MailViews";
import { ThreadView } from "./features/mail/ThreadView";
import { ComposePage } from "./features/compose/ComposeViews";
import { ContactsView } from "./features/contacts/ContactsView";
import { SettingsView, AdminUsersView, SyncRunView } from "./features/settings/SettingsViews";
import type { RuntimePlugin, RuntimePlugins } from "./plugins/runtime";
import { securityUnlockPlugin } from "./plugins/securityUnlock";

type AccountSettingsRoutePlugin = RuntimePlugin & {
  accountSettingsRoutes?: Array<{
    path: string;
    render: (context: {
      csrf: string;
      user: User;
      mailboxes: Mailbox[];
      navigate: (url: string) => void;
      addToast: (message: string, kind?: Toast["kind"]) => number;
    }) => React.ReactNode;
  }>;
};

function pluginAccountSettingsRoute(plugins: RuntimePlugin[], path: string, context: {
  csrf: string;
  user: User;
  mailboxes: Mailbox[];
  navigate: (url: string) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  for (const plugin of plugins as AccountSettingsRoutePlugin[]) {
    for (const route of plugin.accountSettingsRoutes || []) {
      if (route.path !== path) continue;
      const rendered = route.render(context);
      if (rendered) return rendered;
    }
  }
  return null;
}

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
  mailGeneration,
  enabledPlugins,
  availableThemes,
  location,
  navigate,
  hiddenMessageIDs,
  openCompose,
  refreshChrome,
  runtimePlugins,
  securityUnlock,
  openSecurityUnlock,
  addToast
}: {
  csrf: string;
  user: User;
  mailboxes: Mailbox[];
  latestSyncRun: SyncRun | null;
  activeSyncRuns: SyncRun[];
  mailGeneration: number;
  enabledPlugins: string[];
  availableThemes: ThemeDefinition[];
  location: LocationState;
  navigate: (url: string) => void;
  hiddenMessageIDs: Set<number>;
  openCompose: (query?: string) => void;
  refreshChrome: () => Promise<Bootstrap | null>;
  runtimePlugins: RuntimePlugins;
  securityUnlock: SecurityUnlockState;
  openSecurityUnlock: (identityID?: number, onUnlocked?: (state: SecurityUnlockState) => void, recipientKeyIDs?: string[], fallbackEmail?: string) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const securityEnabled = Boolean(securityUnlockPlugin(runtimePlugins.all));
  if (location.path === "/snoozes") {
    return <SnoozedView csrf={csrf} datePrefs={user} location={location} navigate={navigate} hiddenMessageIDs={hiddenMessageIDs} mailGeneration={mailGeneration} messageSecurityPlugins={runtimePlugins.all} addToast={addToast} />;
  }
  if (location.path === "/search" || location.path.startsWith("/search/")) {
    return <SearchView csrf={csrf} location={location} navigate={navigate} hiddenMessageIDs={hiddenMessageIDs} datePrefs={user} activeSyncRuns={activeSyncRuns} messageSecurityPlugins={runtimePlugins.all} searchActionPlugins={runtimePlugins.all} addToast={addToast} />;
  }
  if (location.path.startsWith("/messages/")) {
    return (
      <ThreadView
        userID={user.id}
        csrf={csrf}
        datePrefs={user}
        location={location}
        navigate={navigate}
        mailboxes={mailboxes}
        enabledPlugins={enabledPlugins}
        refreshChrome={refreshChrome}
        openCompose={openCompose}
        messageSecurityPlugins={runtimePlugins.all}
        securityUnlock={securityUnlock}
        openSecurityUnlock={openSecurityUnlock}
        addToast={addToast}
      />
    );
  }
  if (location.path === "/compose") {
    return <ComposePage userID={user.id} csrf={csrf} location={location} navigate={navigate} securityEnabled={securityEnabled} securityPlugins={runtimePlugins.all} securityUnlock={securityUnlock} openSecurityUnlock={openSecurityUnlock} addToast={addToast} />;
  }
  if (location.path === "/contacts") {
    return <ContactsView csrf={csrf} contactPlugins={runtimePlugins.all} addToast={addToast} />;
  }
  if (location.path.startsWith("/settings/account/")) {
    const rendered = pluginAccountSettingsRoute(runtimePlugins.all, location.path, { csrf, user, mailboxes, navigate, addToast });
    if (rendered) return rendered;
  }
  if (location.path === "/settings/account" || location.path.startsWith("/settings/account/")) {
    return <SettingsView csrf={csrf} user={user} mailboxes={mailboxes} activeSyncRuns={activeSyncRuns} availableThemes={availableThemes} location={location} navigate={navigate} refreshChrome={refreshChrome} identitySecurityPlugins={runtimePlugins.all} addToast={addToast} />;
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
      mailGeneration={mailGeneration}
      refreshChrome={refreshChrome}
      addToast={addToast}
      messageSecurityPlugins={runtimePlugins.all}
    />
  );
}
