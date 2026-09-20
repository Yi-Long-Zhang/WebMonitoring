// Presentation helpers for evidence download links, kept out of the component
// so they can be tested without mounting anything.

// The server answers in two shapes: an absolute S3 presigned URL when evidence
// lives in object storage, and a relative control-plane path ("/api/v1/...")
// when it lives on local disk. Both are usable as-is in an href from this page
// -- the relative one resolves against the console's own origin, which is the
// API behind the same edge -- so nothing rewrites them. This lives here to name
// that fact rather than leave the next reader wondering which case is handled.
export function isControlPlaneUrl(url: string) {
  return url.startsWith("/");
}

// Counting down rather than showing the issue time: an operator looking at a
// one-shot download link needs to know how long they have left, not when it was
// minted.
export function secondsRemaining(expiresAt: string, now = Date.now()) {
  const expires = Date.parse(expiresAt);
  if (Number.isNaN(expires)) return 0;
  return Math.max(0, Math.floor((expires - now) / 1000));
}

export function formatRemaining(seconds: number) {
  if (seconds <= 0) return "已过期";
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  return minutes > 0 ? `${minutes} 分 ${rest} 秒后过期` : `${rest} 秒后过期`;
}
