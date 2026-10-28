import { useUi, openTerminalTab } from "../store";
import { formatBinding, useKeybindings } from "../keybindings";
import { useShell } from "../shell";
import { sessionApi } from "../../ipc/commands";
import { IconCommand, IconPlus, IconTerminal, Logo } from "../../ui/icons";

export function WorkspaceEmpty({ onNew }: { onNew: () => void }) {
  return (
    <div className="flex h-full items-center justify-center bg-neutral-900 px-6">
      <div className="flex w-[300px] flex-col items-center gap-3.5 rounded-xl border border-neutral-800/70 bg-neutral-950/45 px-6 py-7 text-center">
        <div className="nx-empty-icon">
          <IconTerminal size={18} />
        </div>
        <div>
          <div className="text-[13.5px] font-semibold tracking-tight text-neutral-100">暂无终端</div>
          <div className="mt-1 text-[11.5px] text-neutral-500">创建一个新的终端会话</div>
        </div>
        <button className="nx-btn nx-btn-primary" onClick={onNew}>
          <IconPlus size={14} />
          新建终端
        </button>
        <div className="nx-hint">在左侧栏的文件树中双击文件，可打开编辑器标签。</div>
      </div>
    </div>
  );
}

export function EmptyState({ onLocal, onPalette }: { onLocal?: () => void; onPalette?: () => void }) {
  const setSessions = useUi((s) => s.setSessions);
  const bindings = useKeybindings();
  const mobile = useShell() === "mobile";
  return (
    <div className="flex h-full flex-col items-center justify-center gap-5 bg-neutral-900 px-6">
      <Logo size={52} />
      <div className="text-center">
        <div className="text-[17px] font-semibold tracking-tight text-neutral-100">NexTerm</div>
        <div className="mt-1 text-xs text-neutral-500">
          SSH、WinRM、文件、Docker、数据库和 AI，集中在一个工作台
        </div>
      </div>
      <div className="flex items-center gap-2">
        {!mobile && (
          <button
            className="nx-btn nx-btn-primary"
            onClick={() => {
              if (onLocal) {
                onLocal();
                return;
              }
              void sessionApi.connectLocal().then((s) => {
                setSessions([...useUi.getState().sessions.filter((x) => x.id !== s.id), s]);
                void openTerminalTab(s);
              });
            }}
          >
            <IconTerminal size={14} />
            打开本地终端
          </button>
        )}
        {onPalette && (
          <button className="nx-btn nx-btn-outline" onClick={onPalette}>
            <IconCommand size={14} />
            跳转面板
          </button>
        )}
      </div>
      <div className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1 text-[11px] text-neutral-500">
        {(
          [
            ["newTerminal", "新建终端"],
            ["quickConnect", "快速连接"],
            ["commandPalette", "跳转面板"],
            ["toggleSidebar", "左栏"],
            ["toggleAiSidebar", "AI 侧栏"],
          ] as const
        ).map(([id, label]) => (
          <span key={id} className="inline-flex items-center gap-1">
            <span className="nx-kbd">{formatBinding(bindings[id])}</span>
            {label}
          </span>
        ))}
      </div>
      <div className="nx-hint max-w-md text-center">
        双击左栏资产打开它的工作区（「当前设备」即本机），主机工作区的左侧栏默认显示该主机的文件树，左栏的容器、端口转发和日志按钮作用于当前主机；点「资产」或「凭据」可临时切换侧栏；主机工作区不会自动新建终端，点「新建终端」打开。容器资产打开容器面板，MySQL / PostgreSQL / Redis 资产打开数据库工作台。
      </div>
    </div>
  );
}
