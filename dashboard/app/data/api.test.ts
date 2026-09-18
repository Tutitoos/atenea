import { describe, expect, it, vi } from "vitest";
import { api, UnauthorizedError } from "./api";

describe("dashboard API boundary", () => {
  it("dispatches auth-required without retaining the token", async () => {
    const listener = vi.fn();
    window.addEventListener("atenea:auth-required", listener);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("no", { status: 401 })));
    await expect(api.overview("24h")).rejects.toBeInstanceOf(UnauthorizedError);
    expect(listener).toHaveBeenCalledOnce();
    window.removeEventListener("atenea:auth-required", listener);
    vi.unstubAllGlobals();
  });

  it("builds same-origin requests and returns the safe envelope", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ data: { runs: 2 } }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(api.overview("1h")).resolves.toEqual({ data: { runs: 2 } });
    expect(fetchMock.mock.calls[0]?.[0]).toContain("/api/v1/overview");
    vi.unstubAllGlobals();
  });

  it("uses the server's configured limit for collection requests", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ data: { items: [] } }), { status: 200, headers: { "Content-Type": "application/json" } })));
    vi.stubGlobal("fetch", fetchMock);
    await api.overview("24h");
    await api.sessions();
    await api.runs();
    await api.metrics();
    await api.incidents();
    for (const [path] of fetchMock.mock.calls) {
      expect(new URL(String(path), "http://localhost").searchParams.has("limit")).toBe(false);
    }
    vi.unstubAllGlobals();
  });

  it("accepts the cookie-setting login response without persisting the token", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(api.login("secret-token")).resolves.toEqual({ data: { ok: true } });
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(init.credentials).toBe("same-origin");
    expect(String(init.body)).toContain("secret-token");
    vi.unstubAllGlobals();
  });
});
