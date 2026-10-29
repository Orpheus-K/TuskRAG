import { copyFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
copyFileSync(
  resolve(root, "contracts/parsed-document.schema.json"),
  resolve(root, "services/core/internal/parser/parsed-document.schema.json"),
);
