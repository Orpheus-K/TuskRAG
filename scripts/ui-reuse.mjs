import ts from "typescript";
import fs from "node:fs";
import path from "node:path";

const root = "apps/web/src";
const components = new Map();
let nativeControls = 0;
let sharedControls = 0;
let calls = 0;
const controls = new Set([
  "Button",
  "Input",
  "Textarea",
  "Select",
  "Checkbox",
  "ConfirmAction",
  "CollapsibleTrigger",
  "DropdownMenuItem",
]);
for (const relative of fs.readdirSync(root, { recursive: true })) {
  if (!relative.endsWith(".tsx") || relative.endsWith(".test.tsx")) continue;
  const file = path.join(root, relative);
  const tree = ts.createSourceFile(
    file,
    fs.readFileSync(file, "utf8"),
    ts.ScriptTarget.Latest,
    true,
  );
  const names = new Map();
  for (const stmt of tree.statements) {
    if (
      !ts.isImportDeclaration(stmt) ||
      !stmt.moduleSpecifier.text.startsWith("@tuskrag/ui/")
    )
      continue;
    const bindings = stmt.importClause?.namedBindings;
    if (!bindings || !ts.isNamedImports(bindings)) continue;
    for (const item of bindings.elements)
      names.set(item.name.text, (item.propertyName ?? item.name).text);
  }
  function visit(node) {
    if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
      const tag = node.tagName.getText(tree);
      if (
        [
          "button",
          "input",
          "textarea",
          "select",
          "option",
          "details",
          "summary",
          "dialog",
          "label",
        ].includes(tag)
      )
        nativeControls++;
      const name = names.get(tag);
      if (name) {
        calls++;
        if (controls.has(name)) sharedControls++;
        if (!components.has(name)) components.set(name, new Set());
        components.get(name).add(file);
      }
    }
    ts.forEachChild(node, visit);
  }
  visit(tree);
}
const crossFile = [...components.values()].filter(
  (files) => files.size > 1,
).length;
console.log({
  sharedComponents: components.size,
  crossFile,
  calls,
  sharedControls,
  nativeControls,
});
if (nativeControls) process.exitCode = 1;
