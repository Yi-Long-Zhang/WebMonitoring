import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import OverviewPage from "./OverviewPage.vue";

// The overview is the first screen after signing in, and every number on it
// falls back to zero and every chart to its empty state while the data is still
// in flight. Without the firstLoad distinction the page opens by reporting "0
// 待处置事件" and "该区间内没有事件" -- a verdict on a gap. ECharts is left out
// of these cases through a stub: ECharts needs a sized canvas, which jsdom does
// not provide, and what is asserted here is which branch renders -- not what the
// charts draw, which the console smoke checks against a real browser.

const baseProps = {
  assets: [],
  incidents: [],
  assetMap: new Map(),
};

describe("OverviewPage", () => {
  it("shows skeletons instead of zeros and empty charts during the first load", () => {
    const wrapper = mount(OverviewPage, { props: { ...baseProps, firstLoad: true }, global: { stubs: { EChart: true } } });

    expect(wrapper.findAll(".el-skeleton").length).toBeGreaterThan(0);
    expect(wrapper.text()).not.toContain("该区间内没有事件");
    expect(wrapper.text()).not.toContain("尚未登记资产");
  });

  it("shows the empty states once the load has finished and there is nothing", () => {
    const wrapper = mount(OverviewPage, { props: { ...baseProps, firstLoad: false }, global: { stubs: { EChart: true } } });

    expect(wrapper.find(".el-skeleton").exists()).toBe(false);
    expect(wrapper.text()).toContain("该区间内没有事件");
    expect(wrapper.text()).toContain("尚未登记资产");
  });
});
