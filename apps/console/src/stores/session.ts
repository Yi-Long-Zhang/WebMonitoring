import { defineStore } from "pinia";
import { HttpError, getSession, login, logout } from "../services/api/client";
import { messageOf } from "../app/errors";
import { canAdmin, canOperate, canSuperAdmin } from "../app/permissions";
import type { Session } from "../types/domain";

export const useSessionStore = defineStore("session", {
  state: () => ({
    session: undefined as Session | undefined,
    loading: false,
    // Set when restore() failed for a reason other than "not signed in", so
    // App.vue can say so. Cleared by the next successful restore and by the
    // operator dismissing the banner.
    restoreFailure: undefined as string | undefined,
  }),
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
        this.restoreFailure = undefined;
        return true;
      } catch (error) {
        this.session = undefined;
        // A 401 is the ordinary "not signed in" answer and stays silent: it is
        // the first thing every visitor to the console produces. Anything else
        // (the API is down, the proxy answered 502, the request never left the
        // browser) leaves the operator on a login form that will reject a
        // correct password, so it is recorded for App.vue to display.
        this.restoreFailure = error instanceof HttpError && error.status === 401
          ? undefined
          : messageOf(error, "无法确认登录状态，请检查网络或稍后重试");
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
