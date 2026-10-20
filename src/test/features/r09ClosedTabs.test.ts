/** @vitest-environment jsdom */
import { describe, expect, it } from "vitest";

import {
  CLOSED_TAB_STACK_LIMIT,
  closedTabEntryFromTab,
  createClosedTabStack,
  deserializeClosedTabStack,
  pushClosedTab,
  restoreClosedTab,
  restoreMostRecentClosedTab,
  serializeClosedTabStack,
  switchClosedTabAccount,
  type ClosedTabEntry,
  type ClosedTabStack,
} from "../../app/closedTabs";
import type { AppTab } from "../../app/store";

function terminalTab(overrides: Partial<AppTab> = {}): AppTab {
  return {
    id: "tab-1",
    kind: "terminal",
    title: "终端",
    sessionId: "session-1",
    assetId: "asset-1",
    tabId: "kernel-1",
    closable: true,
    ...overrides,
  };
}

function entryFrom(tab: AppTab, options?: { workspaceId?: string; detached?: boolean }): ClosedTabEntry {
  const entry = closedTabEntryFromTab(tab, options);
  if (!entry) throw new Error("expected a restorable closed-tab entry");
  return entry;
}

function pushedStack(tabs: AppTab[], accountId = "account-1") {
  let stack = createClosedTabStack(accountId);
  for (const tab of tabs) stack = pushClosedTab(stack, entryFrom(tab));
  return stack;
}

describe("closed-tab restore semantics", () => {
  it("restores a detached terminal by reattaching to the still-running process without dialing a new connection", () => {
    const tab = terminalTab({ containerId: "container-1" });
    const entry = entryFrom(tab, { workspaceId: "ws-1", detached: true });
    expect(entry.kind).toBe("detached-terminal");
    expect(entry.tabId).toBe("kernel-1");
    expect(entry.workspaceId).toBe("ws-1");

    const stack = pushClosedTab(createClosedTabStack("account-1"), entry);
    const result = restoreMostRecentClosedTab(stack);
    expect(result.restored?.mode).toBe("reattach");
    expect(result.restored?.tab.kind).toBe("terminal");
    expect(result.restored?.tab.tabId).toBe("kernel-1");
    expect(result.restored?.tab.restorePending).toBeUndefined();
    expect(result.restored?.tab.sessionId).toBe("session-1");
    expect(result.restored?.tab.assetId).toBe("asset-1");
    expect(result.restored?.tab.containerId).toBe("container-1");
    expect(result.stack.entries).toEqual([]);
  });

  it("restores a killed terminal as a disconnected tab that requires an explicit reconnect", () => {
    const tab = terminalTab({ tabId: "kernel-dead" });
    const entry = entryFrom(tab);
    expect(entry.kind).toBe("terminal");
    expect(entry.tabId).toBeUndefined();

    const stack = pushClosedTab(createClosedTabStack("account-1"), entry);
    const result = restoreMostRecentClosedTab(stack);
    expect(result.restored?.mode).toBe("reconnect");
    expect(result.restored?.tab.restorePending).toBe(true);
    expect(result.restored?.tab.tabId).toBeUndefined();
  });

  it("never detaches an exited or dead terminal even when detach is requested", () => {
    expect(entryFrom(terminalTab({ exited: true }), { detached: true }).kind).toBe("terminal");
    expect(entryFrom(terminalTab({ dead: true }), { detached: true }).kind).toBe("terminal");
    expect(entryFrom(terminalTab({ exited: true }), { detached: true }).tabId).toBeUndefined();
  });

  it("restores a file tab with its path and a pending session reconnect instead of a live connection", () => {
    const tab: AppTab = {
      id: "file-1",
      kind: "files",
      title: "hosts",
      sessionId: "session-1",
      path: "/etc/hosts",
      closable: true,
    };
    const entry = entryFrom(tab, { workspaceId: "ws-1" });
    expect(entry.kind).toBe("file");
    expect(entry.key).toBe("file:files:session-1:/etc/hosts");

    const stack = pushClosedTab(createClosedTabStack("account-1"), entry);
    const result = restoreMostRecentClosedTab(stack);
    expect(result.restored?.mode).toBe("reopen");
    expect(result.restored?.tab.kind).toBe("files");
    expect(result.restored?.tab.path).toBe("/etc/hosts");
    expect(result.restored?.tab.restorePending).toBe(true);
    expect(result.restored?.tab.tabId).toBeUndefined();
  });

  it("restores the background session list without any connection or live process", () => {
    const tab: AppTab = {
      id: "background-ws-1",
      kind: "background",
      title: "后台会话",
      sessionId: "session-1",
      closable: true,
    };
    const entry = entryFrom(tab, { workspaceId: "ws-1" });
    expect(entry.kind).toBe("detached-terminal");
    expect(entry.tabId).toBeUndefined();

    const stack = pushClosedTab(createClosedTabStack("account-1"), entry);
    const result = restoreMostRecentClosedTab(stack);
    expect(result.restored?.mode).toBe("reopen");
    expect(result.restored?.tab.kind).toBe("background");
    expect(result.restored?.tab.tabId).toBeUndefined();
    expect(result.restored?.tab.restorePending).toBeUndefined();
  });

  it("rejects tabs outside the restorable terminal/file/detached-terminal kinds", () => {
    const unsupported: Partial<AppTab>[] = [
      { kind: "db", connId: "conn-1", dbKind: "mysql" },
      { kind: "settings" },
      { kind: "credentials", credId: "cred-1" },
      { kind: "forward" },
      { kind: "docker", containerId: "container-1" },
      { kind: "mount" },
      { kind: "history" },
      { kind: "execBatch" },
      { kind: "devices" },
      { kind: "audit" },
    ];
    for (const kind of unsupported) {
      expect(closedTabEntryFromTab(terminalTab(kind))).toBeNull();
    }
    expect(closedTabEntryFromTab(terminalTab({ kind: "log", path: undefined }))).toBeNull();
    expect(
      closedTabEntryFromTab({ id: "files-1", kind: "files", title: "文件", closable: true }),
    ).toBeNull();
  });

  it("restoring an unknown key returns null and leaves the stack untouched", () => {
    const stack = pushedStack([terminalTab({ id: "tab-1" })]);
    const result = restoreClosedTab(stack, "missing");
    expect(result.restored).toBeNull();
    expect(result.stack).toBe(stack);
  });

  it("pops the restored entry so the same entry cannot be restored twice", () => {
    const stack = pushedStack([terminalTab({ id: "tab-1" })]);
    const first = restoreMostRecentClosedTab(stack);
    expect(first.restored).not.toBeNull();
    const second = restoreMostRecentClosedTab(first.stack);
    expect(second.restored).toBeNull();
    expect(second.stack.entries).toEqual([]);
  });
});

