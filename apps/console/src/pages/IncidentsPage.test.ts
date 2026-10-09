import { describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import IncidentsPage from "./IncidentsPage.vue";
import { tableStubs } from "../test/elTableStub";
import type { Asset, Incident } from "../types/domain";

// Same shape as the assets page test: the page is prop-driven, so mounting it is
// the whole of what it decides. What is specific to incidents is that the
// actions depend on the row's status -- a "认领" button on an already
// acknowledged incident, or a "标记恢复" on one that was never investigated,
// would each send a transition the API rejects. The table is stubbed; see
// src/test/elTableStub.ts.

function incident(overrides: Partial<Incident> = {}): Incident {
  return {
    id: "i1",
    assetId: "a1",
    severity: "HIGH",
    status: "OPEN",
    title: "首页被注入脚本",
    occurrenceCount: 3,
    firstSeenAt: "2026-10-01T08:00:00Z",
    lastSeenAt: "2026-10-01T09:30:00Z",
    evidenceIds: [],
    ...overrides,
  };
}

const assetMap = new Map<string, Asset>([
  ["a1", {
    id: "a1",
    tenantId: "t1",
    name: "官网首页",
    url: "https://example.com",
    type: "OFFICIAL",
    enabled: true,
    verificationStatus: "VERIFIED",
    createdAt: "2026-10-01T00:00:00Z",
    updatedAt: "2026-10-01T00:00:00Z",
  }],
]);

const baseProps = {
  incidents: [] as Incident[],
  assetMap,
  evidence: [],
  deliveries: [],
  users: [],
  canOperate: true,
  canAdmin: true,
  timelineKey: 0,
  onComment: vi.fn(async () => {}),
  hasMore: false,
  loadingMore: false,
  firstLoad: false,
};

function render(props: Partial<typeof baseProps> = {}) {
  return mount(IncidentsPage, { props: { ...baseProps, ...props }, global: { stubs: tableStubs } });
}

function actionsColumn(wrapper: ReturnType<typeof render>) {
  return wrapper.find('[data-column="处置"]');
}

describe("IncidentsPage", () => {
  it("says the list is empty rather than rendering a bare table", () => {
    const wrapper = render();

    expect(wrapper.text()).toContain("暂无安全事件");
    expect(wrapper.findAll(".el-table__row")).toHaveLength(0);
  });

  it("shows a skeleton rather than the empty state while the first load is in flight", () => {
    const wrapper = render({ firstLoad: true });

    expect(wrapper.find(".table-skeleton").exists()).toBe(true);
    expect(wrapper.text()).not.toContain("暂无安全事件");
  });

  it("renders the incident with its asset url, severity and count", () => {
    const wrapper = render({ incidents: [incident()] });

    expect(wrapper.text()).toContain("首页被注入脚本");
    expect(wrapper.text()).toContain("https://example.com");
    expect(wrapper.text()).toContain("HIGH");
    expect(wrapper.text()).toContain("3");
  });

  it("offers only the transition the row's status allows", () => {
    const open = render({ incidents: [incident({ status: "OPEN" })] });
    expect(actionsColumn(open).text()).toContain("认领");
    expect(actionsColumn(open).text()).not.toContain("开始调查");
    expect(actionsColumn(open).text()).not.toContain("标记恢复");

    const acknowledged = render({ incidents: [incident({ status: "ACKNOWLEDGED" })] });
    expect(actionsColumn(acknowledged).text()).toContain("开始调查");
    expect(actionsColumn(acknowledged).text()).not.toContain("认领");

    const investigating = render({ incidents: [incident({ status: "INVESTIGATING" })] });
    expect(actionsColumn(investigating).text()).toContain("标记恢复");
    expect(actionsColumn(investigating).text()).not.toContain("开始调查");

    // A closed incident has no transition left, and the export links stay.
    const closed = render({ incidents: [incident({ status: "CLOSED" })] });
    expect(actionsColumn(closed).text()).not.toContain("认领");
    expect(actionsColumn(closed).text()).toContain("导出证据");
  });

  it("emits the transition with the incident it belongs to", async () => {
    const wrapper = render({ incidents: [incident({ id: "i9", status: "ACKNOWLEDGED", ownerId: "u1" })] });

    const start = actionsColumn(wrapper).findAll("button").find((button) => button.text() === "开始调查");
    await start?.trigger("click");

    const emitted = wrapper.emitted("transition");
    expect(emitted).toHaveLength(1);
    expect((emitted?.[0]?.[0] as Incident).id).toBe("i9");
    expect(emitted?.[0]?.[1]).toBe("INVESTIGATING");
    // The owner travels with the transition: the API validates a state change on
    // every call, so an empty owner here would silently unassign the incident.
    expect(emitted?.[0]?.[2]).toBe("u1");
  });

  it("offers no mutating action to a read-only role", () => {
    const wrapper = render({ incidents: [incident({ status: "OPEN" })], canOperate: false });

    expect(actionsColumn(wrapper).text()).not.toContain("认领");
    expect(actionsColumn(wrapper).text()).not.toContain("误报");
    expect(actionsColumn(wrapper).text()).toContain("导出证据");
  });

  it("disables the export for a non-admin instead of hiding it", () => {
    const wrapper = render({ canAdmin: false });

    const exportButton = wrapper.findAll("button").find((button) => button.text() === "导出 CSV");
    expect(exportButton?.attributes("disabled")).toBeDefined();

    const admin = render();
    const enabled = admin.findAll("button").find((button) => button.text() === "导出 CSV");
    expect(enabled?.attributes("disabled")).toBeUndefined();
  });

  it("exports what the filter selects, not what the page shows", async () => {
    const wrapper = render({ canAdmin: true });

    await wrapper.find('input[placeholder="标题关键字"]').setValue("注入");
    await wrapper.findAll("button").find((button) => button.text() === "导出 CSV")?.trigger("click");

    // The keyword box is the filter whose value is free text, so it is the one
    // that proves the export carries the operator's input rather than the
    // defaults the selects happen to hold.
    expect(wrapper.emitted("export")?.[0]?.[0]).toMatchObject({ keyword: "注入" });
  });
});
