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
  // vitest reads this block; the vite type doesn't know it.
  // @ts-ignore
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
  },
});