describe("path-less files browser restore", () => {
  // Real shape from App.tsx openFiles: a session-scoped files browser tab without path.
  function filesBrowserTab(sessionId: string, id: string): AppTab {
    return { id, kind: "files", title: "文件", sessionId, closable: true };
  }

  it("accepts the session-scoped files browser tab and restores it as reopen + restorePending without a connection", () => {
    const entry = entryFrom(filesBrowserTab("session-1", "files-session-1-7"), { workspaceId: "ws-1" });
    expect(entry.kind).toBe("file");
    expect(entry.tabKind).toBe("files");
    expect(entry.key).toBe("files:session-1");
    expect(entry.path).toBeUndefined();

    const stack = pushClosedTab(createClosedTabStack("account-1"), entry);
    const result = restoreMostRecentClosedTab(stack);
    expect(result.restored?.mode).toBe("reopen");
    expect(result.restored?.tab.kind).toBe("files");
    expect(result.restored?.tab.sessionId).toBe("session-1");
    expect(result.restored?.tab.path).toBeUndefined();
    expect(result.restored?.tab.tabId).toBeUndefined();
    expect(result.restored?.tab.restorePending).toBe(true);
  });

  it("dedupes the browser per session while browsers of different sessions stay distinct", () => {
    let stack = createClosedTabStack("account-1");
    stack = pushClosedTab(stack, entryFrom(filesBrowserTab("session-1", "files-session-1-1")));
    stack = pushClosedTab(stack, entryFrom(filesBrowserTab("session-2", "files-session-2-1")));
    stack = pushClosedTab(stack, entryFrom(filesBrowserTab("session-1", "files-session-1-2")));
    expect(stack.entries).toHaveLength(2);
    expect(stack.entries[0]?.key).toBe("files:session-1");
    expect(stack.entries[0]?.id).toBe("files-session-1-2");
    expect(stack.entries[1]?.key).toBe("files:session-2");
  });

  it("keeps the path-less browser entry restorable across a serialize/deserialize round trip", () => {
    let stack = createClosedTabStack("account-1");
    stack = pushClosedTab(stack, entryFrom(filesBrowserTab("session-1", "files-session-1-1"), { workspaceId: "ws-1" }));
    const restored = deserializeClosedTabStack(serializeClosedTabStack(stack), "account-1");
    expect(restored.entries).toHaveLength(1);
    const result = restoreMostRecentClosedTab(restored);
    expect(result.restored?.mode).toBe("reopen");
    expect(result.restored?.tab.kind).toBe("files");
    expect(result.restored?.tab.sessionId).toBe("session-1");
    expect(result.restored?.tab.restorePending).toBe(true);
  });
});

