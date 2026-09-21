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
  // Closed explicitly rather than left to the next navigation: the drawer's
  // overlay covers the page, so anything after this would be clicking through
  // it and failing for a reason that has nothing to do with what is tested.
  await page.locator(".el-drawer__close-btn").click();
  await page.locator(".el-drawer").waitFor({ state: "detached", timeout: 15000 });

  // The short-lived link is minted on demand and is the one credential this
  // screen hands out. It is deliberately NOT fetched from here: the API signs
  // it for S3_ENDPOINT (http://minio:9000 in this stack), a name that only
  // resolves inside the compose network, so a request from outside it fails
  // with ENOTFOUND no matter how correct the link is. What can be asserted
  // from the browser is that a link was minted, that it is a credential rather
  // than the control-plane path, and that it carries a real expiry -- which is
  // the whole claim the countdown beside it makes.
  await page.goto(`${baseURL}/#/evidence`);
  await page.getByRole("button", { name: "短时链接" }).first().click();
  await page.locator(".signed-url").waitFor({ timeout: 15000 });
  const signedUrl = (await page.locator(".signed-url").innerText()).trim();
  assert.ok(signedUrl.length > 0, "the short-lived link rendered empty");
  assert.ok(
    /^https?:\/\//.test(signedUrl),
    `expected an absolute presigned url from object storage, got: ${signedUrl}`,
  );
  assert.ok(
    /X-Amz-Signature=|token=/.test(signedUrl),
    "the issued link does not look like a signed credential",
  );
  assert.ok(
    /\d+ (秒|分)/.test(await page.locator(".signed-body").innerText()),
    "the issued link did not show a countdown to its expiry",
  );

  // The authenticated download stays the default and is the fallback when
  // signing is unconfigured, so it has to keep working either way.
  const downloadHref = await page.locator("a.evidence-link").first().getAttribute("href");
  assert.ok(downloadHref?.startsWith("/api/v1/evidence/"), "the authenticated download link disappeared");

  await assertIncidentResponse(page, baseURL);
  await assertAlertChannelAuthoring(page, baseURL);
  await assertTenantDeletion(page, baseURL);

  await page.getByRole("button", { name: "退出登录" }).click();
  await page.locator(".login-panel").waitFor({ timeout: 15000 });
} finally {
  await browser.close();
}

function loginField(page, label) {
  return page.locator(".login-panel .el-form-item", { hasText: label }).locator("input");
}

// Element Plus form labels are not `for`-bound, so a field is found by the
// form item that contains its label text.
function fieldIn(scope, label) {
  return scope.locator(".el-form-item", { hasText: label }).locator("input").first();
}

async function assertPageHasContent(page, title) {
  await page.locator("main").waitFor();
  assert.ok((await page.locator("main").innerText()).includes(title), `console view did not render: ${title}`);
}


