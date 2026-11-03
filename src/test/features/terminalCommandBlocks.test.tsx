/** @vitest-environment jsdom */

import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  write: vi.fn(),
  snippetCreate: vi.fn(),
  promptText: vi.fn(),
  ask: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, promptText: mocks.promptText };
});
vi.mock("../../ipc/commands", () => ({
  terminalApi: { write: mocks.write },
  assetApi: { snippetCreate: mocks.snippetCreate },
}));

import { TerminalCommandBlocks } from "../../features/terminal/TerminalCommandBlocks";
import type { CommandBlock } from "../../features/terminal/commandBlocks";
import { useUi } from "../../app/store";
import { flush, mountWithClient, type MountedView } from "./reactTestUtils";

let mounted: MountedView | undefined;

const done: CommandBlock = {
  index: 1, command: "ls -l", reliable: true, startedAt: 1000, endedAt: 2000, startLine: 0, endLine: 1, exitCode: 0,
};

function view(kernelTabId: string | null, embedded?: boolean) {
  mounted = mountWithClient(
    <TerminalCommandBlocks
      blocks={[done]}
      getText={() => "file-a\nfile-b"}
      onLocate={() => undefined}
      onClear={() => undefined}
      onToast={() => undefined}
      kernelTabId={kernelTabId}
      sourceLabel="prod-1"
      embedded={embedded}
    />,
  );
  return mounted.container;
}

const button = (c: HTMLElement, label: string) => c.querySelector<HTMLButtonElement>(`button[aria-label='${label}']`);

beforeEach(() => {
  vi.resetAllMocks();
  mocks.write.mockResolvedValue(undefined);
  mocks.snippetCreate.mockResolvedValue({ id: "n1" });
  mocks.ask.mockResolvedValue(true);
  mocks.promptText.mockResolvedValue("my snippet");
  useUi.setState({ aiPendingRefs: [], rightOpen: false });
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    const height = this.hasAttribute("data-command-block") ? 54 : this.classList.contains("overflow-y-auto") ? 600 : 0;
    return { x: 0, y: 0, top: 0, left: 0, right: 252, bottom: height, width: 252, height, toJSON: () => ({}) } as DOMRect;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(252);
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    return this.classList.contains("overflow-y-auto") ? 600 : 54;
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.restoreAllMocks();
});

describe("TerminalCommandBlocks", () => {
  it("reruns the command in the given terminal", async () => {
    const c = view("tab-7");
    await act(async () => button(c, "重新运行")!.click());
    await flush();
    expect(mocks.write).toHaveBeenCalledTimes(1);
    expect(mocks.write.mock.calls[0][0]).toBe("tab-7");
  });

  it("has no rerun button without a kernel tab", () => {
    const c = view(null);
    expect(button(c, "重新运行")).toBeNull();
    expect(button(c, "保存为片段")).not.toBeNull();
  });

  it("queues the output as an AI text reference labelled with the source", async () => {
    const c = view("tab-7");
    await act(async () => button(c, "让 AI 解释这段输出")!.click());
    const refs = useUi.getState().aiPendingRefs;
    expect(refs).toHaveLength(1);
    expect(refs[0].kind).toBe("text");
    expect(refs[0].detail).toBe("prod-1");
  });

  it("saves the command as a snippet", async () => {
    const c = view("tab-7");
    await act(async () => button(c, "保存为片段")!.click());
    await flush();
    expect(mocks.snippetCreate).toHaveBeenCalledWith("my snippet", "ls -l");
  });

  it("drops the fixed side-panel width when embedded", () => {
    const fixed = view("tab-7");
    expect(fixed.firstElementChild?.className).toContain("w-[252px]");
    mounted?.unmount();
    const embedded = view("tab-7", true);
    expect(embedded.firstElementChild?.className).not.toContain("w-[252px]");
  });
});
