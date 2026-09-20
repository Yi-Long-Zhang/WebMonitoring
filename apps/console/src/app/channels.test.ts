import { describe, expect, it } from "vitest";
import { CHANNEL_LABELS, channelFields, channelNeeds } from "./channels";
import type { AlertChannel } from "../types/domain";

const TYPES: AlertChannel["type"][] = ["WEBHOOK", "WECHAT_WORK", "DINGTALK", "EMAIL"];

// The dialog offers one radio button per labelled type, so a type the server
// accepts but this map omits would be unreachable from the console -- which is
// exactly how DINGTALK and EMAIL were missing before.
describe("channel field requirements", () => {
  it("offers every type the server accepts", () => {
    expect(Object.keys(CHANNEL_LABELS).sort()).toEqual([...TYPES].sort());
    for (const type of TYPES) {
      expect(CHANNEL_LABELS[type]).not.toBe("");
    }
  });

  it("requires a signing secret for WEBHOOK and DINGTALK only", () => {
    expect(channelNeeds("WEBHOOK", "secret")).toBe(true);
    expect(channelNeeds("DINGTALK", "secret")).toBe(true);
    expect(channelNeeds("WECHAT_WORK", "secret")).toBe(false);
    expect(channelNeeds("EMAIL", "secret")).toBe(false);
  });

  it("takes a URL for every webhook-shaped type and a recipient list for EMAIL", () => {
    expect(channelNeeds("WEBHOOK", "url")).toBe(true);
    expect(channelNeeds("WECHAT_WORK", "url")).toBe(true);
    expect(channelNeeds("DINGTALK", "url")).toBe(true);
    // The server forces EMAIL's URL to empty, so the dialog must not ask for it.
    expect(channelNeeds("EMAIL", "url")).toBe(false);

    expect(channelNeeds("EMAIL", "recipients")).toBe(true);
    for (const type of ["WEBHOOK", "WECHAT_WORK", "DINGTALK"] as const) {
      expect(channelNeeds(type, "recipients")).toBe(false);
    }
  });

  it("asks for exactly one of url and recipients per type", () => {
    // The dialog renders url and recipients as sibling form items; a type that
    // needed both or neither would render a form the server rejects.
    for (const type of TYPES) {
      const addressFields = channelFields(type).filter((field) => field === "url" || field === "recipients");
      expect(addressFields).toHaveLength(1);
    }
  });

  it("falls back to the WEBHOOK shape for an unknown type", () => {
    expect(channelFields("CARRIER_PIGEON" as AlertChannel["type"])).toEqual(channelFields("WEBHOOK"));
  });
});
