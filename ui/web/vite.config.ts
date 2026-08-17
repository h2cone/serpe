import { reactRouter } from "@react-router/dev/vite";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";
import { defineConfig } from "vite";
import { apiOrigin } from "./api-origin";

export default defineConfig({
  plugins: [tailwindcss(), reactRouter()],
  resolve: {
    alias: {
      "~": path.resolve(__dirname, "app"),
    },
  },
  server: {
    proxy: {
      "/api": {
        target: apiOrigin(),
        changeOrigin: true,
        bypass(req) {
          const url = req.url ?? "";
          if (url === "/api" || url.startsWith("/api/")) return undefined;
          return url;
        },
      },
    },
  },
});
