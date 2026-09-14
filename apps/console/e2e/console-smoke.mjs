import assert from "node:assert/strict";
import { chromium } from "playwright";

const baseURL = process.env.CONSOLE_BASE_URL || "http://localhost:18088";
const password = process.env.ADMIN_PASSWORD || "change-me-before-production";
const browser = await chromium.launch({ headless: true });

try {
  const page = await browser.newPage();
  await page.goto(`${baseURL}/login`);
  await page.locator(".login-panel input").nth(0).fill("admin");
  await page.locator(".login-panel input").nth(1).fill(password);
  await page.getByRole("button", { name: "登录控制台" }).click();
  await page.waitForURL(`${baseURL}/`);

  const views = [
    ["资产中心", "REGISTERED TARGETS"],
    ["基线审批", "BASELINE REVIEW"],
    ["检测任务", "DETECTION PIPELINE"],
    ["事件中心", "INCIDENT RESPONSE"],
    ["证据中心", "IMMUTABLE ARTIFACTS"],
  ];
  for (const [view, expectedText] of views) {
    await page.getByRole("button", { name: new RegExp(view) }).click();
    await assertPageHasContent(page, expectedText);
  }

  await page.getByRole("button", { name: "退出登录" }).click();
  await page.waitForURL(`${baseURL}/login`);
} finally {
  await browser.close();
}

async function assertPageHasContent(page, title) {
  await page.locator("main").waitFor();
  assert.ok((await page.locator("main").innerText()).includes(title), `console view did not render: ${title}`);
}
