/** @vitest-environment jsdom */

import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  write: vi.fn(),
  generate: vi.fn(),
  overview: vi.fn(),
  ask: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    terminalApi: { ...actual.terminalApi, write: mocks.write },
    modelApi: { ...actual.modelApi, overview: mocks.overview },
  };
});
vi.mock("../../ipc/aiCommandApi", () => ({ aiCommandApi: { generate: mocks.generate } }));
vi.mock("../../ui/dialogs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ui/dialogs")>()),
  ask: mocks.ask,
}));

import { FailedCommandChip, failedCommandBlock } from "../../features/terminal/FailedCommandChip";
import { StickyCommandHeader } from "../../features/terminal/StickyCommandHeader";
import { AiCommandPrompt } from "../../features/terminal/AiCommandPrompt";
import { insertCommandAtPrompt, type CommandInsertTarget } from "../../features/terminal/insertCommand";
import { Markdown } from "../../features/ai/Markdown";
import type { CommandBlock } from "../../features/terminal/commandBlocks";
import { useUi } from "../../app/store";
import { click, flush, mount, setInputValue, waitForRendered as waitFor, type MountedView } from "./reactTestUtils";

let mounted: MountedView | undefined;

const block = (patch: Partial<CommandBlock> = {}): CommandBlock => ({
  index: 3, command: "make test", reliable: true, startedAt: 1000, endedAt: 3500, startLine: 4, endLine: 9, exitCode: 2, ...patch,
});

const target: CommandInsertTarget = { tabId: "k1", hostLabel: "web-1", productionEnvironment: null };
const written = () => mocks.write.mock.calls.map(([tab, bytes]) => [tab, new TextDecoder().decode(bytes as Uint8Array)]);

function render(node: Parameters<typeof mount>[0]) {
  mounted = mount(node);
  return mounted.container;
}

function pressEnter(input: HTMLInputElement) {
  act(() => {
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  });
}

beforeEach(() => {
  vi.resetAllMocks();
  mocks.write.mockResolvedValue(undefined);
  mocks.overview.mockResolvedValue({ profiles: [{ id: "p1" }] });
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ toasts: [], aiPendingRefs: [], rightOpen: false, workspaces: [], activeWorkspaceId: null, sessions: [] });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("failed command chip", () => {
  it("shows for the latest block with a non-zero exit until it is dismissed or a new command starts", () => {
    expect(failedCommandBlock([block()], null)?.index).toBe(3);
    expect(failedCommandBlock([block()], 3)).toBeNull();
    expect(failedCommandBlock([block({ exitCode: 0 })], null)).toBeNull();
    expect(failedCommandBlock([block({ exitCode: null })], null)).toBeNull();
    // The next command is running: the chip of the failed one is gone.
    expect(failedCommandBlock([block(), block({ index: 4, endedAt: null, exitCode: null })], null)).toBeNull();
  });

  it("offers the AI explanation and a close button", () => {
    const onExplain = vi.fn();
    const onDismiss = vi.fn();
    const container = render(<FailedCommandChip block={block()} onExplain={onExplain} onDismiss={onDismiss} />);
    const chip = container.querySelector("[data-testid='failed-command-chip']")!;
    expect(chip.textContent).toContain("退出码 2");
    expect(chip.textContent).toContain("让 AI 解释");
    click(chip.querySelector("button")!);
    expect(onExplain).toHaveBeenCalledWith(expect.objectContaining({ index: 3 }));
    click(chip.querySelector("button[aria-label='关闭提示']")!);
    expect(onDismiss).toHaveBeenCalledWith(expect.objectContaining({ index: 3 }));
  });
});

describe("sticky command header", () => {
  it("shows the command, duration and exit status and scrolls to the command on click", () => {
    const onLocate = vi.fn();
    const container = render(<StickyCommandHeader block={block()} onLocate={onLocate} />);
    const header = container.querySelector<HTMLButtonElement>("[data-testid='sticky-command-header']")!;
    expect(header.dataset.status).toBe("failure");
    expect(header.textContent).toContain("make test");
    expect(header.textContent).toContain("2.5s");
    expect(header.textContent).toContain("exit 2");
    click(header);
    expect(onLocate).toHaveBeenCalledWith(3);
  });
});

