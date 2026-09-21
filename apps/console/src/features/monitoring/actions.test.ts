import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

vi.mock("element-plus", () => ({
  ElMessage: { success: vi.fn(), error: vi.fn() },
  ElMessageBox: { prompt: vi.fn(), alert: vi.fn() },
}));

import { useMonitoringActions } from "./actions";
import { useConsoleStore } from "../../stores/console";
import type { AlertChannel, DetectionJob, Tenant } from "../../types/domain";

let requests: string[] = [];

function job(status: DetectionJob["status"]): DetectionJob {
  return { id: "job-1", assetId: "asset-1", mode: "HTTP", status, createdAt: new Date().toISOString() } as DetectionJob;
}

function tenant(id: string): Tenant {
  return { id, slug: "acme", name: "Acme", disabled: false, createdAt: new Date().toISOString() } as Tenant;
}

beforeEach(() => {
  setActivePinia(createPinia());
  requests = [];
});

afterEach(() => {
  vi.unstubAllGlobals();
});

// The server answers a job that already reached a terminal state with 409
// INVALID_JOB_STATE. Swallowing it would leave the operator believing the
// cancel worked, so the action must let it through with the server's message.
describe("cancelDetectionJob", () => {
  it("surfaces the conflict message and leaves the list alone", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        requests.push(String(input));
        return new Response(
          JSON.stringify({ code: "INVALID_JOB_STATE", message: "仅排队或运行中的任务可取消", traceId: "t" }),
          { status: 409, headers: { "content-type": "application/json" } },
        );
      }),
    );
    const actions = useMonitoringActions();
    const store = useConsoleStore();
    store.jobs = [job("SUCCEEDED")];

    await expect(actions.cancelDetectionJob(job("SUCCEEDED"))).rejects.toThrow("仅排队或运行中的任务可取消");

    // No refresh was attempted, and the row the operator was looking at is
    // still there with the status it actually has.
    expect(requests).toHaveLength(1);
    expect(store.jobs.map((item) => item.status)).toEqual(["SUCCEEDED"]);
  });

  it("refreshes the list after the cancel lands", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        requests.push(url);
        if (url.includes("/cancel")) {
          return new Response(JSON.stringify(job("CANCELLED")), { status: 200, headers: { "content-type": "application/json" } });
        }
        if (url.includes("/detection-jobs")) {
          return new Response(JSON.stringify({ items: [job("CANCELLED")], nextCursor: "" }), { status: 200, headers: { "content-type": "application/json" } });
        }
        return new Response(JSON.stringify({ items: [], nextCursor: "" }), { status: 200, headers: { "content-type": "application/json" } });
      }),
    );
    const actions = useMonitoringActions();
    const store = useConsoleStore();

    await actions.cancelDetectionJob(job("RUNNING"));

    expect(requests.some((url) => url.includes("/detection-jobs/job-1/cancel"))).toBe(true);
    expect(store.jobs.map((item) => item.status)).toEqual(["CANCELLED"]);
  });
});

describe("setTenantDisabled", () => {
  it("sends only the disabled flag", async () => {
    let body: Record<string, unknown> = {};
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        requests.push(url);
        if (init?.method === "PATCH") {
          body = JSON.parse(String(init.body));
          return new Response(JSON.stringify({ ...tenant("t1"), disabled: true }), { status: 200, headers: { "content-type": "application/json" } });
        }
        return new Response(JSON.stringify({ items: [tenant("t1")], nextCursor: "" }), { status: 200, headers: { "content-type": "application/json" } });
      }),
    );
    const actions = useMonitoringActions();

    await actions.setTenantDisabled(tenant("t1"), true);

    // A PATCH carrying name and slug would re-run the slug uniqueness check, so
    // flipping a switch could 409 TENANT_EXISTS for an unrelated reason.
    expect(body).toEqual({ disabled: true });
    expect(Object.keys(body)).not.toContain("slug");
    expect(Object.keys(body)).not.toContain("name");
  });

  it("surfaces the protection conflict for the caller's own tenant", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response(
          JSON.stringify({ code: "TENANT_PROTECTED", message: "默认租户或当前登录租户不可停用", traceId: "t" }),
          { status: 409, headers: { "content-type": "application/json" } },
        ),
      ),
    );
    const actions = useMonitoringActions();

    await expect(actions.setTenantDisabled(tenant("self"), true)).rejects.toThrow("默认租户或当前登录租户不可停用");
  });
});

// A channel test is the one channel action that reports a failure rather than a
// success toast. The operator asked a direct question about whether the channel
// works, so "已发送测试消息" on a channel that just returned 401 would be the
// wrong answer -- and it must not refresh the list, because nothing changed.
describe("testChannel", () => {
  it("surfaces the delivery error the server reported", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        requests.push(String(input));
        return new Response(
          JSON.stringify({ code: "CHANNEL_TEST_FAILED", message: "notification endpoint returned 401", traceId: "t" }),
          { status: 502, headers: { "content-type": "application/json" } },
        );
      }),
    );
    const actions = useMonitoringActions();
    const channel = { id: "channel-1", name: "运维群" } as AlertChannel;

    await expect(actions.testChannel(channel)).rejects.toThrow("notification endpoint returned 401");

    expect(requests).toEqual(["/api/v1/alert-channels/channel-1/test"]);
  });

  it("reports success without refreshing the channel list", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        requests.push(String(input));
        return new Response(JSON.stringify({ status: "DELIVERED" }), { status: 200, headers: { "content-type": "application/json" } });
      }),
    );
    const actions = useMonitoringActions();

    await actions.testChannel({ id: "channel-1", name: "运维群" } as AlertChannel);

    // One request: the test itself. A refresh would be a second, and a test
    // changes nothing about the channel list.
    expect(requests).toHaveLength(1);
  });
});
