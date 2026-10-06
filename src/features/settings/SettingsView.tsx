// 设置页：模型档案管理（与 AI 侧栏共用 ModelManager）+ 凭据库。
//
// 模型设置以前在这里单开了一份「单 provider 表单」，和 AI 侧栏的多档案面板是两份重复且
// 版本落后的实现 —— 现在统一内联复用 `ModelManager`，这里不再碰 aiApi 的
// getProvider / setProvider / presets（连通性测试除外，见下方说明）。
import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  aiApi,
  vaultApi,
  type AiPermissionConfig,
  type VaultStatus,
} from "../../ipc/commands";
import { useUi } from "../../app/store";
import { DEMO, WEB } from "../../demo";
import { ask, promptText } from "../../ui/dialogs";
import { ModelManager } from "../ai/ModelPanel";
import { SyncCard } from "./SyncCard";
import { describeError } from "../../ui/errorText";
import {
  IconCheckCircle,
  IconClose,
  IconInfo,
  IconLock,
  IconPlus,
  IconRefresh,
  IconSettings,
  IconShield,
  IconSparkles,
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

  // 凭据保护（重做后：设置页只留一个开关；解锁 / 立即锁定挪到「凭据」页）
  const [vault, setVault] = useState<VaultStatus | null>(null);
  const [protPwd, setProtPwd] = useState("");
  const [pendingEnable, setPendingEnable] = useState(false);

  // 开关一变，凭据页 / 状态栏的 vault-status 缓存都要跟着失效
  const qc = useQueryClient();
  const refreshVault = () => {
    void qc.invalidateQueries({ queryKey: ["vault-status"] });
    return vaultApi
      .status()
      .then(setVault)
      .catch(() => undefined);
  };
  useEffect(() => {
    void vaultApi
      .status()
      .then(setVault)
      .catch(() => undefined);
  }, []);

  // ── AI 拦截规则（从 AI 侧栏的权限浮层挪进来的规则库）──────────────────
  const [aiPerm, setAiPerm] = useState<AiPermissionConfig | null>(null);
  const [ruleDraft, setRuleDraft] = useState("");
  const [ruleDraftOpen, setRuleDraftOpen] = useState(false);
  /** AI 侧栏确认卡片「加为拦截规则」跳过来时带的预填命令。 */
  const rulePrefill = useUi((s) => s.aiRulePrefill);

  useEffect(() => {
    void aiApi
      .getPermission()
      .then(setAiPerm)
      .catch(() => undefined);
  }, []);

  useEffect(() => {
    if (rulePrefill === null) return;
    setRuleDraft(rulePrefill);
    setRuleDraftOpen(true);
    useUi.getState().setAiRulePrefill(null);
  }, [rulePrefill]);

  /**
   * 保存规则：先取最新配置再改 —— 权限档位可能同时在 AI 侧栏被切，
   * 拿着手里的旧对象整包 setPermission 会把档位也覆盖回去。
   */
  const saveRules = async (rules: string[]) => {
    try {
      const latest = await aiApi.getPermission();
      const next = { ...latest, dangerRules: rules };
      setAiPerm(next);
      await aiApi.setPermission(next);
    } catch (e) {
      pushToast("error", `保存拦截规则失败：${describeError(e)}`);
      void aiApi
        .getPermission()
        .then(setAiPerm)
        .catch(() => undefined);
    }
  };

  /** 提交新增规则（Enter 或失焦）：空值和重复都不落库，草稿行一律收起。 */
  const addRule = () => {
    const r = ruleDraft.trim();
    setRuleDraft("");
    setRuleDraftOpen(false);
    if (!r || !aiPerm) return;
    if (aiPerm.dangerRules.some((x) => x.toLowerCase() === r.toLowerCase())) return;
    void saveRules([...aiPerm.dangerRules, r]);
  };

  /** 改一条已有规则：返回是否真的落了库（空值 / 重复当"没改"处理）。 */
  const editRule = (orig: string, next: string): boolean => {
    if (!aiPerm) return false;
    const r = next.trim();
    if (!r || r === orig) return false;
    if (aiPerm.dangerRules.some((x) => x !== orig && x.toLowerCase() === r.toLowerCase())) {
      return false;
    }
    void saveRules(aiPerm.dangerRules.map((x) => (x === orig ? r : x)));
    return true;
  };

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
      setTestResult({ modelsOk: false, chatOk: false, fatal: describeError(e) });
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

        {/* AI 拦截规则：侧栏权限浮层只留一行入口，规则库整套在这里 */}
        <section className="nx-card">
          <div className="mb-1 flex items-center gap-2">
            <IconShield size={15} className="text-neutral-400" />
            <span className="nx-card-title">AI 拦截规则</span>
            {aiPerm && (
              <span className={`nx-badge ${aiPerm.dangerRules.length ? "nx-badge-amber" : ""}`}>
                {aiPerm.dangerRules.length} 条
              </span>
            )}
          </div>
          <p className="nx-hint mb-3.5">
            命中即拦截，静默模式同样生效；子串匹配，忽略大小写。
          </p>

          <div className="mb-1.5 flex flex-col gap-1">
            {(aiPerm?.dangerRules ?? []).map((r) => (
              <div key={r} className="flex items-center gap-1">
                <input
                  className="nx-input nx-input-sm min-w-0 flex-1 font-mono"
                  defaultValue={r}
                  title={r}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      // Enter 只负责"提交"，真正保存交给 onBlur —— 两处都写会存两遍
                      e.currentTarget.blur();
                    }
                  }}
                  onBlur={(e) => {
                    // 空值 / 重复都不会落库，输入框还原成库里的那条，
                    // 否则界面显示的和真实生效的规则对不上
                    if (!editRule(r, e.currentTarget.value)) e.currentTarget.value = r;
                  }}
                />
                <button
                  className="nx-icon-btn nx-icon-btn-sm"
                  title="删除这条规则"
                  onClick={() =>
                    void saveRules((aiPerm?.dangerRules ?? []).filter((x) => x !== r))
                  }
                >
                  <IconClose size={11} />
                </button>
              </div>
            ))}
            {ruleDraftOpen && (
              <input
                autoFocus
                className="nx-input nx-input-sm w-full font-mono"
                placeholder="例如 kubectl delete"
                value={ruleDraft}
                onChange={(e) => setRuleDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    e.preventDefault();
                    addRule();
                  }
                  if (e.key === "Escape") {
                    setRuleDraft("");
                    setRuleDraftOpen(false);
                  }
                }}
                onBlur={addRule}
              />
            )}
            {aiPerm && aiPerm.dangerRules.length === 0 && !ruleDraftOpen && (
              <div className="nx-hint text-[11px]">还没有拦截规则</div>
            )}
          </div>
          <button className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => setRuleDraftOpen(true)}>
            <IconPlus size={11} />
            添加一条规则
          </button>

          <p className="nx-hint mt-3 border-t border-neutral-800/60 pt-2 text-[11px]">
            规则命中 → 先确认；不可逆操作（格式化磁盘、清空系统目录、删库）→ 直接拒绝。
          </p>
        </section>

        {/* 凭据保护：文案只写"会发生什么"，不出现算法与密钥层级 */}
        <section className="nx-card">
          <div className="mb-1 flex items-center gap-2">
            <IconLock size={15} className="text-neutral-400" />
            <span className="nx-card-title">凭据保护</span>
            {vault &&
              !WEB &&
              (vault.mode === "master" ? (
                <span className="nx-badge nx-badge-green">已开启</span>
              ) : (
                <span className="nx-badge">未开启</span>
              ))}
          </div>

          {WEB && (
            <p className="nx-hint">
              浏览器版的凭据由服务端的部署密钥托管，启动时自动解锁 ——
              这里没有密码开关，也不需要手动解锁。
            </p>
          )}

          <div className={WEB ? "hidden" : "flex items-start gap-3"}>
            <div className="min-w-0 flex-1">
              <div className="text-[12.5px] text-neutral-200">用密码保护凭据</div>
              <p className="nx-hint mt-0.5">
                {vault?.mode === "master"
                  ? "开启：每次启动需输入密码后方可使用凭据。"
                  : "关闭：无需密码，启动后凭据直接可用。"}
              </p>
            </div>
            <input
              type="checkbox"
              className="mt-0.5 h-4 w-4 shrink-0"
              checked={vault?.mode === "master"}
              onChange={() => {
                if (vault?.mode === "master") {
                  // 关闭不是无感的：要明确知道"从此不需要密码"
                  void ask("关闭密码保护？\n关闭后凭据无需密码即可使用。").then((ok) => {
                    if (!ok) return;
                    void vaultApi
                      .initDpapi()
                      .then(() => {
                        pushToast("success", "已关闭密码保护");
                        return refreshVault();
                      })
                      .catch((e) => pushToast("error", describeError(e)));
                  });
                } else {
                  setProtPwd("");
                  setPendingEnable(true);
                }
              }}
            />
          </div>

          {!WEB && pendingEnable && vault?.mode !== "master" && (
            <div className="mt-3 flex flex-wrap items-center gap-2">
              <input
                type="password"
                className="nx-input max-w-[240px]"
                placeholder="设置保护密码（至少 8 位）"
                value={protPwd}
                autoComplete="off"
                onChange={(e) => setProtPwd(e.target.value)}
              />
              <button
                className="nx-btn nx-btn-primary"
                disabled={protPwd.length < 8}
                onClick={() =>
                  void vaultApi
                    .initMaster(protPwd)
                    .then(() => {
                      setPendingEnable(false);
                      pushToast("success", "已开启密码保护 · 下次启动生效");
                      return refreshVault();
                    })
                    .catch((e) => pushToast("error", describeError(e)))
                }
              >
                启用保护
              </button>
              <button className="nx-btn nx-btn-ghost" onClick={() => setPendingEnable(false)}>
                取消
              </button>
            </div>
          )}

          {!WEB && vault?.mode === "master" && (
            <div className="mt-3 flex items-center gap-2">
              <span className="nx-hint">锁定与解锁在「凭据」页</span>
              <button
                className="nx-btn nx-btn-outline nx-btn-sm"
                onClick={() =>
                  void (async () => {
                    const old = await promptText("输入当前保护密码：", "", { secret: true });
                    if (old === null) return;
                    const next = await promptText("输入新密码（至少 8 位）：", "", { secret: true });
                    if (next === null || next.length < 8) {
                      if (next !== null) pushToast("error", "新密码至少 8 位");
                      return;
                    }
                    try {
                      await vaultApi.changePassword(old, next);
                      pushToast("success", "密码已修改 · 凭据不受影响");
                    } catch (e) {
                      pushToast("error", describeError(e));
                    }
                  })()
                }
              >
                修改密码
              </button>
            </div>
          )}
        </section>

        {/* 资产同步：桌面与微服之间搬资产。桌面是发起方，浏览器版是被同步的一端 */}
        <SyncCard />

        {/* 快捷键速查。
            ⚠️ `@container` 挂在这一段上、阈值给下面的网格用：窄面板里「两列」会把
            每个单元压到 ~135px，而最长那条 `Ctrl+Shift+P / Ctrl+K` 自己就占 ~130px，
            于是右边的中文标签被挤成**一列单字**（实测 12×64 的竖排「命令面板」）。
            容器查询判的是这一段的宽度 = 主区宽度，跟视口无关（视口断点在这里判错：
            1080 宽 + 开着 AI 侧栏，主区也只有 366px）。 */}
        <section className="nx-card @container">
          <div className="mb-3 flex items-center gap-2">
            <IconSettings size={15} className="text-neutral-400" />
            <span className="nx-card-title">快捷键</span>
          </div>
          <div className="grid grid-cols-1 gap-x-6 gap-y-1.5 @min-[400px]:grid-cols-2">
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
                <span className="nx-kbd shrink-0">{k}</span>
                <span className="whitespace-nowrap">{label}</span>
              </div>
            ))}
          </div>
        </section>
      </div>
    </div>
  );
}
