/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, flush, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});

import { AgentRosterPanel } from "../features/ai/AgentRosterPanel";
import { useUi } from "../app/store";
import type { AgentRosterApi } from "../features/ai/agentApi";
import type { AgentRosterEntry } from "../features/ai/agentTypes";

function entry(partial: Partial<AgentRosterEntry> = {}): AgentRosterEntry {
  return {
    handle: "build",
    host: "web-01",
    cwd: "/srv/app",
    ownerId: "u-1",
    ownerName: "alice",
    isMine: true,
    production: false,
    persistent: true,
    incarnation: 3,
    presence: "attached",
    evidence: { kind: "attach", observedAt: 1_760_000_000_000, summary: "附加成功" },
    releasePending: false,
    tabId: "tab-1",
    lastRun: null,
    ...partial,
  };
}

function makeApi(rows: AgentRosterEntry[]) {
  return {
    list: vi.fn(async () => rows),
    open: vi.fn(async () => ({ tabId: "tab-opened" })),
    attach: vi.fn(async () => ({ tabId: "tab-attached" })),
    rename: vi.fn(async (_handle: string, next: string) => ({ ...entry(), handle: next })),
    release: vi.fn(async () => {}),
    resolveApproval: vi.fn(async () => {}),
  } satisfies AgentRosterApi;
}

let view: MountedView | null = null;

function rowFor(handle: string): HTMLElement {
  const row = view!.container.querySelector(`[data-handle="${handle}"]`);
  if (!row) throw new Error(`row not found: ${handle}`);
  return row as HTMLElement;
}

function buttonIn(scope: ParentNode, text: string): HTMLButtonElement {
  const b = [...scope.querySelectorAll("button")].find(
    (candidate) => candidate.textContent?.trim() === text,
  );
  if (!b) throw new Error(`button not found: ${text}`);
  return b as HTMLButtonElement;
}

async function mountPanel(
  rows: AgentRosterEntry[],
  extra: { onMention?: (token: string, e: AgentRosterEntry) => void; onOpened?: (tabId: string, e: AgentRosterEntry) => void } = {},
) {
  const api = makeApi(rows);
  view = mount(
    createElement(AgentRosterPanel, { api, onMention: extra.onMention, onOpened: extra.onOpened }),
  );
  await flush();
  return api;
}

