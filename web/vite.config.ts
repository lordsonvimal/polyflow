import { defineConfig } from "vite";
import solid from "vite-plugin-solid";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [solid(), tailwindcss()],
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
