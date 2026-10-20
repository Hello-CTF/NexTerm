import { useEffect, useRef, useState } from "react";
import { ProductionConfirmationDialog } from "../execbatch/ProductionConfirmationDialog";
import { describeError } from "../../ui/errorText";
import { dockerConfirmation, getDockerConfirmationChallenge, type DockerConfirmationChallenge } from "../docker/productionGuard";
import { inventoryExecCancelled, inventoryExecFailure, inventoryTargetApi } from "./inventoryTargetApi";
import type { DockerProductionConfirmation } from "../../ipc/commands";
import type { InventoryTargetExecRequest, InventoryTargetExecResponse } from "../../ipc/generated/types";

export interface InventoryTargetSession {
  id: string;
  name: string;
  assetId?: string | null;
}

export interface InventoryTargetActionsProps {
  kind: string;
  target: Record<string, unknown>;
  ownerKey: string;
  sessions?: InventoryTargetSession[];
  requireSession?: boolean;
}

function stringField(target: Record<string, unknown>, key: string): string {
  const value = target[key];
  return typeof value === "string" ? value : "";
}

function targetText(kind: string, target: Record<string, unknown>): string {
  if (kind === "docker-context") {
    const name = stringField(target, "name");
    return name ? `context/${name}` : "docker context";
  }
  if (kind === "kubernetes") {
    if (target.type === "pod") {
      return `${stringField(target, "namespace")}/${stringField(target, "pod")}`;
    }
    const name = stringField(target, "name");
    return name ? `context/${name}` : "kubernetes context";
  }
  return kind;
}

export function targetExecutable(kind: string, target: Record<string, unknown>): boolean {
  return kind === "docker-context" || (kind === "kubernetes" && target.type === "pod");
}

function targetIdentity(kind: string, target: Record<string, unknown>): string {
  return [
    kind,
    String(target.type ?? ""),
    stringField(target, "name"),
    stringField(target, "context"),
    stringField(target, "namespace"),
    stringField(target, "pod"),
  ].join("/");
}

// LiveOperation is the in-flight lifecycle of one exec, tracked
// independently of the busy/ignored display state. The snapshot is bound to
// the owner and target it was launched with, so cancel only ever addresses
// that exact instance under that owner.
interface LiveOperation {
  snapshot: InventoryTargetExecRequest;
  ownerKey: string;
  identity: string;
  status: "running" | "ignored" | "cancel-pending" | "cancel-failed";
  cancelError?: string;
  settled?: boolean;
  outcome?: "cancelled" | "result" | "failure";
  result?: InventoryTargetExecResponse;
  failure?: { message: string; exitCode?: number; output?: string };
  onSettle?: () => void;
}

// The registry keeps live operations visible across unmounts in an
// owner-isolated, bounded way: a remount with the same owner restores the
// operation and its cancel control, while the exec's own settle clears it.
const LIVE_REGISTRY_LIMIT = 32;
const liveOperationRegistry = new Map<string, LiveOperation>();

function registryStore(op: LiveOperation) {
  liveOperationRegistry.set(op.ownerKey, op);
  if (liveOperationRegistry.size > LIVE_REGISTRY_LIMIT) {
    const oldest = liveOperationRegistry.keys().next().value;
    if (oldest !== undefined) liveOperationRegistry.delete(oldest);
  }
}

function registryDelete(ownerKey: string) {
  liveOperationRegistry.delete(ownerKey);
}

function registryGet(ownerKey: string): LiveOperation | null {
  return liveOperationRegistry.get(ownerKey) ?? null;
}

// ResetLiveOperationRegistry clears the cross-unmount registry; it exists
// for tests so each case starts without another case's live operation.
export function ResetLiveOperationRegistry() {
  liveOperationRegistry.clear();
}

