// File overview: Contact management view. It edits Me identities and address-book data used by
// compose, sender display, reply targeting, and avatar/icon lookup.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { api } from "../../api";
import type { Contact, ContactAddress, ContactEmail, ContactPhone, ContactURL } from "../../types";
import type { Toast } from "../../appTypes";
import { Icon } from "../../components/Icon";
import { messageFromError } from "../../lib/errors";

/** ContactsView manages the user address book and Me contacts used by compose/reply identity logic. */
export function ContactsView({
  csrf,
  addToast
}: {
  csrf: string;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const [contacts, setContacts] = useState<Contact[]>([]);
  const [query, setQuery] = useState("");
  const [selectedID, setSelectedID] = useState<number | "new" | null>(null);
  const [draft, setDraft] = useState<Contact>(() => blankContact());
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const importRef = useRef<HTMLInputElement | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data = await api.contacts(query);
      const nextContacts = data.contacts || [];
      setContacts(nextContacts);
      if (selectedID === null) {
        const first = nextContacts[0];
        if (first) {
          setSelectedID(first.id);
          setDraft(cloneContact(first));
        } else {
          setSelectedID("new");
          setDraft(blankContact());
        }
      } else if (selectedID !== "new") {
        const selected = nextContacts.find((contact) => contact.id === selectedID);
        if (selected) setDraft(cloneContact(selected));
        else {
          setSelectedID("new");
          setDraft(blankContact());
        }
      }
    } finally {
      setLoading(false);
    }
  }, [query, selectedID]);

  useEffect(() => {
    void load().catch((err) => addToast(messageFromError(err), "error"));
  }, [addToast, load]);

  const selected = useMemo(() => contacts.find((contact) => contact.id === selectedID) || null, [contacts, selectedID]);

  function choose(contact: Contact) {
    setSelectedID(contact.id);
    setDraft(cloneContact(contact));
  }

  function newContact() {
    setSelectedID("new");
    setDraft(blankContact());
  }

  function setField<K extends keyof Contact>(field: K, value: Contact[K]) {
    setDraft((current) => ({ ...current, [field]: value }));
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    setSaving(true);
    try {
      const data = draft.id ? await api.updateContact(csrf, draft) : await api.createContact(csrf, draft);
      addToast("Contact saved.");
      setSelectedID(data.contact.id);
      setDraft(cloneContact(data.contact));
      await load();
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setSaving(false);
    }
  }

  async function deleteContact() {
    if (!draft.id) return;
    try {
      await api.deleteContact(csrf, draft.id);
      addToast("Contact deleted.");
      setSelectedID("new");
      setDraft(blankContact());
      await load();
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  async function uploadIcon(file: File | null) {
    if (!file || !draft.id) return;
    try {
      const data = await api.uploadContactIcon(csrf, draft.id, file);
      setDraft(cloneContact(data.contact));
      setContacts((current) => current.map((contact) => contact.id === data.contact.id ? data.contact : contact));
      addToast("Contact icon updated.");
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  async function removeIcon() {
    if (!draft.id) return;
    try {
      const data = await api.deleteContactIcon(csrf, draft.id);
      setDraft(cloneContact(data.contact));
      setContacts((current) => current.map((contact) => contact.id === data.contact.id ? data.contact : contact));
      addToast("Contact icon removed.");
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  async function importContacts(file: File | null) {
    if (!file) return;
    try {
      const data = await api.importContacts(csrf, file);
      addToast(`Imported ${data.imported}, updated ${data.updated}.`);
      await load();
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      if (importRef.current) importRef.current.value = "";
    }
  }

  return (
    <>
      <div className="content-head">
        <div>
          <h1>Contacts</h1>
          <span className="label-pill">{contacts.length.toLocaleString()}</span>
        </div>
        <div className="contact-actions">
          <button className="secondary" type="button" onClick={newContact}>
            <Icon name="edit" />
            New
          </button>
          <button className="secondary" type="button" onClick={() => importRef.current?.click()}>
            <Icon name="archive" />
            Import VCF
          </button>
          <a className="button secondary" href="/api/contacts/export">
            <Icon name="send" />
            Export VCF
          </a>
          <input ref={importRef} type="file" accept=".vcf,text/vcard,text/x-vcard" hidden onChange={(event) => void importContacts(event.target.files?.[0] || null)} />
        </div>
      </div>
      <section className="contacts-shell">
        <aside className="contacts-list">
          <div className="contacts-search">
            <Icon name="search" />
            <input value={query} placeholder="Search contacts" onChange={(event) => setQuery(event.target.value)} />
          </div>
          {loading ? <div className="muted">Loading contacts...</div> : null}
          <div className="contacts-list-items">
            {contacts.map((contact) => (
              <button
                type="button"
                className={`contact-row ${contact.id === selectedID ? "active" : ""}`}
                key={contact.id}
                onClick={() => choose(contact)}
              >
                <ContactAvatar contact={contact} />
                <span>
                  <strong>{contact.display_name || primaryEmail(contact) || "Unnamed contact"}</strong>
                  <small>{primaryEmail(contact) || contact.organization}</small>
                </span>
                {contact.is_me ? <em>Me</em> : null}
              </button>
            ))}
          </div>
        </aside>
        <form className="contact-editor" onSubmit={save}>
          <div className="contact-editor-head">
            <ContactAvatar contact={draft} large />
            <div>
              <label className="icon-upload">
                <input type="file" accept="image/png,image/jpeg,image/gif,image/webp" disabled={!draft.id} onChange={(event) => void uploadIcon(event.target.files?.[0] || null)} />
                <span>{draft.id ? "Upload icon" : "Save before icon upload"}</span>
              </label>
              {draft.icon_url ? <button className="ghost text-link" type="button" onClick={() => void removeIcon()}>Remove icon</button> : null}
            </div>
          </div>
          <div className="contact-grid">
            <Field label="Display name" value={draft.display_name} required onChange={(value) => setField("display_name", value)} />
            <Field label="Nickname" value={draft.nickname} onChange={(value) => setField("nickname", value)} />
            <Field label="Prefix" value={draft.name_prefix} onChange={(value) => setField("name_prefix", value)} />
            <Field label="Given" value={draft.given_name} onChange={(value) => setField("given_name", value)} />
            <Field label="Middle" value={draft.additional_name} onChange={(value) => setField("additional_name", value)} />
            <Field label="Family" value={draft.family_name} onChange={(value) => setField("family_name", value)} />
            <Field label="Suffix" value={draft.name_suffix} onChange={(value) => setField("name_suffix", value)} />
            <Field label="Organization" value={draft.organization} onChange={(value) => setField("organization", value)} />
            <Field label="Department" value={draft.department} onChange={(value) => setField("department", value)} />
            <Field label="Job title" value={draft.job_title} onChange={(value) => setField("job_title", value)} />
            <Field label="Birthday" value={draft.birthday} type="text" placeholder="YYYY-MM-DD" onChange={(value) => setField("birthday", value)} />
            <Field label="Categories" value={draft.categories} onChange={(value) => setField("categories", value)} />
          </div>
          <div className="contact-flags">
            <label><input type="checkbox" checked={draft.is_me} onChange={(event) => setField("is_me", event.target.checked)} /> Me identity</label>
            <label><input type="checkbox" checked={draft.is_primary} disabled={!draft.is_me} onChange={(event) => setField("is_primary", event.target.checked)} /> Primary From identity</label>
          </div>
          <ContactEmailEditor value={draft.emails} onChange={(emails) => setField("emails", emails)} />
          <ContactPhoneEditor value={draft.phones} onChange={(phones) => setField("phones", phones)} />
          <ContactAddressEditor value={draft.addresses} onChange={(addresses) => setField("addresses", addresses)} />
          <ContactURLEditor value={draft.urls} onChange={(urls) => setField("urls", urls)} />
          <label className="contact-notes">
            Notes
            <textarea value={draft.notes} onChange={(event) => setField("notes", event.target.value)} />
          </label>
          <div className="contact-savebar">
            <button disabled={saving}>{saving ? "Saving..." : "Save contact"}</button>
            {selected ? <button className="secondary" type="button" onClick={() => void deleteContact()}>Delete</button> : null}
          </div>
        </form>
      </section>
    </>
  );
}

function Field({
  label,
  value,
  type = "text",
  placeholder = "",
  required = false,
  onChange
}: {
  label: string;
  value: string;
  type?: string;
  placeholder?: string;
  required?: boolean;
  onChange: (value: string) => void;
}) {
  return (
    <label>
      {label}
      <input type={type} value={value} placeholder={placeholder} required={required} onChange={(event) => onChange(event.target.value)} />
    </label>
  );
}

function ContactEmailEditor({ value, onChange }: { value: ContactEmail[]; onChange: (value: ContactEmail[]) => void }) {
  const rows = value.length > 0 ? value : [{ label: "Email", email: "", is_primary: true }];
  return (
    <ContactSection title="Emails" onAdd={() => onChange([...rows, { label: "Email", email: "", is_primary: false }])}>
      {rows.map((row, index) => (
        <div className="contact-repeat-row" key={index}>
          <input value={row.label} placeholder="Label" onChange={(event) => onChange(updateAt(rows, index, { ...row, label: event.target.value }))} />
          <input value={row.email} type="email" placeholder="email@example.com" onChange={(event) => onChange(updateAt(rows, index, { ...row, email: event.target.value }))} />
          <PrimaryToggle checked={row.is_primary} onChange={() => onChange(markPrimary(rows, index))} />
          <RemoveButton onClick={() => onChange(removeAt(rows, index))} />
        </div>
      ))}
    </ContactSection>
  );
}

function ContactPhoneEditor({ value, onChange }: { value: ContactPhone[]; onChange: (value: ContactPhone[]) => void }) {
  return (
    <ContactSection title="Phones" onAdd={() => onChange([...value, { label: "Phone", number: "", is_primary: value.length === 0 }])}>
      {value.map((row, index) => (
        <div className="contact-repeat-row" key={index}>
          <input value={row.label} placeholder="Label" onChange={(event) => onChange(updateAt(value, index, { ...row, label: event.target.value }))} />
          <input value={row.number} placeholder="Number" onChange={(event) => onChange(updateAt(value, index, { ...row, number: event.target.value }))} />
          <PrimaryToggle checked={row.is_primary} onChange={() => onChange(markPrimary(value, index))} />
          <RemoveButton onClick={() => onChange(removeAt(value, index))} />
        </div>
      ))}
    </ContactSection>
  );
}

function ContactAddressEditor({ value, onChange }: { value: ContactAddress[]; onChange: (value: ContactAddress[]) => void }) {
  return (
    <ContactSection title="Addresses" onAdd={() => onChange([...value, blankAddress(value.length === 0)])}>
      {value.map((row, index) => (
        <div className="contact-address-row" key={index}>
          <input value={row.label} placeholder="Label" onChange={(event) => onChange(updateAt(value, index, { ...row, label: event.target.value }))} />
          <input value={row.street} placeholder="Street" onChange={(event) => onChange(updateAt(value, index, { ...row, street: event.target.value }))} />
          <input value={row.locality} placeholder="City" onChange={(event) => onChange(updateAt(value, index, { ...row, locality: event.target.value }))} />
          <input value={row.region} placeholder="State/region" onChange={(event) => onChange(updateAt(value, index, { ...row, region: event.target.value }))} />
          <input value={row.postal_code} placeholder="Postal code" onChange={(event) => onChange(updateAt(value, index, { ...row, postal_code: event.target.value }))} />
          <input value={row.country} placeholder="Country" onChange={(event) => onChange(updateAt(value, index, { ...row, country: event.target.value }))} />
          <PrimaryToggle checked={row.is_primary} onChange={() => onChange(markPrimary(value, index))} />
          <RemoveButton onClick={() => onChange(removeAt(value, index))} />
        </div>
      ))}
    </ContactSection>
  );
}

function ContactURLEditor({ value, onChange }: { value: ContactURL[]; onChange: (value: ContactURL[]) => void }) {
  return (
    <ContactSection title="URLs" onAdd={() => onChange([...value, { label: "Website", url: "", is_primary: value.length === 0 }])}>
      {value.map((row, index) => (
        <div className="contact-repeat-row" key={index}>
          <input value={row.label} placeholder="Label" onChange={(event) => onChange(updateAt(value, index, { ...row, label: event.target.value }))} />
          <input value={row.url} placeholder="https://example.com" onChange={(event) => onChange(updateAt(value, index, { ...row, url: event.target.value }))} />
          <PrimaryToggle checked={row.is_primary} onChange={() => onChange(markPrimary(value, index))} />
          <RemoveButton onClick={() => onChange(removeAt(value, index))} />
        </div>
      ))}
    </ContactSection>
  );
}

function ContactSection({ title, onAdd, children }: { title: string; onAdd: () => void; children: ReactNode }) {
  return (
    <section className="contact-section">
      <div>
        <h2>{title}</h2>
        <button className="secondary" type="button" onClick={onAdd}>Add</button>
      </div>
      {children}
    </section>
  );
}

function PrimaryToggle({ checked, onChange }: { checked: boolean; onChange: () => void }) {
  return <label className="primary-toggle"><input type="radio" checked={checked} onChange={onChange} /> Primary</label>;
}

function RemoveButton({ onClick }: { onClick: () => void }) {
  return <button className="ghost icon-only" type="button" title="Remove" onClick={onClick}><Icon name="close" /></button>;
}

function ContactAvatar({ contact, large = false }: { contact: Contact; large?: boolean }) {
  const label = contact.display_name || primaryEmail(contact) || "?";
  if (contact.icon_url) {
    return <img className={`contact-avatar ${large ? "large" : ""}`} src={contact.icon_url} alt="" />;
  }
  return <span className={`contact-avatar ${large ? "large" : ""}`}>{label.slice(0, 1).toUpperCase()}</span>;
}

function blankContact(): Contact {
  return {
    id: 0,
    name_prefix: "",
    given_name: "",
    additional_name: "",
    family_name: "",
    name_suffix: "",
    display_name: "",
    nickname: "",
    organization: "",
    department: "",
    job_title: "",
    birthday: "",
    notes: "",
    categories: "",
    is_me: false,
    is_primary: false,
    emails: [{ label: "Email", email: "", is_primary: true }],
    phones: [],
    addresses: [],
    urls: [],
    icon_url: ""
  };
}

function cloneContact(contact: Contact): Contact {
  return JSON.parse(JSON.stringify(contact)) as Contact;
}

function primaryEmail(contact: Contact): string {
  return contact.emails.find((email) => email.is_primary && email.email.trim())?.email || contact.emails.find((email) => email.email.trim())?.email || "";
}

function updateAt<T>(items: T[], index: number, value: T): T[] {
  return items.map((item, itemIndex) => itemIndex === index ? value : item);
}

function removeAt<T>(items: T[], index: number): T[] {
  return items.filter((_item, itemIndex) => itemIndex !== index);
}

function markPrimary<T extends { is_primary: boolean }>(items: T[], index: number): T[] {
  return items.map((item, itemIndex) => ({ ...item, is_primary: itemIndex === index }));
}

function blankAddress(isPrimary: boolean): ContactAddress {
  return {
    label: "Address",
    street: "",
    locality: "",
    region: "",
    postal_code: "",
    country: "",
    is_primary: isPrimary
  };
}
