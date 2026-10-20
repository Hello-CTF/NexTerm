import { useEffect, useRef, useState } from "react";
import { ProductionConfirmationDialog } from "../execbatch/ProductionConfirmationDialog";
import { describeError } from "../../ui/errorText";
import { dockerConfirmation, getDockerConfirmationChallenge, type DockerConfirmationChallenge } from "../docker/productionGuard";
import { inventoryExecCancelled, inventoryExecFailure, inventoryTargetApi } from "./inventoryTargetApi";
import type { DockerProductionConfirmation } from "../../ipc/commands";
import type { InventoryTargetExecRequest } from "../../ipc/generated/types";
import type { LiveOperation } from "./inventoryLiveOperationStore";
import { liveOperationStore } from "./inventoryLiveOperationStore";

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

// ResetLiveOperationRegistry clears the operation store; it exists for
// tests so each case starts without another case's live operation.
export function ResetLiveOperationRegistry() {
  liveOperationStore.reset();
}

// SetCancelTimeoutForTest overrides the cancel timeout; it exists for tests
// so they do not wait the full production timeout.
export function SetCancelTimeoutForTest(ms: number) {
  cancelTimeoutMs = ms;
}

let cancelTimeoutMs = 10000;

export function InventoryTargetActions({ kind, target, ownerKey, sessions = [], requireSession = false }: InventoryTargetActionsProps) {
  const executable = targetExecutable(kind, target);
  const isPod = kind === "kubernetes" && target.type === "pod";
  const containers = isPod && Array.isArray(target.containers)
    ? target.containers.filter((item): item is string => typeof item === "string")
    : [];
  const identity = targetIdentity(kind, target);
  const targetRef = useRef(target);
  targetRef.current = target;
  const identityRef = useRef(identity);
  identityRef.current = identity;
  const ownerRef = useRef(ownerKey);
  ownerRef.current = ownerKey;
  const [command, setCommand] = useState("");
  const [container, setContainer] = useState(isPod ? stringField(target, "container") : "");
  const [sessionId, setSessionId] = useState("");
  const [busy, setBusy] = useState(false);
  const [inputError, setInputError] = useState<string | null>(null);
  const [production, setProduction] = useState<{ challenge: DockerConfirmationChallenge; command: string; snapshot: InventoryTargetExecRequest; generation: number } | null>(null);
  const generationRef = useRef(0);
  // The live operation lives in a ref so its identity stays stable across
  // renders, mutations, and the store; a version counter drives renders.
  const liveOpRef = useRef<LiveOperation | null>(null);
  const [, setLiveOpVersion] = useState(0);
  const bumpLiveOp = () => setLiveOpVersion((version) => version + 1);

  // The outcome display is derived from the store's retained outcome for
  // the current owner and target, so it can never leak across contexts and
  // every view of the same owner and target stays consistent. An ignored
  // operation shows only its done notice, never its output.
  const outcome = liveOperationStore.getOutcome(ownerKey, identity);
  const derivedOutcomeRef = useRef<LiveOperation | null>(null);
  derivedOutcomeRef.current = outcome;
  const ignoredDone = outcome !== null && outcome.status === "ignored";
  const result = !ignoredDone && outcome?.outcome === "result" ? outcome.result ?? null : null;
  const failure = !ignoredDone && outcome?.outcome === "failure" ? outcome.failure ?? null : null;
  const cancelled = !ignoredDone && outcome?.outcome === "cancelled";

  // On mount and on owner/target switch, reset the target-scoped form
  // state and restore the owner+target's single unsettled operation, which
  // the store guarantees is unique per identity. A tracked operation stays
  // tracked across switches with its cancel control.
  useEffect(() => {
    generationRef.current++;
    setInputError(null);
    setProduction(null);
    setContainer(targetRef.current.type === "pod" ? stringField(targetRef.current, "container") : "");
    setSessionId("");
    const unsettled = liveOperationStore.getUnsettled(ownerKey, identity);
    if (unsettled !== null && unsettled !== liveOpRef.current) {
      liveOpRef.current = unsettled;
      bumpLiveOp();
    }
  }, [ownerKey, identity]);

  // Subscribe to the tracked operation's lifecycle: settle and clear end
  // its tracking, cancel and ignore re-render the shared status. The
  // subscribe read is atomic with the registration, so an operation that
  // settled or was removed between the restore scan and this subscription
  // is reconciled on the spot instead of leaving a stuck running notice.
  useEffect(() => {
    const op = liveOpRef.current;
    if (op === null) return;
    const { operation, unsubscribe } = liveOperationStore.subscribeOp(op.ownerKey, op.snapshot.operationId, (event) => {
      if (event.type === "settle" || event.type === "clear") {
        if (liveOpRef.current === event.operation) liveOpRef.current = null;
      }
      bumpLiveOp();
    });
    if (liveOpRef.current === op && (operation === null || operation.settled)) {
      liveOpRef.current = null;
      bumpLiveOp();
    }
    return () => {
      unsubscribe();
    };
  }, [liveOpRef.current?.snapshot.operationId, ownerKey, identity]);

  // Subscribe to the owner+target's outcome channel: a settle or clear
  // anywhere re-renders the derived outcome. The atomic read reconciles an
  // outcome that settled or was cleared between the render and this
  // subscription.
  useEffect(() => {
    const { outcome: current, unsubscribe } = liveOperationStore.subscribeOutcome(ownerKey, identity, () => {
      bumpLiveOp();
    });
    if (derivedOutcomeRef.current !== current) {
      bumpLiveOp();
    }
    return () => {
      unsubscribe();
    };
  }, [ownerKey, identity]);

  // launch freezes the given snapshot as a live operation bound to the
  // current owner and target and runs it. The store rejects a replacement
  // for an identity that already has an unsettled operation, and rejects
  // at capacity, both visibly before any call is made.
  const launch = async (snapshot: InventoryTargetExecRequest) => {
    if (busy || liveOpRef.current !== null || ignoredDone) return;
    const generation = generationRef.current;
    setBusy(true);
    setInputError(null);
    const op: LiveOperation = { snapshot, ownerKey, identity, status: "running" };
    const storeError = liveOperationStore.store(op);
    if (storeError !== null) {
      setInputError(storeError.message);
      setBusy(false);
      return;
    }
    liveOpRef.current = op;
    bumpLiveOp();
    try {
      const response = await inventoryTargetApi.execTarget(snapshot);
      op.result = response;
      op.outcome = "result";
    } catch (cause) {
      if (inventoryExecCancelled(cause)) {
        op.outcome = "cancelled";
        return;
      }
      const challenge = getDockerConfirmationChallenge(cause);
      if (challenge) {
        // The challenge ends this operation without an outcome; remove its
        // registry entry so no stale record is kept. A challenge that
        // arrives after the launch generation, owner, or target moved on
        // only ends the original operation and never opens the dialog in
        // the new context.
        liveOperationStore.remove(op.ownerKey, op.snapshot.operationId);
        if (generation === generationRef.current && ownerRef.current === op.ownerKey && identityRef.current === op.identity) {
          setProduction({ challenge, command: op.snapshot.command.join(" "), snapshot: op.snapshot, generation });
        }
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
        return;
      }
      op.outcome = "failure";
      op.failure = { message: describeError(cause) };
    } finally {
      // The operation's lifecycle ends only when the exec itself settles;
      // until then its snapshot and cancel control are retained. Settling
      // notifies every subscriber, and the registry keeps the settled
      // outcome until a user explicitly clears it, so no outcome is lost
      // between an unmount and a remount.
      liveOperationStore.markSettled(op);
      if (liveOpRef.current === op) liveOpRef.current = null;
      setBusy(false);
    }
  };

  const run = async (confirmation?: DockerProductionConfirmation) => {
    // A live operation blocks a replacement until it settles; an ignored
    // operation that already settled blocks until its done notice is
    // cleared, so a retry is never blind.
    if (busy || liveOpRef.current !== null || ignoredDone) return;
    const argv = command.trim() ? command.trim().split(/\s+/) : [];
    if (argv.length === 0) {
      setInputError("请输入要执行的命令");
      return;
    }
    const resolvedContainer = container.trim();
    if (kind === "docker-context" && resolvedContainer === "") {
      setInputError("请输入容器名");
      return;
    }
    if (requireSession && sessionId === "") {
      setInputError("请选择受管会话");
      return;
    }
    // Each launch freezes an immutable snapshot of the full request
    // identity bound to the launching owner and target; cancel addresses
    // exactly that instance.
    await launch({
      kind,
      target,
      action: "exec",
      container: resolvedContainer === "" ? undefined : resolvedContainer,
      command: argv,
      sessionId: sessionId === "" ? undefined : sessionId,
      operationId: globalThis.crypto.randomUUID(),
      confirmation,
    });
  };

  // Ignoring discards the response while the command keeps running on the
  // target; the operation stays live and tracked until it settles. Ignore
  // is an operation-level behavior, so every subscribed view is notified
  // and shows the same ignored state.
  const ignoreResults = () => {
    const op = liveOpRef.current;
    if (op === null || op.status !== "running") return;
    generationRef.current++;
    op.status = "ignored";
    liveOperationStore.notifyIgnore(op);
    setBusy(false);
    bumpLiveOp();
  };

  // Real cancellation: the backend kills the in-flight command, so retry
  // is safe once the cancelled contract settles. This is distinct from
  // ignoring a result, which leaves the command running. The cancel reads
  // only the frozen launch snapshot, and only under the launching owner.
  // A permanently pending cancel times out into a retryable cancel-failed;
  // late settles are handled by the operation-level cancel sequence.
  const cancelExecution = async () => {
    const op = liveOpRef.current;
    if (op === null || op.ownerKey !== ownerKey || op.status === "cancel-pending") return;
    // The cancel sequence belongs to the operation, not the component
    // instance, so a late reject of an earlier cancel never clobbers the
    // state of a retry issued from another view.
    const seq = (op.cancelSeq ?? 0) + 1;
    op.cancelSeq = seq;
    op.status = "cancel-pending";
    op.cancelError = undefined;
    liveOperationStore.notifyCancel(op);
    try {
      await Promise.race([
        inventoryTargetApi.cancelExec(op.snapshot),
        new Promise((_, reject) => setTimeout(() => reject(new Error("取消请求超时，结果未知")), cancelTimeoutMs)),
      ]);
    } catch (cause) {
      if (op.cancelSeq !== seq) return;
      op.status = "cancel-failed";
      op.cancelError = describeError(cause);
      liveOperationStore.notifyCancel(op);
    }
  };

  // Clearing dismisses the dialog and any pre-IPC error and clears the
  // retained outcome through the store, so every view of the owner and
  // target drops it together and it does not revive on a later remount.
  const clearSettled = () => {
    setProduction(null);
    setInputError(null);
    if (outcome !== null) {
      liveOperationStore.remove(outcome.ownerKey, outcome.snapshot.operationId);
    }
  };

  const liveOp = liveOpRef.current;
  // Cancel control and the running notice derive from the live operation's
  // status, not from the busy display flag, so a restored operation always
  // has the right entries.
  const isRunning = liveOp !== null && !liveOp.settled && liveOp.status === "running";
  const isIgnore = liveOp !== null && !liveOp.settled && liveOp.status === "ignored";
  const isCancelPending = liveOp !== null && !liveOp.settled && liveOp.status === "cancel-pending";
  const isCancelFailed = liveOp !== null && !liveOp.settled && liveOp.status === "cancel-failed";
  const canCancel = liveOp !== null && !liveOp.settled && liveOp.ownerKey === ownerKey && !isCancelPending;

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
            <button type="button" className="nx-btn nx-btn-primary nx-btn-xs" disabled={liveOp !== null || ignoredDone} onClick={() => void run()}>
              执行
            </button>
            {(isRunning || isIgnore || isCancelFailed) && canCancel && (
              <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" onClick={() => void cancelExecution()}>
                {isCancelFailed ? "重试取消" : "取消执行"}
              </button>
            )}
            {isRunning && (
              <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" onClick={ignoreResults}>
                忽略结果
              </button>
            )}
            {(result || failure || cancelled || ignoredDone || inputError) && (
              <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" onClick={clearSettled}>
                清除
              </button>
            )}
          </div>
          {(isRunning || isCancelPending || isCancelFailed) && <div className="text-[11px] text-neutral-400">执行中…</div>}
          {liveOp !== null && liveOp.ownerKey !== ownerKey && (
            <div role="note" className="text-[11px] text-amber-300">
              操作仍在目标上执行；切换到 {liveOp.ownerKey} 后可取消
            </div>
          )}
          {isIgnore && (
            <div role="note" className="text-[11px] text-amber-300">
              命令仍在目标上执行；结果已忽略，不代表命令已停止
            </div>
          )}
          {isCancelFailed && (
            <div role="note" className="text-[11px] text-amber-300">
              取消失败：{liveOp?.cancelError}；命令仍在目标上执行
            </div>
          )}
          {isCancelPending && (
            <div role="note" className="text-[11px] text-amber-300">
              命令仍在目标上执行<span>取消中…</span>
            </div>
          )}
          {cancelled && (
            <div role="note" className="text-[11px] text-neutral-300">执行已取消</div>
          )}
          {ignoredDone && (
            <div role="note" className="text-[11px] text-neutral-300">命令已结束；结果已忽略</div>
          )}
          {result && (
            <div role="status" className="text-[11px] text-neutral-300">
              <div>退出码 {result.exitCode}</div>
              <pre className="nx-pre max-h-56 overflow-auto whitespace-pre-wrap break-all text-[11px]">{result.output}</pre>
            </div>
          )}
          {inputError !== null && (
            <div role="alert" className="text-[11px] text-red-300">{inputError}</div>
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
            if (current === null || current.generation !== generationRef.current) return;
            // The confirmation is bound to the original launch snapshot:
            // the retry re-issues exactly that request with a fresh
            // operationId, never the page's current inputs.
            void launch({
              ...current.snapshot,
              operationId: globalThis.crypto.randomUUID(),
              confirmation: dockerConfirmation(current.challenge),
            });
          }}
        />
      )}
    </div>
  );
}
