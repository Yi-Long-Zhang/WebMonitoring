import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

export default defineConfig({
  plugins: [vue()],
  cacheDir: "../../.cache/vite-console",
  server: {
    port: 5173,
    proxy: {
      "/api": "http://localhost:8080",
    },
  },
  build: {
    sourcemap: true,
    rollupOptions: {
      output: {
        manualChunks: {
          vue: ["vue", "@tanstack/vue-query"],
          "element-plus": ["element-plus"],
        },
      },
    },
  },
});
