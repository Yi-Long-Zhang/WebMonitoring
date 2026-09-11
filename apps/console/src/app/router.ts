import { createRouter, createWebHashHistory } from "vue-router";
import LoginPage from "../pages/LoginPage.vue";
import ConsolePage from "../pages/ConsolePage.vue";

export const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: "/login", component: LoginPage, meta: { public: true } },
    { path: "/:pathMatch(.*)*", component: ConsolePage },
  ],
});
