ALTER TABLE alert_channels DROP CONSTRAINT alert_channels_channel_type_check;
ALTER TABLE alert_channels ADD CONSTRAINT alert_channels_channel_type_check
    CHECK (channel_type IN ('WEBHOOK', 'WECHAT_WORK', 'DINGTALK', 'EMAIL'));
ALTER TABLE alert_channels ADD COLUMN recipients text NOT NULL DEFAULT '';
