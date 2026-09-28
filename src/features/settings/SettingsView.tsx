// 设置页：模型档案管理（与 AI 侧栏共用 ModelManager）+ 凭据库。
//
// 模型设置以前在这里单开了一份「单 provider 表单」，和 AI 侧栏的多档案面板是两份重复且
// 版本落后的实现 —— 现在统一内联复用 `ModelManager`，这里不再碰 aiApi 的
// getProvider / setProvider / presets（连通性测试除外，见下方说明）。
import { useEffect, useState } from "react";
import { aiApi, vaultApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { DEMO } from "../../demo";
import { ModelManager } from "../ai/ModelPanel";
import {
  IconCheckCircle,
  IconInfo,
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

  // 连通性测试的结果。原先这里先把表单值 setProvider 再测；现在只测激活档案，
  // 所以除了两步的成败，还要把各自的错误信息留着给用户看。
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{
    modelsOk: boolean;
    chatOk: boolean;
    modelsError?: string;
    chatError?: string;
    /** 连调用本身都失败（IPC / 后端异常）时的兜底说明。 */
    fatal?: string;
  } | null>(null);

  // 凭据库
  const [vault, setVault] = useState<{
    initialized: boolean;
    mode: string;
    unlocked: boolean;
  } | null>(null);
  const [masterPwd, setMasterPwd] = useState("");
  const [unlockPwd, setUnlockPwd] = useState("");

  useEffect(() => {
    void vaultApi.status().then(setVault).catch(() => undefined);
  }, []);

  /**
   * 两步连通性测试。
   *
   * 为什么只能测「当前激活的模型」：后端 `ai_test_provider` 测的是**运行时生效的那份
   * provider**，不接受前端临时塞一份配置（这正是它与旧版 setProvider + 测试的差别）。
   * 想验证一份还没保存的草稿，请用模型管理区里的「刷新模型列表」。
   */
  const test = async () => {
    setTesting(true);
    setTestResult(null);
    try {
      const r = await aiApi.testProvider();
      setTestResult({
        modelsOk: r.modelsOk,
        chatOk: r.chatOk,
        modelsError: r.modelsError,
        chatError: r.chatError,
      });
    } catch (e) {
      setTestResult({ modelsOk: false, chatOk: false, fatal: String(e) });
    } finally {
      setTesting(false);
    }
  };

  const testPassed = !!testResult && testResult.modelsOk && testResult.chatOk;
  const testDetail = testResult
    ? testResult.fatal
      ? `调用失败：${testResult.fatal}`
      : [
          `模型列表：${testResult.modelsOk ? "可用" : `失败 — ${testResult.modelsError ?? "未知错误"}`}`,
          `实际对话：${testResult.chatOk ? "可用" : `失败 — ${testResult.chatError ?? "未知错误"}`}`,
        ].join("\n")
    : "";

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

        {/* AI 模型：内联复用侧栏那套多档案管理，避免两份实现漂移 */}
        <section className="nx-card">
          <div className="mb-1 flex items-center gap-2">
            <IconSparkles size={15} className="text-blue-300" />
            <span className="nx-card-title">AI 模型</span>
            <span className="nx-badge">OpenAI 兼容协议 · BYOK</span>
          </div>
          <p className="nx-hint mb-3.5">
            可存多份模型档案（不同厂商 / 不同 Key），选中一份「设为当前」供 AI 使用。
            密钥以明文存在本机 sqlite，不会上传；模型可换成本地 Ollama / LM Studio / vLLM。
          </p>

          <ModelManager />

          {/* 连通性测试：只能针对激活档案，故独立成块放在管理区下方 */}
          <div className="mt-4 border-t border-neutral-800/60 pt-3.5">
            <div className="flex flex-wrap items-center gap-2">
              <button className="nx-btn nx-btn-outline" disabled={testing} onClick={() => void test()}>
                {testing ? <IconRefresh size={13} className="animate-spin" /> : <IconZap size={13} />}
                {testing ? "测试中…" : "连通性测试（两步）· 当前激活的模型"}
              </button>
              <span className="nx-hint">
                只能测当前激活的模型：后端测的是运行时生效的那份 provider，不能临时塞一份没保存的草稿；
                要验证草稿的连接参数，请用上方的「刷新模型列表」。
              </span>
            </div>

            {testResult && (
              <div className={`mt-3.5 nx-alert ${testPassed ? "" : "nx-alert-danger"}`}>
                <div className="mb-1 flex items-center gap-1.5 font-semibold">
                  {testPassed ? <IconCheckCircle size={13} /> : <IconXCircle size={13} />}
                  测试结果
                </div>
                <pre className="whitespace-pre-wrap font-mono text-[11px]">{testDetail}</pre>
              </div>
            )}
          </div>
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
