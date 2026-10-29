import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";

const source = process.argv[2];
if (process.argv.length !== 3) {
  throw new Error("usage: node s6-seccomp.mjs <docker-default-seccomp.json>");
}
const contents = readFileSync(source);
const hash = createHash("sha256").update(contents).digest("hex");
if (hash !== "01536f1d1df938ae611eba20d6349e0de7a99b6ecdee1549427a0b01b8301e28") {
  throw new Error("unexpected Docker 28.5.2 seccomp profile");
}
const profile = JSON.parse(contents.toString("utf8"));
let removed = 0;
profile.syscalls = profile.syscalls.filter((rule) => {
  if (rule.action === "SCMP_ACT_ERRNO" && rule.errnoRet === 38 && rule.names.includes("clone3")) {
    removed += 1;
    return false;
  }
  return true;
});
if (removed !== 1) throw new Error("unexpected clone3 rule");
profile.syscalls.push({
  names: ["clone3", "clone", "unshare", "mount", "umount2", "pivot_root"],
  action: "SCMP_ACT_ALLOW",
});
process.stdout.write(`${JSON.stringify(profile, null, 2)}\n`);
