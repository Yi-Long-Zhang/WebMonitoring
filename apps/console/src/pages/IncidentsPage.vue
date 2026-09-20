<script setup lang="ts">
import { computed, ref } from "vue";
import IncidentTimeline from "../components/monitoring/IncidentTimeline.vue";
import ListFooter from "../components/monitoring/ListFooter.vue";
import type { AlertDelivery, Asset, Evidence, Incident, User } from "../types/domain";

type Props = {
  incidents: Incident[];
  assetMap: Map<string, Asset>;
  evidence: Evidence[];
  deliveries: AlertDelivery[];
  users: User[];
  canOperate: boolean;
  // Owned by the parent, which bumps it once a write has actually landed. Doing
  // it here on click would fire a reload that can reach the API before the
  // entry it is meant to show exists.
  timelineKey: number;
  // Comments go through the features layer like every other write, so the page
  // takes a handler rather than calling the API itself. It returns a promise so
  // the draft is only cleared once the write succeeded.
  onComment: (incident: Incident, note: string) => Promise<void>;
  hasMore: boolean;
  loadingMore: boolean;
};

const props = defineProps<Props>();
const emit = defineEmits<{
  transition: [incident: Incident, status: Incident["status"], ownerId: string];
  loadMore: [];
}>();

// Held by id rather than by object: the store replaces data.incidents on every
// refresh, so a captured object would keep showing the state the drawer was
// opened with -- including after the incident changed underneath it.
const selectedId = ref("");
const detailOpen = ref(false);
const selected = computed(() => props.incidents.find((item) => item.id === selectedId.value));

const ownerId = ref("");
const commentDraft = ref("");
const commentSubmitting = ref(false);

const detailEvidence = computed(() => selected.value ? props.evidence.filter((item) => selected.value?.evidenceIds?.includes(item.id)) : []);
const detailDeliveries = computed(() => selected.value ? props.deliveries.filter((item) => item.incidentId === selected.value?.id) : []);
const canSubmitComment = computed(() => commentDraft.value.trim().length >= 2);

function openIncident(incident: Incident) {
  selectedId.value = incident.id;
  ownerId.value = incident.ownerId ?? "";
  commentDraft.value = "";
  detailOpen.value = true;
}

function formatTime(value?: string) {
  return value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "-";
}

function statusType(status: string) {
  return ["CRITICAL", "HIGH"].includes(status) ? "danger" : ["MEDIUM", "ACKNOWLEDGED", "INVESTIGATING"].includes(status) ? "warning" : "info";
}

function ownerName(id?: string) {
  if (!id) return "未指派";
  return props.users.find((user) => user.id === id)?.username ?? id;
}

function transition(status: Incident["status"]) {
  if (!selected.value) return;
  // The owner travels with the transition because the API validates a state
  // change on every call; there is no owner-only endpoint to call instead.
  emit("transition", selected.value, status, ownerId.value);
}

async function submitComment() {
  if (!selected.value || !canSubmitComment.value) return;
  commentSubmitting.value = true;
  try {
    await props.onComment(selected.value, commentDraft.value.trim());
    commentDraft.value = "";
  } finally {
    commentSubmitting.value = false;
  }
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
        <el-table-column label="负责人" width="120"><template #default="{ row }">{{ ownerName(row.ownerId) }}</template></el-table-column>
        <el-table-column prop="status" label="状态" width="145" />
        <el-table-column label="最近发现" width="180"><template #default="{ row }">{{ formatTime(row.lastSeenAt) }}</template></el-table-column>
        <el-table-column label="处置" width="250" fixed="right"><template #default="{ row }">
          <el-button v-if="row.status === 'OPEN' && canOperate" link @click.stop="emit('transition', row, 'ACKNOWLEDGED', row.ownerId || '')">认领</el-button>
          <el-button v-if="row.status === 'ACKNOWLEDGED' && canOperate" link @click.stop="emit('transition', row, 'INVESTIGATING', row.ownerId || '')">开始调查</el-button>
          <el-button v-if="row.status === 'INVESTIGATING' && canOperate" type="success" link @click.stop="emit('transition', row, 'RESOLVED', row.ownerId || '')">标记恢复</el-button>
          <el-button v-if="['OPEN', 'INVESTIGATING'].includes(row.status) && canOperate" type="danger" link @click.stop="emit('transition', row, 'FALSE_POSITIVE', row.ownerId || '')">误报</el-button>
          <a class="evidence-link" :href="`/api/v1/reports/${row.id}`" @click.stop>导出证据</a>
        </template></el-table-column>
      </el-table>
      <ListFooter :has-more="hasMore" :loading="loadingMore" @load-more="emit('loadMore')" />
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
          <el-descriptions-item label="负责人">{{ ownerName(selected.ownerId) }}</el-descriptions-item>
          <el-descriptions-item label="首次发现">{{ formatTime(selected.firstSeenAt) }}</el-descriptions-item>
          <el-descriptions-item label="最近发现">{{ formatTime(selected.lastSeenAt) }}</el-descriptions-item>
          <el-descriptions-item label="确认时间">{{ formatTime(selected.acknowledgedAt) }}</el-descriptions-item>
          <el-descriptions-item label="恢复时间">{{ formatTime(selected.resolvedAt) }}</el-descriptions-item>
          <el-descriptions-item label="处置说明">{{ selected.resolution || "-" }}</el-descriptions-item>
        </el-descriptions>

        <h3 class="detail-heading">当前检测信号</h3>
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

        <IncidentTimeline :incident-id="selected.id" :refresh-key="timelineKey" />

        <template v-if="canOperate">
          <h3 class="detail-heading">追加备注</h3>
          <el-input v-model="commentDraft" type="textarea" :rows="2" maxlength="2000" show-word-limit placeholder="记录与客户或运维的沟通、判断依据；备注作为时间线条目保存，不覆盖既有记录" />
          <el-button class="comment-submit" type="primary" plain :loading="commentSubmitting" :disabled="!canSubmitComment" @click="submitComment">记录备注</el-button>

          <h3 class="detail-heading">处置</h3>
          <el-select v-model="ownerId" clearable filterable placeholder="负责人（随本次处置一并指派）" class="owner-select">
            <el-option v-for="user in users" :key="user.id" :label="user.username" :value="user.id" />
          </el-select>
          <div class="detail-actions">
            <el-button v-if="selected.status === 'OPEN'" type="primary" @click="transition('ACKNOWLEDGED')">认领事件</el-button>
            <el-button v-if="selected.status === 'ACKNOWLEDGED'" type="primary" @click="transition('INVESTIGATING')">开始调查</el-button>
            <el-button v-if="selected.status === 'INVESTIGATING'" type="success" @click="transition('RESOLVED')">标记恢复</el-button>
            <el-button v-if="['OPEN', 'INVESTIGATING'].includes(selected.status)" type="danger" plain @click="transition('FALSE_POSITIVE')">标记误报</el-button>
            <el-button v-if="selected.status === 'RESOLVED'" type="primary" plain @click="transition('CLOSED')">关闭事件</el-button>
          </div>
        </template>
      </template>
    </el-drawer>
  </section>
</template>
