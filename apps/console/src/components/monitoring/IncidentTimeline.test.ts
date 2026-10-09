import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import IncidentTimeline from "./IncidentTimeline.vue";
import type { IncidentEvent } from "../../types/domain";

// This is the one component in the incident view that owns its own request, so
// it is also the one place where the three states an operator actually sees --
// loading, empty, failed -- are the component's business rather than the
// shell's. They are asserted here because each of them used to be a silent
// blank: a skeleton that never resolves, a timeline with no entries, and a
// failure that looked exactly like an incident with no history.

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

function event(overrides: Partial<IncidentEvent> = {}): IncidentEvent {
  return {
    id: "e1",
    incidentId: "i1",
    type: "STATUS_CHANGED",
    fromStatus: "OPEN",
    toStatus: "ACKNOWLEDGED",
    title: "认领事件",
    createdAt: "2026-10-01T09:30:00Z",
    ...overrides,
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("IncidentTimeline", () => {
  it("shows a skeleton while the timeline is in flight", async () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise(() => {})));
    const wrapper = mount(IncidentTimeline, { props: { incidentId: "i1" } });
    await flushPromises();

    expect(wrapper.find(".el-skeleton").exists()).toBe(true);
    expect(wrapper.find(".el-empty").exists()).toBe(false);
    wrapper.unmount();
  });

  it("says there is no history rather than showing nothing", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json({ items: [], nextCursor: "" })));
    const wrapper = mount(IncidentTimeline, { props: { incidentId: "i1" } });
    await flushPromises();

    expect(wrapper.find(".el-skeleton").exists()).toBe(false);
    expect(wrapper.text()).toContain("暂无时间线记录");
  });

  it("shows the failure and a retry that refetches", async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({ message: "时间线不可用" }, 503));
    vi.stubGlobal("fetch", fetchMock);
    const wrapper = mount(IncidentTimeline, { props: { incidentId: "i1" } });
    await flushPromises();

    expect(wrapper.text()).toContain("时间线不可用");
    // The failure must not be presented as an empty history: those two look
    // identical on screen and mean opposite things.
    expect(wrapper.text()).not.toContain("暂无时间线记录");

    fetchMock.mockResolvedValue(json({ items: [event()], nextCursor: "" }));
    await wrapper.findAll("button").find((button) => button.text() === "重试")?.trigger("click");
    await flushPromises();

    // The refetched entry is on screen, and it replaced the alert rather than
    // sitting under it.
    expect(wrapper.text()).toContain("OPEN → ACKNOWLEDGED");
    expect(wrapper.text()).not.toContain("时间线不可用");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("renders a status transition as from → to", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json({ items: [event()], nextCursor: "" })));
    const wrapper = mount(IncidentTimeline, { props: { incidentId: "i1" } });
    await flushPromises();

    expect(wrapper.text()).toContain("OPEN → ACKNOWLEDGED");
  });
});
