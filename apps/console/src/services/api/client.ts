import type {
  AlertChannel, Asset, AuditEvent, Baseline, Dashboard, DetectionJob, Evidence, Incident, IncidentStatus, ListResponse, Policy, Session,
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

export async function login(username: string, password: string) {
  const session = await request<Session>("/auth/session", {
    method: "POST",
    body: JSON.stringify({ username, password }),
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

export function createAsset(input: Pick<Asset, "name" | "url" | "type">) {
  return request<Asset>("/assets", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export function updateAsset(id: string, input: Partial<Pick<Asset, "name" | "ownerId" | "enabled">>) {
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

export function getAlertChannels() {
  return request<ListResponse<AlertChannel>>("/alert-channels");
}

export function createAlertChannel(input: {
  name: string;
  type: AlertChannel["type"];
  url: string;
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
