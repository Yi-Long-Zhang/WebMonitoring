import type {
  AlertChannel, AlertDelivery, Asset, AuditEvent, Baseline, Dashboard, DetectionJob, Evidence, Incident, IncidentStatus, ListResponse, Policy, ReportSummary, Session, Site, Tenant, User,
} from "../../types/domain";

let csrfToken = "";

export function setCsrfToken(value: string) {
  csrfToken = value;
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body) {
    headers.set("Content-Type", "application/json");
  }
  if (init.method && !["GET", "HEAD", "OPTIONS"].includes(init.method.toUpperCase())) {
    headers.set("X-CSRF-Token", csrfToken);
  }
  const response = await fetch(`/api/v1${path}`, {
    credentials: "include",
    ...init,
    headers,
  });
  if (!response.ok) {
    let payload: { message?: string; code?: string } = {};
    try {
      payload = await response.json();
    } catch {
      // Keep the network status as the fallback error.
    }
    throw new Error(payload.message || `${response.status} ${response.statusText}`);
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return response.json() as Promise<T>;
}

export async function login(username: string, password: string, tenant = "") {
  const session = await request<Session>("/auth/session", {
    method: "POST",
    body: JSON.stringify({ tenant, username, password }),
  });
  setCsrfToken(session.csrfToken);
  return session;
}

export async function getSession() {
  const session = await request<Session>("/auth/session");
  setCsrfToken(session.csrfToken);
  return session;
}

export function logout() {
  return request<void>("/auth/session", { method: "DELETE" });
}

export function getDashboard() {
  return request<Dashboard>("/dashboard");
}

export function getAssets() {
  return request<ListResponse<Asset>>("/assets");
}

export function createAsset(input: Pick<Asset, "name" | "url" | "type" | "siteId">) {
  return request<Asset>("/assets", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export function importAssets(input: {
  assets: Array<Pick<Asset, "name" | "url" | "type"> & { ownerId?: string; siteId?: string }>;
  dryRun?: boolean;
  siteId?: string;
  ownerId?: string;
}) {
  return request<{
    dryRun: boolean;
    rows: number;
    created: number;
    results: Array<{ index: number; name?: string; url?: string; status: "CREATED" | "VALIDATED" | "REJECTED"; code?: string; message?: string }>;
  }>("/assets/import", { method: "POST", body: JSON.stringify(input) });
}

export function exportAssets(format: "json" | "csv" = "csv") {
  return request<{ items: Asset[] }>(`/assets/export?format=${format}`);
}

export function updateAsset(id: string, input: Partial<Pick<Asset, "name" | "ownerId" | "siteId" | "enabled">>) {
  return request<Asset>(`/assets/${id}`, {
    method: "PATCH",
    body: JSON.stringify(input),
  });
}

export function issueVerification(id: string) {
  return request<{ path: string; content: string }>(`/assets/${id}/verification`, {
    method: "POST",
    body: JSON.stringify({ action: "ISSUE" }),
  });
}

export function verifyAsset(id: string) {
  return request<Asset>(`/assets/${id}/verification`, {
    method: "POST",
    body: JSON.stringify({ action: "VERIFY" }),
  });
}

export function getPolicies() {
  return request<ListResponse<Policy>>("/policies");
}

export function updatePolicy(assetId: string, input: Partial<Policy>) {
  return request<Policy>(`/policies/${assetId}`, {
    method: "PUT",
    body: JSON.stringify(input),
  });
}

export function getBaselines(assetId?: string) {
  const query = assetId ? `?assetId=${encodeURIComponent(assetId)}` : "";
  return request<ListResponse<Baseline>>(`/baselines${query}`);
}

export function approveBaseline(id: string) {
  return request<Baseline>(`/baselines/${id}/approve`, { method: "POST" });
}

export function rejectBaseline(id: string) {
  return request<Baseline>(`/baselines/${id}/reject`, { method: "POST" });
}
export function retireBaseline(id: string) {
  return request<Baseline>(`/baselines/${id}/retire`, { method: "POST" });
}

export function getJobs() {
  return request<ListResponse<DetectionJob>>("/detection-jobs");
}

export function runDetection(assetId: string, mode: "HTTP" | "BROWSER") {
  return request<DetectionJob>("/detection-jobs", {
    method: "POST",
    body: JSON.stringify({ assetId, mode }),
  });
}

export function getIncidents() {
  return request<ListResponse<Incident>>("/incidents");
}

export function getEvidence() {
  return request<ListResponse<Evidence>>("/evidence");
}

export function issueEvidenceSignedUrl(id: string) {
  return request<{ url: string; expiresAt: string }>(`/evidence/${id}/signed-url`, {
    method: "POST",
  });
}

export function getAlertChannels() {
  return request<ListResponse<AlertChannel>>("/alert-channels");
}

export function getAlertDeliveries(incidentId?: string) {
  const query = incidentId ? `?incidentId=${encodeURIComponent(incidentId)}` : "";
  return request<ListResponse<AlertDelivery>>(`/alert-deliveries${query}`);
}

export function getReportSummary(params: { from?: string; to?: string; assetId?: string; severity?: string } = {}) {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value) query.set(key, value);
  }
  const suffix = query.toString() ? `?${query.toString()}` : "";
  return request<ReportSummary>(`/reports/summary${suffix}`);
}

