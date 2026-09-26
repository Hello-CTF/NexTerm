// 应用外壳（§5.1）：三栏布局 + 标签栏 + 命令面板 + 全局快捷键 + 状态栏。
import { useEffect, useState } from "react";
import { useUi, openTerminalTab } from "./store";
import { sessionApi, vaultApi } from "../ipc/commands";
import { AssetTree } from "../features/explorer/AssetTree";
import { TerminalPane } from "../features/terminal/TerminalPane";
import { FileBrowser } from "../features/files/FileBrowser";
import { MountPanel } from "../features/files/MountPanel";
import { FileEditor } from "../features/files/FileEditor";
import { DockerPanel } from "../features/docker/DockerPanel";
import { DbPanel } from "../features/db/DbPanel";
import { AiSidebar } from "../features/ai/AiSidebar";
import { SettingsView } from "../features/settings/SettingsView";
import { AuditView } from "../features/settings/AuditView";
import { CommandPalette } from "./CommandPalette";
import { TakeoverBanner } from "./TakeoverBanner";
import { PromptModal } from "../ui/PromptModal";
import { registerPromptHandler } from "../ui/dialogs";

export default function App() {
  const {
    tabs,
    activeTabId,
    setActiveTab,
    closeTab,
    sessions,
    setSessions,
    leftOpen,
    setLeftOpen,
    rightOpen,
    setRightOpen,
    toasts,
    dismissToast,
    pushToast,
  } = useUi();

  const [paletteOpen, setPaletteOpen] = useState(false);
  const [vaultStatus, setVaultStatus] = useState<string>("…");

  // ── 全局快捷键（§5.1 / §10 shortcuts.ts 精神）──
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.ctrlKey || e.metaKey;
      if (mod && e.shiftKey && e.key.toLowerCase() === "p") {
        e.preventDefault();
        setPaletteOpen(true);
      } else if (mod && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPaletteOpen(true);
      } else if (mod && e.key.toLowerCase() === "b") {
        e.preventDefault();
        setLeftOpen(!leftOpen);
      } else if (mod && e.key.toLowerCase() === "j") {
        e.preventDefault();
        setRightOpen(!rightOpen);
      } else if (mod && e.key.toLowerCase() === "t") {
        e.preventDefault();
        void (async () => {
          const s = await sessionApi.connectLocal();
          setSessions([...sessions.filter((x) => x.id !== s.id), s]);
          await openTerminalTab(s, "本地终端");
        })();
      } else if (mod && e.key.toLowerCase() === "w" && activeTabId) {
        e.preventDefault();
        void closeTab(activeTabId);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [leftOpen, rightOpen, activeTabId, sessions, setSessions, setLeftOpen, setRightOpen, closeTab]);

  // 注册全局文本输入弹窗（替代 window.prompt）
  useEffect(() => {
    registerPromptHandler((message, value) => {
      const { openTextPrompt } = useUi.getState();
      return new Promise<string | null>((resolve) => {
        openTextPrompt({ message, value, resolve });
      });
    });
  }, []);

  // 启动时刷新会话列表 + 凭据库状态
  useEffect(() => {
    void sessionApi
      .list()
      .then(setSessions)
      .catch(() => undefined);
    void vaultApi
      .status()
      .then((v) =>
        setVaultStatus(
          !v.initialized ? "凭据库未初始化" : v.unlocked ? "凭据库已解锁" : "凭据库已锁定",
        ),
      )
      .catch(() => setVaultStatus("凭据库不可用"));
  }, [setSessions]);

  const active = tabs.find((t) => t.id === activeTabId);
  const activeSessionId = active?.sessionId ?? sessions[0]?.id;
  const activeTerminalTab = active?.tabId;

  return (
    <div className="flex h-full flex-col">
      {/* 接管横幅（§8.6）：置顶，非空即表示 AI 正在操作某个终端 */}
      <TakeoverBanner />

      {/* 标签栏 */}
      <div className="flex h-9 shrink-0 items-center gap-1 border-b border-neutral-800 bg-neutral-950 px-2">
        <span className="mr-2 text-sm font-semibold text-blue-400">NexTerm</span>
        <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
          {tabs.map((t) => (
            <button
              key={t.id}
              className={`group flex items-center gap-1 rounded-t px-3 py-1.5 text-xs whitespace-nowrap ${
                t.id === activeTabId
                  ? "bg-neutral-800 text-neutral-100"
                  : "text-neutral-400 hover:bg-neutral-900"
              }`}
              onClick={() => setActiveTab(t.id)}
            >
              {t.kind === "terminal" ? "⌨ " : t.kind === "files" ? "📂 " : ""}
              {t.title}
              {t.closable && (
                <span
                  className="ml-1 hidden text-neutral-500 hover:text-red-400 group-hover:inline"
                  onClick={(e) => {
                    e.stopPropagation();
                    void closeTab(t.id);
                  }}
                >
                  ✕
                </span>
              )}
            </button>
          ))}
        </div>
        <button
          className="rounded px-2 py-0.5 text-xs text-neutral-400 hover:bg-neutral-800"
          title="打开文件浏览器标签"
          onClick={() => {
            if (activeSessionId) {
              useUi.getState().addTab({
                id: `files-${activeSessionId}-${Date.now()}`,
                kind: "files",
                title: "文件",
                sessionId: activeSessionId,
                closable: true,
              });
            } else {
              pushToast("info", "先连接一台主机");
            }
          }}
        >
          文件
        </button>
        <button
          className="rounded px-2 py-0.5 text-xs text-neutral-400 hover:bg-neutral-800"
          title="打开挂载面板"
          onClick={() => {
            if (activeSessionId) {
              useUi.getState().addTab({
                id: `mount-${activeSessionId}-${Date.now()}`,
                kind: "mount",
                title: "挂载",
                sessionId: activeSessionId,
                closable: true,
              });
            } else {
              pushToast("info", "先连接一台主机");
            }
          }}
        >
          挂载
        </button>
        <button
          className="rounded px-2 py-0.5 text-xs text-neutral-400 hover:bg-neutral-800"
          onClick={() => setPaletteOpen(true)}
        >
          命令面板
        </button>
      </div>

      {/* 三栏主体 */}
      <div className="flex min-h-0 flex-1">
        <AssetTree />
        <button
          className="h-full w-4 shrink-0 items-center justify-center rounded text-neutral-600 hover:bg-neutral-900"
          onClick={() => setLeftOpen(false)}
          title="收起"
        >
          ‹
        </button>

        <main className="min-w-0 flex-1">
          {active ? (
            active.kind === "terminal" && active.sessionId ? (
              <TerminalPane
                key={active.id}
                sessionId={active.sessionId}
                title={active.title}
              />
            ) : active.kind === "mount" && active.sessionId ? (
              <MountPanel sessionId={active.sessionId} />
            ) : active.kind === "files" && active.sessionId ? (
              active.path ? (
                <FileEditor
                  sessionId={active.sessionId}
                  path={active.path}
                  onClose={() => void closeTab(active.id)}
                />
              ) : (
                <FileBrowser sessionId={active.sessionId} />
              )
            ) : active.kind === "docker" && active.sessionId ? (
              <DockerPanel sessionId={active.sessionId} />
            ) : active.kind === "db" && active.connId ? (
              <DbPanel connId={active.connId} kind="mysql" />
            ) : active.kind === "settings" ? (
              <SettingsView />
            ) : active.kind === "audit" ? (
              <AuditView />
            ) : (
              <EmptyState />
            )
          ) : (
            <EmptyState />
          )}
        </main>

        {rightOpen && activeTerminalTab === undefined && sessions.length === 0 ? (
          <AiSidebar sessionId={activeSessionId} tabId={undefined} />
        ) : (
          <AiSidebar sessionId={activeSessionId} tabId={activeTerminalTab} />
        )}
      </div>

      {/* 状态栏 */}
      <div className="flex h-6 shrink-0 items-center gap-4 border-t border-neutral-800 bg-neutral-950 px-3 text-[11px] text-neutral-500">
        <span>{sessions.length} 个会话</span>
        <span
          className={
            sessions.some((s) => s.status === "connected") ? "text-green-500" : "text-neutral-600"
          }
        >
          ● {sessions.filter((s) => s.status === "connected").length} 已连接
        </span>
        <span>{vaultStatus}</span>
        <div className="flex-1" />
        <button
          className="hover:text-neutral-300"
          onClick={() =>
            useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true })
          }
        >
          设置
        </button>
        <button
          className="hover:text-neutral-300"
          onClick={() =>
            useUi.getState().addTab({ id: "audit", kind: "audit", title: "审计日志", closable: true })
          }
        >
          审计
        </button>
      </div>

      {/* Toast */}
      <div className="pointer-events-none fixed right-4 bottom-8 z-50 flex flex-col gap-2">
        {toasts.map((t) => (
          <button
            key={t.id}
            className={`pointer-events-auto max-w-sm rounded-lg px-4 py-2 text-sm shadow-lg ${
              t.kind === "error"
                ? "bg-red-600 text-white"
                : t.kind === "success"
                  ? "bg-green-600 text-white"
                  : "bg-neutral-800 text-neutral-100"
            }`}
            onClick={() => dismissToast(t.id)}
          >
            {t.text}
          </button>
        ))}
      </div>

      {paletteOpen && <CommandPalette onClose={() => setPaletteOpen(false)} />}
      <PromptModal />
    </div>
  );
}

function EmptyState() {
  const { setSessions, sessions } = useUi();
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 text-neutral-600">
      <div className="text-5xl">⌨</div>
      <div className="text-lg">NexTerm</div>
      <div className="text-sm">一体化开发运维终端</div>
      <div className="max-w-md text-center text-xs text-neutral-700">
        左侧双击资产连接 · Ctrl+T 本地终端 · Ctrl+Shift+P 命令面板
      </div>
      <button
        className="rounded bg-neutral-800 px-4 py-1.5 text-sm text-neutral-300 hover:bg-neutral-700"
        onClick={() => {
          void sessionApi.connectLocal().then((s) => {
            setSessions([...sessions.filter((x) => x.id !== s.id), s]);
            void openTerminalTab(s, "本地终端");
          });
        }}
      >
        打开本地终端
      </button>
    </div>
  );
}
