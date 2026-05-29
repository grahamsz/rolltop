import type { ComponentType, ReactNode } from "react";
import type { PGPUnlockState, Toast } from "../../../frontend/src/appTypes";
import type { ContactPGPKey, IdentityPGPPrivateKey } from "../../../frontend/src/types";

export type PGPSignatureStatus = "none" | "verified" | "unverified" | "invalid";

export type PGPMessageOpenResult = {
  text: string;
  encrypted: boolean;
  pgpMime?: boolean;
  signed: boolean;
  signatureStatus: PGPSignatureStatus;
  signatureDetail: string;
  signerKeyID?: string;
  signatureHashAlgorithm?: string;
  signaturePublicKeyAlgorithm?: string;
  encryptionKeyIDs?: string[];
  symmetricAlgorithm?: string;
  protectedSubject?: string;
  autocryptGossip?: AutocryptGossipKey[];
};

export type AutocryptGossipKey = {
  email: string;
  publicKeyArmored: string;
};

export type MessageSecurityState = {
  is_encrypted: boolean;
  is_signed: boolean;
};

export type MessageSecurityIndicatorContext = {
  location: "message-list" | "thread-summary";
  message?: unknown;
  state: MessageSecurityState;
};

export type PGPMIMEAttachmentInput = {
  filename: string;
  contentType: string;
  contentID?: string;
  inline?: boolean;
  data: Uint8Array;
};

export type DecryptedMIMEAttachment = {
  id: string;
  filename: string;
  contentType: string;
  contentID: string;
  inline: boolean;
  size: number;
  objectURL: string;
};

export type PGPUnlockDialogProps = {
  userID: number;
  identityID: number | null;
  recipientKeyIDs: string[];
  onClose: () => void;
  onUnlocked: (state: PGPUnlockState) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
};

export type PGPKeyImportModalProps = {
  title: string;
  description: string;
  placeholder: string;
  importLabel?: string;
  busy?: boolean;
  onCancel: () => void;
  onImport: (armored: string) => Promise<void> | void;
};

export type ContactPGPKeyImportResolution = {
  status: "new" | "same" | "different";
  existing?: ContactPGPKey;
};

export type PGPKeyGenerateModalProps = {
  email: string;
  busy?: boolean;
  validatePassphrase: (passphrase: string) => string[];
  onCancel: () => void;
  onGenerate: (passphrase: string) => Promise<void> | void;
};

export type ClientSidePGPPlugin = {
  UnlockDialog: ComponentType<PGPUnlockDialogProps>;
  KeyImportModal: ComponentType<PGPKeyImportModalProps>;
  KeyGenerateModal: ComponentType<PGPKeyGenerateModalProps>;
  privateKeys(): Promise<{ keys: IdentityPGPPrivateKey[] }>;
  savePrivateKey(csrf: string, key: IdentityPGPPrivateKey): Promise<{ ok: boolean; key: IdentityPGPPrivateKey }>;
  deletePrivateKey(csrf: string, id: number): Promise<{ ok: boolean }>;
  publicKeys(emails: string[], all?: boolean): Promise<{ keys: ContactPGPKey[] }>;
  savePublicKey(csrf: string, key: ContactPGPKey): Promise<{ ok: boolean; key: ContactPGPKey }>;
  serializeUnlockState(state: PGPUnlockState): Promise<unknown>;
  restoreUnlockState(state: unknown): Promise<PGPUnlockState>;
  publicKeyRecordFromArmored(publicKeyArmored: string, email?: string, sourceKind?: string, sourceDetail?: string): Promise<ContactPGPKey>;
  privateKeyRecordFromArmoredSource(privateKeyArmored: string, publicKeyArmored?: string, email?: string): Promise<IdentityPGPPrivateKey>;
  generatePrivateKey(name: string, email: string, passphrase: string): Promise<IdentityPGPPrivateKey>;
  pgpPassphraseIssues(passphrase: string, identityValues: string[]): string[];
  pgpUserIDsMatchEmail(userIDs: string, email: string): boolean;
  pgpUserIDEmails(userIDs: string): string[];
  saveBrowserPGPPrivateKey(userID: number, key: IdentityPGPPrivateKey, privateKeyArmored: string): Promise<void>;
  loadBrowserPGPPrivateKey(userID: number, keyID: number): Promise<string>;
  deleteBrowserPGPPrivateKey(userID: number, keyID: number): Promise<void>;
  encryptedPreviewText: string;
  previewText(snippet: string, isEncrypted: boolean, isSigned: boolean): string;
  messageSecurityPreviewText(snippet: string, state: MessageSecurityState): string;
  messageSecuritySnippetClassName(state: MessageSecurityState): string;
  renderMessageSecurityIndicators(context: MessageSecurityIndicatorContext): ReactNode;
  encryptMessageText(text: string, recipientKeys: ContactPGPKey[], signingKey?: PGPUnlockState["keys"][number]): Promise<string>;
  signPGPMIMEEntity(entity: string, signingKey: PGPUnlockState["keys"][number]): Promise<string>;
  pgpMIMEEntityFromBody(text: string, html: string, attachments?: PGPMIMEAttachmentInput[]): string;
  addAutocryptGossipHeaders(payload: string, keys: ContactPGPKey[]): string;
  encryptionKeyRecordsForRecipients(recipientEmails: string[], candidateKeys: ContactPGPKey[]): Promise<ContactPGPKey[]>;
  decryptPGPSource(source: string, keys: PGPUnlockState["keys"], verificationKeyArmors?: string[]): Promise<PGPMessageOpenResult>;
  decryptedHTMLDoc(content: string, attachments?: DecryptedMIMEAttachment[]): Promise<string>;
  decryptedPlainText(content: string): string;
  decryptedMIMEAttachments(content: string): DecryptedMIMEAttachment[];
  encryptionRecipientKeyIDsFromSource(source: string): Promise<string[]>;
  resolveContactPGPKeyImport(existingKeys: ContactPGPKey[], candidate: ContactPGPKey): ContactPGPKeyImportResolution;
  autocryptKeyRecordFromMessageSource(source: string, senderEmail?: string): Promise<ContactPGPKey | null>;
};
