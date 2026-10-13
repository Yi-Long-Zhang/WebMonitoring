import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

vi.mock("element-plus", () => ({
  ElMessage: { success: vi.fn(), error: vi.fn() },
  ElMessageBox: { prompt: vi.fn(), alert: vi.fn() },
}));

import { ElMessageBox } from "element-plus";

import { useMonitoringActions } from "./actions";
import { useConsoleStore } from "../../stores/console";
import type { AlertChannel, Asset, DetectionJob, Tenant } from "../../types/domain";

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
  vi.clearAllMocks();
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

// The verification dialog is the operator's only instruction for proving they
// own the target. What it has to say depends on the asset's method, and the API
// names that proof by key: FILE sends `path`, DNS `record`, META `tag`. The
// dialog read `path` regardless, so a DNS asset was told to create a file
// named "undefined" -- an instruction that cannot be carried out, in the step
// that gates every detection against that target.
describe("verifyAssetOwnership", () => {
  function json(body: unknown) {
    return new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
  }

  function stubVerification(issue: Record<string, unknown>) {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        requests.push(url);
        if (url.includes("/verification")) {
          const body = JSON.parse(String(init?.body ?? "{}")) as { action?: string };
          return body.action === "ISSUE" ? json(issue) : json({ id: "asset-1", enabled: true, verificationStatus: "VERIFIED" });
        }
        return json({ items: [], nextCursor: "" });
      }),
    );
  }

  async function dialogText() {
    const actions = useMonitoringActions();
    await actions.verifyAssetOwnership({ id: "asset-1", name: "门户" } as Asset);
    const calls = vi.mocked(ElMessageBox.alert).mock.calls;
    expect(calls).toHaveLength(1);
    return String(calls[0]?.[0]);
  }

  it("tells a DNS asset to add the TXT record the API named", async () => {
    stubVerification({
      method: "DNS",
      record: "_webmonitoring-verify.example.com",
      recordType: "TXT",
      content: "webmonitoring-verification=0199b0a4",
    });
    const text = await dialogText();
    expect(text).toContain("_webmonitoring-verify.example.com");
    expect(text).toContain("webmonitoring-verification=0199b0a4");
    expect(text).not.toContain("undefined");
  });

  it("tells a META asset to paste the tag", async () => {
    stubVerification({
      method: "META",
      tag: `<meta name="webmonitoring-verification" content="webmonitoring-verification=0199b0a4">`,
      content: "webmonitoring-verification=0199b0a4",
    });
    const text = await dialogText();
    expect(text).toContain("<meta name=");
    expect(text).not.toContain("undefined");
  });

  // Verification is what actually looks for the token, so a VERIFY that fails
  // must not be swallowed into "资产已验证并启用".
  it("surfaces a failed verification and never claims success", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        requests.push(url);
        if (url.includes("/verification")) {
          const body = JSON.parse(String(init?.body ?? "{}")) as { action?: string };
          if (body.action === "VERIFY") {
            return new Response(
              JSON.stringify({ code: "VERIFICATION_FAILED", message: "未在目标站点找到有效令牌", traceId: "t" }),
              { status: 422, headers: { "content-type": "application/json" } },
            );
          }
          return json({ method: "FILE", path: "/.well-known/webmonitoring-verification.txt", content: "token" });
        }
        return json({ items: [], nextCursor: "" });
      }),
    );
    const actions = useMonitoringActions();

    await expect(actions.verifyAssetOwnership({ id: "asset-1", name: "门户" } as Asset)).rejects.toThrow(
      "未在目标站点找到有效令牌",
    );
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
