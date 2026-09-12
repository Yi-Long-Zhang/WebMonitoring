<script setup lang="ts">
import { ref, watch } from "vue";
import type { Asset, Policy } from "../../types/domain";

type PolicyInput = Pick<Policy, "httpIntervalSeconds" | "browserIntervalSeconds" | "timeoutSeconds" | "maxResponseBytes" | "ignoreSelectors" | "trustedDomains" | "maintenanceWindowStart" | "maintenanceWindowEnd">;
type FormState = Omit<PolicyInput, "ignoreSelectors" | "trustedDomains"> & { ignoreSelectors: string; trustedDomains: string };

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
  };
}, { immediate: true });

function lines(value: string) {
  return value.split(/\r?\n/).map((item) => item.trim()).filter(Boolean);
}

function submit() {
  emit("save", {
    ...form.value,
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