describe("files and log tab identity", () => {
  const editorTab = (id: string): AppTab => ({
    id,
    kind: "files",
    title: "one.txt",
    sessionId: "session-1",
    path: "/a/one.txt",
    closable: true,
  });
  const logTab = (id: string): AppTab => ({
    id,
    kind: "log",
    title: "one.txt",
    sessionId: "session-1",
    path: "/a/one.txt",
    closable: true,
  });

  it("keeps a files editor and a log viewer for the same session and path distinct in both close orders", () => {
    const orders: [AppTab, AppTab][] = [
      [editorTab("editor-1"), logTab("log-1")],
      [logTab("log-1"), editorTab("editor-1")],
    ];
    for (const [first, second] of orders) {
      let stack = createClosedTabStack("account-1");
      stack = pushClosedTab(stack, entryFrom(first));
      stack = pushClosedTab(stack, entryFrom(second));
      expect(stack.entries).toHaveLength(2);

      const roundTripped = deserializeClosedTabStack(serializeClosedTabStack(stack), "account-1");
      expect(roundTripped.entries).toHaveLength(2);

      const restoredSecond = restoreMostRecentClosedTab(roundTripped);
      expect(restoredSecond.restored?.tab.kind).toBe(second.kind);
      expect(restoredSecond.restored?.tab.path).toBe("/a/one.txt");
      const restoredFirst = restoreMostRecentClosedTab(restoredSecond.stack);
      expect(restoredFirst.restored?.tab.kind).toBe(first.kind);
      expect(restoredFirst.restored?.tab.path).toBe("/a/one.txt");
      expect(restoreMostRecentClosedTab(restoredFirst.stack).restored).toBeNull();
    }
  });

  it("still dedupes repeated closes of the same file kind", () => {
    let stack = createClosedTabStack("account-1");
    stack = pushClosedTab(stack, entryFrom(editorTab("editor-1")));
    stack = pushClosedTab(stack, entryFrom(logTab("log-1")));
    stack = pushClosedTab(stack, entryFrom(editorTab("editor-2")));
    stack = pushClosedTab(stack, entryFrom(logTab("log-2")));
    expect(stack.entries).toHaveLength(2);
    expect(stack.entries[0]?.id).toBe("log-2");
    expect(stack.entries[1]?.id).toBe("editor-2");
  });
});

