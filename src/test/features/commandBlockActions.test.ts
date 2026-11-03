/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  write: vi.fn(),
  snippetCreate: vi.fn(),
  ask: vi.fn(),
  promptText: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, promptText: mocks.promptText };
});
vi.mock("../../ipc/commands", () => ({
  terminalApi: { write: mocks.write },
  assetApi: { snippetCreate: mocks.snippetCreate },
}));

import {
  explainCommandBlock,
  rerunCommandBlock,
  saveCommandBlockAsSnippet,
} from "../../features/terminal/commandBlockActions";
import type { CommandBlock } from "../../features/terminal/commandBlocks";
import { useUi } from "../../app/store";

function block(command: string, index = 4): CommandBlock {
  return { index, command, reliable: true, startedAt: 1, endedAt: 2, startLine: 0, endLine: 1, exitCode: 0 };
}

beforeEach(() => {
  vi.resetAllMocks();
  mocks.write.mockResolvedValue(undefined);
  mocks.snippetCreate.mockResolvedValue({ id: "n1" });
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ pushToast: mocks.toast, aiPendingRefs: [], rightOpen: false });
});

describe("rerunCommandBlock", () => {
  it("writes the command plus enter to the given terminal", async () => {
    expect(await rerunCommandBlock("tab-9", block("ls -l"))).toBe(true);
    expect(mocks.write).toHaveBeenCalledTimes(1);
    expect(mocks.write.mock.calls[0][0]).toBe("tab-9");
    expect(new TextDecoder().decode(mocks.write.mock.calls[0][1])).toBe("ls -l\r");
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("asks first when the command has a newline and writes nothing when refused", async () => {
    mocks.ask.mockResolvedValue(false);
    expect(await rerunCommandBlock("tab-9", block("a\nb"))).toBe(false);
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.write).not.toHaveBeenCalled();
  });

  it("writes after the user confirms a multi-line command", async () => {
    expect(await rerunCommandBlock("tab-9", block("a\nb"))).toBe(true);
    expect(mocks.write).toHaveBeenCalledTimes(1);
  });

  it("reports a write failure", async () => {
    mocks.write.mockRejectedValue(new Error("gone"));
    expect(await rerunCommandBlock("tab-9", block("ls"))).toBe(false);
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("gone"));
  });
});

describe("saveCommandBlockAsSnippet", () => {
  it("defaults the name to the first 40 characters and creates the snippet", async () => {
    const command = "x".repeat(60);
    mocks.promptText.mockImplementation(async (_m: string, value: string) => value);
    const queryClient = { invalidateQueries: vi.fn() };
    expect(await saveCommandBlockAsSnippet(block(command), queryClient as never)).toBe(true);
    expect(mocks.snippetCreate).toHaveBeenCalledWith("x".repeat(40), command);
    expect(queryClient.invalidateQueries).toHaveBeenCalledWith({ queryKey: ["snippets"] });
  });

  it("creates nothing when the name prompt is cancelled", async () => {
    mocks.promptText.mockResolvedValue(null);
    expect(await saveCommandBlockAsSnippet(block("ls"))).toBe(false);
    expect(mocks.snippetCreate).not.toHaveBeenCalled();
  });

  it("uses the name the user typed", async () => {
    mocks.promptText.mockResolvedValue("  list files ");
    await saveCommandBlockAsSnippet(block("ls"));
    expect(mocks.snippetCreate).toHaveBeenCalledWith("list files", "ls");
  });
});

describe("explainCommandBlock", () => {
  it("queues a text reference with the last 16000 characters and opens the AI sidebar", () => {
    const text = "a".repeat(100) + "b".repeat(16000);
    expect(explainCommandBlock(block("journalctl -u app --no-pager --since yesterday please"), text, "web-01")).toBe(true);
    const refs = useUi.getState().aiPendingRefs;
    expect(refs).toHaveLength(1);
    expect(refs[0].kind).toBe("text");
    expect(refs[0].text).toBe("b".repeat(16000));
    expect(refs[0].refId).toBe(refs[0].id);
    expect(refs[0].detail).toBe("web-01");
    expect(refs[0].label).toContain("#4");
    expect(useUi.getState().rightOpen).toBe(true);
  });

  it("does nothing for output that already scrolled out", () => {
    expect(explainCommandBlock(block("ls"), "", "web-01")).toBe(false);
    expect(useUi.getState().aiPendingRefs).toEqual([]);
    expect(useUi.getState().rightOpen).toBe(false);
  });

  it("clears queued references on account reset", () => {
    explainCommandBlock(block("ls"), "out", "web-01");
    useUi.getState().resetAiAccount();
    expect(useUi.getState().aiPendingRefs).toEqual([]);
  });
});
