// 命令面板（§5.1 Ctrl+Shift+P）：动作中心注册表。
import { useEffect, useMemo, useRef, useState } from "react";
import { useUi, connectAsset, nextTabId, openTerminalTab } from "./store";
import { assetApi, dbApi, sessionApi } from "../ipc/commands";
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
  /** 图标组件（用真实存在的组件取类型，避免为类型单独引入值）。 */
  icon: typeof IconTerminal;
  run: () => void;
}

export function CommandPalette({
  onClose,
  onOpenFiles,
}: {
  onClose: () => void;
  /** 打开宽幅文件浏览器标签（左栏文件树之外的另一条路）。 */
  onOpenFiles?: () => void;
}) {
  const { sessions, setSessions, addTab, pushToast } = useUi();
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const [assets, setAssets] = useState<{ id: string; name: string; kind: string; host: string | null }[]>([]);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
    void assetApi.list().then(setAssets).catch(() => undefined);
  }, []);

  const actions = useMemo<Action[]>(() => {
    const list: Action[] = [
      {
        id: "local-terminal",
        label: "打开本地终端",
        hint: "Ctrl+T",
        icon: IconTerminal,
        run: () => {
          void sessionApi
            .connectLocal()
            .then((s) => {
              setSessions([...sessions.filter((x) => x.id !== s.id), s]);
              return openTerminalTab(s, "本地终端");
            })
            .catch((e) => pushToast("error", String(e)));
        },
      },
      {
        id: "database",
        label: "打开数据库工作台",
        hint: "MySQL / Redis",
        icon: IconDatabase,
        run: () => {
          const asset = assets.find((a) => a.kind === "mysql") ?? assets.find((a) => a.kind === "redis");
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
            .catch((e) => pushToast("error", String(e)));
        },
      },
      {
        id: "split-pane",
        label: "上下分屏 / 取消分屏",
        hint: "Ctrl+\\",
        icon: IconSplitH,
        run: () => {
          const cur = useUi.getState();
          const w = cur.workspaces.find((x) => x.id === cur.activeWorkspaceId);
          if (!w) {
            pushToast("info", "先连接一台机器（双击左侧资产）");
            return;
          }
          if (w.panes.length > 1) void cur.unsplitWorkspace(w.panes[1].id, w.id);
          else cur.splitWorkspace(w.id);
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
        hint: "AI / 凭据库 / MCP",
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
    for (const a of assets) {
      list.push({
        id: `conn-${a.id}`,
        label: `连接 ${a.name}`,
        hint: [a.host, a.kind].filter(Boolean).join(" · "),
        icon: assetIcon(a.kind),
        run: () => void connectAsset(a),
      });
    }
    return list;
  }, [assets, sessions, setSessions, addTab, pushToast, onOpenFiles]);

  const filtered = actions.filter((a) =>
    `${a.label} ${a.hint ?? ""}`.toLowerCase().includes(query.toLowerCase()),
  );

  return (
    <div className="nx-overlay items-start justify-center pt-24" onClick={onClose}>
      <div
        className="nx-modal w-[560px] overflow-hidden p-0"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-2.5 border-b border-neutral-800/70 px-4">
          <IconSearch size={15} className="shrink-0 text-neutral-500" />
          <input
            ref={inputRef}
            className="nx-command-input border-none px-0"
            placeholder="输入命令或资产名…"
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setCursor(0);
            }}
            onKeyDown={(e) => {
              if (e.key === "Escape") onClose();
              if (e.key === "ArrowDown") setCursor((c) => Math.min(c + 1, filtered.length - 1));
              if (e.key === "ArrowUp") setCursor((c) => Math.max(c - 1, 0));
              if (e.key === "Enter" && filtered[cursor]) {
                filtered[cursor].run();
                onClose();
              }
            }}
          />
        </div>
        <div className="max-h-[340px] overflow-y-auto p-1.5">
          {filtered.map((a, i) => (
            <button
              key={a.id}
              className={`nx-command-item ${i === cursor ? "is-active" : ""}`}
              onMouseEnter={() => setCursor(i)}
              onClick={() => {
                a.run();
                onClose();
              }}
            >
              <a.icon size={15} className="shrink-0 text-neutral-500" />
              <span className="min-w-0 flex-1 truncate">{a.label}</span>
              {a.hint && <span className="shrink-0 text-[11px] text-neutral-500">{a.hint}</span>}
            </button>
          ))}
          {filtered.length === 0 && (
            <div className="px-4 py-8 text-center text-xs text-neutral-600">没有匹配的命令或资产</div>
          )}
        </div>
        <div className="flex items-center gap-3 border-t border-neutral-800/70 bg-neutral-950/40 px-4 py-2 text-[10.5px] text-neutral-500">
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
