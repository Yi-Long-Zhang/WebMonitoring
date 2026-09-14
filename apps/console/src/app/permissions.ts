import type { Role } from "../types/domain";

export function canOperate(role?: Role) {
  return role === "ADMIN" || role === "OPERATOR" || role === "SUPER_ADMIN";
}

export function canAdmin(role?: Role) {
  return role === "ADMIN" || role === "SUPER_ADMIN";
}

export function canSuperAdmin(role?: Role) {
  return role === "SUPER_ADMIN";
}
