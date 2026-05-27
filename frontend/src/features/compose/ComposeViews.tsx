// File overview: Compose, reply, and forward UI. It owns the editable body, recipient fields,
// identity choice, file uploads, inline media CIDs, and optional client-side photo resizing.

import { useEffect, useMemo, useRef, useState } from "react";
import type { ClipboardEvent, DragEvent, FormEvent } from "react";
import { api } from "../../api";
import type { LocationState, PGPUnlockState, Toast } from "../../appTypes";
import type { ContactAutocomplete, ComposeAttachmentUpload, ComposeExistingAttachment, ComposeForm, ComposeIdentity } from "../../types";
import { Icon } from "../../components/Icon";
import { messageFromError } from "../../lib/errors";
import { textToHTML } from "../../lib/html";
import { encryptMessageText, signMessageText } from "../../lib/pgp";

const ATTACHMENT_WARNING_BYTES = 20 * 1024 * 1024;
const RESIZE_PHOTO_MAX_EDGE = 1920;
const RESIZE_PHOTO_QUALITY = 0.82;

type ComposeAttachment = ComposeAttachmentUpload & {
  id: string;
  objectURL?: string;
};

/** Floating compose dialog used by the shell for new mail, replies, and forwards. */
export function ComposeOverlay({
  csrf,
  query,
  pgpEnabled,
  pgpUnlock,
  openPGPUnlock,
  addToast,
  onClose
}: {
  csrf: string;
  query: string;
  pgpEnabled: boolean;
  pgpUnlock: PGPUnlockState;
  openPGPUnlock: () => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
  onClose: () => void;
}) {
  const [form, setForm] = useState<ComposeForm | null>(null);
  const [from, setFrom] = useState("");
  const [identities, setIdentities] = useState<ComposeIdentity[]>([]);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    setForm(null);
    setError("");
    api
      .compose(query)
      .then((data) => {
        if (cancelled) return;
        setForm(data.compose);
        setFrom(data.compose_from);
        setIdentities(data.from_identities || []);
      })
      .catch((err) => {
        if (!cancelled) setError(messageFromError(err));
      });
    return () => {
      cancelled = true;
    };
  }, [query]);

  return (
    <div className="compose-popover" role="dialog" aria-label="Compose message">
      {error ? <div className="error">{error}</div> : null}
      {form ? (
        <ComposeBox
          csrf={csrf}
          composeFrom={from}
          identities={identities}
          initial={form}
          pgpEnabled={pgpEnabled}
          pgpUnlock={pgpUnlock}
          openPGPUnlock={openPGPUnlock}
          addToast={addToast}
          onSent={onClose}
          onCancel={onClose}
        />
      ) : (
        <div className="compose-window compose-loading">
          <div className="compose-head">
            <span>New Message</span>
            <button className="ghost" type="button" title="Close" onClick={onClose}>
              <Icon name="close" />
            </button>
          </div>
          <div className="panel muted">Loading compose...</div>
        </div>
      )}
    </div>
  );
}

/** Full-page compose route, mainly useful for direct links or narrow layouts. */
export function ComposePage({
  csrf,
  location,
  navigate,
  pgpEnabled,
  pgpUnlock,
  openPGPUnlock,
  addToast
}: {
  csrf: string;
  location: LocationState;
  navigate: (url: string) => void;
  pgpEnabled: boolean;
  pgpUnlock: PGPUnlockState;
  openPGPUnlock: () => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const [form, setForm] = useState<ComposeForm | null>(null);
  const [from, setFrom] = useState("");
  const [identities, setIdentities] = useState<ComposeIdentity[]>([]);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    api
      .compose(location.search.replace(/^\?/, ""))
      .then((data) => {
        if (cancelled) return;
        setForm(data.compose);
        setFrom(data.compose_from);
        setIdentities(data.from_identities || []);
      })
      .catch((err) => {
        if (!cancelled) setError(messageFromError(err));
      });
    return () => {
      cancelled = true;
    };
  }, [location.search]);

  return (
    <div className="compose-page">
      {error ? <div className="error">{error}</div> : null}
      {form ? (
        <ComposeBox
          csrf={csrf}
          composeFrom={from}
          identities={identities}
          initial={form}
          pgpEnabled={pgpEnabled}
          pgpUnlock={pgpUnlock}
          openPGPUnlock={openPGPUnlock}
          addToast={addToast}
          onSent={() => navigate("/mail")}
          onCancel={() => navigate("/mail")}
        />
      ) : (
        <div className="panel muted">Loading compose...</div>
      )}
    </div>
  );
}

