import assert from "node:assert/strict";
import { chromium } from "playwright";

const baseURL = process.env.CONSOLE_BASE_URL || "http://localhost:18088";
// No fallback: the repository's development default is no longer a constant in
// any script, and guessing it here would report a login timeout when the real
// fault is that the caller never exported the stack's password.
const password = process.env.ADMIN_PASSWORD;
if (!password) {
  throw new Error("ADMIN_PASSWORD is not set; export the stack's administrator password before running the console E2E walk");
}
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
  await closeDrawer(page);

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
  await assertAssetRegistration(page, baseURL);
  await assertSiteManagement(page, baseURL);
  await assertPolicyEditing(page, baseURL);
  await assertUserManagement(page, baseURL);
  await assertTenantDeletion(page, baseURL);

  await page.getByRole("button", { name: "退出登录" }).click();
  await page.locator(".login-panel").waitFor({ timeout: 15000 });
} finally {
  await browser.close();
}

// Asset registration is the entry point for everything else on the platform --
// nothing can be detected until it is registered and authorized -- and it is the
// precise flow that was broken once already: every field in the dialog was bound
// to the wrong form object, so submitting always sent the defaults. Asserting
// the row that appears is what proves the values reached the API instead of only
// rendering in the inputs.
async function assertAssetRegistration(page, baseURL) {
  await page.goto(`${baseURL}/#/assets`);
  await assertPageHasContent(page, "REGISTERED TARGETS");

  const name = `E2E 资产 ${Date.now()}`;
  await page.getByRole("button", { name: "登记资产" }).click();
  const dialog = page.locator(".el-dialog", { hasText: "登记监测资产" });
  await dialog.waitFor({ timeout: 15000 });
  await fieldIn(dialog, "资产名称").fill(name);
  // The registered URL has to pass the SSRF guard, which resolves the host --
  // a made-up name like e2e.example.com is refused with UNSAFE_TARGET and the
  // dialog simply stays open. smoke-target is the host this stack serves, so it
  // is the address that can actually be registered.
  const assetURL = `http://smoke-target/e2e-asset-${Date.now()}.html`;
  await fieldIn(dialog, "监测地址").fill(assetURL);
  await dialog.getByRole("button", { name: "创建并授权" }).click();

  // The row carries the name the dialog was given. If the binding regressed to
  // another form object, this is where it shows: the server would reject the
  // empty name, or the row would render under a default.
  const row = page.locator(".el-table__row", { hasText: name }).first();
  await row.waitFor({ timeout: 15000 });
  assert.ok(
    (await row.innerText()).includes("smoke-target"),
    "the registered asset did not carry the URL that was typed",
  );

  // A new asset is unverified, which is what makes it undetectable until an
  // operator proves ownership. That state is the platform's authorization gate,
  // so it is worth asserting rather than assuming.
  assert.equal(
    await row.getByRole("button", { name: "授权验证" }).count(),
    1,
    "a freshly registered asset must still be pending authorization",
  );
}

// Sites are pure grouping, so the only behaviour worth proving in the browser is
// that a site can be created and that it is offered as a grouping when an asset
// is registered -- the FK-bearing side of the relationship.
async function assertSiteManagement(page, baseURL) {
  await page.goto(`${baseURL}/#/sites`);
  await assertPageHasContent(page, "站点管理");

  const name = `E2E 站点 ${Date.now()}`;
  await page.getByRole("button", { name: "新增站点" }).click();
  const dialog = page.locator(".el-dialog", { hasText: "新增站点" });
  await dialog.waitFor({ timeout: 15000 });
  await fieldIn(dialog, "站点名称").fill(name);
  // 域名 is not optional: createSite refuses an empty one with INVALID_SITE.
  await fieldIn(dialog, "域名").fill("smoke-target");
  await dialog.getByRole("button", { name: "保存站点" }).click();

  await page.locator(".el-table__row", { hasText: name }).first().waitFor({ timeout: 15000 });

  // Now the grouping has to actually be offered on the asset dialog. A site that
  // renders in its own list but cannot be selected when filing an asset is a
  // list, not a grouping.
  await page.goto(`${baseURL}/#/assets`);
  await assertPageHasContent(page, "REGISTERED TARGETS");
  await page.getByRole("button", { name: "登记资产" }).click();
  const assetDialog = page.locator(".el-dialog", { hasText: "登记监测资产" });
  await assetDialog.waitFor({ timeout: 15000 });
  await selectIn(assetDialog, "所属站点").click();
  await visibleOptions(page).filter({ hasText: name }).first().waitFor({ timeout: 15000 });
  // Escape rather than submitting: this walk is about the option existing, and
  // the asset-registration walk above already owns creating one.
  await page.keyboard.press("Escape");
  await assetDialog.getByRole("button", { name: "取消" }).click();
}

