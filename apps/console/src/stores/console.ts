import { defineStore } from "pinia";
import { ref } from "vue";
import type { Ref } from "vue";
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
  getUsers,
} from "../services/api/client";
import type { PageParams } from "../services/api/client";
// The element types of the paged lists are inferred from the client function
// signatures (getAssets returns ListResponse<Asset>), so only the types used
// explicitly below are imported here.
import type { AlertChannel, Dashboard, ListResponse, ReportSummary, User } from "../types/domain";

// The API pages every list with a keyset cursor and caps limit at 100. 50 is
// the server default and keeps the first paint small on a tenant with a lot of
// evidence.
const pageSize = 50;

// paged mirrors that contract: loadFirst takes the head page, loadMore appends
// the next one and advances the cursor. This is the same shape
// IncidentTimeline.vue already used for one list, lifted into the store so the
// page components keep doing no IO.
function paged<T extends { id: string }>(fetch: (params: PageParams) => Promise<ListResponse<T>>) {
  const items = ref([]) as Ref<T[]>;
  const nextCursor = ref("");
  const loadingMore = ref(false);
  // extended records that the operator has pulled pages past the head, which is
  // what decides whether a refresh may replace the list or has to merge into it.
  const extended = ref(false);

  async function loadFirst() {
    const page = await fetch({ limit: pageSize });
    if (!extended.value) {
      items.value = page.items;
      nextCursor.value = page.nextCursor;
      return;
    }
    // refresh() is also the SSE callback, so it fires on every job and incident
    // event. Replacing the list wholesale here would silently discard every page
    // the operator loaded past the first. Rows are ordered (createdAt DESC,
    // id DESC) and new rows land at the head, so the cursor taken before the
    // merge still points past the tail already held.
    const headIds = new Set(page.items.map((item) => item.id));
    items.value = [...page.items, ...items.value.filter((item) => !headIds.has(item.id))];
  }

  async function loadMore() {
    if (!nextCursor.value || loadingMore.value) return;
    loadingMore.value = true;
    try {
      const page = await fetch({ limit: pageSize, cursor: nextCursor.value });
      items.value = [...items.value, ...page.items];
      nextCursor.value = page.nextCursor;
      extended.value = true;
    } finally {
      loadingMore.value = false;
    }
  }

  return { items, nextCursor, loadingMore, loadFirst, loadMore };
}

export const useConsoleStore = defineStore("console", () => {
  const dashboard = ref<Dashboard>();
  const assets = paged(getAssets);
  const sites = paged(getSites);
  const policies = paged(getPolicies);
  const baselines = paged(getBaselines);
  const incidents = paged(getIncidents);
  const jobs = paged(getJobs);
  const evidence = paged(getEvidence);
  const alertDeliveries = paged(getAlertDeliveries);
  const audits = paged(getAuditEvents);
  const tenants = paged(getTenants);
  // Not paged: GET /alert-channels and GET /users take no limit or cursor, so
  // accepting a cursor here would silently ignore it. Both lists are naturally
  // small -- channels are configured per tenant, and the user list only feeds
  // the incident drawer's owner picker.
  const alertChannels = ref<AlertChannel[]>([]);
  const users = ref<User[]>([]);
  const reportSummary = ref<ReportSummary>();
  const loading = ref(false);

  async function refresh() {
    loading.value = true;
    try {
      const [dashboardData, channelData, reportData] = await Promise.all([
        getDashboard(), getAlertChannels(), getReportSummary(),
      ]);
      dashboard.value = dashboardData;
      alertChannels.value = channelData.items;
      reportSummary.value = reportData;
      // The head page of every list is refreshed after the summary data, so a
      // failure here still leaves the overview populated.
      await Promise.all([
        assets.loadFirst(), sites.loadFirst(), policies.loadFirst(),
        baselines.loadFirst(), incidents.loadFirst(), jobs.loadFirst(),
        evidence.loadFirst(), alertDeliveries.loadFirst(), audits.loadFirst(),
      ]);
    } finally {
      loading.value = false;
    }
  }

  // Loaded on demand rather than in refresh(): only the tenant page needs it.
  async function loadTenants() {
    await tenants.loadFirst();
  }

  // Loaded on demand rather than in refresh(): only the incident drawer needs
  // the user list, and it is not part of the periodic dashboard pull.
  async function loadUsers() {
    const userData = await getUsers();
    users.value = userData.items;
  }

  const pages = { assets, sites, policies, baselines, incidents, jobs, evidence, alertDeliveries, audits, tenants };
  type PageKey = keyof typeof pages;

  // Exposed as functions rather than as the paged objects: the paged objects
  // hold refs, and Pinia only unwraps refs at the store's top level, so a page
  // reading `pages.assets.nextCursor` would get a Ref instead of the cursor.
  // Called from a template these read .value during render and so stay reactive.
  function hasMore(key: PageKey) {
    return Boolean(pages[key].nextCursor.value);
  }
  function isLoadingMore(key: PageKey) {
    return pages[key].loadingMore.value;
  }
  function loadMore(key: PageKey) {
    return pages[key].loadMore();
  }

  return {
    dashboard,
    assets: assets.items,
    sites: sites.items,
    tenants: tenants.items,
    users,
    policies: policies.items,
    baselines: baselines.items,
    incidents: incidents.items,
    jobs: jobs.items,
    evidence: evidence.items,
    alertChannels,
    alertDeliveries: alertDeliveries.items,
    audits: audits.items,
    reportSummary,
    loading,
    refresh,
    loadTenants,
    loadUsers,
    hasMore,
    isLoadingMore,
    loadMore,
  };
});
