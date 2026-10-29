import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { FileEditor, type FileEditorSaveChange } from "../files/FileEditor";
import { bytesToBase64 } from "../files/fileCodec";
import { dockerApi, type ComposeStack, type DockerProductionConfirmation } from "../../ipc/commands";
import { createBinaryChannel, disposeChannel } from "../../ipc/events";
import { describeError } from "../../ui/errorText";
import { IconClose } from "../../ui/icons";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { ProductionConfirmationDialog } from "../execbatch/ProductionConfirmationDialog";
import { dockerConfirmation, getDockerConfirmationChallenge, type DockerConfirmationChallenge } from "./productionGuard";
import { DockerStreamView, type DockerOutputStream } from "./DockerStreamView";
import { dockerTextDiff } from "./textDiff";

type PendingSave = {
  change: FileEditorSaveChange;
  redactEnv: boolean;
  resolve: (saved: boolean) => void;
};

type ProductionSave = {
  pending: PendingSave;
  challenge: DockerConfirmationChallenge;
};

export function ComposeEditor({
  sessionId,
  stack,
  onClose,
  onChanged,
}: {
  sessionId: string;
  stack: ComposeStack;
  onClose: () => void;
  onChanged: () => void;
}) {
  const files = [...stack.configFiles, ...(stack.envExists ? [stack.envPath] : [])];
  const [activePath, setActivePath] = useState(files[0] ?? "");
  const [pending, setPending] = useState<PendingSave | null>(null);
  const [production, setProduction] = useState<ProductionSave | null>(null);
  const [stream, setStream] = useState<DockerOutputStream | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const modalRef = useRef<HTMLDivElement>(null);
  const finishInFlight = useRef(false);
  const layer = useOverlayFocus(true, modalRef);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.repeat || isImeKeyEvent(event) || !layer.isTopmost() || pending || production) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      onClose();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [layer, onClose, pending, production]);

  const saveFile = async (change: FileEditorSaveChange): Promise<boolean> => {
    if (busy || pending || production) return false;
    setError(null);
    return new Promise<boolean>((resolve) => {
      setPending({ change, redactEnv: change.path === stack.envPath, resolve });
    });
  };

  const finish = async (pendingSave: PendingSave, confirmation?: DockerProductionConfirmation) => {
    if (finishInFlight.current) return;
    finishInFlight.current = true;
    setBusy(true);
    setError(null);
    // A retry closes the previous challenge: if the backend re-challenges, the
    // dialog remounts for the new identity instead of reusing its typed input.
    setProduction(null);
    const { change } = pendingSave;
    try {
      const bytes = new TextEncoder().encode(change.content);
      const prepared = await dockerApi.composeRedeployPrepare({
        sessionId,
        project: stack.project,
        path: change.path,
        baseContentBase64: change.baseContentBase64,
        contentBase64: bytesToBase64(bytes),
        ...(confirmation ? { productionConfirmation: confirmation } : {}),
      });
      pendingSave.resolve(true);
      setPending(null);
      setProduction(null);
      onChanged();
      const sink = { onBytes: null as ((bytes: Uint8Array) => void) | null };
      const channel = createBinaryChannel((bytes) => sink.onBytes?.(bytes));
      try {
        const id = await dockerApi.composeRedeployAttach({
          sessionId,
          project: stack.project,
          path: change.path,
          ticket: prepared.ticket,
          channel,
        });
        setStream({ id, sessionId, title: `${stack.project} · up -d`, channel, sink });
      } catch (cause) {
        disposeChannel(channel);
        setError(`文件已保存，但重新部署未能启动：${describeError(cause)}`);
      }
    } catch (cause) {
      const challenge = getDockerConfirmationChallenge(cause);
      if (challenge) {
        setProduction({ pending: pendingSave, challenge });
        setPending(null);
      } else {
        pendingSave.resolve(false);
        setPending(null);
        setProduction(null);
        setError(`保存或 redeploy 失败：${describeError(cause)}`);
      }
    } finally {
      finishInFlight.current = false;
      setBusy(false);
    }
  };

  const confirmDiff = async () => {
    const current = pending;
    if (!current || busy) return;
    await finish(current);
  };

  const cancelPending = () => {
    pending?.resolve(false);
    production?.pending.resolve(false);
    setPending(null);
    setProduction(null);
  };

  return createPortal(
    <>
      <div className="nx-overlay" onClick={onClose}>
        <div
          ref={modalRef}
          className="nx-modal flex h-[86vh] w-[94vw] max-w-[1200px] flex-col"
          role="dialog"
          aria-modal="true"
          aria-label={`编辑并重新部署 ${stack.project}`}
          tabIndex={-1}
          onClick={(event) => event.stopPropagation()}
          onKeyDown={(event) => {
            event.stopPropagation();
            if (layer.isTopmost()) trapOverlayTab(event, modalRef.current);
          }}
        >
          <div className="nx-modal-header">
            <span className="truncate">编辑并重新部署 · {stack.project}</span>
            <span className="nx-badge">保存前先查看差异</span>
            <div className="nx-spacer" />
            <button type="button" className="nx-icon-btn nx-icon-btn-sm" aria-label="关闭 Compose 编辑器" onClick={onClose}>
              <IconClose size={12} />
            </button>
          </div>
          <div className="flex shrink-0 flex-wrap gap-1 border-b border-neutral-800/70 px-2 py-1.5">
            {files.map((file) => (
              <button
                type="button"
                key={file}
                className={`nx-btn nx-btn-sm font-mono ${activePath === file ? "nx-btn-primary" : "nx-btn-ghost"}`}
                onClick={() => setActivePath(file)}
              >
                {file === stack.envPath ? ".env" : file.split(/[\\/]/).at(-1)}
              </button>
            ))}
            {!stack.envExists && (
              <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm font-mono" disabled title=".env 不存在；仅已有 .env 可在本流程中编辑">
                .env（不存在）
              </button>
            )}
          </div>
          {error && <div role="alert" className="shrink-0 border-b border-red-900/60 bg-red-950/30 px-3 py-2 text-xs text-red-300">{error}</div>}
          <div className="min-h-0 flex-1">
            {activePath && <FileEditor key={activePath} sessionId={sessionId} path={activePath} saveFile={saveFile} onClose={onClose} />}
          </div>
          {stream && <DockerStreamView stream={stream} onClose={() => setStream(null)} />}
        </div>
      </div>
      {pending && (
        <DiffSaveDialog
          pending={pending}
          busy={busy}
          onCancel={cancelPending}
          onConfirm={() => void confirmDiff()}
        />
      )}
      {production && (
        <ProductionConfirmationDialog
          key={`${production.challenge.targetSetHash}:${production.challenge.commandHash}`}
          challenge={production.challenge}
          contentHash={production.challenge.commandHash}
          command={`保存 ${production.pending.change.path} 并执行 docker compose up -d`}
          onCancel={cancelPending}
          onConfirm={(environmentNames) => void finish(production.pending, dockerConfirmation(production.challenge, environmentNames))}
        />
      )}
    </>,
    document.body,
  );
}

