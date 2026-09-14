<script setup lang="ts">
import type { Site } from "../types/domain";

defineProps<{ sites: Site[]; canAdmin: boolean }>();
const emit = defineEmits<{
  create: [];
  edit: [site: Site];
}>();
function formatTime(value: string) { return new Date(value).toLocaleString("zh-CN", { hour12: false }); }
</script>

<template>
  <section class="view-content">
    <div class="page-toolbar">
      <div><span class="section-kicker">SITES</span><p class="toolbar-copy">站点是资产的组织维度，只做归类与列表过滤，不进入检测路径。</p></div>
      <el-button v-if="canAdmin" type="primary" @click="emit('create')">新增站点</el-button>
    </div>
    <section class="panel">
      <el-table :data="sites" empty-text="尚未创建站点">
        <el-table-column prop="name" label="站点名称" min-width="180" />
        <el-table-column prop="domain" label="域名" min-width="240" />
        <el-table-column label="创建时间" width="180"><template #default="{ row }">{{ formatTime(row.createdAt) }}</template></el-table-column>
        <el-table-column v-if="canAdmin" label="操作" width="120" fixed="right"><template #default="{ row }"><el-button text @click="emit('edit', row)">编辑</el-button></template></el-table-column>
      </el-table>
    </section>
  </section>
</template>
