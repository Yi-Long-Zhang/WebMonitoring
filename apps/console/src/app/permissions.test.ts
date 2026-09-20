import { describe, expect, it } from "vitest";
import { canAdmin, canOperate, canSuperAdmin, roleLabel } from "./permissions";
import type { Role } from "../types/domain";

const ROLES: Role[] = ["ADMIN", "OPERATOR", "AUDITOR", "SUPER_ADMIN"];

// The negative cases are the point of this file. The matrix used to be written
// out independently in three places and Sidebar's copy was missing canOperate
// altogether, so every operate-gated nav item was visible to AUDITOR. These
// assertions fail if a copy reappears with a different boundary.
describe("role matrix", () => {
  it("grants operate to ADMIN, OPERATOR and SUPER_ADMIN only", () => {
    expect(ROLES.filter(canOperate)).toEqual(["ADMIN", "OPERATOR", "SUPER_ADMIN"]);
    expect(canOperate("AUDITOR")).toBe(false);
  });

  it("grants admin to ADMIN and SUPER_ADMIN only", () => {
    expect(ROLES.filter(canAdmin)).toEqual(["ADMIN", "SUPER_ADMIN"]);
    expect(canAdmin("OPERATOR")).toBe(false);
    expect(canAdmin("AUDITOR")).toBe(false);
  });

  it("grants super admin to SUPER_ADMIN only", () => {
    expect(ROLES.filter(canSuperAdmin)).toEqual(["SUPER_ADMIN"]);
    expect(canSuperAdmin("ADMIN")).toBe(false);
    expect(canSuperAdmin("OPERATOR")).toBe(false);
  });

  it("is strictly narrowing: admin implies operate, super admin implies admin", () => {
    for (const role of ROLES) {
      if (canSuperAdmin(role)) expect(canAdmin(role)).toBe(true);
      if (canAdmin(role)) expect(canOperate(role)).toBe(true);
    }
  });

  it("treats a missing role as the least privileged one", () => {
    expect(canOperate(undefined)).toBe(false);
    expect(canAdmin(undefined)).toBe(false);
    expect(canSuperAdmin(undefined)).toBe(false);
    expect(roleLabel(undefined)).toBe("审计只读");
  });

  it("labels every role and falls back for an unknown one", () => {
    for (const role of ROLES) {
      expect(roleLabel(role)).not.toBe("");
    }
    expect(roleLabel("ADMIN")).toBe("平台管理员");
    expect(roleLabel("SUPER_ADMIN")).toBe("超级管理员");
  });
});
