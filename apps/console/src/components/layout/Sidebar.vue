<script setup lang="ts">
import {
  Aim,
  Bell,
  Checked,
  Collection,
  Grid,
  House,
  List,
  Monitor,
  OfficeBuilding,
  SwitchButton,
  WarningFilled,
} from "@element-plus/icons-vue";
import { canAdmin, canOperate, canSuperAdmin } from "../../app/permissions";
import type { Role } from "../../types/domain";
import type { View } from "../../types/views";

defineProps<{
  activeView: View;
  username: string;
  role: Role;
  roleLabel: string;
  candidateCount: number;
  openIncidentCount: number;
}>();

const emit = defineEmits<{
  navigate: [view: View];
  signOut: [];
}>();
</script>

<template>
  <aside class="sidebar">
    <div class="brand">
      <div class="brand-mark small">WM</div>
      <div><strong>WebMonitoring</strong><span>Integrity Control</span></div>
    </div>
    <div class="sidebar-caption">运营工作台</div>
    <nav class="nav-list">
      <button :class="{ active: activeView === 'overview' }" @click="emit('navigate', 'overview')"><el-icon class="nav-icon"><House /></el-icon> 总览</button>
      <button :class="{ active: activeView === 'assets' }" @click="emit('navigate', 'assets')"><el-icon class="nav-icon"><Monitor /></el-icon> 资产中心</button>
      <button v-if="canAdmin(role)" :class="{ active: activeView === 'sites' }" @click="emit('navigate', 'sites')"><el-icon class="nav-icon"><Grid /></el-icon> 站点管理</button>
      <button v-if="canOperate(role)" :class="{ active: activeView === 'baselines' }" @click="emit('navigate', 'baselines')"><el-icon class="nav-icon"><Checked /></el-icon> 基线审批<span v-if="candidateCount" class="nav-count">{{ candidateCount }}</span></button>
      <!-- 事件中心 stays ungated on purpose: GET /api/v1/incidents is readable by
           every authenticated role, and an auditor who cannot reach the incident
           list cannot audit anything. 总览 and 资产中心 are readable for the same
           reason. 审计日志 has no server-side role gate either, so gating it here
           is a UI reduction only -- see the note in app/permissions.ts. -->
      <button :class="{ active: activeView === 'incidents' }" @click="emit('navigate', 'incidents')"><el-icon class="nav-icon"><WarningFilled /></el-icon> 事件中心<span v-if="openIncidentCount" class="nav-count alert">{{ openIncidentCount }}</span></button>
      <button v-if="canOperate(role)" :class="{ active: activeView === 'jobs' }" @click="emit('navigate', 'jobs')"><el-icon class="nav-icon"><Aim /></el-icon> 检测任务</button>
      <button v-if="canOperate(role)" :class="{ active: activeView === 'evidence' }" @click="emit('navigate', 'evidence')"><el-icon class="nav-icon"><Collection /></el-icon> 证据中心</button>
      <button v-if="canOperate(role)" :class="{ active: activeView === 'channels' }" @click="emit('navigate', 'channels')"><el-icon class="nav-icon"><Bell /></el-icon> 告警渠道</button>
      <button v-if="canOperate(role)" :class="{ active: activeView === 'audits' }" @click="emit('navigate', 'audits')"><el-icon class="nav-icon"><List /></el-icon> 审计日志</button>
      <button v-if="canSuperAdmin(role)" :class="{ active: activeView === 'tenants' }" @click="emit('navigate', 'tenants')"><el-icon class="nav-icon"><OfficeBuilding /></el-icon> 租户管理</button>
    </nav>
    <div class="sidebar-footer">
      <div class="status-dot"><span /> 检测服务运行中</div>
      <div class="user-chip">
        <span class="avatar">{{ username.slice(0, 1).toUpperCase() }}</span>
        <span><strong>{{ username }}</strong><small>{{ roleLabel }}</small></span>
        <button title="退出登录" aria-label="退出登录" @click="emit('signOut')"><el-icon><SwitchButton /></el-icon></button>
      </div>
    </div>
  </aside>
</template>
