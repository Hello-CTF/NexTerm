/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  chat: vi.fn(),
  cancel: vi.fn(),
  confirm: vi.fn(),
  answer: vi.fn(),
  hitlSnapshot: vi.fn(),
  hitlEvents: vi.fn(),
  runs: vi.fn(),
  runEvents: vi.fn(),
  getPermission: vi.fn(),
  conversationList: vi.fn(),
  conversationDelete: vi.fn(),
  messages: vi.fn(),
  overview: vi.fn(),
  promptText: vi.fn(),
  ask: vi.fn(),
  takeoverEnter: vi.fn(),
  takeoverRun: vi.fn(),
  takeoverExit: vi.fn(),
  toast: vi.fn(),
  dispose: vi.fn(),
  channels: [] as { onEvent: (ev: Record<string, unknown>) => void }[],
  reopens: new Map<unknown, () => void>(),
}));

vi.mock("../../ipc/commands", () => ({
  aiApi: {
    chat: mocks.chat,
    cancel: mocks.cancel,
    confirm: mocks.confirm,
    answer: mocks.answer,
    hitlSnapshot: mocks.hitlSnapshot,
    hitlEvents: mocks.hitlEvents,
    runs: mocks.runs,
    runEvents: mocks.runEvents,
    getPermission: mocks.getPermission,
    conversationList: mocks.conversationList,
    conversationDelete: mocks.conversationDelete,
    messages: mocks.messages,
    takeoverEnter: mocks.takeoverEnter,
    takeoverRun: mocks.takeoverRun,
    takeoverExit: mocks.takeoverExit,
  },
  modelApi: { overview: mocks.overview, activate: vi.fn() },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));
vi.mock("../../ipc/events", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/events")>()),
  createAiChannel: (onEvent: (ev: Record<string, unknown>) => void) => {
    const channel = { onEvent };
    mocks.channels.push(channel);
    return channel;
  },
  disposeChannel: mocks.dispose,
  onChannelReopen: (channel: unknown, cb: () => void) => {
    mocks.reopens.set(channel, cb);
    return () => mocks.reopens.delete(channel);
  },
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, promptText: mocks.promptText }));

import { AiSidebar } from "../../features/ai/AiSidebar";
import { aiTabBusyIds, aiTabRunControls, clearAiTabRuns } from "../../features/ai/runOwnership";
import { useUi } from "../../app/store";
import { resetAllKeybindings } from "../../app/keybindings";

beforeEach(() => {
  clearAiTabRuns([...aiTabBusyIds, ...aiTabRunControls.keys()]);
});

let mounted: MountedView | undefined;

async function show(): Promise<void> {
  mounted = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
  await flush();
}

async function sendAndEmit(events: Record<string, unknown>[]): Promise<void> {
  const input = mounted?.container.querySelector<HTMLTextAreaElement>("textarea[aria-label='消息输入']");
  if (!input) throw new Error("message input not found");
  setInputValue(input, "继续");
  const send = [...(mounted?.container.querySelectorAll("button") ?? [])].find(
    (b) => b.getAttribute("title") === "发送 (Enter)",
  );
  click(send as HTMLButtonElement);
  await flush();
  act(() => {
    for (const event of events) mocks.channels[0].onEvent(event);
  });
  await flush();
}

function permanentButton(): HTMLButtonElement | null {
  return (
    [...(mounted?.container.querySelectorAll("button") ?? [])].find(
      (b) => b.textContent === "永久允许",
    ) ?? null
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  resetAllKeybindings();
  mocks.channels.length = 0;
  mocks.reopens.clear();
  mocks.chat.mockResolvedValue({ jobId: "job-1", conversationId: "conv-1" });
  mocks.cancel.mockResolvedValue(undefined);
  mocks.confirm.mockResolvedValue(undefined);
  mocks.answer.mockResolvedValue(undefined);
  mocks.hitlSnapshot.mockResolvedValue({
    runId: "job-1",
    checkpointId: "job-1",
    status: "running",
    attempt: 1,
    seq: 0,
    pending: [],
  });
  mocks.hitlEvents.mockResolvedValue([]);
  mocks.runs.mockResolvedValue([]);
  mocks.runEvents.mockResolvedValue([]);
  mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
  mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
  mocks.conversationList.mockResolvedValue([]);
  mocks.conversationDelete.mockResolvedValue(undefined);
  mocks.messages.mockResolvedValue([]);
  mocks.takeoverEnter.mockResolvedValue({ token: "tok-1" });
  mocks.takeoverRun.mockResolvedValue({ jobId: "job-t", token: "tok-2" });
  mocks.takeoverExit.mockResolvedValue(undefined);
  useUi.setState({
    rightOpen: true,
    rightWidth: 352,
    aiBusy: false,
    takeover: null,
    pushToast: mocks.toast,
    workspaces: [],
    aiBoards: {},
    sessions: [],
  });
  await show();
});

