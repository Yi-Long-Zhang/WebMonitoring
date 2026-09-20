<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { useRoute, useRouter } from "vue-router";
import Sidebar from "./Sidebar.vue";
import Topbar from "./Topbar.vue";
import OverviewPage from "../../pages/OverviewPage.vue";
import AssetsPage from "../../pages/AssetsPage.vue";
import SitesPage from "../../pages/SitesPage.vue";
import BaselinesPage from "../../pages/BaselinesPage.vue";
import IncidentsPage from "../../pages/IncidentsPage.vue";
import JobsPage from "../../pages/JobsPage.vue";
import EvidencePage from "../../pages/EvidencePage.vue";
import ChannelsPage from "../../pages/ChannelsPage.vue";
import AuditsPage from "../../pages/AuditsPage.vue";
import TenantsPage from "../../pages/TenantsPage.vue";
import PolicyDialog from "../monitoring/PolicyDialog.vue";
import AssetImportDialog from "../monitoring/AssetImportDialog.vue";
import { useMonitoringActions } from "../../features/monitoring/actions";
import { exportAssets } from "../../services/api/client";
import { saveBlob } from "../../services/download";
import { roleLabel as roleLabelFor } from "../../app/permissions";
import { CHANNEL_LABELS, channelNeeds } from "../../app/channels";
import { useSessionStore } from "../../stores/session";
import { useConsoleStore } from "../../stores/console";
import { connectEventStream } from "../../services/events/stream";
import type { AlertChannel, Asset, AssetType, DetectionJob, Incident, Site, Tenant } from "../../types/domain";
import type { View } from "../../types/views";
import { isView } from "../../types/views";

const router = useRouter();
const route = useRoute();
const session = useSessionStore();
const data = useConsoleStore();
const actions = useMonitoringActions();
const activeView = ref<View>("overview");
const showAssetDialog = ref(false);
const showImportDialog = ref(false);
const showChannelDialog = ref(false);
const showPolicyDialog = ref(false);
const showSiteDialog = ref(false);
const showTenantDialog = ref(false);
const policyAsset = ref<Asset>();
const editingSite = ref<Site>();
const editingTenant = ref<Tenant>();
const assetForm = ref<{ name: string; url: string; type: AssetType; siteId: string }>({ name: "", url: "", type: "OFFICIAL", siteId: "" });
const channelForm = ref<{ name: string; type: AlertChannel["type"]; url: string; recipients: string; secret: string; enabled: boolean }>({
  name: "", type: "WECHAT_WORK", url: "", recipients: "", secret: "", enabled: true,
});
const siteForm = ref<{ name: string; domain: string }>({ name: "", domain: "" });
const tenantForm = ref<{ name: string; slug: string; adminUsername: string; adminPassword: string }>({
  name: "", slug: "", adminUsername: "admin", adminPassword: "",
});
let eventStream: EventSource | undefined;

const assetMap = computed(() => new Map(data.assets.map((asset) => [asset.id, asset])));
const roleLabel = computed(() => roleLabelFor(session.user?.role));
const editingPolicy = computed(() => policyAsset.value ? data.policies.find((policy) => policy.assetId === policyAsset.value?.id) : undefined);
const viewTitle = computed(() => ({
  overview: "安全总览", assets: "资产中心", sites: "站点管理", baselines: "基线审批", incidents: "事件中心",
  jobs: "检测任务", evidence: "证据中心", channels: "告警渠道", audits: "审计日志", tenants: "租户管理",
}[activeView.value]));

