import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useSessionStore } from "./session";

const sessionBody = {
  user: { id: "u1", tenantId: "t1", username: "admin", role: "ADMIN" },
  tenant: { id: "t1", slug: "default", name: "Default" },
  csrfToken: "csrf-value",
  expiresAt: "2026-10-10T00:00:00Z",
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

beforeEach(() => {
  setActivePinia(createPinia());
});

afterEach(() => {
  vi.unstubAllGlobals();
});

// restore() answers two different questions with the same `false`: "this
// browser has no session" (normal, silent) and "the API could not be reached"
// (the operator must be told, because the login form they are looking at will
// reject a correct password). These tests exist to keep those two apart.
describe("session store restore", () => {
  it("stays silent on 401, which is the ordinary signed-out answer", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json({ message: "未登录", code: "UNAUTHENTICATED" }, 401)));
    const session = useSessionStore();

    expect(await session.restore()).toBe(false);
    expect(session.restoreFailure).toBeUndefined();
  });

  it("records a failure when the API answers with a server error", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json({ message: "数据库不可用" }, 503)));
    const session = useSessionStore();

    expect(await session.restore()).toBe(false);
    expect(session.restoreFailure).toBe("数据库不可用");
  });

  it("records a failure when the request never reaches the API", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Failed to fetch")));
    const session = useSessionStore();

    expect(await session.restore()).toBe(false);
    expect(session.restoreFailure).toBe("Failed to fetch");
  });

  it("clears a recorded failure once a restore succeeds", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Failed to fetch")));
    const session = useSessionStore();
    await session.restore();
    expect(session.restoreFailure).toBe("Failed to fetch");

    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json(sessionBody)));
    expect(await session.restore()).toBe(true);
    expect(session.restoreFailure).toBeUndefined();
    expect(session.isAuthenticated).toBe(true);
  });
});