/**
 * ComposeBox owns the mutable compose draft: contenteditable HTML, plain-text
 * fallback, From identity, recipient fields, files, inline media, and send state.
 */
export function ComposeBox({
  csrf,
  composeFrom,
  identities = [],
  initial,
  inline = false,
  pgpEnabled = false,
  pgpUnlock,
  openPGPUnlock,
  addToast,
  onSent,
  onCancel
}: {
  csrf: string;
  composeFrom: string;
  identities?: ComposeIdentity[];
  initial: ComposeForm;
  inline?: boolean;
  pgpEnabled?: boolean;
  pgpUnlock: PGPUnlockState;
  openPGPUnlock: () => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
  onSent: () => void;
  onCancel?: () => void;
}) {
  const [form, setForm] = useState<ComposeForm>(initial);
  const [showCc, setShowCc] = useState(Boolean(initial.cc));
  const [showBcc, setShowBcc] = useState(Boolean(initial.bcc));
  const [sending, setSending] = useState(false);
  const [savingDraft, setSavingDraft] = useState(false);
  const [resizing, setResizing] = useState(false);
  const [pgpEncrypt, setPGPEncrypt] = useState(Boolean(initial.pgp_encrypted));
  const [pgpSign, setPGPSign] = useState(Boolean(initial.pgp_signed));
  const [attachPublicKey, setAttachPublicKey] = useState(Boolean(initial.attach_public_key));
  const [attachments, setAttachments] = useState<ComposeAttachment[]>([]);
  const editorRef = useRef<HTMLDivElement | null>(null);
  const attachmentInputRef = useRef<HTMLInputElement | null>(null);
  const inlineMediaInputRef = useRef<HTMLInputElement | null>(null);
  const attachmentsRef = useRef<ComposeAttachment[]>([]);
  const primaryIdentity = useMemo(() => identities.find((identity) => identity.is_primary) || identities[0] || null, [identities]);
  const availableExistingAttachments = form.available_attachments || [];
  const includedExistingAttachmentIDs = form.include_attachment_ids || [];
  const forwardedMessageAttachment = form.forward_attachment_message_id ? form.forward_attachment || null : null;
  const includedExistingAttachments = useMemo(() => {
    const ids = new Set(includedExistingAttachmentIDs);
    return availableExistingAttachments.filter((attachment) => ids.has(attachment.id));
  }, [availableExistingAttachments, includedExistingAttachmentIDs]);
  const remainingExistingAttachmentCount = Math.max(0, availableExistingAttachments.length - includedExistingAttachments.length);
  const totalAttachmentBytes = useMemo(() => {
    const uploadBytes = attachments.reduce((total, attachment) => total + attachment.size, 0);
    const existingBytes = includedExistingAttachments.reduce((total, attachment) => total + attachment.size, 0);
    const forwardedBytes = forwardedMessageAttachment?.size || 0;
    return uploadBytes + existingBytes + forwardedBytes;
  }, [attachments, includedExistingAttachments, forwardedMessageAttachment]);
  const hasAttachmentWarning = totalAttachmentBytes > ATTACHMENT_WARNING_BYTES;
  const canResizePhotos = attachments.some((attachment) => isResizablePhoto(attachment.file));
  const selectedIdentity = identities.find((identity) => identity.id === (form.from_identity_id || primaryIdentity?.id || 0)) || primaryIdentity;
  const hasAttachedItems = attachments.length > 0 || includedExistingAttachments.length > 0 || Boolean(forwardedMessageAttachment);
  const pgpActive = pgpEnabled && (pgpEncrypt || pgpSign);
  const pgpAttachmentsDisabled = pgpActive;
  const unlockedSigningKey = selectedIdentity
    ? pgpUnlock.keys.find((key) => key.identity_id === selectedIdentity.pgp_identity_id) || null
    : null;

  useEffect(() => {
    setForm({
      ...initial,
      from_identity_id: initial.from_identity_id || primaryIdentity?.id || 0
    });
    setShowCc(Boolean(initial.cc));
    setShowBcc(Boolean(initial.bcc));
    setPGPEncrypt(Boolean(initial.pgp_encrypted));
    setPGPSign(Boolean(initial.pgp_signed));
    setAttachPublicKey(Boolean(initial.attach_public_key));
    setAttachments((current) => {
      revokeAttachmentObjectURLs(current);
      return [];
    });
    if (editorRef.current) {
      editorRef.current.innerHTML = initialEditorHTML(initial);
      placeInitialCaret(editorRef.current);
    }
  }, [initial, primaryIdentity?.id]);

  useEffect(() => {
    attachmentsRef.current = attachments;
  }, [attachments]);

  useEffect(() => () => revokeAttachmentObjectURLs(attachmentsRef.current), []);

  function setField<K extends keyof ComposeForm>(field: K, value: ComposeForm[K]) {
    setForm((current) => ({ ...current, [field]: value }));
  }

  function applyFormat(command: string, value?: string) {
    editorRef.current?.focus();
    document.execCommand(command, false, value);
  }

  // Attachments are kept as File objects until submit. Inline media also gets an
  // object URL immediately so the editor can render it before it becomes a CID.
  function addFiles(fileList: FileList | File[], inline = false) {
    if (pgpAttachmentsDisabled) {
      addToast("Attachments are disabled while PGP encrypt/sign is enabled.", "error");
      return;
    }
    const files = Array.from(fileList).filter((file) => file.size > 0 || file.name);
    if (files.length === 0) return;
    const nextAttachments = files.map((file) => createComposeAttachment(file, inline));
    setAttachments((current) => [...current, ...nextAttachments]);
    if (inline) {
      nextAttachments.forEach((attachment) => insertInlineAttachment(attachment));
    }
  }

  function insertInlineAttachment(attachment: ComposeAttachment) {
    const editor = editorRef.current;
    if (!editor || !attachment.objectURL) return;
    const media = document.createElement(attachment.file.type.startsWith("video/") ? "video" : "img");
    media.setAttribute("src", attachment.objectURL);
    media.setAttribute("data-compose-attachment-id", attachment.id);
    media.setAttribute("class", "compose-inline-media");
    media.setAttribute("alt", attachment.filename);
    if (media instanceof HTMLVideoElement) {
      media.controls = true;
      media.preload = "metadata";
    }
    const spacer = document.createElement("br");
    const fragment = document.createDocumentFragment();
    fragment.appendChild(media);
    fragment.appendChild(spacer);
    editor.focus();
    const selection = window.getSelection();
    const range = selection && selection.rangeCount > 0 ? selection.getRangeAt(0) : null;
    if (range && editor.contains(range.commonAncestorContainer)) {
      range.deleteContents();
      range.insertNode(fragment);
      range.setStartAfter(spacer);
      range.collapse(true);
      selection?.removeAllRanges();
      selection?.addRange(range);
    } else {
      editor.appendChild(fragment);
    }
  }

  function removeAttachment(id: string) {
    setAttachments((current) => {
      current.filter((attachment) => attachment.id === id).forEach((attachment) => {
        if (attachment.objectURL) URL.revokeObjectURL(attachment.objectURL);
      });
      return current.filter((attachment) => attachment.id !== id);
    });
    removeInlineAttachmentElement(editorRef.current, id);
  }

  function includeExistingAttachments() {
    setForm((current) => {
      const ids = new Set(current.include_attachment_ids || []);
      (current.available_attachments || []).forEach((attachment) => ids.add(attachment.id));
      return { ...current, include_attachment_ids: Array.from(ids) };
    });
  }

  function removeExistingAttachment(id: number) {
    setForm((current) => ({
      ...current,
      include_attachment_ids: (current.include_attachment_ids || []).filter((attachmentID) => attachmentID !== id)
    }));
  }

  function removeForwardAttachment() {
    setForm((current) => ({
      ...current,
      forward_attachment_message_id: 0,
      forward_attachment: undefined
    }));
  }

  async function resizePhotos() {
    if (resizing || !canResizePhotos) return;
    setResizing(true);
    try {
      const resized = await Promise.all(attachmentsRef.current.map(async (attachment) => {
        const file = await resizePhotoFile(attachment.file);
        if (!file) return attachment;
        if (attachment.objectURL) URL.revokeObjectURL(attachment.objectURL);
        return {
          ...attachment,
          file,
          filename: file.name,
          content_type: file.type || attachment.content_type,
          size: file.size,
          objectURL: attachment.inline ? URL.createObjectURL(file) : undefined
        };
      }));
      setAttachments(resized);
      syncInlineAttachmentObjectURLs(editorRef.current, resized);
    } catch {
      addToast("Could not resize photos.", "error");
    } finally {
      setResizing(false);
    }
  }

  function handleEditorPaste(event: ClipboardEvent<HTMLDivElement>) {
    const files = Array.from(event.clipboardData.files).filter((file) => isInlineMedia(file));
    if (files.length === 0) return;
    event.preventDefault();
    addFiles(files, true);
  }

  function handleComposeDrop(event: DragEvent<HTMLDivElement>) {
    const files = Array.from(event.dataTransfer.files);
    if (files.length === 0) return;
    event.preventDefault();
    addFiles(files, false);
  }

  // Before sending, replace inline object URLs with cid: URLs and only upload
  // inline files that are still referenced in the edited body.
  async function submit(event: FormEvent) {
    event.preventDefault();
    const editor = editorRef.current;
    const preparedHTML = prepareComposeHTML(editor?.innerHTML || "", attachments);
    const uploadAttachments = attachments.filter((attachment) => !attachment.inline || preparedHTML.inlineIDs.has(attachment.id));
    const nextForm: ComposeForm = {
      ...form,
      from_identity_id: form.from_identity_id || primaryIdentity?.id || 0,
      body: editor?.innerText || "",
      body_html: preparedHTML.html,
      attach_public_key: attachPublicKey
    };
    setSending(true);
    try {
      const prepared = await preparePGPSubmitForm(nextForm, uploadAttachments);
      await api.send(csrf, prepared.form, prepared.attachments);
      addToast("Message sent.");
      onSent();
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setSending(false);
    }
  }


  async function preparePGPSubmitForm(nextForm: ComposeForm, uploadAttachments: ComposeAttachment[]) {
    if (!pgpEnabled || (!pgpEncrypt && !pgpSign)) {
      return { form: { ...nextForm, pgp_encrypted: false, pgp_signed: false }, attachments: uploadAttachments };
    }
    if (uploadAttachments.length > 0 || hasAttachedItems || attachPublicKey) {
      throw new Error("Remove attachments before enabling PGP encrypt/sign.");
    }
    if (pgpSign && !unlockedSigningKey) {
      openPGPUnlock();
      throw new Error("Unlock this identity's PGP key before signing.");
    }
    const payload = nextForm.body_html.trim() ? nextForm.body_html : nextForm.body;
    let armored = payload;
    if (pgpEncrypt) {
      const recipientEmails = recipientEmailAddresses([nextForm.to, nextForm.cc, nextForm.bcc]);
      const data = await api.pgpPublicKeys(recipientEmails);
      const keys = data.keys || [];
      const keyEmails = new Set(keys.map((key) => key.email.toLowerCase()));
      const missing = recipientEmails.filter((email) => !keyEmails.has(email.toLowerCase()));
      if (missing.length > 0) throw new Error(`Missing PGP public key for ${missing.join(", ")}.`);
      armored = await encryptMessageText(payload, keys, pgpSign ? unlockedSigningKey || undefined : undefined);
    } else if (pgpSign && unlockedSigningKey) {
      armored = await signMessageText(payload, unlockedSigningKey);
    }
    return {
      form: { ...nextForm, body: armored, body_html: "", pgp_encrypted: pgpEncrypt, pgp_signed: pgpSign },
      attachments: [] as ComposeAttachment[]
    };
  }

  async function saveDraft() {
    const editor = editorRef.current;
    const preparedHTML = prepareComposeHTML(editor?.innerHTML || "", attachments);
    const uploadAttachments = attachments.filter((attachment) => !attachment.inline || preparedHTML.inlineIDs.has(attachment.id));
    const nextForm: ComposeForm = {
      ...form,
      from_identity_id: form.from_identity_id || primaryIdentity?.id || 0,
      body: editor?.innerText || "",
      body_html: preparedHTML.html,
      attach_public_key: attachPublicKey
    };
    setSavingDraft(true);
    try {
      const prepared = await preparePGPSubmitForm(nextForm, uploadAttachments);
      const data = await api.saveDraft(csrf, prepared.form, prepared.attachments);
      setForm((current) => ({ ...current, draft_message_id: data.message_id }));
      addToast("Draft saved.");
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setSavingDraft(false);
    }
  }

  return (
    <form className={inline ? "inline-reply" : "compose-window"} onSubmit={submit}>
      {!inline ? (
        <div className="compose-head">
          <span>New Message</span>
          <div className="compose-head-actions">
            <button className="ghost" type="button" title="Minimize" onClick={onCancel}>
              <Icon name="minimize" />
            </button>
            <button className="ghost" type="button" title="Close" onClick={onCancel}>
              <Icon name="close" />
            </button>
          </div>
        </div>
      ) : null}
      {!inline ? (
        <div className="compose-fields">
          <div className="compose-line">
            <span>From</span>
            {identities.length > 1 ? (
              <select
                value={form.from_identity_id || primaryIdentity?.id || 0}
                onChange={(event) => setField("from_identity_id", Number(event.target.value))}
              >
                {identities.map((identity) => (
                  <option key={identity.id} value={identity.id}>
                    {identity.header || identity.email}
                  </option>
                ))}
              </select>
            ) : (
              <strong>{identities[0]?.header || composeFrom}</strong>
            )}
          </div>
          <div className="compose-line">
            <span>To</span>
            <RecipientInput value={form.to} onChange={(value) => setField("to", value)} required />
            <button className="ghost text-link" type="button" onClick={() => setShowCc((value) => !value)}>Cc</button>
            <button className="ghost text-link" type="button" onClick={() => setShowBcc((value) => !value)}>Bcc</button>
          </div>
          {showCc ? (
            <div className="compose-line">
              <span>Cc</span>
              <RecipientInput value={form.cc} onChange={(value) => setField("cc", value)} />
            </div>
          ) : null}
          {showBcc ? (
            <div className="compose-line">
              <span>Bcc</span>
              <RecipientInput value={form.bcc} onChange={(value) => setField("bcc", value)} />
            </div>
          ) : null}
          <div className="compose-line">
            <span>Subject</span>
            <input
              aria-label="Subject"
              value={form.subject}
              onChange={(event) => setField("subject", event.target.value)}
              required
            />
          </div>
        </div>
      ) : (
        <div className="inline-reply-meta">
          {identities.length > 1 ? (
            <>
              <span>From</span>
              <select
                value={form.from_identity_id || primaryIdentity?.id || 0}
                onChange={(event) => setField("from_identity_id", Number(event.target.value))}
              >
                {identities.map((identity) => (
                  <option key={identity.id} value={identity.id}>
                    {identity.header || identity.email}
                  </option>
                ))}
              </select>
            </>
          ) : null}
          <span>To</span>
          <strong>{form.to}</strong>
          <button className="ghost text-link" type="button" onClick={() => setShowCc((value) => !value)}>Cc</button>
          <button className="ghost text-link" type="button" onClick={() => setShowBcc((value) => !value)}>Bcc</button>
          <button className="ghost inline-close" type="button" title="Discard reply" onClick={onCancel}>
            <Icon name="close" />
          </button>
        </div>
      )}
      {inline && showCc ? (
        <div className="inline-reply-meta">
          <span>Cc</span>
          <RecipientInput value={form.cc} onChange={(value) => setField("cc", value)} />
        </div>
      ) : null}
      {inline && showBcc ? (
        <div className="inline-reply-meta">
          <span>Bcc</span>
          <RecipientInput value={form.bcc} onChange={(value) => setField("bcc", value)} />
        </div>
      ) : null}
      <div className="compose-body" onDragOver={(event) => event.preventDefault()} onDrop={handleComposeDrop}>
        <div
          ref={editorRef}
          className="compose-editor"
          contentEditable
          data-placeholder="Write a message"
          onPaste={handleEditorPaste}
          suppressContentEditableWarning
        />
      </div>
      {attachments.length > 0 || includedExistingAttachments.length > 0 || forwardedMessageAttachment || remainingExistingAttachmentCount > 0 || hasAttachmentWarning ? (
        <div className="compose-attachments" aria-live="polite">
          {remainingExistingAttachmentCount > 0 ? (
            <button className="compose-existing-attachment-link ghost text-link" type="button" onClick={includeExistingAttachments}>
              <Icon name="attach_file" />
              Include previous {remainingExistingAttachmentCount === 1 ? "attachment" : "attachments"}
            </button>
          ) : null}
          {includedExistingAttachments.length > 0 || attachments.length > 0 ? (
            <div className="compose-attachment-list">
              {includedExistingAttachments.map((attachment) => (
                <div className="compose-attachment compose-attachment-existing" key={`existing-${attachment.id}`}>
                  <Icon name="attach_file" />
                  <span>
                    <strong>{composeExistingAttachmentName(attachment)}</strong>
                    <small>Previous attachment - {formatBytes(attachment.size)}</small>
                  </span>
                  <button className="ghost" type="button" title="Remove attachment" onClick={() => removeExistingAttachment(attachment.id)}>
                    <Icon name="close" />
                  </button>
                </div>
              ))}
              {forwardedMessageAttachment ? (
                <div className="compose-attachment compose-attachment-existing" key="forwarded-message">
                  <Icon name="file_text" />
                  <span>
                    <strong>{composeExistingAttachmentName(forwardedMessageAttachment)}</strong>
                    <small>Original message - {formatBytes(forwardedMessageAttachment.size)}</small>
                  </span>
                  <button className="ghost" type="button" title="Remove attachment" onClick={removeForwardAttachment}>
                    <Icon name="close" />
                  </button>
                </div>
              ) : null}
              {attachments.map((attachment) => (
                <div className="compose-attachment" key={attachment.id}>
                  <Icon name={attachment.inline ? "image" : "attach_file"} />
                  <span>
                    <strong>{attachment.filename}</strong>
                    <small>{attachment.inline ? "Inline media" : "Attachment"} - {formatBytes(attachment.size)}</small>
                  </span>
                  <button className="ghost" type="button" title="Remove attachment" onClick={() => removeAttachment(attachment.id)}>
                    <Icon name="close" />
                  </button>
                </div>
              ))}
            </div>
          ) : null}
          {hasAttachmentWarning ? (
            <div className="compose-attachment-warning">
              <Icon name="report" />
              <span>Attachments total {formatBytes(totalAttachmentBytes)}. Many providers reject messages over 20 MB.</span>
              {canResizePhotos ? (
                <button className="ghost text-link" type="button" disabled={resizing} onClick={resizePhotos}>
                  {resizing ? "Resizing..." : "Resize photos"}
                </button>
              ) : null}
            </div>
          ) : null}
        </div>
      ) : null}
      {pgpEnabled ? <div className="compose-pgp-controls" aria-label="PGP options">
        <label title={hasAttachedItems ? "Remove attachments before enabling PGP encryption" : "Encrypt with recipient public keys"}>
          <input type="checkbox" checked={pgpEncrypt} disabled={hasAttachedItems} onChange={(event) => setPGPEncrypt(event.target.checked)} />
          Encrypt
        </label>
        <label title={hasAttachedItems ? "Remove attachments before enabling PGP signing" : "Sign with your unlocked private key"}>
          <input type="checkbox" checked={pgpSign} disabled={hasAttachedItems || !selectedIdentity?.has_pgp_private_key} onChange={(event) => setPGPSign(event.target.checked)} />
          Sign
        </label>
        <label title={pgpActive ? "Disable PGP encrypt/sign before attaching your public key" : "Attach your public key"}>
          <input type="checkbox" checked={attachPublicKey} disabled={pgpActive || !selectedIdentity?.pgp_public_key_armored} onChange={(event) => setAttachPublicKey(event.target.checked)} />
          Public key
        </label>
        {pgpSign && !unlockedSigningKey ? <button className="ghost text-link" type="button" onClick={openPGPUnlock}>Unlock</button> : null}
      </div> : null}
      <div className="compose-format" aria-label="Formatting">
        <button type="button" title="Bold" onClick={() => applyFormat("bold")}>B</button>
        <button type="button" title="Italic" onClick={() => applyFormat("italic")}><em>I</em></button>
        <button type="button" title="Underline" onClick={() => applyFormat("underline")}><u>U</u></button>
        <button type="button" title="Text color" onClick={() => applyFormat("foreColor", "#c46b44")}>
          <Icon name="format_color_text" />
        </button>
        <button type="button" title="Bulleted list" onClick={() => applyFormat("insertUnorderedList")}>
          <Icon name="format_list_bulleted" />
        </button>
        <button type="button" title="Numbered list" onClick={() => applyFormat("insertOrderedList")}>
          <Icon name="format_list_numbered" />
        </button>
        <button type="button" title="Quote" onClick={() => applyFormat("formatBlock", "blockquote")}>
          <Icon name="format_quote" />
        </button>
      </div>
      <input
        ref={attachmentInputRef}
        className="compose-file-input"
        type="file"
        multiple
        onChange={(event) => {
          if (event.currentTarget.files) addFiles(event.currentTarget.files, false);
          event.currentTarget.value = "";
        }}
      />
      <input
        ref={inlineMediaInputRef}
        className="compose-file-input"
        type="file"
        accept="image/*,video/*"
        multiple
        onChange={(event) => {
          if (event.currentTarget.files) addFiles(event.currentTarget.files, true);
          event.currentTarget.value = "";
        }}
      />
      <div className="compose-sendbar">
        <div className="compose-send-actions">
          <button className="send-button" disabled={sending || savingDraft || resizing}>
            {sending ? "Sending..." : "Send"}
          </button>
          <button className="secondary save-draft-button" type="button" disabled={sending || savingDraft || resizing} onClick={() => void saveDraft()}>
            {savingDraft ? "Saving..." : "Save draft"}
          </button>
          <button className="ghost" type="button" title={pgpAttachmentsDisabled ? "Disable PGP encrypt/sign to attach files" : "Attach files"} disabled={pgpAttachmentsDisabled} onClick={() => attachmentInputRef.current?.click()}>
            <Icon name="attach_file" />
          </button>
          <button className="ghost" type="button" title={pgpAttachmentsDisabled ? "Disable PGP encrypt/sign to insert media" : "Insert inline media"} disabled={pgpAttachmentsDisabled} onClick={() => inlineMediaInputRef.current?.click()}>
            <Icon name="image" />
          </button>
        </div>
        <button className="ghost" type="button" title="Discard" onClick={onCancel}>
          <Icon name="delete" />
        </button>
      </div>
    </form>
  );
}

