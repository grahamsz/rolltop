import { useEffect, useMemo, useRef, useState } from "react";
import type { ClipboardEvent, DragEvent, FormEvent } from "react";
import { api } from "../../api";
import type { LocationState, Toast } from "../../appTypes";
import type { ContactAutocomplete, ComposeAttachmentUpload, ComposeForm, ComposeIdentity } from "../../types";
import { Icon } from "../../components/Icon";
import { messageFromError } from "../../lib/errors";
import { textToHTML } from "../../lib/html";

const ATTACHMENT_WARNING_BYTES = 20 * 1024 * 1024;
const RESIZE_PHOTO_MAX_EDGE = 1920;
const RESIZE_PHOTO_QUALITY = 0.82;

type ComposeAttachment = ComposeAttachmentUpload & {
  id: string;
  objectURL?: string;
};

export function ComposeOverlay({
  csrf,
  query,
  addToast,
  onClose
}: {
  csrf: string;
  query: string;
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

export function ComposePage({
  csrf,
  location,
  navigate,
  addToast
}: {
  csrf: string;
  location: LocationState;
  navigate: (url: string) => void;
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

export function ComposeBox({
  csrf,
  composeFrom,
  identities = [],
  initial,
  inline = false,
  addToast,
  onSent,
  onCancel
}: {
  csrf: string;
  composeFrom: string;
  identities?: ComposeIdentity[];
  initial: ComposeForm;
  inline?: boolean;
  addToast: (message: string, kind?: Toast["kind"]) => number;
  onSent: () => void;
  onCancel?: () => void;
}) {
  const [form, setForm] = useState<ComposeForm>(initial);
  const [showCc, setShowCc] = useState(Boolean(initial.cc));
  const [showBcc, setShowBcc] = useState(Boolean(initial.bcc));
  const [sending, setSending] = useState(false);
  const [resizing, setResizing] = useState(false);
  const [attachments, setAttachments] = useState<ComposeAttachment[]>([]);
  const editorRef = useRef<HTMLDivElement | null>(null);
  const attachmentInputRef = useRef<HTMLInputElement | null>(null);
  const inlineMediaInputRef = useRef<HTMLInputElement | null>(null);
  const attachmentsRef = useRef<ComposeAttachment[]>([]);
  const primaryIdentity = useMemo(() => identities.find((identity) => identity.is_primary) || identities[0] || null, [identities]);
  const totalAttachmentBytes = useMemo(() => attachments.reduce((total, attachment) => total + attachment.size, 0), [attachments]);
  const hasAttachmentWarning = totalAttachmentBytes > ATTACHMENT_WARNING_BYTES;
  const canResizePhotos = attachments.some((attachment) => isResizablePhoto(attachment.file));

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
      editorRef.current.innerHTML = initial.body_html || textToHTML(initial.body);
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

  async function submit(event: FormEvent) {
    event.preventDefault();
    const editor = editorRef.current;
    const preparedHTML = prepareComposeHTML(editor?.innerHTML || "", attachments);
    const uploadAttachments = attachments.filter((attachment) => !attachment.inline || preparedHTML.inlineIDs.has(attachment.id));
    const nextForm: ComposeForm = {
      ...form,
      from_identity_id: form.from_identity_id || primaryIdentity?.id || 0,
      body: editor?.innerText || "",
      body_html: preparedHTML.html
    };
    setSending(true);
    try {
      await api.send(csrf, nextForm, uploadAttachments);
      addToast("Message sent.");
      onSent();
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setSending(false);
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
            <input
              placeholder="Subject"
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
      {attachments.length > 0 || hasAttachmentWarning ? (
        <div className="compose-attachments" aria-live="polite">
          {attachments.length > 0 ? (
            <div className="compose-attachment-list">
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
      <div className="compose-format" aria-label="Formatting">
        <button type="button" title="Bold" onClick={() => applyFormat("bold")}>B</button>
        <button type="button" title="Italic" onClick={() => applyFormat("italic")}><em>I</em></button>
        <button type="button" title="Underline" onClick={() => applyFormat("underline")}><u>U</u></button>
        <button type="button" title="Text color" onClick={() => applyFormat("foreColor", "#d95f3d")}>
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
          <button className="send-button" disabled={sending || resizing}>
            {sending ? "Sending..." : "Send"}
          </button>
          <button className="ghost" type="button" title="Attach files" onClick={() => attachmentInputRef.current?.click()}>
            <Icon name="attach_file" />
          </button>
          <button className="ghost" type="button" title="Insert inline media" onClick={() => inlineMediaInputRef.current?.click()}>
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

function prepareComposeHTML(html: string, attachments: ComposeAttachment[]): { html: string; inlineIDs: Set<string> } {
  const template = document.createElement("template");
  template.innerHTML = html;
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
