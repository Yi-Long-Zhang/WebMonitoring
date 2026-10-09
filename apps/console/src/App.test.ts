// The test components below are deliberately several to a file: each is a
// three-line stand-in for a routed view, and giving them one file each would
// scatter the assertions they exist for.
/* eslint-disable vue/one-component-per-file */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createRouter, createMemoryHistory, type Router } from "vue-router";
import { defineComponent, h } from "vue";
import App from "./App.vue";
import { appFailure } from "./app/errors";

// The error page is the one screen nobody tests by using the product, so it is
// the one that rots. What is asserted here is what the operator gets: the
// failure's own message (not a symptom of Vue tearing the broken component
// down), a retry that renders the view again once the cause has passed, and a
// banner for failures no component owns.

const BrokenView = defineComponent({
  name: "BrokenView",
  setup() {
    throw new Error("首页图表渲染失败");
  },
  // The render function reads a setup binding that the failed setup never
  // provided, which is what every page in this console does. It is not
  // decoration: when Vue re-renders around the broken component it runs this
  // again, and the second error used to replace the real cause on screen.
  render() {
    return h("div", String((this as unknown as { sessionStore: { loading: boolean } }).sessionStore.loading));
  },
});

const FineView = defineComponent({ name: "FineView", render: () => h("div", { class: "fine-view" }, "正常页面") });

function routerFor(component: unknown): Router {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", component: component as never },
      { path: "/login", component: FineView },
    ],
  });
  return router;
}

async function mountApp(router: Router) {
  // App.vue reads the session store, so pinia has to be installed the same way
  // main.ts installs it.
  const wrapper = mount(App, { global: { plugins: [createPinia(), router] } });
  await router.isReady();
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  setActivePinia(createPinia());
});

afterEach(() => {
  appFailure.value = undefined;
  vi.unstubAllGlobals();
});

describe("App error boundary", () => {
  it("replaces a view that throws with a fallback that names the real cause", async () => {
    // The session never resolves: this test is about the boundary, and a real
    // answer here would only add noise to the console.
    vi.stubGlobal("fetch", vi.fn(() => new Promise(() => {})));
    const wrapper = await mountApp(routerFor(BrokenView));

    expect(wrapper.find(".fatal-error").exists()).toBe(true);
    expect(wrapper.text()).toContain("这个页面没能渲染");
    expect(wrapper.text()).toContain("首页图表渲染失败");
  });

  it("renders the view again when retry is pressed, once the cause has passed", async () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise(() => {})));
    let shouldThrow = true;
    const FlakyView = defineComponent({
      name: "FlakyView",
      setup() {
        if (shouldThrow) throw new Error("一次性渲染失败");
      },
      render: () => h("div", { class: "recovered" }, "恢复后的页面"),
    });
    const wrapper = await mountApp(routerFor(FlakyView));
    expect(wrapper.find(".fatal-error").exists()).toBe(true);

    shouldThrow = false;
    await wrapper.find(".fatal-error-button.primary").trigger("click");
    await flushPromises();

    expect(wrapper.find(".fatal-error").exists()).toBe(false);
    expect(wrapper.find(".recovered").exists()).toBe(true);
  });

  it("shows a banner and keeps the view for a failure no component owns", async () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise(() => {})));
    const wrapper = await mountApp(routerFor(FineView));
    expect(wrapper.find(".fine-view").exists()).toBe(true);

    appFailure.value = { message: "后台刷新失败", detail: "未捕获的异步错误" };
    await flushPromises();

    expect(wrapper.find(".error-banner").exists()).toBe(true);
    expect(wrapper.text()).toContain("后台刷新失败");
    // The view is untouched: an unattributed failure is not a reason to take the
    // screen away from the operator.
    expect(wrapper.find(".fine-view").exists()).toBe(true);

    await wrapper.find(".error-banner-dismiss").trigger("click");
    expect(wrapper.find(".error-banner").exists()).toBe(false);
  });

  it("reports a session that could not be restored instead of leaving the login form silent", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ message: "网关不可达" }), { status: 502, headers: { "content-type": "application/json" } })));
    const router = routerFor(FineView);
    await router.push("/login");
    const wrapper = await mountApp(router);

    expect(wrapper.find(".error-banner").exists()).toBe(true);
    expect(wrapper.text()).toContain("网关不可达");
  });
});
