import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ write: vi.fn(), ask: vi.fn(), toast: vi.fn() }));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});
vi.mock("../../ipc/commands", () => ({ terminalApi: { write: mocks.write } }));

import { executeSnippet, type Snippet } from "../../features/explorer/snippetInsert";
import { snippetRuns } from "../../features/explorer/snippetRuns";
import { publishTerminalOutput, registerTerminalScreenSource } from "../../features/terminal/terminalOutputTap";
import { useUi, type AppTab } from "../../app/store";

type WaitStep = { name?: string; command: string; waitFor?: string; waitForRegex?: boolean; timeoutMs?: number; confirm?: boolean };

function waiting(steps: WaitStep[]): Snippet {
  return {
    id: "w1", name: "login", groupId: null, sort: 1,
    body: steps.map((s) => s.command).join("\n"),
    steps: steps.map((s, i) => ({ id: `s${i}`, ...s, name: s.name ?? "" })),
  };
}

function seedTerminal() {
  const tab: AppTab = { id: "tab-1", kind: "terminal", title: "T", tabId: "term-1", closable: true };
  useUi.setState({
    workspaces: [{
      id: "ws-1", kind: "session", title: "W",
      panes: [{ id: "pane-1", tabs: [tab], activeTabId: tab.id }],
      activePaneId: "pane-1", closable: true,
    }],
    activeWorkspaceId: "ws-1",
  });
}

function written(): string[] {
  return mocks.write.mock.calls.map(([, data]) => new TextDecoder().decode(data as Uint8Array));
}

const emit = (text: string) => publishTerminalOutput("term-1", new TextEncoder().encode(text));

// The fake terminal echoes each written command and answers it with the given output.
function answer(replies: Record<string, string>) {
  mocks.write.mockImplementation(async (_tabId: string, data: Uint8Array) => {
    const command = new TextDecoder().decode(data).replace(/\r$/, "");
    const reply = replies[command];
    if (reply !== undefined) queueMicrotask(() => emit(`${command}\r\n${reply}`));
  });
}

// Lets the run reach its next wait.
const settle = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

let offScreen: () => void = () => undefined;
function screen(text: string) {
  offScreen();
  offScreen = registerTerminalScreenSource({ tabId: () => "term-1", screenText: () => text });
}

beforeEach(() => {
  vi.resetAllMocks();
  mocks.write.mockResolvedValue(undefined);
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ workspaces: [], activeWorkspaceId: null, toasts: [], pushToast: mocks.toast });
  seedTerminal();
  screen("");
});

afterEach(() => {
  offScreen();
  vi.useRealTimers();
});

describe("multi-step snippet steps that wait for output", () => {
  it("sends the first step when the screen matches and each later step when the previous output matches", async () => {
    screen("user@host:~$ ");
    answer({ "ssh db": "\x1b[1mPassword: \x1b[0m", hunter2: "\x1b[32mroot@db:~#\x1b[0m " });
    const onWaiting = vi.fn();
    const result = await executeSnippet(waiting([
      { command: "ssh db", waitFor: "$ " },
      { command: "hunter2", waitFor: "Password:" },
      { command: "uptime", waitFor: "[#$] $", waitForRegex: true },
    ]), { onWaiting });
    expect(result).toBe(true);
    expect(written()).toEqual(["ssh db\r", "hunter2\r", "uptime\r"]);
    expect(onWaiting).toHaveBeenCalledTimes(1);
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("login"));
    expect(snippetRuns()).toEqual([]);
  });

  it("holds a step until its output arrives", async () => {
    const run = executeSnippet(waiting([{ command: "sudo -i" }, { command: "secret", waitFor: "password for" }]));
    await settle();
    expect(written()).toEqual(["sudo -i\r"]);
    expect(snippetRuns()).toMatchObject([{ name: "login", step: 2, total: 2, waitingFor: "password for" }]);
    emit("[sudo] password for ");
    emit("alice: ");
    expect(await run).toBe(true);
    expect(written()).toEqual(["sudo -i\r", "secret\r"]);
  });

  it("does not match output that arrived before the previous step was sent", async () => {
    screen("old Password: prompt");
    const run = executeSnippet(waiting([{ command: "first" }, { command: "second", waitFor: "Password:", timeoutMs: 1000 }]));
    await settle();
    expect(written()).toEqual(["first\r"]);
    emit("Pass");
    emit("word:");
    expect(await run).toBe(true);
  });

  it("stops at the timeout and names the step", async () => {
    vi.useFakeTimers();
    const run = executeSnippet(waiting([
      { command: "ssh db" },
      { name: "enter password", command: "hunter2", waitFor: "Password:", timeoutMs: 3000 },
      { command: "uptime" },
    ]));
    await vi.advanceTimersByTimeAsync(0);
    emit("Connection refused\r\n$ ");
    await vi.advanceTimersByTimeAsync(3000);
    expect(await run).toBe(false);
    expect(written()).toEqual(["ssh db\r"]);
    const [kind, text] = mocks.toast.mock.calls.at(-1) ?? [];
    expect(kind).toBe("error");
    expect(text).toContain("enter password");
    expect(text).toContain("Password:");
    expect(snippetRuns()).toEqual([]);
  });

  it("stops when the user cancels a wait", async () => {
    const run = executeSnippet(waiting([{ command: "a" }, { command: "b", waitFor: "never" }, { command: "c" }]));
    await settle();
    expect(snippetRuns()).toHaveLength(1);
    snippetRuns()[0].cancel();
    expect(await run).toBe(false);
    expect(written()).toEqual(["a\r"]);
    expect(mocks.toast).toHaveBeenLastCalledWith("info", expect.stringContaining("login"));
    expect(snippetRuns()).toEqual([]);
  });

  it("stops at once when the terminal closes during a wait", async () => {
    const run = executeSnippet(waiting([{ command: "a" }, { command: "b", waitFor: "never", timeoutMs: 600000 }]));
    await settle();
    useUi.setState({ workspaces: [] });
    expect(await run).toBe(false);
    expect(written()).toEqual(["a\r"]);
    expect(mocks.toast).toHaveBeenLastCalledWith("info", expect.stringContaining("1"));
  });

  it("waits before asking to confirm a step", async () => {
    const run = executeSnippet(waiting([{ command: "a" }, { command: "b", waitFor: "ready", confirm: true }]));
    await settle();
    expect(mocks.ask).not.toHaveBeenCalled();
    emit("ready");
    expect(await run).toBe(true);
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(written()).toEqual(["a\r", "b\r"]);
  });

  it("refuses an invalid regex before sending anything", async () => {
    const result = await executeSnippet(waiting([{ command: "a" }, { name: "bad", command: "b", waitFor: "(", waitForRegex: true }]));
    expect(result).toBe(false);
    expect(mocks.write).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("bad"));
  });

  it("does not observe output for snippets without waits", async () => {
    expect(await executeSnippet(waiting([{ command: "a" }, { command: "b" }]))).toBe(true);
    expect(written()).toEqual(["a\r", "b\r"]);
    expect(snippetRuns()).toEqual([]);
  });
});
