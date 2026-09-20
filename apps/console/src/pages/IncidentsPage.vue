<script setup lang="ts">
import { computed, ref, watch } from "vue";
import IncidentTimeline from "../components/monitoring/IncidentTimeline.vue";
import type { AlertDelivery, Asset, Evidence, Incident } from "../types/domain";

type Props = {
  incidents: Incident[];
  assetMap: Map<string, Asset>;
  evidence: Evidence[];
  deliveries: AlertDelivery[];
  canOperate: boolean;
};

const props = defineProps<Props>();
const emit = defineEmits<{ transition: [incident: Incident, status: Incident["status"]] }>();
// Held by id rather than by object: the store replaces data.incidents on every
// refresh, so a captured object would keep showing the state the drawer was
// opened with -- including after the incident changed underneath it.
const selectedId = ref("");
const detailOpen = ref(false);
const selected = computed(() => props.incidents.find((item) => item.id === selectedId.value));

// Bumped when an action writes to the timeline. The status only reaches this
// component through the store refresh that follows the transition, so watching
// it keeps the refetch behind the write instead of racing it.
const timelineKey = ref(0);
const renderedStatus = ref("");
watch(selected, (current) => {
  if (!current) return;
  if (renderedStatus.value && current.status !== renderedStatus.value) {
    timelineKey.value += 1;
  }
  renderedStatus.value = current.status;
});

const detailEvidence = computed(() => selected.value ? props.evidence.filter((item) => selected.value?.evidenceIds?.includes(item.id)) : []);
const detailDeliveries = computed(() => selected.value ? props.deliveries.filter((item) => item.incidentId === selected.value?.id) : []);

function openIncident(incident: Incident) {
  selectedId.value = incident.id;
  renderedStatus.value = incident.status;
  detailOpen.value = true;
}

function formatTime(value?: string) {
  return value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "-";
}

function statusType(status: string) {
  return ["CRITICAL", "HIGH"].includes(status) ? "danger" : ["MEDIUM", "ACKNOWLEDGED", "INVESTIGATING"].includes(status) ? "warning" : "info";
}

function transition(status: Incident["status"]) {
  if (!selected.value) return;
  // The drawer stays open: the whole point of the timeline is seeing the entry
  // the action just produced.
  emit("transition", selected.value, status);
}
</script>

