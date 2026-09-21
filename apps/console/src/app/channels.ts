import type { AlertChannel } from "../types/domain";

export type ChannelField = "url" | "recipients" | "secret";

// Which fields the create dialog has to collect per channel type. This mirrors
// the server's validation in createAlertChannel: EMAIL requires a recipient
// list and forces the URL empty; the three webhook-shaped types all require a
// URL, and WEBHOOK and DINGTALK additionally use a signing secret.
const CHANNEL_FIELDS: Record<AlertChannel["type"], ChannelField[]> = {
  WEBHOOK: ["url", "secret"],
  WECHAT_WORK: ["url"],
  DINGTALK: ["url", "secret"],
  EMAIL: ["recipients"],
};

export const CHANNEL_LABELS: Record<AlertChannel["type"], string> = {
  WECHAT_WORK: "企业微信",
  WEBHOOK: "Webhook",
  DINGTALK: "钉钉",
  EMAIL: "邮件",
};

export function channelFields(type: AlertChannel["type"]): ChannelField[] {
  return CHANNEL_FIELDS[type] ?? CHANNEL_FIELDS.WEBHOOK;
}

export function channelNeeds(type: AlertChannel["type"], field: ChannelField): boolean {
  return channelFields(type).includes(field);
}
