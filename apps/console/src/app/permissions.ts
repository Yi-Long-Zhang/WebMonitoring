import type { Role } from "../types/domain";

// The single source of truth for the role matrix. `stores/session.ts` and
// `components/layout/Sidebar.vue` both call into this file rather than
// re-deriving it: the matrix has been written out independently three times
// before, and each copy drifted (Sidebar was missing canOperate entirely, so
// every operate-gated nav item was visible to read-only roles).
//
// These are UI affordances only. Hiding a control is not an access control --
// every route behind them enforces its own roles server-side (requireRoles in
// internal/api/server.go). Treat anything here as presentation, never as
// protection.

export function canOperate(role?: Role) {
  return role === "ADMIN" || role === "OPERATOR" || role === "SUPER_ADMIN";
}

export function canAdmin(role?: Role) {
  return role === "ADMIN" || role === "SUPER_ADMIN";
}

export function canSuperAdmin(role?: Role) {
  return role === "SUPER_ADMIN";
}

const ROLE_LABELS: Record<Role, string> = {
  ADMIN: "平台管理员",
  OPERATOR: "安全运营",
  AUDITOR: "审计只读",
  SUPER_ADMIN: "超级管理员",
};

export function roleLabel(role?: Role): string {
  return ROLE_LABELS[role ?? "AUDITOR"] ?? ROLE_LABELS.AUDITOR;
}
