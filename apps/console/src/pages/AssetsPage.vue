<script setup lang="ts">
import ListFooter from "../components/monitoring/ListFooter.vue";
import type { Asset, AssetType, Policy } from "../types/domain";

defineProps<{ assets: Asset[]; policies: Policy[]; canOperate: boolean; canAdmin: boolean; hasMore: boolean; loadingMore: boolean }>();
const emit = defineEmits<{
  create: [input: { name: string; url: string; type: AssetType }];
  verify: [asset: Asset];
  toggle: [asset: Asset];
  detect: [asset: Asset, mode: "HTTP" | "BROWSER"];
  policy: [asset: Asset];
  loadMore: [];
  import: [];
  export: [];
}>();
function statusType(status: string) { return status === "VERIFIED" ? "success" : "warning"; }
</script>

<template>
  <section class="view-content">
    <div class="page-toolbar"><div><span class="section-kicker">REGISTERED TARGETS</span><p class="toolbar-copy">只监测已完成授权的页面，检测证据按租户隔离保存。</p></div><div class="toolbar-actions"><template v-if="canAdmin"><el-button @click="emit('import')">批量导入</el-button><el-button @click="emit('export')">导出</el-button></template><el-button v-if="canOperate" type="primary" @click="$emit('create', { name: '', url: '', type: 'OFFICIAL' })">登记资产</el-button></div></div>
    <section class="panel">
      <el-table :data="assets" empty-text="尚未登记监测资产">
        <el-table-column label="资产名称" min-width="180"><template #default="{ row }"><strong>{{ row.name }}</strong><small class="table-sub">{{ row.url }}</small></template></el-table-column>
        <el-table-column prop="type" label="类型" width="120" />
        <el-table-column label="授权" width="120"><template #default="{ row }"><el-tag :type="statusType(row.verificationStatus)" effect="plain">{{ row.verificationStatus }}</el-tag></template></el-table-column>
        <el-table-column label="监测" width="100"><template #default="{ row }"><el-switch :model-value="row.enabled" :disabled="!canOperate || row.verificationStatus !== 'VERIFIED'" @change="emit('toggle', row)" /></template></el-table-column>
        <el-table-column label="操作" width="300" fixed="right"><template #default="{ row }"><template v-if="canOperate"><el-button v-if="row.verificationStatus !== 'VERIFIED'" text type="warning" @click="emit('verify', row)">授权验证</el-button><el-button text :disabled="!row.enabled" @click="emit('detect', row, 'HTTP')">HTTP 检测</el-button><el-button text :disabled="!row.enabled" @click="emit('detect', row, 'BROWSER')">浏览器检测</el-button><el-button text @click="emit('policy', row)">策略</el-button></template><span v-else class="muted">只读</span></template></el-table-column>
      </el-table>
      <ListFooter :has-more="hasMore" :loading="loadingMore" @load-more="emit('loadMore')" />
    </section>
  </section>
</template>
