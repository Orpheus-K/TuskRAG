import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";

// Inspect tracked content only; report paths, never matching credential values.
const files = execFileSync("git", ["ls-files", "-z"], {
  encoding: "utf8",
})
  .split("\0")
  .filter(Boolean);
const patterns = [
  /\bsk-[A-Za-z0-9_-]{20,}/,
  /\bgh[pousr]_[A-Za-z0-9]{20,}/,
  /-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----/,
];
const failures = [];
for (const file of files) {
  const data = readFileSync(file);
  if (data.includes(0)) continue;
  const text = data.toString("utf8");
  if (patterns.some((pattern) => pattern.test(text))) failures.push(file);
}
if (failures.length) {
  console.error(`Credential-like content in: ${failures.join(", ")}`);
  process.exitCode = 1;
} else {
  console.log(
    `Credential pattern scan passed for ${files.length} tracked files.`,
  );
}
