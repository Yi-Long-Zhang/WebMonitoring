// The single source of truth for the view union and the runtime list. The hash
// route needs a value to validate a URL segment against, and the union is
// derived from the array rather than written out beside it, so the two cannot
// drift apart.
export const VIEWS = ["overview", "assets", "sites", "baselines", "incidents", "jobs", "evidence", "channels", "audits", "tenants", "users"] as const;

export type View = (typeof VIEWS)[number];

export function isView(value: unknown): value is View {
  return typeof value === "string" && (VIEWS as readonly string[]).includes(value);
}