describe("insertCommandAtPrompt", () => {
  it("types the command without a newline", async () => {
    await expect(insertCommandAtPrompt(target, "ls -la\n")).resolves.toBe(true);
    expect(written()).toEqual([["k1", "ls -la"]]);
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("refuses multi-line or control text and offers to copy it", async () => {
    await expect(insertCommandAtPrompt(target, "cd /tmp\nrm -rf x")).resolves.toBe(false);
    await expect(insertCommandAtPrompt(target, "echo \u001b[2J")).resolves.toBe(false);
    expect(mocks.write).not.toHaveBeenCalled();
    const toast = useUi.getState().toasts.at(-1);
    expect(toast?.text).toContain("未填入终端");
    expect(toast?.action?.label).toBe("复制命令");
  });

  it("asks before typing into a production host and stops when declined", async () => {
    const production = { ...target, productionEnvironment: "prod" };
    mocks.ask.mockResolvedValueOnce(false);
    await expect(insertCommandAtPrompt(production, "systemctl restart nginx")).resolves.toBe(false);
    expect(mocks.write).not.toHaveBeenCalled();
    expect(mocks.ask.mock.calls[0][0]).toContain("prod");
    await expect(insertCommandAtPrompt(production, "systemctl restart nginx")).resolves.toBe(true);
    expect(written()).toEqual([["k1", "systemctl restart nginx"]]);
  });
});

describe("AI command prompt", () => {
  it("generates a command from the description and context, then inserts it on confirm", async () => {
    mocks.generate.mockResolvedValue({ command: "du -sh * | sort -h" });
    const onInsert = vi.fn(async () => true);
    const onClose = vi.fn();
    const container = render(
      <AiCommandPrompt context={{ cwd: "/var/log", host: "web-1", shell: "posix" }} target={target} onInsert={onInsert} onClose={onClose} />,
    );
    await flush();
    const input = container.querySelector<HTMLInputElement>("input")!;
    setInputValue(input, "哪些目录最大");
    pressEnter(input);
    await waitFor(() => expect(container.querySelector("[data-testid='ai-command-result']")?.textContent).toBe("du -sh * | sort -h"));
    expect(mocks.generate).toHaveBeenCalledWith({ cwd: "/var/log", host: "web-1", shell: "posix", prompt: "哪些目录最大" });
    pressEnter(input);
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(onInsert).toHaveBeenCalledWith("du -sh * | sort -h");
  });

  it("says when no model is configured", async () => {
    mocks.overview.mockResolvedValue({ profiles: [] });
    const container = render(<AiCommandPrompt context={{}} target={target} onInsert={vi.fn()} onClose={vi.fn()} />);
    await waitFor(() => expect(container.textContent).toContain("尚未配置模型"));
    expect(container.querySelector<HTMLInputElement>("input")!.disabled).toBe(true);
  });

  it("explains an unavailable AI runtime and other failures", async () => {
    mocks.generate.mockRejectedValueOnce({ code: "unsupported", message: "AI 模型不可用" });
    const container = render(<AiCommandPrompt context={{}} target={target} onInsert={vi.fn()} onClose={vi.fn()} />);
    await flush();
    const input = container.querySelector<HTMLInputElement>("input")!;
    setInputValue(input, "list files");
    pressEnter(input);
    await waitFor(() => expect(container.querySelector("[role='alert']")?.textContent).toContain("AI 不可用"));
  });
});

describe("markdown code blocks", () => {
  const text = "```bash\nuptime\n```";

  it("offer insertion only while a terminal is active", async () => {
    const container = render(<Markdown text={text} />);
    expect(container.querySelector("button[aria-label='插入到终端']")).toBeNull();
    act(() => {
      useUi.setState({
        sessions: [{ id: "s1", assetId: null, name: "web-1", kind: "ssh", status: "connected", tabs: ["k1"], createdAt: 1 }] as never,
        workspaces: [{ id: "w1", activePaneId: "p1", panes: [{ id: "p1", activeTabId: "t1", tabs: [{ id: "t1", kind: "terminal", title: "web-1", sessionId: "s1", tabId: "k1", closable: true }] }] }] as never,
        activeWorkspaceId: "w1",
      });
    });
    const insert = container.querySelector<HTMLButtonElement>("button[aria-label='插入到终端']");
    expect(insert).not.toBeNull();
    click(insert!);
    await waitFor(() => expect(written()).toEqual([["k1", "uptime"]]));
  });
});
