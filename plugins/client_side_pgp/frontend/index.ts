import { createElement } from "react";
import { deletePGPPrivateKey, deletePGPPublicKey, pgpPrivateKeys, pgpPublicKeys, savePGPPrivateKey, savePGPPublicKey } from "./api/keys";
import { PGPKeyGenerateModal } from "./components/PGPKeyGenerateModal";
import { PGPKeyImportModal } from "./components/PGPKeyImportModal";
import { ContactPGPKeyEditor } from "./contact/ContactPGPKeyEditor";
import { IdentityPGPSettings } from "./settings/IdentityPGPSettings";
import {
  addAutocryptGossipHeaders,
  autocryptKeyRecordFromMessageSource,
  decryptPGPSource,
  decryptedHTMLDoc,
  decryptedMIMEAttachments,
  decryptedPlainText,
  encryptMessageText,
  encryptionKeyRecordsForRecipients,
  encryptionRecipientKeyIDsFromSource,
  generatePrivateKey,
  openSecureMessageSource,
  pgpMIMEEntityFromBody,
  pgpPassphraseIssues,
  pgpUserIDEmails,
  pgpUserIDsMatchEmail,
  privateKeyRecordFromArmoredSource,
  publicKeyRecordFromArmored,
  restorePGPUnlockState,
  serializePGPUnlockState,
  resolveContactPGPKeyImport,
  signPGPMIMEEntity
} from "./crypto/pgp";
import { pgpMessageSecurityPreviewText, pgpMessageSecuritySnippetClassName, renderPGPMessageSecurityIndicators } from "./messageSecurity/indicators";
import { ENCRYPTED_PREVIEW_TEXT, pgpPreviewText } from "./messageSecurity/preview";
import { deleteBrowserPGPPrivateKey, loadBrowserPGPPrivateKey, saveBrowserPGPPrivateKey } from "./storage/browserPGPKeys";
import { PGPUnlockDialog } from "./components/PGPUnlockDialog";
import type { ClientSidePGPPlugin } from "./types";

export type * from "./types";
export { resolveContactPGPKeyImport } from "./crypto/pgp";

const plugin: ClientSidePGPPlugin = {
  UnlockDialog: PGPUnlockDialog,
  KeyImportModal: PGPKeyImportModal,
  KeyGenerateModal: PGPKeyGenerateModal,
  renderContactKeyEditor: (context) => createElement(ContactPGPKeyEditor, {
    csrf: context.csrf,
    contactID: context.contactID,
    emails: context.emails,
    pgpPlugin: plugin,
    addToast: context.addToast
  }),
  renderIdentitySecuritySettings: (context) => createElement(IdentityPGPSettings, {
    csrf: context.csrf,
    user: context.user,
    identities: context.identities,
    identityDraft: context.identityDraft,
    updateIdentityDraft: context.updateIdentityDraft,
    markIdentitySecurityReady: context.markIdentitySecurityReady,
    pgpPlugin: plugin,
    addToast: context.addToast
  }),
  privateKeys: pgpPrivateKeys,
  savePrivateKey: savePGPPrivateKey,
  deletePrivateKey: deletePGPPrivateKey,
  publicKeys: pgpPublicKeys,
  savePublicKey: savePGPPublicKey,
  deletePublicKey: deletePGPPublicKey,
  serializeUnlockState: serializePGPUnlockState,
  restoreUnlockState: restorePGPUnlockState,
  publicKeyRecordFromArmored,
  privateKeyRecordFromArmoredSource,
  generatePrivateKey,
  pgpPassphraseIssues,
  pgpUserIDsMatchEmail,
  pgpUserIDEmails,
  saveBrowserPGPPrivateKey,
  loadBrowserPGPPrivateKey,
  deleteBrowserPGPPrivateKey,
  encryptedPreviewText: ENCRYPTED_PREVIEW_TEXT,
  previewText: pgpPreviewText,
  messageSecurityPreviewText: pgpMessageSecurityPreviewText,
  messageSecuritySnippetClassName: pgpMessageSecuritySnippetClassName,
  renderMessageSecurityIndicators: renderPGPMessageSecurityIndicators,
  encryptMessageText,
  signPGPMIMEEntity,
  pgpMIMEEntityFromBody,
  addAutocryptGossipHeaders,
  encryptionKeyRecordsForRecipients,
  decryptPGPSource,
  openSecureMessageSource,
  decryptedHTMLDoc,
  decryptedPlainText,
  decryptedMIMEAttachments,
  encryptionRecipientKeyIDsFromSource,
  resolveContactPGPKeyImport,
  autocryptKeyRecordFromMessageSource
};

export default plugin;
