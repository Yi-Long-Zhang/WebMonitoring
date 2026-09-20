import { ElMessage, ElMessageBox } from "element-plus";
import {
  addIncidentComment,
  approveBaseline,
  createAlertChannel,
  createAsset,
  createSite,
  createTenant,
  issueVerification,
  rejectBaseline,
  retireBaseline as retireBaselineRequest,
  runDetection,
  transitionIncident,
  updateAsset,
  updatePolicy,
  updateSite,
  updateTenant,
  verifyAsset,
} from "../../services/api/client";
import type { AlertChannel, Asset, AssetType, Incident, Site, Tenant } from "../../types/domain";
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
    secret: string;
    enabled: boolean;
  }) {
    await createAlertChannel(input);
    await consoleStore.refresh();
    ElMessage.success("告警渠道已创建");
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

  async function commentOnIncident(incident: Incident, note: string) {
    await addIncidentComment(incident.id, note);
    ElMessage.success("备注已记录");
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
    createNewAlertChannel,
    createNewSite,
    saveSite,
    createNewTenant,
    saveTenant,
  };
}
