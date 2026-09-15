import assert from "node:assert/strict";
import { chromium } from "playwright";

const baseURL = process.env.CONSOLE_BASE_URL || "http://localhost:18088";
const password = process.env.ADMIN_PASSWORD || "change-me-before-production";
const browser = await chromium.launch({ headless: true });

try {
  const page = await browser.newPage();
  // The console routes with a hash history, so the login route is #/login.
  await page.goto(`${baseURL}/#/login`);
  // The panel has three fields (tenant, account, password). Selecting them by
  // label instead of by position keeps the credentials out of the wrong input
  // when a field is added, and the tenant is filled explicitly so the test
  // exercises slug-scoped login rather than the implicit default tenant.
  await loginField(page, "租户").fill("default");
  await loginField(page, "账号").fill("admin");
  await loginField(page, "密码").fill(password);
  await page.getByRole("button", { name: "登录控制台" }).click();
  // Waiting for the navigation shell is stable across hash-route changes,
  // unlike waitForURL, which would have to match the exact post-login hash.
  await page.getByRole("button", { name: /资产中心/ }).waitFor({ timeout: 15000 });

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
  await page.locator(".login-panel").waitFor({ timeout: 15000 });
} finally {
  await browser.close();
}

function loginField(page, label) {
  return page.locator(".login-panel .el-form-item", { hasText: label }).locator("input");
}

async function assertPageHasContent(page, title) {
  await page.locator("main").waitFor();
  assert.ok((await page.locator("main").innerText()).includes(title), `console view did not render: ${title}`);
}
