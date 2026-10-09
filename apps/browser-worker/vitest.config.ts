import { defineConfig } from "vitest/config";

// The include is explicit because the default moved under us. Vitest 3's default
// exclude covered `dist/`; Vitest 5's does not, so after a `pnpm build` the suite
// ran every test twice -- once against the TypeScript source and once against the
// compiled copy in `dist/test/*.test.js` (this package's `tsc` build emits the
// test sources too). Two problems with that, both quiet:
//
// - local and CI would run different sets: CI runs the tests before the build,
//   when `dist/` does not exist yet;
// - the compiled copies are stale by construction, so a failing test could be
//   masked by an older passing file, or an old file could fail for a reason the
//   source no longer has.
export default defineConfig({
  test: {
    include: ["test/**/*.test.ts"],
    exclude: ["**/node_modules/**", "dist/**"],
  },
});
