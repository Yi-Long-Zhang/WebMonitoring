import http from "node:http";
import crypto from "node:crypto";
import { access } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { chromium, type BrowserContext, type Page } from "playwright";
import { assertSafeTarget, probeTLSFingerprint } from "./security.js";

const port = Number(process.env.PORT || 8081);
const workerToken = process.env.WORKER_TOKEN || "";
const maxResponseBytes = 10 * 1024 * 1024;

// LoginRequest carries decrypted customer credentials. ADR-0004 explains why the
// control plane sends plaintext rather than sharing the master key: nothing in
// this worker may log, echo or return these values.
type LoginRequest = {
  loginUrl: string;
  usernameField: string;
  passwordField: string;
  extraFields?: Record<string, string>;
  submitSelector?: string;
  username: string;
  password: string;
};

type CheckRequest = {
  url: string;
  timeoutSeconds?: number;
  maxResponseBytes?: number;
  ignoreSelectors?: string[];
  allowPrivateTargets?: boolean;
  auth?: LoginRequest;
};

// RequestGuard accumulates what the context-level route interceptor saw.
// Recording is off during sign-in so the login page's own resources do not end
// up in the target snapshot, where they would show up as spurious differences
// between runs.
type RequestGuard = {
  requests: string[];
  blockedRequests: string[];
  recording: boolean;
  blockedDuringLogin: number;
};

type BrowserEvents = {
  resourceRequests?: string[];
  blockedRequests?: string[];
  popups?: string[];
  dialogs?: string[];
  downloads?: string[];
  responseHeaders?: Record<string, string[]>;
  resolvedIps?: string[];
  tlsFingerprint?: string;
};

type Snapshot = {
  url: string;
  finalUrl: string;
  statusCode: number;
  title: string;
  contentHash: string;
  textHash: string;
  screenshotHash?: string;
  scripts: Array<{ url?: string; inlineHash?: string }>;
  links: string[];
  iframes: string[];
  forms: Array<{ action: string; method: string }>;
  redirectChain: string[];
  resolvedIps: string[];
  tlsFingerprint?: string;
  responseHeaders: Record<string, string[]>;
  resourceRequests?: string[];
  blockedRequests?: string[];
  popups?: string[];
  dialogs?: string[];
  downloads?: string[];
  durationMillis: number;
  capturedAt: string;
};

const server = http.createServer(async (request, response) => {
  if (request.method === "GET" && request.url === "/healthz") {
    writeJSON(response, 200, { status: "ok" });
    return;
  }
  if (request.method === "GET" && request.url === "/readyz") {
    try {
      await access(chromium.executablePath());
      writeJSON(response, 200, { status: "ready" });
    } catch {
      writeJSON(response, 503, { status: "not_ready" });
    }
    return;
  }
  if (request.method !== "POST" || request.url !== "/check") {
    writeJSON(response, 404, { code: "NOT_FOUND" });
    return;
  }
  if (!workerToken || request.headers.authorization !== `Bearer ${workerToken}`) {
    writeJSON(response, 401, { code: "UNAUTHORIZED" });
    return;
  }
  try {
    const input = await readJSON<CheckRequest>(request);
    const result = await check(input);
    writeJSON(response, 200, result);
  } catch (error) {
    writeJSON(response, 422, {
      code: "BROWSER_CHECK_FAILED",
      message: error instanceof Error ? error.message : "browser check failed",
    });
  }
});

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  server.listen(port, "0.0.0.0", () => {
    console.log(`browser worker listening on :${port}`);
  });
  // Docker stop delivers SIGTERM to the container. Without a handler the
  // process dies mid-check and the chromium launched for it is orphaned until
  // the runtime reaps the whole container. Closing the server stops accepting
  // new checks and waits for in-flight ones -- each already bounded by its own
  // timeout -- before the process exits and takes its chromium children down.
  const drainTimeoutMillis = 60_000;
  const shutdown = (signal: string) => {
    console.log(`received ${signal}; draining in-flight checks`);
    const forceExit = setTimeout(() => {
      console.error("drain timeout exceeded; forcing exit");
      process.exit(1);
    }, drainTimeoutMillis);
    forceExit.unref();
    server.close(() => {
      clearTimeout(forceExit);
      process.exit(0);
    });
    // Idle keep-alive connections would otherwise hold the close callback
    // hostage; connections with an in-flight request are unaffected.
    server.closeIdleConnections?.();
  };
  process.on("SIGTERM", () => shutdown("SIGTERM"));
  process.on("SIGINT", () => shutdown("SIGINT"));
}