// Incident response is the flow the whole platform exists to serve, and until
// this walk it was the only major screen the browser test never touched: it
// asserted the view rendered and stopped there. The two things worth proving
// are that a status transition moves the row and that a note reaches the
// timeline without overwriting it.
//
// The incidents come from the compose smoke, which runs detection before this
// script and is what creates them. If none is present the walk fails rather
// than skipping -- a silently skipped assertion is worse than none, because it
// reports green for a screen it never opened.
async function assertIncidentResponse(page, baseURL) {
  await page.goto(`${baseURL}/#/incidents`);
  await assertPageHasContent(page, "INCIDENT RESPONSE");

  const rows = page.locator(".el-table__row");
  assert.ok((await rows.count()) > 0, "the compose smoke should have produced at least one incident before this runs");

  // The drawer is where the transitions and the comment box live, and it is
  // opened by clicking the row rather than a button -- so this is also the
  // assertion that the row-click affordance still works.
  await rows.first().click();
  await page.locator(".el-drawer").waitFor({ timeout: 15000 });
  const drawer = page.locator(".el-drawer");

  // The comment must land on the timeline and must not replace it. A note that
  // overwrote the entry it was appended to would still "work" as far as the
  // request goes, so this checks the timeline grew.
  const note = `E2E 备注 ${Date.now()}`;
  const timelineBefore = await drawer.locator(".el-timeline-item").count();
  const commentBox = drawer.getByPlaceholder(/记录与客户或运维的沟通/);
  await commentBox.fill(note);
  // The submit button is disabled below two characters, so this also proves the
  // guard is wired rather than decorative.
  const submitComment = drawer.getByRole("button", { name: "记录备注" });
  for (let attempt = 0; attempt < 50 && (await submitComment.isDisabled()); attempt += 1) {
    await page.waitForTimeout(100);
  }
  assert.equal(await submitComment.isEnabled(), true, "the comment submit stayed disabled after typing");
  await submitComment.click();
  // Scoped to the timeline entry rather than a page-wide text match, which the
  // textarea the note was typed into would also satisfy.
  await drawer.locator(".timeline-note", { hasText: note }).waitFor({ timeout: 15000 });
  assert.ok(
    (await drawer.locator(".el-timeline-item").count()) > timelineBefore,
    "the note replaced the timeline instead of being appended to it",
  );

  // A transition is only offered for the status the incident is actually in, so
  // the walk takes whichever one is available rather than assuming OPEN --
  // earlier runs of this script leave incidents in whatever state they left
  // them, and the compose smoke transitions one to ACKNOWLEDGED already. If no
  // incident is in a transitionable state the walk fails rather than skipping:
  // a silently skipped assertion reports green for a flow it never exercised.
  //
  // RESOLVED is deliberately not in this list. Reaching it opens a prompt for a
  // disposal note, and a walk that drove that would be asserting the prompt as
  // much as the transition; the note itself is covered by the resolution field
  // the prompt feeds, which is exercised in the compose smoke's API half.
  const transitionable = [
    ["认领事件", "ACKNOWLEDGED"],
    ["开始调查", "INVESTIGATING"],
  ];
  let moved = false;
  let movedTitle = "";
  for (const [label, expectedStatus] of transitionable) {
    // A fresh drawer on the row that offers this transition. Taking the first
    // row for every step would only work while it happened to be in the right
    // state, which is exactly what earlier runs change.
    await page.locator(".el-drawer__close-btn").click().catch(() => {});
    await page.locator(".el-drawer").waitFor({ state: "detached", timeout: 15000 }).catch(() => {});
    const candidate = page.locator(".el-table__row").filter({ has: page.getByRole("button", { name: label }) }).first();
    if ((await candidate.count()) === 0) continue;
    await candidate.click();
    await page.locator(".el-drawer").waitFor({ timeout: 15000 });
    movedTitle = (await page.locator(".el-drawer .incident-detail-head strong").innerText()).trim();
    await page.locator(".el-drawer").getByRole("button", { name: label }).click();
    // The status tag in the drawer head is what the operator reads, so it is
    // what this asserts rather than a re-fetch.
    await page.locator(".el-drawer").locator(".detail-status", { hasText: expectedStatus }).waitFor({ timeout: 15000 });
    moved = true;
    break;
  }
  assert.ok(moved, `no incident offered a transition; the list showed: ${(await page.locator(".el-table").innerText()).slice(0, 300)}`);

  await page.locator(".el-drawer__close-btn").click();
  await page.locator(".el-drawer").waitFor({ state: "detached", timeout: 15000 });

  // Reopened by title -- the same incident, not whichever is first -- to prove
  // the transition was persisted rather than only reflected in the drawer that
  // made it. A status that moved in the UI but not on the server would look
  // identical until the page was reloaded.
  const movedRow = page.locator(".el-table__row", { hasText: movedTitle });
  await movedRow.first().click();
  await page.locator(".el-drawer").waitFor({ timeout: 15000 });
  const reopened = page.locator(".el-drawer");
  const status = (await reopened.locator(".detail-status").innerText()).trim();
  assert.notEqual(status, "OPEN", `the incident was still OPEN after its transition: ${movedTitle}`);
  // The affordance that was used must be gone -- a stale one would offer a
  // transition the server would reject.
  assert.equal(
    await reopened.getByRole("button", { name: "认领事件" }).count(),
    0,
    `an incident in ${status} still offered the acknowledgement it had already made`,
  );
  await page.locator(".el-drawer__close-btn").click();
  await page.locator(".el-drawer").waitFor({ state: "detached", timeout: 15000 });
}

