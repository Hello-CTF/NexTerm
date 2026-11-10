/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Terminal } from "@xterm/xterm";

const pushToast = vi.fn();
const copyText = vi.fn(async (_text: string) => undefined);
const tabs = new Map<string, { shell?: string; dead?: boolean; exited?: boolean }>();

vi.mock("../../app/store", () => ({
  useUi: { getState: () => ({ pushToast, sessions: [{ id: "s1", kind: "ssh" }] }) },
  findTab: (id: string) => (tabs.has(id) ? { tab: tabs.get(id), ws: {} } : null),
  openFileTab: vi.fn(),
}));
vi.mock("../../ui/clipboard", () => ({ copyText: (text: string) => copyText(text) }));

import { attachTerminalTargets, startTerminalQuickSelect, type TerminalTargets } from "../../features/terminal/terminalTargets";
import { requestFileReveal, revealOffset, takeFileReveal } from "../../features/files/fileReveal";

describe("quick select overlay", () => {
  let host: HTMLElement;
  let term: Terminal;
  let targets: TerminalTargets;
  const insert = vi.fn();
  const write = (data: string) => new Promise<void>((resolve) => term.write(data, resolve));
  const key = (init: KeyboardEventInit) => window.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init }));

  beforeEach(() => {
    host = document.createElement("div");
    document.body.append(host);
    term = new Terminal({ allowProposedApi: true, cols: 80, rows: 6 });
    term.open(host);
    tabs.set("tab-1", { shell: "posix" });
    targets = attachTerminalTargets(term, host, { sessionId: "s1", viewKey: () => "tab-1", insert });
  });

  afterEach(() => {
    targets.dispose();
    term.dispose();
    host.remove();
    vi.clearAllMocks();
    tabs.clear();
  });

  const labels = () => [...host.querySelectorAll<HTMLButtonElement>(".nx-quick-select-label")];

  it("labels what is on screen and copies the chosen match", async () => {
    await write("commit 3e13226f\r\nsee https://example.com/x\r\n");
    expect(startTerminalQuickSelect("tab-1")).toBe(true);
    expect(labels().map((l) => [l.dataset.label, l.title])).toEqual([
      ["s", "3e13226f"],
      ["a", "https://example.com/x"],
    ]);
    key({ key: "s" });
    await Promise.resolve();
    expect(copyText).toHaveBeenCalledWith("3e13226f");
    expect(host.querySelector(".nx-quick-select")).toBeNull();
    expect(insert).not.toHaveBeenCalled();
  });

  it("inserts with Shift, without Enter", async () => {
    await write("pull 192.168.0.7\r\n");
    targets.startQuickSelect();
    key({ key: "A", shiftKey: true });
    expect(insert).toHaveBeenCalledWith("192.168.0.7");
    expect(copyText).not.toHaveBeenCalled();
  });

  it("cancels on Escape and keeps the key from the terminal", async () => {
    await write("id 3e13226f\r\n");
    targets.startQuickSelect();
    const onData = vi.fn();
    term.onData(onData);
    key({ key: "Escape" });
    expect(host.querySelector(".nx-quick-select")).toBeNull();
    expect(onData).not.toHaveBeenCalled();
  });

  it("taps a label to copy, or to insert in insert mode", async () => {
    await write("id 3e13226f\r\n");
    targets.startQuickSelect();
    host.querySelector<HTMLButtonElement>(".nx-quick-select-mode")!.click();
    labels()[0].click();
    expect(insert).toHaveBeenCalledWith("3e13226f");
  });

  it("says so when nothing on screen can be selected", async () => {
    await write("hello world\r\n");
    targets.startQuickSelect();
    expect(host.querySelector(".nx-quick-select")).toBeNull();
    expect(pushToast).toHaveBeenCalledWith("info", expect.any(String));
  });

  it("does not insert into an ended terminal", async () => {
    tabs.set("tab-1", { exited: true });
    await write("id 3e13226f\r\n");
    targets.startQuickSelect();
    key({ key: "A", shiftKey: true });
    expect(insert).not.toHaveBeenCalled();
    expect(pushToast).toHaveBeenCalledWith("info", expect.any(String));
  });

  it("offers relative paths once the shell reports its directory", async () => {
    await write("edit src/a.ts:3\r\n");
    targets.startQuickSelect();
    expect(labels()).toHaveLength(0);
    await write("\x1b]7;file:///home/u/proj\x07edit src/a.ts:3\r\n");
    targets.startQuickSelect();
    // Both printed lines are paths now; repeated text shares one label.
    expect(labels().map((l) => [l.dataset.label, l.title])).toEqual([
      ["a", "src/a.ts:3"],
      ["a", "src/a.ts:3"],
    ]);
  });
});

describe("file reveal requests", () => {
  it("are taken once and clamp to the document", () => {
    requestFileReveal("s1", "/a.ts", { line: 99, col: 50 });
    expect(takeFileReveal("s1", "/b.ts")).toBeNull();
    const position = takeFileReveal("s1", "/a.ts");
    expect(position).toEqual({ line: 99, col: 50 });
    expect(takeFileReveal("s1", "/a.ts")).toBeNull();
    const lines = ["abc", "defgh"];
    const doc = {
      lines: lines.length,
      line: (n: number) => ({ from: lines.slice(0, n - 1).reduce((sum, l) => sum + l.length + 1, 0), length: lines[n - 1].length }),
    };
    expect(revealOffset(doc, position!)).toBe(4 + 5);
    expect(revealOffset(doc, { line: 1, col: 2 })).toBe(1);
  });
});
