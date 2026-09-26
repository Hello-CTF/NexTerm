// 设置页：AI 提供方（预设/自动填参/两步连通性测试）+ 凭据库 + MCP。
import { useEffect, useState } from "react";
import { aiApi, vaultApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { DEMO } from "../../demo";
import { McpSection } from "./McpSection";
import {
  IconCheckCircle,
  IconInfo,
  IconKey,
  IconLock,
  IconRefresh,
  IconSettings,
  IconSparkles,
  IconUnlock,
  IconXCircle,
  IconZap,
} from "../../ui/icons";

export function SettingsView() {
  const { pushToast } = useUi();
  const [presets, setPresets] = useState<string[]>([]);
  const [preset, setPreset] = useState("");
  const [cfg, setCfg] = useState({
    baseUrl: "",
    apiKey: "",
    model: "",
    temperature: 0.3,
    contextWindow: 32768,
    proxy: "",
    stream: true,
  });
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{ modelsOk: boolean; chatOk: boolean; detail: string } | null>(
    null,
  );

  // 凭据库
  const [vault, setVault] = useState<{
    initialized: boolean;
    mode: string;
    unlocked: boolean;
  } | null>(null);
  const [masterPwd, setMasterPwd] = useState("");
  const [unlockPwd, setUnlockPwd] = useState("");

  useEffect(() => {
    void aiApi.presets().then(setPresets).catch(() => undefined);
    void aiApi
      .getProvider()
      .then((c) =>
        setCfg({
          baseUrl: c.baseUrl,
          apiKey: c.apiKey,
          model: c.model,
          temperature: c.temperature,
          contextWindow: c.contextWindow,
          proxy: c.proxy ?? "",
          stream: c.stream,
        }),
      )
      .catch(() => undefined);
    void vaultApi.status().then(setVault).catch(() => undefined);
  }, []);

  const applyPreset = (name: string) => {
    setPreset(name);
    // 预设自动填参（§8.7）
    const known: Record<string, { baseUrl: string; model: string; contextWindow: number }> = {
      deepseek: { baseUrl: "https://api.deepseek.com/v1", model: "deepseek-chat", contextWindow: 64000 },
      openai: { baseUrl: "https://api.openai.com/v1", model: "gpt-4o-mini", contextWindow: 128000 },
      dashscope: { baseUrl: "https://dashscope.aliyuncs.com/compatible-mode/v1", model: "qwen-plus", contextWindow: 128000 },
      moonshot: { baseUrl: "https://api.moonshot.cn/v1", model: "moonshot-v1-32k", contextWindow: 128000 },
      zhipu: { baseUrl: "https://open.bigmodel.cn/api/paas/v4", model: "glm-4-flash", contextWindow: 128000 },
      ollama: { baseUrl: "http://127.0.0.1:11434/v1", model: "qwen2.5:7b", contextWindow: 32000 },
      lmstudio: { baseUrl: "http://127.0.0.1:1234/v1", model: "local-model", contextWindow: 32000 },
      vllm: { baseUrl: "http://127.0.0.1:8000/v1", model: "local-model", contextWindow: 32000 },
    };
    const k = known[name];
    if (k) {
      setCfg((c) => ({ ...c, baseUrl: k.baseUrl, model: k.model, contextWindow: k.contextWindow }));
    }
  };

  const payload = () => ({
    baseUrl: cfg.baseUrl,
    apiKey: cfg.apiKey,
    model: cfg.model,
    temperature: cfg.temperature,
    contextWindow: cfg.contextWindow,
    proxy: cfg.proxy || null,
    stream: cfg.stream,
  });

  const save = async () => {
    try {
      await aiApi.setProvider(payload());
      pushToast("success", "AI 配置已保存");
    } catch (e) {
      pushToast("error", String(e));
    }
  };

  const test = async () => {
    setTesting(true);
    setTestResult(null);
    try {
      // 两步测试：模型列表 + 实际对话（§8.7）
      await aiApi.setProvider(payload());
      const r = await aiApi.testProvider();
      setTestResult({
        modelsOk: r.modelsOk,
        chatOk: r.chatOk,
        detail: [
          `模型列表：${r.modelsOk ? "可用" : `失败 — ${r.modelsError ?? "未知错误"}`}`,
          `实际对话：${r.chatOk ? "可用" : `失败 — ${r.chatError ?? "未知错误"}`}`,
        ].join("\n"),
      });
    } catch (e) {
      setTestResult({ modelsOk: false, chatOk: false, detail: `测试失败: ${String(e)}` });
    } finally {
      setTesting(false);
    }
  };

  return (
    <div className="nx-pane h-full overflow-y-auto">
      <div className="nx-toolbar">
        <IconSettings size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">设置</span>
        <div className="nx-spacer" />
        {DEMO && (
          <span className="nx-badge nx-badge-amber" title="数据来自内置假数据，不会连真实服务">
            演示模式
          </span>
        )}
      </div>

      <div className="mx-auto flex w-full max-w-[720px] flex-col gap-4 p-5">
        {DEMO && (
          <section className="nx-alert nx-alert-info flex items-start gap-2.5">
            <IconInfo size={15} className="mt-0.5 shrink-0" />
            <div>
              <b>当前是演示模式。</b>
              资产、容器、数据库、终端、AI 回复全部来自前端内置的假数据，不会连接任何真实服务器，
              输入的内容也不会外发。想接真实后端请在 Tauri 里启动（URL 加 <span className="nx-code">?demo=0</span> 可关闭）。
            </div>
          </section>
        )}

        {/* AI 提供方 */}
        <section className="nx-card">
          <div className="mb-1 flex items-center gap-2">
            <IconSparkles size={15} className="text-blue-300" />
            <span className="nx-card-title">AI 模型</span>
            <span className="nx-badge">OpenAI 兼容协议 · BYOK</span>
          </div>
          <p className="nx-hint mb-3.5">
            密钥只存在本机加密凭据库里，不会上传；模型可换成本地 Ollama / LM Studio / vLLM。
          </p>

          <div className="mb-4 flex flex-wrap gap-1.5">
            {presets.map((p) => (
              <button
                key={p}
                className={`nx-chip ${preset === p ? "nx-chip-accent" : ""}`}
                onClick={() => applyPreset(p)}
              >
                {p}
              </button>
            ))}
          </div>

          <div className="nx-form-row">
            <label className="nx-label">Base URL</label>
            <input
              className="nx-input font-mono"
              value={cfg.baseUrl}
              onChange={(e) => setCfg({ ...cfg, baseUrl: e.target.value })}
              placeholder="https://api.deepseek.com/v1"
            />
          </div>

          <div className="nx-form-row">
            <label className="nx-label">API Key（仅本机加密存储）</label>
            <div className="nx-field">
              <span className="nx-field-icon">
                <IconKey size={13} />
              </span>
              <input
                type="password"
                className="nx-input"
                value={cfg.apiKey}
                onChange={(e) => setCfg({ ...cfg, apiKey: e.target.value })}
                placeholder="sk-…"
              />
            </div>
          </div>

          <div className="mb-3 flex flex-wrap gap-2">
            <div className="min-w-[180px] flex-1">
              <label className="nx-label">模型</label>
              <input
                className="nx-input font-mono"
                value={cfg.model}
                onChange={(e) => setCfg({ ...cfg, model: e.target.value })}
              />
            </div>
            <div className="w-[150px]">
              <label className="nx-label">上下文窗口 (1k–2M)</label>
              <input
                type="number"
                className="nx-input"
                value={cfg.contextWindow}
                onChange={(e) => setCfg({ ...cfg, contextWindow: Number(e.target.value) })}
              />
            </div>
            <div className="w-[92px]">
              <label className="nx-label">温度</label>
              <input
                type="number"
                step="0.1"
                className="nx-input"
                value={cfg.temperature}
                onChange={(e) => setCfg({ ...cfg, temperature: Number(e.target.value) })}
              />
            </div>
          </div>

          <div className="nx-form-row">
            <label className="nx-label">代理（留空 = 跟随系统；注意不要让它影响 SSH 认证）</label>
            <input
              className="nx-input font-mono"
              value={cfg.proxy}
              onChange={(e) => setCfg({ ...cfg, proxy: e.target.value })}
              placeholder="http://127.0.0.1:7890"
            />
          </div>

          <div className="flex items-center gap-2">
            <button className="nx-btn nx-btn-primary" onClick={() => void save()}>
              保存
            </button>
            <button className="nx-btn nx-btn-outline" disabled={testing} onClick={() => void test()}>
              {testing ? <IconRefresh size={13} className="animate-spin" /> : <IconZap size={13} />}
              {testing ? "测试中…" : "连通性测试（两步）"}
            </button>
          </div>

          {testResult && (
            <div
              className={`mt-3.5 nx-alert ${testResult.modelsOk && testResult.chatOk ? "" : "nx-alert-danger"}`}
            >
              <div className="mb-1 flex items-center gap-1.5 font-semibold">
                {testResult.modelsOk && testResult.chatOk ? (
                  <IconCheckCircle size={13} />
                ) : (
                  <IconXCircle size={13} />
                )}
                测试结果
              </div>
              <pre className="whitespace-pre-wrap font-mono text-[11px]">{testResult.detail}</pre>
            </div>
          )}
        </section>

        {/* 凭据库 */}
        <section className="nx-card">
          <div className="mb-1 flex items-center gap-2">
            <IconLock size={15} className="text-neutral-400" />
            <span className="nx-card-title">凭据库</span>
            <span
              className={`nx-badge ${
                vault?.unlocked ? "nx-badge-green" : vault?.initialized ? "nx-badge-amber" : ""
              }`}
            >
              {vault ? (vault.initialized ? (vault.unlocked ? "已解锁" : "已锁定") : "未初始化") : "读取中…"}
            </span>
          </div>
          <p className="nx-hint mb-3.5">
            主密码 → Argon2id 派生 KEK → 包裹 DEK；凭据用 XChaCha20-Poly1305 加密。
            也可以走 Windows DPAPI 免主密码模式。
          </p>

          {!vault?.initialized && (
            <div className="flex flex-wrap items-center gap-2">
              <input
                type="password"
                className="nx-input max-w-[240px]"
                placeholder="设置主密码（≥8 位）"
                value={masterPwd}
                onChange={(e) => setMasterPwd(e.target.value)}
              />
              <button
                className="nx-btn nx-btn-primary"
                disabled={masterPwd.length < 8}
                onClick={() =>
                  void vaultApi
                    .initMaster(masterPwd)
                    .then(() => {
                      pushToast("success", "凭据库已初始化（主密码模式）");
                      return vaultApi.status().then(setVault);
                    })
                    .catch((e) => pushToast("error", String(e)))
                }
              >
                初始化（主密码）
              </button>
              <button
                className="nx-btn nx-btn-outline"
                onClick={() =>
                  void vaultApi
                    .initDpapi()
                    .then(() => {
                      pushToast("success", "凭据库已初始化（DPAPI 模式）");
                      return vaultApi.status().then(setVault);
                    })
                    .catch((e) => pushToast("error", String(e)))
                }
              >
                初始化（Windows DPAPI，无主密码）
              </button>
            </div>
          )}

          {vault?.initialized && !vault.unlocked && vault.mode === "master" && (
            <div className="flex flex-wrap items-center gap-2">
              <input
                type="password"
                className="nx-input max-w-[240px]"
                placeholder="主密码"
                value={unlockPwd}
                onChange={(e) => setUnlockPwd(e.target.value)}
              />
              <button
                className="nx-btn nx-btn-primary"
                onClick={() =>
                  void vaultApi
                    .unlock(unlockPwd)
                    .then(() => {
                      pushToast("success", "已解锁");
                      return vaultApi.status().then(setVault);
                    })
                    .catch((e) => pushToast("error", String(e)))
                }
              >
                <IconUnlock size={13} />
                解锁
              </button>
            </div>
          )}

          {vault?.unlocked && (
            <button
              className="nx-btn nx-btn-outline nx-btn-sm"
              onClick={() =>
                void vaultApi
                  .lock()
                  .then(() => vaultApi.status().then(setVault))
                  .catch((e) => pushToast("error", String(e)))
              }
            >
              <IconLock size={13} />
              立即锁定
            </button>
          )}
        </section>

        <McpSection />

        {/* 快捷键速查 */}
        <section className="nx-card">
          <div className="mb-3 flex items-center gap-2">
            <IconSettings size={15} className="text-neutral-400" />
            <span className="nx-card-title">快捷键</span>
          </div>
          <div className="grid grid-cols-2 gap-x-6 gap-y-1.5">
            {(
              [
                ["Ctrl+Shift+P / Ctrl+K", "命令面板"],
                ["Ctrl+T", "新建本地终端"],
                ["Ctrl+B", "收起 / 展开资产树"],
                ["Ctrl+J", "收起 / 展开 AI 侧栏"],
                ["Ctrl+W", "关闭当前标签"],
                ["Ctrl+F", "终端内搜索"],
                ["Esc", "AI 接管中一键夺回"],
                ["Ctrl+Enter", "SQL 编辑器内运行"],
              ] as [string, string][]
            ).map(([k, label]) => (
              <div key={k} className="flex items-center gap-2 text-xs text-neutral-400">
                <span className="nx-kbd">{k}</span>
                <span>{label}</span>
              </div>
            ))}
          </div>
        </section>
      </div>
    </div>
  );
}
