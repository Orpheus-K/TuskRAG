import { spawnSync } from "node:child_process";

const pnpm = process.platform === "win32" ? "pnpm.cmd" : "pnpm";
const steps = [
  [pnpm, ["lint"], "."],
  [pnpm, ["typecheck"], "."],
  [pnpm, ["test:web"], "."],
  ["go", ["vet", "./..."], "services/core"],
  // Integration tests inject table-level failures into the same isolated database.
  ["go", ["test", "-p", "1", "./..."], "services/core"],
  ["uv", ["run", "--locked", "ruff", "check", "."], "services/parser"],
  [
    "uv",
    ["run", "--locked", "ruff", "format", "--check", "."],
    "services/parser",
  ],
  ["uv", ["run", "--locked", "mypy", "src", "tests"], "services/parser"],
  ["uv", ["run", "--locked", "pytest", "-q"], "services/parser"],
];

for (const [command, args, cwd] of steps) {
  const result = spawnSync(command, args, {
    cwd,
    stdio: "inherit",
    shell: process.platform === "win32",
  });
  if (result.error) throw result.error;
  if (result.status !== 0) process.exit(result.status ?? 1);
}
