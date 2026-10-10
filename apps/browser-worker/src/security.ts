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
    return isBlockedIPv4(address);
  }
  if (net.isIPv6(address)) {
    const groups = parseIPv6(address);
    // Node says this is IPv6 but we cannot take it apart: refuse rather than
    // guess. Nothing legitimate is lost -- an address we cannot classify is one
    // whose network we cannot reason about.
    if (groups === null) {
      return true;
    }
    if (isBlockedIPv6(groups)) {
      return true;
    }
    // An IPv4-mapped (::ffff:a.b.c.d) or IPv4-compatible (::a.b.c.d) address is
    // an IPv4 address wearing IPv6 clothes, and has to be judged by the IPv4
    // rules. Go folds it first with netip.Addr.Unmap (internal/security/
    // urlguard.go); without the same fold here, ::ffff:169.254.169.254 reached
    // the metadata service while its dotted form was blocked.
    const embedded = embeddedIPv4(groups);
    return embedded === null ? false : isBlockedIPv4(embedded);
  }
  return true;
}

function isBlockedIPv4(address: string): boolean {
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

// parseIPv6 expands an address into its eight 16-bit groups, or returns null when
// it cannot. Node exposes net.isIPv6 but no parser, and the prefix matching this
// replaces could not express the things that matter: "the address this one
// embeds is private", or "these zero groups are spelled out rather than written
// as ::".
function parseIPv6(rawAddress: string): number[] | null {
  let address = rawAddress;
  // A trailing dotted quad stands for the last two groups.
  const dotted = address.match(/(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})$/);
  if (dotted) {
    const octets = dotted[1].split(".").map(Number);
    if (octets.some((octet) => octet > 255)) {
      return null;
    }
    const high = ((octets[0] ?? 0) << 8) | (octets[1] ?? 0);
    const low = ((octets[2] ?? 0) << 8) | (octets[3] ?? 0);
    address = `${address.slice(0, dotted.index)}${high.toString(16)}:${low.toString(16)}`;
  }

  const halves = address.split("::");
  if (halves.length > 2) {
    return null;
  }
  const parseGroups = (part: string): number[] | null => {
    if (part === "") {
      return [];
    }
    const groups: number[] = [];
    for (const group of part.split(":")) {
      if (!/^[0-9a-f]{1,4}$/.test(group)) {
        return null;
      }
      groups.push(parseInt(group, 16));
    }
    return groups;
  };

  const head = parseGroups(halves[0] ?? "");
  if (head === null) {
    return null;
  }
  if (halves.length === 1) {
    return head.length === 8 ? head : null;
  }
  const tail = parseGroups(halves[1] ?? "");
  if (tail === null) {
    return null;
  }
  const missing = 8 - head.length - tail.length;
  // "::" has to stand for at least one group, otherwise the address is
  // over-specified and Node would not have accepted it as IPv6 either.
  if (missing < 1) {
    return null;
  }
  return [...head, ...new Array<number>(missing).fill(0), ...tail];
}

// isBlockedIPv6 mirrors the predicates Go applies before its IPv4 table:
// unspecified, loopback, unique-local, link-local and multicast.
//
// The first two overlap with the IPv4-compatible fold below -- ::1 folds to
// 0.0.0.1, which the IPv4 rules already block -- so on today's table they change
// no verdict. They stay because that overlap is an accident of 0.0.0.0/8 being
// blocked: narrow the IPv4 table and these two are what keeps the loopback and
// the unspecified address blocked. Mutation testing cannot tell them apart from
// the fold, which is exactly why the reason is written down here.
function isBlockedIPv6(groups: number[]): boolean {
  const first = groups[0] ?? 0;
  const allZero = groups.every((group) => group === 0);
  const loopback = groups.slice(0, 7).every((group) => group === 0) && groups[7] === 1;
  return (
    allZero ||
    loopback ||
    (first & 0xfe00) === 0xfc00 || // fc00::/7 unique local
    (first & 0xffc0) === 0xfe80 || // fe80::/10 link-local unicast
    (first & 0xff00) === 0xff00 // ff00::/8 multicast
  );
}

// embeddedIPv4 returns the IPv4 address an IPv4-mapped (::ffff:a.b.c.d) or
// IPv4-compatible (::a.b.c.d) address carries, or null when it carries none.
function embeddedIPv4(groups: number[]): string | null {
  if (!groups.slice(0, 5).every((group) => group === 0)) {
    return null;
  }
  if (groups[5] !== 0 && groups[5] !== 0xffff) {
    return null;
  }
  const high = groups[6] ?? 0;
  const low = groups[7] ?? 0;
  return [high >> 8, high & 0xff, low >> 8, low & 0xff].join(".");
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
