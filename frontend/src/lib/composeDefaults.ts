// File overview: Default compose payload used before the server returns a reply, forward, or blank
// compose form.

import type { ComposeForm } from "../types";

/** emptyCompose is the blank compose payload used before a server-provided draft is loaded. */
export const emptyCompose: ComposeForm = {
  to: "",
  cc: "",
  bcc: "",
  subject: "",
  body: "",
  body_html: "",
  in_reply_to_id: 0,
  from_identity_id: 0,
  available_attachments: [],
  include_attachment_ids: []
};
