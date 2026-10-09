<script setup lang="ts">
import { computed, onErrorCaptured, onMounted, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import ErrorBanner from "./components/system/ErrorBanner.vue";
import FatalErrorPage from "./components/system/FatalErrorPage.vue";
import { appFailure, messageOf, type AppFailure } from "./app/errors";
import { useSessionStore } from "./stores/session";

const router = useRouter();
const route = useRoute();
const session = useSessionStore();

// A render-time error inside the routed view lands here. Returning false stops
// it at this boundary: app.config.errorHandler would otherwise also record it,
// and the same failure would show up twice.
const viewFailure = ref<AppFailure | undefined>();
onErrorCaptured((error, _instance, info) => {
  // First error wins. A setup failure leaves the broken component in the tree
  // while Vue re-renders around it, and that re-render raises a second,
  // internal error ("Cannot read properties of undefined") which would
  // otherwise replace the real cause with a symptom of the teardown.
  if (!viewFailure.value) viewFailure.value = { message: messageOf(error), detail: `渲染失败：${info}` };
  return false;
});

// One banner for both unattributed failures and a session that could not be
// restored. They are different facts with different homes (the module ref and
// the store), so this is the only place that has to know about both.
const banner = computed<AppFailure | undefined>(() => {
  if (appFailure.value) return appFailure.value;
  if (session.restoreFailure) {
    return { message: session.restoreFailure, detail: "会话恢复失败：无法确认当前登录状态。" };
  }
  return undefined;
});
function dismissBanner() {
  appFailure.value = undefined;
  session.restoreFailure = undefined;
}

onMounted(async () => {
  // restore() returns false for "no session" as well as for "the API could not
  // be reached"; the store records the second case so it reaches the operator
  // instead of leaving them on a login form that rejects a correct password.
  //
  // The redirect is only away from the login page. Replacing the location with
  // "/" unconditionally (as this did) cancelled every deep link on a full page
  // load -- /#/incidents landed on the overview -- which is the contract the
  // router comment and the console smoke's deep-link walk both state, and it
  // downloaded the overview's ECharts chunk for a view that was never shown.
  // A segment the console does not know is still normalized away, by
  // ConsoleLayout's syncFromRoute.
  if ((await session.restore()) && route.path === "/login") await router.replace("/");
});
</script>

<template>
  <ErrorBanner v-if="banner" :message="banner.message" :detail="banner.detail" @dismiss="dismissBanner" />
  <FatalErrorPage v-if="viewFailure" :message="viewFailure.message" :detail="viewFailure.detail" @retry="viewFailure = undefined" />
  <router-view v-else />
</template>
