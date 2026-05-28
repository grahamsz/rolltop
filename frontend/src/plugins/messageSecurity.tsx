// File overview: Protocol-neutral frontend hooks for message security plugins.

import { Fragment } from "react";
import type { ReactNode } from "react";
import type { Message } from "../types";
import type { RuntimePlugin } from "./runtime";

export type MessageSecurityState = Pick<Message, "is_encrypted" | "is_signed">;

export type MessageSecurityIndicatorContext = {
  location: "message-list" | "thread-summary";
  message: Message;
  state: MessageSecurityState;
};

export type MessageSecurityRuntimePlugin = RuntimePlugin & {
  messageSecurityPreviewText?: (snippet: string, state: MessageSecurityState) => string;
  messageSecuritySnippetClassName?: (state: MessageSecurityState) => string;
  renderMessageSecurityIndicators?: (context: MessageSecurityIndicatorContext) => ReactNode;
};

export function messageHasSecurityState(state: MessageSecurityState) {
  return Boolean(state.is_encrypted || state.is_signed);
}

export function messageSecurityPreviewText(plugins: readonly RuntimePlugin[], snippet: string, state: MessageSecurityState) {
  if (!messageHasSecurityState(state)) return snippet;
  for (const plugin of securityPlugins(plugins)) {
    const next = plugin.messageSecurityPreviewText?.(snippet, state);
    if (typeof next === "string") return next;
  }
  return snippet;
}

export function messageSecuritySnippetClassName(plugins: readonly RuntimePlugin[], state: MessageSecurityState) {
  if (!messageHasSecurityState(state)) return "";
  const classes = securityPlugins(plugins)
    .map((plugin) => plugin.messageSecuritySnippetClassName?.(state) || "")
    .filter(Boolean);
  return classes.join(" ");
}

export function messageSecurityIndicators(plugins: readonly RuntimePlugin[], context: MessageSecurityIndicatorContext) {
  if (!messageHasSecurityState(context.state)) return null;
  const nodes = securityPlugins(plugins)
    .map((plugin) => plugin.renderMessageSecurityIndicators?.(context))
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

function securityPlugins(plugins: readonly RuntimePlugin[]): MessageSecurityRuntimePlugin[] {
  return plugins.filter((plugin): plugin is MessageSecurityRuntimePlugin => {
    const candidate = plugin as MessageSecurityRuntimePlugin;
    return Boolean(
      candidate.messageSecurityPreviewText ||
      candidate.messageSecuritySnippetClassName ||
      candidate.renderMessageSecurityIndicators
    );
  });
}
