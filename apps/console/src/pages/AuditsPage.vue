<script setup lang="ts">
import type { AuditEvent } from "../types/domain";
defineProps<{ audits: AuditEvent[] }>();
function formatTime(value: string) { return new Date(value).toLocaleString("zh-CN", { hour12: false }); }
</script>
<template><section class="view-content"><div class="page-toolbar"><div><span class="section-kicker">AUDIT TRAIL</span><p class="toolbar-copy">关键操作关联操作者、资源、结果和追踪 ID。</p></div></div><section class="panel"><el-table :data="audits" empty-text="暂无审计记录"><el-table-column prop="action" label="操作" min-width="190" /><el-table-column prop="resourceType" label="资源类型" width="140" /><el-table-column prop="resourceId" label="资源 ID" min-width="220" /><el-table-column prop="result" label="结果" width="110" /><el-table-column prop="traceId" label="追踪 ID" min-width="220" /><el-table-column label="时间" width="180"><template #default="{ row }">{{ formatTime(row.createdAt) }}</template></el-table-column></el-table></section></section></template>
