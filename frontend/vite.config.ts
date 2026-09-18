import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "~": "/src",
    },
  },
  server: {
    port: 51800,
    // 开发模式：API 与 WebSocket 代理到本地 Go 服务（默认 37421，被占用会顺延，必要时改这里）
    proxy: {
      "/api/v1": { target: "http://127.0.0.1:37421", ws: true },
      "/healthz": "http://127.0.0.1:37421",
    },
  },
  build: {
    // esbuild chokes on Tailwind v4 empty :is() from @apply motion-reduce
    cssMinify: "lightningcss",
    emptyOutDir: false,
  },
});
