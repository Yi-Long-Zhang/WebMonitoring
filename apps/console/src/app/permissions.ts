import type { Role } from "../types/domain";

export function canOperate(role?: Role) {
  return role === "ADMIN" || role === "OPERATOR";
}

export function canAdmin(role?: Role) {
  return role === "ADMIN";
}
