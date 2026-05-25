// File overview: Error-message extraction helpers for user-visible toast and panel failures.

import { ApiError } from "../api";

/** messageFromError extracts a useful user-facing message from thrown API or runtime errors. */
export function messageFromError(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return "Something went wrong.";
}
