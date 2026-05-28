import { deletePGPPrivateKey, pgpPrivateKeys, pgpPublicKeys, savePGPPrivateKey, savePGPPublicKey } from "./api/keys";
import { PGPKeyGenerateModal } from "./components/PGPKeyGenerateModal";
import { PGPKeyImportModal } from "./components/PGPKeyImportModal";
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
  pgpMIMEEntityFromBody,
  pgpPassphraseIssues,
  pgpUserIDEmails,
  pgpUserIDsMatchEmail,
  privateKeyRecordFromArmoredSource,
  publicKeyRecordFromArmored,
  restorePGPUnlockState,
  serializePGPUnlockState,
  signPGPMIMEEntity
} from "./crypto/pgp";
import { pgpMessageSecurityPreviewText, pgpMessageSecuritySnippetClassName, renderPGPMessageSecurityIndicators } from "./messageSecurity/indicators";
import { ENCRYPTED_PREVIEW_TEXT, pgpPreviewText } from "./messageSecurity/preview";
import { deleteBrowserPGPPrivateKey, loadBrowserPGPPrivateKey, saveBrowserPGPPrivateKey } from "./storage/browserPGPKeys";
import { PGPUnlockDialog } from "./components/PGPUnlockDialog";
import type { ClientSidePGPPlugin } from "./types";

export type * from "./types";

const plugin: ClientSidePGPPlugin = {
  UnlockDialog: PGPUnlockDialog,
  KeyImportModal: PGPKeyImportModal,
  KeyGenerateModal: PGPKeyGenerateModal,
  privateKeys: pgpPrivateKeys,
  savePrivateKey: savePGPPrivateKey,
  deletePrivateKey: deletePGPPrivateKey,
  publicKeys: pgpPublicKeys,
  savePublicKey: savePGPPublicKey,
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
  decryptedHTMLDoc,
  decryptedPlainText,
  decryptedMIMEAttachments,
  encryptionRecipientKeyIDsFromSource,
  autocryptKeyRecordFromMessageSource
};

export default plugin;
