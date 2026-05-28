import { useRef, useState } from "react";
import type { ChangeEvent, DragEvent, FormEvent } from "react";
import { Icon } from "./Icon";
import { messageFromError } from "../lib/errors";

type PGPKeyImportModalProps = {
  title: string;
  description: string;
  placeholder: string;
  importLabel?: string;
  busy?: boolean;
  onImport: (armored: string) => Promise<void> | void;
  onCancel: () => void;
};

export function PGPKeyImportModal({
  title,
  description,
  placeholder,
  importLabel = "Import key",
  busy = false,
  onImport,
  onCancel
}: PGPKeyImportModalProps) {
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const [armored, setArmored] = useState("");
  const [dragging, setDragging] = useState(false);
  const [error, setError] = useState("");

  async function readFile(file: File) {
    setError("");
    try {
      setArmored(await file.text());
    } catch (err) {
      setError(`Could not read ${file.name}: ${messageFromError(err)}`);
    }
  }

  function fileChanged(event: ChangeEvent<HTMLInputElement>) {
    const file = event.currentTarget.files?.[0];
    event.currentTarget.value = "";
    if (file) void readFile(file);
  }

  function dragOver(event: DragEvent<HTMLElement>) {
    event.preventDefault();
    setDragging(true);
  }

  function dragLeave(event: DragEvent<HTMLElement>) {
    if (event.currentTarget.contains(event.relatedTarget as Node | null)) return;
    setDragging(false);
  }

  function dropFile(event: DragEvent<HTMLElement>) {
    event.preventDefault();
    setDragging(false);
    const file = event.dataTransfer.files?.[0];
    if (file) void readFile(file);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!armored.trim() || busy) return;
    setError("");
    try {
      await onImport(armored.trim());
    } catch (err) {
      setError(messageFromError(err));
    }
  }

  return (
    <div className="confirm-backdrop pgp-import-backdrop" role="presentation" onClick={onCancel}>
      <form className="confirm-dialog pgp-import-dialog" role="dialog" aria-modal="true" aria-label={title} onClick={(event) => event.stopPropagation()} onSubmit={(event) => void submit(event)}>
        <div className="pgp-import-heading">
          <div>
            <h2>{title}</h2>
            <p>{description}</p>
          </div>
          <button className="icon-action" type="button" title="Close" aria-label="Close" onClick={onCancel}>
            <Icon name="close" />
          </button>
        </div>
        <input
          ref={fileInputRef}
          className="compose-file-input"
          type="file"
          accept=".asc,.pgp,.gpg,.txt,application/pgp-keys,application/pgp-signature,text/plain"
          onChange={fileChanged}
        />
        <button className="secondary pgp-import-file-button" type="button" onClick={() => fileInputRef.current?.click()}>
          <Icon name="attach_file" /> Choose key file
        </button>
        <label className={`pgp-import-drop ${dragging ? "dragging" : ""}`} onDragOver={dragOver} onDragLeave={dragLeave} onDrop={dropFile}>
          <span>Paste key text or drop a key file here</span>
          <textarea value={armored} placeholder={placeholder} onChange={(event) => setArmored(event.target.value)} />
        </label>
        {error ? <div className="notice error">{error}</div> : null}
        <div className="pgp-import-actions">
          <button className="secondary" type="button" onClick={onCancel}>Cancel</button>
          <button type="submit" disabled={busy || !armored.trim()}>{busy ? "Importing..." : importLabel}</button>
        </div>
      </form>
    </div>
  );
}
