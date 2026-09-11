import http from "node:http";
import crypto from "node:crypto";
import { fileURLToPath } from "node:url";
import { chromium, type BrowserContext, type Page } from "playwright";
import { assertSafeTarget } from "./security.js";

const port = Number(process.env.PORT || 8081);
const workerToken = process.env.WORKER_TOKEN || "";
const maxResponseBytes = 10 * 1024 * 1024;

type CheckRequest = {
  url: string;
  timeoutSeconds?: number;
  maxResponseBytes?: number;
  ignoreSelectors?: string[];
  allowPrivateTargets?: boolean;
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
  responseHeaders: Record<string, string[]>;
  durationMillis: number;
  capturedAt: string;
};

const server = http.createServer(async (request, response) => {
  if (request.method === "GET" && request.url === "/healthz") {
    writeJSON(response, 200, { status: "ok" });
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
}

export async function check(input: CheckRequest) {
  const target = await assertSafeTarget(input.url, input.allowPrivateTargets === true);
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
      acceptDownloads: false,
    });
    const redirects: string[] = [];
    const requests: string[] = [];
    const page = await context.newPage();
    page.setDefaultTimeout(timeout);
    await guardPageRequests(page, input.allowPrivateTargets === true, requests);
    page.on("framenavigated", (frame) => {
      if (frame === page.mainFrame() && frame.url() !== input.url) {
        redirects.push(frame.url());
      }
    });
    const response = await page.goto(target.toString(), {
      waitUntil: "networkidle",
      timeout,
    });
    if (!response) {
      throw new Error("page navigation returned no response");
    }
    const body = await page.content();
    if (Buffer.byteLength(body) > limit) {
      throw new Error(`rendered page exceeds ${limit} bytes`);
    }
    await page.evaluate((selectors) => {
      for (const selector of selectors) {
        document.querySelectorAll(selector).forEach((node) => node.remove());
      }
    }, input.ignoreSelectors ?? []);
    const [snapshot, screenshot] = await Promise.all([
      extractSnapshot(page, redirects, requests, response.status(), started, []),
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

async function guardPageRequests(
  page: Page,
  allowPrivate: boolean,
  requests: string[],
) {
  await page.route("**/*", async (route) => {
    const requestURL = route.request().url();
    requests.push(requestURL);
    try {
      await assertSafeTarget(requestURL, allowPrivate);
      await route.continue();
    } catch {
      await route.abort("blockedbyclient");
    }
  });
}

export async function extractSnapshot(
  page: Page,
  redirects: string[],
  requests: string[],
  statusCode: number,
  started: number,
  ignoreSelectors: string[],
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
    redirectChain: redirects,
    resolvedIps: [],
    responseHeaders: {},
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
