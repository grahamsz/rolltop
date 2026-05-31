// File overview: Frontend hooks for plugins that add identity security settings.

import { Fragment } from "react";
import type { ReactNode } from "react";
import type { Toast } from "../appTypes";
import type { MailIdentity, User } from "../types";
import type { RuntimePlugin } from "./runtime";

export type IdentitySecuritySettingsContext = {
  csrf: string;
  user: User;
  identities: MailIdentity[];
  identityDraft: MailIdentity;
  updateIdentityDraft: (patch: Partial<MailIdentity>) => void;
  markIdentitySecurityReady: (identityID: number) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
};

export type IdentitySecurityRuntimePlugin = RuntimePlugin & {
  renderIdentitySecuritySettings?: (context: IdentitySecuritySettingsContext) => ReactNode;
};

export function identitySecuritySettings(plugins: readonly RuntimePlugin[], context: IdentitySecuritySettingsContext) {
  const nodes = plugins
    .map((plugin) => (plugin as IdentitySecurityRuntimePlugin).renderIdentitySecuritySettings?.(context))
    .filter((node): node is ReactNode => Boolean(node));
  if (nodes.length === 0) return null;
  return (
    <>
      {nodes.map((node, index) => (
        <Fragment key={index}>{node}</Fragment>
      ))}
    </>
  );
}
