import { createRouter, createWebHashHistory } from "vue-router";
import LoginPage from "../pages/LoginPage.vue";
import ConsolePage from "../pages/ConsolePage.vue";

export const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    // /login stays first: routes are matched in order, and /:view? would
    // otherwise swallow it as the view named "login".
    { path: "/login", component: LoginPage, meta: { public: true } },
    // The visible view is part of the hash so deep links and the back button
    // work. This is not a nested route -- ConsolePage still switches views
    // itself, and validates the segment against VIEWS. The catch-all below is
    // kept so a stale or hand-typed multi-segment hash still renders the shell
    // instead of a blank page.
    { path: "/:view?", component: ConsolePage },
    { path: "/:pathMatch(.*)*", component: ConsolePage },
  ],
});
