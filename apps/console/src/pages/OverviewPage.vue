<script setup lang="ts">
import { computed } from "vue";
import EChart from "../components/monitoring/EChart.vue";
import { detectionGaugeOption, severityPieOption, trendOption, type TrendPoint } from "../features/monitoring/charts";
import type { Asset, Dashboard, Incident } from "../types/domain";

type ReportSummary = {
  successfulJobs: number;
  failedJobs: number;
  incidents: number;
  mttdSeconds: number;
  mttaSeconds: number;
  mttrSeconds: number;
  severityCounts: Record<string, number>;
  trend: TrendPoint[];
};

const props = defineProps<{
  dashboard?: Dashboard;
  reportSummary?: ReportSummary;
  assets: Asset[];
  incidents: Incident[];
  assetMap: Map<string, Asset>;
}>();
const emit = defineEmits<{ navigate: [view: "incidents"] }>();
const successRate = computed(() => {
  const total = (props.reportSummary?.successfulJobs ?? 0) + (props.reportSummary?.failedJobs ?? 0);
  return total ? Math.round((props.reportSummary?.successfulJobs ?? 0) / total * 100) : 0;
});
// The three charts read only what the summary already carries, so they are
// computed rather than fetched: the page has no second loading state to get
// wrong, and a summary refresh redraws them for free.
const trend = computed(() => trendOption(props.reportSummary?.trend ?? []));
const severityPie = computed(() => severityPieOption(props.reportSummary?.severityCounts ?? {}));
const detectionGauge = computed(() =>
  detectionGaugeOption({
    successfulJobs: props.reportSummary?.successfulJobs ?? 0,
    failedJobs: props.reportSummary?.failedJobs ?? 0,
    severityCounts: props.reportSummary?.severityCounts ?? {},
  }),
);
const hasTrend = computed(() => (props.reportSummary?.trend ?? []).some((point) => point.total > 0));
function formatTime(value: string) { return new Date(value).toLocaleString("zh-CN", { hour12: false }); }
function formatDuration(seconds = 0) { if (!seconds) return "-"; return seconds < 60 ? `${Math.round(seconds)} 秒` : `${Math.round(seconds / 60)} 分钟`; }
function statusType(status: string) {
  if (["CRITICAL", "HIGH"].includes(status)) return "danger";
  if (["MEDIUM", "ACKNOWLEDGED", "INVESTIGATING"].includes(status)) return "warning";
  return "info";
}
</script>

<template>
  <section class="view-content">
    <div class="summary-grid">
      <div class="summary-card accent"><span class="summary-label">纳管资产</span><strong>{{ dashboard?.assets ?? 0 }}</strong><small>{{ dashboard?.verifiedAssets ?? 0 }} 个已完成授权</small></div>
      <div class="summary-card"><span class="summary-label">待处置事件</span><strong>{{ dashboard?.openIncidents ?? 0 }}</strong><small>持续监测中</small></div>
      <div class="summary-card danger-card"><span class="summary-label">严重风险</span><strong>{{ dashboard?.criticalIncidents ?? 0 }}</strong><small>需要优先处理</small></div>
      <div class="summary-card"><span class="summary-label">检测失败</span><strong>{{ dashboard?.failedJobs ?? 0 }}</strong><small>请检查节点或目标状态</small></div>
    </div>
    <section class="panel metrics-panel">
      <div class="panel-header"><div><span class="section-kicker">OPERATIONS METRICS</span><h2>运行质量</h2></div><small class="muted">基于当前租户数据</small></div>
      <div class="metric-row"><div><span>检测成功率</span><strong>{{ successRate }}%</strong></div><div><span>MTTD</span><strong>{{ formatDuration(reportSummary?.mttdSeconds) }}</strong></div><div><span>MTTA</span><strong>{{ formatDuration(reportSummary?.mttaSeconds) }}</strong></div><div><span>MTTR</span><strong>{{ formatDuration(reportSummary?.mttrSeconds) }}</strong></div><div><span>告警事件</span><strong>{{ reportSummary?.incidents ?? 0 }}</strong></div></div>
    </section>
    <div class="chart-grid">
      <section class="panel chart-panel wide">
        <div class="panel-header"><div><span class="section-kicker">INCIDENT TREND</span><h2>事件趋势</h2></div><small class="muted">按天统计，未指定区间时为最近 30 天</small></div>
        <EChart v-if="hasTrend" :option="trend" height="280px" />
        <el-empty v-else description="该区间内没有事件" :image-size="70" />
      </section>
      <section class="panel chart-panel">
        <div class="panel-header"><div><span class="section-kicker">SEVERITY MIX</span><h2>风险分布</h2></div></div>
        <EChart v-if="reportSummary?.incidents" :option="severityPie" height="280px" />
        <el-empty v-else description="暂无风险数据" :image-size="70" />
      </section>
      <section class="panel chart-panel">
        <div class="panel-header"><div><span class="section-kicker">DETECTION HEALTH</span><h2>检测成功率</h2></div></div>
        <EChart :option="detectionGauge" height="280px" />
      </section>
    </div>
    <div class="content-grid">
      <section class="panel wide">
        <div class="panel-header"><div><span class="section-kicker">ACTIVE CASES</span><h2>待处置事件</h2></div><el-button text @click="emit('navigate', 'incidents')">查看全部 →</el-button></div>
        <el-table :data="incidents.filter((item) => !['CLOSED', 'FALSE_POSITIVE'].includes(item.status)).slice(0, 6)" empty-text="当前没有待处置事件">
          <el-table-column label="风险" width="100"><template #default="{ row }"><el-tag :type="statusType(row.severity)" effect="dark">{{ row.severity }}</el-tag></template></el-table-column>
          <el-table-column label="事件" min-width="240"><template #default="{ row }"><strong>{{ row.title }}</strong><small class="table-sub">{{ assetMap.get(row.assetId)?.url }}</small></template></el-table-column>
          <el-table-column prop="occurrenceCount" label="出现次数" width="100" /><el-table-column label="最近发现" width="170"><template #default="{ row }">{{ formatTime(row.lastSeenAt) }}</template></el-table-column><el-table-column prop="status" label="状态" width="120" />
        </el-table>
      </section>
      <section class="panel"><div class="panel-header"><div><span class="section-kicker">MONITORING</span><h2>资产状态</h2></div></div><div class="health-list"><div v-for="asset in assets.slice(0, 6)" :key="asset.id" class="health-row"><span class="health-light" :class="{ off: !asset.enabled, pending: asset.verificationStatus !== 'VERIFIED' }" /><span><strong>{{ asset.name }}</strong><small>{{ asset.url }}</small></span><el-tag size="small" :type="asset.verificationStatus === 'VERIFIED' ? 'success' : 'warning'" effect="plain">{{ asset.verificationStatus }}</el-tag></div><el-empty v-if="!assets.length" description="尚未登记资产" :image-size="60" /></div></section>
    </div>
  </section>
</template>