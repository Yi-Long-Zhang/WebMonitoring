import crypto from "node:crypto";
import dns from "node:dns/promises";
import net from "node:net";
import tls from "node:tls";

export interface ResolvedTarget {
  target: URL;
  addresses: string[];
}

export async function assertSafeTarget(rawUrl: string, allowPrivate = false): Promise<ResolvedTarget> {
  const target = new URL(rawUrl);
  if (!["http:", "https:"].includes(target.protocol)) {
    throw new Error("only http and https targets are allowed");
  }
  if (target.username || target.password) {
    throw new Error("target credentials are not allowed");
  }
  const port = target.port || (target.protocol === "https:" ? "443" : "80");
  if (!["80", "443"].includes(port)) {
    throw new Error("target port is not allowed");
  }
  if (allowPrivate) {
    return { target, addresses: [] };
  }
  const addresses = net.isIP(target.hostname)
    ? [{ address: target.hostname }]
    : await dns.lookup(target.hostname, { all: true });
  if (!addresses.length || addresses.some((entry) => isBlockedAddress(entry.address))) {
    throw new Error("target resolves to a blocked network address");
  }
  return { target, addresses: addresses.map((entry) => entry.address) };
}

export function isBlockedAddress(rawAddress: string): boolean {
  const address = rawAddress.toLowerCase().replace(/^\[|\]$/g, "");
  if (address === "localhost" || address === "metadata.google.internal") {
    return true;
  }
  if (net.isIPv4(address)) {
    const octets = address.split(".").map(Number);
    const [a, b] = octets;
    return (
      a === 0 ||
      a === 10 ||
      a === 127 ||
      (a === 100 && b >= 64 && b <= 127) ||
      (a === 169 && b === 254) ||
      (a === 172 && b >= 16 && b <= 31) ||
      (a === 192 && (b === 0 || b === 168)) ||
      (a === 198 && (b === 18 || b === 19 || b === 51)) ||
      (a === 203 && b === 0 && octets[2] === 113) ||
      a >= 224
    );
  }
  if (net.isIPv6(address)) {
    return (
      address === "::" ||
      address === "::1" ||
      address.startsWith("fc") ||
      address.startsWith("fd") ||
      address.startsWith("fe8") ||
      address.startsWith("fe9") ||
      address.startsWith("fea") ||
      address.startsWith("feb")
    );
  }
  return true;
}

// probeTLSFingerprint returns the SHA-256 of the peer's leaf certificate DER,
// matching the Go HTTP checker's tlsFingerprint. Callers must have already
// validated the host with assertSafeTarget so private/metadata targets are
// blocked before a raw TLS connection is opened. HTTPS only; empty on failure.
export function probeTLSFingerprint(host: string, port = 443, timeoutMs = 5000): Promise<string> {
  return new Promise((resolve) => {
    let settled = false;
    const done = (value: string) => {
      if (settled) return;
      settled = true;
      resolve(value);
    };
    const socket = tls.connect(
      {
        host,
        port,
        servername: host,
        rejectUnauthorized: false,
        timeout: timeoutMs,
      },
      () => {
        const cert = socket.getPeerCertificate(false);
        socket.end();
        done(cert && cert.raw && cert.raw.length ? sha256(cert.raw) : "");
      },
    );
    socket.on("error", () => {
      socket.destroy();
      done("");
    });
    socket.on("timeout", () => {
      socket.destroy();
      done("");
    });
  });
}

function sha256(value: Buffer | string) {
  return crypto.createHash("sha256").update(value).digest("hex");
}
