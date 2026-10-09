<script setup lang="ts">
// For failures the app cannot attribute to a component -- a watcher, an async
// event handler, a lifecycle hook -- where there is no subtree to replace. The
// view keeps working, so this is a banner rather than the full-page fallback,
// and it is dismissible because a repeated failure (a poll that keeps 500ing)
// must not be able to cover the screen.
defineProps<{ message: string; detail: string }>();
const emit = defineEmits<{ dismiss: [] }>();
</script>

<template>
  <div class="error-banner" role="alert">
    <div class="error-banner-body">
      <strong>操作未完成</strong>
      <span class="error-banner-message">{{ message }}</span>
      <details v-if="detail" class="error-banner-detail">
        <summary>技术细节</summary>
        <pre>{{ detail }}</pre>
      </details>
    </div>
    <button type="button" class="error-banner-dismiss" aria-label="关闭错误提示" @click="emit('dismiss')">关闭</button>
  </div>
</template>
