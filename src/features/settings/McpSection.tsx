// MCP 设置区（M4-T1 + M4-T2）：开关/端口/token、逐工具权限门、一键写入外部 AI 工具配置。
//
// 为什么单独成文件：SettingsView 已经很长，且这块与 AI 提供方配置无耦合。
import { useEffect, useState } from "react";
import { mcpApi, type McpSettings, type McpToolDto } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { IconCopy, IconEye, IconEyeOff, IconPlug, IconRefresh, IconZap } from "../../ui/icons";

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
      pushToast("success", restart ? "已保存 —— 端口 / token / 启停变更需重启应用生效" : "已保存");
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
      <section className="nx-card">
        <div className="mb-1 flex items-center gap-2">
          <IconPlug size={15} className="text-neutral-400" />
          <span className="nx-card-title">MCP（对外暴露能力）</span>
        </div>
        <p className="nx-hint">读取中…</p>
      </section>
    );
  }

  const writeCount = tools.filter((t) => !t.readOnly && t.writeEnabled).length;
  const writeTotal = tools.filter((t) => !t.readOnly).length;

  return (
    <section className="nx-card">
      <div className="mb-1 flex items-center gap-2">
        <IconPlug size={15} className="text-neutral-400" />
        <span className="nx-card-title">MCP（对外暴露能力）</span>
        <span className={`nx-badge ${settings.enabled ? "nx-badge-green" : ""}`}>
          {settings.enabled ? "已启用" : "已停用"}
        </span>
        {writeTotal > 0 && (
          <span className="nx-badge nx-badge-amber">
            写工具 {writeCount}/{writeTotal} 开闸
          </span>
        )}
      </div>
      <p className="nx-hint mb-3.5">
        让 Claude Code / Cursor 这类外部 AI 直接操作 NexTerm 里已连接的服务器、文件、容器与数据库。
        写工具默认全部关闭，逐个放闸；连接时需要校验 token。
      </p>

      <label className="mb-3.5 flex cursor-pointer items-center gap-2 text-[12.5px] text-neutral-300">
        <input
          type="checkbox"
          className="nx-check"
          checked={settings.enabled}
          onChange={(e) => void save({ ...settings, enabled: e.target.checked })}
        />
        启用 MCP 服务
      </label>

      <div className="mb-3.5 grid grid-cols-[88px_1fr] items-center gap-2.5">
        <span className="text-xs text-neutral-400">HTTP 端口</span>
        <input
          className="nx-input max-w-[160px] font-mono"
          placeholder={PORT_PLACEHOLDER}
          value={settings.httpPort || ""}
          onChange={(e) =>
            setSettings({ ...settings, httpPort: Number(e.target.value.replace(/\D/g, "")) || 0 })
          }
          onBlur={() => void save(settings)}
        />
        <span className="text-xs text-neutral-400">Token</span>
        <div className="flex items-center gap-1.5">
          <input
            className="nx-input font-mono"
            type={showToken ? "text" : "password"}
            value={settings.token}
            onChange={(e) => setSettings({ ...settings, token: e.target.value })}
            onBlur={() => void save(settings)}
          />
          <button
            className="nx-icon-btn"
            title={showToken ? "隐藏 token" : "显示 token"}
            onClick={() => setShowToken((v) => !v)}
          >
            {showToken ? <IconEyeOff size={14} /> : <IconEye size={14} />}
          </button>
          <button
            className="nx-btn nx-btn-outline nx-btn-sm"
            disabled={busy}
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
            <IconRefresh size={13} />
            生成
          </button>
        </div>
      </div>

      {settings.enabled && (
        <div className="nx-alert nx-alert-info mb-4">
          端点：<span className="nx-code">http://127.0.0.1:{settings.httpPort}/mcp</span>
          <br />
          只监听 127.0.0.1，仅本机可连。端口 / token / 启停的变更需重启应用生效。
        </div>
      )}

      <h3 className="nx-label mt-1">工具权限</h3>
      <div className="mb-4 overflow-hidden rounded-md border border-neutral-800/70">
        {tools.map((t, i) => (
          <div
            key={t.name}
            className={`flex items-center gap-3 px-3 py-2 text-xs ${
              i > 0 ? "border-t border-neutral-800/50" : ""
            }`}
          >
            <span className="w-[132px] shrink-0 font-mono text-neutral-200">{t.name}</span>
            <span className="min-w-0 flex-1 truncate text-neutral-500" title={t.description}>
              {t.description}
            </span>
            {t.readOnly ? (
              <span className="nx-badge shrink-0">只读</span>
            ) : (
              <label className="flex shrink-0 cursor-pointer items-center gap-1.5" title="放闸后该写工具才可用">
                <input
                  type="checkbox"
                  className="nx-check"
                  checked={t.writeEnabled}
                  disabled={busy}
                  onChange={(e) => void toggleWriteTool(t.name, e.target.checked)}
                />
                <span className={t.writeEnabled ? "text-[10.5px] text-amber-300" : "text-[10.5px] text-neutral-500"}>
                  写入
                </span>
              </label>
            )}
          </div>
        ))}
      </div>

      <h3 className="nx-label">写入外部 AI 工具配置</h3>
      <div className="flex flex-wrap items-center gap-2">
        {TARGETS.map((t) => (
          <button
            key={t.key}
            className="nx-btn nx-btn-outline nx-btn-sm"
            disabled={busy || !settings.enabled}
            title={`写入 ${t.hint}（合并 + 备份）`}
            onClick={() => void writeTo(t.key)}
          >
            <IconZap size={12} />
            写入 {t.label}
          </button>
        ))}
        <button className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => void copySnippet()}>
          <IconCopy size={12} />
          复制 JSON
        </button>
      </div>
      <p className="nx-hint mt-2.5">
        写入是「合并」而不是覆盖：只新增 / 更新 <span className="nx-code">mcpServers.nexterm</span>，
        原文件先备份为 <span className="nx-code">.nexterm.bak</span>；目标文件若不是合法 JSON 会直接拒绝写入。
        VS Code 的 MCP 配置是工作区级的，请用「复制 JSON」。
      </p>
    </section>
  );
}
