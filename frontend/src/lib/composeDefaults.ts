import type { ComposeForm } from "../types";

export const emptyCompose: ComposeForm = {
  to: "",
  cc: "",
  bcc: "",
  subject: "",
  body: "",
  body_html: "",
  in_reply_to_id: 0,
  from_identity_id: 0
};
