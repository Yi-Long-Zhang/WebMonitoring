<script setup lang="ts">
import ListFooter from "../components/monitoring/ListFooter.vue";
import type { Asset, DetectionJob } from "../types/domain";
defineProps<{ jobs: DetectionJob[]; assetMap: Map<string, Asset>; canOperate: boolean; hasMore: boolean; loadingMore: boolean }>();
const emit = defineEmits<{ loadMore: []; cancel: [job: DetectionJob] }>();
// Only a job the queue still owns can be cancelled; the server rejects a
// terminal one with 409 INVALID_JOB_STATE.
function cancellable(job: DetectionJob) { return ["QUEUED", "RUNNING"].includes(job.status); }
function formatTime(value: string) { return new Date(value).toLocaleString("zh-CN", { hour12: false }); }
</script>
<template><section class="view-content"><div class="page-toolbar"><div><span class="section-kicker">DETECTION PIPELINE</span><p class="toolbar-copy">HTTP 高频巡检，浏览器检测按策略降频或由异常触发。</p></div></div><section class="panel"><el-table :data="jobs" empty-text="暂无检测任务"><el-table-column label="资产" min-width="260"><template #default="{ row }"><strong>{{ assetMap.get(row.assetId)?.name || row.assetId }}</strong><small class="table-sub">{{ assetMap.get(row.assetId)?.url }}</small></template></el-table-column><el-table-column prop="mode" label="模式" width="110" /><el-table-column prop="status" label="状态" width="140" /><el-table-column label="创建时间" width="180"><template #default="{ row }">{{ formatTime(row.createdAt) }}</template></el-table-column><el-table-column label="失败原因" min-width="220"><template #default="{ row }"><span class="muted">{{ row.errorMessage || "-" }}</span></template></el-table-column><el-table-column label="操作" width="110" fixed="right"><template #default="{ row }"><el-button v-if="canOperate && cancellable(row)" type="danger" link @click="emit('cancel', row)">取消</el-button><span v-else class="muted">{{ canOperate ? "已结束" : "只读" }}</span></template></el-table-column></el-table><ListFooter :has-more="hasMore" :loading="loadingMore" @load-more="emit('loadMore')" /></section></section></template>
