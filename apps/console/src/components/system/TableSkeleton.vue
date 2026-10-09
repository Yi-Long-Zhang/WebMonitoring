<script setup lang="ts">
// Shown while a list's first load is in flight, in place of the table.
//
// Not cosmetic: a table whose data has not arrived renders its empty-text, so
// "尚未登记监测资产" is on screen while the assets are still being fetched. An
// operator reading that acts on it -- the two states are identical on screen and
// mean opposite things. The skeleton says which one this is.
//
// role="status" rather than aria-hidden: a screen reader user gets no rows
// either way, and "the list is loading" is the one thing there is to say.
defineProps<{ rows?: number }>();
</script>

<template>
  <div class="table-skeleton" role="status" aria-label="列表加载中">
    <el-skeleton :rows="rows ?? 4" animated />
  </div>
</template>
