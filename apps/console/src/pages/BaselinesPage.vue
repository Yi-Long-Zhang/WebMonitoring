<script setup lang="ts">
import type { Asset, Baseline } from "../types/domain";

defineProps<{ baselines: Baseline[]; assetMap: Map<string, Asset>; canOperate: boolean }>();
const emit = defineEmits<{ review: [id: string, accepted: boolean]; retire: [id: string] }>();
function formatTime(value: string) { return new Date(value).toLocaleString("zh-CN", { hour12: false }); }
</script>

<template>
  <section class="view-content">
    <div class="page-toolbar"><div><span class="section-kicker">BASELINE REVIEW</span><p class="toolbar-copy">候选基线必须人工确认，批准后才参与篡改判断；旧版本只允许显式废弃。</p></div></div>
    <section class="panel">
      <el-table :data="baselines" empty-text="暂无候选基线">
        <el-table-column label="资产" min-width="220"><template #default="{ row }"><strong>{{ assetMap.get(row.assetId)?.name || row.assetId }}</strong><small class="table-sub">{{ assetMap.get(row.assetId)?.url }}</small></template></el-table-column>
        <el-table-column prop="version" label="版本" width="90" /><el-table-column prop="status" label="状态" width="130" /><el-table-column label="创建时间" width="180"><template #default="{ row }">{{ formatTime(row.createdAt) }}</template></el-table-column>
        <el-table-column label="操作" width="220"><template #default="{ row }"><template v-if="canOperate && row.status === 'CANDIDATE'"><el-button type="success" link @click="emit('review', row.id, true)">批准</el-button><el-button type="danger" link @click="emit('review', row.id, false)">拒绝</el-button></template><el-button v-if="canOperate && row.status === 'APPROVED'" type="warning" link @click="emit('retire', row.id)">废弃</el-button><span v-if="!canOperate || !['CANDIDATE', 'APPROVED'].includes(row.status)" class="muted">{{ canOperate ? "已处理" : "只读" }}</span></template></el-table-column>
      </el-table>
    </section>
  </section>
</template>