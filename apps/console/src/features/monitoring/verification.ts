// The ownership-verification dialog's text, kept out of the component the way
// signedUrl.ts and charts.ts are, so it can be tested without mounting anything.

import type { VerificationInstructions } from "../../types/domain";

// The ISSUE answer carries the proof in the key its method names: FILE sends
// `path`, DNS sends `record` and `recordType`, META sends a finished `tag`. The
// dialog used to read `path` whatever the method was, so an asset registered
// through the API or the importer as DNS or META -- the console's own create
// dialog only ever offers FILE -- was told to "创建文件 undefined".
//
// Every branch therefore checks the field it is about to print and falls back to
// the token itself. A missing key then costs the operator a little prose, not a
// verification step they cannot perform.
export function verificationInstructionText(issue: VerificationInstructions) {
  switch (issue.method) {
    case "FILE":
      if (issue.path) {
        return `请在目标站点创建文件 ${issue.path}，内容为：${issue.content}`;
      }
      break;
    case "DNS":
      if (issue.record) {
        return `请在目标域名的 DNS 中添加一条 ${issue.recordType ?? "TXT"} 记录：名称为 ${issue.record}，值为：${issue.content}`;
      }
      break;
    case "META":
      // `tag` already contains the token, so printing `content` as well would
      // give the operator two things to paste into one page.
      if (issue.tag) {
        return `请在目标页面 <head> 中加入以下标签：${issue.tag}`;
      }
      break;
  }
  return `请按资产登记时的验证方式，在目标站点发布以下内容：${issue.content}`;
}
