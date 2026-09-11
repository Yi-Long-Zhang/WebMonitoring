<script setup lang="ts">
import { ref } from "vue";
import { ElMessage } from "element-plus";
import { useRouter } from "vue-router";
import { useSessionStore } from "../stores/session";

const router = useRouter();
const sessionStore = useSessionStore();
const form = ref({ username: "admin", password: "" });

async function submit() {
  try {
    await sessionStore.signIn(form.value.username, form.value.password);
    await router.replace("/");
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : "登录失败");
  }
}
</script>

<template>
  <div class="login-page">
    <div class="login-panel">
      <div class="brand-mark">WM</div>
      <p class="eyebrow">SECURITY OPERATIONS</p>
      <h1>网页完整性监测台</h1>
      <p class="login-copy">监测页面变化，保留证据，缩短处置时间。</p>
      <el-form @submit.prevent="submit">
        <el-form-item label="账号"><el-input v-model="form.username" autocomplete="username" /></el-form-item>
        <el-form-item label="密码"><el-input v-model="form.password" type="password" show-password autocomplete="current-password" /></el-form-item>
        <el-button class="login-button" type="primary" native-type="submit" :loading="sessionStore.loading">登录控制台</el-button>
      </el-form>
    </div>
  </div>
</template>
