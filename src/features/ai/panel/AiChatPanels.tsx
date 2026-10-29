import { IconShield, IconClose, IconRefresh, IconList } from "../../../ui/icons";
import { GrantPanel } from "../GrantPanel";
import { useUi } from "../../../app/store";
import { MODE_OPTIONS, PLAN_MODE_HINT } from "./aiSidebarShared";
import { BackgroundTasksPanel } from "./BackgroundTasksPanel";
import type { AiChatCtx } from "./aiChatCtx";

export function AiChatPanels({ ctx }: { ctx: AiChatCtx }) {
  const { permOpen, setPermOpen, permError, loadPermission, perm, permScope, permDeviceOverride, followGlobalMode, switchMode, planMode, setPlanMode, aiScopeAssetId, tasksOpen } = ctx;
  return (
    <>
      {permOpen && (
        <div className="max-h-[70%] min-w-0 shrink-0 overflow-x-hidden overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60 p-2.5">
          <div className="mb-1.5 flex items-center gap-1.5 text-[11.5px] font-medium text-neutral-200">
            <IconShield size={12} />
            AI 权限
            <span className="nx-spacer" />
            <button
              className="nx-icon-btn nx-icon-btn-sm"
              title="关闭 AI 权限面板"
              aria-label="关闭 AI 权限面板"
              onClick={() => setPermOpen(false)}
            >
              <IconClose size={11} />
            </button>
          </div>

          {permError ? (
            <div className="px-2 py-3 text-center" role="alert">
              <div className="nx-hint text-red-300">未能读取权限设置：{permError}</div>
              <button className="nx-btn nx-btn-ghost nx-btn-xs mt-1.5" onClick={() => void loadPermission()}>
                <IconRefresh size={11} />
                重试
              </button>
            </div>
          ) : !perm ? (
            <div className="nx-hint px-2 py-3 text-center text-[11px]">正在读取权限设置…</div>
          ) : (
            <>
              <div className="mb-1 flex items-center gap-1.5 text-[11px] text-neutral-300">
                {permScope ? "当前主机的模式" : "默认模式"}
                <span className="nx-spacer" />
                {permScope &&
                  (permDeviceOverride ? (
                    <button
                      className="text-[10.5px] text-neutral-500 hover:text-neutral-300"
                      title="清除当前主机的专属模式，改用全局默认模式"
                      onClick={followGlobalMode}
                    >
                      改为跟随全局
                    </button>
                  ) : (
                    <span className="text-[10.5px] text-neutral-500">跟随全局默认</span>
                  ))}
              </div>
              <div className="mb-1 grid grid-cols-3 gap-1" role="radiogroup" aria-label="AI 权限模式">
                {MODE_OPTIONS.map((m) => (
                  <button
                    key={m.value}
                    role="radio"
                    aria-checked={perm.mode === m.value}
                    title={m.hint}
                    className={`nx-btn nx-btn-xs justify-center ${perm.mode === m.value ? "nx-btn-primary" : "nx-btn-outline"}`}
                    onClick={() => switchMode(m.value)}
                  >
                    {m.label}
                  </button>
                ))}
              </div>
              <div className="mb-2.5 text-[10.5px] leading-relaxed text-neutral-500">
                {MODE_OPTIONS.find((m) => m.value === perm.mode)?.hint}
              </div>

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
                <span className="nx-menu-label">计划模式</span>
                <span className="nx-menu-hint">{planMode ? "已开启：先提供方案，确认后再执行" : "已关闭"}</span>
              </button>

              <div className="mb-1 border-t border-neutral-800/60 pt-2 text-[11px] text-neutral-300">免确认授权</div>
              <div className="mb-2.5">
                <GrantPanel currentAssetId={aiScopeAssetId} />
              </div>

              <div className="flex items-center gap-1.5 border-t border-neutral-800/60 pt-2">
                <span className="text-[11px] text-neutral-300">拦截规则</span>
                <span className="nx-count">
                  {perm.dangerRules.length > 0 ? `${perm.dangerRules.length} 条` : "未设置"}
                </span>
                <span className="nx-spacer" />
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  title="在设置中管理拦截规则"
                  onClick={() =>
                    useUi
                      .getState()
                      .addTab({ id: "settings", kind: "settings", title: "设置", closable: true })
                  }
                >
                  去设置
                </button>
              </div>
              <div className="mt-1.5 text-[10px] leading-relaxed text-neutral-500">
                高危和无法判断的操作始终询问，禁止的操作始终拒绝，任何模式和授权都不会跳过。
              </div>
            </>
          )}
        </div>
      )}

      {tasksOpen && <BackgroundTasksPanel key={ctx.conversationId ?? "none"} ctx={ctx} />}
    </>
  );
}
