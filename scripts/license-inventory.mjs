import { createHash } from "node:crypto";
import { readdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const hash = (data) => createHash("sha256").update(data).digest("hex");
const lockfiles = {};
for (const name of [
  "services/core/go.mod",
  "services/core/go.sum",
  "services/parser/uv.lock",
  "pnpm-lock.yaml",
]) {
  lockfiles[name] = hash(await readFile(path.join(root, name)));
}
const site = process.argv[2];
if (!site)
  throw new Error(
    "Usage: node scripts/license-inventory.mjs <parser-site-packages>",
  );
const distributions = [];
for (const entry of await readdir(site, { withFileTypes: true })) {
  if (!entry.isDirectory() || !entry.name.endsWith(".dist-info")) continue;
  const bytes = await readFile(path.join(site, entry.name, "METADATA"));
  const metadata = bytes.toString("utf8");
  const field = (name) =>
    metadata.match(new RegExp(`^${name}: (.*)$`, "m"))?.[1]?.trim() ?? null;
  distributions.push({
    name: field("Name"),
    version: field("Version"),
    licenseExpression: field("License-Expression"),
    license: field("License"),
    classifiers: [...metadata.matchAll(/^Classifier: (License :: .*)$/gm)].map(
      (m) => m[1].trim(),
    ),
    metadataSHA256: hash(bytes),
  });
}
distributions.sort((a, b) => String(a.name).localeCompare(String(b.name)));
const normalize = (name) => name.toLowerCase().replaceAll(/[-_.]+/g, "-");
const project = await readFile(
  path.join(root, "services/parser/pyproject.toml"),
  "utf8",
);
const expected = [
  ...project.matchAll(/^\s*"([a-zA-Z0-9_.-]+)==([^"\s]+)",?$/gm),
].map((match) => [match[1], match[2]]);
expected.push([
  "tuskrag-parser",
  project.match(/^version = "([^"]+)"$/m)?.[1] ?? "",
]);
for (const [name, version] of expected) {
  if (
    !distributions.some(
      (item) =>
        item.name &&
        normalize(item.name) === normalize(name) &&
        item.version === version,
    )
  ) {
    throw new Error(
      `Parser environment does not match manifest: ${name} ${version}`,
    );
  }
}
const texts = [];
async function walk(directory) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const filename = path.join(directory, entry.name);
    if (entry.isDirectory()) await walk(filename);
    else if (
      entry.isFile() &&
      !entry.name.endsWith(".json") &&
      !entry.name.endsWith(".csv") &&
      entry.name !== "README.md"
    ) {
      texts.push({
        path: path.relative(root, filename).replaceAll("\\", "/"),
        sha256: hash(await readFile(filename)),
      });
    }
  }
}
await walk(path.join(root, "deploy/licenses"));
await walk(path.join(root, "deploy/postgres/licenses"));
texts.sort((a, b) => a.path.localeCompare(b.path));
await writeFile(
  path.join(root, "deploy/licenses/inventory.json"),
  `${JSON.stringify({ schemaVersion: 1, lockfiles, parserDistributions: distributions, licenseTexts: texts, projectLicense: "UNRESOLVED", pgSearchLegalReview: "PENDING", limitations: ["Installed Python distributions include development tools; bundled native libraries need separate review.", "Metadata declarations and stored license texts do not constitute legal clearance.", "Go and frontend dependency reports are separate files in this directory."] }, null, 2)}\n`,
);