describe("AgentRosterPanel", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
    useUi.setState({ pushToast: mocks.toast });
  });

  afterEach(() => {
    view?.unmount();
    view = null;
  });

  it("renders roster rows as handle@host:cwd with evidence-based presence", async () => {
    await mountPanel([entry()]);
    const row = rowFor("build");
    expect(row.textContent).toContain("build@web-01:/srv/app");
    expect(row.textContent).toContain("已附加");
  });

  it("renders an idle last run as 空闲 and never as 已完成", async () => {
    await mountPanel([entry({ lastRun: { state: "idle", evidence: null } })]);
    const row = rowFor("build");
    expect(row.textContent).toContain("空闲");
    expect(row.textContent).not.toContain("已完成");
  });

  it("renders done without evidence as unknown, and done with evidence as 已完成", async () => {
    await mountPanel([
      entry({ handle: "no-evidence", lastRun: { state: "done", evidence: null } }),
      entry({
        handle: "with-evidence",
        lastRun: {
          state: "done",
          evidence: { kind: "output", observedAt: 1_760_000_000_000, summary: "输出锚点匹配" },
        },
      }),
    ]);
    expect(rowFor("no-evidence").textContent).toContain("未知");
    expect(rowFor("no-evidence").textContent).not.toContain("已完成");
    const withEvidence = rowFor("with-evidence");
    expect(withEvidence.textContent).toContain("已完成");
    expect(withEvidence.textContent).not.toContain("未知");
  });

  it("renders gone without evidence as unknown instead of claiming absence", async () => {
    await mountPanel([entry({ presence: "gone", evidence: null })]);
    const row = rowFor("build");
    expect(row.textContent).toContain("未知");
    expect(row.textContent).not.toContain("已消失");
  });

  it("open and attach call the api and report the opened tab", async () => {
    const onOpened = vi.fn();
    const api = await mountPanel([entry()], { onOpened });
    clickButton(rowFor("build"), "打开");
    await flush();
    expect(api.open).toHaveBeenCalledWith("build");
    expect(onOpened).toHaveBeenCalledWith("tab-opened", expect.objectContaining({ handle: "build" }));

    clickButton(rowFor("build"), "附加");
    await flush();
    expect(api.attach).toHaveBeenCalledWith("build");
    expect(onOpened).toHaveBeenCalledWith("tab-attached", expect.objectContaining({ handle: "build" }));
  });

  it("gone rows disable open and attach with a visible reason", async () => {
    const api = await mountPanel([
      entry({ presence: "gone", evidence: { kind: "reconcile", observedAt: 1, summary: "对账确认缺席" } }),
    ]);
    const row = rowFor("build");
    expect(buttonIn(row, "打开").disabled).toBe(true);
    expect(buttonIn(row, "附加").disabled).toBe(true);
    expect(row.textContent).toContain("已消失");
    expect(api.open).not.toHaveBeenCalled();
  });

  it("rename commits a valid new handle through the api", async () => {
    const api = await mountPanel([entry()]);
    const row = rowFor("build");
    clickButton(row, "重命名");
    const input = row.querySelector('input[aria-label="新句柄名称"]') as HTMLInputElement;
    setInputValue(input, "deploy-2");
    clickButton(row, "保存");
    await flush();
    expect(api.rename).toHaveBeenCalledWith("build", "deploy-2");
    expect(view!.container.querySelector('[data-handle="deploy-2"]')).not.toBeNull();
  });

  it("rename rejects mention-unsafe handles without calling the api", async () => {
    const api = await mountPanel([entry()]);
    const row = rowFor("build");
    clickButton(row, "重命名");
    const input = row.querySelector('input[aria-label="新句柄名称"]') as HTMLInputElement;
    setInputValue(input, "bad name@x");
    clickButton(row, "保存");
    await flush();
    expect(api.rename).not.toHaveBeenCalled();
    expect(row.textContent).toContain("句柄只能包含");
  });

  it("mention reports the @handle token for the composer", async () => {
    const onMention = vi.fn();
    await mountPanel([entry()], { onMention });
    clickButton(rowFor("build"), "提及");
    expect(onMention).toHaveBeenCalledWith("@build", expect.objectContaining({ handle: "build" }));
  });

  it("release confirms first, then marks the row release-pending instead of claiming it is gone", async () => {
    const api = await mountPanel([entry()]);
    const row = rowFor("build");
    clickButton(row, "释放");
    await flush();
    expect(mocks.ask).toHaveBeenCalledOnce();
    expect(api.release).toHaveBeenCalledWith("build");
    expect(rowFor("build").textContent).toContain("释放待确认");
    expect(rowFor("build").textContent).not.toContain("已消失");
    expect(buttonIn(rowFor("build"), "释放").disabled).toBe(true);
  });

  it("a declined release confirmation never calls the api", async () => {
    mocks.ask.mockResolvedValue(false);
    const api = await mountPanel([entry()]);
    clickButton(rowFor("build"), "释放");
    await flush();
    expect(api.release).not.toHaveBeenCalled();
    expect(rowFor("build").textContent).not.toContain("释放待确认");
  });

  it("production release confirmation names the production guard", async () => {
    mocks.ask.mockResolvedValue(false);
    await mountPanel([entry({ production: true })]);
    const row = rowFor("build");
    expect(row.textContent).toContain("生产环境");
    clickButton(row, "释放");
    await flush();
    expect(mocks.ask).toHaveBeenCalledOnce();
    const message = mocks.ask.mock.calls[0][0] as string;
    expect(message).toContain("生产环境");
  });

  it("other-owner rows keep rename and release disabled with a visible owner badge", async () => {
    const api = await mountPanel([entry({ isMine: false, ownerName: "bob" })]);
    const row = rowFor("build");
    expect(row.textContent).toContain("所有者：bob");
    expect(buttonIn(row, "重命名").disabled).toBe(true);
    expect(buttonIn(row, "释放").disabled).toBe(true);
    expect(buttonIn(row, "打开").disabled).toBe(false);
    expect(api.rename).not.toHaveBeenCalled();
  });

  it("unverifiable rows surface the automatic-write ban", async () => {
    await mountPanel([entry({ presence: "unverifiable", evidence: null })]);
    const row = rowFor("build");
    expect(row.textContent).toContain("无法验证");
    expect(row.textContent).toContain("自动写入已禁止");
  });

  it("load failure offers a retry that reloads the roster", async () => {
    const api = makeApi([]);
    api.list.mockRejectedValueOnce(new Error("roster offline"));
    view = mount(createElement(AgentRosterPanel, { api }));
    await flush();
    expect(view.container.textContent).toContain("加载失败");
    clickButton(view.container, "重试");
    await flush();
    expect(api.list).toHaveBeenCalledTimes(2);
    expect(view.container.textContent).toContain("暂无句柄");
  });
});
