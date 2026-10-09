import { shallowRef } from "vue";

// appFailure is the unattributed failure channel. Vue reports a render-time
// error to the nearest errorCaptured hook -- App.vue has one and replaces the
// view with a full-page fallback. Everything else (a watcher, an async event
// handler, a lifecycle hook) has no subtree to replace and goes to
// app.config.errorHandler, which is a plain callback with no way to render.
// It records here instead, and App.vue shows a dismissible banner.
//
// shallowRef rather than ref: the value is replaced whole and never mutated
// in place, so deep reactivity would only add proxies to an error object.
export type AppFailure = {
  message: string;
  // Where it came from (Vue's info string, the endpoint, the action name).
  // Shown collapsed: the operator needs something to paste into a report, not
  // a stack trace on the screen.
  detail: string;
};

export const appFailure = shallowRef<AppFailure | undefined>();

// messageOf is the one place that decides what a caught value says. Anything
// that is an Error contributes its own message (the API client's messages are
// already user-facing); everything else falls back, because String(undefined)
// and "[object Object]" have both been seen in operator-facing text.
export function messageOf(error: unknown, fallback = "控制台遇到未知错误"): string {
  if (error instanceof Error && error.message.trim()) return error.message;
  if (typeof error === "string" && error.trim()) return error;
  return fallback;
}
