import { defineStore } from "pinia";
import { getSession, login, logout } from "../services/api/client";
import { canAdmin, canOperate, canSuperAdmin } from "../app/permissions";
import type { Session } from "../types/domain";

export const useSessionStore = defineStore("session", {
  state: () => ({ session: undefined as Session | undefined, loading: false }),
  getters: {
    user: (state) => state.session?.user,
    isAuthenticated: (state) => Boolean(state.session),
    // Delegated rather than re-derived, so the matrix has exactly one home.
    // Keeping the getter names means every existing `session.canOperate` call
    // site keeps working unchanged.
    canOperate: (state) => canOperate(state.session?.user.role),
    canAdmin: (state) => canAdmin(state.session?.user.role),
    canSuperAdmin: (state) => canSuperAdmin(state.session?.user.role),
  },
  actions: {
    async restore() {
      try {
        this.session = await getSession();
        return true;
      } catch {
        this.session = undefined;
        return false;
      }
    },
    async signIn(username: string, password: string, tenant = "") {
      this.loading = true;
      try {
        this.session = await login(username, password, tenant);
      } finally {
        this.loading = false;
      }
    },
    async signOut() {
      try {
        await logout();
      } finally {
        this.session = undefined;
      }
    },
  },
});
