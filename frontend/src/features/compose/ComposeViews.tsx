// File overview: Compose, reply, and forward UI. It owns the editable body, recipient fields,
// identity choice, file uploads, inline media CIDs, and optional client-side photo resizing.

import { useEffect, useMemo, useRef, useState } from "react";
import type { ClipboardEvent, DragEvent, FormEvent } from "react";
import { api } from "../../api";
import type { LocationState, Toast } from "../../appTypes";
import type { ContactAutocomplete, ComposeAttachmentUpload, ComposeExistingAttachment, ComposeForm, ComposeIdentity } from "../../types";
import { Icon, LogoMark } from "../../components/Icon";
import { messageFromError } from "../../lib/errors";
import { textToHTML } from "../../lib/html";
import {
  androidContactSuggestions,
  androidNativeAvailable,
  loadAndroidSharedFiles,
  pickAndroidContactEmail,
  requestAndroidContactAccess
} from "../../lib/androidNative";
import type { AndroidContactAccess, NativeContactEmail } from "../../lib/androidNative";
import type { RuntimePlugin } from "../../plugins/runtime";
import { useComposeSecurity } from "../../plugins/composeSecurity";
import type { ComposeSecurityUnlockState, OpenComposeSecurityUnlock } from "../../plugins/composeSecurity";

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
  securityEnabled,
  securityPlugins,
  securityUnlock,
  openSecurityUnlock,
  addToast,
  onClose
}: {
  csrf: string;
  query: string;
  securityEnabled: boolean;
  securityPlugins: readonly RuntimePlugin[];
  securityUnlock: ComposeSecurityUnlockState;
  openSecurityUnlock: OpenComposeSecurityUnlock;
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
          securityEnabled={securityEnabled}
          securityPlugins={securityPlugins}
          securityUnlock={securityUnlock}
          openSecurityUnlock={openSecurityUnlock}
          addToast={addToast}
          onSent={onClose}
          onCancel={onClose}
        />
      ) : (
        <div className="compose-window compose-loading">
          <div className="compose-head">
            <span className="compose-head-title">
              <LogoMark className="compose-head-mark" />
              <span>New Message</span>
            </span>
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
  securityEnabled,
  securityPlugins,
  securityUnlock,
  openSecurityUnlock,
  addToast
}: {
  csrf: string;
  location: LocationState;
  navigate: (url: string) => void;
  securityEnabled: boolean;
  securityPlugins: readonly RuntimePlugin[];
  securityUnlock: ComposeSecurityUnlockState;
  openSecurityUnlock: OpenComposeSecurityUnlock;
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
          securityEnabled={securityEnabled}
          securityPlugins={securityPlugins}
          securityUnlock={securityUnlock}
          openSecurityUnlock={openSecurityUnlock}
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
  securityEnabled = false,
  securityPlugins,
  securityUnlock,
  openSecurityUnlock,
  addToast,
  onSent,
  onCancel
}: {
  csrf: string;
  composeFrom: string;
  identities?: ComposeIdentity[];
  initial: ComposeForm;
  inline?: boolean;
  securityEnabled?: boolean;
  securityPlugins: readonly RuntimePlugin[];
  securityUnlock: ComposeSecurityUnlockState;
  openSecurityUnlock: OpenComposeSecurityUnlock;
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
  const [attachments, setAttachments] = useState<ComposeAttachment[]>([]);
  const editorRef = useRef<HTMLDivElement | null>(null);
  const formRef = useRef<HTMLFormElement | null>(null);
  const attachmentInputRef = useRef<HTMLInputElement | null>(null);
  const inlineMediaInputRef = useRef<HTMLInputElement | null>(null);
  const attachmentsRef = useRef<ComposeAttachment[]>([]);
  const nativeShareID = !inline ? new URLSearchParams(window.location.search).get("android_share") || "" : "";
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
  const recipientEmails = useMemo(() => recipientEmailAddresses([form.to, form.cc, form.bcc]), [form.to, form.cc, form.bcc]);
  const composeSecurity = useComposeSecurity({
    enabled: securityEnabled,
    plugins: securityPlugins,
    initial,
    selectedIdentity,
    recipientEmails,
    includedExistingAttachments,
    forwardedMessageAttachment,
    unlockState: securityUnlock,
    openUnlock: openSecurityUnlock,
    addToast
  });

  useEffect(() => {
    setForm({
      ...initial,
      from_identity_id: initial.from_identity_id || primaryIdentity?.id || 0
    });
    setShowCc(Boolean(initial.cc));
    setShowBcc(Boolean(initial.bcc));
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

  // Android WebView can report the keyboard through VisualViewport before dvh
  // catches up. Size the composer within its container so the persistent mobile
  // app bar remains visible and the action bar stays above the IME.
  useEffect(() => {
    if (inline) return;
    const viewport = window.visualViewport;
    const updateViewport = () => {
      const viewportTop = viewport?.offsetTop || 0;
      const viewportBottom = viewportTop + (viewport?.height || window.innerHeight);
      const containerTop = formRef.current?.parentElement?.getBoundingClientRect().top || 0;
      const visibleTop = Math.max(viewportTop, containerTop);
      const height = Math.max(1, Math.round(viewportBottom - visibleTop));
      const top = Math.max(0, Math.round(visibleTop - containerTop));
      formRef.current?.style.setProperty("--compose-viewport-height", `${height}px`);
      formRef.current?.style.setProperty("--compose-viewport-top", `${top}px`);
    };
    updateViewport();
    viewport?.addEventListener("resize", updateViewport);
    viewport?.addEventListener("scroll", updateViewport);
    window.addEventListener("resize", updateViewport);
    return () => {
      viewport?.removeEventListener("resize", updateViewport);
      viewport?.removeEventListener("scroll", updateViewport);
      window.removeEventListener("resize", updateViewport);
    };
  }, [inline]);

  useEffect(() => {
    if (!nativeShareID) return;
    let cancelled = false;
    void loadAndroidSharedFiles(nativeShareID)
      .then((files) => {
        if (!cancelled && files.length > 0) addFiles(files, false);
      })
      .catch((err) => {
        if (!cancelled) addToast(err instanceof Error ? err.message : "Could not load Android shared files.", "error");
      });
    return () => {
      cancelled = true;
    };
  }, [nativeShareID]);

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
    if (!composeSecurity.beginSubmit(formRef)) return;
    const editor = editorRef.current;
    const preparedHTML = prepareComposeHTML(editor?.innerHTML || "", attachments);
    const uploadAttachments = attachments.filter((attachment) => !attachment.inline || preparedHTML.inlineIDs.has(attachment.id));
    const nextForm: ComposeForm = {
      ...form,
      from_identity_id: form.from_identity_id || primaryIdentity?.id || 0,
      body: editor?.innerText || "",
      body_html: preparedHTML.html,
      attach_public_key: composeSecurity.attachPublicKey
    };
    let sent = false;
    setSending(true);
    if (composeSecurity.active) {
      composeSecurity.setTransform({ active: true, phase: "plaintext", ciphertext: "" });
      await waitForFrame();
    }
    try {
      const prepared = await composeSecurity.prepareSubmitForm(nextForm, uploadAttachments, (ciphertext) => {
        composeSecurity.setTransform({ active: true, phase: "ciphertext", ciphertext });
      });
      if (composeSecurity.active) await delay(520);
      await api.send(csrf, prepared.form, prepared.attachments);
      sent = true;
      addToast("Message sent.");
      onSent();
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      if (!sent) composeSecurity.setTransform({ active: false, phase: "plaintext", ciphertext: "" });
      setSending(false);
    }
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
      attach_public_key: composeSecurity.attachPublicKey
    };
    setSavingDraft(true);
    try {
      const prepared = await composeSecurity.prepareSubmitForm(nextForm, uploadAttachments);
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
    <form ref={formRef} className={inline ? "inline-reply" : "compose-window"} onSubmit={submit}>
      {!inline ? (
        <div className="compose-head">
          <span className="compose-head-title">
            <LogoMark className="compose-head-mark" />
            <span>New Message</span>
          </span>
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
      <div className="compose-scroll-region">
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
              <div className="inline-reply-from">
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
              </div>
            ) : null}
            <div className="inline-reply-to">
              <span>To</span>
              <strong>{form.to}</strong>
            </div>
            <div className="inline-reply-actions">
              <button className="ghost text-link" type="button" onClick={() => setShowCc((value) => !value)}>Cc</button>
              <button className="ghost text-link" type="button" onClick={() => setShowBcc((value) => !value)}>Bcc</button>
              <button className="ghost inline-close" type="button" title="Discard reply" onClick={onCancel}>
                <Icon name="close" />
              </button>
            </div>
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
        <div
          className={`compose-body ${composeSecurity.transform.active ? `pgp-transforming pgp-transform-${composeSecurity.transform.phase}` : ""}`}
          onDragOver={(event) => event.preventDefault()}
          onDrop={handleComposeDrop}
        >
          <div
            ref={editorRef}
            className="compose-editor"
            contentEditable={!sending}
            data-placeholder="Write a message"
            onPaste={handleEditorPaste}
            suppressContentEditableWarning
          />
          {composeSecurity.transform.active ? (
            <pre className="compose-pgp-ciphertext" aria-hidden="true">
              {composeSecurity.transform.ciphertext || "Preparing PGP message..."}
            </pre>
          ) : null}
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
      </div>
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
      {composeSecurity.renderSendChoice(formRef)}
      <div className="compose-sendbar">
        <div className="compose-send-actions">
          <button className="send-button" disabled={sending || savingDraft || resizing}>
            <Icon name="send" />
            {composeSecurity.sendButtonLabel(sending)}
          </button>
          <button className="secondary save-draft-button" type="button" title="Save draft" aria-label={savingDraft ? "Saving draft" : "Save draft"} disabled={sending || savingDraft || resizing} onClick={() => void saveDraft()}>
            <Icon name="draft" />
            <span>{savingDraft ? "Saving..." : "Save draft"}</span>
          </button>
          <div className="compose-lower-tools" aria-label="Message tools">
            {composeSecurity.renderControls({ attachmentInputRef, inlineMediaInputRef })}
          </div>
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
    content_id: `rolltop-${safeID}@compose.local`,
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
  return body.includes("rolltop-forwarded-body") || body.includes("forwarded message");
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

type RecipientSuggestion = {
  key: string;
  name: string;
  email: string;
  iconURL: string;
  source: "rolltop" | "android";
};

function RecipientInput({
  value,
  required = false,
  onChange
}: {
  value: string;
  required?: boolean;
  onChange: (value: string) => void;
}) {
  const [rolltopSuggestions, setRolltopSuggestions] = useState<ContactAutocomplete[]>([]);
  const [nativeSuggestions, setNativeSuggestions] = useState<NativeContactEmail[]>([]);
  const [nativeAccess, setNativeAccess] = useState<AndroidContactAccess | "unknown">("unknown");
  const [focused, setFocused] = useState(false);
  const [pickingAndroidContact, setPickingAndroidContact] = useState(false);
  const [requestingNativeAccess, setRequestingNativeAccess] = useState(false);
  const query = lastRecipientToken(value);
  const nativeAvailable = androidNativeAvailable();
  const suggestions = useMemo(
    () => mergeRecipientSuggestions(rolltopSuggestions, nativeSuggestions),
    [rolltopSuggestions, nativeSuggestions]
  );

  useEffect(() => {
    let cancelled = false;
    if (!focused || query.length < 2) {
      setRolltopSuggestions([]);
      setNativeSuggestions([]);
      return;
    }
    api.contactAutocomplete(query).then((data) => {
      if (!cancelled) setRolltopSuggestions(data.contacts || []);
    }).catch(() => {
      if (!cancelled) setRolltopSuggestions([]);
    });
    if (nativeAvailable && nativeAccess !== "denied") {
      androidContactSuggestions(query).then((data) => {
        if (cancelled) return;
        setNativeAccess(data.access);
        setNativeSuggestions(data.contacts);
      }).catch(() => {
        if (!cancelled) setNativeSuggestions([]);
      });
    } else {
      setNativeSuggestions([]);
    }
    return () => {
      cancelled = true;
    };
  }, [focused, nativeAccess, nativeAvailable, query]);

  function choose(contact: RecipientSuggestion) {
    onChange(replaceLastRecipient(value, formatNativeRecipient(contact.name, contact.email)));
    setRolltopSuggestions([]);
    setNativeSuggestions([]);
    setFocused(false);
  }

  async function enableAndroidContactSuggestions() {
    if (requestingNativeAccess) return;
    setRequestingNativeAccess(true);
    try {
      const access = await requestAndroidContactAccess();
      setNativeAccess(access);
      if (access !== "granted") {
        setNativeSuggestions([]);
        return;
      }
      const data = await androidContactSuggestions(query);
      setNativeAccess(data.access);
      setNativeSuggestions(data.contacts);
    } catch {
      setNativeAccess("permission_required");
      setNativeSuggestions([]);
    } finally {
      setRequestingNativeAccess(false);
    }
  }

  async function chooseAndroidContact() {
    if (pickingAndroidContact) return;
    setPickingAndroidContact(true);
    try {
      const contact = await pickAndroidContactEmail();
      if (!contact) return;
      onChange(replaceLastRecipient(value, formatNativeRecipient(contact.name, contact.email)));
      setRolltopSuggestions([]);
      setNativeSuggestions([]);
    } catch {
      // The system picker may be unavailable or dismissed while the WebView is changing pages.
    } finally {
      setPickingAndroidContact(false);
    }
  }

  return (
    <div className="recipient-input">
      <div className="recipient-input-control">
        <input
          value={value}
          required={required}
          onFocus={() => setFocused(true)}
          onBlur={() => window.setTimeout(() => setFocused(false), 120)}
          onChange={(event) => onChange(event.target.value)}
        />
        {nativeAvailable ? (
          <button
            className="ghost native-contact-picker"
            type="button"
            title="Choose Android contact"
            disabled={pickingAndroidContact}
            onClick={() => void chooseAndroidContact()}
          >
            <Icon name="group" />
          </button>
        ) : null}
      </div>
      {focused && (suggestions.length > 0 || (nativeAvailable && nativeAccess === "permission_required")) ? (
        <div className="recipient-suggest">
          {suggestions.map((contact) => (
            <button
              type="button"
              key={contact.key}
              title={contact.source === "android" ? "Android contact" : undefined}
              onMouseDown={() => choose(contact)}
            >
              {contact.iconURL ? <img src={contact.iconURL} alt="" /> : <span>{(contact.name || contact.email).slice(0, 1).toUpperCase()}</span>}
              <strong>{contact.name || contact.email}</strong>
              <small>{contact.email}</small>
            </button>
          ))}
          {nativeAvailable && nativeAccess === "permission_required" ? (
            <button
              type="button"
              className="native-contact-enable"
              disabled={requestingNativeAccess}
              onMouseDown={(event) => {
                event.preventDefault();
                void enableAndroidContactSuggestions();
              }}
            >
              <span><Icon name="group" /></span>
              <strong>{requestingNativeAccess ? "Requesting access..." : "Enable Android contacts"}</strong>
              <small>Device suggestions</small>
            </button>
          ) : null}
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

function mergeRecipientSuggestions(
  rolltop: ContactAutocomplete[],
  android: NativeContactEmail[]
): RecipientSuggestion[] {
  const serverRows = rolltop.map((contact) => ({
    key: `rolltop:${contact.contact_id}:${contact.email.toLowerCase()}`,
    name: contact.name,
    email: contact.email,
    iconURL: contact.icon_url,
    source: "rolltop" as const
  }));
  const nativeRows = android.map((contact) => ({
    key: `android:${contact.email.toLowerCase()}`,
    name: contact.name,
    email: contact.email,
    iconURL: "",
    source: "android" as const
  }));
  const seen = new Set<string>();
  const merged: RecipientSuggestion[] = [];
  for (let index = 0; index < Math.max(serverRows.length, nativeRows.length); index++) {
    for (const row of [serverRows[index], nativeRows[index]]) {
      if (!row) continue;
      const key = row.email.trim().toLowerCase();
      if (!key || seen.has(key)) continue;
      seen.add(key);
      merged.push(row);
      if (merged.length === 8) return merged;
    }
  }
  return merged;
}

function formatNativeRecipient(name: string, email: string): string {
  const trimmedName = name.trim();
  if (!trimmedName || trimmedName.toLowerCase() === email.toLowerCase()) return email;
  return `"${trimmedName.replaceAll('"', "'")}" <${email}>`;
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

function waitForFrame(): Promise<void> {
  return new Promise((resolve) => window.requestAnimationFrame(() => resolve()));
}
