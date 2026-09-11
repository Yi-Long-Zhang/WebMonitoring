<script setup lang="ts">
import { Refresh } from "@element-plus/icons-vue";

defineProps<{ title: string; loading: boolean }>();
const emit = defineEmits<{ refresh: [] }>();
</script>

<template>
  <header class="topbar">
    <div><p class="eyebrow">WEBSITE INTEGRITY / PRIVATE DEPLOYMENT</p><h1>{{ title }}</h1></div>
    <div class="topbar-actions">
      <span class="last-sync">最近同步 {{ new Date().toLocaleString("zh-CN", { hour12: false }) }}</span>
      <el-button circle title="刷新数据" aria-label="刷新数据" :icon="Refresh" :loading="loading" @click="emit('refresh')" />
    </div>
  </header>
</template>
