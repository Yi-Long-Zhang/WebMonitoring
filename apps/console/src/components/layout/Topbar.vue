<script setup lang="ts">
import { ref } from "vue";
import { ElMessage } from "element-plus";
import { Key, Refresh } from "@element-plus/icons-vue";

defineProps<{ title: string; loading: boolean; username: string; roleLabel: string }>();
const emit = defineEmits<{
  refresh: [];
  changePassword: [currentPassword: string, newPassword: string];
}>();

const showPasswordDialog = ref(false);
const currentPassword = ref("");
const newPassword = ref("");
const confirmPassword = ref("");
const busy = ref(false);

function openPasswordDialog() {
  currentPassword.value = "";
  newPassword.value = "";
  confirmPassword.value = "";
  showPasswordDialog.value = true;
}

function closePasswordDialog() {
  // Dropped with the dialog rather than left in the refs after the write, so a
  // plaintext password does not outlive the request in memory.
  currentPassword.value = "";
  newPassword.value = "";
  confirmPassword.value = "";
  showPasswordDialog.value = false;
}

async function submit() {
  if (newPassword.value !== confirmPassword.value) {
    ElMessage.error("两次输入的新密码不一致");
    return;
  }
  busy.value = true;
  try {
    await emit("changePassword", currentPassword.value, newPassword.value);
    closePasswordDialog();
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <header class="topbar">
    <div><p class="eyebrow">WEBSITE INTEGRITY / PRIVATE DEPLOYMENT</p><h1>{{ title }}</h1></div>
    <div class="topbar-actions">
      <span class="last-sync">最近同步 {{ new Date().toLocaleString("zh-CN", { hour12: false }) }}</span>
      <el-button circle title="刷新数据" aria-label="刷新数据" :icon="Refresh" :loading="loading" @click="emit('refresh')" />
      <el-dropdown trigger="click">
        <el-button text class="account-menu">
          <span class="avatar">{{ username.slice(0, 1).toUpperCase() }}</span>
          {{ username }}
        </el-button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item disabled>{{ roleLabel }}</el-dropdown-item>
            <!-- Self-service is the only channel that can change the seeded
                 SUPER_ADMIN's password: the admin reset endpoint rejects that
                 account on purpose. So this entry is ungated by role. -->
            <el-dropdown-item :icon="Key" @click="openPasswordDialog">修改密码</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </div>

    <el-dialog v-model="showPasswordDialog" title="修改密码" width="420px" @closed="closePasswordDialog">
      <el-form label-position="top">
        <el-form-item label="当前密码"><el-input v-model="currentPassword" type="password" show-password autocomplete="current-password" /></el-form-item>
        <el-form-item label="新密码"><el-input v-model="newPassword" type="password" show-password autocomplete="new-password" placeholder="至少 12 位" /></el-form-item>
        <el-form-item label="确认新密码"><el-input v-model="confirmPassword" type="password" show-password autocomplete="new-password" @keyup.enter="submit" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="closePasswordDialog">取消</el-button>
        <el-button type="primary" :loading="busy" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </header>
</template>
