import { createRouter, createWebHashHistory } from "vue-router";

export const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    // Both pages are loaded on demand. They used to be static imports, which
    // made the whole console part of the login page's download: ConsolePage
    // imports ConsoleLayout, and that imports all twelve views.
    { path: "/login", component: () => import("../pages/LoginPage.vue"), meta: { public: true } },
    // The visible view is part of the hash so deep links and the back button
    // work. This is not a nested route -- ConsolePage still switches views
    // itself, and validates the segment against VIEWS. The catch-all below is
    // kept so a stale or hand-typed multi-segment hash still renders the shell
    // instead of a blank page.
    { path: "/:view?", component: () => import("../pages/ConsolePage.vue") },
    { path: "/:pathMatch(.*)*", component: () => import("../pages/ConsolePage.vue") },
  ],
});
