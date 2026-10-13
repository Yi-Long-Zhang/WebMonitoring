import { describe, expect, it } from "vitest";

import { verificationInstructionText } from "./verification";

const token = "webmonitoring-verification=0199b0a4-0000-7000-8000-000000000000";

// The regression this pins: the dialog interpolated `path` for every method, so
// these two assets read "创建文件 undefined" and the operator had nothing to
// publish. The assertion is on the token rather than on the whole sentence --
// the wording may change, but the instruction must always carry the value the
// server will look for.
describe("verificationInstructionText", () => {
  it("names the file to create for a FILE asset", () => {
    const text = verificationInstructionText({
      method: "FILE",
      path: "/.well-known/webmonitoring-verification.txt",
      content: token,
    });
    expect(text).toContain("/.well-known/webmonitoring-verification.txt");
    expect(text).toContain(token);
  });

  it("names the record to add for a DNS asset", () => {
    const text = verificationInstructionText({
      method: "DNS",
      record: "_webmonitoring-verify.example.com",
      recordType: "TXT",
      content: token,
    });
    expect(text).toContain("TXT");
    expect(text).toContain("_webmonitoring-verify.example.com");
    expect(text).toContain(token);
  });

  it("hands over the ready-made tag for a META asset", () => {
    const tag = `<meta name="webmonitoring-verification" content="${token}">`;
    const text = verificationInstructionText({ method: "META", tag, content: token });
    expect(text).toContain(tag);
  });

  // A shape the contract does not promise still has to leave the operator with
  // something to do -- and never with the word "undefined" in a step they are
  // about to follow.
  it("falls back to the token when the method's own field is absent", () => {
    for (const issue of [
      { method: "FILE" as const, content: token },
      { method: "DNS" as const, content: token },
      { method: "META" as const, content: token },
    ]) {
      const text = verificationInstructionText(issue);
      expect(text).toContain(token);
      expect(text).not.toContain("undefined");
    }
  });
});
