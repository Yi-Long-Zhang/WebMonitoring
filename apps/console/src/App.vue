<script setup lang="ts">
import { onMounted } from "vue";
import { useRouter } from "vue-router";
import { useSessionStore } from "./stores/session";

const router = useRouter();
const session = useSessionStore();

onMounted(async () => {
  if (await session.restore()) await router.replace("/");
});
</script>

<template><router-view /></template>
