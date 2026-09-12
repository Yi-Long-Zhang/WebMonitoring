<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import { ElMessage } from "element-plus";
import { useRouter } from "vue-router";
import Sidebar from "./Sidebar.vue";
import Topbar from "./Topbar.vue";
import OverviewPage from "../../pages/OverviewPage.vue";
import AssetsPage from "../../pages/AssetsPage.vue";
import BaselinesPage from "../../pages/BaselinesPage.vue";
import IncidentsPage from "../../pages/IncidentsPage.vue";
import JobsPage from "../../pages/JobsPage.vue";
import EvidencePage from "../../pages/EvidencePage.vue";
import ChannelsPage from "../../pages/ChannelsPage.vue";
import AuditsPage from "../../pages/AuditsPage.vue";
import PolicyDialog from "../monitoring/PolicyDialog.vue";
import { useMonitoringActions } from "../../features/monitoring/actions";
import { useSessionStore } from "../../stores/session";
import { useConsoleStore } from "../../stores/console";
import { connectEventStream } from "../../services/events/stream";
import type { AlertChannel, Asset, AssetType, Incident } from "../../types/domain";
import type { View } from "../../types/views";

const router = useRouter();
const session = useSessionStore();
const data = useConsoleStore();
const actions = useMonitoringActions();
const activeView = ref<View>("overview");
const showAssetDialog = ref(false);
const showChannelDialog = ref(false);
const showPolicyDialog = ref(false);
const policyAsset = ref<Asset>();
const assetForm = ref<{ name: string; url: string; type: AssetType }>({ name: "", url: "", type: "OFFICIAL" });
const channelForm = ref<{ name: string; type: AlertChannel["type"]; url: string; secret: string; enabled: boolean }>({
  name: "", type: "WECHAT_WORK", url: "", secret: "", enabled: true,
});
let eventStream: EventSource | undefined;

const assetMap = computed(() => new Map(data.assets.map((asset) => [asset.id, asset])));
const roleLabel = computed(() => ({ ADMIN: "平台管理员", OPERATOR: "安全运营", AUDITOR: "审计只读" }[session.user?.role || "AUDITOR"]));
const editingPolicy = computed(() => policyAsset.value ? data.policies.find((policy) => policy.assetId === policyAsset.value?.id) : undefined);
const viewTitle = computed(() => ({
  overview: "安全总览", assets: "资产中心", baselines: "基线审批", incidents: "事件中心",
  jobs: "检测任务", evidence: "证据中心", channels: "告警渠道", audits: "审计日志",
}[activeView.value]));

async function refresh() {
  try { await data.refresh(); } catch (error) { ElMessage.error(error instanceof Error ? error.message : "加载失败"); }
}
function reportError(error: unknown, fallback: string) { ElMessage.error(error instanceof Error ? error.message : fallback); }
async function createAsset() {
  try { await actions.createNewAsset(assetForm.value); showAssetDialog.value = false; assetForm.value = { name: "", url: "", type: "OFFICIAL" }; } catch (error) { reportError(error, "创建资产失败"); }
}
async function verify(asset: Asset) { try { await actions.verifyAssetOwnership(asset); } catch (error) { reportError(error, "验证失败"); } }
async function toggle(asset: Asset) { try { await actions.toggleMonitoring(asset); } catch (error) { reportError(error, "更新资产失败"); } }
async function detect(asset: Asset, mode: "HTTP" | "BROWSER") { try { await actions.startDetection(asset, mode); activeView.value = "jobs"; } catch (error) { reportError(error, "检测启动失败"); } }
async function review(id: string, accepted: boolean) { try { await actions.reviewBaseline(id, accepted); } catch (error) { reportError(error, "基线操作失败"); } }
async function retireBaseline(id: string) { try { await actions.retireBaseline(id); } catch (error) { reportError(error, "基线废弃失败"); } }
async function transition(incident: Incident, status: Incident["status"]) { try { await actions.changeIncident(incident, status); } catch (error) { reportError(error, "事件更新失败"); } }
function editPolicy(asset: Asset) {
  policyAsset.value = asset;
  showPolicyDialog.value = true;
}
async function savePolicy(input: { httpIntervalSeconds: number; browserIntervalSeconds: number; timeoutSeconds: number; maxResponseBytes: number; ignoreSelectors: string[]; trustedDomains: string[]; maintenanceWindowStart?: string; maintenanceWindowEnd?: string }) {
  if (!policyAsset.value) return;
  try {
    await actions.savePolicy(policyAsset.value, input);
    showPolicyDialog.value = false;
  } catch (error) {
    reportError(error, "策略更新失败");
  }
}
async function createChannel() {
  try { await actions.createNewAlertChannel(channelForm.value); showChannelDialog.value = false; channelForm.value = { name: "", type: "WECHAT_WORK", url: "", secret: "", enabled: true }; } catch (error) { reportError(error, "创建告警渠道失败"); }
}
async function signOut() { await session.signOut(); eventStream?.close(); await router.replace("/login"); }

