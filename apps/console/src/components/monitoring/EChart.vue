<script setup lang="ts">
// A thin wrapper around ECharts that owns the two things every chart in this
// console would otherwise have to get right on its own: disposal and resize.
//
// Disposal matters because the console switches views without remounting the
// app -- a chart left alive after its view is gone keeps a canvas, a resize
// listener and a timer or two, and the leak is invisible until an operator has
// been clicking around for an hour. Resize matters because the sidebar
// collapses and the window can be any width, and ECharts sizes to its
// container at init and then never again unless told.
//
// The option is passed whole rather than through per-chart props: this is a
// rendering boundary, not an abstraction over ECharts, and inventing a props
// vocabulary for every chart type would cost more than it saves.
import * as echarts from "echarts";
import { onBeforeUnmount, onMounted, useTemplateRef, watch } from "vue";

const props = defineProps<{ option: echarts.EChartsOption; height?: string }>();

// useTemplateRef infers the element type from the template, so the component
// never has to name a DOM type to hold its own node.
const host = useTemplateRef("host");
let chart: echarts.ECharts | undefined;
let observer: ResizeObserver | undefined;

onMounted(() => {
  if (!host.value) return;
  chart = echarts.init(host.value);
  chart.setOption(props.option);
  // ResizeObserver rather than a window listener: the container changes size
  // when the sidebar collapses without the window changing at all.
  observer = new ResizeObserver(() => chart?.resize());
  observer.observe(host.value);
});

// A changed option re-renders in place. `notMerge` is left off so a chart that
// receives a new series keeps its animation; `lazy` is on so a burst of
// updates coalesces into one frame instead of one draw each.
watch(
  () => props.option,
  (option) => chart?.setOption(option, { lazyUpdate: true }),
  { deep: true },
);

onBeforeUnmount(() => {
  observer?.disconnect();
  chart?.dispose();
  chart = undefined;
});
</script>

<template>
  <div ref="host" class="echart" :style="{ height: height ?? '260px' }" />
</template>