async function refresh() {
  try { await data.refresh(); } catch (error) { ElMessage.error(error instanceof Error ? error.message : "加载失败"); }
}
function reportError(error: unknown, fallback: string) { ElMessage.error(error instanceof Error ? error.message : fallback); }
async function createAsset() {
  try { await actions.createNewAsset(assetForm.value); showAssetDialog.value = false; assetForm.value = { name: "", url: "", type: "OFFICIAL", siteId: "" }; } catch (error) { reportError(error, "创建资产失败"); }
}
// The hash owns which view is on screen. navigate() only writes the route, and
// the watcher below is the only reader that touches activeView, so the sidebar,
// a deep link and the back button cannot end up disagreeing with the address
// bar about what is rendered.
async function selectView(view: View) {
  activeView.value = view;
  if (view === "tenants" && session.canSuperAdmin) {
    try { await data.loadTenants(); } catch (error) { reportError(error, "加载租户失败"); }
  }
}
async function syncFromRoute() {
  const raw = route.params.view;
  const known = isView(raw);
  // A segment the app does not know is normalized away rather than left in the
  // address bar describing a view that is not rendered.
  if (raw !== undefined && !known) await router.replace("/");
  await selectView(known ? raw : "overview");
}
async function navigate(view: View) {
  const target = view === "overview" ? "/" : `/${view}`;
  if (route.path !== target) await router.replace(target);
}
watch(() => route.params.view, () => { void syncFromRoute(); });
function openSiteCreate() { editingSite.value = undefined; siteForm.value = { name: "", domain: "" }; showSiteDialog.value = true; }
function openSiteEdit(site: Site) { editingSite.value = site; siteForm.value = { name: site.name, domain: site.domain }; showSiteDialog.value = true; }
async function submitSite() {
  try {
    if (editingSite.value) await actions.saveSite(editingSite.value, siteForm.value);
    else await actions.createNewSite(siteForm.value);
    showSiteDialog.value = false;
  } catch (error) { reportError(error, "保存站点失败"); }
}
function openTenantCreate() { editingTenant.value = undefined; tenantForm.value = { name: "", slug: "", adminUsername: "admin", adminPassword: "" }; showTenantDialog.value = true; }
function openTenantEdit(tenant: Tenant) { editingTenant.value = tenant; tenantForm.value = { name: tenant.name, slug: tenant.slug, adminUsername: "", adminPassword: "" }; showTenantDialog.value = true; }
async function submitTenant() {
  try {
    if (editingTenant.value) await actions.saveTenant(editingTenant.value, { name: tenantForm.value.name, slug: tenantForm.value.slug });
    else await actions.createNewTenant(tenantForm.value);
    showTenantDialog.value = false;
  } catch (error) { reportError(error, "保存租户失败"); }
}
async function verify(asset: Asset) { try { await actions.verifyAssetOwnership(asset); } catch (error) { reportError(error, "验证失败"); } }
async function toggle(asset: Asset) { try { await actions.toggleMonitoring(asset); } catch (error) { reportError(error, "更新资产失败"); } }
async function detect(asset: Asset, mode: "HTTP" | "BROWSER") { try { await actions.startDetection(asset, mode); await navigate("jobs"); } catch (error) { reportError(error, "检测启动失败"); } }
async function review(id: string, accepted: boolean) { try { await actions.reviewBaseline(id, accepted); } catch (error) { reportError(error, "基线操作失败"); } }
async function retireBaseline(id: string) { try { await actions.retireBaseline(id); } catch (error) { reportError(error, "基线废弃失败"); } }
async function toggleTenant(tenant: Tenant, disabled: boolean) { try { await actions.setTenantDisabled(tenant, disabled); } catch (error) { reportError(error, "切换租户状态失败"); } }
async function cancelDetectionJob(job: DetectionJob) { try { await actions.cancelDetectionJob(job); } catch (error) { reportError(error, "任务取消失败"); } }
// Bumped after a write to an incident lands, so the drawer's timeline reloads
// behind it rather than racing it. Owned here because this is where the writes
// complete.
const timelineKey = ref(0);
async function transition(incident: Incident, status: Incident["status"], ownerId = "") { try { await actions.changeIncident(incident, status, ownerId); timelineKey.value += 1; } catch (error) { reportError(error, "事件更新失败"); } }
async function comment(incident: Incident, note: string) { try { await actions.commentOnIncident(incident, note); timelineKey.value += 1; } catch (error) { reportError(error, "备注提交失败"); } }
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
  const form = channelForm.value;
  // Only the fields this channel type actually uses are transmitted. The dialog
  // keeps whatever was typed when the operator switches type, and sending a stale
  // URL or recipient list would hand the server credentials the chosen type does
  // not use.
  try {
    await actions.createNewAlertChannel({
      name: form.name,
      type: form.type,
      url: channelNeeds(form.type, "url") ? form.url : "",
      recipients: channelNeeds(form.type, "recipients") ? form.recipients : "",
      secret: channelNeeds(form.type, "secret") ? form.secret : "",
      enabled: form.enabled,
    });
    showChannelDialog.value = false;
    channelForm.value = { name: "", type: "WECHAT_WORK", url: "", recipients: "", secret: "", enabled: true };
  } catch (error) { reportError(error, "创建告警渠道失败"); }
}
async function signOut() { await session.signOut(); eventStream?.close(); await router.replace("/login"); }

// The server writes the CSV so field escaping has one implementation; the export
// is an operator-initiated snapshot, so it is named with the date it was taken.
async function exportAssetList() {
  try {
    const blob = await exportAssets("csv");
    const stamp = new Date().toISOString().slice(0, 10);
    saveBlob(blob, `assets-${stamp}.csv`);
  } catch (error) { reportError(error, "导出失败"); }
}
async function onImported() { showImportDialog.value = false; await data.reloadAssets(); }

