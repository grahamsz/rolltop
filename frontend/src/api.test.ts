import { afterEach, describe, expect, it, vi } from "vitest";
import { deleteJSON, postJSON } from "./api";

describe("CSRF mutation recovery", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("refreshes a stale token once and retries the mutation", async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(new Response("bad csrf token", { status: 403 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ csrf: "fresh-token" }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    vi.stubGlobal("fetch", fetch);

    await expect(postJSON<{ ok: boolean }>("/api/example", "stale-token", { value: 1 }))
      .resolves.toEqual({ ok: true });

    expect(fetch).toHaveBeenNthCalledWith(1, "/api/example", expect.objectContaining({
      headers: expect.objectContaining({ "X-CSRF-Token": "stale-token" })
    }));
    expect(fetch).toHaveBeenNthCalledWith(2, "/api/bootstrap", { headers: { Accept: "application/json" } });
    expect(fetch).toHaveBeenNthCalledWith(3, "/api/example", expect.objectContaining({
      headers: expect.objectContaining({ "X-CSRF-Token": "fresh-token" })
    }));
  });

  it("preserves keepalive and fresh CSRF tokens when retrying a reminder deletion", async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(new Response("bad csrf token", { status: 403 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ csrf: "fresh-token" }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    vi.stubGlobal("fetch", fetch);
    await expect(deleteJSON("/api/messages/1/snooze", "stale-token", { keepalive: true })).resolves.toEqual({ ok: true });
    expect(fetch).toHaveBeenNthCalledWith(1, "/api/messages/1/snooze", expect.objectContaining({
      method: "DELETE", keepalive: true, headers: expect.objectContaining({ "X-CSRF-Token": "stale-token" })
    }));
    expect(fetch).toHaveBeenNthCalledWith(3, "/api/messages/1/snooze", expect.objectContaining({
      method: "DELETE", keepalive: true, headers: expect.objectContaining({ "X-CSRF-Token": "fresh-token" })
    }));
  });
});
