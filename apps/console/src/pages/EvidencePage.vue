<script setup lang="ts">
import { computed, ref } from "vue";
import type { Asset, Evidence } from "../types/domain";
const props = defineProps<{ evidence: Evidence[]; assets: Asset[]; assetMap: Map<string, Asset> }>();
const selectedAsset = ref("");
const visibleEvidence = computed(() => selectedAsset.value ? props.evidence.filter((item) => item.assetId === selectedAsset.value) : props.evidence);
function formatTime(value: string) { return new Date(value).toLocaleString("zh-CN", { hour12: false }); }
</script>
<template><section class="view-content"><div class="page-toolbar"><div><span class="section-kicker">IMMUTABLE ARTIFACTS</span><p class="toolbar-copy">原始证据只追加、不覆盖，下载操作受会话和租户权限保护。</p></div><el-select v-model="selectedAsset" clearable placeholder="全部资产" class="asset-filter"><el-option v-for="asset in assets" :key="asset.id" :label="asset.name" :value="asset.id" /></el-select></div><section class="panel"><el-table :data="visibleEvidence" empty-text="暂无检测证据"><el-table-column label="资产" min-width="180"><template #default="{ row }">{{ assetMap.get(row.assetId)?.name || row.assetId }}</template></el-table-column><el-table-column prop="kind" label="证据类型" width="150" /><el-table-column prop="mediaType" label="媒体类型" min-width="170" /><el-table-column label="大小" width="110"><template #default="{ row }">{{ Math.ceil(row.size / 1024) }} KB</template></el-table-column><el-table-column label="采集时间" width="180"><template #default="{ row }">{{ formatTime(row.createdAt) }}</template></el-table-column><el-table-column label="操作" width="100"><template #default="{ row }"><a class="evidence-link no-margin" :href="`/api/v1/evidence/${row.id}/download`">下载</a></template></el-table-column></el-table></section></section></template>
