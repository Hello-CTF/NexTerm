/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, StrictMode } from "react";
import { clickButton, flush, mount, setInputValue, setSelectValue, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  call: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, call: mocks.call };
});

import { InventoryTargetActions, ResetLiveOperationRegistry, SetCancelTimeoutForTest } from "../../features/explorer/InventoryTargetActions";

const dockerTarget = { type: "context", name: "prod", dockerEndpoint: "ssh://ops@prod.example" };
const podTarget = {
  type: "pod",
  context: "staging",
  namespace: "ops",
  pod: "api-0",
  container: "app",
  containers: ["app", "sidecar"],
};

let mounted: MountedView | undefined;

function render(props: Partial<Parameters<typeof InventoryTargetActions>[0]> & { kind: string; target: Record<string, unknown> }): MountedView {
  return mount(
    createElement(InventoryTargetActions, {
      ownerKey: "owner-a",
      ...props,
    }),
  );
}

function challengeFor(sessionName: string) {
  return {
    code: "needs_confirm",
    detail: {
      kind: "docker_production_confirmation",
      targetSetHash: "a".repeat(64),
      commandHash: "b".repeat(64),
      productionTargets: [{ sessionId: "s1", sessionName, environment: "production" }],
    },
  };
}

function challenge() {
  return challengeFor("production");
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  ResetLiveOperationRegistry();
  SetCancelTimeoutForTest(10000);
  mounted = undefined;
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("inventory target actions", () => {
  it("executes a docker context target with the typed exec request", async () => {
    mocks.call.mockResolvedValue({ output: "Linux\n", exitCode: 0 });
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    expect(m.container.textContent).toContain("context/prod");

    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "uname -a");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();

    expect(mocks.call).toHaveBeenCalledTimes(1);
    const [cmd, args] = mocks.call.mock.calls[0];
    expect(cmd).toBe("inventory_target_exec");
    expect(args.args).toMatchObject({
      kind: "docker-context",
      target: dockerTarget,
      action: "exec",
      container: "web",
      command: ["uname", "-a"],
      sessionId: undefined,
      confirmation: undefined,
    });
    expect(typeof args.args.operationId).toBe("string");
    expect(args.args.operationId.length).toBeGreaterThan(0);
    await waitFor(() => expect(m.container.textContent).toContain("Linux"));
    expect(m.container.textContent).toContain("退出码 0");
  });

  it("requires a container for docker context targets", async () => {
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    clickButton(m.container, "执行");
    await flush();
    expect(mocks.call).not.toHaveBeenCalled();
    expect(m.container.textContent).toContain("请输入容器名");
  });

  it("selects the container for a kubernetes pod target", async () => {
    mocks.call.mockResolvedValue({ output: "ok\n", exitCode: 0 });
    const m = (mounted = render({ kind: "kubernetes", target: podTarget }));
    expect(m.container.textContent).toContain("ops/api-0");
    const select = m.container.querySelector<HTMLSelectElement>('select[aria-label="容器"]')!;
    expect([...select.options].map((option) => option.value)).toEqual(["app", "sidecar"]);

    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setSelectValue(select, "sidecar");
    clickButton(m.container, "执行");
    await flush();

    expect(mocks.call).toHaveBeenCalledTimes(1);
    const [, args] = mocks.call.mock.calls[0];
    expect(args.args).toMatchObject({
      kind: "kubernetes",
      target: podTarget,
      action: "exec",
      container: "sidecar",
      command: ["true"],
    });
  });

  it("requires a managed session for server docker exec", async () => {
    const m = (mounted = render({
      kind: "docker-context",
      target: dockerTarget,
      requireSession: true,
      sessions: [{ id: "sess-1", name: "prod-host" }],
    }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();
    expect(mocks.call).not.toHaveBeenCalled();
    expect(m.container.textContent).toContain("请选择受管会话");

    mocks.call.mockResolvedValue({ output: "ok\n", exitCode: 0 });
    setSelectValue(m.container.querySelector('select[aria-label="受管会话"]')!, "sess-1");
    clickButton(m.container, "执行");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(1);
    expect(mocks.call.mock.calls[0][1].args.sessionId).toBe("sess-1");
  });

  it("runs the production confirmation flow and retries with the hashes", async () => {
    mocks.call.mockRejectedValueOnce(challenge()).mockResolvedValueOnce({ output: "ok\n", exitCode: 0 });
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "uname -a");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();

    const input = document.body.querySelector<HTMLInputElement>('input[aria-label="输入生产环境确认文字"]');
    expect(input).not.toBeNull();
    const confirm = [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("确认执行"));
    expect(confirm?.disabled).toBe(true);
    setInputValue(input!, "production");
    expect(confirm?.disabled).toBe(false);
    act(() => confirm!.click());
    await flush();

    expect(mocks.call).toHaveBeenCalledTimes(2);
    const retry = mocks.call.mock.calls[1][1].args;
    expect(retry.confirmation).toEqual({
      confirmed: true,
      targetSetHash: "a".repeat(64),
      commandHash: "b".repeat(64),
    });
    expect(retry.command).toEqual(["uname", "-a"]);
    expect(retry.container).toBe("web");
    await waitFor(() => expect(m.container.textContent).toContain("ok"));
  });

  it("cancels the production confirmation without retrying", async () => {
    mocks.call.mockRejectedValueOnce(challenge());
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();
    expect(document.body.querySelector('input[aria-label="输入生产环境确认文字"]')).not.toBeNull();

    const cancel = [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("取消"));
    act(() => cancel!.click());
    await flush();
    expect(document.body.querySelector('input[aria-label="输入生产环境确认文字"]')).toBeNull();
    expect(mocks.call).toHaveBeenCalledTimes(1);
    expect(m.container.textContent).not.toContain("退出码");
  });

  it("presents a non-zero exit code with the preserved output", async () => {
    mocks.call.mockRejectedValue({ code: "io", message: "命令已执行但返回非零退出码", detail: { exitCode: 7, output: "boom\n" } });
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "false");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();

    const alert = m.container.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain("非零退出码");
    expect(alert?.textContent).toContain("退出码 7");
    expect(alert?.textContent).toContain("boom");
  });

  it("clears the result with the settled clear action", async () => {
    mocks.call.mockResolvedValue({ output: "ok\n", exitCode: 0 });
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();
    await waitFor(() => expect(m.container.textContent).toContain("ok"));

    clickButton(m.container, "清除");
    await flush();
    expect(m.container.querySelector('[role="status"]')).toBeNull();
    expect(m.container.textContent).not.toContain("ok");
  });

  it("ignores an in-flight result and keeps the command visible as still running", async () => {
    let resolveExec: (value: { output: string; exitCode: number }) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveExec = resolve; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();
    expect(m.container.textContent).toContain("执行中");

    clickButton(m.container, "忽略结果");
    await flush();
    const note = m.container.querySelector('[role="note"]');
    expect(note?.textContent).toContain("命令仍在目标上执行");
    expect(note?.textContent).toContain("不代表命令已停止");
    expect(m.container.textContent).not.toContain("执行中");

    await act(async () => { resolveExec({ output: "ignored secret\n", exitCode: 0 }); });
    await flush();
    expect(m.container.textContent).not.toContain("ignored secret");
    expect(m.container.querySelector('[role="status"]')).toBeNull();
    expect(m.container.querySelector('[role="note"]')?.textContent).toContain("命令已结束");

    clickButton(m.container, "清除");
    await flush();
    expect(m.container.querySelector('[role="note"]')).toBeNull();
  });

  it("blocks re-execution while an ignored command is still running and keeps its notice", async () => {
    let resolveA: (value: { output: string; exitCode: number }) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveA = resolve; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();
    clickButton(m.container, "忽略结果");
    await flush();
    expect(m.container.querySelector('[role="note"]')?.textContent).toContain("命令仍在目标上执行");

    // B must not start while A is still running: no second call, the
    // execute action stays disabled, and A's notice is not dropped.
    const execButton = () => [...m.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "执行");
    expect(execButton()?.disabled).toBe(true);
    clickButton(m.container, "执行");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(1);
    expect(m.container.querySelector('[role="note"]')?.textContent).toContain("命令仍在目标上执行");

    // A settles: the notice updates and its output stays hidden, but the
    // ignored generation is still tracked until the notice is cleared.
    await act(async () => { resolveA({ output: "A secret\n", exitCode: 0 }); });
    await flush();
    expect(m.container.textContent).not.toContain("A secret");
    expect(m.container.querySelector('[role="note"]')?.textContent).toContain("命令已结束");
    expect(execButton()?.disabled).toBe(true);

    clickButton(m.container, "清除");
    await flush();
    expect(m.container.querySelector('[role="note"]')).toBeNull();
    expect(execButton()?.disabled).toBe(false);

    mocks.call.mockResolvedValueOnce({ output: "B ok\n", exitCode: 0 });
    clickButton(m.container, "执行");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(2);
    await waitFor(() => expect(m.container.textContent).toContain("B ok"));
  });

  it("keeps the running notice clear-proof and re-entry blocked until the ignored command settles", async () => {
    let resolveA: (value: { output: string; exitCode: number }) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveA = resolve; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();
    clickButton(m.container, "忽略结果");
    await flush();
    const note = () => m.container.querySelector('[role="note"]');
    expect(note()?.textContent).toContain("命令仍在目标上执行");

    // While A runs there is no effective clear: the button is absent, the
    // notice cannot be deleted, and B cannot start.
    const clearButton = () => [...m.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "清除");
    expect(clearButton()).toBeUndefined();
    const execButton = () => [...m.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "执行");
    expect(execButton()?.disabled).toBe(true);
    clickButton(m.container, "执行");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(1);
    expect(note()?.textContent).toContain("命令仍在目标上执行");

    // A settles: done is shown without output, only now may the notice be
    // cleared, and only clearing it re-enables execution.
    await act(async () => { resolveA({ output: "A secret\n", exitCode: 0 }); });
    await flush();
    expect(m.container.textContent).not.toContain("A secret");
    expect(note()?.textContent).toContain("命令已结束");
    expect(execButton()?.disabled).toBe(true);
    expect(clearButton()).not.toBeUndefined();

    clickButton(m.container, "清除");
    await flush();
    expect(note()).toBeNull();
    expect(execButton()?.disabled).toBe(false);
  });

  it("cancels an in-flight command and allows retry once the cancelled contract settles", async () => {
    let rejectExec: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectExec = reject; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();
    expect(m.container.textContent).toContain("执行中");

    clickButton(m.container, "取消执行");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(2);
    const cancelArgs = mocks.call.mock.calls[1][1].args;
    expect(cancelArgs.action).toBe("cancel");
    expect(cancelArgs.kind).toBe("docker-context");
    expect(cancelArgs.target).toEqual(dockerTarget);
    expect(cancelArgs.container).toBe("web");
    // The cancel carries the frozen launch identity of the in-flight exec.
    expect(cancelArgs.operationId).toBe(mocks.call.mock.calls[0][1].args.operationId);
    expect(typeof cancelArgs.operationId).toBe("string");

    await act(async () => { rejectExec({ code: "io", message: "执行已取消", detail: { cancelled: true } }); });
    await flush();
    const note = m.container.querySelector('[role="note"]');
    expect(note?.textContent).toContain("执行已取消");
    expect(m.container.textContent).not.toContain("命令仍在目标上执行");
    expect(m.container.querySelector('[role="alert"]')).toBeNull();

    const execButton = () => [...m.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "执行");
    expect(execButton()?.disabled).toBe(false);

    clickButton(m.container, "清除");
    await flush();
    expect(m.container.querySelector('[role="note"]')).toBeNull();
  });

  it("cancel after editing the container input still cancels the original exec", async () => {
    let rejectExec: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectExec = reject; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();

    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "other");
    clickButton(m.container, "取消执行");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(2);
    const cancelArgs = mocks.call.mock.calls[1][1].args;
    // The cancel reads the frozen launch snapshot, not the edited input.
    expect(cancelArgs.container).toBe("web");
    expect(cancelArgs.operationId).toBe(mocks.call.mock.calls[0][1].args.operationId);

    await act(async () => { rejectExec({ code: "io", message: "执行已取消", detail: { cancelled: true } }); });
    await flush();
    expect(m.container.querySelector('[role="note"]')?.textContent).toContain("执行已取消");
  });

  it("cancel after editing the session selection still cancels the original exec", async () => {
    let rejectExec: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectExec = reject; }));
    const m = (mounted = render({
      kind: "docker-context",
      target: dockerTarget,
      requireSession: true,
      sessions: [{ id: "sess-1", name: "prod-host" }, { id: "sess-2", name: "other-host" }],
    }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    setSelectValue(m.container.querySelector('select[aria-label="受管会话"]')!, "sess-1");
    clickButton(m.container, "执行");
    await flush();

    setSelectValue(m.container.querySelector('select[aria-label="受管会话"]')!, "sess-2");
    clickButton(m.container, "取消执行");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(2);
    const cancelArgs = mocks.call.mock.calls[1][1].args;
    expect(cancelArgs.sessionId).toBe("sess-1");
    expect(cancelArgs.operationId).toBe(mocks.call.mock.calls[0][1].args.operationId);

    await act(async () => { rejectExec({ code: "io", message: "执行已取消", detail: { cancelled: true } }); });
    await flush();
    expect(m.container.querySelector('[role="note"]')?.textContent).toContain("执行已取消");
  });

  it("keeps the in-flight exec cancellable on target switch with the frozen snapshot", async () => {
    let rejectA: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectA = reject; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();
    expect(m.container.textContent).toContain("执行中");

    // Switch target while A is in flight: the operation is retained with
    // its cancel control (same owner), and B cannot start until it settles.
    const otherTarget = { type: "context", name: "other", dockerEndpoint: "ssh://ops@other.example" };
    act(() => m.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: otherTarget, ownerKey: "owner-a" })));
    await flush();
    expect(m.container.textContent).toContain("context/other");
    expect(m.container.textContent).toContain("执行中");
    expect(mocks.call).toHaveBeenCalledTimes(1);
    const execButton = () => [...m.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "执行");
    expect(execButton()?.disabled).toBe(true);

    // The retained operation is cancelled with its frozen snapshot; A
    // settles as cancelled and B is not polluted by A's late result.
    clickButton(m.container, "取消执行");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(2);
    const cancelArgs = mocks.call.mock.calls[1][1].args;
    expect(cancelArgs.action).toBe("cancel");
    expect(cancelArgs.target).toEqual(dockerTarget);
    expect(cancelArgs.operationId).toBe(mocks.call.mock.calls[0][1].args.operationId);
    await act(async () => { rejectA({ code: "io", message: "执行已取消", detail: { cancelled: true } }); });
    await flush();
    expect(m.container.querySelector('[role="status"]')).toBeNull();
    expect(m.container.textContent).not.toContain("执行已取消");
    expect(execButton()?.disabled).toBe(false);
  });

  it("cancel after editing the command input still uses the original request", async () => {
    let rejectExec: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectExec = reject; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();

    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "false");
    clickButton(m.container, "取消执行");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(2);
    const cancelArgs = mocks.call.mock.calls[1][1].args;
    // The cancel reads the frozen launch snapshot, not the edited command.
    expect(cancelArgs.command).toEqual(["true"]);
    expect(cancelArgs.operationId).toBe(mocks.call.mock.calls[0][1].args.operationId);

    await act(async () => { rejectExec({ code: "io", message: "执行已取消", detail: { cancelled: true } }); });
    await flush();
    expect(m.container.querySelector('[role="note"]')?.textContent).toContain("执行已取消");
  });

  it("keeps the in-flight exec tracked on owner switch and restores cancel for the original owner", async () => {
    let rejectA: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectA = reject; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();
    const operationIdA = mocks.call.mock.calls[0][1].args.operationId;

    // Switch to owner B: A's operation stays tracked with the switch-back
    // note and B cannot cancel it or start a replacement.
    act(() => m.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-b" })));
    await flush();
    expect(m.container.textContent).toContain("切换到 owner-a 后可取消");
    expect(mocks.call).toHaveBeenCalledTimes(1);
    const execButton = () => [...m.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "执行");
    expect(execButton()?.disabled).toBe(true);

    // Switch back to owner A: cancel is restored with A's own snapshot
    // identity and the operation is cancelled.
    act(() => m.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" })));
    await flush();
    clickButton(m.container, "取消执行");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(2);
    const cancelArgs = mocks.call.mock.calls[1][1].args;
    expect(cancelArgs.action).toBe("cancel");
    expect(cancelArgs.operationId).toBe(operationIdA);
    expect(cancelArgs.target).toEqual(dockerTarget);
    await act(async () => { rejectA({ code: "io", message: "执行已取消", detail: { cancelled: true } }); });
    await flush();
    expect(execButton()?.disabled).toBe(false);

    // B's own launch uses a fresh operationId, never A's.
    mocks.call.mockResolvedValueOnce({ output: "B ok\n", exitCode: 0 });
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(3);
    expect(mocks.call.mock.calls[2][1].args.operationId).not.toBe(operationIdA);
  });

  it("keeps cancel and ignore-results as distinct actions", async () => {
    let settleA: (resolve: boolean) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve, reject) => { settleA = (ok) => (ok ? resolve({ output: "A\n", exitCode: 0 }) : reject({ code: "io", message: "执行已取消", detail: { cancelled: true } })); }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();

    const cancelButton = () => [...m.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "取消执行");
    const ignoreButton = () => [...m.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "忽略结果");
    expect(cancelButton()).not.toBeUndefined();
    expect(ignoreButton()).not.toBeUndefined();

    // Ignore never calls the cancel API and starts the still-running track.
    clickButton(m.container, "忽略结果");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(1);
    expect(m.container.querySelector('[role="note"]')?.textContent).toContain("命令仍在目标上执行");

    // Settle A as ignored; the notice clears only through the clear action.
    await act(async () => { settleA(true); });
    await flush();
    expect(m.container.querySelector('[role="note"]')?.textContent).toContain("命令已结束");
    clickButton(m.container, "清除");
    await flush();
    expect(m.container.querySelector('[role="note"]')).toBeNull();

    // Cancel calls the cancel action and never opens the still-running track.
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { settleA = () => reject({ code: "io", message: "执行已取消", detail: { cancelled: true } }); }));
    clickButton(m.container, "执行");
    await flush();
    clickButton(m.container, "取消执行");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(3);
    expect(mocks.call.mock.calls[2][1].args.action).toBe("cancel");
    await act(async () => { settleA(false); });
    await flush();
    expect(m.container.querySelector('[role="note"]')?.textContent).toContain("执行已取消");
  });

  it("cancel transport failure surfaces the error and offers retry", async () => {
    let rejectExec: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectExec = reject; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();

    // The cancel IPC fails: the error is visible and retryable, and the
    // operation is not silently dropped.
    mocks.call.mockRejectedValueOnce(new Error("transport down"));
    clickButton(m.container, "取消执行");
    await flush();
    expect(m.container.textContent).toContain("取消失败");
    expect(m.container.textContent).toContain("transport down");
    const retryButton = () => [...m.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "重试取消");
    expect(retryButton()).not.toBeUndefined();
    const execButton = () => [...m.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "执行");
    expect(execButton()?.disabled).toBe(true);

    // Retry succeeds; the original exec settles as cancelled.
    mocks.call.mockResolvedValueOnce({});
    clickButton(m.container, "重试取消");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(3);
    await act(async () => { rejectExec({ code: "io", message: "执行已取消", detail: { cancelled: true } }); });
    await flush();
    expect(m.container.querySelector('[role="note"]')?.textContent).toContain("执行已取消");
    expect(execButton()?.disabled).toBe(false);
  });

  it("cancel pending retains the snapshot and blocks replacement until settle", async () => {
    let rejectExec: (reason: unknown) => void = () => {};
    let resolveCancel: (value: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectExec = reject; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();

    // The cancel IPC is slow: the pending state is visible, the snapshot
    // and cancel control are retained, and no replacement is allowed.
    mocks.call.mockImplementationOnce(() => new Promise((resolve) => { resolveCancel = resolve; }));
    clickButton(m.container, "取消执行");
    await flush();
    expect(m.container.textContent).toContain("取消中");
    const execButton = () => [...m.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "执行");
    expect(execButton()?.disabled).toBe(true);

    // The cancel resolves; the original exec settles as cancelled.
    await act(async () => { resolveCancel({}); });
    await flush();
    await act(async () => { rejectExec({ code: "io", message: "执行已取消", detail: { cancelled: true } }); });
    await flush();
    expect(m.container.querySelector('[role="note"]')?.textContent).toContain("执行已取消");
    expect(execButton()?.disabled).toBe(false);
  });

  it("ignore then target switch keeps the operation cancellable and B isolated", async () => {
    let rejectA: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectA = reject; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();
    clickButton(m.container, "忽略结果");
    await flush();
    expect(m.container.textContent).toContain("命令仍在目标上执行");

    // Switch target: the ignored operation stays live and cancellable with
    // its frozen snapshot; the new target cannot start a replacement.
    const otherTarget = { type: "context", name: "other", dockerEndpoint: "ssh://ops@other.example" };
    act(() => m.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: otherTarget, ownerKey: "owner-a" })));
    await flush();
    expect(m.container.textContent).toContain("命令仍在目标上执行");
    const execButton = () => [...m.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "执行");
    expect(execButton()?.disabled).toBe(true);

    // The ignored operation is cancelled with its frozen snapshot; A
    // settles as cancelled and B is not polluted.
    clickButton(m.container, "取消执行");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(2);
    expect(mocks.call.mock.calls[1][1].args.action).toBe("cancel");
    expect(mocks.call.mock.calls[1][1].args.operationId).toBe(mocks.call.mock.calls[0][1].args.operationId);
    await act(async () => { rejectA({ code: "io", message: "执行已取消", detail: { cancelled: true } }); });
    await flush();
    expect(m.container.textContent).not.toContain("执行已取消");
    expect(execButton()?.disabled).toBe(false);
  });

  it("ignore then unmount restores the operation and cancel on remount", async () => {
    let rejectA: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectA = reject; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();
    clickButton(m.container, "忽略结果");
    await flush();

    // Unmount with the ignored operation live: it is kept in the
    // owner-isolated registry, and a remount with the same owner restores
    // it with its cancel control.
    act(() => m.unmount());
    const m2 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    mounted = m2;
    await flush();
    expect(m2.container.textContent).toContain("命令仍在目标上执行");

    // The restored operation is cancelled with its frozen snapshot.
    clickButton(m2.container, "取消执行");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(2);
    expect(mocks.call.mock.calls[1][1].args.action).toBe("cancel");
    expect(mocks.call.mock.calls[1][1].args.operationId).toBe(mocks.call.mock.calls[0][1].args.operationId);
    await act(async () => { rejectA({ code: "io", message: "执行已取消", detail: { cancelled: true } }); });
    await flush();
    expect(m2.container.querySelector('[role="note"]')?.textContent).toContain("执行已取消");
  });

  it("invalidates the in-flight request and resets target state on a same-owner target switch", async () => {
    let resolveExec: (value: { output: string; exitCode: number }) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveExec = resolve; }));
    const m = (mounted = render({
      kind: "docker-context",
      target: dockerTarget,
      requireSession: true,
      sessions: [{ id: "sess-1", name: "prod-host" }],
    }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    setSelectValue(m.container.querySelector('select[aria-label="受管会话"]')!, "sess-1");
    clickButton(m.container, "执行");
    await flush();
    expect(m.container.textContent).toContain("执行中");

    const otherTarget = { type: "context", name: "other", dockerEndpoint: "ssh://ops@other.example" };
    act(() => m.root.render(createElement(InventoryTargetActions, {
      kind: "docker-context",
      target: otherTarget,
      ownerKey: "owner-a",
      requireSession: true,
      sessions: [{ id: "sess-1", name: "prod-host" }],
    })));
    await act(async () => { resolveExec({ output: "stale prod output\n", exitCode: 0 }); });
    await flush();

    expect(m.container.textContent).toContain("context/other");
    expect(m.container.textContent).not.toContain("stale prod output");
    expect(m.container.querySelector('[role="status"]')).toBeNull();
    expect(m.container.querySelector<HTMLInputElement>('input[aria-label="容器"]')?.value).toBe("");
    expect(m.container.querySelector<HTMLSelectElement>('select[aria-label="受管会话"]')?.value).toBe("");
  });

  it("discards a stale response after the owner switches", async () => {
    let resolveExec: (value: { output: string; exitCode: number }) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveExec = resolve; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();
    expect(m.container.textContent).toContain("执行中");

    act(() => m.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-b" })));
    await act(async () => { resolveExec({ output: "stale secret\n", exitCode: 0 }); });
    await flush();
    expect(m.container.textContent).not.toContain("stale secret");
    expect(m.container.querySelector('[role="status"]')).toBeNull();
  });

  it("marks non-executable targets without offering exec", () => {
    const m = (mounted = render({ kind: "kubernetes", target: { type: "context", name: "staging" } }));
    expect(m.container.textContent).toContain("该目标不支持 exec");
    expect(m.container.querySelector('input[aria-label="命令"]')).toBeNull();
  });

  it("does not evict another owner's live operation when many owners are active", async () => {
    mocks.call.mockImplementation(() => new Promise(() => {}));
    for (let index = 0; index < 33; index++) {
      const m = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: `owner-${index}` }));
      setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
      setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
      clickButton(m.container, "执行");
      await flush();
      act(() => m.unmount());
    }
    const m0 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-0" }));
    mounted = m0;
    await flush();
    expect(m0.container.textContent).toContain("执行中");
    const cancelButton = () => [...m0.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "取消执行");
    expect(cancelButton()).not.toBeUndefined();
    expect(cancelButton()?.disabled).toBe(false);
  });

  it("rejects a new exec visibly when the owner's live operations are at capacity", async () => {
    mocks.call.mockImplementation(() => new Promise(() => {}));
    for (let index = 0; index < 32; index++) {
      const target = { type: "context", name: `ctx-${index}`, dockerEndpoint: "ssh://ops@prod.example" };
      const m = mount(createElement(InventoryTargetActions, { kind: "docker-context", target, ownerKey: "owner-a" }));
      setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
      setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
      clickButton(m.container, "执行");
      await flush();
      act(() => m.unmount());
    }
    const otherTarget = { type: "context", name: "other", dockerEndpoint: "ssh://ops@other.example" };
    const m33 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: otherTarget, ownerKey: "owner-a" }));
    mounted = m33;
    setInputValue(m33.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m33.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m33.container, "执行");
    await flush();
    expect(m33.container.textContent).toContain("操作记录已达上限");
    expect(mocks.call).toHaveBeenCalledTimes(32);
  });

  it("an old settle does not delete a newer operation of the same owner", async () => {
    let rejectA: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectA = reject; }));
    const m1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m1.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m1.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m1.container, "执行");
    await flush();
    act(() => m1.unmount());

    const otherTarget = { type: "context", name: "other", dockerEndpoint: "ssh://ops@other.example" };
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>(() => {}));
    const m2 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: otherTarget, ownerKey: "owner-a" }));
    mounted = m2;
    setInputValue(m2.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m2.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m2.container, "执行");
    await flush();
    act(() => m2.unmount());

    // op-1 settles; op-2 must remain tracked.
    await act(async () => { rejectA({ code: "io", message: "执行已取消", detail: { cancelled: true } }); });
    await flush();
    const m3 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: otherTarget, ownerKey: "owner-a" }));
    mounted = m3;
    await flush();
    expect(m3.container.textContent).toContain("执行中");
  });

  it("restores the running notice and cancel/ignore entries on remount", async () => {
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>(() => {}));
    const m1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m1.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m1.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m1.container, "执行");
    await flush();
    act(() => m1.unmount());

    const m2 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    mounted = m2;
    await flush();
    // The plain running operation is restored with its running notice and
    // both cancel and ignore entries.
    expect(m2.container.textContent).toContain("执行中");
    const cancelButton = () => [...m2.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "取消执行");
    const ignoreButton = () => [...m2.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "忽略结果");
    expect(cancelButton()).not.toBeUndefined();
    expect(ignoreButton()).not.toBeUndefined();
    expect(cancelButton()?.disabled).toBe(false);
  });

  it("does not leak a late settle across owner or target switches", async () => {
    let resolveA: (value: { output: string; exitCode: number }) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveA = resolve; }));
    const m1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m1.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m1.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m1.container, "执行");
    await flush();
    act(() => m1.unmount());

    const m2 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    mounted = m2;
    await flush();
    // Switch to owner-b and another target before A settles.
    const otherTarget = { type: "context", name: "other", dockerEndpoint: "ssh://ops@other.example" };
    act(() => m2.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: otherTarget, ownerKey: "owner-b" })));
    await act(async () => { resolveA({ output: "A-SECRET\n", exitCode: 0 }); });
    await flush();
    expect(m2.container.textContent).not.toContain("A-SECRET");
  });

  it("does not show the ignored output after a natural settle", async () => {
    let resolveA: (value: { output: string; exitCode: number }) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveA = resolve; }));
    const m1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m1.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m1.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m1.container, "执行");
    await flush();
    clickButton(m1.container, "忽略结果");
    await flush();
    act(() => m1.unmount());

    const m2 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    mounted = m2;
    await flush();
    await act(async () => { resolveA({ output: "IGNORED-SECRET\n", exitCode: 0 }); });
    await flush();
    expect(m2.container.textContent).not.toContain("IGNORED-SECRET");
    expect(m2.container.textContent).toContain("命令已结束；结果已忽略");
    const execButton = () => [...m2.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "执行");
    expect(execButton()?.disabled).toBe(true);
    clickButton(m2.container, "清除");
    await flush();
    expect(execButton()?.disabled).toBe(false);
  });

  it("clears settled result, failure, cancelled, ignoredDone, and production on owner/target switch", async () => {
    mocks.call.mockResolvedValue({ output: "SETTLED-A\n", exitCode: 0 });
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();
    await waitFor(() => expect(m.container.textContent).toContain("SETTLED-A"));

    act(() => m.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-b" })));
    await flush();
    expect(m.container.textContent).not.toContain("SETTLED-A");
    expect(m.container.querySelector('[role="status"]')).toBeNull();
  });

  it("times out a permanently pending cancel into a retryable cancel-failed", async () => {
    let rejectExec: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectExec = reject; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();

    // The cancel IPC never settles: the pending state times out into a
    // visible, retryable cancel-failed while the snapshot is retained.
    SetCancelTimeoutForTest(50);
    mocks.call.mockImplementationOnce(() => new Promise(() => {}));
    clickButton(m.container, "取消执行");
    await flush();
    expect(m.container.textContent).toContain("取消中");
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 120)); });
    await flush();
    expect(m.container.textContent).toContain("取消失败");
    const retryButton = () => [...m.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "重试取消");
    expect(retryButton()).not.toBeUndefined();

    // Retry succeeds; the original exec settles as cancelled.
    mocks.call.mockResolvedValueOnce({});
    clickButton(m.container, "重试取消");
    await flush();
    await act(async () => { rejectExec({ code: "io", message: "执行已取消", detail: { cancelled: true } }); });
    await flush();
    expect(m.container.querySelector('[role="note"]')?.textContent).toContain("执行已取消");
  });

  it("does not leak an owner-only switched result after remount", async () => {
    let resolveA: (value: { output: string; exitCode: number }) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveA = resolve; }));
    const m1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m1.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m1.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m1.container, "执行");
    await flush();
    act(() => m1.unmount());

    // Remount to install the restore callback, then switch owner only.
    const m2 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    mounted = m2;
    await flush();
    act(() => m2.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-b" })));
    await act(async () => { resolveA({ output: "A-OWNER-ONLY-SECRET\n", exitCode: 0 }); });
    await flush();
    expect(m2.container.textContent).not.toContain("A-OWNER-ONLY-SECRET");
  });

  it("notifies all restore views of a settle, not just the last subscriber", async () => {
    let resolveA: (value: { output: string; exitCode: number }) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveA = resolve; }));
    const m1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m1.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m1.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m1.container, "执行");
    await flush();
    act(() => m1.unmount());

    // Two restore views of the same owner and target both receive the settle.
    const viewA = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    const viewB = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    mounted = viewB;
    await flush();
    await act(async () => { resolveA({ output: "shared result\n", exitCode: 0 }); });
    await flush();
    expect(viewA.container.textContent).toContain("shared result");
    expect(viewB.container.textContent).toContain("shared result");

    // Out-of-order unsubscribe: closing the first view does not affect the second.
    act(() => viewA.unmount());
    await flush();
    expect(viewB.container.textContent).toContain("shared result");
  });

  it("preserves the ignored-done notice when settling with no mounted view", async () => {
    let resolveA: (value: { output: string; exitCode: number }) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveA = resolve; }));
    const m1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m1.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m1.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m1.container, "执行");
    await flush();
    clickButton(m1.container, "忽略结果");
    await flush();
    act(() => m1.unmount());

    // A settles naturally with no mounted view; the ignored-done notice is
    // preserved for the remount instead of being lost.
    await act(async () => { resolveA({ output: "ignored secret\n", exitCode: 0 }); });
    await flush();
    const m2 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    mounted = m2;
    await flush();
    expect(m2.container.textContent).toContain("命令已结束；结果已忽略");
    expect(m2.container.textContent).not.toContain("ignored secret");
  });

  it("notifies a remounted view of a cancel timeout with a retry entry", async () => {
    let rejectExec: (reason: unknown) => void = () => {};
    let rejectCancel: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectExec = reject; }));
    const m1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m1.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m1.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m1.container, "执行");
    await flush();

    // Send a permanently pending cancel, then unmount before it settles.
    SetCancelTimeoutForTest(50);
    mocks.call.mockImplementationOnce(() => new Promise((_, reject) => { rejectCancel = reject; }));
    clickButton(m1.container, "取消执行");
    await flush();
    act(() => m1.unmount());

    // The remounted view shows cancel-pending, then the timeout with retry.
    const m2 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    mounted = m2;
    await flush();
    expect(m2.container.textContent).toContain("取消中");
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 120)); });
    await flush();
    expect(m2.container.textContent).toContain("取消失败");
    const retryButton = () => [...m2.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "重试取消");
    expect(retryButton()).not.toBeUndefined();

    // Retry succeeds; a late reject of the first cancel is ignored by the
    // operation-level sequence and does not clobber the retry state.
    mocks.call.mockResolvedValueOnce({});
    clickButton(m2.container, "重试取消");
    await flush();
    await act(async () => { rejectCancel(new Error("late cancel reject")); });
    await flush();
    expect(m2.container.textContent).toContain("取消中");
    expect(m2.container.textContent).not.toContain("取消失败");

    // The original exec settles as cancelled.
    await act(async () => { rejectExec({ code: "io", message: "执行已取消", detail: { cancelled: true } }); });
    await flush();
    expect(m2.container.querySelector('[role="note"]')?.textContent).toContain("执行已取消");
  });

  it("does not leak an owner-only switched failure after remount", async () => {
    let rejectA: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectA = reject; }));
    const m1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m1.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m1.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m1.container, "执行");
    await flush();
    act(() => m1.unmount());

    // Remount to install the restore subscription, then switch owner only.
    const m2 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    mounted = m2;
    await flush();
    act(() => m2.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-b" })));
    await act(async () => { rejectA(new Error("A-FAILURE-SECRET")); });
    await flush();
    expect(m2.container.textContent).not.toContain("A-FAILURE-SECRET");
    expect(m2.container.querySelector('[role="alert"]')).toBeNull();
  });

  it("does not leak an owner-only switched cancelled settle after remount", async () => {
    let rejectA: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectA = reject; }));
    const m1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m1.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m1.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m1.container, "执行");
    await flush();
    act(() => m1.unmount());

    const m2 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    mounted = m2;
    await flush();
    act(() => m2.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-b" })));
    await act(async () => { rejectA({ code: "io", message: "执行已取消", detail: { cancelled: true } }); });
    await flush();
    expect(m2.container.textContent).not.toContain("执行已取消");
  });

  it("restores the original owner's outcome after switching back", async () => {
    let resolveA: (value: { output: string; exitCode: number }) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveA = resolve; }));
    const m1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m1.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m1.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m1.container, "执行");
    await flush();
    act(() => m1.unmount());

    // The settle lands while the remounted view is switched to owner B and
    // is not rendered there; switching back to owner A replays it.
    const m2 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    mounted = m2;
    await flush();
    act(() => m2.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-b" })));
    await act(async () => { resolveA({ output: "A-RESTORED\n", exitCode: 0 }); });
    await flush();
    expect(m2.container.textContent).not.toContain("A-RESTORED");

    act(() => m2.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" })));
    await flush();
    expect(m2.container.textContent).toContain("A-RESTORED");
    expect(m2.container.textContent).toContain("退出码 0");
  });

  it("restores a running operation under StrictMode double subscription", async () => {
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>(() => {}));
    const m1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m1.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m1.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m1.container, "执行");
    await flush();
    act(() => m1.unmount());

    // StrictMode replays effects: the first subscribe/unsubscribe cycle must
    // not detach the replayed subscription, and exec still happened once.
    const m2 = mount(createElement(StrictMode, null, createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" })));
    mounted = m2;
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(1);
    expect(m2.container.textContent).toContain("执行中");
    const cancelButton = () => [...m2.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "取消执行");
    expect(cancelButton()).not.toBeUndefined();
    expect(cancelButton()?.disabled).toBe(false);
  });

  it("rejects at capacity without evicting an unconsumed outcome", async () => {
    mocks.call.mockResolvedValueOnce({ output: "KEPT-RESULT\n", exitCode: 0 });
    mocks.call.mockImplementation(() => new Promise(() => {}));
    // 31 never-settling operations plus one that settles and stays retained.
    for (let index = 0; index < 32; index++) {
      const target = { type: "context", name: `ctx-${index}`, dockerEndpoint: "ssh://ops@prod.example" };
      const m = mount(createElement(InventoryTargetActions, { kind: "docker-context", target, ownerKey: "owner-a" }));
      setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
      setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
      clickButton(m.container, "执行");
      await flush();
      act(() => m.unmount());
    }
    const otherTarget = { type: "context", name: "other", dockerEndpoint: "ssh://ops@other.example" };
    const m33 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: otherTarget, ownerKey: "owner-a" }));
    mounted = m33;
    setInputValue(m33.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m33.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m33.container, "执行");
    await flush();
    expect(m33.container.textContent).toContain("操作记录已达上限");
    expect(mocks.call).toHaveBeenCalledTimes(32);

    // The retained outcome was not evicted: its target still replays it.
    const keptTarget = { type: "context", name: "ctx-0", dockerEndpoint: "ssh://ops@prod.example" };
    const m0 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: keptTarget, ownerKey: "owner-a" }));
    mounted = m0;
    await flush();
    expect(m0.container.textContent).toContain("KEPT-RESULT");
  });

  it("does not open the production dialog for a stale owner-only challenge", async () => {
    let rejectA: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectA = reject; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "uname -a");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();

    // The challenge lands after an owner-only switch: it only ends the
    // original operation and never opens B's dialog.
    act(() => m.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-b" })));
    await act(async () => { rejectA(challengeFor("A-PRODUCTION-SECRET")); });
    await flush();
    expect(document.body.querySelector('[role="dialog"]')).toBeNull();
    expect(document.body.textContent).not.toContain("A-PRODUCTION-SECRET");
    expect(document.body.textContent).not.toContain("uname -a");

    // Switching back to owner A shows no dialog and no retained record.
    act(() => m.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" })));
    await flush();
    expect(document.body.querySelector('[role="dialog"]')).toBeNull();
    expect(document.body.textContent).not.toContain("A-PRODUCTION-SECRET");
  });

  it("does not open the production dialog for a stale target-switch challenge", async () => {
    let rejectA: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectA = reject; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "uname -a");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();

    // The challenge lands after switching target: it ends the original
    // operation without opening the dialog on the new target's page.
    const otherTarget = { type: "context", name: "other", dockerEndpoint: "ssh://ops@other.example" };
    act(() => m.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: otherTarget, ownerKey: "owner-a" })));
    await act(async () => { rejectA(challengeFor("A-TARGET-SECRET")); });
    await flush();
    expect(document.body.querySelector('[role="dialog"]')).toBeNull();
    expect(m.container.textContent).not.toContain("A-TARGET-SECRET");
  });

  it("binds the dialog and its retry to the original launch snapshot", async () => {
    let rejectA: (reason: unknown) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((_, reject) => { rejectA = reject; }));
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "uname -a");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();

    // Edit both inputs while the exec is in flight; the challenge binds the
    // dialog to the frozen launch, not to the page's current inputs.
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "rm -rf /");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "other");
    await act(async () => { rejectA(challenge()); });
    await flush();

    const dialog = document.body.querySelector('[role="dialog"]');
    expect(dialog).not.toBeNull();
    expect(dialog?.textContent).toContain("uname -a");
    expect(dialog?.textContent).not.toContain("rm -rf");

    const input = document.body.querySelector<HTMLInputElement>('input[aria-label="输入生产环境确认文字"]');
    setInputValue(input!, "production");
    const confirm = [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("确认执行"));
    act(() => confirm!.click());
    await flush();

    expect(mocks.call).toHaveBeenCalledTimes(2);
    const retry = mocks.call.mock.calls[1][1].args;
    expect(retry.command).toEqual(["uname", "-a"]);
    expect(retry.container).toBe("web");
    expect(retry.confirmation).toEqual({
      confirmed: true,
      targetSetHash: "a".repeat(64),
      commandHash: "b".repeat(64),
    });
    expect(retry.operationId).not.toBe(mocks.call.mock.calls[0][1].args.operationId);
  });

  it("visibly rejects a second exec on the same owner/target while one is unsettled", async () => {
    mocks.call.mockImplementation(() => new Promise(() => {}));
    const viewA = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    const viewB = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    mounted = viewB;
    await flush();
    setInputValue(viewA.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(viewA.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(viewA.container, "执行");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(1);

    // B was mounted before the launch and holds no local operation; its
    // replacement attempt is rejected visibly before any call.
    setInputValue(viewB.container.querySelector('input[aria-label="命令"]')!, "false");
    setInputValue(viewB.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(viewB.container, "执行");
    await flush();
    expect(mocks.call).toHaveBeenCalledTimes(1);
    expect(viewB.container.textContent).toContain("该目标已有在途操作");
    expect(viewA.container.textContent).toContain("执行中");
  });

  it("tracks two operations on different targets through out-of-order settle and unmount", async () => {
    let resolveA: (value: { output: string; exitCode: number }) => void = () => {};
    let resolveB: (value: { output: string; exitCode: number }) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveA = resolve; }));
    const m1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m1.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m1.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m1.container, "执行");
    await flush();
    act(() => m1.unmount());

    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveB = resolve; }));
    const otherTarget = { type: "context", name: "other", dockerEndpoint: "ssh://ops@other.example" };
    const m2 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: otherTarget, ownerKey: "owner-a" }));
    setInputValue(m2.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m2.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m2.container, "执行");
    await flush();
    act(() => m2.unmount());

    // B settles first, then A; both outcomes are retained per identity.
    await act(async () => { resolveB({ output: "B-OUT\n", exitCode: 0 }); });
    await flush();
    await act(async () => { resolveA({ output: "A-OUT\n", exitCode: 0 }); });
    await flush();

    // Remount in reverse order: each target replays only its own outcome.
    const m2b = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: otherTarget, ownerKey: "owner-a" }));
    mounted = m2b;
    await flush();
    expect(m2b.container.textContent).toContain("B-OUT");
    expect(m2b.container.textContent).not.toContain("A-OUT");
    act(() => m2b.unmount());
    const m1b = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    mounted = m1b;
    await flush();
    expect(m1b.container.textContent).toContain("A-OUT");
    expect(m1b.container.textContent).not.toContain("B-OUT");
  });

  it("replays a settled outcome to sequential remounts until cleared", async () => {
    let resolveA: (value: { output: string; exitCode: number }) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveA = resolve; }));
    const m1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m1.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m1.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m1.container, "执行");
    await flush();
    clickButton(m1.container, "忽略结果");
    await flush();
    act(() => m1.unmount());

    // A settles naturally with no mounted view; every remount replays the
    // done notice until a user explicitly clears it.
    await act(async () => { resolveA({ output: "ignored secret\n", exitCode: 0 }); });
    await flush();
    const view1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    await flush();
    expect(view1.container.textContent).toContain("命令已结束；结果已忽略");
    act(() => view1.unmount());

    const view2 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    mounted = view2;
    await flush();
    expect(view2.container.textContent).toContain("命令已结束；结果已忽略");
    expect(view2.container.textContent).not.toContain("ignored secret");
    const execButton = () => [...view2.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "执行");
    expect(execButton()?.disabled).toBe(true);

    clickButton(view2.container, "清除");
    await flush();
    expect(view2.container.textContent).not.toContain("命令已结束");
    expect(execButton()?.disabled).toBe(false);
    act(() => view2.unmount());

    const view3 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    mounted = view3;
    await flush();
    expect(view3.container.textContent).not.toContain("命令已结束");
  });

  it("keeps the done notice across a StrictMode remount", async () => {
    let resolveA: (value: { output: string; exitCode: number }) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveA = resolve; }));
    const m1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m1.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m1.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m1.container, "执行");
    await flush();
    clickButton(m1.container, "忽略结果");
    await flush();
    act(() => m1.unmount());

    // The StrictMode effect replay must not consume the retained outcome.
    await act(async () => { resolveA({ output: "ignored secret\n", exitCode: 0 }); });
    await flush();
    const m = mount(createElement(StrictMode, null, createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" })));
    mounted = m;
    await flush();
    expect(m.container.textContent).toContain("命令已结束；结果已忽略");
    const execButton = () => [...m.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "执行");
    expect(execButton()?.disabled).toBe(true);
  });

  it("does not revive a cleared outcome after switch-back", async () => {
    mocks.call.mockResolvedValue({ output: "CLEARED-SECRET\n", exitCode: 0 });
    const m = (mounted = render({ kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m.container, "执行");
    await flush();
    await waitFor(() => expect(m.container.textContent).toContain("CLEARED-SECRET"));

    clickButton(m.container, "清除");
    await flush();
    expect(m.container.textContent).not.toContain("CLEARED-SECRET");

    act(() => m.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-b" })));
    await flush();
    act(() => m.root.render(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" })));
    await flush();
    expect(m.container.textContent).not.toContain("CLEARED-SECRET");
    expect(m.container.querySelector('[role="status"]')).toBeNull();
  });

  it("notifies every view when the operation is ignored", async () => {
    let resolveA: (value: { output: string; exitCode: number }) => void = () => {};
    mocks.call.mockImplementationOnce(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveA = resolve; }));
    const m1 = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    setInputValue(m1.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(m1.container.querySelector('input[aria-label="容器"]')!, "web");
    clickButton(m1.container, "执行");
    await flush();
    act(() => m1.unmount());

    const viewA = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    const viewB = mount(createElement(InventoryTargetActions, { kind: "docker-context", target: dockerTarget, ownerKey: "owner-a" }));
    mounted = viewB;
    await flush();
    expect(viewA.container.textContent).toContain("执行中");
    expect(viewB.container.textContent).toContain("执行中");

    // Ignore is operation-level: both views show the ignored state and the
    // ignore entry is gone from both.
    clickButton(viewA.container, "忽略结果");
    await flush();
    const note = (container: HTMLElement) => container.querySelector('[role="note"]')?.textContent;
    expect(note(viewA.container)).toContain("命令仍在目标上执行");
    expect(note(viewB.container)).toContain("命令仍在目标上执行");
    const ignoreButton = (container: HTMLElement) => [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "忽略结果");
    expect(ignoreButton(viewA.container)).toBeUndefined();
    expect(ignoreButton(viewB.container)).toBeUndefined();

    // The settle is ignored for both views: done without output.
    await act(async () => { resolveA({ output: "IGNORED-SECRET\n", exitCode: 0 }); });
    await flush();
    expect(viewA.container.textContent).not.toContain("IGNORED-SECRET");
    expect(viewB.container.textContent).not.toContain("IGNORED-SECRET");
    expect(note(viewA.container)).toContain("命令已结束");
    expect(note(viewB.container)).toContain("命令已结束");
  });
});