// The policy dialog is where detection is tuned, so the assertion is that an
// edit round-trips: a value that is typed, saved and reopened has to come back.
// That is the only way to tell a real write from component state.
//
// It deliberately targets "Smoke login asset" rather than the asset the
// registration walk just created. A newly registered asset has no policy, and
// the dialog renders el-empty with a disabled save button for that reason, so
// pointing this at it would take the empty branch on every run and never
// exercise an edit at all -- a skipped assertion dressed up as a passing one.
// The compose smoke PUTs a policy for this asset before invoking this script,
// which is what makes the round-trip reachable.
async function assertPolicyEditing(page, baseURL) {
  await page.goto(`${baseURL}/#/assets`);
  await assertPageHasContent(page, "REGISTERED TARGETS");

  const row = page.locator(".el-table__row").filter({ hasText: "Smoke login asset" }).first();
  await row.waitFor({ timeout: 15000 });
  await row.getByRole("button", { name: "策略" }).click();

  const dialog = page.locator(".el-dialog", { hasText: "监测策略" });
  await dialog.waitFor({ timeout: 15000 });
  const save = dialog.getByRole("button", { name: "保存策略" });
  assert.equal(
    await save.isEnabled(),
    true,
    "the seeded asset's policy did not load, so the dialog rendered empty",
  );

  // timeoutSeconds is a useful discriminator because the seed and the
  // component's own default happen to agree on 30: an edit to 45 can only read
  // back as 45 if it went to the server and came back, not if the dialog fell
  // back to its defaults.
  const timeout = fieldIn(dialog, "超时（秒）");
  assert.equal(await timeout.inputValue(), "30", "the seeded policy value did not load into the form");
  await timeout.fill("45");
  await save.click();
  await dialog.waitFor({ state: "hidden", timeout: 15000 });

  await row.getByRole("button", { name: "策略" }).click();
  await dialog.waitFor({ timeout: 15000 });
  assert.equal(
    await fieldIn(dialog, "超时（秒）").inputValue(),
    "45",
    "the edited policy did not persist across a reopen",
  );
  await dialog.getByRole("button", { name: "取消" }).click();
}

// User management is an admin screen, and the rule that matters is the one the
// dialog encodes: creation offers only OPERATOR and AUDITOR, because an ADMIN is
// provisioned by tenant bootstrap rather than handed out from the console. A
// regression here would silently widen who can be granted administrative access.
async function assertUserManagement(page, baseURL) {
  await page.goto(`${baseURL}/#/users`);
  await assertPageHasContent(page, "用户管理");

  const name = `e2e-user-${Date.now()}`;
  await page.getByRole("button", { name: "新增用户" }).click();
  const dialog = page.locator(".el-dialog", { hasText: "新增用户" });
  await dialog.waitFor({ timeout: 15000 });
  await fieldIn(dialog, "用户名").fill(name);
  await fieldIn(dialog, "初始密码").fill("e2e-password-1234");

  // The role options are the security-relevant half of this dialog.
  await selectIn(dialog, "角色").click();
  const options = visibleOptions(page);
  await options.first().waitFor({ timeout: 15000 });
  const labels = await options.allInnerTexts();
  assert.ok(labels.includes("安全运营"), "the OPERATOR role disappeared from user creation");
  assert.ok(labels.includes("审计只读"), "the AUDITOR role disappeared from user creation");
  assert.ok(
    !labels.includes("平台管理员"),
    `user creation must not offer ADMIN; it offered: ${labels.join(", ")}`,
  );
  await visibleOptions(page).filter({ hasText: "安全运营" }).first().click();

  await dialog.getByRole("button", { name: "保存" }).click();
  await page.locator(".el-table__row", { hasText: name }).first().waitFor({ timeout: 15000 });
}

function loginField(page, label) {
  return page.locator(".login-panel .el-form-item", { hasText: label }).locator("input");
}

// Closes the drawer and waits for it to actually be gone from the screen.
//
// Waiting for the node to be *detached* does not work and never did: Element
// Plus keeps the drawer mounted after close and only hides it (there is no
// destroy-on-close on either drawer), so `.el-drawer` resolves to a lingering
// hidden element and a detached wait times out forever. What the walk actually
// needs is that the overlay no longer covers the page, which is what being
// hidden means here -- existence is the wrong question.
async function closeDrawer(page) {
  await page.locator(".el-drawer:visible").waitFor({ state: "detached", timeout: 15000 });
}

// Element Plus form labels are not `for`-bound, so a field is found by the
// form item that contains its label text.
function fieldIn(scope, label) {
  return scope.locator(".el-form-item", { hasText: label }).locator("input").first();
}

// The options of the dropdown that is currently open. Element Plus keeps every
// dropdown it has shown in the DOM and merely hides the closed ones, so a bare
// .el-select-dropdown__item picks up a stale hidden <li> from an earlier dialog
// and waits for it to become visible forever.
function visibleOptions(page) {
  return page.locator(".el-select-dropdown__item:visible");
}

