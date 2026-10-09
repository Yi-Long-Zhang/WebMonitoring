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

  // What a proxy answers when the API behind it is down: HTML, not the JSON
  // error envelope. The console used to surface "502 Bad Gateway" verbatim.
  it("explains a status the API did not wrap in JSON", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response("<html><body>502 Bad Gateway</body></html>", {
          status: 502,
          headers: { "content-type": "text/html" },
        }),
      ),
    );

    await expect(request("/assets")).rejects.toThrow("服务端错误（502），控制台无法完成本次请求");
  });
});
