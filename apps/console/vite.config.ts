// vitest/config re-exports Vite's defineConfig with the `test` key typed; the
// bare one from "vite" would reject it.
import { defineConfig } from "vitest/config";
import vue from "@vitejs/plugin-vue";
import Components from "unplugin-vue-components/vite";
import { ElementPlusResolver } from "unplugin-vue-components/resolvers";

export default defineConfig({
  plugins: [
    vue(),
    // Element Plus components are imported where they are used in a template,
    // with their own styles, instead of the whole library being registered on
    // the app. `app.use(ElementPlus)` pulled 922 kB of JavaScript and 373 kB of
    // CSS into every visit -- including the login page, which uses four
    // components. The programmatic APIs (ElMessage, ElMessageBox) are still
    // imported by name in the modules that call them, so their styles are
    // imported by name in main.ts; the resolver only sees templates.
    //
    // The declaration file is off. Generating it (the plugin's default) makes
    // vue-tsc type-check every Element Plus prop, which turns el-table's slot
    // scope into DefaultRow and fails the build in every page that passes a row
    // to an emit -- a real improvement, but a separate piece of work from this
    // one, and not what a bundle-size change should carry. Nothing is lost
    // against the previous state: `app.use(ElementPlus)` declared no component
    // types to editors either.
    Components({
      resolvers: [ElementPlusResolver()],
      dts: false,
    }),
  ],
  cacheDir: "../../.cache/vite-console",
  // The console is a browser application, so its own tests need a DOM. Without
  // this block every .vue test would have to opt in individually, and a test
  // that mounts a page silently could not exist at all.
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    // Element Plus is inlined rather than externalized. On-demand imports pull
    // their own stylesheets, and an externalized dependency is loaded by Node
    // as-is, where "Unknown file extension .css" is the end of the test run.
    server: { deps: { inline: ["element-plus"] } },
  },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://localhost:8080",
    },
  },
  build: {
    // Off by default: nginx serves the dist directory as-is, so a source map is
    // published to anyone who can open the console (and 12 MB of them ship in
    // the image). Set CONSOLE_SOURCEMAP=true for a build whose stack traces
    // need to be readable; the dev server has its own maps either way.
    sourcemap: process.env.CONSOLE_SOURCEMAP === "true",
    // Vite 8 builds with Rolldown, so this is `rolldownOptions` (the old
    // `rollupOptions` name is an alias that warns) and the object form of
    // `manualChunks` is gone -- `manualChunks is not a function` is what the
    // migration actually fails with, not a warning.
    //
    // What the removed object form, `{ vue: ["vue", "@tanstack/vue-query"] }`,
    // did: those packages and their dependencies formed one stable chunk, so
    // changing application code did not invalidate the vendor bundle.
    // `codeSplitting.groups` is the replacement. The pattern is anchored on the
    // package directory, so `vue-router` (a separate dependency, and one the
    // old config also left out) is not swept in, while the scoped @vue/* and
    // @tanstack/* packages that come with them are.
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            { name: "vue", test: /node_modules[\\/](vue|@vue|@tanstack)[\\/]/ },
          ],
        },
      },
    },
  },
});
