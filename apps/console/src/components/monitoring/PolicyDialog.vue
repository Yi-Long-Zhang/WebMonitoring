<script setup lang="ts">
import { ref, watch } from "vue";
import type { Asset, Policy } from "../../types/domain";

type PolicyInput = Pick<Policy, "httpIntervalSeconds" | "browserIntervalSeconds" | "timeoutSeconds" | "maxResponseBytes" | "ignoreSelectors" | "trustedDomains" | "maintenanceWindowStart" | "maintenanceWindowEnd"> & { maintenanceSchedule: Policy["maintenanceSchedule"] | null };
type FormState = Omit<PolicyInput, "ignoreSelectors" | "trustedDomains"> & {
  ignoreSelectors: string;
  trustedDomains: string;
  // The recurrence is edited as wall-clock times and a fixed offset rather than
  // as the minutes the API stores: "每周三 02:00" is what the operator means, and
  // making them convert to minutes is how a window ends up an hour off.
  scheduleEnabled: boolean;
  scheduleWeekdays: number[];
  scheduleStart: string;
  scheduleEnd: string;
  scheduleOffset: number;
};

const WEEKDAYS = [
  { value: 1, label: "周一" }, { value: 2, label: "周二" }, { value: 3, label: "周三" },
  { value: 4, label: "周四" }, { value: 5, label: "周五" }, { value: 6, label: "周六" },
  { value: 0, label: "周日" },
];
const OFFSETS = [
  { value: 480, label: "UTC+8 北京时间" }, { value: 0, label: "UTC" },
  { value: 540, label: "UTC+9" }, { value: 420, label: "UTC+7" },
  { value: -300, label: "UTC-5" }, { value: -480, label: "UTC-8" },
];

function minutesOfDay(value: string) {
  const match = /^(\d{1,2}):(\d{2})$/.exec(value.trim());
  if (!match) return undefined;
  return Number(match[1]) * 60 + Number(match[2]);
}
function timeOfDay(minutes: number) {
  return `${String(Math.floor(minutes / 60)).padStart(2, "0")}:${String(minutes % 60).padStart(2, "0")}`;
}

const props = defineProps<{
  modelValue: boolean;
  asset?: Asset;
  policy?: Policy;
}>();

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  save: [value: PolicyInput];
}>();

const form = ref<FormState>({
  httpIntervalSeconds: 300,
  browserIntervalSeconds: 1800,
  timeoutSeconds: 30,
  maxResponseBytes: 5 * 1024 * 1024,
  ignoreSelectors: "",
  trustedDomains: "",
  maintenanceWindowStart: undefined,
  maintenanceWindowEnd: undefined,
  maintenanceSchedule: null,
  scheduleEnabled: false,
  scheduleWeekdays: [],
  scheduleStart: "02:00",
  scheduleEnd: "04:00",
  scheduleOffset: 480,
});

watch(() => [props.modelValue, props.policy], () => {
  if (!props.policy || !props.modelValue) return;
  form.value = {
    httpIntervalSeconds: props.policy.httpIntervalSeconds,
    browserIntervalSeconds: props.policy.browserIntervalSeconds,
    timeoutSeconds: props.policy.timeoutSeconds,
    maxResponseBytes: props.policy.maxResponseBytes,
    ignoreSelectors: props.policy.ignoreSelectors.join("\n"),
    trustedDomains: props.policy.trustedDomains.join("\n"),
    maintenanceWindowStart: props.policy.maintenanceWindowStart,
    maintenanceWindowEnd: props.policy.maintenanceWindowEnd,
    maintenanceSchedule: props.policy.maintenanceSchedule ?? null,
    scheduleEnabled: Boolean(props.policy.maintenanceSchedule),
    scheduleWeekdays: props.policy.maintenanceSchedule?.weekdays ?? [],
    scheduleStart: timeOfDay(props.policy.maintenanceSchedule?.startMinute ?? 120),
    scheduleEnd: timeOfDay(props.policy.maintenanceSchedule?.endMinute ?? 240),
    scheduleOffset: props.policy.maintenanceSchedule?.utcOffsetMinutes ?? 480,
  };
}, { immediate: true });

function lines(value: string) {
  return value.split(/\r?\n/).map((item) => item.trim()).filter(Boolean);
}

