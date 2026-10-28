import { useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { createPortal } from "react-dom";
import { useUi, useSessionList, useWorkspaceList } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { IconAlert, IconClose, IconZap } from "../../ui/icons";
import {
  broadcastRiskMessage,
  collectBroadcastCandidates,
  groupBroadcastCandidates,
  localRemoteMix,
  quickBroadcastIds,
} from "./broadcast";

export function BroadcastPickerModal(props: {
  workspaceId: string;
  myPaneId: string;
  myStoreTabId: string;
  onClose: () => void;
}) {
  const broadcast = useUi((state) => state.broadcast);
  const workspaces = useWorkspaceList();
  const sessions = useSessionList();
  const setBroadcast = useUi((state) => state.setBroadcast);
  const pushToast = useUi((state) => state.pushToast);
  const candidates = collectBroadcastCandidates(workspaces, sessions);
  const groups = groupBroadcastCandidates(candidates);
  const existing = broadcast?.targetIds ?? null;
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

  const selectedSet = new Set(selected);
  const chosen = candidates.filter((candidate) => selectedSet.has(candidate.storeTabId) && candidate.eligible);
  const mix = localRemoteMix(chosen);

  const toggle = (id: string) => {
    setSelected((current) =>
      current.includes(id) ? current.filter((value) => value !== id) : [...current, id],
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
    setBroadcast({
      origin: broadcast?.origin ?? {
        workspaceId: props.workspaceId,
        storeTabId: props.myStoreTabId,
      },
      targetIds: chosen.map((candidate) => candidate.storeTabId),
    });
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
        className="nx-modal nx-modal-md"
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
              只广播来源终端的输入。目标是所有已打开工作区中的已连接终端；失效、已退出、未连接和非交互终端会自动跳过
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
              onClick={() => setSelected(quickBroadcastIds(candidates, "all"))}
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
          <div className="flex max-h-[320px] flex-col gap-3 overflow-y-auto">
            {groups.map((host) => (
              <section key={host.hostKey} aria-label={`主机 ${host.hostName}`}>
                <div className="mb-1 flex items-center gap-2 px-1 text-[12px] font-medium text-neutral-200">
                  <span className="truncate">{host.hostName}</span>
                  <span className="text-[11px] font-normal text-neutral-500">
                    {host.workspaces.reduce((count, workspace) => count + workspace.candidates.length, 0)} 个终端
                  </span>
                </div>
                {host.workspaces.map((workspace) => (
                  <div key={workspace.workspaceId} className="mb-1.5 last:mb-0">
                    <div className="px-1 py-0.5 text-[11px] text-neutral-500">
                      工作区：{workspace.workspaceTitle}
                    </div>
                    <div className="flex flex-col gap-1">
                      {workspace.candidates.map((candidate) => (
                        <label
                          key={candidate.storeTabId}
                          className={`flex items-center gap-2 rounded-md border border-neutral-800/60 bg-neutral-900/50 px-2 py-1.5 ${
                            candidate.eligible ? "" : "opacity-50"
                          }`}
                        >
                          <input
                            type="checkbox"
                            className="h-4 w-4 shrink-0"
                            disabled={!candidate.eligible}
                            checked={selectedSet.has(candidate.storeTabId) && candidate.eligible}
                            onChange={() => toggle(candidate.storeTabId)}
                          />
                          <span className="min-w-0 flex-1 truncate text-[12px] text-neutral-200">
                            {candidate.title}
                          </span>
                          {candidate.local && <span className="nx-badge nx-badge-amber shrink-0">本地</span>}
                          {candidate.sessionKind === "winrm" && <span className="nx-badge shrink-0">WinRM</span>}
                          {!candidate.eligible && (
                            <span className="shrink-0 text-[11px] text-neutral-500">
                              {candidate.ineligibleReason}
                            </span>
                          )}
                        </label>
                      ))}
                    </div>
                  </div>
                ))}
              </section>
            ))}
            {candidates.length === 0 && (
              <div className="nx-hint px-1 py-2">所有已打开工作区中暂无终端标签页。</div>
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
