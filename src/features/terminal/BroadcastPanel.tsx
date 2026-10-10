import { useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { createPortal } from "react-dom";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { IconAlert, IconClose, IconZap } from "../../ui/icons";
import {
  broadcastRiskMessage,
  collectBroadcastCandidates,
  localRemoteMix,
  quickBroadcastIds,
  type BroadcastCandidate,
} from "./broadcast";

function eligibleTargets(
  candidates: BroadcastCandidate[],
  targetIds: string[],
): BroadcastCandidate[] {
  const member = new Set(targetIds);
  return candidates.filter((c) => member.has(c.storeTabId) && c.eligible);
}

export function BroadcastStrip(props: {
  workspaceId: string;
  myStoreTabId: string;
  onManage: () => void;
}) {
  const broadcast = useUi((s) => s.broadcast);
  const workspaces = useUi((s) => s.workspaces);
  const sessions = useUi((s) => s.sessions);
  const setBroadcast = useUi((s) => s.setBroadcast);
  const pushToast = useUi((s) => s.pushToast);
  if (!broadcast || broadcast.workspaceId !== props.workspaceId) return null;
  if (!broadcast.targetIds.includes(props.myStoreTabId)) return null;
  const ws = workspaces.find((w) => w.id === props.workspaceId);
  if (!ws) return null;
  const candidates = collectBroadcastCandidates(ws, sessions);
  const writable = eligibleTargets(candidates, broadcast.targetIds).length;
  return (
    <div
      role="alert"
      className="flex shrink-0 items-center gap-2 border-b border-red-500/40 bg-red-500/10 px-2.5 py-1 text-[12px] text-red-300"
    >
      <IconZap size={13} className="shrink-0" />
      <span className="min-w-0 truncate">
        广播已开启：此终端的输入将同时发送到 {writable} 个终端
        {writable < broadcast.targetIds.length
          ? `（${broadcast.targetIds.length - writable} 个目标当前不可写，发送时自动跳过）`
          : ""}
      </span>
      <div className="nx-spacer" />
      <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={props.onManage}>
        管理目标
      </button>
      <button
        type="button"
        className="nx-btn nx-btn-ghost nx-btn-sm"
        onClick={() => {
          setBroadcast(null);
          pushToast("info", "广播已关闭，输入只发送到当前终端");
        }}
      >
        <IconClose size={12} />
        关闭广播
      </button>
    </div>
  );
}

export function BroadcastPickerModal(props: {
  workspaceId: string;
  myPaneId: string;
  onClose: () => void;
}) {
  const broadcast = useUi((s) => s.broadcast);
  const workspaces = useUi((s) => s.workspaces);
  const sessions = useUi((s) => s.sessions);
  const setBroadcast = useUi((s) => s.setBroadcast);
  const pushToast = useUi((s) => s.pushToast);
  const ws = workspaces.find((w) => w.id === props.workspaceId);
  const candidates = ws ? collectBroadcastCandidates(ws, sessions) : [];
  const existing = broadcast && broadcast.workspaceId === props.workspaceId ? broadcast.targetIds : null;
  const [selected, setSelected] = useState<readonly string[]>(
    () => existing ?? quickBroadcastIds(candidates, "pane", props.myPaneId),
  );
  const modalRef = useRef<HTMLDivElement>(null);
  const applyButtonRef = useRef<HTMLButtonElement>(null);
  const layer = useOverlayFocus(true, modalRef, { initialFocus: () => applyButtonRef.current });
  const onCloseRef = useRef(props.onClose);
  onCloseRef.current = props.onClose;

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.repeat || isImeKeyEvent(event) || !layer.isTopmost()) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      onCloseRef.current();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [layer]);

  if (!ws) return null;

  const selectedSet = new Set(selected);
  const chosen = candidates.filter((c) => selectedSet.has(c.storeTabId) && c.eligible);
  const mix = localRemoteMix(chosen);

  const toggle = (id: string) => {
    setSelected((current) =>
      current.includes(id) ? current.filter((x) => x !== id) : [...current, id],
    );
  };

  const apply = async () => {
    if (chosen.length === 0) {
      setBroadcast(null);
      pushToast("info", "广播已关闭，输入只发送到当前终端");
      props.onClose();
      return;
    }
    const confirmed = await ask(broadcastRiskMessage(chosen.length, mix), {
      title: "开启命令广播",
      kind: "warning",
    });
    if (!confirmed) return;
    setBroadcast({ workspaceId: props.workspaceId, targetIds: chosen.map((c) => c.storeTabId) });
    pushToast("info", `广播已开启：输入将发送到 ${chosen.length} 个终端`);
    props.onClose();
  };

  const onModalKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (event.key === "Escape" && !event.repeat && !isImeKeyEvent(event)) {
      event.preventDefault();
      props.onClose();
      return;
    }
    if (layer.isTopmost()) trapOverlayTab(event, modalRef.current);
  };

  return createPortal(
    <div className="nx-overlay" onClick={props.onClose}>
      <div
        ref={modalRef}
        className="nx-modal max-w-[440px]"
        role="dialog"
        aria-modal="true"
        aria-label="命令广播目标"
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={onModalKeyDown}
      >
        <div className="nx-modal-header">
          <span className="flex items-center gap-1.5 truncate">
            <IconZap size={13} className="text-red-300" />
            命令广播目标
          </span>
          <div className="nx-spacer" />
          <button
            type="button"
            className="nx-icon-btn nx-icon-btn-sm"
            aria-label="关闭广播目标选择"
            onClick={props.onClose}
          >
            <IconClose size={12} />
          </button>
        </div>
        <div className="nx-modal-body">
          <p className="nx-hint mb-3 flex items-start gap-1.5">
            <IconAlert size={13} className="mt-0.5 shrink-0 text-amber-300" />
            <span>
              开启后，在任一选中终端里的输入会同时发送到全部选中终端。已失效、已退出和非交互终端会自动跳过
            </span>
          </p>
          <div className="mb-2 flex flex-wrap items-center gap-1.5">
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-sm"
              onClick={() => setSelected(quickBroadcastIds(candidates, "pane", props.myPaneId))}
            >
              当前分屏
            </button>
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-sm"
              onClick={() => setSelected(quickBroadcastIds(candidates, "workspace"))}
            >
              全部终端
            </button>
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-sm"
              onClick={() => setSelected([])}
            >
              清空
            </button>
            <div className="nx-spacer" />
            <span className="text-[11.5px] text-neutral-400">已选 {chosen.length} 个</span>
          </div>
          <div className="flex max-h-[240px] flex-col gap-1 overflow-y-auto">
            {candidates.map((c) => (
              <label
                key={c.storeTabId}
                className={`flex items-center gap-2 rounded-md border border-neutral-800/60 bg-neutral-900/50 px-2 py-1.5 ${
                  c.eligible ? "" : "opacity-50"
                }`}
              >
                <input
                  type="checkbox"
                  className="h-4 w-4 shrink-0"
                  disabled={!c.eligible}
                  checked={selectedSet.has(c.storeTabId) && c.eligible}
                  onChange={() => toggle(c.storeTabId)}
                />
                <span className="min-w-0 flex-1 truncate text-[12px] text-neutral-200">{c.title}</span>
                {c.local && <span className="nx-badge nx-badge-amber shrink-0">本地</span>}
                {c.sessionKind === "winrm" && <span className="nx-badge shrink-0">WinRM</span>}
                {!c.eligible && (
                  <span className="shrink-0 text-[11px] text-neutral-500">{c.ineligibleReason}</span>
                )}
              </label>
            ))}
            {candidates.length === 0 && (
              <div className="nx-hint px-1 py-2">这个工作区还没有终端标签。</div>
            )}
          </div>
          {mix.mixed && (
            <p className="mt-2 text-[11.5px] text-amber-300">
              选中目标同时包含本地终端（{mix.local} 个）与远程终端（{mix.remote} 个）。
            </p>
          )}
        </div>
        <div className="nx-modal-footer">
          <button type="button" className="nx-btn nx-btn-ghost" onClick={props.onClose}>
            取消
          </button>
          <button
            ref={applyButtonRef}
            type="button"
            className="nx-btn nx-btn-primary"
            disabled={chosen.length === 0 && !existing}
            onClick={() => void apply()}
          >
            {chosen.length > 0 ? `开启广播（${chosen.length}）` : "关闭广播"}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
