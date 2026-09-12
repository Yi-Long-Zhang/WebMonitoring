import { defineStore } from "pinia";
import { ref } from "vue";
import {
  getAlertChannels,
  getAlertDeliveries,
  getAssets,
  getAuditEvents,
  getBaselines,
  getDashboard,
  getEvidence,
  getIncidents,
  getJobs,
  getPolicies,
  getReportSummary,
} from "../services/api/client";
import type {
  AlertChannel,
  AlertDelivery,
  Asset,
  AuditEvent,
  Baseline,
  Dashboard,
  DetectionJob,
  Evidence,
  Incident,
  Policy,
  ReportSummary,
} from "../types/domain";

export const useConsoleStore = defineStore("console", () => {
  const dashboard = ref<Dashboard>();
  const assets = ref<Asset[]>([]);
  const policies = ref<Policy[]>([]);
  const baselines = ref<Baseline[]>([]);
  const incidents = ref<Incident[]>([]);
  const jobs = ref<DetectionJob[]>([]);
  const evidence = ref<Evidence[]>([]);
  const alertChannels = ref<AlertChannel[]>([]);
  const alertDeliveries = ref<AlertDelivery[]>([]);
  const reportSummary = ref<ReportSummary>();
  const audits = ref<AuditEvent[]>([]);
  const loading = ref(false);

  async function refresh() {
    loading.value = true;
    try {
      const [dashboardData, assetData, policyData, baselineData, incidentData, jobData, evidenceData, channelData, deliveryData, auditData, reportData] = await Promise.all([
        getDashboard(), getAssets(), getPolicies(), getBaselines(), getIncidents(), getJobs(), getEvidence(), getAlertChannels(), getAlertDeliveries(), getAuditEvents(), getReportSummary(),
      ]);
      dashboard.value = dashboardData;
      assets.value = assetData.items;
      policies.value = policyData.items;
      baselines.value = baselineData.items;
      incidents.value = incidentData.items;
      jobs.value = jobData.items;
      evidence.value = evidenceData.items;
      alertChannels.value = channelData.items;
      alertDeliveries.value = deliveryData.items;
      audits.value = auditData.items;
      reportSummary.value = reportData;
    } finally {
      loading.value = false;
    }
  }

  return { dashboard, assets, policies, baselines, incidents, jobs, evidence, alertChannels, alertDeliveries, audits, reportSummary, loading, refresh };
});
