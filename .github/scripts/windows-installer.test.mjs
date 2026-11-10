import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const script = readFileSync(new URL("../packaging/windows/NexTerm.nsi", import.meta.url), "utf8");

// Returns the LogicLib conditions enclosing each line that matches pattern.
function enclosingConditions(pattern) {
  const stack = [];
  const matches = [];
  for (const raw of script.split(/\r?\n/)) {
    const line = raw.trim();
    if (line.startsWith(";") || line.startsWith("#")) continue;
    if (/^\$\{(If|IfNot|Unless)\}/.test(line)) {
      stack.push(line);
    } else if (/^\$\{EndIf\}/.test(line)) {
      stack.pop();
    } else if (pattern.test(line)) {
      matches.push([...stack]);
    }
  }
  return matches;
}

test("silent installs (in-app updates) never recreate the desktop shortcut", () => {
  assert.match(script, /^!include "LogicLib\.nsh"/m);
  const shortcuts = enclosingConditions(/^CreateShortcut\s+"\$DESKTOP\\/i);
  assert.ok(shortcuts.length > 0, "installer no longer creates a desktop shortcut");
  for (const conditions of shortcuts) {
    assert.ok(
      conditions.some((condition) => /^\$\{IfNot\}\s+\$\{Silent\}$/.test(condition)),
      `desktop shortcut is created outside \${IfNot} \${Silent}: ${JSON.stringify(conditions)}`,
    );
  }
});
