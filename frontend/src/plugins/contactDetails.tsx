// File overview: Frontend hooks for plugins that add contact detail editors.

import { Fragment } from "react";
import type { ReactNode } from "react";
import type { Toast } from "../appTypes";
import type { ContactEmail, ContactPGPKey } from "../types";
import type { RuntimePlugin } from "./runtime";

export type ContactKeyEditorContext = {
  csrf: string;
  contactID: number;
  emails: ContactEmail[];
  addToast: (message: string, kind?: Toast["kind"]) => number;
};

export type ContactDetailsRuntimePlugin = RuntimePlugin & {
  renderContactKeyEditor?: (context: ContactKeyEditorContext) => ReactNode;
};

export function contactKeyEditors(plugins: readonly RuntimePlugin[], context: ContactKeyEditorContext) {
  const nodes = plugins
    .map((plugin) => (plugin as ContactDetailsRuntimePlugin).renderContactKeyEditor?.(context))
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
