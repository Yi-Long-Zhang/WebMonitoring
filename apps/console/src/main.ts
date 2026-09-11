import { createApp } from "vue";
import { createPinia } from "pinia";
import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import ElementPlus from "element-plus";
import { router } from "./app/router";
import App from "./App.vue";
import "element-plus/dist/index.css";
import "./styles/index.css";

const app = createApp(App);
app.use(createPinia());
app.use(router);
app.use(ElementPlus);
app.use(VueQueryPlugin, { queryClient: new QueryClient() });
app.mount("#app");
