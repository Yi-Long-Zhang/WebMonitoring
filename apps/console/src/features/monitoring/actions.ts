import { ElMessage, ElMessageBox } from "element-plus";
import {
  approveBaseline,
  createAlertChannel,
  createAsset,
  issueVerification,
  rejectBaseline,
  runDetection,
  transitionIncident,
  updateAsset,
  updatePolicy,
  verifyAsset,
} from "../../services/api/client";
import type { AlertChannel, Asset, AssetType, Incident } from "../../types/domain";
import { useConsoleStore } from "../../stores/console";

export function useMonitoringActions() {
  const consoleStore = useConsoleStore();

  async function createNewAsset(input: { name: string; url: string; type: AssetType }) {
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

  async function changeIncident(incident: Incident, status: Incident["status"]) {
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
    await transitionIncident(incident.id, status, resolution);
    await consoleStore.refresh();
    ElMessage.success("事件状态已更新");
  }

  async function editPolicy(asset: Asset) {
    const current = consoleStore.policies.find((policy) => policy.assetId === asset.id);
    if (!current) return;
    try {
      const result = await ElMessageBox.prompt("请输入需要忽略的 CSS Selector，多个选择器用换行分隔", `配置 ${asset.name}`, {
        inputValue: current.ignoreSelectors.join("\n"),
        inputType: "textarea",
      });
      await updatePolicy(asset.id, {
        httpIntervalSeconds: current.httpIntervalSeconds,
        browserIntervalSeconds: current.browserIntervalSeconds,
        timeoutSeconds: current.timeoutSeconds,
        maxResponseBytes: current.maxResponseBytes,
        trustedDomains: current.trustedDomains,
        ignoreSelectors: result.value.split(/\r?\n/).map((value) => value.trim()).filter(Boolean),
      });
      await consoleStore.refresh();
      ElMessage.success("策略已更新");
    } catch {
      return;
    }
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

  return {
    createNewAsset,
    verifyAssetOwnership,
    toggleMonitoring,
    startDetection,
    reviewBaseline,
    changeIncident,
    editPolicy,
    createNewAlertChannel,
  };
}
