/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { registerAccountPreferenceStore, type AccountPreferenceView } from "../../app/preferences";
import {
  DEFAULT_TERMINAL_KEY_IDS,
  loadTerminalKeyConfig,
  resetTerminalKeyConfig,
  saveTerminalKeyConfig,
} from "../../features/terminal/terminalKeys";
import {
  getQuickCommands,
  parseQuickCommands,
  quickCommandError,
  quickCommandListError,
  saveQuickCommands,
  type QuickCommand,
} from "../../features/terminal/quickCommands";
import { flushMacrotask } from "./reactTestUtils";

const LEGACY_KEY_BAR = "nexterm.terminalKeys.v1";

function memoryStore(initial: Record<string, unknown> = {}, options: { failPuts?: number; deviceOnly?: boolean } = {}) {
  const data = { ...initial };
  let failures = options.failPuts ?? 0;
  const store = {
    data,
    puts: [] as Record<string, unknown>[],
    async getView(): Promise<AccountPreferenceView> {
      return { defaults: {}, overrides: { ...data }, deviceOnly: options.deviceOnly };
    },
    async putOverrides(set: Record<string, unknown>) {
      store.puts.push(set);
      if (failures > 0) {
        failures -= 1;
        throw new Error("offline");
      }
      Object.assign(data, set);
    },
    async deleteOverrides(keys: string[]) {
      for (const key of keys) delete data[key];
    },
  };
  return store;
}

async function signIn(store: ReturnType<typeof memoryStore>) {
  registerAccountPreferenceStore(store);
  await flushMacrotask();
  await flushMacrotask();
}

beforeEach(() => {
  registerAccountPreferenceStore(null);
  localStorage.clear();
});

afterEach(() => {
  registerAccountPreferenceStore(null);
  localStorage.clear();
});

describe("key bar layout preference", () => {
  it("imports the device layout into an account that has none, then drops the device copy", async () => {
    const layout = ["tab", "escape", "digit-1", "y", "n"];
    localStorage.setItem(LEGACY_KEY_BAR, JSON.stringify(layout));
    const store = memoryStore();
    await signIn(store);
    expect(store.puts).toEqual([{ "terminal.keyBar": layout }]);
    expect(localStorage.getItem(LEGACY_KEY_BAR)).toBeNull();
    expect(loadTerminalKeyConfig()).toEqual(layout);

    // Another device signed in to the same account gets the imported layout.
    registerAccountPreferenceStore(null);
    expect(loadTerminalKeyConfig()).toEqual(DEFAULT_TERMINAL_KEY_IDS);
    await signIn(memoryStore({ ...store.data }));
    expect(loadTerminalKeyConfig()).toEqual(layout);
  });

  it("keeps the device layout when the import fails and retries on the next load", async () => {
    const layout = ["escape", "enter"];
    localStorage.setItem(LEGACY_KEY_BAR, JSON.stringify(layout));
    const store = memoryStore({}, { failPuts: 1 });
    await signIn(store);
    expect(store.puts).toHaveLength(1);
    expect(localStorage.getItem(LEGACY_KEY_BAR)).toBe(JSON.stringify(layout));
    expect(loadTerminalKeyConfig()).toEqual(layout);

    await signIn(store);
    expect(store.data["terminal.keyBar"]).toEqual(layout);
    expect(localStorage.getItem(LEGACY_KEY_BAR)).toBeNull();
    expect(loadTerminalKeyConfig()).toEqual(layout);
  });

  it("does not overwrite an account layout with the device one", async () => {
    localStorage.setItem(LEGACY_KEY_BAR, JSON.stringify(["escape"]));
    const store = memoryStore({ "terminal.keyBar": ["tab", "up"] });
    await signIn(store);
    expect(store.puts).toEqual([]);
    expect(loadTerminalKeyConfig()).toEqual(["tab", "up"]);
  });

  it("stays on the device without a sync account", async () => {
    const store = memoryStore({}, { deviceOnly: true });
    await signIn(store);
    await saveTerminalKeyConfig(["escape", "digit-2"]);
    expect(store.puts).toEqual([]);
    expect(JSON.parse(localStorage.getItem(LEGACY_KEY_BAR) ?? "null")).toEqual(["escape", "digit-2"]);
  });

  it("saves to the account, reverts a failed write, and resets to the defaults explicitly", async () => {
    const store = memoryStore({ "terminal.keyBar": ["escape"] });
    await signIn(store);
    await saveTerminalKeyConfig(["escape", "tab"]);
    expect(store.data["terminal.keyBar"]).toEqual(["escape", "tab"]);
    expect(localStorage.getItem(LEGACY_KEY_BAR)).toBeNull();

    const failing = memoryStore({ "terminal.keyBar": ["escape"] }, { failPuts: 1 });
    await signIn(failing);
    await expect(saveTerminalKeyConfig(["up"])).rejects.toThrow("offline");
    expect(loadTerminalKeyConfig()).toEqual(["escape"]);

    await resetTerminalKeyConfig();
    expect(failing.data["terminal.keyBar"]).toEqual(DEFAULT_TERMINAL_KEY_IDS);
  });
});

