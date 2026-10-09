import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import AssetsPage from "./AssetsPage.vue";
import { tableStubs } from "../test/elTableStub";
import type { Asset, Policy } from "../types/domain";

// The page is a prop-driven table, so mounting it exercises the whole of what it
// decides: which rows it shows, which actions a role is offered, and whether the
// pagination footer can be reached. Those are the three things an operator
// notices when they are wrong -- an empty page that should have rows, a button
// that should not be there, and a list that silently stops at the first page.
//
// The table itself is stubbed; see src/test/elTableStub.ts for why and for what
// that leaves uncovered.

function asset(overrides: Partial<Asset> = {}): Asset {
  return {
    id: "a1",
    tenantId: "t1",
    name: "官网首页",
    url: "https://example.com",
    type: "OFFICIAL",
    enabled: true,
    verificationStatus: "VERIFIED",
    createdAt: "2026-10-01T00:00:00Z",
    updatedAt: "2026-10-01T00:00:00Z",
    ...overrides,
  };
}

const baseProps = {
  assets: [] as Asset[],
  policies: [] as Policy[],
  canOperate: true,
  canAdmin: true,
  hasMore: false,
  loadingMore: false,
};

function render(props: Partial<typeof baseProps> = {}) {
  return mount(AssetsPage, { props: { ...baseProps, ...props }, global: { stubs: tableStubs } });
}

describe("AssetsPage", () => {
  it("says the list is empty rather than rendering a bare table", () => {
    const wrapper = render();

    expect(wrapper.text()).toContain("尚未登记监测资产");
    expect(wrapper.findAll(".el-table__row")).toHaveLength(0);
  });

  it("renders a row per asset with its verification state and type", () => {
    const wrapper = render({ assets: [asset({ name: "被篡改页", verificationStatus: "PENDING" })] });

    expect(wrapper.text()).toContain("被篡改页");
    expect(wrapper.text()).toContain("https://example.com");
    expect(wrapper.text()).toContain("PENDING");
    expect(wrapper.text()).toContain("OFFICIAL");
  });

  it("offers the mutating actions to an operator and only 只读 otherwise", () => {
    expect(render({ assets: [asset()] }).text()).toContain("HTTP 检测");

    const readOnly = render({ assets: [asset()], canOperate: false, canAdmin: false });
    expect(readOnly.text()).not.toContain("HTTP 检测");
    expect(readOnly.text()).not.toContain("批量导入");
    expect(readOnly.text()).toContain("只读");
  });

  it("hides the import and export entry points from a non-admin", () => {
    const wrapper = render({ canAdmin: false });

    expect(wrapper.text()).not.toContain("批量导入");
    expect(wrapper.text()).not.toContain("导出");
    expect(wrapper.text()).toContain("登记资产");
  });

  it("emits the row that was acted on, not the first row", async () => {
    const second = asset({ id: "a2", name: "第二行", verificationStatus: "PENDING" });
    const wrapper = render({ assets: [asset(), second] });

    const actions = wrapper.find('[data-column="操作"]');
    const verify = actions.find('[data-row-index="1"]').findAll("button").find((button) => button.text() === "授权验证");
    await verify?.trigger("click");

    const emitted = wrapper.emitted("verify");
    expect(emitted).toHaveLength(1);
    expect((emitted?.[0]?.[0] as Asset).id).toBe("a2");
  });

  it("refuses to detect on a disabled asset, which is what the switch is for", async () => {
    const wrapper = render({ assets: [asset({ enabled: false })] });

    const http = wrapper.find('[data-column="操作"]').findAll("button").find((button) => button.text() === "HTTP 检测");
    expect(http?.attributes("disabled")).toBeDefined();
    await http?.trigger("click");
    expect(wrapper.emitted("detect")).toBeUndefined();
  });

  it("keeps the load-more footer reachable and shows it loading", async () => {
    const idle = render({ hasMore: true });
    expect(idle.text()).toContain("加载更多");

    const loading = render({ hasMore: true, loadingMore: true });
    expect(loading.find(".list-footer button").classes()).toContain("is-loading");

    await idle.find(".list-footer button").trigger("click");
    expect(idle.emitted("loadMore")).toHaveLength(1);
  });

  it("hides the footer when the list is complete", () => {
    expect(render({ hasMore: false }).find(".list-footer").exists()).toBe(false);
  });
});
