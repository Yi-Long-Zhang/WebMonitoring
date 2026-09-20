<script setup lang="ts">
import { computed, ref } from "vue";
import { ElMessage } from "element-plus";
import { importAssets } from "../../services/api/client";
import type { AssetType } from "../../types/domain";

const emit = defineEmits<{ imported: []; close: [] }>();

// The parser below is deliberately small: it accepts the JSON the export and the
// docs describe, and a plain headerless CSV of name,url[,type]. Everything it
// cannot read is reported as a row-level rejection here, so an operator fixing a
// 500-row file is told which lines the browser could not even parse instead of
// getting one opaque failure.
const TYPE_CODES: Record<string, AssetType> = { OFFICIAL: "OFFICIAL", BUSINESS: "BUSINESS", LOGIN: "LOGIN", OTHER: "OTHER" };
const maxRows = 500;

type Draft = { name: string; url: string; type: AssetType };
type RowResult = { index: number; name?: string; url?: string; status: "CREATED" | "VALIDATED" | "REJECTED"; code?: string; message?: string };

const text = ref("");
const parsed = ref<Draft[]>([]);
const parseErrors = ref<RowResult[]>([]);
const results = ref<RowResult[]>([]);
const validated = ref(false);
const submitting = ref(false);
const notice = ref("");

const canValidate = computed(() => parsed.value.length > 0 && !submitting.value);
const createdCount = computed(() => results.value.filter((row) => row.status === "CREATED").length);
const rejectedCount = computed(() => results.value.filter((row) => row.status === "REJECTED").length);
// Rows the parser refused are shown alongside the server's verdicts, so the
// table an operator reads is the whole file and not just the part that reached
// the API.
const displayRows = computed(() => [...parseErrors.value, ...results.value]);

// CSV cells may be quoted and may contain the delimiter or a newline. Splitting
// on commas would silently mis-parse a URL that contains one, and a name with a
// comma is ordinary. Only the quoting rules the export side emits are handled.
function parseCsvLine(line: string): string[] {
  const cells: string[] = [];
  let cell = "";
  let quoted = false;
  for (let i = 0; i < line.length; i += 1) {
    const char = line[i];
    if (quoted) {
      if (char !== '"') cell += char;
      else if (line[i + 1] === '"') { cell += '"'; i += 1; }
      else quoted = false;
    } else if (char === '"') quoted = true;
    else if (char === ",") { cells.push(cell); cell = ""; }
    else cell += char;
  }
  cells.push(cell);
  return cells.map((value) => value.trim());
}

function asType(value: string | undefined): AssetType {
  return TYPE_CODES[(value ?? "").toUpperCase()] ?? "OFFICIAL";
}

function reset() {
  results.value = [];
  parseErrors.value = [];
  validated.value = false;
  notice.value = "";
}

function validate() {
  reset();
  const body = text.value.trim();
  if (!body) { notice.value = "请先粘贴 JSON 或 CSV 内容"; return; }
  const drafts: Draft[] = [];
  const errors: RowResult[] = [];
  if (body.startsWith("[")) {
    let parsedJson: unknown;
    try {
      parsedJson = JSON.parse(body);
    } catch {
      notice.value = "JSON 格式无法解析，请检查括号与引号";
      return;
    }
    if (!Array.isArray(parsedJson)) { notice.value = "JSON 顶层必须是数组"; return; }
    parsedJson.forEach((entry, index) => {
      const row = entry as Partial<Draft>;
      if (!row || typeof row.name !== "string" || typeof row.url !== "string") {
        errors.push({ index, status: "REJECTED", code: "BAD_ROW", message: "每行必须包含 name 和 url 字符串" });
        return;
      }
      drafts.push({ name: row.name, url: row.url, type: asType(row.type) });
    });
  } else {
    const lines = body.split(/\r?\n/).filter((line) => line.trim() !== "");
    lines.forEach((line, index) => {
      const cells = parseCsvLine(line);
      const isHeader = index === 0 && cells[0]?.toLowerCase() === "name";
      if (isHeader) return;
      if (cells.length < 2 || !cells[0] || !cells[1]) {
        errors.push({ index, url: cells[1] ?? "", status: "REJECTED", code: "BAD_ROW", message: "每行至少需要 name,url 两列" });
        return;
      }
      drafts.push({ name: cells[0], url: cells[1], type: asType(cells[2]) });
    });
  }
  if (drafts.length > maxRows) { notice.value = `单次最多导入 ${maxRows} 行，当前 ${drafts.length} 行`; return; }
  if (drafts.length === 0 && errors.length === 0) { notice.value = "没有解析到可导入的行"; return; }
  parsed.value = drafts;
  parseErrors.value = errors;
  validated.value = true;
}

