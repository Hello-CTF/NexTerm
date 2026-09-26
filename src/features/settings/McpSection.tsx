// MCP 设置区（M4-T1 + M4-T2）：开关/端口/token、逐工具权限门、一键写入外部 AI 工具配置。
//
// 为什么单独成文件：SettingsView 已经很长，且这块与 AI 提供方配置无耦合。
import { useEffect, useState } from "react";
import { mcpApi, type McpSettings, type McpToolDto } from "../../ipc/commands";
import { useUi } from "../../app/store";

const PORT_PLACEHOLDER = "8799";

/** 三个可一键写入的目标；VS Code 是工作区级配置，没有稳定用户级路径，故走「复制 JSON」。 */
const TARGETS: { key: "claude_code" | "claude_desktop" | "cursor"; label: string; hint: string }[] = [
  { key: "claude_code", label: "Claude Code", hint: "~/.claude.json" },
  { key: "claude_desktop", label: "Claude Desktop", hint: "claude_desktop_config.json" },
  { key: "cursor", label: "Cursor", hint: "~/.cursor/mcp.json" },
];

export function McpSection() {
  const pushToast = useUi((s) => s.pushToast);
  const [settings, setSettings] = useState<McpSettings | null>(null);
  const [tools, setTools] = useState<McpToolDto[]>([]);
  const [showToken, setShowToken] = useState(false);
  const [busy, setBusy] = useState(false);

  // 挂载时读一次；后续变更由用户操作驱动（save / toggleWriteTool 内部更新本地 state）
  useEffect(() => {
    let alive = true;
    void (async () => {
      try {
        const [s, t] = await Promise.all([mcpApi.getSettings(), mcpApi.listTools()]);
        if (!alive) return;
        setSettings(s);
        setTools(t);
      } catch (e) {
        if (alive) pushToast("error", `读取 MCP 设置失败：${String(e)}`);
      }
    })();
    return () => {
      alive = false;
    };
  }, [pushToast]);

  const save = async (next: McpSettings) => {
    setBusy(true);
    try {
      const restart = await mcpApi.saveSettings(next);
      setSettings(next);
      pushToast(
        "success",
        restart ? "已保存 —— 端口/token/启停变更需重启应用生效" : "已保存",
      );
    } catch (e) {
      pushToast("error", String(e));
    } finally {
      setBusy(false);
    }
  };

  const toggleWriteTool = async (name: string, on: boolean) => {
    if (!settings) return;
    const next: McpSettings = {
      ...settings,
      writeTools: { ...settings.writeTools, [name]: on },
    };
    // 权限门是每次调用现读设置，改完立即生效，无需重启
    setBusy(true);
    try {
      await mcpApi.saveSettings(next);
      setSettings(next);
      setTools((prev) => prev.map((t) => (t.name === name ? { ...t, writeEnabled: on } : t)));
    } catch (e) {
      pushToast("error", String(e));
    } finally {
      setBusy(false);
    }
  };

  const copySnippet = async () => {
    try {
      const snippet = await mcpApi.clientConfigSnippet();
      await navigator.clipboard.writeText(JSON.stringify(snippet, null, 2));
      pushToast("success", "已复制 MCP 配置片段，可粘贴到任意 AI 工具的 MCP 配置里");
    } catch (e) {
      pushToast("error", `复制失败：${String(e)}`);
    }
  };

  const writeTo = async (target: "claude_code" | "claude_desktop" | "cursor") => {
    setBusy(true);
    try {
      const r = await mcpApi.writeClientConfig(target);
      pushToast(
        "success",
        `已写入 ${r.path}${r.created ? "（新建）" : `（原文件已备份到 ${r.backup ?? "同目录"}）`}`,
      );
    } catch (e) {
      pushToast("error", String(e));
    } finally {
      setBusy(false);
    }
  };

  if (!settings) {
    return (
      <section className="mb-8 max-w-2xl rounded-lg border border-neutral-800 p-4">
        <h2 className="mb-3 text-sm font-medium text-neutral-200">MCP（对外暴露能力）</h2>
        <p className="text-xs text-neutral-500">读取中…</p>
      </section>
    );
  }

  const inputCls =
    "w-full rounded bg-neutral-800 px-2 py-1 text-sm text-neutral-200 outline-none focus:ring-1 focus:ring-blue-500";
  const btnCls =
    "rounded bg-neutral-700 px-2 py-1 text-xs hover:bg-neutral-600 disabled:opacity-40";

  return (
    <section className="mb-8 max-w-2xl rounded-lg border border-neutral-800 p-4">
      <h2 className="mb-1 text-sm font-medium text-neutral-200">MCP（对外暴露能力）</h2>
      <p className="mb-3 text-xs leading-relaxed text-neutral-500">
        让 Claude Code / Cursor 等外部 AI 直接操作 NexTerm 里已连接的服务器、文件、容器与数据库。
        写工具默认全部关闭，逐个放闸。
      </p>

      <label className="mb-3 flex items-center gap-2 text-sm text-neutral-300">
        <input
          type="checkbox"
          checked={settings.enabled}
          onChange={(e) => void save({ ...settings, enabled: e.target.checked })}
        />
        启用 MCP 服务
      </label>

      <div className="mb-3 grid grid-cols-[90px_1fr] items-center gap-2 text-sm">
        <span className="text-neutral-400">HTTP 端口</span>
        <input
          className={inputCls}
          placeholder={PORT_PLACEHOLDER}
          value={settings.httpPort || ""}
          onChange={(e) =>
            setSettings({ ...settings, httpPort: Number(e.target.value.replace(/\D/g, "")) || 0 })
          }
          onBlur={() => void save(settings)}
        />
        <span className="text-neutral-400">Token</span>
        <div className="flex items-center gap-1">
          <input
            className={inputCls}
            type={showToken ? "text" : "password"}
            value={settings.token}
            onChange={(e) => setSettings({ ...settings, token: e.target.value })}
            onBlur={() => void save(settings)}
          />
          <button className={btnCls} onClick={() => setShowToken((v) => !v)}>
            {showToken ? "隐藏" : "显示"}
          </button>
          <button
            className={btnCls}
            onClick={async () => {
              try {
                const t = await mcpApi.generateToken();
                setSettings({ ...settings, token: t });
                await save({ ...settings, token: t });
              } catch (e) {
                pushToast("error", String(e));
              }
            }}
          >
            生成
          </button>
        </div>
      </div>

      {settings.enabled && (
        <div className="mb-4 rounded border border-neutral-800 bg-neutral-900/50 p-2 text-[11px] text-neutral-500">
          端点：<code className="text-neutral-400">http://127.0.0.1:{settings.httpPort}/mcp</code>
          <br />
          只监听 127.0.0.1，仅本机可连。端口 / token / 启停的变更需重启应用生效。
        </div>
      )}

      <h3 className="mb-2 text-xs font-medium text-neutral-300">工具权限</h3>
      <div className="mb-4 space-y-1">
        {tools.map((t) => (
          <div key={t.name} className="flex items-start gap-2 text-xs">
            <span className="w-32 shrink-0 font-mono text-neutral-300">{t.name}</span>
            <span className="min-w-0 flex-1 text-neutral-500">{t.description}</span>
            {t.readOnly ? (
              <span className="shrink-0 rounded bg-neutral-800 px-1.5 py-0.5 text-[10px] text-neutral-400">
                只读
              </span>
            ) : (
              <label className="shrink-0">
                <input
                  type="checkbox"
                  checked={t.writeEnabled}
                  disabled={busy}
                  onChange={(e) => void toggleWriteTool(t.name, e.target.checked)}
                />
                <span className="ml-1 text-[10px] text-amber-400">写</span>
              </label>
            )}
          </div>
        ))}
      </div>

      <h3 className="mb-2 text-xs font-medium text-neutral-300">写入外部 AI 工具配置</h3>
      <div className="flex flex-wrap items-center gap-2">
        {TARGETS.map((t) => (
          <button
            key={t.key}
            className={btnCls}
            disabled={busy || !settings.enabled}
            title={`写入 ${t.hint}（合并 + 备份）`}
            onClick={() => void writeTo(t.key)}
          >
            写入 {t.label}
          </button>
        ))}
        <button className={btnCls} onClick={() => void copySnippet()}>
          复制 JSON
        </button>
      </div>
      <p className="mt-2 text-[11px] leading-relaxed text-neutral-600">
        写入是「合并」而不是覆盖：只新增/更新 <code>mcpServers.nexterm</code>，原文件先备份为
        <code>.nexterm.bak</code>；目标文件若不是合法 JSON 会直接拒绝写入。VS Code 的 MCP 配置是
        工作区级的，请用「复制 JSON」。
      </p>
    </section>
  );
}
