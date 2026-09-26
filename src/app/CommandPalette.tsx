// 命令面板（§5.1 Ctrl+Shift+P）：动作中心注册表。
import { useEffect, useMemo, useRef, useState } from "react";
import { useUi, connectAsset, openTerminalTab } from "./store";
import { assetApi, sessionApi } from "../ipc/commands";

interface Action {
  id: string;
  label: string;
  hint?: string;
  run: () => void;
}

export function CommandPalette({ onClose }: { onClose: () => void }) {
  const { sessions, setSessions, addTab, pushToast } = useUi();
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  const actions = useMemo<Action[]>(() => {
    const list: Action[] = [
      {
        id: "local-terminal",
        label: "打开本地终端 (Ctrl+T)",
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
        id: "settings",
        label: "打开设置",
        run: () => addTab({ id: "settings", kind: "settings", title: "设置", closable: true }),
      },
      {
        id: "audit",
        label: "查看审计日志",
        run: () => addTab({ id: "audit", kind: "audit", title: "审计日志", closable: true }),
      },
    ];
    void assetApi
      .list()
      .then((assets) => {
        for (const a of assets) {
          list.push({
            id: `conn-${a.id}`,
            label: `连接 ${a.name} (${a.kind})`,
            hint: a.host ?? undefined,
            run: () => void connectAsset(a),
          });
        }
      })
      .catch(() => undefined);
    return list;
  }, [sessions, setSessions, addTab, pushToast]);

  const filtered = actions.filter((a) =>
    a.label.toLowerCase().includes(query.toLowerCase()),
  );

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 pt-24"
      onClick={onClose}
    >
      <div
        className="w-[520px] overflow-hidden rounded-xl border border-neutral-700 bg-neutral-900 shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <input
          ref={inputRef}
          className="w-full border-b border-neutral-800 bg-transparent px-4 py-3 text-sm outline-none"
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
        <div className="max-h-80 overflow-y-auto py-1">
          {filtered.map((a, i) => (
            <button
              key={a.id}
              className={`flex w-full items-center gap-2 px-4 py-2 text-left text-sm ${
                i === cursor ? "bg-neutral-800" : "hover:bg-neutral-800/60"
              }`}
              onClick={() => {
                a.run();
                onClose();
              }}
            >
              <span className="flex-1 text-neutral-200">{a.label}</span>
              {a.hint && <span className="text-xs text-neutral-500">{a.hint}</span>}
            </button>
          ))}
          {filtered.length === 0 && (
            <div className="px-4 py-6 text-center text-xs text-neutral-600">无匹配</div>
          )}
        </div>
      </div>
    </div>
  );
}