function composeExistingAttachmentName(attachment: ComposeExistingAttachment): string {
  return attachment.filename || attachment.content_type || "Attachment";
}

function createComposeAttachment(file: File, inline: boolean): ComposeAttachment {
  const id = randomAttachmentID();
  const safeID = id.replace(/[^a-zA-Z0-9]/g, "_");
  return {
    id,
    field: `attachment_${safeID}`,
    filename: file.name || "attachment",
    content_type: file.type || "application/octet-stream",
    content_id: `mailmirror-${safeID}@compose.local`,
    inline,
    size: file.size,
    file,
    objectURL: inline ? URL.createObjectURL(file) : undefined
  };
}

function randomAttachmentID(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID();
  }
  return `${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

// Convert editor-only inline media markers into MIME Content-ID references that
// the backend can package into the outgoing message.
function prepareComposeHTML(html: string, attachments: ComposeAttachment[]): { html: string; inlineIDs: Set<string> } {
  const template = document.createElement("template");
  template.innerHTML = html;
  template.content.querySelectorAll<HTMLElement>("[data-compose-caret-start]").forEach((node) => {
    node.removeAttribute("data-compose-caret-start");
  });
  const byID = new Map(attachments.map((attachment) => [attachment.id, attachment]));
  const inlineIDs = new Set<string>();
  template.content.querySelectorAll<HTMLElement>("[data-compose-attachment-id]").forEach((node) => {
    const id = node.dataset.composeAttachmentId || "";
    const attachment = byID.get(id);
    if (!attachment || !attachment.inline) return;
    inlineIDs.add(id);
    node.removeAttribute("data-compose-attachment-id");
    node.classList.remove("compose-inline-media");
    if (node instanceof HTMLImageElement || node instanceof HTMLVideoElement) {
      node.setAttribute("src", `cid:${attachment.content_id}`);
    }
  });
  return { html: template.innerHTML, inlineIDs };
}

function initialEditorHTML(initial: ComposeForm): string {
  const html = initial.body_html || textToHTML(initial.body);
  if (!isForwardDraft(initial)) return html;
  return `<div data-compose-caret-start="true"><br></div>${stripLeadingBreaks(html)}`;
}

function isForwardDraft(initial: ComposeForm): boolean {
  const subject = initial.subject.trim().toLowerCase();
  if (subject.startsWith("fwd:") || subject.startsWith("fw:")) return true;
  const body = `${initial.body_html}\n${initial.body}`.toLowerCase();
  return body.includes("mailmirror-forwarded-body") || body.includes("forwarded message");
}

function stripLeadingBreaks(html: string): string {
  return html.replace(/^(?:\s|<br\s*\/?>|<div><br><\/div>|<p><br><\/p>)+/i, "");
}

function placeInitialCaret(editor: HTMLDivElement) {
  const marker = editor.querySelector<HTMLElement>("[data-compose-caret-start]");
  if (!marker) return;
  editor.focus({ preventScroll: true });
  const range = document.createRange();
  range.selectNodeContents(marker);
  range.collapse(true);
  const selection = window.getSelection();
  selection?.removeAllRanges();
  selection?.addRange(range);
}

function removeInlineAttachmentElement(editor: HTMLDivElement | null, id: string) {
  if (!editor) return;
  editor.querySelectorAll<HTMLElement>("[data-compose-attachment-id]").forEach((node) => {
    if (node.dataset.composeAttachmentId !== id) return;
    const next = node.nextSibling;
    node.remove();
    if (next instanceof HTMLBRElement) next.remove();
  });
}

function syncInlineAttachmentObjectURLs(editor: HTMLDivElement | null, attachments: ComposeAttachment[]) {
  if (!editor) return;
  const byID = new Map(attachments.map((attachment) => [attachment.id, attachment]));
  editor.querySelectorAll<HTMLElement>("[data-compose-attachment-id]").forEach((node) => {
    const attachment = byID.get(node.dataset.composeAttachmentId || "");
    if (!attachment?.objectURL) return;
    if (node instanceof HTMLImageElement || node instanceof HTMLVideoElement) {
      node.setAttribute("src", attachment.objectURL);
    }
  });
}

function revokeAttachmentObjectURLs(attachments: ComposeAttachment[]) {
  attachments.forEach((attachment) => {
    if (attachment.objectURL) URL.revokeObjectURL(attachment.objectURL);
  });
}

function isInlineMedia(file: File): boolean {
  return file.type.startsWith("image/") || file.type.startsWith("video/");
}

function isResizablePhoto(file: File): boolean {
  return file.type === "image/jpeg" || file.type === "image/png" || file.type === "image/webp";
}

// Client-side photo resizing is opportunistic: if the resized JPEG is not
// smaller, keep the original file so image quality is not degraded for no gain.
async function resizePhotoFile(file: File): Promise<File | null> {
  if (!isResizablePhoto(file)) return null;
  const image = await loadImage(file);
  const largestEdge = Math.max(image.naturalWidth, image.naturalHeight);
  if (largestEdge <= 0) return null;
  const scale = Math.min(1, RESIZE_PHOTO_MAX_EDGE / largestEdge);
  const width = Math.max(1, Math.round(image.naturalWidth * scale));
  const height = Math.max(1, Math.round(image.naturalHeight * scale));
  const canvas = document.createElement("canvas");
  canvas.width = width;
  canvas.height = height;
  const context = canvas.getContext("2d");
  if (!context) return null;
  context.fillStyle = "#fff";
  context.fillRect(0, 0, width, height);
  context.drawImage(image, 0, 0, width, height);
  const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, "image/jpeg", RESIZE_PHOTO_QUALITY));
  if (!blob || blob.size >= file.size) return null;
  return new File([blob], photoFilename(file.name), { type: "image/jpeg", lastModified: Date.now() });
}

function loadImage(file: File): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file);
    const image = new Image();
    image.onload = () => {
      URL.revokeObjectURL(url);
      resolve(image);
    };
    image.onerror = () => {
      URL.revokeObjectURL(url);
      reject(new Error("image load failed"));
    };
    image.src = url;
  });
}

function photoFilename(filename: string): string {
  const trimmed = filename.trim() || "photo";
  return trimmed.replace(/\.[^.]*$/, "") + ".jpg";
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}


function recipientEmailAddresses(values: string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  values.join(",").split(/[;,]/).forEach((part) => {
    const trimmed = part.trim();
    if (!trimmed) return;
    const angle = trimmed.match(/<([^>]+)>/);
    const raw = (angle ? angle[1] : trimmed).replace(/^"|"$/g, "").trim().toLowerCase();
    const match = raw.match(/[a-z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-z0-9-]+(?:\.[a-z0-9-]+)+/i);
    const email = (match ? match[0] : raw).toLowerCase();
    if (!email.includes("@") || seen.has(email)) return;
    seen.add(email);
    out.push(email);
  });
  return out;
}

function RecipientInput({
  value,
  required = false,
  onChange
}: {
  value: string;
  required?: boolean;
  onChange: (value: string) => void;
}) {
  const [suggestions, setSuggestions] = useState<ContactAutocomplete[]>([]);
  const [focused, setFocused] = useState(false);
  const query = lastRecipientToken(value);

  useEffect(() => {
    let cancelled = false;
    if (!focused || query.length < 2) {
      setSuggestions([]);
      return;
    }
    api.contactAutocomplete(query).then((data) => {
      if (!cancelled) setSuggestions(data.contacts || []);
    }).catch(() => {
      if (!cancelled) setSuggestions([]);
    });
    return () => {
      cancelled = true;
    };
  }, [focused, query]);

  function choose(contact: ContactAutocomplete) {
    onChange(replaceLastRecipient(value, formatRecipient(contact)));
    setSuggestions([]);
    setFocused(false);
  }

  return (
    <div className="recipient-input">
      <input
        value={value}
        required={required}
        onFocus={() => setFocused(true)}
        onBlur={() => window.setTimeout(() => setFocused(false), 120)}
        onChange={(event) => onChange(event.target.value)}
      />
      {focused && suggestions.length > 0 ? (
        <div className="recipient-suggest">
          {suggestions.map((contact) => (
            <button type="button" key={`${contact.contact_id}:${contact.email}`} onMouseDown={() => choose(contact)}>
              {contact.icon_url ? <img src={contact.icon_url} alt="" /> : <span>{(contact.name || contact.email).slice(0, 1).toUpperCase()}</span>}
              <strong>{contact.name || contact.email}</strong>
              <small>{contact.email}</small>
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function lastRecipientToken(value: string): string {
  const parts = value.split(/[;,]/);
  return (parts[parts.length - 1] || "").trim();
}

function replaceLastRecipient(value: string, next: string): string {
  const match = value.match(/[;,][^;,]*$/);
  if (!match || match.index === undefined) return `${next}, `;
  return `${value.slice(0, match.index + 1)} ${next}, `;
}

function formatRecipient(contact: ContactAutocomplete): string {
  const name = (contact.name || "").trim();
  if (!name || name.toLowerCase() === contact.email.toLowerCase()) return contact.email;
  const escaped = name.replaceAll('"', "'");
  return `"${escaped}" <${contact.email}>`;
}
