import { spawnSync } from "node:child_process";
import { cpSync, mkdirSync, readdirSync, rmSync } from "node:fs";
import { resolve } from "node:path";

const pnpm = process.platform === "win32" ? "pnpm.cmd" : "pnpm";
const result = spawnSync(pnpm, ["build:web"], {
  stdio: "inherit",
  shell: process.platform === "win32",
});
if (result.error) throw result.error;
if (result.status !== 0) process.exit(result.status ?? 1);

const target = resolve("services/core/internal/httpapi/web");
mkdirSync(target, { recursive: true });
for (const name of readdirSync(target)) {
  if (name !== ".gitkeep")
    rmSync(resolve(target, name), { recursive: true, force: true });
}
cpSync(resolve("apps/web/dist"), target, { recursive: true });

const outputDir = resolve("dist");
mkdirSync(outputDir, { recursive: true });
const targetOS = process.env.GOOS ?? process.platform;
const binary =
  targetOS === "windows" || targetOS === "win32" ? "tuskrag.exe" : "tuskrag";
const build = spawnSync(
  "go",
  ["build", "-trimpath", "-o", resolve(outputDir, binary), "./cmd/tuskrag"],
  {
    cwd: "services/core",
    stdio: "inherit",
    env: { ...process.env, CGO_ENABLED: "0" },
  },
);
if (build.error) throw build.error;
if (build.status !== 0) process.exit(build.status ?? 1);
