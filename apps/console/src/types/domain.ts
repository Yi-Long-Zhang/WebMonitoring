import type { components } from "./generated/api";

export type Role = components["schemas"]["Role"];
export type AssetType = components["schemas"]["AssetType"];
export type VerificationStatus = components["schemas"]["VerificationStatus"];
export type VerificationInstructions = components["schemas"]["VerificationInstructions"];
export type Severity = components["schemas"]["Severity"];
export type IncidentStatus = components["schemas"]["IncidentStatus"];
export type User = components["schemas"]["User"];
export type Session = components["schemas"]["Session"];
export type Tenant = components["schemas"]["Tenant"];
export type Site = components["schemas"]["Site"];
export type Asset = components["schemas"]["Asset"];
export type Incident = components["schemas"]["Incident"];
export type IncidentEvent = components["schemas"]["IncidentEvent"];
export type IncidentEventType = components["schemas"]["IncidentEventType"];
export type Baseline = components["schemas"]["Baseline"];
export type DetectionJob = components["schemas"]["DetectionJob"];
export type Policy = components["schemas"]["Policy"];
export type Evidence = components["schemas"]["Evidence"];
export type AlertChannel = components["schemas"]["AlertChannel"];
export type CreateAlertChannel = components["schemas"]["CreateAlertChannel"];
export type AlertDelivery = components["schemas"]["AlertDelivery"];
export type AuditEvent = components["schemas"]["AuditEvent"];
export type ReportSummary = components["schemas"]["ReportSummary"];

export type Dashboard = {
  assets: number;
  verifiedAssets: number;
  openIncidents: number;
  criticalIncidents: number;
  failedJobs: number;
};

export type ListResponse<T> = {
  items: T[];
  nextCursor: string;
};
