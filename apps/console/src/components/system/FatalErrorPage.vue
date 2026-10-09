<script setup lang="ts">
// The last thing between a broken view and a white page. Deliberately built
// from plain markup and the stylesheet's own tokens rather than Element Plus:
// the failure it exists to cover includes "the chunk that holds the component
// library never arrived", and a fallback that needs that chunk is no fallback.
const props = defineProps<{ message: string; detail: string }>();
const emit = defineEmits<{ retry: [] }>();

// A full reload is the honest second option: retry re-renders the same route
// and, if the failure was a stale chunk reference from a redeploy, that will
// fail the same way until the page is fetched again.
function reload() {
  window.location.reload();
}
</script>

<template>
  <div class="fatal-error" role="alert">
    <div class="fatal-error-panel">
      <p class="section-kicker">CONSOLE ERROR</p>
      <h1>这个页面没能渲染</h1>
      <p class="fatal-error-copy">{{ props.message }}</p>
      <p class="fatal-error-hint">页面其余部分已停止渲染，以免在你不知情的情况下继续操作数据。</p>
      <div class="fatal-error-actions">
        <button type="button" class="fatal-error-button primary" @click="emit('retry')">重试渲染</button>
        <button type="button" class="fatal-error-button" @click="reload">重新加载页面</button>
      </div>
      <details class="fatal-error-detail">
        <summary>技术细节（可复制给维护者）</summary>
        <pre>{{ props.detail }}</pre>
      </details>
    </div>
  </div>
</template>
