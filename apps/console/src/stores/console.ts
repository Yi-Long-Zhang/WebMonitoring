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
  getSites,
  getTenants,
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
  Site,
  Tenant,
} from "../types/domain";

export const useConsoleStore = defineStore("console", () => {
  const dashboard = ref<Dashboard>();
  const assets = ref<Asset[]>([]);
  const sites = ref<Site[]>([]);
  const tenants = ref<Tenant[]>([]);
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
      const [dashboardData, assetData, siteData, policyData, baselineData, incidentData, jobData, evidenceData, channelData, deliveryData, auditData, reportData] = await Promise.all([
        getDashboard(), getAssets(), getSites(), getPolicies(), getBaselines(), getIncidents(), getJobs(), getEvidence(), getAlertChannels(), getAlertDeliveries(), getAuditEvents(), getReportSummary(),
      ]);
      dashboard.value = dashboardData;
      assets.value = assetData.items;
      sites.value = siteData.items;
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

  async function loadTenants() {
    const tenantData = await getTenants();
    tenants.value = tenantData.items;
  }

  return { dashboard, assets, sites, tenants, policies, baselines, incidents, jobs, evidence, alertChannels, alertDeliveries, audits, reportSummary, loading, refresh, loadTenants };
});