function DiffSaveDialog({ pending, busy, onCancel, onConfirm }: {
  pending: PendingSave;
  busy: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const layer = useOverlayFocus(true, ref);
  const diff = dockerTextDiff(pending.change.previous, pending.change.content, pending.redactEnv);
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.repeat || isImeKeyEvent(event) || !layer.isTopmost()) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      onCancel();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [layer, onCancel]);
  return createPortal(
    <div className="nx-overlay" onClick={onCancel}>
      <div
        ref={ref}
        className="nx-modal flex max-h-[80vh] w-[720px] max-w-[92vw] flex-col"
        role="dialog"
        aria-modal="true"
        aria-label="确认 Compose 变更"
        onClick={(event) => event.stopPropagation()}
        onKeyDown={(event) => {
          event.stopPropagation();
          if (layer.isTopmost()) trapOverlayTab(event, ref.current);
        }}
      >
        <div className="nx-modal-header">保存并重新部署</div>
        <div className="nx-modal-body min-h-0 overflow-auto">
          <p className="mb-2 break-all font-mono text-xs text-neutral-400">{pending.change.path}</p>
          {diff.length === 0 ? (
            <p className="text-xs text-amber-300">文件内容没有变化；继续仍会执行 up -d。</p>
          ) : (
            <pre className="nx-pre overflow-auto text-[11px] leading-relaxed">{diff.map((line, index) => (
              <div key={index} className={line.kind === "add" ? "text-green-300" : line.kind === "del" ? "text-red-300" : "text-neutral-500"}>
                {line.kind === "add" ? "+ " : line.kind === "del" ? "- " : "  "}{line.text}
              </div>
            ))}</pre>
          )}
        </div>
        <div className="nx-modal-footer">
          <button type="button" className="nx-btn nx-btn-ghost" disabled={busy} onClick={onCancel}>取消</button>
          <button type="button" className="nx-btn nx-btn-danger" disabled={busy} onClick={onConfirm}>
            {busy ? "正在预检…" : "保存并 up -d"}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
