// File overview: Frontend hooks for plugins that add compose-time message security.

import { useEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import type { SecurityUnlockState, Toast } from "../appTypes";
import type { ComposeAttachmentUpload, ComposeExistingAttachment, ComposeForm, ComposeIdentity, ContactPGPKey } from "../types";
import { Icon } from "../components/Icon";
import { messageFromError } from "../lib/errors";
import type { RuntimePlugin } from "./runtime";

type ComposeSecurityAttachment = ComposeAttachmentUpload & {
  id: string;
  file: File;
};

type ComposeSecurityTransformState = {
  active: boolean;
  phase: "plaintext" | "ciphertext";
  ciphertext: string;
};

type ComposeSecurityChoice = "plain" | "signed" | "signed_encrypted";

export type ComposeSecurityUnlockState = SecurityUnlockState;
export type OpenComposeSecurityUnlock = (identityID?: number, onUnlocked?: (state: ComposeSecurityUnlockState) => void, recipientKeyIDs?: string[], fallbackEmail?: string) => void;

type ComposeSecurityRuntimePlugin = RuntimePlugin & {
  publicKeys: (emails: string[], all?: boolean) => Promise<{ keys: ContactPGPKey[] }>;
  encryptionKeyRecordsForRecipients: (recipientEmails: string[], candidateKeys: ContactPGPKey[]) => Promise<ContactPGPKey[]>;
  pgpMIMEEntityFromBody: (text: string, html: string, attachments?: PGPMIMEAttachmentInput[]) => string;
  encryptMessageText: (text: string, recipientKeys: ContactPGPKey[], signingKey?: SecurityUnlockState["keys"][number]) => Promise<string>;
  signPGPMIMEEntity: (entity: string, signingKey: SecurityUnlockState["keys"][number]) => Promise<string>;
  addAutocryptGossipHeaders: (payload: string, keys: ContactPGPKey[]) => string;
};

type PGPMIMEAttachmentInput = {
  filename: string;
  contentType: string;
  contentID?: string;
  inline?: boolean;
  data: Uint8Array;
};

export function useComposeSecurity({
  enabled,
  plugins,
  initial,
  selectedIdentity,
  recipientEmails,
  includedExistingAttachments,
  forwardedMessageAttachment,
  unlockState,
  openUnlock,
  addToast
}: {
  enabled: boolean;
  plugins: readonly RuntimePlugin[];
  initial: ComposeForm;
  selectedIdentity: ComposeIdentity | null | undefined;
  recipientEmails: string[];
  includedExistingAttachments: ComposeExistingAttachment[];
  forwardedMessageAttachment: ComposeExistingAttachment | null;
  unlockState: SecurityUnlockState;
  openUnlock: OpenComposeSecurityUnlock;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const plugin = plugins.find(isComposeSecurityPlugin);
  const [transform, setTransform] = useState<ComposeSecurityTransformState>({ active: false, phase: "plaintext", ciphertext: "" });
  const [encrypt, setEncrypt] = useState(Boolean(initial.pgp_encrypted));
  const [sign, setSign] = useState(Boolean(initial.pgp_signed));
  const [attachPublicKey, setAttachPublicKey] = useState(Boolean(initial.attach_public_key));
  const [sendPromptOpen, setSendPromptOpen] = useState(false);
  const [sendSuggestionAvailable, setSendSuggestionAvailable] = useState(false);
  const sendChoiceBypassRef = useRef(false);
  const active = enabled && Boolean(plugin) && (encrypt || sign);
  const selectedIdentityCanEncrypt = Boolean(selectedIdentity?.has_pgp_private_key && selectedIdentity?.pgp_public_key_armored?.trim());
  const unlockedSigningKey = selectedIdentity
    ? unlockState.keys.find((key) => key.identity_id === selectedIdentity.pgp_identity_id) || null
    : null;
  const recipientEmailKey = recipientEmails.join("|");

  useEffect(() => {
    setEncrypt(Boolean(initial.pgp_encrypted));
    setSign(Boolean(initial.pgp_signed));
    setAttachPublicKey(Boolean(initial.attach_public_key));
    setSendPromptOpen(false);
    setSendSuggestionAvailable(false);
    setTransform({ active: false, phase: "plaintext", ciphertext: "" });
  }, [initial]);

  useEffect(() => {
    let cancelled = false;
    let timer = 0;
    if (!enabled || active || !plugin || !selectedIdentityCanEncrypt || recipientEmails.length === 0) {
      setSendSuggestionAvailable(false);
      setSendPromptOpen(false);
      return;
    }
    timer = window.setTimeout(() => {
      plugin.publicKeys(recipientEmails, true)
        .then(async (data) => {
          await plugin.encryptionKeyRecordsForRecipients(recipientEmails, data.keys || []);
          if (!cancelled) setSendSuggestionAvailable(true);
        })
        .catch(() => {
          if (!cancelled) setSendSuggestionAvailable(false);
        });
    }, 350);
    return () => {
      cancelled = true;
      if (timer) window.clearTimeout(timer);
    };
  }, [active, enabled, plugin, recipientEmailKey, selectedIdentityCanEncrypt]);

  function sendButtonLabel(sending: boolean) {
    return sending
      ? active ? "Preparing PGP..." : "Sending..."
      : encrypt && sign ? "Sign, Encrypt & Send"
        : encrypt ? "Encrypt & Send"
          : sign ? "Sign & Send"
            : "Send";
  }

  function beginSubmit(formRef: RefObject<HTMLFormElement | null>) {
    if (sendSuggestionAvailable && !active && !sendChoiceBypassRef.current) {
      setSendPromptOpen(true);
      return false;
    }
    sendChoiceBypassRef.current = false;
    if (enabled && sign && !unlockedSigningKey) {
      if (!selectedIdentity?.has_pgp_private_key) {
        addToast("Add a PGP private key to this identity before signing.", "error");
        return false;
      }
      openUnlock(selectedIdentity.pgp_identity_id || undefined, () => {
        window.setTimeout(() => formRef.current?.requestSubmit(), 0);
      });
      return false;
    }
    return true;
  }

  function chooseSend(choice: ComposeSecurityChoice, formRef: RefObject<HTMLFormElement | null>) {
    setSendPromptOpen(false);
    sendChoiceBypassRef.current = true;
    if (choice === "plain") {
      setEncrypt(false);
      setSign(false);
    } else if (choice === "signed") {
      setEncrypt(false);
      setSign(true);
      setAttachPublicKey(false);
    } else {
      setEncrypt(true);
      setSign(true);
      setAttachPublicKey(false);
    }
    window.setTimeout(() => formRef.current?.requestSubmit(), 0);
  }

  async function prepareSubmitForm(nextForm: ComposeForm, uploadAttachments: ComposeSecurityAttachment[], onArmored?: (armored: string) => void) {
    if (!enabled || !plugin || (!encrypt && !sign)) {
      return { form: { ...nextForm, pgp_encrypted: false, pgp_signed: false, pgp_mime: false }, attachments: uploadAttachments };
    }
    if (sign && !unlockedSigningKey) {
      openUnlock(selectedIdentity?.pgp_identity_id || undefined);
      throw new Error("Unlock this identity's PGP key before signing.");
    }
    if (encrypt && !selectedIdentityCanEncrypt) {
      throw new Error("Add an active PGP encryption key to this identity before encrypting. Rolltop encrypts a copy to your own key so sent mail stays readable.");
    }
    let armored = nextForm.body_html.trim() ? nextForm.body_html : nextForm.body;
    let pgpMime = false;
    let pgpSignature = "";
    const pgpAttachments = await pgpMIMEAttachments(uploadAttachments);
    if (encrypt) {
      let data;
      try {
        data = await plugin.publicKeys(recipientEmails, true);
      } catch (err) {
        throw new Error(`Could not load recipient PGP public keys: ${messageFromError(err)}`);
      }
      const recipientKeys = await plugin.encryptionKeyRecordsForRecipients(recipientEmails, data.keys || []);
      const keys = encryptionKeysWithSender(recipientKeys);
      pgpMime = true;
      const mimeEntity = plugin.pgpMIMEEntityFromBody(nextForm.body, nextForm.body_html, pgpAttachments);
      armored = await plugin.encryptMessageText(plugin.addAutocryptGossipHeaders(mimeEntity, keys), keys, sign ? unlockedSigningKey || undefined : undefined);
    } else if (sign && unlockedSigningKey) {
      pgpMime = true;
      const mimeEntity = plugin.pgpMIMEEntityFromBody(nextForm.body, nextForm.body_html, pgpAttachments);
      armored = mimeEntity;
      pgpSignature = await plugin.signPGPMIMEEntity(mimeEntity, unlockedSigningKey);
    }
    onArmored?.(armored);
    return {
      form: { ...nextForm, body: armored, body_html: "", include_attachment_ids: [], forward_attachment_message_id: 0, attach_public_key: false, pgp_encrypted: encrypt, pgp_signed: sign, pgp_mime: pgpMime, pgp_signature: pgpSignature },
      attachments: [] as ComposeSecurityAttachment[]
    };
  }

  async function pgpMIMEAttachments(uploadAttachments: ComposeSecurityAttachment[]): Promise<PGPMIMEAttachmentInput[]> {
    const out: PGPMIMEAttachmentInput[] = [];
    for (const attachment of uploadAttachments) {
      out.push({
        filename: attachment.filename,
        contentType: attachment.content_type,
        contentID: attachment.content_id,
        inline: attachment.inline,
        data: new Uint8Array(await attachment.file.arrayBuffer())
      });
    }
    for (const attachment of includedExistingAttachments) {
      out.push(await pgpMIMEAttachmentFromExisting(attachment));
    }
    if (forwardedMessageAttachment) {
      out.push(await pgpMIMEAttachmentFromExisting(forwardedMessageAttachment));
    }
    if (attachPublicKey && selectedIdentity?.pgp_public_key_armored?.trim()) {
      const filename = `OpenPGP_${(selectedIdentity.email || "public-key").replace(/[^A-Za-z0-9_.-]/g, "_")}.asc`;
      out.push({
        filename,
        contentType: "application/pgp-keys",
        data: new TextEncoder().encode(selectedIdentity.pgp_public_key_armored.trim() + "\n")
      });
    }
    return out;
  }

  function encryptionKeysWithSender(keys: ContactPGPKey[]): ContactPGPKey[] {
    const selfArmored = selectedIdentity?.pgp_public_key_armored?.trim() || "";
    if (!selfArmored) return keys;
    if (keys.some((key) => key.public_key_armored.trim() === selfArmored)) return keys;
    return [
      ...keys,
      {
        email: selectedIdentity?.email || "",
        label: selectedIdentity?.email ? `${selectedIdentity.email} sender key` : "Sender key",
        fingerprint: "",
        key_id: "",
        user_ids: selectedIdentity?.header || selectedIdentity?.email || "",
        public_key_armored: selfArmored,
        is_preferred: false
      }
    ];
  }

  return {
    active,
    attachPublicKey,
    transform,
    setTransform,
    sendButtonLabel,
    beginSubmit,
    prepareSubmitForm,
    renderSendChoice(formRef: RefObject<HTMLFormElement | null>) {
      if (!sendPromptOpen) return null;
      return (
        <div className="compose-pgp-send-choice" role="dialog" aria-label="PGP send options">
          <span>PGP keys are available for every recipient.</span>
          <button className="secondary" type="button" onClick={() => chooseSend("plain", formRef)}>Send unencrypted</button>
          <button className="secondary" type="button" onClick={() => chooseSend("signed", formRef)}>Sign only</button>
          <button type="button" onClick={() => chooseSend("signed_encrypted", formRef)}>Sign & encrypt</button>
        </div>
      );
    },
    renderControls({ attachmentInputRef, inlineMediaInputRef }: {
      attachmentInputRef: RefObject<HTMLInputElement | null>;
      inlineMediaInputRef: RefObject<HTMLInputElement | null>;
    }) {
      const suggestionPulse = sendSuggestionAvailable && !active;
      return (
        <>
          <button className="ghost" type="button" title={active ? "Attach files inside the PGP/MIME message" : "Attach files"} onClick={() => attachmentInputRef.current?.click()}>
            <Icon name="attach_file" />
          </button>
          <button className="ghost" type="button" title={active ? "Insert inline media inside the PGP/MIME message" : "Insert inline media"} onClick={() => inlineMediaInputRef.current?.click()}>
            <Icon name="image" />
          </button>
          {enabled && plugin ? (
            <div className={`compose-pgp-bar ${suggestionPulse ? "suggested" : ""}`} aria-label="PGP options" title="PGP protects the message body. Subject, recipients, dates, and other headers remain visible.">
              <span className="compose-pgp-label">PGP:</span>
              <button
                className={`ghost icon-toggle pgp-security-option ${encrypt ? "active" : ""}`}
                type="button"
                title={selectedIdentityCanEncrypt ? "Encrypt with recipient public keys and your own identity key" : "Add an active PGP encryption key to this identity before encrypting"}
                aria-label="Encrypt with PGP"
                aria-pressed={encrypt}
                disabled={!selectedIdentityCanEncrypt}
                onClick={() => setEncrypt((value) => { const next = !value; if (next) setAttachPublicKey(false); return next; })}
              >
                <Icon name="lock" weight={encrypt ? "bold" : "regular"} />
              </button>
              <button
                className={`ghost icon-toggle pgp-security-option ${sign ? "active" : ""}`}
                type="button"
                title={selectedIdentity?.has_pgp_private_key ? "Sign with your unlocked private key" : "Add a PGP private key to this identity before signing"}
                aria-label="Sign with PGP"
                aria-pressed={sign}
                disabled={!selectedIdentity?.has_pgp_private_key}
                onClick={() => setSign((value) => { const next = !value; if (next) setAttachPublicKey(false); return next; })}
              >
                <Icon name="signature" weight={sign ? "bold" : "regular"} />
              </button>
              <button
                className={`ghost icon-toggle ${attachPublicKey ? "active" : ""}`}
                type="button"
                title={active ? "Attach your public key inside the PGP/MIME message" : selectedIdentity?.pgp_public_key_armored ? "Attach your public key" : "Add a PGP key to this identity before attaching a public key"}
                aria-label="Attach public key"
                aria-pressed={attachPublicKey}
                disabled={!selectedIdentity?.pgp_public_key_armored}
                onClick={() => setAttachPublicKey((value) => !value)}
              >
                <Icon name="key" weight={attachPublicKey ? "bold" : "regular"} />
              </button>
              {active ? <span className="compose-pgp-scope">PGP/MIME</span> : null}
            </div>
          ) : null}
        </>
      );
    }
  };
}

async function pgpMIMEAttachmentFromExisting(attachment: ComposeExistingAttachment): Promise<PGPMIMEAttachmentInput> {
  const response = await fetch(attachment.download_url, { credentials: "same-origin" });
  if (!response.ok) {
    throw new Error(`Could not load ${composeExistingAttachmentName(attachment)} for PGP/MIME packaging.`);
  }
  return {
    filename: composeExistingAttachmentName(attachment),
    contentType: attachment.content_type || response.headers.get("Content-Type") || "application/octet-stream",
    data: new Uint8Array(await response.arrayBuffer())
  };
}

function composeExistingAttachmentName(attachment: ComposeExistingAttachment): string {
  return attachment.filename || attachment.content_type || "Attachment";
}

function isComposeSecurityPlugin(plugin: RuntimePlugin): plugin is ComposeSecurityRuntimePlugin {
  const candidate = plugin as Partial<ComposeSecurityRuntimePlugin>;
  return Boolean(
    candidate.publicKeys &&
    candidate.encryptionKeyRecordsForRecipients &&
    candidate.pgpMIMEEntityFromBody &&
    candidate.encryptMessageText &&
    candidate.signPGPMIMEEntity &&
    candidate.addAutocryptGossipHeaders
  );
}
