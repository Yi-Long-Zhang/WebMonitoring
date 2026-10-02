<script setup lang="ts">
import { ref } from "vue";
import ListFooter from "../components/monitoring/ListFooter.vue";
import type { AuditFilterParams } from "../services/api/client";
import type { AuditEvent } from "../types/domain";

defineProps<{ audits: AuditEvent[]; hasMore: boolean; loadingMore: boolean }>();
const emit = defineEmits<{ loadMore: []; filter: [params: AuditFilterParams]; export: [params: AuditFilterParams] }>();

// The action vocabulary is open: every handler writes its own, and
// `baseline.*` is built dynamically from the target status. This list is a
// convenience, and the select is filterable + allow-create so an action that is
// not listed here is still searchable.
const KNOWN_ACTIONS = [
  "asset.create", "asset.update", "asset.import", "asset.export",
  "asset.suspend", "asset.resume", "asset.verification.issue", "asset.verification.verify",
  "baseline.approved", "baseline.rejected", "baseline.retired",
  "detection.run", "detection.cancel", "detection.dead_letter",
  "incident.transition", "incident.comment",
  "policy.update", "evidence.download", "evidence.signed_url.issued",
  "alert_channel.create", "alert_channel.update", "alert_channel.delete",
  "alert.dead_letter",
  "site.create", "site.update",
  "tenant.create", "tenant.update", "tenant.delete",
  "user.create", "user.update", "user.password.reset",
  "auth.login", "auth.logout", "auth.password.change",
  "report.export", "maintenance.retention.scheduled",
];

const RESOURCE_TYPES = ["asset", "site", "tenant", "user", "incident", "evidence", "detection_job", "policy", "alert_channel", "session", "retention"];
const RESULTS = ["SUCCESS", "FAILED", "PARTIAL"];

const actorId = ref("");
const action = ref("");
const resourceType = ref("");
const result = ref("");
// A [from, to] pair in RFC3339. The server rejects an unparseable value and a
// reversed range with a 400 rather than answering an empty set, so the picker
// is configured to always emit the exact format the contract expects.
const range = ref<[string, string] | undefined>();

// currentFilterParams is what the trail is filtered by right now, shared by the
// query button and the export so the two cannot disagree about the filter.
function currentFilterParams(): AuditFilterParams {
  // Empty values are sent as absent rather than as empty strings: the store
  // spreads these over the page parameters, and listQuery drops the empties.
  return {
    actorId: actorId.value.trim() || undefined,
    action: action.value || undefined,
    resourceType: resourceType.value || undefined,
    result: result.value || undefined,
    from: range.value?.[0] || undefined,
    to: range.value?.[1] || undefined,
  };
}

function apply() {
  emit("filter", currentFilterParams());
}

function clear() {
  actorId.value = "";
  action.value = "";
  resourceType.value = "";
  result.value = "";
  range.value = undefined;
  apply();
}

function formatTime(value: string) { return new Date(value).toLocaleString("zh-CN", { hour12: false }); }
</script>

<template>
  <section class="view-content">
    <div class="page-toolbar"><div><span class="section-kicker">AUDIT TRAIL</span><p class="toolbar-copy">关键操作关联操作者、资源、结果和追踪 ID。</p></div></div>
    <section class="panel audit-filters">
      <el-input v-model="actorId" clearable placeholder="操作者 UUID" class="filter-input" @keyup.enter="apply" />
      <el-select v-model="action" clearable filterable allow-create default-first-option placeholder="操作" class="filter-select">
        <el-option v-for="value in KNOWN_ACTIONS" :key="value" :label="value" :value="value" />
      </el-select>
      <el-select v-model="resourceType" clearable placeholder="资源类型" class="filter-select">
        <el-option v-for="value in RESOURCE_TYPES" :key="value" :label="value" :value="value" />
      </el-select>
      <el-select v-model="result" clearable placeholder="结果" class="filter-select narrow">
        <el-option v-for="value in RESULTS" :key="value" :label="value" :value="value" />
      </el-select>
      <el-date-picker
        v-model="range"
        type="datetimerange"
        value-format="YYYY-MM-DDTHH:mm:ss[Z]"
        start-placeholder="开始时间"
        end-placeholder="结束时间"
        class="filter-range"
      />
      <el-button type="primary" @click="apply">查询</el-button>
      <el-button @click="clear">重置</el-button>
      <el-button @click="emit('export', currentFilterParams())">导出 CSV</el-button>
    </section>
    <section class="panel">
      <el-table :data="audits" empty-text="暂无审计记录">
        <el-table-column prop="action" label="操作" min-width="190" />
        <el-table-column prop="resourceType" label="资源类型" width="140" />
        <el-table-column prop="resourceId" label="资源 ID" min-width="220" />
        <el-table-column prop="result" label="结果" width="110" />
        <el-table-column prop="traceId" label="追踪 ID" min-width="220" />
        <el-table-column label="时间" width="180"><template #default="{ row }">{{ formatTime(row.createdAt) }}</template></el-table-column>
      </el-table>
      <ListFooter :has-more="hasMore" :loading="loadingMore" @load-more="emit('loadMore')" />
    </section>
  </section>
</template>
