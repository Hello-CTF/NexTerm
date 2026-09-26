// 设置页：AI 提供方（预设/自动填参/两步连通性测试）+ 凭据库 + MCP。
import { useEffect, useState } from "react";
import { aiApi, vaultApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { McpSection } from "./McpSection";

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
  const [testResult, setTestResult] = useState<string | null>(null);

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

  const save = async () => {
    try {
      await aiApi.setProvider({
        baseUrl: cfg.baseUrl,
        apiKey: cfg.apiKey,
        model: cfg.model,
        temperature: cfg.temperature,
        contextWindow: cfg.contextWindow,
        proxy: cfg.proxy || null,
        stream: cfg.stream,
      });
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
      await aiApi.setProvider({
        baseUrl: cfg.baseUrl,
        apiKey: cfg.apiKey,
        model: cfg.model,
        temperature: cfg.temperature,
        contextWindow: cfg.contextWindow,
        proxy: cfg.proxy || null,
        stream: cfg.stream,
      });
      const r = await aiApi.testProvider();
      setTestResult(
        `模型列表: ${r.modelsOk ? "✓ 可用" : `✕ ${r.modelsError ?? "失败"}`}\n实际对话: ${r.chatOk ? "✓ 可用" : `✕ ${r.chatError ?? "失败"}`}`,
      );
    } catch (e) {
      setTestResult(`测试失败: ${String(e)}`);
    } finally {
      setTesting(false);
    }
  };

  return (
    <div className="h-full overflow-y-auto bg-neutral-900 p-6 text-sm text-neutral-300">
      <h2 className="mb-4 text-lg font-medium text-neutral-100">设置</h2>

      {/* AI */}
      <section className="mb-8 max-w-2xl rounded-lg border border-neutral-800 p-4">
        <h3 className="mb-3 font-medium">AI 模型（OpenAI 兼容协议 · BYOK）</h3>
        <div className="mb-3 flex flex-wrap gap-1">
          {presets.map((p) => (
            <button
              key={p}
              className={`rounded px-2 py-0.5 text-xs ${
                preset === p ? "bg-blue-600 text-white" : "bg-neutral-800 hover:bg-neutral-700"
              }`}
              onClick={() => applyPreset(p)}
            >
              {p}
            </button>
          ))}
        </div>
        <label className="mb-1 block text-xs text-neutral-500">Base URL</label>
        <input
          className="mb-2 w-full rounded bg-neutral-800 px-2 py-1 outline-none"
          value={cfg.baseUrl}
          onChange={(e) => setCfg({ ...cfg, baseUrl: e.target.value })}
          placeholder="https://api.deepseek.com/v1"
        />
        <label className="mb-1 block text-xs text-neutral-500">API Key（仅存本机加密库）</label>
        <input
          type="password"
          className="mb-2 w-full rounded bg-neutral-800 px-2 py-1 outline-none"
          value={cfg.apiKey}
          onChange={(e) => setCfg({ ...cfg, apiKey: e.target.value })}
        />
        <div className="mb-2 flex gap-2">
          <div className="flex-1">
            <label className="mb-1 block text-xs text-neutral-500">模型</label>
            <input
              className="w-full rounded bg-neutral-800 px-2 py-1 outline-none"
              value={cfg.model}
              onChange={(e) => setCfg({ ...cfg, model: e.target.value })}
            />
          </div>
          <div className="w-36">
            <label className="mb-1 block text-xs text-neutral-500">上下文窗口 (1k–2M)</label>
            <input
              type="number"
              className="w-full rounded bg-neutral-800 px-2 py-1 outline-none"
              value={cfg.contextWindow}
              onChange={(e) => setCfg({ ...cfg, contextWindow: Number(e.target.value) })}
            />
          </div>
          <div className="w-24">
            <label className="mb-1 block text-xs text-neutral-500">温度</label>
            <input
              type="number"
              step="0.1"
              className="w-full rounded bg-neutral-800 px-2 py-1 outline-none"
              value={cfg.temperature}
              onChange={(e) => setCfg({ ...cfg, temperature: Number(e.target.value) })}
            />
          </div>
        </div>
        <label className="mb-1 block text-xs text-neutral-500">代理（留空 = 跟随系统）</label>
        <input
          className="mb-3 w-full rounded bg-neutral-800 px-2 py-1 outline-none"
          value={cfg.proxy}
          onChange={(e) => setCfg({ ...cfg, proxy: e.target.value })}
          placeholder="http://127.0.0.1:7890"
        />
        <div className="flex items-center gap-2">
          <button
            className="rounded bg-blue-600 px-4 py-1.5 text-white hover:bg-blue-500"
            onClick={() => void save()}
          >
            保存
          </button>
          <button
            className="rounded bg-neutral-700 px-4 py-1.5 hover:bg-neutral-600 disabled:opacity-50"
            disabled={testing}
            onClick={() => void test()}
          >
            {testing ? "测试中…" : "连通性测试（两步）"}
          </button>
        </div>
        {testResult && (
          <pre className="mt-3 rounded bg-neutral-950 p-2 font-mono text-xs text-neutral-300">
            {testResult}
          </pre>
        )}
      </section>

      {/* 凭据库 */}
      <section className="mb-8 max-w-2xl rounded-lg border border-neutral-800 p-4">
        <h3 className="mb-3 font-medium">凭据库</h3>
        <div className="mb-3 text-xs text-neutral-500">
          状态：{vault ? (vault.initialized ? `已初始化（${vault.mode}）` : "未初始化") : "…"}
          {vault?.initialized && (vault.unlocked ? " · 已解锁" : " · 已锁定")}
        </div>
        {!vault?.initialized && (
          <div className="mb-3 flex flex-wrap items-center gap-2">
            <input
              type="password"
              className="w-56 rounded bg-neutral-800 px-2 py-1 outline-none"
              placeholder="设置主密码（≥8 位）"
              value={masterPwd}
              onChange={(e) => setMasterPwd(e.target.value)}
            />
            <button
              className="rounded bg-blue-600 px-3 py-1 text-white hover:bg-blue-500 disabled:opacity-50"
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
              初始化（主密码模式）
            </button>
            <button
              className="rounded bg-neutral-700 px-3 py-1 hover:bg-neutral-600"
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
          <div className="flex items-center gap-2">
            <input
              type="password"
              className="w-56 rounded bg-neutral-800 px-2 py-1 outline-none"
              placeholder="主密码"
              value={unlockPwd}
              onChange={(e) => setUnlockPwd(e.target.value)}
            />
            <button
              className="rounded bg-green-600 px-3 py-1 text-white hover:bg-green-500"
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
              解锁
            </button>
          </div>
        )}
        {vault?.unlocked && (
          <button
            className="rounded bg-neutral-700 px-3 py-1 text-xs hover:bg-neutral-600"
            onClick={() =>
              void vaultApi
                .lock()
                .then(() => vaultApi.status().then(setVault))
                .catch((e) => pushToast("error", String(e)))
            }
          >
            立即锁定
          </button>
        )}
      </section>
      <McpSection />
    </div>
  );
}