// The channel dialog is the only place a credential is ever typed, and the rule
// that makes editing it safe is that leaving the secret blank keeps the stored
// one. Nothing in the UI can prove that -- the field is empty on open either way
// -- so this walks the two halves separately: a rename with the field untouched
// has to leave the row working, and an explicit delete has to remove it.
async function assertAlertChannelAuthoring(page, baseURL) {
  await page.goto(`${baseURL}/#/channels`);
  await assertPageHasContent(page, "NOTIFICATION ROUTES");

  const name = `e2e-channel-${Date.now()}`;
  await page.getByRole("button", { name: "新增渠道" }).click();
  const create = page.locator(".el-dialog", { hasText: "新增告警渠道" });
  // Webhook rather than the default type: it is the one that carries a signing
  // secret, and the whole point of this walk is the rule about that field.
  await create.locator(".el-radio-button", { hasText: "Webhook" }).click();
  await fieldIn(create, "渠道名称").fill(name);
  await fieldIn(create, "通知地址").fill("https://example.com/e2e-hook");
  await fieldIn(create, "签名密钥").fill("e2e-channel-secret");
  await create.getByRole("button", { name: "创建渠道" }).click();

  const row = page.locator(".el-table__row", { hasText: name });
  await row.waitFor({ timeout: 15000 });

  // The edit dialog opens with an empty secret box. That is the whole contract:
  // the server never sends the plaintext back, so there is nothing to prefill.
  // The address box is empty for the same reason -- the list gives a redacted
  // one -- so both have to be re-supplied rather than echoed back.
  await row.getByRole("button", { name: "编辑" }).click();
  const edit = page.locator(".el-dialog", { hasText: "编辑告警渠道" });
  await edit.waitFor({ timeout: 15000 });
  const renamed = `${name}-renamed`;
  await fieldIn(edit, "渠道名称").fill(renamed);
  assert.equal(
    await fieldIn(edit, "签名密钥").inputValue(),
    "",
    "the edit dialog must not prefill the stored secret",
  );
  assert.equal(
    await fieldIn(edit, "通知地址").inputValue(),
    "",
    "the edit dialog must not prefill the redacted address",
  );
  // A save that leaves the address blank is refused before the request, because
  // an empty URL is not a channel and the dialog cannot show what the real one
  // was.
  await edit.getByRole("button", { name: "保存渠道" }).click();
  await page.locator(".el-message", { hasText: "请重新填写通知地址" }).waitFor({ timeout: 10000 });
  assert.equal(
    await page.locator(".el-table__row", { hasText: renamed }).count(),
    0,
    "a save with no address was applied anyway",
  );

  await fieldIn(edit, "通知地址").fill("https://example.com/e2e-hook-renamed");
  await edit.getByRole("button", { name: "保存渠道" }).click();

  const renamedRow = page.locator(".el-table__row", { hasText: renamed });
  await renamedRow.waitFor({ timeout: 15000 });

  // Deletion is confirmed rather than immediate, and the confirmation names the
  // consequence, so the row survives until the second click.
  await renamedRow.getByRole("button", { name: "删除" }).click();
  const confirm = page.locator(".el-dialog", { hasText: "删除告警渠道" });
  await confirm.waitFor({ timeout: 15000 });
  assert.equal(
    await page.locator(".el-table__row", { hasText: renamed }).count(),
    1,
    "the channel was deleted before the confirmation",
  );
  await confirm.getByRole("button", { name: "删除渠道" }).click();
  await page.locator(".el-table__row", { hasText: renamed }).waitFor({ state: "detached", timeout: 15000 });
}

