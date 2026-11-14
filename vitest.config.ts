import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "jsdom",
    setupFiles: ["tests/setup-dom.ts"],
    include: ["apps/web/src/**/*.test.tsx"],
  },
});
