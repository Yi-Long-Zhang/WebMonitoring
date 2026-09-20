import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useConsoleStore } from "./console";

type RequestedPage = { path: string; params: URLSearchParams };

let requested: RequestedPage[] = [];
// Keyed by the cursor the request carried, so a test can change what the head
// page returns between refreshes.
let assetPages: Record<string, { items: Array<{ id: string }>; nextCursor: string }> = {};

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

function installFetch() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://localhost");
      requested.push({ path: url.pathname, params: url.searchParams });
      if (url.pathname === "/api/v1/assets") {
        return json(assetPages[url.searchParams.get("cursor") ?? ""] ?? { items: [], nextCursor: "" });
      }
      if (url.pathname === "/api/v1/audit-events") {
        // A narrowed request is served from a different set, which is what makes
        // a stale tail detectable: the filtered answer shares no id with the
        // unfiltered one.
        if (url.searchParams.get("action") === "asset.create") {
          return json({ items: [{ id: "f1" }], nextCursor: "" });
        }
        const cursor = url.searchParams.get("cursor") ?? "";
        if (cursor === "auc1") return json({ items: [{ id: "u3" }], nextCursor: "" });
        return json({ items: [{ id: "u1" }, { id: "u2" }], nextCursor: "auc1" });
      }
      if (url.pathname === "/api/v1/dashboard") return json({});
      if (url.pathname === "/api/v1/reports/summary") return json({});
      return json({ items: [], nextCursor: "" });
    }),
  );
}

function assetRequests() {
  return requested.filter((entry) => entry.path === "/api/v1/assets");
}

function auditRequests() {
  return requested.filter((entry) => entry.path === "/api/v1/audit-events");
}

beforeEach(() => {
  setActivePinia(createPinia());
  requested = [];
  assetPages = {
    "": { items: [{ id: "a1" }, { id: "a2" }], nextCursor: "c1" },
    c1: { items: [{ id: "a3" }, { id: "a4" }], nextCursor: "c2" },
  };
  installFetch();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

// Without the limit the server applies its own default and the console never
// learns there is more, which was exactly the old behaviour: every list showed
// page one and silently dropped the rest.
describe("console store pagination", () => {
  it("asks for a bounded head page on refresh", async () => {
    const store = useConsoleStore();
    await store.refresh();

    const [first] = assetRequests();
    expect(first).toBeDefined();
    expect(first.params.get("limit")).toBe("50");
    expect(first.params.get("cursor")).toBeNull();
    expect(store.assets.map((asset) => asset.id)).toEqual(["a1", "a2"]);
    expect(store.hasMore("assets")).toBe(true);
  });

  it("sends the returned cursor when loading more and appends the page", async () => {
    const store = useConsoleStore();
    await store.refresh();
    await store.loadMore("assets");

    const second = assetRequests()[1];
    expect(second.params.get("limit")).toBe("50");
    expect(second.params.get("cursor")).toBe("c1");
    expect(store.assets.map((asset) => asset.id)).toEqual(["a1", "a2", "a3", "a4"]);
    expect(store.hasMore("assets")).toBe(true);
  });

  it("stops offering more once the cursor runs out", async () => {
    assetPages = { "": { items: [{ id: "a1" }], nextCursor: "" } };
    const store = useConsoleStore();
    await store.refresh();

    expect(store.hasMore("assets")).toBe(false);
  });

  it("keeps the pages already loaded when a refresh fires mid-session", async () => {
    const store = useConsoleStore();
    await store.refresh();
    await store.loadMore("assets");
    expect(store.assets).toHaveLength(4);

    // refresh() is the SSE callback, so it fires on every job and incident
    // event. A new row lands at the head; the tail the operator pulled in has
    // to survive, and the cursor has to stay past that tail rather than
    // rewind to the head page's cursor.
    assetPages[""] = { items: [{ id: "a0" }, { id: "a1" }], nextCursor: "c0" };
    await store.refresh();

    expect(store.assets.map((asset) => asset.id)).toEqual(["a0", "a1", "a2", "a3", "a4"]);
    expect(assetRequests().at(-1)?.params.get("cursor")).toBeNull();

    // The next load-more must continue from where the tail ended, not from c0.
    await store.loadMore("assets");
    expect(assetRequests().at(-1)?.params.get("cursor")).toBe("c2");
  });

  it("does not duplicate a row that appears in both the head and the tail", async () => {
    const store = useConsoleStore();
    await store.refresh();
    await store.loadMore("assets");

    assetPages[""] = { items: [{ id: "a4" }, { id: "a5" }], nextCursor: "c1" };
    await store.refresh();

    expect(store.assets.map((asset) => asset.id)).toEqual(["a4", "a5", "a1", "a2", "a3"]);
    expect(new Set(store.assets.map((asset) => asset.id)).size).toBe(store.assets.length);
  });

  it("ignores a load-more while one is already in flight", async () => {
    const store = useConsoleStore();
    await store.refresh();

    await Promise.all([store.loadMore("assets"), store.loadMore("assets")]);
    // One request, not two: a double click must not interleave two cursors and
    // duplicate the page.
    expect(assetRequests().filter((entry) => entry.params.get("cursor") === "c1")).toHaveLength(1);
    expect(store.assets.map((asset) => asset.id)).toEqual(["a1", "a2", "a3", "a4"]);
  });

  it("sends the chosen audit filter and drops the pages it no longer matches", async () => {
    const store = useConsoleStore();
    await store.refresh();
    await store.loadMore("audits");
    expect(store.audits.map((event) => event.id)).toEqual(["u1", "u2", "u3"]);

    await store.applyAuditFilters({ action: "asset.create" });

    const last = auditRequests().at(-1);
    expect(last?.params.get("action")).toBe("asset.create");
    // No cursor: a changed query restarts from the head.
    expect(last?.params.get("cursor")).toBeNull();
    // The narrowed answer replaced the list. Merging would have kept u1..u3,
    // which the new filter excludes.
    expect(store.audits.map((event) => event.id)).toEqual(["f1"]);
  });

  it("keeps the audit filter across an SSE-triggered refresh", async () => {
    const store = useConsoleStore();
    await store.applyAuditFilters({ action: "asset.create" });

    // refresh() is the SSE callback; the operator's filter has to survive it.
    await store.refresh();

    expect(auditRequests().at(-1)?.params.get("action")).toBe("asset.create");
    expect(store.audits.map((event) => event.id)).toEqual(["f1"]);
  });

  it("clears the loading flag when a refresh fails", async () => {
    const store = useConsoleStore();
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify({ code: "STORAGE_UNAVAILABLE", message: "boom" }), { status: 500, headers: { "content-type": "application/json" } })),
    );

    await expect(store.refresh()).rejects.toThrow("boom");
    expect(store.loading).toBe(false);
  });
});
