// File overview: Display helpers that keep PGP armor out of list and collapsed-message previews.

export const ENCRYPTED_PREVIEW_TEXT = "Lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor";

export function pgpPreviewText(snippet: string, isEncrypted: boolean, isSigned: boolean): string {
  if (isEncrypted) return ENCRYPTED_PREVIEW_TEXT;
  if (!isSigned) return snippet;
  const cleaned = stripSignedArmorFromPreview(snippet);
  return cleaned || "PGP signed message";
}

function stripSignedArmorFromPreview(value: string): string {
  let cleaned = value.replace(/-*\s*BEGIN PGP SIGNED MESSAGE-*/gi, " ");
  cleaned = cleaned.replace(/\bHash:\s*\S+/gi, " ");
  const signatureStart = cleaned.search(/-*\s*BEGIN PGP SIGNATURE-*/i);
  if (signatureStart >= 0) cleaned = cleaned.slice(0, signatureStart);
  cleaned = cleaned.replace(/-*\s*END PGP SIGNATURE-*/gi, " ");
  cleaned = cleaned.replace(/\s+/g, " ").trim();
  return cleaned;
}
