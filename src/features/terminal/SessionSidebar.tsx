import { useMemo, useState, type ReactNode } from "react";
import { isHostWorkspace, restoreClosedTab, useUi } from "../../app/store";
import { useClosedTabs } from "../../app/closedTabs";
import { PortsSection } from "../hosts/PortsSection";
import { IconChevronDown, IconChevronRight, IconClose, IconFile, IconFolder, IconSparkles } from "../../ui/icons";
import { t } from "../../i18n";

const FILE_LIMIT = 10;

export interface SessionSidebarProps {
  workspaceId: string | null;
  storeTabId: string;
  kernelTabId: string | null;
  title: string;
  visible: boolean;
  // Already wired command block panel, or null when this terminal records no command blocks.
  commandBlocks: ReactNode | null;
  onClose: () => void;
}

interface OpenFile {
  tabId: string;
  path: string;
}

function baseName(path: string): string {
  const trimmed = path.replace(/[\\/]+$/, "");
  const i = Math.max(trimmed.lastIndexOf("/"), trimmed.lastIndexOf("\\"));
  return i >= 0 ? trimmed.slice(i + 1) || trimmed : trimmed;
}

function Collapsible({ label, children }: { label: string; children: ReactNode }) {
  const [open, setOpen] = useState(true);
  return (
    <div className="shrink-0 border-b border-neutral-800/60">
      <button
        type="button"
        className="flex w-full items-center gap-1 px-2.5 py-1.5 text-left text-[11.5px] font-semibold text-neutral-200 hover:bg-neutral-900"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        {open ? <IconChevronDown size={12} /> : <IconChevronRight size={12} />}
        {label}
      </button>
      {open && <div className="min-w-0 px-2.5 pb-2">{children}</div>}
    </div>
  );
}

function FileRow({ path, onClick }: { path: string; onClick: () => void }) {
  return (
    <button
      type="button"
      className="flex w-full min-w-0 items-center gap-1.5 rounded px-1 py-0.5 text-left text-[11.5px] text-neutral-300 hover:bg-neutral-800"
      title={path}
      onClick={onClick}
    >
      <IconFile size={11} className="shrink-0 text-neutral-500" />
      <span className="min-w-0 truncate">{baseName(path)}</span>
    </button>
  );
}

export function SessionSidebar({ workspaceId, storeTabId, kernelTabId, title, visible, commandBlocks, onClose }: SessionSidebarProps) {
  const workspace = useUi((s) => (workspaceId ? s.workspaces.find((w) => w.id === workspaceId) : undefined));
  const [portsOpen, setPortsOpen] = useState(true);
  const entries = useClosedTabs((s) => s.stack.entries);
  const hostName = workspace?.title ?? title;

  const openFiles = useMemo<OpenFile[]>(() => {
    const out: OpenFile[] = [];
    for (const pane of workspace?.panes ?? []) {
      for (const tab of pane.tabs) {
        if ((tab.kind === "files" || tab.kind === "log") && tab.path) out.push({ tabId: tab.id, path: tab.path });
      }
    }
    return out.slice(0, FILE_LIMIT);
  }, [workspace]);

  const closedFiles = useMemo(() => {
    if (!workspace) return [];
    return entries
      .filter((e) => {
        if (e.kind !== "file" || !e.path) return false;
        if (workspace.sessionId && e.sessionId === workspace.sessionId) return true;
        return Boolean(workspace.assetId) && e.assetId === workspace.assetId;
      })
      .slice(0, FILE_LIMIT);
  }, [entries, workspace]);

  const hostWorkspace = isHostWorkspace(workspace);
  const openExplorer = () => {
    const ui = useUi.getState();
    ui.setLeftOverride(null);
    ui.setLeftOpen(true);
  };

  const referenceTerminal = () => {
    if (!kernelTabId) return;
    const ui = useUi.getState();
    ui.queueAiRef({ id: `tab:${storeTabId}`, kind: "tab", label: title, detail: t("{hostName} 的终端", { hostName }), refId: kernelTabId });
    ui.setRightOpen(true);
  };

  return (
    <aside
      role="complementary"
      aria-label={t("会话侧栏")}
      className="flex h-full w-[280px] min-w-0 shrink-0 flex-col overflow-x-hidden border-l border-neutral-800/60 bg-neutral-950 @max-[560px]:hidden"
    >
      <div className="flex h-[31px] shrink-0 items-center gap-1.5 border-b border-neutral-800/60 px-2.5">
        <span className="min-w-0 truncate text-[11.5px] font-semibold text-neutral-200" title={hostName}>
          {t("会话侧栏")}
        </span>
        <div className="nx-spacer" />
        <button type="button" className="nx-icon-btn nx-icon-btn-sm" aria-label={t("收起会话侧栏")} title={t("收起会话侧栏")} onClick={onClose}>
          <IconClose size={12} />
        </button>
      </div>

      <div className="shrink-0 overflow-y-auto border-b border-neutral-800/60 max-h-[45%]">
        {portsOpen ? (
          <div className="p-2">
            <PortsSection workspace={workspace} visible={visible} hostName={hostName} />
            <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs mt-1" onClick={() => setPortsOpen(false)}>
              {t("收起端口")}
            </button>
          </div>
        ) : (
          <button
            type="button"
            className="flex w-full items-center gap-1 px-2.5 py-1.5 text-left text-[11.5px] font-semibold text-neutral-200 hover:bg-neutral-900"
            aria-expanded={false}
            onClick={() => setPortsOpen(true)}
          >
            <IconChevronRight size={12} />
            {t("端口")}
          </button>
        )}

        <Collapsible label={t("最近文件")}>
          <div className="flex flex-col gap-1.5">
            {openFiles.length > 0 && (
              <div>
                <div className="text-[10.5px] text-neutral-500">{t("已打开")}</div>
                {openFiles.map((f) => (
                  <FileRow key={f.tabId} path={f.path} onClick={() => useUi.getState().setActiveTab(f.tabId)} />
                ))}
              </div>
            )}
            {closedFiles.length > 0 && (
              <div>
                <div className="text-[10.5px] text-neutral-500">{t("最近关闭")}</div>
                {closedFiles.map((e) => (
                  <FileRow key={e.key} path={e.path ?? ""} onClick={() => void restoreClosedTab(e.key)} />
                ))}
              </div>
            )}
            {openFiles.length === 0 && closedFiles.length === 0 && <p className="nx-hint m-0 text-[12px]">{t("暂无最近文件")}</p>}
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-xs self-start"
              disabled={!hostWorkspace}
              title={hostWorkspace ? t("在左侧栏打开文件浏览器") : t("只在主机工作区可用")}
              onClick={openExplorer}
            >
              <IconFolder size={11} />
              {t("打开文件浏览器")}
            </button>
            {!hostWorkspace && <p className="nx-hint m-0 text-[11px]">{t("文件浏览器只在主机工作区可用。")}</p>}
          </div>
        </Collapsible>
      </div>

      <div className="flex min-h-0 flex-1 flex-col">
        {commandBlocks ?? <p className="nx-hint m-0 p-3 text-[12px]">{t("此终端不支持命令记录。")}</p>}
      </div>

      <div className="shrink-0 border-t border-neutral-800/60 p-2">
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm w-full justify-center"
          aria-label={t("在 AI 中引用此终端")}
          disabled={!kernelTabId}
          onClick={referenceTerminal}
        >
          <IconSparkles size={12} />
          {t("在 AI 中引用此终端")}
        </button>
      </div>
    </aside>
  );
}
