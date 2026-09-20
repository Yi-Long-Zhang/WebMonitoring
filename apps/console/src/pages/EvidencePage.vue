<script setup lang="ts">
import { computed, onUnmounted, ref } from "vue";
import { ElMessage } from "element-plus";
import ListFooter from "../components/monitoring/ListFooter.vue";
import { issueEvidenceSignedUrl } from "../services/api/client";
import { formatRemaining, secondsRemaining } from "../features/monitoring/signedUrl";
import type { Asset, Evidence } from "../types/domain";

const props = defineProps<{ evidence: Evidence[]; assets: Asset[]; assetMap: Map<string, Asset>; canOperate: boolean; hasMore: boolean; loadingMore: boolean }>();
const emit = defineEmits<{ loadMore: [] }>();
const selectedAsset = ref("");
const visibleEvidence = computed(() => selectedAsset.value ? props.evidence.filter((item) => item.assetId === selectedAsset.value) : props.evidence);
function formatTime(value: string) { return new Date(value).toLocaleString("zh-CN", { hour12: false }); }

// One issued link at a time, keyed to the evidence row it belongs to. A signed
// URL is a bearer credential, so it is held in memory only -- never persisted,
// never logged -- and it is dropped when the operator closes the panel or the
// row it belongs to leaves the list.
const issuedFor = ref("");
const issuedUrl = ref("");
const issuedExpiresAt = ref("");
const issuing = ref(false);
const now = ref(Date.now());
let ticker: ReturnType<typeof setInterval> | undefined;

function stopTicker() {
  if (ticker) { clearInterval(ticker); ticker = undefined; }
}
onUnmounted(stopTicker);

function clearIssued() {
  issuedFor.value = "";
  issuedUrl.value = "";
  issuedExpiresAt.value = "";
  stopTicker();
}

async function issue(item: Evidence) {
  issuing.value = true;
  try {
    const answer = await issueEvidenceSignedUrl(item.id);
    issuedFor.value = item.id;
    issuedUrl.value = answer.url;
    issuedExpiresAt.value = answer.expiresAt;
    now.value = Date.now();
    stopTicker();
    ticker = setInterval(() => {
      now.value = Date.now();
      if (secondsRemaining(issuedExpiresAt.value, now.value) <= 0) { ElMessage.warning("短时链接已过期"); clearIssued(); }
    }, 1000);
  } catch (error) {
    // Not fatal: the authenticated download link below still works, so the
    // failure is reported and the page stays usable. The usual cause is that
    // signing is unconfigured on this deployment (SIGNED_URL_FAILED).
    ElMessage.error(error instanceof Error ? error.message : "无法签发短时下载链接");
  } finally {
    issuing.value = false;
  }
}

// navigator.clipboard is unavailable on an insecure origin, which is exactly how
// the default Compose stack is served, so this is allowed to fail quietly and
// the URL is shown in full for the operator to copy by hand.
async function copyIssued() {
  try {
    await navigator.clipboard.writeText(new URL(issuedUrl.value, window.location.origin).href);
    ElMessage.success("链接已复制");
  } catch {
    ElMessage.info("无法自动复制，请手动选择链接");
  }
}

const remaining = computed(() => formatRemaining(secondsRemaining(issuedExpiresAt.value, now.value)));
</script>

<template>
  <section class="view-content">
    <div class="page-toolbar">
      <div><span class="section-kicker">IMMUTABLE ARTIFACTS</span><p class="toolbar-copy">原始证据只追加、不覆盖，下载操作受会话和租户权限保护。</p></div>
      <el-select v-model="selectedAsset" clearable placeholder="全部资产" class="asset-filter"><el-option v-for="asset in assets" :key="asset.id" :label="asset.name" :value="asset.id" /></el-select>
    </div>
    <section v-if="issuedFor" class="panel signed-panel">
      <div class="signed-body">
        <div><strong>短时下载链接</strong><small class="table-sub">{{ remaining }} · 任何持有者均可下载，请勿转发</small></div>
        <code class="signed-url">{{ issuedUrl }}</code>
        <div class="signed-actions"><el-button text @click="copyIssued">复制</el-button><el-button text @click="clearIssued">关闭</el-button></div>
      </div>
    </section>
    <section class="panel">
      <el-table :data="visibleEvidence" empty-text="暂无检测证据">
        <el-table-column label="资产" min-width="180"><template #default="{ row }">{{ assetMap.get(row.assetId)?.name || row.assetId }}</template></el-table-column>
        <el-table-column prop="kind" label="证据类型" width="150" />
        <el-table-column prop="mediaType" label="媒体类型" min-width="170" />
        <el-table-column label="大小" width="110"><template #default="{ row }">{{ Math.ceil(row.size / 1024) }} KB</template></el-table-column>
        <el-table-column label="采集时间" width="180"><template #default="{ row }">{{ formatTime(row.createdAt) }}</template></el-table-column>
        <el-table-column label="操作" width="220" fixed="right">
          <template #default="{ row }">
            <!-- The authenticated download stays the default: it is scoped to the
                 session and needs no credential handling in the page. The signed
                 link is the deliberate second option for handing an object to a
                 tool that cannot carry the session. -->
            <a class="evidence-link no-margin" :href="`/api/v1/evidence/${row.id}/download`">下载</a>
            <el-button v-if="canOperate" text :loading="issuing && issuedFor === row.id" @click="issue(row)">短时链接</el-button>
          </template>
        </el-table-column>
      </el-table>
      <ListFooter :has-more="hasMore" :loading="loadingMore" @load-more="emit('loadMore')" />
    </section>
  </section>
</template>

<style scoped>
.table-sub { display: block; color: var(--wm-muted); font-size: 12px; }
.signed-panel { border-color: var(--el-color-warning-light-5); }
.signed-body { display: flex; flex-direction: column; gap: 6px; }
.signed-url { display: block; font-size: 12px; word-break: break-all; color: var(--wm-muted); }
.signed-actions { display: flex; gap: 8px; }
</style>