onMounted(async () => {
  if (!session.isAuthenticated && !(await session.restore())) { await router.replace("/login"); return; }
  await refresh();
  eventStream = connectEventStream(refresh);
});
onUnmounted(() => eventStream?.close());
</script>

<template>
  <div v-if="session.user" class="app-shell">
    <Sidebar :active-view="activeView" :username="session.user.username" :role-label="roleLabel" :candidate-count="data.baselines.filter((item) => item.status === 'CANDIDATE').length" :open-incident-count="data.dashboard?.openIncidents || 0" @navigate="activeView = $event" @sign-out="signOut" />
    <main class="main-content">
      <Topbar :title="viewTitle" :loading="data.loading" @refresh="refresh" />
      <OverviewPage v-if="activeView === 'overview'" :dashboard="data.dashboard" :report-summary="data.reportSummary" :assets="data.assets" :incidents="data.incidents" :asset-map="assetMap" @navigate="activeView = $event" />
      <AssetsPage v-else-if="activeView === 'assets'" :assets="data.assets" :policies="data.policies" :can-operate="session.canOperate" @create="showAssetDialog = true" @verify="verify" @toggle="toggle" @detect="detect" @policy="editPolicy" />
      <BaselinesPage v-else-if="activeView === 'baselines'" :baselines="data.baselines" :asset-map="assetMap" :can-operate="session.canOperate" @review="review" @retire="retireBaseline" />
      <IncidentsPage v-else-if="activeView === 'incidents'" :incidents="data.incidents" :evidence="data.evidence" :deliveries="data.alertDeliveries" :asset-map="assetMap" :can-operate="session.canOperate" @transition="transition" />
      <JobsPage v-else-if="activeView === 'jobs'" :jobs="data.jobs" :asset-map="assetMap" />
      <EvidencePage v-else-if="activeView === 'evidence'" :evidence="data.evidence" :assets="data.assets" :asset-map="assetMap" />
      <ChannelsPage v-else-if="activeView === 'channels'" :channels="data.alertChannels" :can-admin="session.canAdmin" @create="showChannelDialog = true" />
      <AuditsPage v-else :audits="data.audits" />
    </main>
    <el-dialog v-model="showAssetDialog" title="登记监测资产" width="520px">
      <el-form label-position="top"><el-form-item label="资产名称"><el-input v-model="assetForm.name" placeholder="例如：官方网站首页" /></el-form-item><el-form-item label="页面 URL"><el-input v-model="assetForm.url" placeholder="https://example.com/" /></el-form-item><el-form-item label="页面类型"><el-select v-model="assetForm.type" style="width: 100%"><el-option label="官网页面" value="OFFICIAL" /><el-option label="业务前台" value="BUSINESS" /><el-option label="后台登录页" value="LOGIN" /><el-option label="其他重点页面" value="OTHER" /></el-select></el-form-item></el-form>
      <template #footer><el-button @click="showAssetDialog = false">取消</el-button><el-button type="primary" @click="createAsset">创建并授权</el-button></template>
    </el-dialog>
    <PolicyDialog v-model="showPolicyDialog" :asset="policyAsset" :policy="editingPolicy" @save="savePolicy" />
    <el-dialog v-model="showChannelDialog" title="新增告警渠道" width="520px">
      <el-form label-position="top"><el-form-item label="渠道名称"><el-input v-model="channelForm.name" placeholder="例如：安全运营群" /></el-form-item><el-form-item label="渠道类型"><el-radio-group v-model="channelForm.type"><el-radio-button value="WECHAT_WORK">企业微信</el-radio-button><el-radio-button value="WEBHOOK">Webhook</el-radio-button></el-radio-group></el-form-item><el-form-item label="通知地址"><el-input v-model="channelForm.url" type="password" show-password autocomplete="off" placeholder="https://..." /></el-form-item><el-form-item v-if="channelForm.type === 'WEBHOOK'" label="签名密钥"><el-input v-model="channelForm.secret" type="password" show-password autocomplete="new-password" /></el-form-item><el-form-item label="创建后启用"><el-switch v-model="channelForm.enabled" /></el-form-item></el-form>
      <template #footer><el-button @click="showChannelDialog = false">取消</el-button><el-button type="primary" @click="createChannel">创建渠道</el-button></template>
    </el-dialog>
  </div>
</template>
