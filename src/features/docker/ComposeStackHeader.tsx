import { useEffect, useRef, useState } from "react";
import { ask } from "../../ui/dialogs";
import { dockerApi, type ComposeStack, type DockerProductionConfirmation } from "../../ipc/commands";
import { createBinaryChannel, disposeChannel } from "../../ipc/events";
import { terminalApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import { IconDownload, IconEdit, IconList, IconPlay, IconRefresh, IconRestart, IconStop } from "../../ui/icons";
import { ContextMenu, type ContextMenuState } from "../../ui/ContextMenu";
import { ProductionConfirmationDialog } from "../execbatch/ProductionConfirmationDialog";
import { dockerConfirmation, getDockerConfirmationChallenge, type DockerConfirmationChallenge } from "./productionGuard";
import { DockerStreamView, type DockerOutputStream } from "./DockerStreamView";
import { ComposeEditor } from "./ComposeEditor";

type ComposeAction = "up" | "down" | "restart" | "pull";

type PendingProduction = {
  challenge: DockerConfirmationChallenge;
  command: string;
  retry: (confirmation: DockerProductionConfirmation) => Promise<void>;
};

export function ComposeStackHeader({
  sessionId,
  project,
  stack,
  onChanged,
  compact = false,
}: {
  sessionId: string;
  project: string;
  stack: ComposeStack | null;
  onChanged: () => void;
  compact?: boolean;
}) {
  const pushToast = useUi((state) => state.pushToast);
  const [busy, setBusy] = useState<ComposeAction | "logs" | null>(null);
  const [output, setOutput] = useState<string | null>(null);
  const [stream, setStream] = useState<DockerOutputStream | null>(null);
  const [production, setProduction] = useState<PendingProduction | null>(null);
  const [editing, setEditing] = useState(false);
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  const attachGeneration = useRef(0);
  const actionInFlight = useRef(false);
  const effective: ComposeStack = stack ?? {
    project,
    workingDir: "",
    configFiles: [],
    envPath: "",
    envExists: false,
    services: [],
    readOnly: true,
    reason: "Compose 元数据尚未加载或读取失败",
  };
  const unavailable = effective.readOnly || busy !== null;

  useEffect(() => () => {
    attachGeneration.current++;
  }, []);

  const execute = async (action: ComposeAction, confirmation?: DockerProductionConfirmation) => {
    if (actionInFlight.current) return;
    actionInFlight.current = true;
    setBusy(action);
    setOutput(null);
    try {
      const result = await dockerApi.composeAction(sessionId, project, action, confirmation);
      setOutput(result || "命令已完成，无输出。");
      pushToast("success", `${project} · ${action} 已完成`);
      onChanged();
    } catch (cause) {
      const challenge = getDockerConfirmationChallenge(cause);
      if (challenge) {
        setProduction({
          challenge,
          command: `docker compose ${action} · ${project}`,
          retry: (nextConfirmation) => execute(action, nextConfirmation),
        });
      } else {
        pushToast("error", `${project} · ${action} 失败：${describeError(cause)}`);
      }
    } finally {
      actionInFlight.current = false;
      setBusy(null);
    }
  };

  const startAction = async (action: ComposeAction) => {
    if (unavailable) return;
    if (action === "down" && !(await ask(
      `停止并删除 Compose 栈 ${project} 的容器和默认网络？\n\n命名卷不会被删除；生产环境还需要逐目标输入实际环境名。`,
      { kind: "warning" },
    ))) return;
    await execute(action);
  };

  const openLogs = async () => {
    if (unavailable) return;
    const generation = ++attachGeneration.current;
    setBusy("logs");
    const sink = { onBytes: null as ((bytes: Uint8Array) => void) | null };
    const channel = createBinaryChannel((bytes) => sink.onBytes?.(bytes));
    try {
      const id = await dockerApi.composeLogsAttach(sessionId, project, 500, channel);
      if (generation !== attachGeneration.current) {
        void terminalApi.closeTab(id, "kill", sessionId).catch(() => undefined);
        disposeChannel(channel);
        return;
      }
      setStream({ id, sessionId, title: `${project} · Compose logs（按 service 前缀合并）`, channel, sink });
    } catch (cause) {
      disposeChannel(channel);
      pushToast("error", `${project} 日志打开失败：${describeError(cause)}`);
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="bg-neutral-950/35 px-2 py-1.5">
      <div className="flex flex-wrap items-center gap-1.5">
        <ContextMenu state={menu} onClose={() => setMenu(null)} />
        <span className="nx-badge nx-badge-purple max-w-[60%] truncate" title={project}>{project}</span>
        <span className="text-[11px] text-neutral-500">{compact ? `${effective.services.length} services` : `Compose stack · ${effective.services.length} services`}</span>
        {effective.readOnly && <span className="nx-badge nx-badge-amber" title={effective.reason}>只读</span>}
        {effective.reason && <span className={`${compact ? "max-w-full" : "max-w-[460px]"} truncate text-[11px] text-amber-300`} title={effective.reason}>{effective.reason}</span>}
        {compact ? (
          <>
        <div className="nx-spacer" />
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs pointer-coarse:h-8" disabled={unavailable} onClick={() => void startAction("up")}>
          <IconPlay size={11} />up -d
        </button>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs pointer-coarse:h-8" disabled={unavailable} onClick={() => void openLogs()}>
          <IconList size={11} />logs
        </button>
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-xs pointer-coarse:h-8"
          aria-label="更多 Compose 操作"
          aria-haspopup="menu"
          disabled={unavailable}
          onClick={(e) => {
            const rect = e.currentTarget.getBoundingClientRect();
            setMenu({
              x: rect.right,
              y: rect.bottom,
              title: project,
              items: [
                { kind: "item", label: "down", icon: <IconStop size={13} />, onSelect: () => void startAction("down") },
                { kind: "item", label: "restart", icon: <IconRestart size={13} />, onSelect: () => void startAction("restart") },
                { kind: "item", label: "pull", icon: <IconDownload size={13} />, onSelect: () => void startAction("pull") },
                { kind: "item", label: "Edit and redeploy", icon: <IconEdit size={13} />, onSelect: () => setEditing(true) },
              ],
            });
          }}
        >
          <span aria-hidden="true">⋯</span>
        </button>
          </>
        ) : (
          <>
        <div className="nx-spacer" />
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={unavailable} onClick={() => void startAction("up")}>
          <IconPlay size={11} />up -d
        </button>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={unavailable} onClick={() => void startAction("down")}>
          <IconStop size={11} />down
        </button>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={unavailable} onClick={() => void startAction("restart")}>
          <IconRestart size={11} />restart
        </button>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={unavailable} onClick={() => void startAction("pull")}>
          {busy === "pull" ? <IconRefresh size={11} className="animate-spin" /> : <IconDownload size={11} />}pull
        </button>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={unavailable} onClick={() => void openLogs()}>
          <IconList size={11} />logs
        </button>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={unavailable} onClick={() => setEditing(true)}>
          <IconEdit size={11} />Edit and redeploy
        </button>
          </>
        )}
      </div>
      {output && (
        <div className="relative mt-1.5">
          <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs absolute right-1 top-1 z-10" onClick={() => setOutput(null)}>关闭</button>
          <pre className="nx-pre max-h-56 overflow-auto whitespace-pre-wrap break-all pr-14 text-[11px]">{output}</pre>
        </div>
      )}
      {stream && <DockerStreamView stream={stream} onClose={() => setStream(null)} />}
      {production && (
        <ProductionConfirmationDialog
          challenge={production.challenge}
          contentHash={production.challenge.commandHash}
          command={production.command}
          onCancel={() => setProduction(null)}
          onConfirm={(environmentNames) => {
            const current = production;
            setProduction(null);
            void current.retry(dockerConfirmation(current.challenge, environmentNames));
          }}
        />
      )}
      {editing && !effective.readOnly && (
        <ComposeEditor sessionId={sessionId} stack={effective} onClose={() => setEditing(false)} onChanged={onChanged} />
      )}
    </div>
  );
}
