import assert from "node:assert/strict";
import test from "node:test";
import { Linter } from "eslint";
import { storeSubscriptionPlugin } from "./lib/store-subscription-rule.mjs";

function lint(code) {
  const linter = new Linter();
  return linter
    .verify(
      code,
      [
        {
          files: ["**/*.js"],
          languageOptions: { ecmaVersion: "latest", sourceType: "module" },
          plugins: { nexterm: storeSubscriptionPlugin },
          rules: { "nexterm/no-whole-collection-subscription": "error" },
        },
      ],
      "sample.js",
    )
    .map((message) => message.message);
}

const flagged = [
  "useUi((s) => s.sessions);",
  "useUi((s) => s.workspaces);",
  "useUi(function (s) { return s.sessions; });",
  "useUi(({ sessions }) => sessions);",
  "useUi(({ workspaces: all }) => all);",
  "useUi(({ sessions }) => { return sessions; });",
  "useUi(useShallow((s) => s.sessions));",
  "useUi(useShallow(({ workspaces }) => workspaces));",
];

const allowed = [
  "useUi((s) => s.activeWorkspaceId);",
  "useUi((s) => s.sessions.length);",
  "useUi((s) => s.sessions.find((x) => x.id === id));",
  "useUi(({ sessions }) => sessions.length);",
  "useUi(({ activeWorkspaceId }) => activeWorkspaceId);",
  "useUi(({ sessions: _ }) => 1);",
  "useUi(useShallow((s) => ({ a: s.activeWorkspaceId })));",
  "useUi(useShallow((s) => s.sessions.map((x) => x.id)));",
  "other((s) => s.sessions);",
];

for (const code of flagged) {
  test(`flags ${code}`, () => {
    const messages = lint(code);
    assert.equal(messages.length, 1);
    assert.match(messages[0], /Subscribing to the whole (sessions|workspaces) collection/);
  });
}

for (const code of allowed) {
  test(`allows ${code}`, () => {
    assert.deepEqual(lint(code), []);
  });
}
