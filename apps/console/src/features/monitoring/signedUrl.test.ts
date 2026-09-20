import { describe, expect, it } from "vitest";
import { formatRemaining, isControlPlaneUrl, secondsRemaining } from "./signedUrl";

describe("signed evidence urls", () => {
  it("distinguishes a control-plane path from an absolute presigned url", () => {
    expect(isControlPlaneUrl("/api/v1/evidence/signed/abc")).toBe(true);
    expect(isControlPlaneUrl("https://minio.example/evidence/a.png?X-Amz-Signature=x")).toBe(false);
  });

  it("counts down to the expiry", () => {
    const now = Date.parse("2026-09-21T00:00:00Z");
    expect(secondsRemaining("2026-09-21T00:05:00Z", now)).toBe(300);
    expect(secondsRemaining("2026-09-21T00:00:30Z", now)).toBe(30);
  });

  it("never goes negative once expired", () => {
    const now = Date.parse("2026-09-21T00:10:00Z");
    expect(secondsRemaining("2026-09-21T00:00:00Z", now)).toBe(0);
  });

  it("treats an unparseable expiry as expired rather than as no limit", () => {
    // Fail closed: showing a link as live when its expiry could not be read
    // would hand out a credential the page cannot vouch for.
    expect(secondsRemaining("not-a-date")).toBe(0);
    expect(secondsRemaining("")).toBe(0);
  });

  it("formats the remaining time for both scales", () => {
    expect(formatRemaining(0)).toBe("已过期");
    expect(formatRemaining(45)).toBe("45 秒后过期");
    expect(formatRemaining(60)).toBe("1 分 0 秒后过期");
    expect(formatRemaining(185)).toBe("3 分 5 秒后过期");
  });
});
