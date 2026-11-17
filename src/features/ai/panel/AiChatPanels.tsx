import { IconShield, IconClose, IconRefresh, IconList } from "../../../ui/icons";
import { GrantPanel } from "../GrantPanel";
import { useUi } from "../../../app/store";
import { MODE_LABEL, MODE_OPTIONS, PLAN_MODE_HINT, appliedPermissionMode, fullAccessConfirmText } from "./aiSidebarShared";
import { ask } from "../../../ui/dialogs";
import type { AiPermissionMode } from "../../../ipc/commands";
import { BackgroundTasksPanel } from "./BackgroundTasksPanel";
import type { AiChatCtx } from "./aiChatCtx";
import { t } from "../../../i18n";

export function AiChatPanels({ ctx }: { ctx: AiChatCtx }) {
  const { permOpen, setPermOpen, permError, loadPermission, perm, permState, permScope, switchMode, setHostOverride, planMode, setPlanMode, aiScopeAssetId, aiScopeProduction, tasksOpen } = ctx;
  const hostOverride = permState?.source === "host";
  const confirmFullAccess = (perHost: boolean) => ask(fullAccessConfirmText(perHost), { title: t("开启完全放行"), kind: "warning" });
  const chooseMode = async (mode: AiPermissionMode) => {
    if (mode === perm?.mode) return;
    if (mode === "full_access" && !(await confirmFullAccess(hostOverride))) return;
    switchMode(mode);
  };
  const toggleHostOverride = async () => {
    if (!permState) return;
    // Following the global mode can widen this host to full access, so that path asks like choosing it does.
    if (hostOverride && permState.globalMode === "full_access" && perm?.mode !== "full_access" && !(await confirmFullAccess(true))) return;
    setHostOverride(!hostOverride);
  };
  const capped = perm ? appliedPermissionMode(perm.mode, aiScopeProduction?.production === true).capped : false;
  return (
    <>
      {permOpen && (
        <div className="max-h-[70%] min-w-0 shrink-0 overflow-x-hidden overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60 p-2.5">
          <div className="mb-1.5 flex items-center gap-1.5 text-[11.5px] font-medium text-neutral-200">
            <IconShield size={12} />
            {t("AI 权限")}
            <span className="nx-spacer" />
            <button
              className="nx-icon-btn nx-icon-btn-sm"
              title={t("关闭 AI 权限面板")}
              aria-label={t("关闭 AI 权限面板")}
              onClick={() => setPermOpen(false)}
            >
              <IconClose size={11} />
            </button>
          </div>

          {permError ? (
            <div className="px-2 py-3 text-center" role="alert">
              <div className="nx-hint text-red-300">{t("未能读取权限设置：{permError}", { permError })}</div>
              <button className="nx-btn nx-btn-ghost nx-btn-xs mt-1.5" onClick={() => void loadPermission()}>
                <IconRefresh size={11} />
                {t("重试")}
              </button>
            </div>
          ) : !perm ? (
            <div className="nx-hint px-2 py-3 text-center text-[11px]">{t("正在读取权限设置…")}</div>
          ) : (
            <>
              <div className="mb-1 flex items-center gap-1.5 text-[11px] text-neutral-300">
                {hostOverride ? t("这台主机的模式") : t("全局模式")}
                <span className="nx-spacer" />
                <span className="text-[10.5px] text-neutral-500" data-testid="ai-permission-source">
                  {hostOverride ? t("仅这台主机") : t("所有主机")}
                </span>
              </div>
              <div className="mb-1 grid grid-cols-4 gap-1" role="radiogroup" aria-label={t("AI 权限模式")}>
                {MODE_OPTIONS.map((m) => (
                  <button
                    key={m.value}
                    role="radio"
                    aria-checked={perm.mode === m.value}
                    title={m.hint}
                    className={`nx-btn nx-btn-xs justify-center ${perm.mode === m.value ? "nx-btn-primary" : "nx-btn-outline"}`}
                    onClick={() => void chooseMode(m.value)}
                  >
                    {m.label}
                  </button>
                ))}
              </div>
              <div className="mb-2.5 text-[10.5px] leading-relaxed text-neutral-500">
                {MODE_OPTIONS.find((m) => m.value === perm.mode)?.hint}
                {capped && (
                  <div className="mt-1 text-amber-300/90">{t("这台主机属于生产环境，完全放行在这里按「自动执行」处理。")}</div>
                )}
              </div>

              {permScope && permState?.hostResolved && (
                <button
                  type="button"
                  role="switch"
                  aria-checked={hostOverride}
                  className={`nx-menu-item mb-2.5 w-full ${hostOverride ? "bg-amber-500/10" : ""}`}
                  title={
                    hostOverride
                      ? t("关闭后这台主机改用全局模式")
                      : t("开启后这台主机使用自己的模式，修改全局模式不会影响它")
                  }
                  onClick={() => void toggleHostOverride()}
                >
                  <span className="nx-menu-label">{t("这台主机单独设置")}</span>
                  <span className="nx-menu-hint">
                    {hostOverride
                      ? t("已开启 · 全局模式为「{mode}」", { mode: MODE_LABEL[permState.globalMode] })
                      : t("已关闭 · 跟随全局模式")}
                  </span>
                </button>
              )}

              <button
                type="button"
                role="switch"
                aria-checked={planMode}
                className={`nx-menu-item mb-2.5 w-full ${planMode ? "bg-blue-500/15" : ""}`}
                title={PLAN_MODE_HINT}
                onClick={() => setPlanMode((v) => !v)}
              >
                <span className={`shrink-0 ${planMode ? "text-blue-300" : "text-neutral-500"}`}>
                  <IconList size={12} />
                </span>
                <span className="nx-menu-label">{t("计划模式")}</span>
                <span className="nx-menu-hint">{planMode ? t("已开启：先提供方案，确认后再执行") : t("已关闭")}</span>
              </button>

              <div className="mb-1 border-t border-neutral-800/60 pt-2 text-[11px] text-neutral-300">{t("免确认授权")}</div>
              <div className="mb-2.5">
                <GrantPanel currentAssetId={aiScopeAssetId} />
              </div>

              <div className="flex items-center gap-1.5 border-t border-neutral-800/60 pt-2">
                <span className="text-[11px] text-neutral-300">{t("拦截规则")}</span>
                <span className="nx-count">
                  {perm.dangerRules.length > 0 ? t("{length} 条", { length: perm.dangerRules.length }) : t("未设置")}
                </span>
                <span className="nx-spacer" />
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  title={t("在设置中管理拦截规则")}
                  onClick={() =>
                    useUi
                      .getState()
                      .addTab({ id: "settings", kind: "settings", title: t("设置"), closable: true })
                  }
                >
                  {t("去设置")}
                </button>
              </div>
              <div className="mt-1.5 text-[10px] leading-relaxed text-neutral-500">
                {t("禁止的操作始终拒绝，任何模式和授权都不会跳过。除「完全放行」外，高危和无法判断的操作不会自动执行；生产环境和跨主机操作在任何模式下都不会比「自动执行」更宽松，定时任务从不使用完全放行。")}
              </div>
            </>
          )}
        </div>
      )}

      {tasksOpen && <BackgroundTasksPanel key={ctx.conversationId ?? "none"} ctx={ctx} />}
    </>
  );
}
