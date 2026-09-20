<script setup lang="ts">
import { computed } from "vue";
import type { Baseline } from "../../types/domain";

// Untrusted by construction: every field below was read off a monitored page or
// its HTTP traffic. None of it is rendered as a link, an image or markup --
// only as text -- so a tampered page cannot steer the operator anywhere from
// the review screen. The one place a value is shortened is the title, which has
// no length bound on the wire.
const props = defineProps<{ baseline: Baseline; loading: boolean }>();
const emit = defineEmits<{ close: [] }>();

type Snapshot = NonNullable<Baseline["snapshot"]>;

const snapshot = computed<Snapshot | undefined>(() => props.baseline.snapshot);

// A capture that failed does not omit `snapshot`; it carries one where every
// field is its zero value. The contract says so explicitly, so the empty table
// below is the honest thing to show rather than a fabricated "no data" row.
function hasCapture(value: Snapshot | undefined) {
  return Boolean(value && value.capturedAt);
}

function formatTime(value?: string) {
  return value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "-";
}

function shorten(value: string, max = 120) {
  return value.length > max ? `${value.slice(0, max)}…` : value;
}

// Record<string, string[]> flattened to rows: responseHeaders is the only map
// in the snapshot, and a table row per header name reads better than one cell
// holding a joined list.
const headerRows = computed(() => Object.entries(snapshot.value?.responseHeaders ?? {}));
const scriptRows = computed(() => snapshot.value?.scripts ?? []);
const formRows = computed(() => snapshot.value?.forms ?? []);
const linkRows = computed(() => snapshot.value?.links ?? []);
const requestRows = computed(() => snapshot.value?.resourceRequests ?? []);
const blockedRows = computed(() => snapshot.value?.blockedRequests ?? []);
const redirectRows = computed(() => snapshot.value?.redirectChain ?? []);
const ipRows = computed(() => snapshot.value?.resolvedIps ?? []);
const popupRows = computed(() => snapshot.value?.popups ?? []);
const dialogRows = computed(() => snapshot.value?.dialogs ?? []);
const downloadRows = computed(() => snapshot.value?.downloads ?? []);
</script>

