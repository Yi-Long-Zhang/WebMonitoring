import { createApp } from "vue";
import { createPinia } from "pinia";
import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { router } from "./app/router";
import { appFailure, messageOf } from "./app/errors";
import App from "./App.vue";
// Element Plus components used in templates are imported on demand by
// unplugin-vue-components (see vite.config.ts). ElMessage and ElMessageBox are
// called from script, so the resolver never sees them -- their two stylesheets
// are imported here by hand. Dropping the full `element-plus/dist/index.css`
// without this leaves toasts and confirm dialogs unstyled, which looks like a
// rendering bug rather than a missing import.
import "element-plus/theme-chalk/el-message.css";
import "element-plus/theme-chalk/el-message-box.css";
import "./styles/index.css";

const app = createApp(App);
app.use(createPinia());
app.use(router);
app.use(VueQueryPlugin, { queryClient: new QueryClient() });

// The catch-all for errors Vue cannot hand to App.vue's boundary: watchers,
// async event handlers and lifecycle hooks have no subtree to replace. The
// view keeps working, so this only records -- App.vue renders the banner.
// Without it these failures were logged by Vue and nowhere else, which on a
// console someone leaves open all day means nobody ever sees them.
app.config.errorHandler = (error, _instance, info) => {
  appFailure.value = { message: messageOf(error), detail: `未捕获的异步错误：${info}` };
  // Kept as well: the browser console is where a developer looks first, and
  // the banner only carries the message.
  console.error("[console] 未捕获的异步错误", info, error);
};

app.mount("#app");
