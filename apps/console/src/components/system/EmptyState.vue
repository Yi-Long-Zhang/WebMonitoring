<script setup lang="ts">
// One empty state for the whole console. Element Plus' el-empty takes an image
// size and a description per call site, and the eight places that used it
// directly had drifted to three sizes and two illustration styles for the same
// idea. This fixes the size in one place so a new screen cannot invent its own.
//
// Tables keep Element Plus' `empty-text` prop instead: it is the table's own
// API, it renders inside the table's layout, and routing it through a component
// would put the message outside the surface it belongs to.
withDefaults(defineProps<{ description: string; size?: number }>(), { size: 54 });
</script>

<template>
  <el-empty :description="description" :image-size="size" />
</template>