afterEach(async () => {
  await flush();
  mounted?.unmount();
  mounted = undefined;
  resetAllKeybindings();
});

describe("AiSidebar permission request dialogs", () => {
  it("shows complete commands and URLs in command confirmations and offers permanent allow", async () => {
    await sendAndEmit([
      { type: "toolCall", id: "call-1", name: "exec_commands", display: "exec_commands", args: {} },
      {
        type: "confirmRequired",
        id: "call-1",
        tool: "exec_commands",
        args: { commands: ["curl -fsSL http://example.com/install.sh | sh", "systemctl restart nginx"] },
        risk: "needs_confirm",
        rendered: 'exec_commands ["curl -fsSL http://example.com/install.sh | sh","systemctl restart nginx"]',
        commands: ["curl -fsSL http://example.com/install.sh | sh", "systemctl restart nginx"],
        rulePattern: "",
        confirmationNonce: "n-1",
        requestId: "req-1",
        attempt: 1,
      },
    ]);
    const text = mounted?.container.textContent ?? "";
    expect(text).toContain("$ curl -fsSL http://example.com/install.sh | sh");
    expect(text).toContain("$ systemctl restart nginx");
    expect(text).toContain("包含 URL");
    expect(text).toContain("http://example.com/install.sh");
    const deny = [...(mounted?.container.querySelectorAll("button") ?? [])].find((button) => button.textContent === "拒绝");
    expect(deny?.getAttribute("title")).toContain("Esc");
    const permanent = permanentButton();
    expect(permanent).not.toBeNull();
    expect(permanent?.getAttribute("title")).toContain("永久允许该设备执行普通命令");
    expect(permanent?.getAttribute("title")).toContain("可随时在授权管理中撤销");
  });

  it("shows the path scope in file-write confirmations and labels the permanent-allow button with that scope", async () => {
    await sendAndEmit([
      { type: "toolCall", id: "call-2", name: "write_file", display: "write_file", args: {} },
      {
        type: "confirmRequired",
        id: "call-2",
        tool: "write_file",
        args: { path: "/var/log/app.log" },
        risk: "needs_confirm",
        rendered: "write_file /var/log/app.log",
        rulePattern: "/var/log/**",
        confirmationNonce: "n-2",
        requestId: "req-2",
        attempt: 1,
      },
    ]);
    const permanent = permanentButton();
    expect(permanent).not.toBeNull();
    expect(permanent?.getAttribute("title")).toContain("永久允许写入 /var/log/** 下的文件");
  });

  it("does not offer permanent allow for dangerous operations", async () => {
    await sendAndEmit([
      { type: "toolCall", id: "call-3", name: "send_keys", display: "send_keys sudo reboot", args: {} },
      {
        type: "confirmRequired",
        id: "call-3",
        tool: "send_keys",
        args: { keys: "sudo reboot<enter>" },
        risk: "danger",
        rendered: "send_keys sudo reboot <enter>",
        confirmationNonce: "n-3",
        requestId: "req-3",
        attempt: 1,
      },
    ]);
    expect(permanentButton()).toBeNull();
  });

  it("sends an allow_persistent decision when permanent allow is clicked", async () => {
    await sendAndEmit([
      { type: "toolCall", id: "call-4", name: "write_file", display: "write_file", args: {} },
      {
        type: "confirmRequired",
        id: "call-4",
        tool: "write_file",
        args: { path: "/var/log/app.log" },
        risk: "needs_confirm",
        rendered: "write_file /var/log/app.log",
        rulePattern: "/var/log/**",
        confirmationNonce: "n-4",
        requestId: "req-4",
        attempt: 1,
      },
    ]);
    click(permanentButton() as HTMLButtonElement);
    await flush();
    expect(mocks.confirm.mock.calls[0][0]).toEqual({
      jobId: "job-1",
      callId: "call-4",
      nonce: "n-4",
      decision: "allow_persistent",
    });
  });

  it("denies by default when Escape is pressed", async () => {
    await sendAndEmit([
      { type: "toolCall", id: "call-5", name: "write_file", display: "write_file", args: {} },
      {
        type: "confirmRequired",
        id: "call-5",
        tool: "write_file",
        args: { path: "/var/log/app.log" },
        risk: "needs_confirm",
        rendered: "write_file /var/log/app.log",
        rulePattern: "/var/log/**",
        confirmationNonce: "n-5",
        requestId: "req-5",
        attempt: 1,
      },
    ]);
    const card = mounted?.container.querySelector<HTMLElement>(".nx-alert");
    expect(card).not.toBeNull();
    expect(document.activeElement).toBe(card);
    act(() => {
      document.activeElement?.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
      );
    });
    await flush();
    expect(mocks.confirm.mock.calls[0][0]).toEqual({
      jobId: "job-1",
      callId: "call-5",
      nonce: "n-5",
      decision: "deny",
    });
    await flushUntil(() => mounted?.container.textContent?.includes("已拒绝") === true);
  });

  it("approves a plan only once without restating it and does not offer approval again after history restoration", async () => {
    click(mounted!.container.querySelector('button[title^="AI 权限"]')!);
    const planToggle = [...mounted!.container.querySelectorAll("button")].find(
      (button) => button.getAttribute("title") === "先生成方案，批准后执行",
    );
    click(planToggle!);
    await sendAndEmit([{ type: "planSubmitted" }, { type: "done", answer: "步骤 A" }]);

    clickButton(mounted!.container, "批准并执行方案");
    await flush();
    const approval = mocks.chat.mock.calls[1][0] as Record<string, unknown>;
    expect(approval.message).toBe("按上面的方案执行。");
    expect(approval.message).not.toContain("步骤 A");
    expect(approval.planMode).toBeUndefined();
    expect(approval.images).toBeUndefined();
    expect(mounted!.container.textContent).toContain("已批准");
    expect([...mounted!.container.querySelectorAll("button")].some((b) => b.textContent === "批准并执行方案")).toBe(false);

    act(() => {
      mocks.channels.at(-1)!.onEvent({ type: "done", answer: "Execution complete" });
    });
    await flush();
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "Demo", createdAt: 0, updatedAt: 0, scope: {} }]);
    mocks.messages.mockResolvedValue([
      { id: "p1", role: "plan", content: { type: "planSubmitted", plan: "步骤 A" } },
      { id: "u1", role: "user", content: { role: "user", content: "按上面的方案执行。" } },
      { id: "a1", role: "assistant", content: { role: "assistant", content: "Execution complete" } },
    ]);
    click(mounted!.container.querySelector('button[title="历史会话"]')!);
    await flush();
    clickButton(mounted!.container, "Demo");
    await flush();
    expect(mounted!.container.textContent).toContain("已批准");
    expect([...mounted!.container.querySelectorAll("button")].some((b) => b.textContent === "批准并执行方案")).toBe(false);
  });

  it("reuses the original request images, references, and plan mode on retry", async () => {
    act(() => {
      useUi.setState({
        sessions: [{ id: "s1", assetId: null, name: "生产机", kind: "ssh", status: "connected", tabs: [], createdAt: 1 }],
        aiRulePrefill: null,
      });
    });
    mounted?.unmount();
    mocks.channels.length = 0;
    await show();
    click(mounted!.container.querySelector('button[title^="AI 权限"]')!);
    const planToggle = [...mounted!.container.querySelectorAll("button")].find(
      (button) => button.getAttribute("title") === "先生成方案，批准后执行",
    );
    click(planToggle!);

    const textarea = mounted!.container.querySelector<HTMLTextAreaElement>("textarea[aria-label='消息输入']")!;
    const file = new File([new Uint8Array([1, 2, 3])], "retry.png", { type: "image/png" });
    const paste = new Event("paste", { bubbles: true, cancelable: true });
    Object.assign(paste, {
      clipboardData: {
        items: [{ kind: "file", type: "image/png", getAsFile: () => file }],
        files: [],
      },
    });
    await act(async () => {
      textarea.dispatchEvent(paste);
    });
    await flushUntil(() => mounted!.container.querySelectorAll(".nx-attach").length === 1);

    setInputValue(textarea, "@");
    click(
      [...mounted!.container.querySelectorAll("button")].find((button) =>
        button.textContent?.includes("生产机"),
      )!,
    );
    setInputValue(textarea, "重试快照");
    click(mounted!.container.querySelector('button[title="发送 (Enter)"]')!);
    await flush();
    act(() => {
      mocks.channels[0].onEvent({ type: "error", message: "Network timeout", retryable: true });
    });
    await flush();

    clickButton(mounted!.container, "重试");
    await flush();
    const retry = mocks.chat.mock.calls[1][0] as Record<string, unknown>;
    expect(retry.message).toBe("[引用对象]\n- 生产机（ssh 会话）\n\n重试快照");
    expect(retry.images).toEqual([expect.stringContaining("data:image/png;base64")]);
    expect(retry.planMode).toBe(true);
  });

  it("does not execute an approved plan again on retry while still allowing a new plan to be approved independently", async () => {
    click(mounted!.container.querySelector('button[title^="AI 权限"]')!);
    const planToggle = [...mounted!.container.querySelectorAll("button")].find(
      (button) => button.getAttribute("title") === "先生成方案，批准后执行",
    );
    click(planToggle!);
    await sendAndEmit([{ type: "planSubmitted" }, { type: "done", answer: "步骤 A" }]);

    clickButton(mounted!.container, "批准并执行方案");
    await flush();
    act(() => {
      mocks.channels.at(-1)!.onEvent({ type: "error", message: "Execution interrupted", retryable: true });
    });
    await flush();

    clickButton(mounted!.container, "重试");
    await flush();
    expect(mocks.chat.mock.calls.length).toBe(2);
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("该计划已执行"));

    click(mounted!.container.querySelector('button[title^="AI 权限与模式"]')!);
    const planToggleAgain = [...mounted!.container.querySelectorAll("button")].find(
      (button) => button.getAttribute("title") === "先生成方案，批准后执行",
    );
    click(planToggleAgain!);
    const input = mounted!.container.querySelector<HTMLTextAreaElement>("textarea[aria-label='消息输入']")!;
    setInputValue(input, "Continue");
    click(mounted!.container.querySelector('button[title="发送 (Enter)"]')!);
    await flush();
    act(() => {
      mocks.channels.at(-1)!.onEvent({ type: "planSubmitted" });
      mocks.channels.at(-1)!.onEvent({ type: "done", answer: "Step B" });
    });
    await flush();
    clickButton(mounted!.container, "批准并执行方案");
    await flush();
    const second = mocks.chat.mock.calls[3][0] as Record<string, unknown>;
    expect(second.message).toBe("按上面的方案执行。");
  });

  it("sends backend-resolvable terminal and file objects for @ references with the message", async () => {
    act(() => {
      useUi.setState({
        sessions: [{ id: "s1", assetId: null, name: "生产机", kind: "ssh", status: "connected", tabs: [], createdAt: 1 }],
        workspaces: [
          {
            id: "w1",
            kind: "session",
            title: "Production",
            sessionId: "s1",
            panes: [
              {
                id: "p1",
                activeTabId: "ui-t2",
                tabs: [
                  { id: "ui-t2", kind: "terminal", title: "构建机", tabId: "term-2", closable: true },
                  { id: "f1", kind: "files", title: "nginx.conf", path: "/etc/nginx/nginx.conf", sessionId: "s1", closable: true },
                ],
              },
            ],
            activePaneId: "p1",
            closable: true,
          },
        ],
      });
    });
    mounted?.unmount();
    await show();

    const textarea = () => mounted!.container.querySelector<HTMLTextAreaElement>("textarea[aria-label='消息输入']")!;
    setInputValue(textarea(), "@");
    click(
      [...mounted!.container.querySelectorAll("button")].find((button) =>
        button.textContent?.includes("构建机"),
      )!,
    );
    setInputValue(textarea(), "@");
    click(
      [...mounted!.container.querySelectorAll("button")].find((button) =>
        button.textContent?.includes("nginx.conf"),
      )!,
    );
    setInputValue(textarea(), "Inspect these two");
    click(mounted!.container.querySelector('button[title="发送 (Enter)"]')!);
    await flush();

    const call = mocks.chat.mock.calls[0][0] as Record<string, unknown>;
    expect(call.refs).toEqual([
      { kind: "tab", id: "term-2", label: "构建机", path: undefined, sessionId: undefined },
      { kind: "file", id: "f1", label: "nginx.conf", path: "/etc/nginx/nginx.conf", sessionId: "s1" },
    ]);
    expect(call.message).toContain("[引用对象]");
    expect(call.message).toContain("构建机");
  });

  it("offers a session approval that names its scope and syncs the decision", async () => {
    await sendAndEmit([
      {
        type: "confirmRequired",
        id: "call-round",
        tool: "exec_commands",
        rendered: "$ terraform plan",
        risk: "needs_confirm",
        commands: ["terraform plan"],
        sessionScope: "terraform 命令",
        confirmationNonce: "n-round",
      },
    ]);
    expect(mounted!.container.textContent).toContain("本次对话内不再询问：terraform 命令");
    clickButton(mounted!.container, "本会话允许");
    await flush();
    expect(mocks.confirm.mock.calls[0][0].decision).toBe("allow_session");
    expect(mounted!.container.textContent).toContain("本会话已允许此类");
  });

  it("shows policy and user refusals as interceptions instead of failures", async () => {
    await sendAndEmit([
      { type: "toolCall", id: "call-policy", name: "exec_commands", display: "rm -rf /data" },
      { type: "toolResult", id: "call-policy", ok: false, summary: "权限策略已拒绝: 只读权限模式禁止此操作", text: "权限策略已拒绝: 只读权限模式禁止此操作", exitCode: 1 },
      { type: "toolCall", id: "call-user", name: "write_file", display: "/etc/hosts" },
      { type: "toolResult", id: "call-user", ok: false, summary: "用户拒绝了此操作", text: "用户拒绝了此操作", exitCode: 1 },
      { type: "toolCall", id: "call-failed", name: "exec_commands", display: "false" },
      { type: "toolResult", id: "call-failed", ok: false, summary: "exit status 1", text: "exit status 1", exitCode: 1 },
    ]);
    const text = mounted!.container.textContent ?? "";
    expect(text).toContain("已拦截");
    expect(text).toContain("拦截原因：只读权限模式禁止此操作");
    expect(text).toContain("你拒绝了这次操作，未执行。");
    expect(text).toContain("exit status 1");
    expect(text.split("已拦截").length - 1).toBe(1);
  });

  it("hides the session approval when the backend offers no scope", async () => {
    await sendAndEmit([
      {
        type: "confirmRequired",
        id: "call-danger",
        tool: "exec_commands",
        rendered: "$ kill -9 1",
        risk: "danger",
        reason: "进程终止",
        commands: ["kill -9 1"],
        confirmationNonce: "n-danger",
      },
    ]);
    const labels = [...mounted!.container.querySelectorAll("button")].map((button) => button.textContent);
    expect(labels).toContain("允许一次");
    expect(labels).not.toContain("本会话允许");
    expect(labels).not.toContain("永久允许");
    expect(mounted!.container.textContent).toContain("高危操作，需要你确认");
    expect(mounted!.container.textContent).toContain("进程终止");
  });

  it("uses structured commands for command interception rules and preview.path for file rules", async () => {
    act(() => useUi.setState({ aiRulePrefill: null }));
    await sendAndEmit([
      {
        type: "confirmRequired",
        id: "call-rule-command",
        tool: "exec_commands",
        rendered: "unexpected rendered",
        risk: "needs_confirm",
        commands: ["ls -l", "pwd"],
        confirmationNonce: "n-rule-command",
      },
    ]);
    clickButton(mounted!.container, "以后都拦截");
    await flush();
    expect(useUi.getState().aiRulePrefill).toBe("ls -l\npwd");

    act(() => mocks.channels[0].onEvent({ type: "done", answer: "Done" }));
    await flush();
    mounted?.unmount();
    mocks.channels.length = 0;
    await show();
    act(() => useUi.setState({ aiRulePrefill: null }));
    await sendAndEmit([
      {
        type: "confirmRequired",
        id: "call-rule-file",
        tool: "write_file",
        rendered: "unexpected rendered",
        risk: "needs_confirm",
        preview: { path: "/etc/nginx/nginx.conf", before: "a", after: "b", kind: "edit" },
        confirmationNonce: "n-rule-file",
      },
    ]);
    clickButton(mounted!.container, "以后都拦截");
    await flush();
    expect(useUi.getState().aiRulePrefill).toBe("/etc/nginx/nginx.conf");
  });

  it("hides interception-rule buttons for unsupported objects and shows an error with retry when permission loading fails", async () => {
    await sendAndEmit([
      {
        type: "confirmRequired",
        id: "call-unsupported",
        tool: "read_screen",
        rendered: "read_screen",
        confirmationNonce: "n-unsupported",
      },
    ]);
    expect([...mounted!.container.querySelectorAll("button")].some((b) => b.textContent === "以后都拦截")).toBe(false);

    mocks.getPermission.mockRejectedValueOnce(new Error("权限服务不可用"));
    click(mounted!.container.querySelector('button[title^="AI 权限"]')!);
    await flush();
    expect(mounted!.container.textContent).toContain("权限读取失败 · 权限服务不可用");
    expect(mounted!.container.textContent).not.toContain("查看直接执行");

    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    clickButton(mounted!.container, "重试");
    await flush();
    expect(mounted!.container.textContent).toContain("查看直接执行");
    expect(mounted!.container.textContent).not.toContain("格式化磁盘");
  });
});
