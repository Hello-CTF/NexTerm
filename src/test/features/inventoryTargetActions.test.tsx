/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { clickButton, flush, mount, setInputValue, setSelectValue, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  call: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, call: mocks.call };
});

import { InventoryTargetActions, ResetLiveOperationRegistry } from "../../features/explorer/InventoryTargetActions";

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

function challenge() {
  return {
    code: "needs_confirm",
    detail: {
      kind: "docker_production_confirmation",
      targetSetHash: "a".repeat(64),
      commandHash: "b".repeat(64),
      productionTargets: [{ sessionId: "s1", sessionName: "production", environment: "production" }],
    },
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  ResetLiveOperationRegistry();
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
});