<template>
  <el-drawer :model-value="true" title="基线快照" size="620px" @close="emit('close')">
    <div v-loading="loading" class="snapshot-body">
      <template v-if="baseline">
        <el-descriptions :column="1" border size="small">
          <el-descriptions-item label="资产版本">v{{ baseline.version }}</el-descriptions-item>
          <el-descriptions-item label="状态">{{ baseline.status }}</el-descriptions-item>
          <el-descriptions-item label="创建时间">{{ formatTime(baseline.createdAt) }}</el-descriptions-item>
        </el-descriptions>

        <el-alert v-if="!hasCapture(snapshot)" type="warning" :closable="false" show-icon title="快照不可用" description="该候选基线的抓取未成功，因此没有可供比对的快照元数据。" />

        <template v-else>
          <h4 class="snapshot-heading">抓取结果</h4>
          <el-descriptions :column="1" border size="small">
            <el-descriptions-item label="请求地址"><span class="mono">{{ snapshot?.url }}</span></el-descriptions-item>
            <el-descriptions-item label="最终地址"><span class="mono">{{ snapshot?.finalUrl }}</span></el-descriptions-item>
            <el-descriptions-item label="状态码">{{ snapshot?.statusCode }}</el-descriptions-item>
            <el-descriptions-item label="标题"><span class="mono">{{ shorten(snapshot?.title || "") }}</span></el-descriptions-item>
            <el-descriptions-item label="内容哈希"><span class="mono">{{ snapshot?.contentHash }}</span></el-descriptions-item>
            <el-descriptions-item label="文本哈希"><span class="mono">{{ snapshot?.textHash }}</span></el-descriptions-item>
            <el-descriptions-item v-if="snapshot?.screenshotHash" label="截图哈希"><span class="mono">{{ snapshot.screenshotHash }}</span></el-descriptions-item>
            <el-descriptions-item label="采集时间">{{ formatTime(snapshot?.capturedAt) }}</el-descriptions-item>
            <el-descriptions-item label="耗时">{{ snapshot?.durationMillis }} ms</el-descriptions-item>
            <el-descriptions-item v-if="snapshot?.tlsFingerprint" label="TLS 指纹"><span class="mono">{{ snapshot.tlsFingerprint }}</span></el-descriptions-item>
          </el-descriptions>

          <!-- Every list below holds values copied out of the monitored page.
               They are displayed as text on purpose: rendering them as links or
               frames would let a tampered page reach the operator from the very
               screen that exists to judge it. -->
          <template v-if="redirectRows.length">
            <h4 class="snapshot-heading">重定向链</h4>
            <ul class="snapshot-list"><li v-for="(item, index) in redirectRows" :key="index" class="mono">{{ item }}</li></ul>
          </template>

          <template v-if="ipRows.length">
            <h4 class="snapshot-heading">解析 IP</h4>
            <ul class="snapshot-list"><li v-for="(item, index) in ipRows" :key="index" class="mono">{{ item }}</li></ul>
          </template>

          <template v-if="scriptRows.length">
            <h4 class="snapshot-heading">脚本（{{ scriptRows.length }}）</h4>
            <ul class="snapshot-list">
              <li v-for="(item, index) in scriptRows" :key="index" class="mono">{{ item.url }}<small v-if="item.inlineHash" class="table-sub">内联哈希 {{ item.inlineHash }}</small></li>
            </ul>
          </template>

          <template v-if="linkRows.length">
            <h4 class="snapshot-heading">链接（{{ linkRows.length }}）</h4>
            <ul class="snapshot-list"><li v-for="(item, index) in linkRows" :key="index" class="mono">{{ item }}</li></ul>
          </template>

          <template v-if="formRows.length">
            <h4 class="snapshot-heading">表单（{{ formRows.length }}）</h4>
            <el-table :data="formRows" size="small">
              <el-table-column label="提交地址" min-width="280"><template #default="{ row }"><span class="mono">{{ row.action }}</span></template></el-table-column>
              <el-table-column label="方法" width="110"><template #default="{ row }"><span class="mono">{{ row.method }}</span></template></el-table-column>
            </el-table>
          </template>

          <template v-if="headerRows.length">
            <h4 class="snapshot-heading">响应头（{{ headerRows.length }}）</h4>
            <el-table :data="headerRows.map(([name, values]) => ({ name, values }))" size="small">
              <el-table-column label="名称" min-width="180"><template #default="{ row }"><span class="mono">{{ row.name }}</span></template></el-table-column>
              <el-table-column label="值" min-width="280"><template #default="{ row }"><span class="mono">{{ row.values.join("; ") }}</span></template></el-table-column>
            </el-table>
          </template>

          <template v-if="requestRows.length">
            <h4 class="snapshot-heading">资源请求（{{ requestRows.length }}）</h4>
            <ul class="snapshot-list"><li v-for="(item, index) in requestRows" :key="index" class="mono">{{ item }}</li></ul>
          </template>

          <template v-if="blockedRows.length">
            <h4 class="snapshot-heading">已拦截请求（{{ blockedRows.length }}）</h4>
            <ul class="snapshot-list"><li v-for="(item, index) in blockedRows" :key="index" class="mono">{{ item }}</li></ul>
          </template>

          <template v-if="popupRows.length">
            <h4 class="snapshot-heading">弹窗（{{ popupRows.length }}）</h4>
            <ul class="snapshot-list"><li v-for="(item, index) in popupRows" :key="index" class="mono">{{ item }}</li></ul>
          </template>

          <template v-if="dialogRows.length">
            <h4 class="snapshot-heading">对话框（{{ dialogRows.length }}）</h4>
            <ul class="snapshot-list"><li v-for="(item, index) in dialogRows" :key="index" class="mono">{{ item }}</li></ul>
          </template>

          <template v-if="downloadRows.length">
            <h4 class="snapshot-heading">下载（{{ downloadRows.length }}）</h4>
            <ul class="snapshot-list"><li v-for="(item, index) in downloadRows" :key="index" class="mono">{{ item }}</li></ul>
          </template>
        </template>
      </template>
    </div>
  </el-drawer>
</template>

<style scoped>
.snapshot-body { display: flex; flex-direction: column; gap: 12px; }
.snapshot-heading { margin: 8px 0 0; font-size: 13px; color: var(--wm-muted); }
.snapshot-list { margin: 0; padding-left: 18px; display: flex; flex-direction: column; gap: 4px; }
.mono { font-family: var(--wm-mono, monospace); font-size: 12px; word-break: break-all; }
</style>
