import { describe, expect, it } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import PolicyDialog from "./PolicyDialog.vue";
import type { Asset, Policy } from "../../types/domain";

// The two list fields come back as null from the API even though the contract
// says they are non-null arrays (the handler builds them with
// append([]string(nil), input...), which yields nil for an empty input). The
// dialog's watcher reads them, so a null there used to throw -- and the failure
// was invisible: the form kept its defaults, and the default timeout is 30, the
// same number the compose smoke's seed uses. A form silently showing somebody
// else's numbers is worse than one that fails.

const policy: Policy = {
  id: "p1",
  assetId: "a1",
  version: 1,
  httpIntervalSeconds: 300,
  browserIntervalSeconds: 1800,
  timeoutSeconds: 30,
  maxResponseBytes: 1048576,
  ignoreSelectors: [],
  trustedDomains: [],
};

const asset = { id: "a1", name: "官网首页" } as Asset;

// Element Plus renders the dialog body one tick after it is told to open, so the
// mount has to settle before the form exists at all.
async function render(overrides: Partial<Policy> = {}) {
  const wrapper = mount(PolicyDialog, {
    props: { modelValue: true, asset, policy: { ...policy, ...overrides } },
    attachTo: document.body,
  });
  await flushPromises();
  return wrapper;
}

function textareas(wrapper: Awaited<ReturnType<typeof render>>) {
  return wrapper.findAll("textarea").map((node) => (node.element as HTMLTextAreaElement).value);
}

describe("PolicyDialog", () => {
  it("shows the stored values, including both lists", async () => {
    const wrapper = await render({
      httpIntervalSeconds: 120,
      ignoreSelectors: ["#banner", ".ad"],
      trustedDomains: ["example.com"],
    });

    const values = textareas(wrapper);
    expect(values).toHaveLength(2);
    expect(values).toContain("#banner\n.ad");
    expect(values).toContain("example.com");
  });

  it("treats a null list as an empty one instead of throwing", async () => {
    // The shape the live API answers, taken from GET /api/v1/policies.
    const wrapper = await render({
      ignoreSelectors: null as unknown as string[],
      trustedDomains: null as unknown as string[],
    });

    const values = textareas(wrapper);
    expect(values).toHaveLength(2);
    expect(values).toEqual(["", ""]);
  });
});
