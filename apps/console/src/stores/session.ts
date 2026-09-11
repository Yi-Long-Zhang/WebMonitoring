import { defineStore } from "pinia";
import { getSession, login, logout } from "../services/api/client";
import type { Session } from "../types/domain";

export const useSessionStore = defineStore("session", {
  state: () => ({ session: undefined as Session | undefined, loading: false }),
  getters: {
    user: (state) => state.session?.user,
    isAuthenticated: (state) => Boolean(state.session),
    canOperate: (state) => ["ADMIN", "OPERATOR"].includes(state.session?.user.role || ""),
    canAdmin: (state) => state.session?.user.role === "ADMIN",
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
    async signIn(username: string, password: string) {
      this.loading = true;
      try {
        this.session = await login(username, password);
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