export async function check(input: CheckRequest) {
  const { target, addresses } = await assertSafeTarget(input.url, input.allowPrivateTargets === true);
  const tlsFingerprint = target.protocol === "https:" ? await probeTLSFingerprint(target.hostname) : "";
  const timeout = Math.min(Math.max((input.timeoutSeconds || 30) * 1000, 1000), 90000);
  const limit = Math.min(Math.max(input.maxResponseBytes || maxResponseBytes, 1024), maxResponseBytes);
  const started = Date.now();
  const browser = await chromium.launch({
    headless: true,
    args: ["--disable-dev-shm-usage", "--disable-gpu"],
  });
  let context: BrowserContext | undefined;
  try {
    context = await browser.newContext({
      javaScriptEnabled: true,
      ignoreHTTPSErrors: false,
      serviceWorkers: "block",
      acceptDownloads: true,
    });
    const redirects: string[] = [];
    const popups: string[] = [];
    const dialogs: string[] = [];
    const downloads: string[] = [];
    const guard: RequestGuard = {
      requests: [],
      blockedRequests: [],
      recording: false,
      blockedDuringLogin: 0,
    };
    await guardContextRequests(context, input.allowPrivateTargets === true, guard);

    // Sign-in runs on its own page inside the same context, so the cookies it
    // sets are visible to the target navigation, and before the popup tracker
    // below is attached, so the login page is not reported as a popup.
    if (input.auth) {
      await signIn(context, input.auth, input.allowPrivateTargets === true, timeout);
      if (guard.blockedDuringLogin > 0) {
        throw new Error(
          "login page requested an address the network policy blocks; refusing to snapshot a login form that may not have rendered",
        );
      }
    }
    guard.recording = true;
    const requests = guard.requests;
    const blockedRequests = guard.blockedRequests;

    const page = await context.newPage();
    page.setDefaultTimeout(timeout);
    const trackedPages = new Set<Page>();
    const recordUnique = (values: string[], value: string) => {
      if (value && !values.includes(value)) values.push(value);
    };
    const trackPage = (candidate: Page, main = false) => {
      if (trackedPages.has(candidate)) return;
      trackedPages.add(candidate);
      if (!main) recordUnique(popups, candidate.url() || "about:blank");
      candidate.on("framenavigated", (frame) => {
        if (frame !== candidate.mainFrame()) return;
        if (main && frame.url() !== input.url) recordUnique(redirects, frame.url());
        if (!main) recordUnique(popups, frame.url());
      });
      candidate.on("dialog", (dialog) => {
        recordUnique(dialogs, dialog.type());
        void dialog.dismiss().catch(() => undefined);
      });
      candidate.on("download", (download) => {
        recordUnique(downloads, download.suggestedFilename());
        void download.cancel().catch(() => undefined);
      });
      candidate.on("popup", (popup) => trackPage(popup));
    };
    trackPage(page, true);
    context.on("page", (popup) => trackPage(popup));

    const response = await page.goto(target.toString(), {
      waitUntil: "networkidle",
      timeout,
    });
    if (!response) {
      throw new Error("page navigation returned no response");
    }
    await page.evaluate((selectors) => {
      for (const selector of selectors) {
        document.querySelectorAll(selector).forEach((node) => node.remove());
      }
    }, input.ignoreSelectors ?? []);
    const body = await page.content();
    if (Buffer.byteLength(body) > limit) {
      throw new Error(`rendered page exceeds ${limit} bytes`);
    }
    const responseHeaders = Object.fromEntries(
      Object.entries(await response.allHeaders()).map(([name, value]) => [name, [value]]),
    );
    const [snapshot, screenshot] = await Promise.all([
      extractSnapshot(page, redirects, requests, response.status(), started, input.ignoreSelectors ?? [], {
        resourceRequests: requests,
        blockedRequests,
        popups,
        dialogs,
        downloads,
        responseHeaders,
        resolvedIps: addresses,
        tlsFingerprint,
      }),
      page.screenshot({ type: "png", fullPage: true }),
    ]);
    snapshot.screenshotHash = hash(screenshot);
    return {
      snapshot,
      htmlBase64: Buffer.from(body).toString("base64"),
      screenshotBase64: screenshot.toString("base64"),
    };
  } finally {
    await context?.close().catch(() => undefined);
    await browser.close().catch(() => undefined);
  }
}

async function guardContextRequests(
  context: BrowserContext,
  allowPrivate: boolean,
  guard: RequestGuard,
) {
  await context.route("**/*", async (route) => {
    const requestURL = route.request().url();
    if (guard.recording) guard.requests.push(requestURL);
    try {
      await assertSafeTarget(requestURL, allowPrivate);
      await route.continue();
    } catch {
      if (guard.recording) {
        if (!guard.blockedRequests.includes(requestURL)) guard.blockedRequests.push(requestURL);
      } else {
        guard.blockedDuringLogin++;
      }
      await route.abort("blockedbyclient");
    }
  });
}

// signIn submits the customer login form on its own page and closes it, leaving
// the resulting cookies in the shared context for the target navigation.
//
// The login URL is revalidated here rather than trusted from the request: the
// control plane already checks it, but this worker is the process that actually
// opens the connection, so it must not rely on that. Errors must not quote the
// credentials -- only the field selector, which is not a secret.
async function signIn(
  context: BrowserContext,
  auth: LoginRequest,
  allowPrivate: boolean,
  timeout: number,
) {
  const { target } = await assertSafeTarget(auth.loginUrl, allowPrivate);
  const page = await context.newPage();
  page.setDefaultTimeout(timeout);
  try {
    await page.goto(target.toString(), { waitUntil: "domcontentloaded", timeout });
    await submitLoginForm(page, auth, timeout);
  } finally {
    await page.close().catch(() => undefined);
  }
}