<template>
  <section class="view-content">
    <div class="page-toolbar">
      <div><span class="section-kicker">INCIDENT RESPONSE</span><p class="toolbar-copy">所有事件都必须关联证据，关闭高风险事件需要处置说明。</p></div>
    </div>
    <section class="panel">
      <el-table :data="incidents" empty-text="暂无安全事件" @row-click="openIncident">
        <el-table-column label="风险" width="105"><template #default="{ row }"><el-tag :type="statusType(row.severity)" effect="dark">{{ row.severity }}</el-tag></template></el-table-column>
        <el-table-column label="事件" min-width="290"><template #default="{ row }"><strong>{{ row.title }}</strong><small class="table-sub">{{ assetMap.get(row.assetId)?.url }}</small></template></el-table-column>
        <el-table-column prop="occurrenceCount" label="次数" width="80" />
        <el-table-column prop="status" label="状态" width="145" />
        <el-table-column label="最近发现" width="180"><template #default="{ row }">{{ formatTime(row.lastSeenAt) }}</template></el-table-column>
        <el-table-column label="处置" width="250" fixed="right"><template #default="{ row }">
          <el-button v-if="row.status === 'OPEN' && canOperate" link @click.stop="emit('transition', row, 'ACKNOWLEDGED')">认领</el-button>
          <el-button v-if="row.status === 'ACKNOWLEDGED' && canOperate" link @click.stop="emit('transition', row, 'INVESTIGATING')">开始调查</el-button>
          <el-button v-if="row.status === 'INVESTIGATING' && canOperate" type="success" link @click.stop="emit('transition', row, 'RESOLVED')">标记恢复</el-button>
          <el-button v-if="['OPEN', 'INVESTIGATING'].includes(row.status) && canOperate" type="danger" link @click.stop="emit('transition', row, 'FALSE_POSITIVE')">误报</el-button>
          <a class="evidence-link" :href="`/api/v1/reports/${row.id}`" @click.stop>导出证据</a>
        </template></el-table-column>
      </el-table>
    </section>

    <el-drawer v-model="detailOpen" title="事件详情" size="min(680px, 100vw)">
      <template v-if="selected">
        <div class="incident-detail-head">
          <div><el-tag :type="statusType(selected.severity)" effect="dark">{{ selected.severity }}</el-tag><el-tag class="detail-status" effect="plain">{{ selected.status }}</el-tag></div>
          <strong>{{ selected.title }}</strong>
          <small>{{ assetMap.get(selected.assetId)?.url || selected.assetId }}</small>
        </div>
        <el-descriptions :column="2" border size="small">
          <el-descriptions-item label="出现次数">{{ selected.occurrenceCount }}</el-descriptions-item>
          <el-descriptions-item label="基线版本">{{ selected.baselineVersion || "-" }}</el-descriptions-item>
          <el-descriptions-item label="首次发现">{{ formatTime(selected.firstSeenAt) }}</el-descriptions-item>
          <el-descriptions-item label="最近发现">{{ formatTime(selected.lastSeenAt) }}</el-descriptions-item>
          <el-descriptions-item label="确认时间">{{ formatTime(selected.acknowledgedAt) }}</el-descriptions-item>
          <el-descriptions-item label="恢复时间">{{ formatTime(selected.resolvedAt) }}</el-descriptions-item>
        </el-descriptions>

        <h3 class="detail-heading">检测信号</h3>
        <el-empty v-if="!selected.signals?.length" description="没有关联信号" :image-size="54" />
        <div v-for="signal in selected.signals" :key="signal.id" class="signal-row">
          <div><el-tag size="small" :type="statusType(signal.severity)" effect="plain">{{ signal.severity }}</el-tag><strong>{{ signal.kind }}</strong></div>
          <p>{{ signal.summary }}</p>
          <small>规则 {{ signal.ruleVersion }}</small>
        </div>

        <h3 class="detail-heading">证据</h3>
        <el-empty v-if="!detailEvidence.length" description="没有可用证据" :image-size="54" />
        <div v-for="item in detailEvidence" :key="item.id" class="evidence-row">
          <span><strong>{{ item.kind }}</strong><small>{{ item.mediaType }} · {{ Math.ceil(item.size / 1024) }} KB · {{ formatTime(item.createdAt) }}</small></span>
          <a class="evidence-link no-margin" :href="`/api/v1/evidence/${item.id}/download`">下载</a>
        </div>

        <h3 class="detail-heading">告警投递</h3>
        <el-empty v-if="!detailDeliveries.length" description="尚未产生投递记录" :image-size="54" />
        <div v-for="delivery in detailDeliveries" :key="delivery.id" class="evidence-row">
          <span><strong>{{ delivery.status }}</strong><small>{{ delivery.attempts }} 次尝试 · {{ formatTime(delivery.updatedAt) }}</small></span>
          <span class="muted">{{ delivery.lastError || "-" }}</span>
        </div>

        <IncidentTimeline v-if="selected" :incident-id="selected.id" :refresh-key="timelineKey" />

        <div v-if="canOperate" class="detail-actions">
          <el-button v-if="selected.status === 'OPEN'" type="primary" @click="transition('ACKNOWLEDGED')">认领事件</el-button>
          <el-button v-if="selected.status === 'ACKNOWLEDGED'" type="primary" @click="transition('INVESTIGATING')">开始调查</el-button>
          <el-button v-if="selected.status === 'INVESTIGATING'" type="success" @click="transition('RESOLVED')">标记恢复</el-button>
          <el-button v-if="['OPEN', 'INVESTIGATING'].includes(selected.status)" type="danger" plain @click="transition('FALSE_POSITIVE')">标记误报</el-button>
        </div>
      </template>
    </el-drawer>
  </section>
</template>