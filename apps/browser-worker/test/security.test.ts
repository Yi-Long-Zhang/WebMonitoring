import { describe, expect, it } from "vitest";
import { chromium } from "playwright";
import { extractSnapshot } from "../src/index.js";
import { assertSafeTarget, isBlockedAddress, probeTLSFingerprint } from "../src/security.js";

describe("browser target security", () => {
  it("blocks private and metadata addresses", () => {
    expect(isBlockedAddress("127.0.0.1")).toBe(true);
    expect(isBlockedAddress("10.0.0.1")).toBe(true);
    expect(isBlockedAddress("169.254.169.254")).toBe(true);
    expect(isBlockedAddress("192.168.1.10")).toBe(true);
  });

  // An IPv4 address wearing IPv6 clothes is still that IPv4 address. Prefix
  // matching let these through while their dotted forms were blocked, so a
  // target whose AAAA record was ::ffff:169.254.169.254 reached the metadata
  // service. Go folds the mapped form with netip.Addr.Unmap; these are the cases
  // that prove the same fold happens here.
  it("blocks private and metadata addresses written as IPv4-mapped IPv6", () => {
    expect(isBlockedAddress("::ffff:127.0.0.1")).toBe(true);
    expect(isBlockedAddress("::ffff:10.0.0.1")).toBe(true);
    expect(isBlockedAddress("::ffff:192.168.1.10")).toBe(true);
    expect(isBlockedAddress("::ffff:169.254.169.254")).toBe(true);
    // The same address in its hexadecimal form: ::ffff:7f00:1 is ::ffff:127.0.0.1.
    expect(isBlockedAddress("::ffff:7f00:1")).toBe(true);
    // IPv4-compatible, deprecated but still accepted by some stacks.
    expect(isBlockedAddress("::127.0.0.1")).toBe(true);
  });

  it("blocks the IPv6 ranges Go blocks, in any spelling", () => {
    expect(isBlockedAddress("::1")).toBe(true);
    expect(isBlockedAddress("0:0:0:0:0:0:0:1")).toBe(true);
    expect(isBlockedAddress("::")).toBe(true);
    expect(isBlockedAddress("fc00::1")).toBe(true);
    expect(isBlockedAddress("fd12:3456:789a::1")).toBe(true);
    expect(isBlockedAddress("fe80::1")).toBe(true);
    // Multicast: the old code had no rule for ff00::/8 at all.
    expect(isBlockedAddress("ff02::1")).toBe(true);
  });

  // The other half of the contract: a rule that blocks everything is not a
  // security control, it is an outage.
  it("leaves public addresses alone, including a mapped public one", () => {
    expect(isBlockedAddress("8.8.8.8")).toBe(false);
    expect(isBlockedAddress("2001:4860:4860::8888")).toBe(false);
    expect(isBlockedAddress("::ffff:8.8.8.8")).toBe(false);
  });

  it("rejects credentials and unsafe protocols", async () => {
    await expect(assertSafeTarget("file:///etc/passwd")).rejects.toThrow();
    await expect(assertSafeTarget("https://user:pass@example.com")).rejects.toThrow();
  });

  it("rejects private targets and returns addresses for allowed ones", async () => {
    await expect(assertSafeTarget("http://127.0.0.1")).rejects.toThrow();
    await expect(assertSafeTarget("http://169.254.169.254")).rejects.toThrow();
    const allowed = await assertSafeTarget("http://127.0.0.1", true);
    expect(allowed.target.hostname).toBe("127.0.0.1");
    expect(allowed.addresses).toEqual([]);
  });

  it("returns an empty or 64-hex TLS fingerprint", async () => {
    const fingerprint = await probeTLSFingerprint("example.com", 443, 3000);
    expect(fingerprint).toMatch(/^([0-9a-f]{64})?$/);
  }, 10_000);

  it("removes ignored nodes before calculating snapshot fields", async () => {
    const browser = await chromium.launch({ headless: true });
    try {
      const snapshotFor = async (variant: string) => {
        const page = await browser.newPage();
        await page.setContent(`
          <html>
            <head><title>Example</title></head>
            <body>
              <main>Keep this content</main>
              <div class="dynamic">Ignore ${variant}</div>
              <script class="dynamic">window.dynamic = "${variant}"</script>
              <script>window.kept = true</script>
              <a class="dynamic" href="about:blank#ignored-${variant}">ignored link</a>
              <a href="https://example.com/kept-link">kept link</a>
              <iframe class="dynamic" src="about:blank#ignored-${variant}"></iframe>
              <form class="dynamic" action="about:blank#ignored-${variant}"></form>
            </body>
          </html>
        `);

        return extractSnapshot(page, [], [], 200, Date.now(), [".dynamic"]);
      };

      const first = await snapshotFor("first");
      const second = await snapshotFor("second");

      expect(first.contentHash).toBe(second.contentHash);
      expect(first.textHash).toBe(second.textHash);
      expect(first.scripts).toHaveLength(1);
      expect(first.scripts[0]?.inlineHash).toBeDefined();
      expect(first.links).toEqual(["https://example.com/kept-link"]);
      expect(first.iframes).toEqual([]);
      expect(first.forms).toEqual([]);
    } finally {
      await browser.close();
    }
  }, 15_000);
});
