import { useEffect, useRef, useState } from "react";
import { ProductionConfirmationDialog } from "../execbatch/ProductionConfirmationDialog";
import { describeError } from "../../ui/errorText";
import { dockerConfirmation, getDockerConfirmationChallenge, type DockerConfirmationChallenge } from "../docker/productionGuard";
import { inventoryExecCancelled, inventoryExecFailure, inventoryTargetApi } from "./inventoryTargetApi";
import type { DockerProductionConfirmation } from "../../ipc/commands";
import type { InventoryTargetExecResponse } from "../../ipc/generated/types";

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
  const [ignoredNotice, setIgnoredNotice] = useState<"running" | "done" | null>(null);
  const [cancelled, setCancelled] = useState(false);
  const generationRef = useRef(0);
  const ignoredGenRef = useRef<number | null>(null);
  const operationIdRef = useRef<string>("");

  useEffect(() => {
    generationRef.current++;
    ignoredGenRef.current = null;
    setBusy(false);
    setResult(null);
    setFailure(null);
    setProduction(null);
    setIgnoredNotice(null);
    setCancelled(false);
    setContainer(targetRef.current.type === "pod" ? stringField(targetRef.current, "container") : "");
    setSessionId("");
  }, [ownerKey, identity]);

  const run = async (confirmation?: DockerProductionConfirmation) => {
    // An ignored command is still running on the target; starting another
    // one now would repeat side effects and drop its settle tracking. The
    // tracked ignored generation is the authority, not the notice text.
    if (busy || ignoredGenRef.current !== null) return;
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
    ignoredGenRef.current = null;
    setIgnoredNotice(null);
    setCancelled(false);
    setBusy(true);
    setResult(null);
    setFailure(null);
    // Each launch freezes a fresh, caller-generated operation identity;
    // retry gets a new one, and cancel addresses exactly this instance.
    const operationId = globalThis.crypto.randomUUID();
    operationIdRef.current = operationId;
    try {
      const response = await inventoryTargetApi.execTarget({
        kind,
        target,
        action: "exec",
        container: resolvedContainer === "" ? undefined : resolvedContainer,
        command: argv,
        sessionId: sessionId === "" ? undefined : sessionId,
        operationId,
        confirmation,
      });
      if (generation !== generationRef.current) {
        if (generation === ignoredGenRef.current) setIgnoredNotice("done");
        return;
      }
      setResult(response);
    } catch (cause) {
      if (generation !== generationRef.current) {
        if (generation === ignoredGenRef.current) setIgnoredNotice("done");
        return;
      }
      if (inventoryExecCancelled(cause)) {
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
        setFailure({
          message: `命令已执行但返回非零退出码（${exitFailure.exitCode}）`,
          exitCode: exitFailure.exitCode,
          output: exitFailure.output,
        });
        return;
      }
      setFailure({ message: describeError(cause) });
    } finally {
      if (generation === generationRef.current) setBusy(false);
    }
  };

  // The typed IPC has no abort handle, so an in-flight command cannot be
  // cancelled here; ignoring only discards its result while the command
  // keeps running on the target.
  const ignoreResults = () => {
    ignoredGenRef.current = generationRef.current;
    generationRef.current++;
    setIgnoredNotice("running");
    setBusy(false);
    setResult(null);
    setFailure(null);
  };

  // Real cancellation: the backend kills the in-flight command, so retry
  // is safe once the cancelled contract settles. This is distinct from
  // ignoring a result, which leaves the command running. The cancel
  // addresses the frozen launch identity, not editable current input.
  const cancelExecution = async () => {
    if (!busy) return;
    const argv = command.trim() ? command.trim().split(/\s+/) : [];
    try {
      await inventoryTargetApi.cancelExec({
        kind,
        target,
        action: "cancel",
        container: container.trim() === "" ? undefined : container.trim(),
        command: argv,
        sessionId: sessionId === "" ? undefined : sessionId,
        operationId: operationIdRef.current,
      });
    } catch {
      // the exec request itself reports the cancellation outcome
    }
  };

  const clearSettled = () => {
    ignoredGenRef.current = null;
    setIgnoredNotice(null);
    setResult(null);
    setFailure(null);
    setProduction(null);
    setCancelled(false);
  };

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
            <button type="button" className="nx-btn nx-btn-primary nx-btn-xs" disabled={busy || ignoredGenRef.current !== null} onClick={() => void run()}>
              执行
            </button>
            {busy && (
              <>
                <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" onClick={() => void cancelExecution()}>
                  取消执行
                </button>
                <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" onClick={ignoreResults}>
                  忽略结果
                </button>
              </>
            )}
            {(result || failure || cancelled || ignoredNotice === "done") && (
              <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" onClick={clearSettled}>
                清除
              </button>
            )}
          </div>
          {busy && <div className="text-[11px] text-neutral-400">执行中…</div>}
          {cancelled && (
            <div role="note" className="text-[11px] text-neutral-300">执行已取消</div>
          )}
          {ignoredNotice && (
            <div role="note" className="text-[11px] text-amber-300">
              {ignoredNotice === "running"
                ? "命令仍在目标上执行；结果已忽略，不代表命令已停止"
                : "命令已结束；结果已忽略"}
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
