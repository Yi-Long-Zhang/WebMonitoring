<script setup lang="ts">
import type { User } from "../types/domain";
import TableSkeleton from "../components/system/TableSkeleton.vue";
// Read-only in the sense that it does no IO: every write goes back out through
// the layout, which owns the actions and the reload. That is what keeps the
// store the only thing that touches the API.
const props = defineProps<{ users: User[]; canAdmin: boolean; selfUserId: string; firstLoad?: boolean }>();
const emit = defineEmits<{
  create: [];
  edit: [user: User];
  resetPassword: [user: User];
}>();

const ROLE_LABELS: Record<User["role"], string> = {
  ADMIN: "平台管理员",
  OPERATOR: "安全运营",
  AUDITOR: "审计只读",
  SUPER_ADMIN: "超级管理员",
};

// The server protects the bootstrap SUPER_ADMIN (403 SUPER_ADMIN_PROTECTED) and
// refuses to let an admin disable or demote themselves (409 SELF_DEMOTION).
// Disabling the buttons spares the operator a round trip; the guards that matter
// are the server's, and a rejection still reaches the operator if this ever
// disagrees with it.
function isSuperAdmin(user: User) { return user.role === "SUPER_ADMIN"; }
function isSelf(user: User) { return user.id === props.selfUserId; }
function isLocked(user: User) { return isSuperAdmin(user) || isSelf(user); }
function formatTime(value: string) { return new Date(value).toLocaleString("zh-CN", { hour12: false }); }
</script>

<template>
  <section class="view-content">
    <div class="page-toolbar">
      <div><span class="section-kicker">USER DIRECTORY</span><p class="toolbar-copy">租户内可创建安全运营与审计只读账号；平台超级管理员由租户初始化时播种。</p></div>
      <el-button v-if="canAdmin" type="primary" @click="emit('create')">新增用户</el-button>
    </div>
    <section class="panel">
      <TableSkeleton v-if="firstLoad && !users.length" />
      <el-table v-else :data="users" empty-text="暂无用户">
        <el-table-column label="用户名" min-width="180"><template #default="{ row }"><strong>{{ row.username }}</strong><small v-if="isSelf(row)" class="table-sub">当前账号</small></template></el-table-column>
        <el-table-column label="角色" width="140"><template #default="{ row }: { row: User }">{{ ROLE_LABELS[row.role] || row.role }}</template></el-table-column>
        <el-table-column label="状态" width="120"><template #default="{ row }: { row: User }"><el-tag :type="row.disabled ? 'info' : 'success'" effect="plain">{{ row.disabled ? "已停用" : "已启用" }}</el-tag></template></el-table-column>
        <el-table-column label="创建时间" width="180"><template #default="{ row }: { row: User }">{{ formatTime(row.createdAt) }}</template></el-table-column>
        <el-table-column label="操作" width="200" fixed="right">
          <template #default="{ row }: { row: User }">
            <template v-if="canAdmin && !isLocked(row)">
              <el-button text @click="emit('edit', row)">编辑</el-button>
              <el-button text @click="emit('resetPassword', row)">重置密码</el-button>
            </template>
            <span v-else-if="isSelf(row)" class="muted">当前账号</span>
            <span v-else class="muted">{{ isSuperAdmin(row) ? "平台账号" : "只读" }}</span>
          </template>
        </el-table-column>
      </el-table>
    </section>
  </section>
</template>

<style scoped>
.table-sub { display: block; color: var(--wm-muted); font-size: 12px; }
.muted { color: var(--wm-muted); }
</style>
