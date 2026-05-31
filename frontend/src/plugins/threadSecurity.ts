// File overview: Structural hook surface for plugins that add message-thread security handling.

import type { SecurityUnlockState } from "../appTypes";
import type { ContactPGPKey } from "../types";
import type { RuntimePlugin } from "./runtime";

export type ThreadSecuritySignatureStatus = "none" | "verified" | "unverified" | "invalid";

export type ThreadSecurityDecryptedAttachment = {
  id: string;
  filename: string;
  contentType: string;
  size: number;
  objectURL: string;
};

export type ThreadSecurityGossipKey = {
  email: string;
  publicKeyArmored: string;
};

export type ThreadSecurityOpenResult = {
  text: string;
  encrypted: boolean;
  signed: boolean;
  signatureStatus: ThreadSecuritySignatureStatus;
  signatureDetail?: string;
  signerKeyID?: string;
  protectedSubject?: string;
  secureMime?: boolean;
  symmetricAlgorithm?: string;
  encryptionKeyIDs?: string[];
  signaturePublicKeyAlgorithm?: string;
  signatureHashAlgorithm?: string;
  autocryptGossip?: ThreadSecurityGossipKey[];
};

export type ContactKeyImportResolution = {
  status: "new" | "same" | "different";
  existing?: ContactPGPKey;
};

export type ThreadSecurityRuntimePlugin = RuntimePlugin & {
  encryptedPreviewText?: string;
  publicKeys(emails: string[], all?: boolean): Promise<{ keys: ContactPGPKey[] }>;
  savePublicKey(csrf: string, key: ContactPGPKey): Promise<{ ok: boolean; key: ContactPGPKey }>;
  publicKeyRecordFromArmored(publicKeyArmored: string, email?: string, sourceKind?: string, sourceDetail?: string): Promise<ContactPGPKey>;
  autocryptKeyRecordFromMessageSource(source: string, senderEmail?: string): Promise<ContactPGPKey | null>;
  resolveContactPGPKeyImport(existingKeys: ContactPGPKey[], candidate: ContactPGPKey): ContactKeyImportResolution;
  openSecureMessageSource(source: string, keys: SecurityUnlockState["keys"], verificationKeyArmors?: string[]): Promise<ThreadSecurityOpenResult>;
  decryptedHTMLDoc(content: string, attachments?: ThreadSecurityDecryptedAttachment[]): Promise<string>;
  decryptedPlainText(content: string): string;
  decryptedMIMEAttachments(content: string): ThreadSecurityDecryptedAttachment[];
  encryptionRecipientKeyIDsFromSource(source: string): Promise<string[]>;
};

export function threadSecurityPlugin(plugins: readonly RuntimePlugin[]): ThreadSecurityRuntimePlugin | undefined {
  return plugins.find((plugin) => {
    const candidate = plugin as Partial<ThreadSecurityRuntimePlugin>;
    return Boolean(candidate.openSecureMessageSource && candidate.publicKeys && candidate.savePublicKey);
  }) as ThreadSecurityRuntimePlugin | undefined;
}