export function createAlertChannel(input: {
  name: string;
  type: AlertChannel["type"];
  url: string;
  recipients?: string;
  secret: string;
  enabled: boolean;
}) {
  return request<AlertChannel>("/alert-channels", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export function getAuditEvents() {
  return request<ListResponse<AuditEvent>>("/audit-events");
}

export function transitionIncident(id: string, status: IncidentStatus, resolution = "") {
  return request<Incident>(`/incidents/${id}/transition`, {
    method: "POST",
    body: JSON.stringify({ status, resolution }),
  });
}

export function getSites() {
  return request<ListResponse<Site>>("/sites");
}

export function createSite(input: { name: string; domain: string; ownerId?: string }) {
  return request<Site>("/sites", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export function updateSite(id: string, input: Partial<Pick<Site, "name" | "domain" | "ownerId">>) {
  return request<Site>(`/sites/${id}`, {
    method: "PATCH",
    body: JSON.stringify(input),
  });
}

export function getTenants() {
  return request<ListResponse<Tenant>>("/tenants");
}

export function createTenant(input: { name: string; slug: string; adminUsername: string; adminPassword: string }) {
  return request<Tenant>("/tenants", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export function updateTenant(id: string, input: Partial<Pick<Tenant, "name" | "slug" | "disabled">>) {
  return request<Tenant>(`/tenants/${id}`, {
    method: "PATCH",
    body: JSON.stringify(input),
  });
}

export function deleteTenant(id: string, confirm: string, cascade = false) {
  return request<{ tenantId: string; deletedResources: Record<string, number>; orphanedEvidenceObjects: number }>(
    `/tenants/${id}`,
    { method: "DELETE", body: JSON.stringify({ confirm, cascade }) },
  );
}

export function getUsers() {
  return request<ListResponse<User>>("/users");
}

export function createUser(input: { username: string; password: string; role: User["role"] }) {
  return request<User>("/users", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export function updateUser(id: string, input: Partial<Pick<User, "username" | "role" | "disabled">>) {
  return request<User>(`/users/${id}`, {
    method: "PATCH",
    body: JSON.stringify(input),
  });
}

export function resetUserPassword(id: string, password: string) {
  return request<void>(`/users/${id}/password`, {
    method: "POST",
    body: JSON.stringify({ password }),
  });
}

export function changeOwnPassword(currentPassword: string, newPassword: string) {
  return request<void>("/auth/password", {
    method: "POST",
    body: JSON.stringify({ currentPassword, newPassword }),
  });
}
