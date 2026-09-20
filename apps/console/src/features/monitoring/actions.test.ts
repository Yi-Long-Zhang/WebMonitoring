import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

vi.mock("element-plus", () => ({
  ElMessage: { success: vi.fn(), error: vi.fn() },
  ElMessageBox: { prompt: vi.fn(), alert: vi.fn() },
}));

import { useMonitoringActions } from "./actions";
import { useConsoleStore } from "../../stores/console";
import type { DetectionJob } from "../../types/domain";

let requests: string[] = [];

function job(status: DetectionJob["status"]): DetectionJob {
  return { id: "job-1", assetId: "asset-1", mode: "HTTP", status, createdAt: new Date().toISOString() } as DetectionJob;
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
