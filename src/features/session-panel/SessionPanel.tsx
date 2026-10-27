import { useEffect, useRef, type KeyboardEvent, type ReactNode } from "react";
import { formatBinding, formatBindingAria, useKeybindings } from "../../app/keybindings";
import { useUi } from "../../app/store";
import { IconBox, IconClose, IconFolderOpen, IconList, IconLock, IconNetwork, IconServer, type IconInfo } from "../../ui/icons";
import { openHostDetail } from "../hosts/hostDetail";
import { trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { useTerminalCwd } from "../files/cwdFollow";
import { FilesTab } from "./FilesTab";
import { ContainersTab, LogsTab, PortsTab } from "./HostTabs";
import { PanelEmpty } from "./PanelEmpty";
import { useSessionPanelContext, type SessionPanelContext } from "./sessionContext";
import { useSessionPanel, type SessionPanelTabId } from "./sessionPanelStore";

interface TabEnv {
  context: SessionPanelContext | null;
}

interface PanelTab {
  id: SessionPanelTabId;
  label: string;
  // title describes the tab where the short label needs it.
  title?: string;
  icon: typeof IconInfo;
  // hostBound tabs show the host of the panel context; a lock on a closed terminal replaces them with an unlock hint.
  hostBound: boolean;
  render: (env: TabEnv) => ReactNode;
}

// The panel's tabs, in order. Each renders from the same context; the AI tab joins this list later.
const PANEL_TABS: readonly PanelTab[] = [
  { id: "files", label: "文件", icon: IconFolderOpen, hostBound: true, render: ({ context }) => <FilesTab context={context} /> },
  {
    id: "containers",
    label: "容器",
    title: "当前主机的 Docker 容器",
    icon: IconBox,
    hostBound: true,
    render: ({ context }) => <ContainersTab context={context} />,
  },
  {
    id: "ports",
    label: "端口",
    title: "经由当前主机的端口转发",
    icon: IconNetwork,
    hostBound: true,
    render: ({ context }) => <PortsTab context={context} />,
  },
  {
    id: "logs",
    label: "日志",
    title: "跟随当前主机的日志",
    icon: IconList,
    hostBound: true,
    render: ({ context }) => <LogsTab context={context} />,
  },
];

export interface SessionPanelProps {
  // overlay is true when the panel is a full-screen sheet (mobile and compact layouts).
  overlay: boolean;
  onClose: () => void;
}

// SessionPanel is the right-side panel that follows the focused terminal session: its host and working directory.
export function SessionPanel({ overlay, onClose }: SessionPanelProps) {
  const { context, source, lockedGone } = useSessionPanelContext();
  const activeTab = useSessionPanel((s) => s.tab);
  const setTab = useSessionPanel((s) => s.setTab);
  const lock = useSessionPanel((s) => s.lock);
  const unlock = useSessionPanel((s) => s.unlock);
  const focusRequest = useSessionPanel((s) => s.focusRequest);
  const bindings = useKeybindings();
  const shortcut = bindings.toggleSessionPanel;
  const cwd = useTerminalCwd(context?.terminalId);
  const tabRefs = useRef(new Map<SessionPanelTabId, HTMLButtonElement>());
  const current = PANEL_TABS.find((tab) => tab.id === activeTab) ?? PANEL_TABS[0];
  const locked = source === "locked";
  // The host behind the panel's session, when it is a saved asset or a device: the header links to its detail page.
  const hostAssetId = useUi((s) => {
    if (!context) return undefined;
    const workspace = s.workspaces.find((w) => w.id === context.workspaceId);
    return workspace?.kind === "session" ? workspace.assetId : undefined;
  });

  const currentId = useRef(current.id);
  currentId.current = current.id;
  const sectionRef = useRef<HTMLElement>(null);

  // The full-screen sheet is a modal layer like the app's dialogs: focus stays inside it while open and returns to
  // where it was when the sheet closes.
  useOverlayFocus(overlay, sectionRef, { initialFocus: () => tabRefs.current.get(currentId.current) ?? null });

  // An explicit open (toolbar button or shortcut) moves focus into the panel; opening on connect does not.
  useEffect(() => {
    if (focusRequest === 0) return;
    tabRefs.current.get(currentId.current)?.focus();
  }, [focusRequest]);

  const selectTab = (id: SessionPanelTabId, focus: boolean) => {
    setTab(id);
    if (focus) tabRefs.current.get(id)?.focus();
  };

  const onTabKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    let next: number | null = null;
    if (event.key === "ArrowRight") next = (index + 1) % PANEL_TABS.length;
    else if (event.key === "ArrowLeft") next = (index - 1 + PANEL_TABS.length) % PANEL_TABS.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = PANEL_TABS.length - 1;
    if (next === null) return;
    event.preventDefault();
    selectTab(PANEL_TABS[next].id, true);
  };

  const subtitle = context
    ? [context.hostLabel, cwd].filter(Boolean).join(" · ")
    : locked
      ? "锁定的会话已关闭"
      : "未聚焦终端";

  return (
    <section
      className="nx-session-panel flex h-full min-h-0 w-full min-w-0 flex-col border-l border-neutral-800/60 bg-neutral-950"
      aria-label="会话侧栏"
      ref={sectionRef}
      role={overlay ? "dialog" : undefined}
      aria-modal={overlay ? true : undefined}
      tabIndex={overlay ? -1 : undefined}
      onKeyDown={(event) => {
        if (overlay) trapOverlayTab(event, sectionRef.current);
        if (overlay && event.key === "Escape" && !event.defaultPrevented) {
          event.preventDefault();
          onClose();
        }
      }}
    >
      <div className="flex h-[38px] shrink-0 items-center gap-1.5 border-b border-neutral-800/60 px-2 pl-3">
        <span className="shrink-0 text-[12.5px] font-semibold text-neutral-100">会话</span>
        <span className="min-w-0 truncate text-[11px] text-neutral-500" title={subtitle} data-testid="session-panel-context">
          {subtitle}
        </span>
        {source === "last" && (
          <span className="nx-badge shrink-0" title="当前标签不是终端，侧栏显示上一个终端">
            上一个终端
          </span>
        )}
        <div className="nx-spacer" />
        {hostAssetId && (
          <button
            type="button"
            className="nx-icon-btn nx-icon-btn-sm"
            aria-label="查看主机详情"
            title="查看主机详情：会话、容器、数据库和指标"
            onClick={() => {
              openHostDetail(hostAssetId);
              // The overlay sheet covers the page that just opened.
              if (overlay) onClose();
            }}
          >
            <IconServer size={13} />
          </button>
        )}
        <button
          type="button"
          className={`nx-icon-btn nx-icon-btn-sm ${locked ? "is-active" : ""}`}
          aria-pressed={locked}
          aria-label="锁定"
          title={locked ? "已锁定：切换会话时侧栏保持不变，点击解除锁定" : "锁定到当前会话：切换会话时侧栏保持不变"}
          disabled={!locked && !context}
          onClick={() => {
            if (locked) unlock();
            else if (context) lock(context);
          }}
        >
          <IconLock size={13} />
        </button>
        <button
          type="button"
          className="nx-icon-btn nx-icon-btn-sm"
          aria-label="收起会话侧栏"
          title={`收起会话侧栏${shortcut ? ` (${formatBinding(shortcut)})` : ""}`}
          aria-keyshortcuts={formatBindingAria(shortcut) ?? undefined}
          onClick={onClose}
        >
          <IconClose size={14} />
        </button>
      </div>
      <div
        role="tablist"
        aria-label="会话侧栏页签"
        className="flex h-[32px] shrink-0 items-center gap-1 overflow-x-auto border-b border-neutral-800/60 px-1.5"
      >
        {PANEL_TABS.map((tab, index) => {
          const selected = tab.id === current.id;
          return (
            <button
              key={tab.id}
              type="button"
              role="tab"
              id={`nx-session-tab-${tab.id}`}
              aria-selected={selected}
              aria-controls={`nx-session-tabpanel-${tab.id}`}
              tabIndex={selected ? 0 : -1}
              ref={(el) => {
                if (el) tabRefs.current.set(tab.id, el);
                else tabRefs.current.delete(tab.id);
              }}
              className={`nx-tab h-6 shrink-0 gap-1 px-2 text-[11.5px] ${selected ? "is-active" : ""}`}
              title={tab.title}
              onClick={() => selectTab(tab.id, false)}
              onKeyDown={(event) => onTabKeyDown(event, index)}
            >
              <tab.icon size={12} />
              {tab.label}
            </button>
          );
        })}
      </div>
      <div
        role="tabpanel"
        id={`nx-session-tabpanel-${current.id}`}
        aria-labelledby={`nx-session-tab-${current.id}`}
        className="min-h-0 flex-1"
      >
        {lockedGone && current.hostBound ? (
          <PanelEmpty
            icon={IconLock}
            title="锁定的会话已关闭"
            hint="解除锁定后，侧栏重新跟随当前聚焦的会话。"
            action={
              <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" onClick={unlock}>
                解除锁定
              </button>
            }
          />
        ) : (
          current.render({ context })
        )}
      </div>
    </section>
  );
}
