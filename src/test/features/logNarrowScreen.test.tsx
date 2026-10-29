/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { useUi } from "../../app/store";
import type { SessionInfo } from "../../ipc/commands";
import {
  click,
  flushUntil,
  setInputValue,
  clickButton,
  mountWithClient,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  start: vi.fn(),
  cancel: vi.fn(),
  save: vi.fn(),
  createId: vi.fn(),
  assetList: vi.fn(),
  dispose: vi.fn(),
  saveBlob: vi.fn(),
  onEvent: null as null | ((event: Record<string, unknown>) => void),
}));

vi.mock("../../ipc/commands", async () => {
  const { createFakeApi } = await import("../helpers/createFakeApi");
  return createFakeApi({
    call: vi.fn(),
    assetApi: { list: mocks.assetList },
    dbApi: {},
    vaultApi: {},
    sessionApi: { connect: vi.fn(), reconnect: vi.fn(), disconnect: vi.fn(), list: vi.fn().mockResolvedValue([]) },
    terminalApi: {
      listLive: vi.fn().mockResolvedValue([]),
      listDurable: vi.fn().mockResolvedValue([]),
      write: vi.fn().mockResolvedValue(undefined),
      detach: vi.fn().mockResolvedValue(undefined),
      setVisible: vi.fn().mockResolvedValue(undefined),
      closeTab: vi.fn().mockResolvedValue(undefined),
    },
  });
});

vi.mock("../../ipc/logTailApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/logTailApi")>();
  return {
    ...actual,
    logTailApi: { start: mocks.start, cancel: mocks.cancel, save: mocks.save },
    createLogTailRunId: mocks.createId,
  };
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    createJsonChannel: vi.fn((onEvent: (event: Record<string, unknown>) => void) => {
      mocks.onEvent = onEvent;
      return { id: "channel-log-tail", onmessage: vi.fn(), toJSON: () => "channel-log-tail" };
    }),
    disposeChannel: mocks.dispose,
  };
});

vi.mock("../../ipc/webFiles", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/webFiles")>();
  return { ...actual, saveBrowserBlob: mocks.saveBlob };
});

import { MultiHostLogTail } from "../../features/logs/MultiHostLogTail";
import { ServiceJournal } from "../../features/services/ServiceJournal";

const sessions: SessionInfo[] = [
  { id: "s1", assetId: null, name: "host-one", kind: "ssh", status: "connected", tabs: [], createdAt: 0 },
];

let mounted: MountedView | undefined;

function setNarrow(narrow: boolean): void {
  window.matchMedia = ((query: string) => ({
    matches: narrow && query.includes("max-width: 560px"),
    media: query,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    onchange: null,
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}

function logArea(container: ParentNode): HTMLElement {
  const log = container.querySelector<HTMLElement>('[role="log"]');
  if (!log) throw new Error("log area missing");
  return log;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ workspaces: [] });
  mocks.createId.mockReturnValue("run-test");
  mocks.start.mockResolvedValue({ runId: "run-test" });
  mocks.cancel.mockResolvedValue({ runId: "run-test", canceled: true });
  mocks.assetList.mockResolvedValue([]);
  mocks.onEvent = null;
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("log views on narrow screens", () => {
  it("turns the popovers into full-width sheets above the keyboard", () => {
    mounted = mountWithClient(createElement(MultiHostLogTail, { sessions }));
    const dialog = mounted.container.querySelector<HTMLElement>('[role="dialog"]');
    expect(dialog).not.toBeNull();
    const cls = dialog!.className;
    expect(cls).toContain("max-[560px]:fixed");
    expect(cls).toContain("max-[560px]:inset-x-0");
    expect(cls).toContain("max-[560px]:bottom-[var(--nx-kb-inset,0px)]");
    expect(cls).toContain("var(--nx-safe-bottom");
    expect(cls).toContain("max-[560px]:w-auto");
  });

  it("puts the include filter on its own full-width row", () => {
    mounted = mountWithClient(createElement(MultiHostLogTail, { sessions }));
    const input = mounted.container.querySelector<HTMLInputElement>('input[aria-label="包含正则"]')!;
    expect(input.className).toContain("max-[560px]:basis-full");
    expect(input.className).toContain("max-[560px]:order-last");
  });

  it("wraps long lines by default on a phone and lets the user switch it off", async () => {
    setNarrow(true);
    mounted = mountWithClient(createElement(MultiHostLogTail, { sessions, initialSelected: ["s1"] }));
    const log = logArea(mounted.container);
    expect(log.dataset.wrap).toBe("true");
    const toggle = mounted.container.querySelector<HTMLButtonElement>('button[aria-label="更多选项"]')!;
    click(toggle);
    const box = Array.from(mounted.container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')).find(
      (input) => input.closest("label")?.textContent?.includes("自动换行"),
    )!;
    expect(box.checked).toBe(true);
    click(box);
    expect(logArea(mounted.container).dataset.wrap).toBe("false");
  });

  it("keeps wide screens on unwrapped lines", () => {
    setNarrow(false);
    mounted = mountWithClient(createElement(MultiHostLogTail, { sessions }));
    expect(logArea(mounted.container).dataset.wrap).toBe("false");
  });

  it("offers a jump-to-latest button once the user scrolls away, and hides it again on tap", async () => {
    setNarrow(false);
    mounted = mountWithClient(createElement(MultiHostLogTail, { sessions, initialSelected: ["s1"] }));
    click(mounted.container.querySelector<HTMLButtonElement>('button[aria-label="选择主机与来源"]')!);
    const sourceInput = mounted.container.querySelector<HTMLInputElement>('input[aria-label="来源 host-one"]')!;
    setInputValue(sourceInput, "/var/log/one.log");
    clickButton(mounted.container, "开始跟随");
    await flushUntil(() => mocks.start.mock.calls.length === 1);
    act(() => {
      mocks.onEvent?.({ runId: "run-test", timeMs: 1, sequence: 1, type: "line", sourceId: "s1", sessionId: "s1", text: "hello" });
    });
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("hello"));
    expect(mounted.container.textContent).not.toContain("跳到最新");
    const log = logArea(mounted.container);
    Object.defineProperty(log, "scrollHeight", { value: 1000, configurable: true });
    Object.defineProperty(log, "clientHeight", { value: 200, configurable: true });
    act(() => {
      log.scrollTop = 0;
      log.dispatchEvent(new Event("scroll", { bubbles: true }));
    });
    const jump = clickButton(mounted.container, "跳到最新");
    expect(jump).toBeTruthy();
    expect(log.scrollTop).toBe(1000);
    expect(mounted.container.textContent).not.toContain("跳到最新");
  });
});

describe("ServiceJournal on narrow screens", () => {
  const session = sessions[0];

  it("lets the filters share a row and wraps lines by default on a phone", () => {
    setNarrow(true);
    mounted = mountWithClient(createElement(ServiceJournal, { session, host: "host-one", unit: "nginx.service", onClose: vi.fn() }));
    const include = mounted.container.querySelector<HTMLInputElement>('input[aria-label="journal 包含正则"]')!;
    expect(include.className).toContain("flex-1");
    expect(include.className).not.toMatch(/(^|\s)w-52(\s|$)/);
    expect(logArea(mounted.container).dataset.wrap).toBe("true");
    const wrapButton = Array.from(mounted.container.querySelectorAll("button")).find((b) => b.textContent === "自动换行")!;
    click(wrapButton);
    expect(logArea(mounted.container).dataset.wrap).toBe("false");
  });
});