onMounted(async () => {
  if (!session.isAuthenticated && !(await session.restore())) { await router.replace("/login"); return; }
  await refresh();
  if (session.canSuperAdmin) {
    try { await data.loadTenants(); } catch { /* best effort; tenants also reload on navigation */ }
  }
  // The incident drawer assigns an owner, so operators need the user list too.
  if (session.canOperate) {
    try { await data.loadUsers(); } catch { /* best effort; the drawer still works without it */ }
  }
  // Applied only after the session is restored, because the per-view loads it
  // triggers are role-gated and there is no role to check until then.
  await syncFromRoute();
  eventStream = connectEventStream(refresh);
});
onUnmounted(() => eventStream?.close());
</script>

<template>
  <div v-if="session.user" class="app-shell">
    <Sidebar :active-view="activeView" :username="session.user.username" :role="session.user.role" :role-label="roleLabel" :candidate-count="data.baselines.filter((item) => item.status === 'CANDIDATE').length" :open-incident-count="data.dashboard?.openIncidents || 0" @navigate="navigate" @sign-out="signOut" />
    <main class="main-content">
      <Topbar :title="viewTitle" :loading="data.loading" @refresh="refresh" />
      <OverviewPage v-if="activeView === 'overview'" :dashboard="data.dashboard" :report-summary="data.reportSummary" :assets="data.assets" :incidents="data.incidents" :asset-map="assetMap" @navigate="navigate" />
      <AssetsPage v-else-if="activeView === 'assets'" :assets="data.assets" :policies="data.policies" :can-operate="session.canOperate" :can-admin="session.canAdmin" :has-more="data.hasMore('assets')" :loading-more="data.isLoadingMore('assets')" @load-more="data.loadMore('assets')" @create="showAssetDialog = true" @verify="verify" @toggle="toggle" @detect="detect" @policy="editPolicy" @import="showImportDialog = true" @export="exportAssetList" />
      <SitesPage v-else-if="activeView === 'sites'" :sites="data.sites" :can-admin="session.canAdmin" :has-more="data.hasMore('sites')" :loading-more="data.isLoadingMore('sites')" @load-more="data.loadMore('sites')" @create="openSiteCreate" @edit="openSiteEdit" />
      <BaselinesPage v-else-if="activeView === 'baselines'" :baselines="data.baselines" :asset-map="assetMap" :can-operate="session.canOperate" :has-more="data.hasMore('baselines')" :loading-more="data.isLoadingMore('baselines')" @load-more="data.loadMore('baselines')" @review="review" @retire="retireBaseline" />
      <IncidentsPage v-else-if="activeView === 'incidents'" :incidents="data.incidents" :evidence="data.evidence" :deliveries="data.alertDeliveries" :asset-map="assetMap" :can-operate="session.canOperate" :timeline-key="timelineKey" :users="data.users" :on-comment="comment" :has-more="data.hasMore('incidents')" :loading-more="data.isLoadingMore('incidents')" @load-more="data.loadMore('incidents')" @transition="transition" />
      <JobsPage v-else-if="activeView === 'jobs'" :jobs="data.jobs" :asset-map="assetMap" :can-operate="session.canOperate" :has-more="data.hasMore('jobs')" :loading-more="data.isLoadingMore('jobs')" @load-more="data.loadMore('jobs')" @cancel="cancelDetectionJob" />
      <EvidencePage v-else-if="activeView === 'evidence'" :evidence="data.evidence" :assets="data.assets" :asset-map="assetMap" :has-more="data.hasMore('evidence')" :loading-more="data.isLoadingMore('evidence')" @load-more="data.loadMore('evidence')" />
      <ChannelsPage v-else-if="activeView === 'channels'" :channels="data.alertChannels" :can-admin="session.canAdmin" @create="showChannelDialog = true" />
      <TenantsPage v-else-if="activeView === 'tenants'" :tenants="data.tenants" :self-tenant-id="session.user?.tenantId || ''" :has-more="data.hasMore('tenants')" :loading-more="data.isLoadingMore('tenants')" @load-more="data.loadMore('tenants')" @create="openTenantCreate" @edit="openTenantEdit" @toggle="toggleTenant" />
      <!-- Explicit rather than a v-else fallback: with v-else any view key that
           matched nothing above would render the audit page, which is how an
           unknown hash segment used to reach the screen silently. -->
      <AuditsPage v-else-if="activeView === 'audits'" :audits="data.audits" :has-more="data.hasMore('audits')" :loading-more="data.isLoadingMore('audits')" @load-more="data.loadMore('audits')" @filter="data.applyAuditFilters" />
    </main>
    <el-dialog v-model="showAssetDialog" title="登记监测资产" width="520px">
      <el-form label-position="top">
        <el-form-item label="渠道名称"><el-input v-model="channelForm.name" placeholder="例如：安全运营群" /></el-form-item>
        <el-form-item label="渠道类型"><el-radio-group v-model="channelForm.type"><el-radio-button v-for="(label, value) in CHANNEL_LABELS" :key="value" :value="value">{{ label }}</el-radio-button></el-radio-group></el-form-item>
        <!-- Which fields this type needs comes from app/channels.ts, which mirrors
             the server's createAlertChannel validation. EMAIL takes a recipient
             list and no URL; the webhook-shaped types take a URL, and WEBHOOK and
             DINGTALK also sign with a secret. -->
        <el-form-item v-if="channelNeeds(channelForm.type, 'url')" label="通知地址"><el-input v-model="channelForm.url" type="password" show-password autocomplete="off" placeholder="https://..." /></el-form-item>
        <el-form-item v-if="channelNeeds(channelForm.type, 'recipients')" label="收件人"><el-input v-model="channelForm.recipients" placeholder="ops@example.com, oncall@example.com（逗号分隔）" /></el-form-item>
        <el-form-item v-if="channelNeeds(channelForm.type, 'secret')" label="签名密钥"><el-input v-model="channelForm.secret" type="password" show-password autocomplete="new-password" /></el-form-item>
        <el-form-item label="创建后启用"><el-switch v-model="channelForm.enabled" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="showAssetDialog = false">取消</el-button><el-button type="primary" @click="createAsset">创建并授权</el-button></template>
    </el-dialog>
    <PolicyDialog v-model="showPolicyDialog" :asset="policyAsset" :policy="editingPolicy" @save="savePolicy" />
    <AssetImportDialog v-if="showImportDialog" @imported="onImported" @close="showImportDialog = false" />
    <el-dialog v-model="showChannelDialog" title="新增告警渠道" width="520px">
      <el-form label-position="top"><el-form-item label="渠道名称"><el-input v-model="channelForm.name" placeholder="例如：安全运营群" /></el-form-item><el-form-item label="渠道类型"><el-radio-group v-model="channelForm.type"><el-radio-button value="WECHAT_WORK">企业微信</el-radio-button><el-radio-button value="WEBHOOK">Webhook</el-radio-button></el-radio-group></el-form-item><el-form-item label="通知地址"><el-input v-model="channelForm.url" type="password" show-password autocomplete="off" placeholder="https://..." /></el-form-item><el-form-item v-if="channelForm.type === 'WEBHOOK'" label="签名密钥"><el-input v-model="channelForm.secret" type="password" show-password autocomplete="new-password" /></el-form-item><el-form-item label="创建后启用"><el-switch v-model="channelForm.enabled" /></el-form-item></el-form>
      <template #footer><el-button @click="showChannelDialog = false">取消</el-button><el-button type="primary" @click="createChannel">创建渠道</el-button></template>
    </el-dialog>
    <el-dialog v-model="showSiteDialog" :title="editingSite ? '编辑站点' : '新增站点'" width="520px">
      <el-form label-position="top">
        <el-form-item label="站点名称"><el-input v-model="siteForm.name" placeholder="例如：官方网站群" /></el-form-item>
        <el-form-item label="域名"><el-input v-model="siteForm.domain" placeholder="example.com" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="showSiteDialog = false">取消</el-button><el-button type="primary" @click="submitSite">保存站点</el-button></template>
    </el-dialog>
    <el-dialog v-model="showTenantDialog" :title="editingTenant ? '编辑租户' : '新增租户'" width="520px">
      <el-form label-position="top">
        <el-form-item label="租户名称"><el-input v-model="tenantForm.name" placeholder="例如：某事业部" /></el-form-item>
        <el-form-item label="租户标识（slug）"><el-input v-model="tenantForm.slug" placeholder="小写字母、数字或连字符" /></el-form-item>
        <template v-if="!editingTenant">
          <el-form-item label="初始管理员账号"><el-input v-model="tenantForm.adminUsername" autocomplete="off" /></el-form-item>
          <el-form-item label="初始管理员密码"><el-input v-model="tenantForm.adminPassword" type="password" show-password autocomplete="new-password" /></el-form-item>
        </template>
      </el-form>
      <template #footer><el-button @click="showTenantDialog = false">取消</el-button><el-button type="primary" @click="submitTenant">保存租户</el-button></template>
    </el-dialog>
  </div>
</template>
