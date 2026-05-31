// File overview: Protocol-neutral unlock surface for frontend plugins that keep
// short-lived private key material available to compose/thread workflows.

import type { ComponentType } from "react";
import type { SecurityUnlockState, Toast } from "../appTypes";
import type { RuntimePlugin } from "./runtime";

export type SecurityUnlockDialogProps = {
  userID: number;
  identityID: number | null;
  recipientKeyIDs: string[];
  fallbackEmail?: string;
  onClose: () => void;
  onUnlocked: (state: SecurityUnlockState) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
};

export type SecurityUnlockRuntimePlugin = RuntimePlugin & {
  UnlockDialog: ComponentType<SecurityUnlockDialogProps>;
  serializeUnlockState(state: SecurityUnlockState): Promise<unknown>;
  restoreUnlockState(state: unknown): Promise<SecurityUnlockState>;
  unlockLabel?: string;
  lockLabel?: string;
  loadingLabel?: string;
  lockedToast?: string;
};

export const emptySecurityUnlockState: SecurityUnlockState = { unlockedUntil: 0, keys: [] };

export function securityUnlockPlugin(plugins: readonly RuntimePlugin[]): SecurityUnlockRuntimePlugin | undefined {
  return plugins.find((plugin) => {
    const candidate = plugin as Partial<SecurityUnlockRuntimePlugin>;
    return Boolean(candidate.UnlockDialog && candidate.serializeUnlockState && candidate.restoreUnlockState);
  }) as SecurityUnlockRuntimePlugin | undefined;
}