describe("bounded closed-tab stack", () => {
  it("keeps at most 20 entries, most recent first, and evicts the oldest beyond the bound", () => {
    const tabs: AppTab[] = [];
    for (let index = 0; index < CLOSED_TAB_STACK_LIMIT + 1; index += 1) {
      tabs.push(terminalTab({ id: `tab-${index}`, tabId: `kernel-${index}`, title: `终端 ${index}` }));
    }
    const stack = pushedStack(tabs);
    expect(stack.entries).toHaveLength(CLOSED_TAB_STACK_LIMIT);
    expect(stack.entries[0]?.id).toBe("tab-20");
    expect(stack.entries.map((entry) => entry.id)).not.toContain("tab-0");
    const seqs = stack.entries.map((entry) => entry.seq);
    expect([...seqs].sort((left, right) => right - left)).toEqual(seqs);
  });

  it("a duplicate close moves the existing entry to the top instead of adding a second one", () => {
    const first = terminalTab({ id: "tab-1", tabId: "kernel-1" });
    const second = terminalTab({ id: "tab-2", tabId: "kernel-2", title: "终端 2" });
    let stack = pushedStack([first, second]);
    const closedAgain = entryFrom(terminalTab({ id: "tab-3", tabId: "kernel-1" }), { detached: true });
    stack = pushClosedTab(stack, closedAgain);

    expect(stack.entries).toHaveLength(2);
    expect(stack.entries[0]?.key).toBe(closedAgain.key);
    expect(stack.entries[0]?.id).toBe("tab-3");
    expect(stack.entries[0]?.seq).toBeGreaterThan(stack.entries[1]?.seq ?? 0);
    expect(stack.entries[1]?.id).toBe("tab-2");
  });

  it("rejects invalid entries without changing the stack", () => {
    const stack = pushedStack([terminalTab({ id: "tab-1" })]);
    const invalid: unknown[] = [
      { ...entryFrom(terminalTab({ id: "tab-9" })), key: "" },
      { ...entryFrom(terminalTab({ id: "tab-9" })), id: "" },
      { ...entryFrom(terminalTab({ id: "tab-9" })), title: "" },
      { ...entryFrom(terminalTab({ id: "tab-9" })), kind: "other" },
      { ...entryFrom(terminalTab({ id: "tab-9" })), tabKind: "settings" },
      { key: "detached:tab:kernel-9", kind: "detached-terminal", tabKind: "terminal", id: "tab-9", title: "t", seq: 1 },
      { key: "k", kind: "terminal", tabKind: "terminal", id: "tab-9", title: "t", seq: 1, sessionId: "" },
      { key: "k", kind: "terminal", tabKind: "terminal", id: "tab-9", title: "t", seq: 1, tabId: "kernel-9" },
      { key: "k", kind: "file", tabKind: "files", id: "tab-9", title: "t", seq: 1, path: "/p", tabId: "kernel-9" },
    ];
    for (const entry of invalid) {
      expect(pushClosedTab(stack, entry as ClosedTabEntry)).toBe(stack);
    }
    expect(stack.entries).toHaveLength(1);
  });
});

describe("per-account generation isolation", () => {
  it("switching to the same account keeps the stack untouched", () => {
    const stack = pushedStack([terminalTab({ id: "tab-1" })]);
    expect(switchClosedTabAccount(stack, "account-1")).toBe(stack);
  });

  it("switching accounts clears entries and bumps the generation while keeping the sequence monotonic", () => {
    const stack = pushedStack([terminalTab({ id: "tab-1" })]);
    const switched = switchClosedTabAccount(stack, "account-2");
    expect(switched.accountId).toBe("account-2");
    expect(switched.entries).toEqual([]);
    expect(switched.generation).toBe(stack.generation + 1);
    expect(switched.nextSeq).toBe(stack.nextSeq);

    const afterSwitch = pushClosedTab(switched, entryFrom(terminalTab({ id: "tab-2" })));
    expect(afterSwitch.entries[0]?.seq).toBeGreaterThan(stack.entries[0]?.seq ?? 0);
  });

  it("fails closed on consecutive switches once the generation ceiling is reached", () => {
    const atCeiling = switchClosedTabAccount(createClosedTabStack("account-1", Number.MAX_SAFE_INTEGER - 1), "account-2");
    expect(atCeiling.generation).toBe(Number.MAX_SAFE_INTEGER);
    expect(atCeiling.entries).toEqual([]);

    expect(() => switchClosedTabAccount(atCeiling, "account-3")).toThrow(RangeError);
    expect(() =>
      switchClosedTabAccount(createClosedTabStack("account-1", Number.MAX_SAFE_INTEGER), "account-2"),
    ).toThrow(RangeError);
    expect(switchClosedTabAccount(atCeiling, "account-2")).toBe(atCeiling);
  });
});

