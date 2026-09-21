import { ElMessage, ElMessageBox } from "element-plus";
import {
  addIncidentComment,
  approveBaseline,
  cancelJob,
  createAlertChannel,
  createAsset,
  createSite,
  createTenant,
  createUser,
  deleteAlertChannel,
  deleteTenantDetailed,
  issueVerification,
  rejectBaseline,
  resetUserPassword as resetUserPasswordRequest,
  retireBaseline as retireBaselineRequest,
  runDetection,
  testAlertChannel,
  transitionIncident,
  updateAlertChannel,
  updateAsset,
  updatePolicy,
  updateSite,
  updateTenant,
  updateUser,
  verifyAsset,
} from "../../services/api/client";
import type { AlertChannel, Asset, AssetType, DetectionJob, Incident, Site, Tenant, User } from "../../types/domain";
import { useConsoleStore } from "../../stores/console";

export function useMonitoringActions() {
  const consoleStore = useConsoleStore();

  async function createNewAsset(input: { name: string; url: string; type: AssetType; siteId?: string }) {
    await createAsset(input);
    await consoleStore.refresh();
    ElMessage.success("资产已创建，请完成授权验证");
  }

  async function verifyAssetOwnership(asset: Asset) {
    const result = await issueVerification(asset.id);
    await ElMessageBox.alert(`请在目标站点创建文件 ${result.path}，内容为：${result.content}`, "完成站点授权", {
      confirmButtonText: "已完成，开始验证",
    });
    await verifyAsset(asset.id);
    await consoleStore.refresh();
    ElMessage.success("资产已验证并启用");
  }

  async function toggleMonitoring(asset: Asset) {
    await updateAsset(asset.id, { enabled: !asset.enabled });
    await consoleStore.refresh();
  }

  async function startDetection(asset: Asset, mode: "HTTP" | "BROWSER") {
    await runDetection(asset.id, mode);
    await consoleStore.refresh();
    ElMessage.success(`${mode} 检测已进入队列`);
  }

  async function reviewBaseline(id: string, accepted: boolean) {
    if (accepted) await approveBaseline(id);
    else await rejectBaseline(id);
    await consoleStore.refresh();
    ElMessage.success(accepted ? "基线已批准" : "基线已拒绝");
  }

  // ownerId is applied together with the transition because the API validates a
  // state change for every call; there is no owner-only endpoint to call.
  async function changeIncident(incident: Incident, status: Incident["status"], ownerId = "") {
    let resolution = "";
    if (status === "RESOLVED" || status === "FALSE_POSITIVE") {
      try {
        const result = await ElMessageBox.prompt("请填写处置说明", "关闭事件", {
          inputPlaceholder: "例如：已回滚至发布版本并完成复测",
          inputValidator: (value) => value.trim().length >= 4 || "至少填写 4 个字符",
        });
        resolution = result.value;
      } catch {
        return;
      }
    }
    await transitionIncident(incident.id, status, resolution, ownerId);
    await consoleStore.refresh();
    ElMessage.success("事件状态已更新");
  }

  async function retireBaseline(id: string) {
    await retireBaselineRequest(id);
    await consoleStore.refresh();
    ElMessage.success("基线已废弃");
  }

  // The 409 for a job that already reached a terminal state is deliberately not
  // caught: request() already surfaces the server's message through
  // ElMessage.error, and letting it throw keeps the list untouched.
  async function cancelDetectionJob(job: DetectionJob) {
    await cancelJob(job.id);
    await consoleStore.refresh();
    ElMessage.success("任务已取消");
  }

  async function savePolicy(asset: Asset, input: {
    httpIntervalSeconds: number;
    browserIntervalSeconds: number;
    timeoutSeconds: number;
    maxResponseBytes: number;
    ignoreSelectors: string[];
    trustedDomains: string[];
    maintenanceWindowStart?: string;
    maintenanceWindowEnd?: string;
  }) {
    await updatePolicy(asset.id, input);
    await consoleStore.refresh();
    ElMessage.success("策略已更新");
  }
  async function createNewAlertChannel(input: {
    name: string;
    type: AlertChannel["type"];
    url: string;
    recipients: string;
    secret: string;
    enabled: boolean;
  }) {
    await createAlertChannel(input);
    await consoleStore.refresh();
    ElMessage.success("告警渠道已创建");
  }

  async function saveAlertChannel(id: string, input: {
    name: string;
    type: AlertChannel["type"];
    url: string;
    recipients: string;
    secret: string;
    enabled: boolean;
  }) {
    // An empty secret is dropped rather than sent: the server reads an absent
    // field as "keep the stored credential", and it never hands the plaintext
    // back, so sending "" would be indistinguishable from a deliberate erase.
    await updateAlertChannel(id, input.secret === "" ? { ...input, secret: undefined } : input);
    await consoleStore.refresh();
    ElMessage.success("告警渠道已更新");
  }

  async function removeAlertChannel(channel: AlertChannel) {
    await deleteAlertChannel(channel.id);
    await consoleStore.refresh();
    ElMessage.success(`告警渠道 ${channel.name} 已删除`);
  }

  // A test send is the one channel action that reports a failure as an error
  // rather than a success toast: the operator asked a direct question about
  // whether the channel works, and "已发送测试消息" on a channel that just
  // returned 401 would be the wrong answer. The caller surfaces the reason.
  async function testChannel(channel: AlertChannel) {
    await testAlertChannel(channel.id);
    ElMessage.success(`测试消息已投递到 ${channel.name}`);
  }

  async function createNewSite(input: { name: string; domain: string }) {
    await createSite(input);
    await consoleStore.refresh();
    ElMessage.success("站点已创建");
  }

  async function saveSite(site: Site, input: { name: string; domain: string }) {
    await updateSite(site.id, input);
    await consoleStore.refresh();
    ElMessage.success("站点已更新");
  }

  async function createNewTenant(input: { name: string; slug: string; adminUsername: string; adminPassword: string }) {
    await createTenant(input);
    await consoleStore.loadTenants();
    ElMessage.success("租户已创建，管理员账号已播种");
  }

  async function saveTenant(tenant: Tenant, input: { name: string; slug: string }) {
    await updateTenant(tenant.id, input);
    await consoleStore.loadTenants();
    ElMessage.success("租户已更新");
  }

  // Deliberately separate from saveTenant and deliberately sending only
  // `disabled`. A PATCH that also carried name and slug would re-run the slug
  // uniqueness check, so flipping a switch could 409 TENANT_EXISTS for a reason
  // that has nothing to do with it. The handler records the audit detail as
  // {"disabled": ...} for exactly this call shape.
  //
  // The server's 409 TENANT_PROTECTED (the default tenant and the caller's own)
  // is not caught here: request() surfaces its message through ElMessage.error.
  async function setTenantDisabled(tenant: Tenant, disabled: boolean) {
    await updateTenant(tenant.id, { disabled });
    await consoleStore.loadTenants();
    ElMessage.success(disabled ? "租户已停用" : "租户已启用");
  }

  // Deletion is a two-state flow rather than one call, because the server
  // answers 409 TENANT_NOT_EMPTY when the tenant still owns resources and
  // expects the caller to say so explicitly. deleteTenantDetailed exists to
  // carry that code back; request() would only have the message. Nothing here
  // counts the resources: the counts live in the server's `details` and the
  // server's message already names them, so the dialog shows that message
  // rather than a second, drift-prone tally.
  async function removeTenant(tenant: Tenant, confirm: string, cascade: boolean) {
    const result = await deleteTenantDetailed(tenant.id, confirm, cascade);
    await consoleStore.loadTenants();
    ElMessage.success(`租户 ${tenant.name} 已删除`);
    return result;
  }

  async function commentOnIncident(incident: Incident, note: string) {
    await addIncidentComment(incident.id, note);
    ElMessage.success("备注已记录");
  }

  // The password is never echoed back and never put in a message: it is read
  // from the form, sent, and the form is cleared by the caller. The server
  // enforces the 12-character minimum; the dialog mirrors it so the operator
  // finds out before the round trip.
  async function createNewUser(input: { username: string; password: string; role: User["role"] }) {
    await createUser(input);
    await consoleStore.loadUsers();
    ElMessage.success("用户已创建");
  }

  // Sends only the fields the dialog actually changed. A PATCH that also carried
  // an untouched role would re-run the LAST_ADMIN check, so flipping a switch
  // could 409 for a reason that has nothing to do with it -- the same reason
  // setTenantDisabled sends only `disabled`.
  async function saveUser(user: User, input: Partial<Pick<User, "username" | "role" | "disabled">>) {
    await updateUser(user.id, input);
    await consoleStore.loadUsers();
    ElMessage.success("用户已更新");
  }

  async function resetUserPassword(user: User, password: string) {
    await resetUserPasswordRequest(user.id, password);
    ElMessage.success(`已重置 ${user.username} 的密码`);
  }

  return {
    createNewAsset,
    verifyAssetOwnership,
    toggleMonitoring,
    startDetection,
    reviewBaseline,
    changeIncident,
    commentOnIncident,
    savePolicy,
    retireBaseline,
    cancelDetectionJob,
    createNewAlertChannel,
    saveAlertChannel,
    removeAlertChannel,
    testChannel,
    createNewSite,
    saveSite,
    createNewTenant,
    saveTenant,
    setTenantDisabled,
    removeTenant,
    createNewUser,
    saveUser,
    resetUserPassword,
  };
}
