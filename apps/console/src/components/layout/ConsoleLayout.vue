<script setup lang="ts">
import { computed, defineAsyncComponent, onMounted, onUnmounted, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { useRoute, useRouter } from "vue-router";
import Sidebar from "./Sidebar.vue";
import Topbar from "./Topbar.vue";
// The views are loaded on demand. This component imports all twelve of them,
// and ConsolePage imports this one, so static imports put every page -- plus
// ECharts, which only the overview uses -- in the chunk the first signed-in
// paint waits for, whether or not the operator ever opens that view.
const OverviewPage = defineAsyncComponent(() => import("../../pages/OverviewPage.vue"));
const AssetsPage = defineAsyncComponent(() => import("../../pages/AssetsPage.vue"));
const SitesPage = defineAsyncComponent(() => import("../../pages/SitesPage.vue"));
const BaselinesPage = defineAsyncComponent(() => import("../../pages/BaselinesPage.vue"));
const IncidentsPage = defineAsyncComponent(() => import("../../pages/IncidentsPage.vue"));
const JobsPage = defineAsyncComponent(() => import("../../pages/JobsPage.vue"));
const EvidencePage = defineAsyncComponent(() => import("../../pages/EvidencePage.vue"));
const ChannelsPage = defineAsyncComponent(() => import("../../pages/ChannelsPage.vue"));
const AuditsPage = defineAsyncComponent(() => import("../../pages/AuditsPage.vue"));
const TenantsPage = defineAsyncComponent(() => import("../../pages/TenantsPage.vue"));
const UsersPage = defineAsyncComponent(() => import("../../pages/UsersPage.vue"));
import PolicyDialog from "../monitoring/PolicyDialog.vue";
import AssetImportDialog from "../monitoring/AssetImportDialog.vue";
import BaselineSnapshotDrawer from "../monitoring/BaselineSnapshotDrawer.vue";
import { useMonitoringActions } from "../../features/monitoring/actions";
import { ApiError, exportAssets, exportAuditEvents, exportIncidents, getBaseline } from "../../services/api/client";
import type { AuditFilterParams, IncidentFilterParams } from "../../services/api/client";
import { saveBlob } from "../../services/download";
import { roleLabel as roleLabelFor } from "../../app/permissions";
import { channelNeeds } from "../../app/channels";
import { useSessionStore } from "../../stores/session";
import { useConsoleStore } from "../../stores/console";
import { connectEventStream } from "../../services/events/stream";
import type { AlertChannel, Asset, AssetType, Baseline, DetectionJob, Incident, Site, Tenant, User } from "../../types/domain";
import type { View } from "../../types/views";
import { isView } from "../../types/views";

const router = useRouter();
const route = useRoute();
const session = useSessionStore();
const data = useConsoleStore();
const actions = useMonitoringActions();
// Seeded from the hash rather than defaulted to the overview: the views are
// async components, so a default of "overview" that is corrected a moment later
// downloads the overview's chunk -- ECharts, 1.1 MB -- on every deep link,
// which is exactly the cost the split exists to avoid. The route is already
// resolved when this component first renders (the router-view only renders it
// after the initial navigation), so the segment is readable here.
const activeView = ref<View>(isView(route.params.view) ? route.params.view : "overview");
const showAssetDialog = ref(false);
const showImportDialog = ref(false);
const showChannelDialog = ref(false);
const editingChannel = ref<AlertChannel>();
const deletingChannel = ref<AlertChannel>();
const showPolicyDialog = ref(false);
const showSiteDialog = ref(false);
const showTenantDialog = ref(false);
const showUserDialog = ref(false);
const showPasswordDialog = ref(false);
const showTenantDeleteDialog = ref(false);
const policyAsset = ref<Asset>();
const editingSite = ref<Site>();
const editingTenant = ref<Tenant>();
const editingUser = ref<User>();
const passwordUser = ref<User>();
const assetForm = ref<{ name: string; url: string; type: AssetType; siteId: string }>({ name: "", url: "", type: "OFFICIAL", siteId: "" });
const channelForm = ref<{ name: string; type: AlertChannel["type"]; url: string; recipients: string; secret: string; enabled: boolean }>({
  name: "", type: "WECHAT_WORK", url: "", recipients: "", secret: "", enabled: true,
});
const siteForm = ref<{ name: string; domain: string }>({ name: "", domain: "" });
const tenantForm = ref<{ name: string; slug: string; adminUsername: string; adminPassword: string }>({
  name: "", slug: "", adminUsername: "admin", adminPassword: "",
});
const userForm = ref<{ username: string; email: string; password: string; role: User["role"]; disabled: boolean }>({
  username: "", email: "", password: "", role: "OPERATOR", disabled: false,
});
const passwordForm = ref<{ password: string; confirm: string }>({ password: "", confirm: "" });
// The tenant seeded by the initial migration (internal/domain.DefaultTenantID).
// The server refuses to delete it with 409 TENANT_PROTECTED; knowing the id here
// only keeps the button from offering an operation that cannot succeed.
const DEFAULT_TENANT_ID = "00000000-0000-4000-8000-000000000001";
const deletingTenant = ref<Tenant>();
const tenantDeleteConfirm = ref("");
const tenantDeleteError = ref("");
const tenantDeleteCascade = ref(false);
const deletingTenantBusy = ref(false);
let eventStream: { close: () => void } | undefined;

const assetMap = computed(() => new Map(data.assets.map((asset) => [asset.id, asset])));
const roleLabel = computed(() => roleLabelFor(session.user?.role));
const editingPolicy = computed(() => policyAsset.value ? data.policies.find((policy) => policy.assetId === policyAsset.value?.id) : undefined);
const viewTitle = computed(() => ({
  overview: "安全总览", assets: "资产中心", sites: "站点管理", baselines: "基线审批", incidents: "事件中心",
  jobs: "检测任务", evidence: "证据中心", channels: "告警渠道", audits: "审计日志", tenants: "租户管理", users: "用户管理",
}[activeView.value]));

async function refresh() {
  try { await data.refresh(); } catch (error) { ElMessage.error(error instanceof Error ? error.message : "加载失败"); }
  // Not inside the try: the version is decoration, and a control plane that
  // cannot answer for it must not make the console report "加载失败" over data
  // that did load.
  await data.loadVersion();
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
  // The snapshot drawer belongs to the baselines view. Leaving it mounted while
  // another view renders would keep its overlay over the new page, swallowing
  // clicks meant for it -- so navigating away dismisses it.
  closeBaselinePreview();
  try {
    if (view === "tenants" && session.canSuperAdmin) await data.loadTenants();
    // Same reason as tenants: the directory is not part of refresh(), and the
    // reload after a write goes through the same call.
    if (view === "users" && session.canAdmin) await data.loadUsers();
    // The audit head page is loaded once during refresh(), so without this the
    // screen keeps showing whatever it held when the app booted -- a tenant
    // deleted later would never appear in the trail that records it. Reloaded
    // rather than refreshed: the trail is append-only, and a full refresh would
    // rescan every other list for rows this view does not show.
    if (view === "audits") await data.reloadAudits();
  } catch (error) { reportError(error, "加载失败"); }
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
async function removeSite(site: Site) { try { await actions.removeSite(site); } catch (error) { reportError(error, "删除站点失败"); } }
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

// The list row is shown immediately so the drawer opens without a blank frame,
// then replaced by the single-resource answer, which is the one that carries the
// snapshot. A row whose fetch fails keeps what the list already had rather than
// closing under the operator.
const previewBaseline = ref<Baseline>();
const previewBaselineId = ref("");
const previewLoading = ref(false);
async function openBaselinePreview(baseline: Baseline) {
  previewBaselineId.value = baseline.id;
  previewBaseline.value = baseline;
  previewLoading.value = true;
  try {
    const full = await getBaseline(baseline.id);
    if (previewBaselineId.value === baseline.id) previewBaseline.value = full;
  } catch (error) {
    reportError(error, "加载基线快照失败");
  } finally {
    previewLoading.value = false;
  }
}
function closeBaselinePreview() { previewBaselineId.value = ""; previewBaseline.value = undefined; }
async function toggleTenant(tenant: Tenant, disabled: boolean) { try { await actions.setTenantDisabled(tenant, disabled); } catch (error) { reportError(error, "切换租户状态失败"); } }

// Deletion is confirmed by typing the slug, mirroring the server's
// CONFIRM_MISMATCH check: a click-through confirm is too easy to hit for an
// operation that removes every resource under the tenant.
function openTenantDelete(tenant: Tenant) {
  deletingTenant.value = tenant;
  tenantDeleteConfirm.value = "";
  tenantDeleteError.value = "";
  tenantDeleteCascade.value = false;
  showTenantDeleteDialog.value = true;
}
function closeTenantDelete() { showTenantDeleteDialog.value = false; deletingTenant.value = undefined; }

// First attempt is always cascade:false, so a tenant that still owns resources
// stops here with 409 TENANT_NOT_EMPTY instead of quietly taking the data with
// it. That 409 is a step rather than a failure: the server's message names the
// resources, and the dialog surfaces it and offers the explicit second state.
async function submitTenantDelete() {
  const tenant = deletingTenant.value;
  if (!tenant) return;
  deletingTenantBusy.value = true;
  tenantDeleteError.value = "";
  try {
    await actions.removeTenant(tenant, tenantDeleteConfirm.value, tenantDeleteCascade.value);
    closeTenantDelete();
  } catch (error) {
    if (error instanceof ApiError && error.code === "TENANT_NOT_EMPTY" && !tenantDeleteCascade.value) {
      tenantDeleteCascade.value = true;
      tenantDeleteError.value = error.message;
    } else {
      tenantDeleteError.value = error instanceof Error ? error.message : "删除租户失败";
    }
  } finally {
    deletingTenantBusy.value = false;
  }
}
async function cancelDetectionJob(job: DetectionJob) { try { await actions.cancelDetectionJob(job); } catch (error) { reportError(error, "任务取消失败"); } }
// Bumped after a write to an incident lands, so the drawer's timeline reloads
// behind it rather than racing it. Owned here because this is where the writes
// complete.
const timelineKey = ref(0);
async function transition(incident: Incident, status: Incident["status"], ownerId = "") { try { await actions.changeIncident(incident, status, ownerId); timelineKey.value += 1; } catch (error) { reportError(error, "事件更新失败"); } }
async function batchTransition(ids: string[], status: Incident["status"]) { try { await actions.batchIncident(ids, status); timelineKey.value += 1; } catch (error) { reportError(error, "批量处置失败"); } }
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
function openChannelCreate() {
  editingChannel.value = undefined;
  channelForm.value = { name: "", type: "WECHAT_WORK", url: "", recipients: "", secret: "", enabled: true };
  showChannelDialog.value = true;
}

// The URL and the secret are both left blank rather than prefilled. The server
// redacts the address on the way out (path and query become "/...") and never
// returns the credential at all, so neither value the list holds is the one the
// row actually stores. Prefilling either would save the masked form back over
// the real address -- an edit that renamed a channel would repoint it at "/..."
// and the next alert would fail to deliver.
function openChannelEdit(channel: AlertChannel) {
  editingChannel.value = channel;
  channelForm.value = {
    name: channel.name, type: channel.type, url: "",
    recipients: channel.recipients ?? "", secret: "", enabled: channel.enabled,
  };
  showChannelDialog.value = true;
}

// The address is required, so an edit that leaves it blank is rejected before
// the request rather than sent as an empty URL. That is the tradeoff of not
// echoing a redacted value: the operator re-supplies the destination. It is the
// safer of the two, because a wrong destination fails loudly on the next alert
// while a silently rewritten one keeps looking configured.
function channelFormIncomplete(form: { type: AlertChannel["type"]; url: string; recipients: string }): string {
  if (channelNeeds(form.type, "url") && form.url.trim() === "") return "请重新填写通知地址（列表不显示完整地址）";
  if (channelNeeds(form.type, "recipients") && form.recipients.trim() === "") return "请填写收件人";
  return "";
}

async function submitChannel() {
  const form = channelForm.value;
  const payload = {
    name: form.name,
    type: form.type,
    url: channelNeeds(form.type, "url") ? form.url : "",
    recipients: channelNeeds(form.type, "recipients") ? form.recipients : "",
    secret: channelNeeds(form.type, "secret") ? form.secret : "",
    enabled: form.enabled,
  };
  // A blank secret on an edit means "leave the stored one alone" -- submitting
  // the form without touching that field must not break the channel's alerting.
  if (editingChannel.value) {
    const incomplete = channelFormIncomplete(payload);
    if (incomplete) { ElMessage.warning(incomplete); return; }
    try {
      await actions.saveAlertChannel(editingChannel.value.id, payload);
      showChannelDialog.value = false;
    } catch (error) { reportError(error, "更新告警渠道失败"); }
    return;
  }
  try {
    await actions.createNewAlertChannel(payload);
    showChannelDialog.value = false;
  } catch (error) { reportError(error, "创建告警渠道失败"); }
}

async function confirmChannelDelete() {
  const channel = deletingChannel.value;
  if (!channel) return;
  try {
    await actions.removeAlertChannel(channel);
    deletingChannel.value = undefined;
  } catch (error) { reportError(error, "删除告警渠道失败"); }
}

// A test send reports its failure through the same error path as everything
// else here, with the server's redacted reason included: "通知端点返回 401" is
// the whole answer to why the test failed, and the operator is the one who can
// act on it.
async function testChannel(channel: AlertChannel) {
  try { await actions.testChannel(channel); }
  catch (error) { reportError(error, `测试告警渠道 ${channel.name} 失败`); }
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
async function exportIncidentList(params: IncidentFilterParams) {
  try {
    saveBlob(await exportIncidents(params, "csv"), `incidents-${new Date().toISOString().slice(0, 10)}.csv`);
  } catch (error) { reportError(error, "导出失败"); }
}
async function exportAuditList(params: AuditFilterParams) {
  try {
    saveBlob(await exportAuditEvents(params, "csv"), `audit-events-${new Date().toISOString().slice(0, 10)}.csv`);
  } catch (error) { reportError(error, "导出失败"); }
}
async function onImported() { showImportDialog.value = false; await data.reloadAssets(); }

function openUserCreate() { editingUser.value = undefined; userForm.value = { username: "", email: "", password: "", role: "OPERATOR", disabled: false }; showUserDialog.value = true; }
function openUserEdit(user: User) { editingUser.value = user; userForm.value = { username: user.username, email: user.email ?? "", password: "", role: user.role, disabled: user.disabled }; showUserDialog.value = true; }
function openPasswordReset(user: User) { passwordUser.value = user; passwordForm.value = { password: "", confirm: "" }; showPasswordDialog.value = true; }

async function submitUser() {
  const form = userForm.value;
  try {
    if (editingUser.value) {
      // Only what changed, so an untouched role cannot re-trigger the server's
      // LAST_ADMIN check on a rename or a disable.
      const patch: Partial<Pick<User, "username" | "email" | "role" | "disabled">> = {};
      if (form.username !== editingUser.value.username) patch.username = form.username;
      // Sent even when emptied: clearing an address is how an operator stops the
      // direct notices for someone, and an omitted field would leave it in place.
      if (form.email !== (editingUser.value.email ?? "")) patch.email = form.email;
      if (form.role !== editingUser.value.role) patch.role = form.role;
      if (form.disabled !== editingUser.value.disabled) patch.disabled = form.disabled;
      if (Object.keys(patch).length === 0) { showUserDialog.value = false; return; }
      await actions.saveUser(editingUser.value, patch);
    } else {
      await actions.createNewUser({ username: form.username, email: form.email || undefined, password: form.password, role: form.role });
    }
    // The password is dropped with the form rather than left in the ref after
    // the write, so it does not outlive the request in memory.
    userForm.value = { username: "", email: "", password: "", role: "OPERATOR", disabled: false };
    showUserDialog.value = false;
  } catch (error) { reportError(error, "保存用户失败"); }
}

async function submitPasswordReset() {
  const user = passwordUser.value;
  if (!user) return;
  if (passwordForm.value.password !== passwordForm.value.confirm) { ElMessage.error("两次输入的密码不一致"); return; }
  try {
    await actions.resetUserPassword(user, passwordForm.value.password);
    passwordForm.value = { password: "", confirm: "" };
    showPasswordDialog.value = false;
  } catch (error) { reportError(error, "重置密码失败"); }
}

async function submitOwnPasswordChange(currentPassword: string, newPassword: string) {
  try {
    await actions.changeOwnPassword(currentPassword, newPassword);
  } catch (error) { reportError(error, "修改密码失败"); }
}

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
      <Topbar :title="viewTitle" :loading="data.loading" :username="session.user.username" :role-label="roleLabel" @refresh="refresh" @change-password="submitOwnPasswordChange" />
      <OverviewPage v-if="activeView === 'overview'" :first-load="data.firstLoad" :dashboard="data.dashboard" :report-summary="data.reportSummary" :version="data.version" :assets="data.assets" :incidents="data.incidents" :asset-map="assetMap" @navigate="navigate" />
      <AssetsPage v-else-if="activeView === 'assets'" :first-load="data.firstLoad" :assets="data.assets" :policies="data.policies" :can-operate="session.canOperate" :can-admin="session.canAdmin" :has-more="data.hasMore('assets')" :loading-more="data.isLoadingMore('assets')" @load-more="data.loadMore('assets')" @create="showAssetDialog = true" @verify="verify" @toggle="toggle" @detect="detect" @policy="editPolicy" @import="showImportDialog = true" @export="exportAssetList" />
      <SitesPage v-else-if="activeView === 'sites'" :first-load="data.firstLoad" :sites="data.sites" :can-admin="session.canAdmin" :has-more="data.hasMore('sites')" :loading-more="data.isLoadingMore('sites')" @load-more="data.loadMore('sites')" @create="openSiteCreate" @edit="openSiteEdit" @remove="removeSite" />
      <BaselinesPage v-else-if="activeView === 'baselines'" :first-load="data.firstLoad" :baselines="data.baselines" :asset-map="assetMap" :can-operate="session.canOperate" :has-more="data.hasMore('baselines')" :loading-more="data.isLoadingMore('baselines')" @load-more="data.loadMore('baselines')" @review="review" @retire="retireBaseline" @preview="openBaselinePreview" />
      <IncidentsPage v-else-if="activeView === 'incidents'" :first-load="data.firstLoad" :incidents="data.incidents" :evidence="data.evidence" :deliveries="data.alertDeliveries" :asset-map="assetMap" :can-operate="session.canOperate" :timeline-key="timelineKey" :users="data.users" :on-comment="comment" :has-more="data.hasMore('incidents')" :loading-more="data.isLoadingMore('incidents')" :can-admin="session.canAdmin" @load-more="data.loadMore('incidents')" @transition="transition" @batch-transition="batchTransition" @filter="data.applyIncidentFilters" @export="exportIncidentList" />
      <JobsPage v-else-if="activeView === 'jobs'" :first-load="data.firstLoad" :jobs="data.jobs" :asset-map="assetMap" :can-operate="session.canOperate" :has-more="data.hasMore('jobs')" :loading-more="data.isLoadingMore('jobs')" @load-more="data.loadMore('jobs')" @cancel="cancelDetectionJob" />
      <EvidencePage v-else-if="activeView === 'evidence'" :first-load="data.firstLoad" :evidence="data.evidence" :assets="data.assets" :asset-map="assetMap" :can-operate="session.canOperate" :has-more="data.hasMore('evidence')" :loading-more="data.isLoadingMore('evidence')" @load-more="data.loadMore('evidence')" />
      <ChannelsPage v-else-if="activeView === 'channels'" :first-load="data.firstLoad" :channels="data.alertChannels" :can-admin="session.canAdmin" @create="openChannelCreate" @edit="openChannelEdit" @remove="deletingChannel = $event" @test="testChannel" />
      <TenantsPage v-else-if="activeView === 'tenants'" :first-load="data.firstLoad" :tenants="data.tenants" :self-tenant-id="session.user?.tenantId || ''" :default-tenant-id="DEFAULT_TENANT_ID" :can-delete="session.canSuperAdmin" :has-more="data.hasMore('tenants')" :loading-more="data.isLoadingMore('tenants')" @load-more="data.loadMore('tenants')" @create="openTenantCreate" @edit="openTenantEdit" @toggle="toggleTenant" @remove="openTenantDelete" />
      <UsersPage v-else-if="activeView === 'users'" :first-load="data.firstLoad" :users="data.users" :can-admin="session.canAdmin" :self-user-id="session.user?.id || ''" @create="openUserCreate" @edit="openUserEdit" @reset-password="openPasswordReset" />
      <!-- Explicit rather than a v-else fallback: with v-else any view key that
           matched nothing above would render the audit page, which is how an
           unknown hash segment used to reach the screen silently. -->
      <AuditsPage v-else-if="activeView === 'audits'" :first-load="data.firstLoad" :audits="data.audits" :has-more="data.hasMore('audits')" :loading-more="data.isLoadingMore('audits')" @load-more="data.loadMore('audits')" @filter="data.applyAuditFilters" @export="exportAuditList" />
    </main>
    <el-dialog v-model="showAssetDialog" title="登记监测资产" width="520px">
      <el-form label-position="top">
        <el-form-item label="资产名称"><el-input v-model="assetForm.name" placeholder="例如：官网首页" /></el-form-item>
        <el-form-item label="监测地址"><el-input v-model="assetForm.url" placeholder="https://example.com" /></el-form-item>
        <el-form-item label="资产类型">
          <el-select v-model="assetForm.type">
            <el-option label="官方网站" value="OFFICIAL" />
            <el-option label="监测站点" value="MONITORING" />
            <el-option label="第三方" value="THIRD_PARTY" />
          </el-select>
        </el-form-item>
        <el-form-item label="所属站点">
          <el-select v-model="assetForm.siteId" clearable placeholder="可选">
            <el-option v-for="site in data.sites" :key="site.id" :label="site.name" :value="site.id" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer><el-button @click="showAssetDialog = false">取消</el-button><el-button type="primary" @click="createAsset">创建并授权</el-button></template>
    </el-dialog>
    <PolicyDialog v-model="showPolicyDialog" :asset="policyAsset" :policy="editingPolicy" @save="savePolicy" />
    <AssetImportDialog v-if="showImportDialog" @imported="onImported" @close="showImportDialog = false" />
    <BaselineSnapshotDrawer v-if="previewBaseline" :baseline="previewBaseline" :loading="previewLoading" @close="closeBaselinePreview" />
    <el-dialog v-model="showChannelDialog" :title="editingChannel ? '编辑告警渠道' : '新增告警渠道'" width="520px">
      <el-form label-position="top"><el-form-item label="渠道名称"><el-input v-model="channelForm.name" placeholder="例如：安全运营群" /></el-form-item><el-form-item label="渠道类型"><el-radio-group v-model="channelForm.type"><el-radio-button value="WECHAT_WORK">企业微信</el-radio-button><el-radio-button value="WEBHOOK">Webhook</el-radio-button><el-radio-button value="DINGTALK">钉钉</el-radio-button><el-radio-button value="EMAIL">邮件</el-radio-button></el-radio-group></el-form-item><el-form-item v-if="channelNeeds(channelForm.type, 'url')" :label="editingChannel ? '通知地址（必填，不显示原值）' : '通知地址'"><el-input v-model="channelForm.url" type="password" show-password autocomplete="off" :placeholder="editingChannel ? '请重新填写完整地址' : 'https://...'" /></el-form-item><el-form-item v-if="channelNeeds(channelForm.type, 'recipients')" label="收件人（逗号分隔）"><el-input v-model="channelForm.recipients" placeholder="secops@example.com,ciso@example.com" /></el-form-item><el-form-item v-if="channelNeeds(channelForm.type, 'secret')" :label="editingChannel ? '签名密钥（留空则不修改）' : '签名密钥'"><el-input v-model="channelForm.secret" type="password" show-password autocomplete="new-password" :placeholder="editingChannel ? '留空则保留现有密钥' : ''" /></el-form-item><el-form-item :label="editingChannel ? '启用' : '创建后启用'"><el-switch v-model="channelForm.enabled" /></el-form-item></el-form>
      <template #footer><el-button @click="showChannelDialog = false">取消</el-button><el-button type="primary" @click="submitChannel">{{ editingChannel ? "保存渠道" : "创建渠道" }}</el-button></template>
    </el-dialog>
    <el-dialog :model-value="Boolean(deletingChannel)" title="删除告警渠道" width="420px" @update:model-value="deletingChannel = undefined">
      <p>删除后该渠道不再收到任何告警，已产生的投递记录会保留。</p>
      <template #footer><el-button @click="deletingChannel = undefined">取消</el-button><el-button type="danger" @click="confirmChannelDelete">删除渠道</el-button></template>
    </el-dialog>
    <el-dialog v-model="showSiteDialog" :title="editingSite ? '编辑站点' : '新增站点'" width="520px">
      <el-form label-position="top">
        <el-form-item label="站点名称"><el-input v-model="siteForm.name" placeholder="例如：官方网站群" /></el-form-item>
        <el-form-item label="域名"><el-input v-model="siteForm.domain" placeholder="example.com" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="showSiteDialog = false">取消</el-button><el-button type="primary" @click="submitSite">保存站点</el-button></template>
    </el-dialog>
    <el-dialog v-model="showUserDialog" :title="editingUser ? '编辑用户' : '新增用户'" width="520px">
      <el-form label-position="top">
        <el-form-item label="用户名"><el-input v-model="userForm.username" autocomplete="off" /></el-form-item>
        <el-form-item label="邮箱">
          <el-input v-model="userForm.email" autocomplete="off" placeholder="可选；指派与升级通知发往这里" />
        </el-form-item>
        <!-- The create handler only accepts OPERATOR and AUDITOR; an ADMIN or
             SUPER_ADMIN account is provisioned by tenant bootstrap, not here.
             Edit offers ADMIN too, because promoting an existing OPERATOR is a
             supported change (updateUser validates the full role set). -->
        <el-form-item label="角色">
          <el-select v-model="userForm.role">
            <el-option label="安全运营" value="OPERATOR" />
            <el-option label="审计只读" value="AUDITOR" />
            <el-option v-if="editingUser" label="平台管理员" value="ADMIN" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="!editingUser" label="初始密码"><el-input v-model="userForm.password" type="password" show-password autocomplete="new-password" placeholder="至少 12 位" /></el-form-item>
        <el-form-item v-else label="状态"><el-switch v-model="userForm.disabled" active-text="停用" inactive-text="启用" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="showUserDialog = false">取消</el-button><el-button type="primary" @click="submitUser">保存</el-button></template>
    </el-dialog>
    <el-dialog v-model="showPasswordDialog" title="重置密码" width="480px" @closed="passwordForm = { password: '', confirm: '' }">
      <el-form label-position="top">
        <el-form-item :label="`为 ${passwordUser?.username || ''} 设置新密码`"><el-input v-model="passwordForm.password" type="password" show-password autocomplete="new-password" placeholder="至少 12 位" /></el-form-item>
        <el-form-item label="确认新密码"><el-input v-model="passwordForm.confirm" type="password" show-password autocomplete="new-password" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="showPasswordDialog = false">取消</el-button><el-button type="primary" @click="submitPasswordReset">重置</el-button></template>
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
    <el-dialog v-model="showTenantDeleteDialog" title="删除租户" width="560px" @closed="closeTenantDelete">
      <template v-if="deletingTenant">
        <el-alert type="error" :closable="false" show-icon title="删除后该租户下的资产、基线、检测任务、事件与证据均不可恢复" />
        <el-form label-position="top">
          <el-form-item :label="`请输入租户标识 ${deletingTenant.slug} 以确认删除`">
            <el-input v-model="tenantDeleteConfirm" autocomplete="off" :placeholder="deletingTenant.slug" />
          </el-form-item>
        </el-form>
        <!-- The server's own message is shown verbatim: it is what names the
             remaining resources, and re-deriving a count here would be a second
             implementation that can disagree with the one that actually deletes. -->
        <el-alert v-if="tenantDeleteError" type="warning" :closable="false" show-icon :title="tenantDeleteError" />
        <p v-if="tenantDeleteCascade">该租户仍有资源。再次确认将连同其全部数据一并删除。</p>
        <div v-else>若该租户仍有关联数据，系统会先提示，不会直接删除。</div>
      </template>
      <template #footer>
        <el-button @click="showTenantDeleteDialog = false">取消</el-button>
        <el-button type="danger" :loading="deletingTenantBusy" :disabled="!deletingTenant || tenantDeleteConfirm !== deletingTenant.slug" @click="submitTenantDelete">
          {{ tenantDeleteCascade ? "确认级联删除" : "删除租户" }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>