describe("closed-tab stack serialization", () => {
  it("round-trips entries deterministically for the same account", () => {
    let stack = createClosedTabStack("account-1");
    stack = pushClosedTab(stack, entryFrom(terminalTab({ id: "tab-1", tabId: "kernel-1" })));
    stack = pushClosedTab(
      stack,
      entryFrom(terminalTab({ id: "tab-2", tabId: "kernel-2" }), { detached: true, workspaceId: "ws-1" }),
    );
    stack = pushClosedTab(
      stack,
      entryFrom(
        { id: "file-1", kind: "files", title: "hosts", sessionId: "session-1", path: "/etc/hosts", closable: true },
        { workspaceId: "ws-1" },
      ),
    );

    const text = serializeClosedTabStack(stack);
    expect(text).toBe(serializeClosedTabStack(stack));
    const restored = deserializeClosedTabStack(text, "account-1");
    expect(restored).toEqual(stack);
    expect(serializeClosedTabStack(restored)).toBe(text);
  });

  it("deserializing another account's payload yields an isolated empty stack with a bumped generation", () => {
    const stack = pushedStack([terminalTab({ id: "tab-1" })]);
    const text = serializeClosedTabStack(stack);
    const restored = deserializeClosedTabStack(text, "account-2");
    expect(restored.accountId).toBe("account-2");
    expect(restored.entries).toEqual([]);
    expect(restored.generation).toBe(stack.generation + 1);
  });

  it("fails closed on a foreign account payload at the generation ceiling", () => {
    const atCeiling = serializeClosedTabStack(createClosedTabStack("account-1", Number.MAX_SAFE_INTEGER));
    expect(() => deserializeClosedTabStack(atCeiling, "account-2")).toThrow(RangeError);

    const oneBelow = serializeClosedTabStack(createClosedTabStack("account-1", Number.MAX_SAFE_INTEGER - 1));
    const isolated = deserializeClosedTabStack(oneBelow, "account-2");
    expect(isolated.accountId).toBe("account-2");
    expect(isolated.entries).toEqual([]);
    expect(isolated.generation).toBe(Number.MAX_SAFE_INTEGER);
  });

  it("round-trips a stack at the generation ceiling without losing the generation", () => {
    let stack = createClosedTabStack("account-1", Number.MAX_SAFE_INTEGER);
    stack = pushClosedTab(stack, entryFrom(terminalTab({ id: "tab-1" })));
    const restored = deserializeClosedTabStack(serializeClosedTabStack(stack), "account-1");
    expect(restored).toEqual(stack);
    expect(restored.generation).toBe(Number.MAX_SAFE_INTEGER);

    const switched = switchClosedTabAccount(createClosedTabStack("account-1", Number.MAX_SAFE_INTEGER - 1), "account-2");
    const roundTripped = deserializeClosedTabStack(serializeClosedTabStack(switched), "account-2");
    expect(roundTripped.generation).toBe(Number.MAX_SAFE_INTEGER);
  });

  it("drops invalid entries and caps the stack at 20 entries on deserialize", () => {
    const valid = (index: number) => ({
      key: `terminal:tab-${index}`,
      kind: "terminal",
      tabKind: "terminal",
      id: `tab-${index}`,
      title: `终端 ${index}`,
      seq: index + 1,
    });
    const payload = {
      v: 1,
      accountId: "account-1",
      generation: 3,
      nextSeq: 999,
      entries: [
        ...Array.from({ length: CLOSED_TAB_STACK_LIMIT + 5 }, (_, index) => valid(CLOSED_TAB_STACK_LIMIT + 4 - index)),
        null,
        "garbage",
        { key: "", kind: "terminal", tabKind: "terminal", id: "x", title: "x", seq: 1 },
        { key: "k", kind: "detached-terminal", tabKind: "terminal", id: "x", title: "x", seq: 2 },
        { key: "k", kind: "terminal", tabKind: "terminal", id: "x", title: "x", seq: 0 },
      ],
    };
    const restored = deserializeClosedTabStack(JSON.stringify(payload), "account-1");
    expect(restored.entries).toHaveLength(CLOSED_TAB_STACK_LIMIT);
    expect(restored.entries[0]?.id).toBe(`tab-${CLOSED_TAB_STACK_LIMIT + 4}`);
    expect(restored.generation).toBe(3);
    expect(restored.nextSeq).toBe(999);
    expect(restored.accountId).toBe("account-1");
  });

  it("keeps the first occurrence when a crafted payload repeats a key", () => {
    const entry = {
      key: "terminal:tab-1",
      kind: "terminal",
      tabKind: "terminal",
      id: "tab-1",
      title: "终端",
      seq: 2,
    };
    const payload = {
      v: 1,
      accountId: "account-1",
      generation: 0,
      nextSeq: 3,
      entries: [entry, { ...entry, id: "tab-1-dupe", seq: 1 }],
    };
    const restored = deserializeClosedTabStack(JSON.stringify(payload), "account-1");
    expect(restored.entries).toHaveLength(1);
    expect(restored.entries[0]?.id).toBe("tab-1");
  });

  it("corrupt or non-envelope payloads degrade to an empty stack", () => {
    for (const raw of ["not json", "[]", "null", "{}", '{"v":2,"accountId":"account-1"}']) {
      const restored = deserializeClosedTabStack(raw, "account-1");
      expect(restored.accountId).toBe("account-1");
      expect(restored.entries).toEqual([]);
      expect(restored.generation).toBe(0);
      expect(restored.nextSeq).toBe(1);
    }
  });
});