// dryRun first on purpose: the server runs the same per-row builder it would run
// for a real import, so the preview shows the authorization and URL checks that
// would actually reject a row. A preview that only checked the shape of the
// input would green-light rows that fail the moment they are submitted.
async function preview() {
  if (!canValidate.value) return;
  submit(true);
}

async function submit(dryRun: boolean) {
  submitting.value = true;
  notice.value = "";
  try {
    const answer = await importAssets({ assets: parsed.value, dryRun });
    results.value = answer.results;
    if (dryRun) {
      notice.value = `预检完成：${answer.results.filter((row) => row.status === "VALIDATED").length} 行可导入。确认后写入。`;
      return;
    }
    ElMessage.success(`已导入 ${answer.created} 个资产`);
    emit("imported");
  } catch (error) {
    notice.value = error instanceof Error ? error.message : "导入失败";
  } finally {
    submitting.value = false;
  }
}

function onPaste(value: string) { text.value = value; reset(); }
</script>

<template>
  <el-dialog :model-value="true" title="批量导入资产" width="720px" @close="emit('close')">
    <el-form label-position="top">
      <el-form-item label="粘贴内容">
        <el-input :model-value="text" type="textarea" :rows="6" placeholder="JSON：[{&quot;name&quot;:&quot;官网首页&quot;,&quot;url&quot;:&quot;https://example.com&quot;,&quot;type&quot;:&quot;OFFICIAL&quot;}]&#10;CSV：name,url,type" @update:model-value="onPaste" />
      </el-form-item>
    </el-form>
    <p class="muted">CSV 每行 name,url,type 三列，type 省略时为 OFFICIAL。导出的 CSV 前两列可直接粘回。</p>
    <p v-if="notice" class="muted">{{ notice }}</p>

    <el-table v-if="displayRows.length" :data="displayRows" max-height="260" empty-text="没有结果">
      <el-table-column prop="index" label="行" width="60" />
      <el-table-column label="资产" min-width="200"><template #default="{ row }"><strong>{{ row.name || "—" }}</strong><small class="table-sub">{{ row.url }}</small></template></el-table-column>
      <el-table-column label="结果" width="110"><template #default="{ row }"><el-tag :type="row.status === 'REJECTED' ? 'danger' : row.status === 'CREATED' ? 'success' : 'info'" effect="plain">{{ row.status }}</el-tag></template></el-table-column>
      <el-table-column label="说明" min-width="200"><template #default="{ row }">{{ row.message || "—" }}</template></el-table-column>
    </el-table>
    <p v-if="results.length" class="muted">写入 {{ createdCount }} 行，拒绝 {{ rejectedCount }} 行。被拒绝的行不会写入，其余行照常导入。</p>

    <template #footer>
      <el-button @click="emit('close')">取消</el-button>
      <el-button :disabled="!canValidate" :loading="submitting" @click="validate">解析</el-button>
      <el-button :disabled="!validated || submitting" :loading="submitting" @click="preview">预检</el-button>
      <el-button type="primary" :disabled="!validated || submitting" :loading="submitting" @click="submit(false)">确认导入</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.table-sub { display: block; color: var(--wm-muted); font-size: 12px; word-break: break-all; }
.muted { color: var(--wm-muted); font-size: 12px; }
</style>
