<script setup lang="ts">
import ListFooter from "../components/monitoring/ListFooter.vue";
import type { Tenant } from "../types/domain";

defineProps<{ tenants: Tenant[]; hasMore: boolean; loadingMore: boolean }>();
const emit = defineEmits<{
  create: [];
  edit: [tenant: Tenant];
  loadMore: [];
}>();
function formatTime(value: string) { return new Date(value).toLocaleString("zh-CN", { hour12: false }); }
</script>

<template>
  <section class="view-content">
    <div class="page-toolbar">
      <div><span class="section-kicker">TENANTS</span><p class="toolbar-copy">创建租户时同时播种该租户首个管理员账号；租户数据严格隔离。</p></div>
      <el-button type="primary" @click="emit('create')">新增租户</el-button>
    </div>
    <section class="panel">
      <el-table :data="tenants" empty-text="暂无租户">
        <el-table-column prop="name" label="租户名称" min-width="180" />
        <el-table-column prop="slug" label="租户标识" min-width="180" />
        <el-table-column label="创建时间" width="180"><template #default="{ row }">{{ formatTime(row.createdAt) }}</template></el-table-column>
        <el-table-column label="操作" width="120" fixed="right"><template #default="{ row }"><el-button text @click="emit('edit', row)">编辑</el-button></template></el-table-column>
      </el-table>
      <ListFooter :has-more="hasMore" :loading="loadingMore" @load-more="emit('loadMore')" />
    </section>
  </section>
</template>