describe("sequence overflow boundaries", () => {
  const terminalEntry = (id: string, seq: number) => ({
    key: `terminal:tab-${id}`,
    kind: "terminal",
    tabKind: "terminal",
    id,
    title: `终端 ${id}`,
    seq,
  });

  function expectSafeSeqs(stack: ClosedTabStack) {
    expect(Number.isSafeInteger(stack.nextSeq)).toBe(true);
    expect(stack.nextSeq).toBeGreaterThan(0);
    const seqs = stack.entries.map((entry) => entry.seq);
    for (const seq of seqs) expect(Number.isSafeInteger(seq)).toBe(true);
    expect(stack.nextSeq).toBeGreaterThan(Math.max(0, ...seqs));
  }

  it("keeps allocating safe sequence numbers after a payload at MAX_SAFE_INTEGER and loses nothing on round trip", () => {
    const payload = {
      v: 1,
      accountId: "account-1",
      generation: 0,
      nextSeq: Number.MAX_SAFE_INTEGER,
      entries: [terminalEntry("t", 1)],
    };
    let stack = deserializeClosedTabStack(JSON.stringify(payload), "account-1");
    expect(stack.entries.map((entry) => entry.id)).toEqual(["t"]);
    expectSafeSeqs(stack);

    stack = pushClosedTab(stack, entryFrom(terminalTab({ id: "u", tabId: "kernel-u" })));
    stack = pushClosedTab(stack, entryFrom(terminalTab({ id: "v", tabId: "kernel-v" })));
    expect(stack.entries.map((entry) => entry.id)).toEqual(["v", "u", "t"]);
    expectSafeSeqs(stack);

    const roundTripped = deserializeClosedTabStack(serializeClosedTabStack(stack), "account-1");
    expect(roundTripped.entries.map((entry) => entry.id)).toEqual(["v", "u", "t"]);
    expectSafeSeqs(roundTripped);
  });

  it("deterministically re-stamps entries whose seq sits at MAX_SAFE_INTEGER", () => {
    const payload = {
      v: 1,
      accountId: "account-1",
      generation: 0,
      nextSeq: Number.MAX_SAFE_INTEGER,
      entries: [terminalEntry("new", Number.MAX_SAFE_INTEGER), terminalEntry("old", Number.MAX_SAFE_INTEGER - 1)],
    };
    const restored = deserializeClosedTabStack(JSON.stringify(payload), "account-1");
    expect(restored.entries.map((entry) => entry.id)).toEqual(["new", "old"]);
    expect(restored.entries.map((entry) => entry.seq)).toEqual([2, 1]);
    expect(restored.nextSeq).toBe(3);
    expectSafeSeqs(restored);
    expect(deserializeClosedTabStack(serializeClosedTabStack(restored), "account-1")).toEqual(restored);
  });

  it("re-stamps a hand-crafted stack with a non-allocatable nextSeq instead of emitting unsafe seqs", () => {
    const corrupt: ClosedTabStack = {
      ...createClosedTabStack("account-1"),
      nextSeq: Number.MAX_SAFE_INTEGER + 1,
      entries: [entryFrom(terminalTab({ id: "t", tabId: "kernel-t" }))],
    };
    const stack = pushClosedTab(corrupt, entryFrom(terminalTab({ id: "u", tabId: "kernel-u" })));
    expect(stack.entries.map((entry) => entry.id)).toEqual(["u", "t"]);
    expect(stack.entries[0]?.seq).toBe(2);
    expect(stack.entries[1]?.seq).toBe(1);
    expect(stack.nextSeq).toBe(3);
    expectSafeSeqs(stack);
  });
});