describe("overlapping account writes", () => {
  // A store whose writes settle when the test decides.
  function controlledStore(initial: Record<string, unknown>, defaults: Record<string, unknown> = {}) {
    const data = { ...initial };
    const pending: Array<{ set: Record<string, unknown>; resolve: () => void; reject: (error: Error) => void }> = [];
    return {
      data,
      pending,
      async getView(): Promise<AccountPreferenceView> {
        return { defaults: { ...defaults }, overrides: { ...data } };
      },
      putOverrides(set: Record<string, unknown>) {
        return new Promise<void>((resolve, reject) => {
          pending.push({
            set,
            resolve: () => {
              Object.assign(data, set);
              resolve();
            },
            reject,
          });
        });
      },
      async deleteOverrides() {},
    };
  }

  async function signInControlled(store: ReturnType<typeof controlledStore>) {
    registerAccountPreferenceStore(store);
    await flushMacrotask();
  }

  it("keeps a newer successful write when an older one fails", async () => {
    const store = controlledStore({ "terminal.keyBar": ["escape"] });
    await signInControlled(store);
    const a = saveTerminalKeyConfig(["tab"]);
    const b = saveTerminalKeyConfig(["up"]);
    store.pending[0].reject(new Error("offline"));
    await expect(a).rejects.toThrow("offline");
    expect(loadTerminalKeyConfig()).toEqual(["up"]);
    store.pending[1].resolve();
    await b;
    expect(loadTerminalKeyConfig()).toEqual(["up"]);
  });

  it("rolls a failed latest write back to the newest confirmed value", async () => {
    const store = controlledStore({ "terminal.keyBar": ["escape"] });
    await signInControlled(store);
    const a = saveTerminalKeyConfig(["tab"]);
    const b = saveTerminalKeyConfig(["up"]);
    store.pending[0].resolve();
    await a;
    store.pending[1].reject(new Error("offline"));
    await expect(b).rejects.toThrow("offline");
    expect(loadTerminalKeyConfig()).toEqual(["tab"]);
  });

  it("shows an older write that succeeds after the latest one failed", async () => {
    const store = controlledStore({ "terminal.keyBar": ["escape"] });
    await signInControlled(store);
    const a = saveTerminalKeyConfig(["tab"]);
    const b = saveTerminalKeyConfig(["up"]);
    store.pending[1].reject(new Error("offline"));
    await expect(b).rejects.toThrow("offline");
    expect(loadTerminalKeyConfig()).toEqual(["escape"]);
    store.pending[0].resolve();
    await a;
    expect(loadTerminalKeyConfig()).toEqual(["tab"]);
  });

  it("resets to the server default when one is set", async () => {
    const store = controlledStore({ "terminal.keyBar": ["escape"] }, { "terminal.keyBar": ["tab", "digit-1"] });
    await signInControlled(store);
    const reset = resetTerminalKeyConfig();
    store.pending[0].resolve();
    await reset;
    expect(store.data["terminal.keyBar"]).toEqual(["tab", "digit-1"]);
    expect(loadTerminalKeyConfig()).toEqual(["tab", "digit-1"]);
  });
});

describe("quick commands preference", () => {
  const commands: QuickCommand[] = [
    { label: "状态", text: "git status", appendEnter: true },
    { label: "两行", text: "cd /tmp\nls", appendEnter: false },
  ];

  it("round-trips through the account and reaches another device", async () => {
    const store = memoryStore();
    await signIn(store);
    await saveQuickCommands(commands);
    expect(store.data["terminal.quickCommands"]).toEqual(commands);

    registerAccountPreferenceStore(null);
    expect(getQuickCommands()).toEqual([]);
    await signIn(memoryStore({ ...store.data }));
    expect(getQuickCommands()).toEqual(commands);
  });

  it("imports commands saved on the device before signing in", async () => {
    await saveQuickCommands(commands);
    const store = memoryStore();
    await signIn(store);
    expect(store.data["terminal.quickCommands"]).toEqual(commands);
    expect(getQuickCommands()).toEqual(commands);
  });

  it("validates commands with the server's limits", async () => {
    expect(quickCommandError({ label: "", text: "x", appendEnter: true })).not.toBeNull();
    expect(quickCommandError({ label: "a".repeat(33), text: "x", appendEnter: true })).not.toBeNull();
    expect(quickCommandError({ label: "标".repeat(32), text: "x", appendEnter: true })).toBeNull();
    expect(quickCommandError({ label: "a\nb", text: "x", appendEnter: true })).not.toBeNull();
    expect(quickCommandError({ label: "a", text: "", appendEnter: true })).not.toBeNull();
    expect(quickCommandError({ label: "a", text: "x\0", appendEnter: true })).not.toBeNull();
    expect(quickCommandError({ label: "a", text: "x".repeat(4097), appendEnter: true })).not.toBeNull();
    expect(quickCommandError({ label: "a", text: "x".repeat(4096), appendEnter: true })).toBeNull();
    const many = Array.from({ length: 51 }, (_, i) => ({ label: `c${i}`, text: "echo", appendEnter: true }));
    expect(quickCommandListError(many)).not.toBeNull();
    expect(quickCommandListError(many.slice(0, 50))).toBeNull();
    await expect(saveQuickCommands(many)).rejects.toThrow();
    expect(getQuickCommands()).toEqual([]);
  });

  it("skips invalid stored entries", () => {
    expect(parseQuickCommands("x")).toBeNull();
    expect(
      parseQuickCommands([
        { label: "ok", text: "ls", appendEnter: true },
        { label: "", text: "ls", appendEnter: true },
        { label: "no flag", text: "ls" },
        null,
      ]),
    ).toEqual([{ label: "ok", text: "ls", appendEnter: true }]);
  });
});
