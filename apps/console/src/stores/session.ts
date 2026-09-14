import { defineStore } from "pinia";
import { getSession, login, logout } from "../services/api/client";
import type { Session } from "../types/domain";

export const useSessionStore = defineStore("session", {
  state: () => ({ session: undefined as Session | undefined, loading: false }),
  getters: {
    user: (state) => state.session?.user,
    isAuthenticated: (state) => Boolean(state.session),
    canOperate: (state) => ["ADMIN", "OPERATOR", "SUPER_ADMIN"].includes(state.session?.user.role || ""),
    canAdmin: (state) => ["ADMIN", "SUPER_ADMIN"].includes(state.session?.user.role || ""),
    canSuperAdmin: (state) => state.session?.user.role === "SUPER_ADMIN",
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
