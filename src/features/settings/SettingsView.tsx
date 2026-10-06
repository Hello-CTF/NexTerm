import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  aiApi,
  vaultApi,
  type AiPermissionConfig,
  type VaultStatus,
} from "../../ipc/commands";
import { useUi } from "../../app/store";
import { DEMO } from "../../demo";
import { ask, promptText } from "../../ui/dialogs";
import { isImeKeyEvent } from "../../ui/DialogHost";
import { ModelManager } from "../ai/ModelPanel";
import { AppearanceCard } from "./AppearanceCard";
import { KnownHostsCard } from "./KnownHostsCard";
import { MemoryCard } from "./MemoryCard";
import { CronCard } from "./CronCard";
import { ShortcutsCard } from "./ShortcutsCard";
import { SyncCard } from "./SyncCard";
import { SyncBundleCard } from "./SyncBundleCard";
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
  IconTerminal,
  IconXCircle,
  IconZap,
} from "../../ui/icons";

export function SettingsView() {
  const { pushToast } = useUi();
  const terminalOsc52 = useUi((s) => s.terminalOsc52);
  const setTerminalOsc52 = useUi((s) => s.setTerminalOsc52);

  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{
    modelsOk: boolean;
    chatOk: boolean;
    modelsError?: string | null;
    chatError?: string | null;
    fatal?: string;
  } | null>(null);

  const [vault, setVault] = useState<VaultStatus | null>(null);
  const [protPwd, setProtPwd] = useState("");
  const [pendingEnable, setPendingEnable] = useState(false);
  const [autoLockDraft, setAutoLockDraft] = useState("");

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
  useEffect(() => {
    setAutoLockDraft(vault ? String(vault.autoLockMinutes) : "");
  }, [vault]);

  const saveAutoLock = () => {
    const trimmed = autoLockDraft.trim();
    const minutes = Number(trimmed);
    if (trimmed === "" || !Number.isInteger(minutes) || minutes < 0 || minutes > 1440) {
      pushToast("error", "自动锁时长需为 0（禁用）至 1440 之间的整数分钟");
      return;
    }
    void vaultApi
      .setAutoLock(minutes)
      .then(() => {
        pushToast("success", minutes === 0 ? "已禁用闲置自动锁定" : `闲置 ${minutes} 分钟后自动锁定`);
        if (vault) setVault({ ...vault, autoLockMinutes: minutes });
        void qc.invalidateQueries({ queryKey: ["vault-status"] });
      })
      .catch((e) => pushToast("error", describeError(e)));
  };

  const [aiPerm, setAiPerm] = useState<AiPermissionConfig | null>(null);
  const [ruleDraft, setRuleDraft] = useState("");
  const [ruleDraftOpen, setRuleDraftOpen] = useState(false);
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

  const addRule = () => {
    const r = ruleDraft.trim();
    setRuleDraft("");
    setRuleDraftOpen(false);
    if (!r || !aiPerm) return;
    if (aiPerm.dangerRules.some((x) => x.toLowerCase() === r.toLowerCase())) return;
    void saveRules([...aiPerm.dangerRules, r]);
  };

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
          `模型列表：${testResult.modelsOk ? "可用" : `失败 — ${testResult.modelsError ?? "原因未查明"}`}`,
          `实际对话：${testResult.chatOk ? "可用" : `失败 — ${testResult.chatError ?? "原因未查明"}`}`,
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
              输入的内容也不会外发。想接真实后端请启动桌面端或 nexterm-server（URL 加 <span className="nx-code">?demo=0</span> 可关闭）。
            </div>
          </section>
        )}

        <AppearanceCard />

        <section className="nx-card">
          <div className="mb-1 flex items-center gap-2">
            <IconTerminal size={15} className="text-neutral-400" />
            <span className="nx-card-title">终端</span>
            <span className={`nx-badge ${terminalOsc52 ? "nx-badge-amber" : ""}`}>
              {terminalOsc52 ? "OSC 52 已开启" : "OSC 52 已关闭"}
            </span>
          </div>
          <label className="flex cursor-pointer items-start gap-3">
            <div className="min-w-0 flex-1">
              <div className="text-[12.5px] text-neutral-200">允许远程主机写入剪贴板（OSC 52）</div>
              <p className="nx-hint mt-0.5">
                {terminalOsc52
                  ? "开启后，远程主机可以通过转义序列直接改写你的系统剪贴板。只在可信的主机与网络环境下保持开启。"
                  : "默认关闭。远程主机发来的 OSC 52 剪贴板写入会被拒绝，并在终端里明确提示，不会静默写入。"}
              </p>
            </div>
            <input
              type="checkbox"
              className="mt-0.5 h-4 w-4 shrink-0"
              checked={terminalOsc52}
              onChange={(e) => setTerminalOsc52(e.target.checked)}
            />
          </label>
        </section>

        <section className="nx-card">
          <div className="mb-1 flex flex-wrap items-center gap-2">
            <IconSparkles size={15} className="text-blue-300" />
            <span className="nx-card-title">AI 模型</span>
            <span className="nx-badge">OpenAI 兼容协议 · 自带密钥</span>
          </div>
          <p className="nx-hint mb-3.5">
            可存多份模型档案（不同厂商 / 不同 Key），选中一份「设为当前」供 AI 使用。
            密钥加密后存在本机 sqlite，不会上传；模型可换成本地 Ollama / LM Studio / vLLM。
          </p>

          <ModelManager />

          <div className="mt-4 border-t border-neutral-800/60 pt-3.5">
            <div className="flex flex-wrap items-center gap-2">
              <button
                className="nx-btn nx-btn-outline h-auto min-h-7 shrink py-1 text-left whitespace-normal"
                disabled={testing}
                onClick={() => void test()}
              >
                {testing ? (
                  <IconRefresh size={13} className="shrink-0 animate-spin" />
                ) : (
                  <IconZap size={13} className="shrink-0" />
                )}
                {testing ? "测试中…" : "连通性测试（两步）· 当前模型"}
              </button>
              <span className="nx-hint">
                只能测当前模型：后端测的是运行时生效的那份 provider，不能临时塞一份没保存的草稿；
                要验证草稿的连接参数，请用上方的「刷新模型列表」。
              </span>
            </div>

            {testResult && (
              <div
                className={`mt-3.5 nx-alert ${
                  testPassed
                    ? "border-green-500/40 bg-[color-mix(in_srgb,var(--color-green-500)_18%,var(--nx-bg-pane))] text-green-300"
                    : "nx-alert-danger"
                }`}
              >
                <div className="mb-1 flex items-center gap-1.5 font-semibold">
                  {testPassed ? <IconCheckCircle size={13} /> : <IconXCircle size={13} />}
                  测试结果
                </div>
                <pre className="whitespace-pre-wrap font-mono text-[11px]">{testDetail}</pre>
              </div>
            )}
          </div>
        </section>

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
                    if (e.key === "Enter" && !isImeKeyEvent(e)) {
                      e.preventDefault();
                      e.currentTarget.blur();
                    }
                  }}
                  onBlur={(e) => {
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
                  if (e.key === "Enter" && !isImeKeyEvent(e)) {
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

        <MemoryCard />

        <CronCard />

        <section className="nx-card">
          <div className="mb-1 flex items-center gap-2">
            <IconLock size={15} className="text-neutral-400" />
            <span className="nx-card-title">凭据保护</span>
            {vault &&
              (vault.mode === "master" ? (
                <span className="nx-badge nx-badge-green">已开启</span>
              ) : vault.mode === "not_init" ? (
                <span className="nx-badge nx-badge-amber">未初始化</span>
              ) : (
                <span className="nx-badge">未开启</span>
              ))}
          </div>

          <label className="flex cursor-pointer items-start gap-3">
            <div className="min-w-0 flex-1">
              <div className="text-[12.5px] text-neutral-200">用密码保护凭据</div>
              <p className="nx-hint mt-0.5">
                {vault?.mode === "master"
                  ? "开启：每次启动需输入密码后方可使用凭据。"
                  : vault?.mode === "not_init"
                    ? "尚未初始化：打开这个开关并设置保护密码即可完成初始化；初始化前无法保存任何密码或私钥。"
                    : "关闭：无需密码，凭据由系统级密钥保护，启动后直接使用。"}
              </p>
            </div>
            <input
              type="checkbox"
              className="mt-0.5 h-4 w-4 shrink-0"
              checked={vault?.mode === "master"}
              onChange={() => {
                if (vault?.mode === "master") {
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
          </label>

          {pendingEnable && vault?.mode !== "master" && (
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
                      pushToast("success", "已开启密码保护");
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
              <p className="nx-hint w-full text-[11px]">
                保护密码只在本机使用，不会上传；忘记后无法找回，已保存的凭据将永远无法解密。
              </p>
            </div>
          )}

          {vault?.mode === "master" && (
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

          {vault?.mode === "master" && (
            <div className="mt-3 border-t border-neutral-800/60 pt-3">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-[12.5px] text-neutral-200">闲置自动锁定</span>
                <input
                  type="number"
                  className="nx-input nx-input-sm w-[96px]"
                  min={0}
                  max={1440}
                  step={1}
                  value={autoLockDraft}
                  onChange={(e) => setAutoLockDraft(e.target.value)}
                />
                <span className="nx-hint">分钟，0 为禁用</span>
                <button className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => void saveAutoLock()}>
                  保存
                </button>
              </div>
              <p className="nx-hint mt-1.5">
                闲置超过该时长后凭据库自动锁定，再次使用需输入保护密码；保存后立即生效，重启后保持。
              </p>
            </div>
          )}
        </section>

        <KnownHostsCard />

        <SyncCard />

        <SyncBundleCard />

        <ShortcutsCard />
      </div>
    </div>
  );
}