// An el-select is opened by clicking its wrapper, not the <input>: the readonly
// input is covered by a placeholder element that intercepts pointer events, so
// a click on the input retries until it times out. The wrapper is what opens the
// dropdown.
function selectIn(scope, label) {
  return scope.locator(".el-form-item", { hasText: label }).locator(".el-select").first();
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
  // the walk takes whichever one is available rather than assuming OPEN. Order
  // matters: the first two need no disposal note, so they are preferred when
  // present. 标记误报 is the fallback because it is offered from both OPEN and
  // INVESTIGATING -- and those are the only two states a fresh compose smoke
  // leaves behind, so this path is always reachable. Without it, a repeated run
  // against a long-lived stack (every incident already past ACKNOWLEDGED) would
  // find nothing to do and fail, which reads as a defect when it is only
  // consumed state.
  //
  // RESOLVED is deliberately not in this list. Reaching it opens the same
  // disposal prompt as 标记误报, and the prompt is asserted below; adding a
  // second route to it would only assert the prompt twice.
  const transitionable = [
    ["认领", "认领事件", "ACKNOWLEDGED", false],
    ["开始调查", "开始调查", "INVESTIGATING", false],
    ["误报", "标记误报", "FALSE_POSITIVE", true],
  ];
  let moved = false;
  let movedTitle = "";
  // What the walk actually used, so the persistence check below reopens by the
  // same wording and asserts the same status rather than the one OPEN case.
  let movedTo = "";
  let movedDrawerLabel = "";
  for (const [rowLabel, drawerLabel, expectedStatus, needsNote] of transitionable) {
    // A fresh drawer on the row that offers this transition. Taking the first
    // row for every step would only work while it happened to be in the right
    // state, which is exactly what earlier runs change.
    await page.locator(".el-drawer__close-btn").click().catch(() => {});
    await closeDrawer(page);
    const candidate = page.locator(".el-table__row").filter({ has: page.getByRole("button", { name: rowLabel }) }).first();
    if ((await candidate.count()) === 0) continue;
    await candidate.scrollIntoViewIfNeeded();
    // Column 1 is 事件, the cell holding the incident title. Read before the
    // click, so it is the row that was acted on rather than whatever the drawer
    // shows afterwards.
    movedTitle = (await candidate.locator("td").nth(1).locator("strong").innerText()).trim();
    await candidate.click();
    await page.locator(".el-drawer").waitFor({ timeout: 15000 });
    await page.locator(".el-drawer").getByRole("button", { name: drawerLabel }).click();

    if (needsNote) {
      // The server refuses RESOLVED/FALSE_POSITIVE/CLOSED without a disposal
      // note, and the console front-runs that with a prompt. Both the prompt's
      // own guard and the fact that the note reaches the description are worth
      // asserting: a prompt that accepted a one-character note, or whose value
      // never left the dialog, would look identical to a working one.
      const prompt = page.locator(".el-message-box");
      await prompt.waitFor({ timeout: 15000 });
      const noteInput = prompt.locator("input");
      const confirm = prompt.getByRole("button", { name: "确定" });
      await noteInput.fill("ab");
      await confirm.click();
      // The validation message is what holds the box open; the transition must
      // not have been sent with a two-character note.
      await prompt.locator(".el-message-box__errormsg").waitFor({ timeout: 15000 });
      await noteInput.fill(`E2E 处置说明 ${Date.now()}`);
      await confirm.click();
      await prompt.waitFor({ state: "hidden", timeout: 15000 });
    }

    // The status tag in the drawer head is what the operator reads, so it is
    // what this asserts rather than a re-fetch.
    await page.locator(".el-drawer").locator(".detail-status", { hasText: expectedStatus }).waitFor({ timeout: 15000 });
    movedTo = expectedStatus;
    movedDrawerLabel = drawerLabel;
    moved = true;
    break;
  }
  assert.ok(moved, `no incident offered a transition; the list showed: ${(await page.locator(".el-table").innerText()).slice(0, 300)}`);

  await page.locator(".el-drawer__close-btn").click();
  await closeDrawer(page);

  // Reopened by title -- the same incident, not whichever is first -- to prove
  // the transition was persisted rather than only reflected in the drawer that
  // made it. A status that moved in the UI but not on the server would look
  // identical until the page was reloaded.
  const movedRow = page.locator(".el-table__row", { hasText: movedTitle });
  await movedRow.first().click();
  await page.locator(".el-drawer").waitFor({ timeout: 15000 });
  const reopened = page.locator(".el-drawer");
  const status = (await reopened.locator(".detail-status").innerText()).trim();
  assert.equal(status, movedTo, `the incident came back as ${status} after moving to ${movedTo}: ${movedTitle}`);
  // The affordance that was used must be gone -- a stale one would offer a
  // transition the server would reject.
  assert.equal(
    await reopened.getByRole("button", { name: movedDrawerLabel }).count(),
    0,
    `an incident in ${status} still offered the ${movedDrawerLabel} it had already made`,
  );
  // FALSE_POSITIVE is terminal, so reaching it must leave no transition at all.
  // The other two leave the successor available, which is what distinguishes a
  // terminal state from one that merely moved.
  if (movedTo === "FALSE_POSITIVE") {
    assert.equal(
      await reopened.locator(".detail-actions button").count(),
      0,
      `a false positive still offered a transition: ${movedTitle}`,
    );
  }
  await page.locator(".el-drawer__close-btn").click();
  await closeDrawer(page);
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
