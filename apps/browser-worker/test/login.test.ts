import http from "node:http";
import type { AddressInfo } from "node:net";
import { describe, expect, it } from "vitest";
import { chromium, type Browser } from "playwright";
import { submitLoginForm } from "../src/index.js";

type LoginInput = Parameters<typeof submitLoginForm>[1];

// loginSite serves a login form and one protected page that only renders for a
// session cookie, mirroring the HTTP checker's fixture.
async function loginSite(): Promise<{ origin: string; close: () => Promise<void>; loginPosts: () => number }> {
  let posts = 0;
  const server = http.createServer((request, response) => {
    const url = new URL(request.url ?? "/", "http://127.0.0.1");
    if (url.pathname === "/login" && request.method === "POST") {
      posts++;
      let body = "";
      request.on("data", (chunk) => {
        body += chunk;
      });
      request.on("end", () => {
        const form = new URLSearchParams(body);
        if (form.get("email") !== "user@example.com" || form.get("password") !== "s3cret") {
          response.writeHead(401).end("bad credentials");
          return;
        }
        response.writeHead(200, {
          "set-cookie": "session=authenticated; Path=/",
          "content-type": "text/html",
        });
        response.end("<html><title>Signed in</title></html>");
      });
      return;
    }
    if (url.pathname === "/login") {
      response.writeHead(200, { "content-type": "text/html" });
      response.end(`
        <html><body>
          <form method="post" action="/login">
            <input name="tenant" value="" />
            <input name="email" />
            <input name="password" type="password" />
            <button id="submit" type="submit">Sign in</button>
          </form>
        </body></html>
      `);
      return;
    }
    const authenticated = (request.headers.cookie ?? "").includes("session=authenticated");
    response.writeHead(200, { "content-type": "text/html" });
    response.end(`<html><title>${authenticated ? "Account Dashboard" : "Login"}</title></html>`);
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address() as AddressInfo;
  return {
    origin: `http://127.0.0.1:${port}`,
    close: () => new Promise<void>((resolve) => server.close(() => resolve())),
    loginPosts: () => posts,
  };
}

const credentials: LoginInput = {
  loginUrl: "",
  usernameField: "email",
  passwordField: "password",
  username: "user@example.com",
  password: "s3cret",
};

describe("browser worker login form", () => {
  it("submits the form so the shared context keeps the session", async () => {
    const site = await loginSite();
    const browser: Browser = await chromium.launch({ headless: true });
    try {
      const context = await browser.newContext();
      const page = await context.newPage();
      await page.goto(`${site.origin}/login`, { waitUntil: "domcontentloaded" });
      await submitLoginForm(page, { ...credentials, loginUrl: `${site.origin}/login` }, 15_000);

      expect(site.loginPosts()).toBe(1);

      // The point of signing in: the protected page must render as the
      // authenticated view rather than the login form.
      const protectedPage = await context.newPage();
      await protectedPage.goto(`${site.origin}/account`, { waitUntil: "domcontentloaded" });
      expect(await protectedPage.title()).toBe("Account Dashboard");
    } finally {
      await browser.close();
      await site.close();
    }
  }, 30_000);

  it("submits through an explicit selector and fills extra fields", async () => {
    const site = await loginSite();
    const browser: Browser = await chromium.launch({ headless: true });
    try {
      const context = await browser.newContext();
      const page = await context.newPage();
      await page.goto(`${site.origin}/login`, { waitUntil: "domcontentloaded" });
      await submitLoginForm(
        page,
        { ...credentials, loginUrl: `${site.origin}/login`, submitSelector: "#submit", extraFields: { tenant: "acme" } },
        15_000,
      );
      expect(site.loginPosts()).toBe(1);
    } finally {
      await browser.close();
      await site.close();
    }
  }, 30_000);

  // A field name is operator-supplied policy data, so a quote in it must not be
  // able to close the attribute selector and reach a different element. The
  // escaped selector matches nothing, so the sign-in fails instead of filling
  // whatever the injected selector pointed at.
  it("escapes field names instead of interpolating them raw", async () => {
    const site = await loginSite();
    const browser: Browser = await chromium.launch({ headless: true });
    try {
      const context = await browser.newContext();
      const page = await context.newPage();
      await page.goto(`${site.origin}/login`, { waitUntil: "domcontentloaded" });
      await expect(
        submitLoginForm(
          page,
          { ...credentials, loginUrl: `${site.origin}/login`, usernameField: 'email"]' },
          2_000,
        ),
      ).rejects.toThrow();
      expect(site.loginPosts()).toBe(0);
    } finally {
      await browser.close();
      await site.close();
    }
  }, 30_000);
});
