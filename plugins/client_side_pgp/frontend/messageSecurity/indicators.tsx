import { Lock, Signature } from "@phosphor-icons/react";
import type { MessageSecurityIndicatorContext, MessageSecurityState } from "../types";
import { pgpPreviewText } from "./preview";

export function pgpMessageSecurityPreviewText(snippet: string, state: MessageSecurityState) {
  return pgpPreviewText(snippet, state.is_encrypted, state.is_signed);
}

export function pgpMessageSecuritySnippetClassName(state: MessageSecurityState) {
  return state.is_encrypted ? "encrypted-preview" : "";
}

export function renderPGPMessageSecurityIndicators({ state }: MessageSecurityIndicatorContext) {
  if (!state.is_encrypted && !state.is_signed) return null;
  const label = [state.is_encrypted ? "Encrypted" : "", state.is_signed ? "Signed" : ""].filter(Boolean).join(", ");
  return (
    <span className="message-pgp-icons" aria-label={label}>
      {state.is_encrypted ? (
        <span className="message-pgp-icon encrypted" title="Encrypted message">
          <Lock className="icon" aria-hidden="true" focusable="false" weight="bold" />
        </span>
      ) : null}
      {state.is_signed ? (
        <span className="message-pgp-icon signature pending" title="Signature pending verification">
          <Signature className="icon" aria-hidden="true" focusable="false" weight="bold" />
        </span>
      ) : null}
    </span>
  );
}
