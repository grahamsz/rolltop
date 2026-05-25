import type { ReactNode } from "react";
import type { Bootstrap, Mailbox, User } from "./types";

export type LocationState = {
  path: string;
  search: string;
};

export type Toast = {
  id: number;
  kind: "loading" | "success" | "error";
  message: string;
};

export type MoveTarget = {
  id: number;
  name: string;
};

export type DatePrefs = Pick<User, "date_locale" | "date_format">;

export type Navigate = (url: string) => void;
export type AddToast = (message: string, kind?: Toast["kind"]) => number;
export type RefreshChrome = () => Promise<Bootstrap | null>;

export type AppShellProps = {
  user: User;
  csrf: string;
  mailboxes: Mailbox[];
  latestSyncRun: import("./types").SyncRun | null;
  activeSyncRuns: import("./types").SyncRun[];
  syncRunning: boolean;
  accountNeedsPassword: boolean;
  accountNotice: string;
  enabledPlugins: string[];
  location: LocationState;
  navigate: Navigate;
  logout: () => void;
  onMoveMessages: (messageIDs: number[], mailbox: MoveTarget) => void;
  openCompose: (query?: string) => void;
  refreshChrome: RefreshChrome;
  addToast: AddToast;
  children: ReactNode;
};
