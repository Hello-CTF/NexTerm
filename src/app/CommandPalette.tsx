// 命令面板（§5.1 Ctrl+Shift+P）：动作中心注册表。
import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { useUi, connectAsset, nextTabId, openTerminalTab } from "./store";
import { isMac } from "./platform";
import { assetApi, dbApi, sessionApi } from "../ipc/commands";
import { describeError } from "../ui/errorText";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../ui/DialogHost";
import {
  IconDatabase,
  IconFolderOpen,
  IconHistory,
  IconNetwork,
  IconSearch,
  IconSettings,
  IconSplitH,
  IconTerminal,
  assetIcon,
} from "../ui/icons";

interface Action {
  id: string;
  label: string;
  hint?: string;
  icon: typeof IconTerminal;
  run: () => void;
}

export function CommandPalette({
  onClose,
  onOpenFiles,
}: {
  onClose: () => void;
  onOpenFiles?: () => void;
}) {
  const { sessions, setSessions, addTab, pushToast } = useUi();
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const [assets, setAssets] = useState<{ id: string; name: string; kind: string; host: string | null }[]>([]);
  const [assetError, setAssetError] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const modalRef = useRef<HTMLDivElement>(null);
  const closedRef = useRef(false);
  const listId = useId();
  const titleId = useId();
  const helpId = useId();
  const layer = useOverlayFocus(true, modalRef, { initialFocus: () => inputRef.current });
  const modHint = isMac() ? "⌘" : "Ctrl";

  useEffect(() => {
    let mounted = true;
    void assetApi.list().then(
      (result) => {
        if (mounted) setAssets(result);
      },
      (error) => {
        if (mounted) setAssetError(`资产列表加载失败：${describeError(error)}`);
      },
    );
    return () => {
      mounted = false;
    };
  }, []);

  const actions = useMemo<Action[]>(() => {
    const list: Action[] = [
      {
        id: "local-terminal",
        label: "打开本地终端",
        hint: `${modHint}+T · 当前设备`,
        icon: IconTerminal,
        run: () => {
          void sessionApi
            .connectLocal()
            .then((session) => {
              setSessions([...sessions.filter((item) => item.id !== session.id), session]);
              return openTerminalTab(session);
            })
            .catch((error) => pushToast("error", describeError(error)));
        },
      },
      {
        id: "database",
        label: "打开数据库工作台",
        hint: "MySQL / Redis",
        icon: IconDatabase,
        run: () => {
          const asset = assets.find((item) => item.kind === "mysql") ?? assets.find((item) => item.kind === "redis");
          if (!asset) {
            pushToast("info", "还没有数据库资产");
            return;
          }
          void dbApi
            .connect(asset.id)
            .then(({ connId }) => {
              const kind = asset.kind === "redis" ? "redis" : "mysql";
              addTab({
                id: `db-${connId}`,
                kind: "db",
                title: `${asset.name} · ${kind === "mysql" ? "SQL" : "Redis"}`,
                connId,
                dbKind: kind,
                closable: true,
              });
            })
            .catch((error) => pushToast("error", describeError(error)));
        },
      },
      {
        id: "split-pane",
        label: "上下分屏 / 取消分屏",
        hint: `${modHint}+\\`,
        icon: IconSplitH,
        run: () => {
          const current = useUi.getState();
          const workspace = current.workspaces.find((item) => item.id === current.activeWorkspaceId);
          if (!workspace) {
            pushToast("info", "先连接一台机器（双击左侧资产）");
            return;
          }
          if (workspace.panes.length > 1) void current.unsplitWorkspace(workspace.panes[1].id, workspace.id);
          else current.splitWorkspace(workspace.id);
        },
      },
      {
        id: "wide-files",
        label: "打开宽幅文件浏览器",
        hint: "含体积 / 修改时间列",
        icon: IconFolderOpen,
        run: () => onOpenFiles?.(),
      },
      {
        id: "port-forward",
        label: "打开端口转发",
        hint: "本地 / SOCKS5",
        icon: IconNetwork,
        run: () =>
          addTab({
            id: nextTabId("forward-cmd"),
            kind: "forward",
            title: "端口转发",
            closable: true,
          }),
      },
      {
        id: "settings",
        label: "打开设置",
        hint: "AI / 凭据库",
        icon: IconSettings,
        run: () => addTab({ id: "settings", kind: "settings", title: "设置", closable: true }),
      },
      {
        id: "audit",
        label: "查看审计日志",
        hint: "用户与 AI 的动作记录",
        icon: IconHistory,
        run: () => addTab({ id: "audit", kind: "audit", title: "审计日志", closable: true }),
      },
    ];
    for (const asset of assets) {
      list.push({
        id: `conn-${asset.id}`,
        label: `连接 ${asset.name}`,
        hint: [asset.host, asset.kind].filter(Boolean).join(" · "),
        icon: assetIcon(asset.kind),
        run: () => void connectAsset(asset),
      });
    }
    return list;
  }, [assets, sessions, setSessions, addTab, pushToast, onOpenFiles, modHint]);

  const filtered = actions.filter((action) =>
    `${action.label} ${action.hint ?? ""}`.toLowerCase().includes(query.toLowerCase()),
  );
  const activeIndex = filtered.length > 0 ? Math.min(cursor, filtered.length - 1) : 0;
  const activeOptionId = filtered[activeIndex] ? `${listId}-option-${activeIndex}` : undefined;

  useEffect(() => {
    if (!activeOptionId) return;
    const option = document.getElementById(activeOptionId);
    if (typeof option?.scrollIntoView === "function") option.scrollIntoView({ block: "nearest" });
  }, [activeOptionId]);

  const closePalette = () => {
    if (closedRef.current) return;
    closedRef.current = true;
    onClose();
  };
  const execute = (action: Action) => {
    if (closedRef.current) return;
    closedRef.current = true;
    try {
      action.run();
    } finally {
      onClose();
    }
  };
  const moveCursor = (next: number) => {
    if (filtered.length > 0) setCursor((next + filtered.length) % filtered.length);
  };
  const onPaletteKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (!layer.isTopmost()) return;
    if (event.key === "Tab") {
      trapOverlayTab(event, modalRef.current);
      return;
    }
    if (isImeKeyEvent(event)) return;
    if (event.key === "Escape" && !event.repeat) {
      event.preventDefault();
      closePalette();
    } else if (event.key === "ArrowDown") {
      event.preventDefault();
      moveCursor(activeIndex + 1);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      moveCursor(activeIndex - 1);
    } else if (event.key === "Home") {
      event.preventDefault();
      moveCursor(0);
    } else if (event.key === "End") {
      event.preventDefault();
      moveCursor(filtered.length - 1);
    } else if (event.key === "Enter" && !event.repeat && filtered[activeIndex]) {
      event.preventDefault();
      execute(filtered[activeIndex]);
    }
  };

  return (
    <div className="nx-overlay items-start justify-center pt-24" onClick={closePalette}>
      <div
        ref={modalRef}
        className="nx-modal nx-command-modal w-[560px] overflow-hidden p-0"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={helpId}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={onPaletteKeyDown}
      >
        <h2 id={titleId} className="nx-sr-only">命令面板</h2>
        <div className="flex items-center gap-2.5 border-b border-neutral-800/70 px-4">
          <IconSearch size={15} className="shrink-0 text-neutral-400" />
          <input
            ref={inputRef}
            className="nx-command-input border-none px-0"
            placeholder="输入命令或资产名…"
            aria-label="命令或资产搜索"
            role="combobox"
            aria-autocomplete="list"
            aria-expanded="true"
            aria-controls={listId}
            aria-activedescendant={activeOptionId}
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              setCursor(0);
            }}
          />
        </div>
        <div id={listId} className="max-h-[340px] overflow-y-auto p-1.5" role="listbox" aria-label="命令结果">
          {filtered.map((action, index) => (
            <button
              key={action.id}
              id={`${listId}-option-${index}`}
              type="button"
              role="option"
              aria-selected={index === activeIndex}
              tabIndex={-1}
              className={`nx-command-item ${index === activeIndex ? "is-active" : ""}`}
              onMouseEnter={() => setCursor(index)}
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => execute(action)}
            >
              <action.icon size={15} className="shrink-0 text-neutral-400" />
              <span className="min-w-0 flex-1 truncate">{action.label}</span>
              {action.hint && <span className="nx-command-hint shrink-0 text-[11px]">{action.hint}</span>}
            </button>
          ))}
          {filtered.length === 0 && (
            <div className="nx-command-empty px-4 py-8 text-center text-xs" role="status">
              没有匹配的命令或资产
            </div>
          )}
        </div>
        {assetError && <div className="nx-command-error" role="alert">{assetError}</div>}
        <div id={helpId} className="nx-command-help flex items-center gap-3 border-t border-neutral-800/70 bg-neutral-950/40 px-4 py-2 text-[10.5px]">
          <span className="flex items-center gap-1.5">
            <span className="nx-kbd">↑</span>
            <span className="nx-kbd">↓</span> 选择
          </span>
          <span className="flex items-center gap-1.5">
            <span className="nx-kbd">Enter</span> 执行
          </span>
          <span className="flex items-center gap-1.5">
            <span className="nx-kbd">Esc</span> 关闭
          </span>
        </div>
      </div>
    </div>
  );
}