// submitLoginForm is the mechanics of the sign-in, split out from the SSRF check
// above so it can be tested against a server on an ephemeral port: every target
// the worker opens in production goes through assertSafeTarget, which only
// allows 80 and 443.
export async function submitLoginForm(page: Page, auth: LoginRequest, timeout: number) {
  // Every action gets the same budget explicitly rather than inheriting the
  // page default, so the check's timeout setting actually bounds the sign-in.
  for (const [name, value] of Object.entries(auth.extraFields ?? {})) {
    await page.fill(attributeSelector(name), value, { timeout });
  }
  await page.fill(attributeSelector(auth.usernameField), auth.username, { timeout });
  await page.fill(attributeSelector(auth.passwordField), auth.password, { timeout });
  if (auth.submitSelector) {
    await page.click(auth.submitSelector, { timeout });
  } else {
    await page.press(attributeSelector(auth.passwordField), "Enter", { timeout });
  }
  await page.waitForLoadState("networkidle", { timeout });
}

// attributeSelector matches a form field by its name, which is the only field
// identifier the control plane stores. The value is escaped rather than
// interpolated raw, so a field name containing a quote cannot break out of the
// selector.
function attributeSelector(name: string) {
  return `[name="${name.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"]`;
}

export async function extractSnapshot(
  page: Page,
  redirects: string[],
  requests: string[],
  statusCode: number,
  started: number,
  ignoreSelectors: string[],
  events: BrowserEvents = {},
): Promise<Snapshot> {
  const data = await page.evaluate((selectors) => {
    for (const selector of selectors) {
      document.querySelectorAll(selector).forEach((node) => node.remove());
    }

    const absolute = (value: string) => {
      try {
        return new URL(value, location.href).toString().split("#")[0];
      } catch {
        return "";
      }
    };
    const visibleText = document.body?.innerText?.replace(/\s+/g, " ").trim() || "";
    const scripts = [...document.scripts].map((node) => {
      if (node.src) return { url: absolute(node.src) };
      const content = node.textContent?.replace(/\s+/g, " ").trim() || "";
      return content ? { inlineHash: content } : {};
    });
    return {
      url: location.href,
      title: document.title,
      html: document.documentElement.outerHTML,
      visibleText,
      scripts,
      links: [...document.querySelectorAll("a[href]")].map((node) => absolute((node as HTMLAnchorElement).href)).filter(Boolean),
      iframes: [...document.querySelectorAll("iframe[src]")].map((node) => absolute((node as HTMLIFrameElement).src)).filter(Boolean),
      forms: [...document.forms].map((form) => ({
        action: absolute(form.getAttribute("action") || location.href),
        method: (form.method || "get").toUpperCase(),
      })),
    };
  }, ignoreSelectors);
  const normalizedHTML = normalizeHTML(data.html);
  const scripts = data.scripts.map((script) =>
    script.inlineHash ? { inlineHash: hash(script.inlineHash) } : script,
  );
  return {
    url: data.url,
    finalUrl: data.url,
    statusCode,
    title: data.title,
    contentHash: hash(normalizedHTML),
    textHash: hash(data.visibleText),
    scripts,
    links: unique(data.links),
    iframes: unique(data.iframes),
    forms: data.forms,
    redirectChain: [...redirects],
    resolvedIps: unique(events.resolvedIps ?? []),
    tlsFingerprint: events.tlsFingerprint,
    responseHeaders: events.responseHeaders ?? {},
    resourceRequests: unique(events.resourceRequests ?? requests),
    blockedRequests: unique(events.blockedRequests ?? []),
    popups: unique(events.popups ?? []),
    dialogs: unique(events.dialogs ?? []),
    downloads: unique(events.downloads ?? []),
    durationMillis: Date.now() - started,
    capturedAt: new Date().toISOString(),
  };
}

function normalizeHTML(value: string) {
  return value
    .replace(/\s+/g, " ")
    .replace(/\s+(nonce|integrity)="[^"]*"/gi, "")
    .trim();
}

function unique(values: string[]) {
  return [...new Set(values)].sort();
}

function hash(value: Buffer | string) {
  return crypto.createHash("sha256").update(value).digest("hex");
}

function readJSON<T>(request: http.IncomingMessage): Promise<T> {
  return new Promise((resolve, reject) => {
    let body = "";
    request.on("data", (chunk) => {
      body += chunk;
      if (Buffer.byteLength(body) > 1024 * 1024) {
        reject(new Error("request body is too large"));
        request.destroy();
      }
    });
    request.on("end", () => {
      try {
        resolve(JSON.parse(body) as T);
      } catch {
        reject(new Error("invalid JSON"));
      }
    });
    request.on("error", reject);
  });
}

function writeJSON(response: http.ServerResponse, status: number, value: unknown) {
  const body = JSON.stringify(value);
  response.writeHead(status, {
    "content-type": "application/json; charset=utf-8",
    "cache-control": "no-store",
    "x-content-type-options": "nosniff",
  });
  response.end(body);
}
