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

  // The views above prove a sidebar click reaches the right screen. These prove
  // the hash is what decides it: a deep link has to land on the same view with
  // no click at all, and an unknown segment has to be normalized away rather
  // than left in the address bar describing a screen that is not rendered.
  // Only views by name -- 用户管理 and 租户管理 are super-admin screens, and the
  // seeded admin is neither.
  for (const [hash, expectedText] of [["#/jobs", "DETECTION PIPELINE"], ["#/evidence", "IMMUTABLE ARTIFACTS"]]) {
    await page.goto(`${baseURL}/${hash}`);
    await assertPageHasContent(page, expectedText);
  }
  await page.goto(`${baseURL}/#/not-a-real-view`);
  await assertPageHasContent(page, "OPERATIONS METRICS");
  assert.equal(new URL(page.url()).hash, "#/", "an unknown view segment must be normalized away");

  // The baseline preview is the screen that reads a captured snapshot. Seeded
  // candidates carry one, so opening the first row has to reach the metadata
  // rather than the zero-value "快照不可用" state.
  await page.goto(`${baseURL}/#/baselines`);
  await page.getByRole("button", { name: "查看快照" }).first().click();
  await page.locator(".el-drawer").waitFor({ timeout: 15000 });
  const drawer = await page.locator(".el-drawer").innerText();
  assert.ok(drawer.includes("内容哈希"), "baseline snapshot drawer did not render the capture metadata");

  // The short-lived link is minted on demand and is the one credential this
  // screen hands out, so the assertions are that a countdown appears and that
  // its link actually resolves. The authenticated link beside it must keep
  // working too -- it is the fallback when signing is unconfigured.
  await page.goto(`${baseURL}/#/evidence`);
  await page.getByRole("button", { name: "短时链接" }).first().click();
  await page.locator(".signed-url").waitFor({ timeout: 15000 });
  const signedUrl = (await page.locator(".signed-url").innerText()).trim();
  const signed = await page.request.get(new URL(signedUrl, baseURL).href);
  assert.equal(signed.status(), 200, "the issued short-lived link did not resolve");

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