function submit() {
  const { scheduleEnabled, scheduleWeekdays, scheduleStart, scheduleEnd, scheduleOffset, ...rest } = form.value;
  const start = minutesOfDay(scheduleStart);
  const end = minutesOfDay(scheduleEnd);
  emit("save", {
    ...rest,
    // null clears the recurrence; the server reads a missing key as "leave it
    // alone", and this dialog always states what the window should be.
    maintenanceSchedule: scheduleEnabled && start !== undefined && end !== undefined
      ? { weekdays: [...scheduleWeekdays].sort(), startMinute: start, endMinute: end, utcOffsetMinutes: scheduleOffset }
      : null,
    ignoreSelectors: lines(form.value.ignoreSelectors),
    trustedDomains: lines(form.value.trustedDomains),
  });
  emit("update:modelValue", false);
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="`监测策略 · ${asset?.name || ''}`"
    width="620px"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <el-form v-if="policy" label-position="top">
      <div class="form-grid">
        <el-form-item label="HTTP 间隔（秒）">
          <el-input-number v-model="form.httpIntervalSeconds" :min="60" :max="86400" controls-position="right" />
        </el-form-item>
        <el-form-item label="浏览器间隔（秒）">
          <el-input-number v-model="form.browserIntervalSeconds" :min="300" :max="604800" controls-position="right" />
        </el-form-item>
        <el-form-item label="超时（秒）">
          <el-input-number v-model="form.timeoutSeconds" :min="1" :max="90" controls-position="right" />
        </el-form-item>
        <el-form-item label="最大响应字节">
          <el-input-number v-model="form.maxResponseBytes" :min="1024" :max="10 * 1024 * 1024" controls-position="right" />
        </el-form-item>
      </div>
      <el-form-item label="忽略 CSS Selector">
        <el-input v-model="form.ignoreSelectors" type="textarea" :rows="3" placeholder="每行一个 Selector" />
      </el-form-item>
      <el-form-item label="可信域名">
        <el-input v-model="form.trustedDomains" type="textarea" :rows="2" placeholder="每行一个域名" />
      </el-form-item>
      <el-divider content-position="left">周期性维护窗口</el-divider>
      <el-form-item label="每周重复">
        <el-switch v-model="form.scheduleEnabled" active-text="启用" inactive-text="关闭" />
        <small class="muted">适合固定发版节奏；与上面的绝对区间同时生效，任一命中即为维护中。</small>
      </el-form-item>
      <template v-if="form.scheduleEnabled">
        <el-form-item label="星期">
          <el-select v-model="form.scheduleWeekdays" multiple collapse-tags placeholder="不选表示每天" style="width: 100%">
            <el-option v-for="day in WEEKDAYS" :key="day.value" :label="day.label" :value="day.value" />
          </el-select>
        </el-form-item>
        <div class="form-grid">
          <el-form-item label="开始"><el-time-picker v-model="form.scheduleStart" value-format="HH:mm" format="HH:mm" placeholder="02:00" /></el-form-item>
          <el-form-item label="结束"><el-time-picker v-model="form.scheduleEnd" value-format="HH:mm" format="HH:mm" placeholder="04:00" /></el-form-item>
        </div>
        <el-form-item label="时区">
          <el-select v-model="form.scheduleOffset" style="width: 100%">
            <el-option v-for="offset in OFFSETS" :key="offset.value" :label="offset.label" :value="offset.value" />
          </el-select>
          <small class="muted">窗口时间按此时区解释；结束早于开始表示跨午夜。</small>
        </el-form-item>
      </template>
      <el-divider content-position="left">一次性维护区间</el-divider>
      <div class="form-grid">
        <el-form-item label="维护窗口开始">
          <el-input v-model="form.maintenanceWindowStart" placeholder="RFC3339，例如 2026-09-12T01:00:00Z" />
        </el-form-item>
        <el-form-item label="维护窗口结束">
          <el-input v-model="form.maintenanceWindowEnd" placeholder="RFC3339，例如 2026-09-12T02:00:00Z" />
        </el-form-item>
      </div>
    </el-form>
    <el-empty v-else description="未找到策略" />
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :disabled="!policy" @click="submit">保存策略</el-button>
    </template>
  </el-dialog>
</template>
