<script setup lang="ts">
import ListFooter from "../components/monitoring/ListFooter.vue";
import TableSkeleton from "../components/system/TableSkeleton.vue";
import type { Tenant } from "../types/domain";

const props = defineProps<{ tenants: Tenant[]; selfTenantId: string; defaultTenantId: string; canDelete: boolean; hasMore: boolean; loadingMore: boolean; firstLoad?: boolean }>();
const emit = defineEmits<{
  create: [];
  edit: [tenant: Tenant];
  toggle: [tenant: Tenant, disabled: boolean];
  remove: [tenant: Tenant];
  loadMore: [];
}>();

// The server refuses to disable the default tenant and the caller's own with
// 409 TENANT_PROTECTED. Disabling the switch only spares the operator the round
// trip; the guard that matters is the server's, and a 409 still reaches the
// operator if this ever disagrees with it.
function isSelf(tenant: Tenant) { return tenant.id === props.selfTenantId; }

// deleteTenant refuses the caller's own tenant (409 TENANT_SELF_DELETE) and the
// default one (409 TENANT_PROTECTED). Same reasoning as isSelf above: this only
// keeps the button from inviting a form the server will reject.
function isProtected(tenant: Tenant) { return isSelf(tenant) || tenant.id === props.defaultTenantId; }
function formatTime(value: string) { return new Date(value).toLocaleString("zh-CN", { hour12: false }); }
</script>

<template>
  <section class="view-content">
    <div class="page-toolbar">
      <div><span class="section-kicker">TENANTS</span><p class="toolbar-copy">创建租户时同时播种该租户首个管理员账号；租户数据严格隔离。</p></div>
      <el-button type="primary" @click="emit('create')">新增租户</el-button>
    </div>
    <section class="panel">
      <TableSkeleton v-if="firstLoad && !tenants.length" />
      <el-table v-else :data="tenants" empty-text="暂无租户">
        <el-table-column prop="name" label="租户名称" min-width="180" />
        <el-table-column prop="slug" label="租户标识" min-width="180" />
        <el-table-column label="状态" width="150"><template #default="{ row }"><el-switch :model-value="!row.disabled" :disabled="isSelf(row)" @change="emit('toggle', row, !row.disabled)" /><small v-if="isSelf(row)" class="table-sub">当前租户</small></template></el-table-column>
        <el-table-column label="创建时间" width="180"><template #default="{ row }">{{ formatTime(row.createdAt) }}</template></el-table-column>
        <el-table-column label="操作" width="170" fixed="right">
          <template #default="{ row }: { row: Tenant }">
            <el-button text @click="emit('edit', row)">编辑</el-button>
            <el-button v-if="canDelete" text type="danger" :disabled="isProtected(row)" @click="emit('remove', row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <ListFooter :has-more="hasMore" :loading="loadingMore" @load-more="emit('loadMore')" />
    </section>
  </section>
</template>