// Tenant deletion is the one destructive flow in the console, and the whole
// point of its dialog is that the delete button stays dead until the slug is
// typed exactly. So the assertion that matters is the negative one -- a wrong
// slug must leave the button disabled -- plus the audit entry, because the
// server writes it before the delete and it is the only surviving record.
async function assertTenantDeletion(page, baseURL) {
  const slug = `e2e-delete-${Date.now()}`;
  await page.goto(`${baseURL}/#/tenants`);

  await page.getByRole("button", { name: "新增租户" }).click();
  const create = page.locator(".el-dialog", { hasText: "新增租户" });
  // Located the same way loginField does it: Element Plus renders the label
  // beside the input rather than as a `for`-bound <label>, so getByLabel does
  // not associate them.
  await fieldIn(create, "租户名称").fill("E2E 删除用例");
  await fieldIn(create, "租户标识（slug）").fill(slug);
  await fieldIn(create, "初始管理员账号").fill("e2e-admin");
  await fieldIn(create, "初始管理员密码").fill("e2e-admin-password");
  await create.getByRole("button", { name: "保存租户" }).click();
  await page.locator(".el-table__row", { hasText: slug }).waitFor({ timeout: 15000 });

  // The first row is the newest, and this tenant is the newest.
  await page.locator(".el-table__row", { hasText: slug }).getByRole("button", { name: "删除" }).click();
  const dialog = page.locator(".el-dialog", { hasText: "删除租户" });
  await dialog.waitFor({ timeout: 15000 });
  const confirmButton = dialog.getByRole("button", { name: "删除租户" });

  await dialog.getByRole("textbox").fill("wrong-slug");
  assert.equal(await confirmButton.isDisabled(), true, "the delete button must stay disabled for a mismatched slug");

  await dialog.getByRole("textbox").fill(slug);
  // Waiting for the enabled state rather than asserting it once: Vue applies
  // the model update on the next tick, so an immediate click can land while the
  // button is still disabled and silently do nothing.
  await confirmButton.waitFor({ state: "visible", timeout: 5000 });
  for (let attempt = 0; attempt < 50 && (await confirmButton.isDisabled()); attempt += 1) {
    await page.waitForTimeout(100);
  }
  assert.equal(await confirmButton.isEnabled(), true, "the delete button must enable once the slug matches");
  await confirmButton.click();

  // A tenant is never empty -- creating one seeds its first administrator -- so
  // the first attempt always comes back 409 TENANT_NOT_EMPTY and the dialog
  // moves to its cascade state rather than deleting. This is the branch that
  // matters: the plain delete must not be the one that succeeds.
  const cascadeButton = dialog.getByRole("button", { name: "确认级联删除" });
  await cascadeButton.waitFor({ timeout: 15000 });
  assert.ok(
    (await dialog.innerText()).includes("租户仍有关联数据"),
    "the cascade step did not surface the server's reason",
  );
  assert.equal(await page.locator(".el-table__row", { hasText: slug }).count(), 1, "the tenant was deleted before the cascade was confirmed");

  await cascadeButton.click();

  await page.locator(".el-table__row", { hasText: slug }).waitFor({ state: "detached", timeout: 15000 });

  await page.goto(`${baseURL}/#/audits`);
  await assertPageHasContent(page, "AUDIT TRAIL");
  // Waited for rather than read once: the audit list arrives with the initial
  // refresh, and reading immediately after navigation catches the table before
  // its rows paint.
  await page.locator(".el-table__row", { hasText: "tenant.delete" }).first().waitFor({ timeout: 15000 });

  // The seeded default tenant must never offer the operation at all.
  await page.goto(`${baseURL}/#/tenants`);
  const defaultRow = page.locator(".el-table__row", { hasText: "default" }).first();
  await defaultRow.waitFor({ timeout: 15000 });
  assert.equal(await defaultRow.getByRole("button", { name: "删除" }).isDisabled(), true, "the default tenant must not be deletable");
}
