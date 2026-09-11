import { afterEach, describe, expect, it, vi } from "vitest";
import { request, setCsrfToken } from "./client";

afterEach(() => {
  vi.unstubAllGlobals();
  setCsrfToken("");
});

describe("API client", () => {
  it("adds CSRF token to mutating requests", async () => {
    setCsrfToken("csrf-value");
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ id: "asset" }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await request("/assets", { method: "POST", body: "{}" });

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(new Headers(init.headers).get("X-CSRF-Token")).toBe("csrf-value");
    expect(init.credentials).toBe("include");
  });

  it("uses the API error message", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ code: "FORBIDDEN", message: "无权执行" }), {
          status: 403,
          headers: { "content-type": "application/json" },
        }),
      ),
    );

    await expect(request("/assets")).rejects.toThrow("无权执行");
  });
});
