<script setup lang="ts">
// The list API is keyset-paginated, so a list without this footer silently
// shows only the first page. Every paginated table renders it once.
defineProps<{ hasMore: boolean; loading: boolean }>();
const emit = defineEmits<{ loadMore: [] }>();
</script>

<template>
  <div v-if="hasMore" class="list-footer">
    <el-button link type="primary" :loading="loading" @click="emit('loadMore')">加载更多</el-button>
  </div>
</template>