export function InventoryTargetActions({ kind, target, ownerKey, sessions = [], requireSession = false }: InventoryTargetActionsProps) {
  const executable = targetExecutable(kind, target);
  const isPod = kind === "kubernetes" && target.type === "pod";
  const containers = isPod && Array.isArray(target.containers)
    ? target.containers.filter((item): item is string => typeof item === "string")
    : [];
  const identity = targetIdentity(kind, target);
  const targetRef = useRef(target);
  targetRef.current = target;
  const [command, setCommand] = useState("");
  const [container, setContainer] = useState(isPod ? stringField(target, "container") : "");
  const [sessionId, setSessionId] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<InventoryTargetExecResponse | null>(null);
  const [failure, setFailure] = useState<{ message: string; exitCode?: number; output?: string } | null>(null);
  const [production, setProduction] = useState<{ challenge: DockerConfirmationChallenge; command: string } | null>(null);
  const [cancelled, setCancelled] = useState(false);
  const [ignoredDone, setIgnoredDone] = useState(false);
  const generationRef = useRef(0);
  // The live operation lives in a ref so its identity stays stable across
  // renders, mutations, and the registry; a version counter drives renders.
  const liveOpRef = useRef<LiveOperation | null>(null);
  const [, setLiveOpVersion] = useState(0);
  const bumpLiveOp = () => setLiveOpVersion((version) => version + 1);

  useEffect(() => {
    generationRef.current++;
    // A remount with the same owner restores the live operation and its
    // cancel control; a different owner never sees another owner's op.
    if (liveOpRef.current === null) {
      const restored = registryGet(ownerKey);
      if (restored !== null && !restored.settled) {
        restored.onSettle = () => {
          liveOpRef.current = null;
          if (restored.outcome === "cancelled") setCancelled(true);
          else if (restored.outcome === "result" && restored.result) setResult(restored.result);
          else if (restored.outcome === "failure" && restored.failure) setFailure(restored.failure);
          bumpLiveOp();
        };
        liveOpRef.current = restored;
        bumpLiveOp();
      }
    }
    // Reset target-scoped form state for the new identity/owner, but keep
    // the live operation tracked with its cancel control.
    setContainer(targetRef.current.type === "pod" ? stringField(targetRef.current, "container") : "");
    setSessionId("");
  }, [ownerKey, identity]);

  // Unmount keeps the live operation in the owner-isolated registry so a
  // remount can see it and retry the cancel; the exec's own settle clears
  // the registry entry.
  useEffect(() => {
    return () => {
      const op = liveOpRef.current;
      if (op !== null && !op.settled) {
        registryStore(op);
      }
    };
  }, []);

  const run = async (confirmation?: DockerProductionConfirmation) => {
    // A live operation blocks a replacement until it settles; an ignored
    // operation that already settled blocks until its done notice is
    // cleared, so a retry is never blind.
    if (busy || liveOpRef.current !== null || ignoredDone) return;
    const argv = command.trim() ? command.trim().split(/\s+/) : [];
    if (argv.length === 0) {
      setFailure({ message: "请输入要执行的命令" });
      return;
    }
    const resolvedContainer = container.trim();
    if (kind === "docker-context" && resolvedContainer === "") {
      setFailure({ message: "请输入容器名" });
      return;
    }
    if (requireSession && sessionId === "") {
      setFailure({ message: "请选择受管会话" });
      return;
    }
    const generation = generationRef.current;
    setCancelled(false);
    setIgnoredDone(false);
    setBusy(true);
    setResult(null);
    setFailure(null);
    // Each launch freezes an immutable snapshot of the full request
    // identity bound to the launching owner and target; cancel addresses
    // exactly that instance.
    const operationId = globalThis.crypto.randomUUID();
    const snapshot: InventoryTargetExecRequest = {
      kind,
      target,
      action: "exec",
      container: resolvedContainer === "" ? undefined : resolvedContainer,
      command: argv,
      sessionId: sessionId === "" ? undefined : sessionId,
      operationId,
      confirmation,
    };
    const op: LiveOperation = { snapshot, ownerKey, identity, status: "running" };
    liveOpRef.current = op;
    registryStore(op);
    bumpLiveOp();
    try {
      const response = await inventoryTargetApi.execTarget(snapshot);
      op.result = response;
      op.outcome = "result";
      if (generation !== generationRef.current) return;
      setResult(response);
    } catch (cause) {
      if (inventoryExecCancelled(cause)) {
        op.outcome = "cancelled";
        if (generation !== generationRef.current) return;
        setCancelled(true);
        return;
      }
      const challenge = getDockerConfirmationChallenge(cause);
      if (challenge) {
        setProduction({ challenge, command: command.trim() });
        return;
      }
      const exitFailure = inventoryExecFailure(cause);
      if (exitFailure) {
        op.outcome = "failure";
        op.failure = {
          message: `命令已执行但返回非零退出码（${exitFailure.exitCode}）`,
          exitCode: exitFailure.exitCode,
          output: exitFailure.output,
        };
        if (generation !== generationRef.current) return;
        setFailure(op.failure);
        return;
      }
      op.outcome = "failure";
      op.failure = { message: describeError(cause) };
      if (generation !== generationRef.current) return;
      setFailure(op.failure);
    } finally {
      // The operation's lifecycle ends only when the exec itself settles;
      // until then its snapshot and cancel control are retained. The busy
      // display clears on settle regardless of generation; only the
      // outcome display is generation-gated.
      if (liveOpRef.current === op) {
        op.settled = true;
        liveOpRef.current = null;
        registryDelete(op.ownerKey);
        if (op.status === "ignored") setIgnoredDone(true);
        bumpLiveOp();
      }
      op.onSettle?.();
      setBusy(false);
    }
  };

  // Ignoring discards the response while the command keeps running on the
  // target; the operation stays live and tracked until it settles.
  const ignoreResults = () => {
    const op = liveOpRef.current;
    if (op === null || op.status !== "running") return;
    generationRef.current++;
    op.status = "ignored";
    setBusy(false);
    setResult(null);
    setFailure(null);
    bumpLiveOp();
  };

  // Real cancellation: the backend kills the in-flight command, so retry
  // is safe once the cancelled contract settles. This is distinct from
  // ignoring a result, which leaves the command running. The cancel reads
  // only the frozen launch snapshot, and only under the launching owner.
  const cancelExecution = async () => {
    const op = liveOpRef.current;
    if (op === null || op.ownerKey !== ownerKey || op.status === "cancel-pending") return;
    op.status = "cancel-pending";
    op.cancelError = undefined;
    bumpLiveOp();
    try {
      await inventoryTargetApi.cancelExec(op.snapshot);
      // The original exec settles as cancelled; its finally clears the op.
    } catch (cause) {
      op.status = "cancel-failed";
      op.cancelError = describeError(cause);
      bumpLiveOp();
    }
  };

  const clearSettled = () => {
    setResult(null);
    setFailure(null);
    setProduction(null);
    setCancelled(false);
    setIgnoredDone(false);
  };

  const liveOp = liveOpRef.current;

  return (
    <div className="flex flex-col gap-2">
      <div className="text-[11px] text-neutral-400">
        目标 <span className="nx-badge nx-badge-purple">{targetText(kind, target)}</span>
      </div>
      {!executable && (
        <div className="text-[11px] text-amber-300">该目标不支持 exec</div>
      )}
      {executable && (
        <>
          <label className="flex items-center gap-2 text-[11px] text-neutral-400">
            命令
            <input
              aria-label="命令"
              className="nx-input flex-1"
              value={command}
              onChange={(event) => setCommand(event.target.value)}
              placeholder="例如 uname -a"
            />
          </label>
          {isPod && containers.length > 0 && (
            <label className="flex items-center gap-2 text-[11px] text-neutral-400">
              容器
              <select
                aria-label="容器"
                className="nx-input flex-1"
                value={container}
                onChange={(event) => setContainer(event.target.value)}
              >
                {containers.map((name) => (
                  <option key={name} value={name}>{name}</option>
                ))}
              </select>
            </label>
          )}
          {kind === "docker-context" && (
            <label className="flex items-center gap-2 text-[11px] text-neutral-400">
              容器
              <input
                aria-label="容器"
                className="nx-input flex-1"
                value={container}
                onChange={(event) => setContainer(event.target.value)}
                placeholder="容器名或 ID"
              />
            </label>
          )}
          {requireSession && (
            <label className="flex items-center gap-2 text-[11px] text-neutral-400">
              受管会话
              <select
                aria-label="受管会话"
                className="nx-input flex-1"
                value={sessionId}
                onChange={(event) => setSessionId(event.target.value)}
              >
                <option value="">选择会话</option>
                {sessions.map((session) => (
                  <option key={session.id} value={session.id}>{session.name}</option>
                ))}
              </select>
            </label>
          )}
          <div className="flex gap-2">
            <button type="button" className="nx-btn nx-btn-primary nx-btn-xs" disabled={busy || liveOp !== null || ignoredDone} onClick={() => void run()}>
              执行
            </button>
            {busy && liveOp !== null && liveOp.status === "running" && (
              <>
                <button
                  type="button"
                  className="nx-btn nx-btn-ghost nx-btn-xs"
                  disabled={liveOp.ownerKey !== ownerKey}
                  onClick={() => void cancelExecution()}
                >
                  取消执行
                </button>
                <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" onClick={ignoreResults}>
                  忽略结果
                </button>
              </>
            )}
            {(result || failure || cancelled || ignoredDone) && (
              <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" onClick={clearSettled}>
                清除
              </button>
            )}
          </div>
          {busy && <div className="text-[11px] text-neutral-400">执行中…</div>}
          {busy && liveOp !== null && liveOp.ownerKey !== ownerKey && (
            <div role="note" className="text-[11px] text-amber-300">
              操作仍在目标上执行；切换到 {liveOp.ownerKey} 后可取消
            </div>
          )}
          {cancelled && (
            <div role="note" className="text-[11px] text-neutral-300">执行已取消</div>
          )}
          {ignoredDone && (
            <div role="note" className="text-[11px] text-neutral-300">命令已结束；结果已忽略</div>
          )}
          {liveOp !== null && !liveOp.settled && liveOp.status !== "running" && (
            <div role="note" className="text-[11px] text-amber-300">
              {liveOp.ownerKey !== ownerKey
                ? `操作仍在目标上执行；切换到 ${liveOp.ownerKey} 后可取消`
                : liveOp.status === "ignored"
                  ? "命令仍在目标上执行；结果已忽略，不代表命令已停止"
                  : liveOp.status === "cancel-failed"
                    ? `取消失败：${liveOp.cancelError}；命令仍在目标上执行`
                    : "命令仍在目标上执行"}
              {liveOp.ownerKey === ownerKey && liveOp.status !== "cancel-pending" && (
                <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" onClick={() => void cancelExecution()}>
                  {liveOp.status === "cancel-failed" ? "重试取消" : "取消执行"}
                </button>
              )}
              {liveOp.status === "cancel-pending" && <span>取消中…</span>}
            </div>
          )}
          {result && (
            <div role="status" className="text-[11px] text-neutral-300">
              <div>退出码 {result.exitCode}</div>
              <pre className="nx-pre max-h-56 overflow-auto whitespace-pre-wrap break-all text-[11px]">{result.output}</pre>
            </div>
          )}
          {failure && (
            <div role="alert" className="text-[11px] text-red-300">
              <div>{failure.message}</div>
              {failure.exitCode !== undefined && <div>退出码 {failure.exitCode}</div>}
              {failure.output !== undefined && (
                <pre className="nx-pre max-h-56 overflow-auto whitespace-pre-wrap break-all text-[11px]">{failure.output}</pre>
              )}
            </div>
          )}
        </>
      )}
      {production && (
        <ProductionConfirmationDialog
          challenge={production.challenge}
          contentHash={production.challenge.commandHash}
          command={production.command}
          typedPhrase="production"
          onCancel={() => setProduction(null)}
          onConfirm={() => {
            const current = production;
            setProduction(null);
            void run(dockerConfirmation(current.challenge));
          }}
        />
      )}
    </div>
  );
}
