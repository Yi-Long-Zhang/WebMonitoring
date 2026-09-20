<script setup lang="ts">
import { ref, watch } from "vue";
import { getIncidentTimeline } from "../../services/api/client";
import type { IncidentEvent } from "../../types/domain";

// refreshKey is bumped by the parent after an action that writes to the
// timeline. Relying on the store refresh instead would not order the two: the
// refetch could reach the API before the write that produced the new entry.
const props = defineProps<{ incidentId: string; refreshKey?: number }>();

const pageSize = 20;
const events = ref<IncidentEvent[]>([]);
const nextCursor = ref("");
const loading = ref(false);
const loadingEarlier = ref(false);
const error = ref("");

async function load() {
  loading.value = true;
  error.value = "";
  try {
    const page = await getIncidentTimeline(props.incidentId, { limit: pageSize });
    events.value = page.items;
    nextCursor.value = page.nextCursor;
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : "时间线加载失败";
    events.value = [];
    nextCursor.value = "";
  } finally {
    loading.value = false;
  }
}

async function loadEarlier() {
  if (!nextCursor.value || loadingEarlier.value) return;
  loadingEarlier.value = true;
  error.value = "";
  try {
    const page = await getIncidentTimeline(props.incidentId, { limit: pageSize, cursor: nextCursor.value });
    events.value = [...events.value, ...page.items];
    nextCursor.value = page.nextCursor;
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : "更早的记录加载失败";
  } finally {
    loadingEarlier.value = false;
  }
}

watch(() => [props.incidentId, props.refreshKey], load, { immediate: true });

const typeLabels: Record<string, string> = {
  CREATED: "首次发现",
  RECURRED: "再次复现",
  STATUS_CHANGED: "状态流转",
  OWNER_ASSIGNED: "指派负责人",
  COMMENTED: "备注",
};

function typeLabel(event: IncidentEvent) {
  return typeLabels[event.type] ?? event.type;
}

function typeTag(event: IncidentEvent) {
  if (event.type === "CREATED") return "danger";
  if (event.type === "RECURRED") return "warning";
  if (event.type === "COMMENTED") return "info";
  return "success";
}

function formatTime(value?: string) {
  return value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "-";
}

// An entry with no actor was written by the detection worker; saying who did it
// would be inventing one.
function actorLabel(event: IncidentEvent) {
  return event.actorId ? event.actorId.slice(0, 8) : "自动检测";
}

function detailText(event: IncidentEvent) {
  const details = (event.details ?? {}) as Record<string, unknown>;
  return typeof details.note === "string" ? details.note : "";
}

function resolutionText(event: IncidentEvent) {
  const details = (event.details ?? {}) as Record<string, unknown>;
  return typeof details.resolution === "string" ? details.resolution : "";
}

// A backfilled anchor records that the incident existed and that its earlier
// occurrences were not captured -- it deliberately carries no signals.
function backfilledNote(event: IncidentEvent) {
  const details = (event.details ?? {}) as Record<string, unknown>;
  if (details.backfilled !== true) return "";
  const missing = Number(details.unrecordedOccurrences ?? 0);
  return missing > 0
    ? `迁移前的记录，此前 ${missing} 次复现的明细未能留存`
    : "迁移前的记录，明细未能留存";
}
</script>

<template>
  <div>
    <h3 class="detail-heading">事件时间线</h3>
    <el-skeleton v-if="loading" :rows="3" animated />
    <template v-else>
      <el-alert v-if="error" :title="error" type="error" :closable="false" show-icon>
        <template #default><el-button link type="primary" @click="load">重试</el-button></template>
      </el-alert>
      <el-empty v-else-if="!events.length" description="暂无时间线记录" :image-size="54" />
      <el-timeline v-else>
        <el-timeline-item
          v-for="event in events"
          :key="event.id"
          :timestamp="formatTime(event.createdAt)"
          placement="top"
        >
          <div class="timeline-head">
            <el-tag size="small" :type="typeTag(event)" effect="plain">{{ typeLabel(event) }}</el-tag>
            <el-tag v-if="event.toStatus" size="small" effect="plain">
              {{ event.fromStatus }} → {{ event.toStatus }}
            </el-tag>
            <small class="muted">{{ actorLabel(event) }}</small>
          </div>

          <el-alert
            v-if="backfilledNote(event)"
            class="timeline-note"
            :title="backfilledNote(event)"
            type="info"
            :closable="false"
          />
          <p v-else-if="detailText(event)" class="timeline-note">{{ detailText(event) }}</p>
          <p v-else-if="resolutionText(event)" class="timeline-note">处置说明：{{ resolutionText(event) }}</p>

          <div v-for="signal in event.signals" :key="signal.id" class="signal-row">
            <div><strong>{{ signal.kind }}</strong></div>
            <p>{{ signal.summary }}</p>
            <small>规则 {{ signal.ruleVersion }}</small>
          </div>
        </el-timeline-item>
      </el-timeline>

      <el-button v-if="nextCursor" link type="primary" :loading="loadingEarlier" @click="loadEarlier">
        加载更早的记录
      </el-button>
    </template>
  </div>
</template>
