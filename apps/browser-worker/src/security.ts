import dns from "node:dns/promises";
import net from "node:net";

export async function assertSafeTarget(rawUrl: string, allowPrivate = false): Promise<URL> {
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
    return target;
  }
  const addresses = net.isIP(target.hostname)
    ? [{ address: target.hostname }]
    : await dns.lookup(target.hostname, { all: true });
  if (!addresses.length || addresses.some((entry) => isBlockedAddress(entry.address))) {
    throw new Error("target resolves to a blocked network address");
  }
  return target;
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
