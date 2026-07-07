import { defineConfig } from "vite";
import solid from "vite-plugin-solid";

export default defineConfig({
  plugins: [solid()],
  server: {
    proxy: {
      "/api": "http://localhost:9400",
    },
  },
  build: {
    outDir: "dist",
    target: "esnext",
  },
});
