import { describe, expect, it } from "vitest";
import { VIEWS, isView } from "./views";

// isView is the only thing standing between a hand-edited hash and the view
// switch, so the negative cases are the point of this file. Before it existed,
// an unknown segment fell through to the v-else branch and silently rendered the
// audit page.
describe("view guard", () => {
  it("accepts every view it declares", () => {
    for (const view of VIEWS) {
      expect(isView(view)).toBe(true);
    }
  });

  it("rejects a segment that is not a view", () => {
    expect(isView("nope")).toBe(false);
    expect(isView("overview ")).toBe(false);
    expect(isView("Overview")).toBe(false);
    expect(isView("")).toBe(false);
  });

  it("rejects the non-string shapes a route param can arrive as", () => {
    // vue-router hands back an array for a repeated param and undefined for an
    // absent one, neither of which is a view.
    expect(isView(undefined)).toBe(false);
    expect(isView(null)).toBe(false);
    expect(isView(["overview"])).toBe(false);
    expect(isView(1)).toBe(false);
  });

  it("declares each view once", () => {
    expect(new Set(VIEWS).size).toBe(VIEWS.length);
  });
});
