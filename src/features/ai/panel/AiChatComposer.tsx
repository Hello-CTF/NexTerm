import { IconList, IconEdit, IconAlert, IconLoader, IconClose, IconImage, IconShield, IconMonitor } from "../../../ui/icons";
import { ProductionWriteRiskNotice } from "../../production/ProductionWarning";
import { DESKTOP } from "../../../ipc/env";
import { isImeKeyEvent } from "../../../ui/DialogHost";
import { ModelSelector } from "../ModelSelector";
import { UsageRing } from "../UsageRing";
import { modelApi } from "../../../ipc/commands";
import { ModelPanel } from "../ModelPanel";
import { PrivacyIndicator, usePrivacyGuard } from "../PrivacyIndicator";
import { PLAN_MODE_HINT, statusText, MODE_LABEL } from "./aiSidebarShared";
import type { AiChatCtx } from "./aiChatCtx";

export function AiChatComposer({ ctx }: { ctx: AiChatCtx }) {
  const privacyGuard = usePrivacyGuard();
  const { planMode, editing, editSubmitting, input, submitEditResend, setEditing, activeRun, stop, follow, syncNotice, activeGeneration, activeRunRef, resyncRun, busy, hitlWaiting, conv, aiScopeProduction, refs, setRefs, images, setImages, slashMenuOpen, slashItems, slashEmptyText, slashIndex, setSlashIndex, modelQuery, skillOptions, atOpen, refCandidates, pickRef, inputRef, sessionId, onInputChange, onPaste, setAtOpen, steer, send, setModelPanelOpen, imageInputRef, onPickImages, conversationRuns, perm, permOpen, permError, permScope, setPermOpen, tabId, bindings, reclaimKeyLabel, runTakeover, modelPanelOpen } = ctx;
  return (
    <>
      <div className="shrink-0 border-t border-neutral-800/60 p-2.5 [padding-bottom:calc(0.625rem+var(--nx-kb-inset,0px))]">
        {planMode && (
          <div className="mb-1.5 flex items-center gap-1.5 text-[11px] text-blue-300">
            <IconList size={10} className="shrink-0" />
            <span className="truncate">计划模式 · {PLAN_MODE_HINT}</span>
          </div>
        )}
        {editing && (
          <div className="mb-1.5 flex items-center gap-1.5 rounded-lg border border-blue-500/30 bg-blue-500/10 px-2 py-1 text-[11px] text-blue-300">
            <IconEdit size={10} className="shrink-0" />
            <span className="min-w-0 flex-1 truncate">
              {editSubmitting ? "正在编辑并重新发送…" : "编辑并重新发送将删除此消息后的消息和运行记录"}
            </span>
            <button
              className="nx-btn nx-btn-primary nx-btn-xs shrink-0"
              disabled={!input.trim() || editSubmitting}
              onClick={() => void submitEditResend()}
            >
              编辑并重新发送
            </button>
            <button
              className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
              disabled={editSubmitting}
              onClick={() => setEditing(null)}
            >
              取消
            </button>
          </div>
        )}
        {activeRun && activeRun.cancellation !== "idle" && (
          <div
            className="mb-1.5 flex items-center gap-1.5 text-[11px] text-amber-300"
            role="status"
            aria-live="polite"
          >
            <IconAlert size={10} className="shrink-0" />
            <span className="truncate">
              {activeRun.cancellation === "cancelling" ? "正在停止，等待任务结束…" : "停止未确认"}
            </span>
            {activeRun.cancellation === "unconfirmed" && !activeRun.settled && (
              <button className="nx-btn nx-btn-outline nx-btn-xs shrink-0" onClick={stop}>
                重试
              </button>
            )}
            {activeRun.cancellation === "unconfirmed" && (
              <button className="nx-btn nx-btn-ghost nx-btn-xs shrink-0" onClick={follow.jumpToLatest}>
                查看任务
              </button>
            )}
          </div>
        )}
        {syncNotice && syncNotice.generation === activeGeneration && (
          <div
            className="mb-1.5 flex items-center gap-1.5 text-[11px] text-neutral-500 [@media(max-height:480px)]:sr-only"
            role="status"
            aria-live="polite"
          >
            {syncNotice.phase === "syncing" ? (
              <>
                <IconLoader size={10} className="animate-spin text-amber-300/80" />
                <span className="truncate">连接已恢复，正在补齐输出…</span>
              </>
            ) : syncNotice.phase === "failed" ? (
              <>
                <IconAlert size={10} className="shrink-0 text-red-300" />
                <span className="truncate">补齐失败，输出可能不完整</span>
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs shrink-0"
                  onClick={() => {
                    const current = activeRunRef.current;
                    if (current?.jobId) resyncRun(current.generation, current.jobId);
                  }}
                >
                  重新补齐
                </button>
              </>
            ) : (
              <span className="truncate text-[var(--nx-fg-tertiary)]">
                {syncNotice.count > 0 ? `已补齐断线期间的 ${syncNotice.count} 条输出` : "连接已恢复，输出无缺失"}
              </span>
            )}
          </div>
        )}
        {busy && hitlWaiting && (
          <div
            className="nx-ai-hitl-waiting mb-1.5 flex items-center gap-1.5 text-[11px] text-amber-300 [@media(max-height:480px)]:sr-only"
            role="status"
            aria-live="polite"
          >
            <IconAlert size={10} className="shrink-0 text-amber-300" />
            <span className="truncate">{hitlWaiting}</span>
          </div>
        )}
        {busy && !hitlWaiting && conv.status && (
          <div
            className="mb-1.5 flex items-center gap-1.5 text-[11px] text-neutral-500 [@media(max-height:480px)]:sr-only"
            role="status"
            aria-live="polite"
          >
            <IconLoader size={10} className="animate-spin text-amber-300/80" />
            <span className="truncate">{statusText(conv.status)}</span>
          </div>
        )}
        {aiScopeProduction?.production && (
          <div className="mb-1.5">
            <ProductionWriteRiskNotice
              environment={aiScopeProduction.environment}
              production
              operation="AI"
            />
          </div>
        )}
        {refs.length > 0 && (
          <div className="mb-1.5 flex flex-wrap gap-1">
            {refs.map((r) => (
              <span key={r.id} className="nx-chip nx-chip-accent" title={r.detail}>
                <span className="truncate">{r.label}</span>
                <button
                  className="nx-chip-x"
                  title="移除引用"
                  onClick={() => setRefs((prev) => prev.filter((x) => x.id !== r.id))}
                >
                  <IconClose size={10} />
                </button>
              </span>
            ))}
          </div>
        )}

        {images.length > 0 && (
          <div className="mb-1.5 flex flex-wrap gap-1.5">
            {images.map((src, i) => (
              <span key={i} className="nx-attach" title="粘贴的图片">
                <img src={src} alt="" />
                <button
                  className="nx-attach-x"
                  title="移除图片"
                  onClick={() => setImages((prev) => prev.filter((_, j) => j !== i))}
                >
                  <IconClose size={10} />
                </button>
              </span>
            ))}
          </div>
        )}

        {slashMenuOpen && (
          <div className="mb-1.5 max-h-56 overflow-y-auto rounded-lg border border-neutral-800 bg-neutral-900 p-1" role="listbox" aria-label="命令与技能">
            {slashItems.length === 0 ? (
              <div className="px-2 py-1.5 text-[11.5px] text-neutral-500">{slashEmptyText}</div>
            ) : (
              slashItems.map((item, index) => (
                <button
                  key={item.key}
                  role="option"
                  aria-selected={index === slashIndex}
                  className={`nx-menu-item w-full ${index === slashIndex ? "bg-neutral-800/70" : ""}`}
                  onMouseEnter={() => setSlashIndex(index)}
                  onClick={() => item.run()}
                >
                  <span className="nx-menu-label">{item.label}</span>
                  <span className="nx-menu-hint truncate">{item.hint}</span>
                </button>
              ))
            )}
            {modelQuery === null && skillOptions !== null && skillOptions.length === 0 && (
              <div className="border-t border-neutral-800/70 px-2 pt-1.5 text-[10.5px] text-neutral-500">在「设置 → AI 技能」里写好的技能也会出现在这里。</div>
            )}
          </div>
        )}

        {atOpen && refCandidates.length > 0 && (
          <div className="mb-1.5 max-h-40 overflow-y-auto rounded-lg border border-neutral-800 bg-neutral-900 p-1">
            {refCandidates.map((r) => (
              <button
                key={r.id}
                className="nx-menu-item w-full"
                onClick={() => pickRef(r)}
              >
                <span className="nx-menu-label">{r.label}</span>
                <span className="nx-menu-hint">{r.detail}</span>
              </button>
            ))}
          </div>
        )}

        <textarea
          ref={inputRef}
          className="nx-textarea pointer-coarse:text-[16px] max-h-40 min-h-[64px] w-full [@media(max-height:480px)]:h-9 [@media(max-height:480px)]:min-h-0"
          placeholder={
            busy
              ? "运行中：Enter 发送补充指令，AI 会在当前步骤完成后收到"
              : sessionId
                ? DESKTOP
                  ? "向 NexTerm 提问，@ 引用资产或终端标签，/ 命令与技能"
                  : "向 NexTerm 提问，@ 引用资产或终端标签，/ 命令与技能，可直接粘贴图片"
                : "未连接会话（仍可全局提问）"
          }
          aria-label="消息输入"
          value={input}
          rows={3}
          onChange={(e) => onInputChange(e.target.value)}
          onPaste={onPaste}
          onKeyDown={(e) => {
            if (slashMenuOpen && slashItems.length > 0) {
              if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                e.preventDefault();
                const step = e.key === "ArrowDown" ? 1 : -1;
                setSlashIndex((index) => (index + step + slashItems.length) % slashItems.length);
                return;
              }
              if ((e.key === "Enter" && !e.shiftKey && !isImeKeyEvent(e)) || e.key === "Tab") {
                e.preventDefault();
                slashItems[Math.min(slashIndex, slashItems.length - 1)].run();
                return;
              }
            }
            if (e.key === "Escape" && atOpen) {
              e.preventDefault();
              setAtOpen(false);
              return;
            }
            if (e.key === "Enter" && !e.shiftKey) {
              if (isImeKeyEvent(e)) return;
              e.preventDefault();
              if (busy) void steer();
              else if (editing) void submitEditResend();
              else void send();
            }
          }}
        />

        <div className="nx-ai-composer-actions mt-1.5 flex flex-wrap items-center gap-1">
          <ModelSelector onManage={() => setModelPanelOpen(true)} />
          <input
            ref={imageInputRef}
            type="file"
            accept="image/*"
            multiple
            className="hidden"
            aria-hidden="true"
            tabIndex={-1}
            onChange={onPickImages}
          />
          <button
            className="nx-icon-btn nx-icon-btn-sm"
            title={privacyGuard.enabled ? "已开启 AI 信息脱敏，暂不支持图片" : DESKTOP ? "添加图片" : "添加图片（也可以直接粘贴）"}
            aria-label="添加图片"
            disabled={privacyGuard.enabled}
            onClick={() => imageInputRef.current?.click()}
          >
            <IconImage size={13} />
          </button>
          <PrivacyIndicator guard={privacyGuard} />
          <div className="nx-spacer" />
          <UsageRing
            usage={conv.usage}
            runs={conversationRuns}
            loadSummary={() => modelApi.usageSummary()}
          />
          <button
            className={`nx-icon-btn nx-icon-btn-sm ${
              perm?.mode === "silent" ? "is-active" : ""
            } ${permOpen ? "bg-neutral-800 text-neutral-100" : ""}`}
            title={
              permError
                ? "AI 权限读取失败（点击查看并重试）"
                : perm
                  ? `AI 权限与模式：${MODE_LABEL[perm.mode]}（${permScope ? "这台主机" : "全局默认"}），含计划模式和免确认授权`
                  : "AI 权限与模式：读取中（点击设置）"
            }
            onClick={() => setPermOpen((v) => !v)}
          >
            <IconShield size={13} />
          </button>
          <button
            className="nx-icon-btn nx-icon-btn-sm text-red-400 hover:text-red-300"
            title={
              tabId
                ? bindings.reclaimTakeover
                  ? `终端接管（实验性功能）：AI 直接接手当前终端，随时按 ${reclaimKeyLabel} 收回控制权`
                  : "终端接管（实验性功能）：AI 直接接手当前终端"
                : "终端接管需要先打开一个终端标签"
            }
            disabled={!tabId || busy}
            onClick={() => void runTakeover()}
          >
            <IconMonitor size={13} />
          </button>
          {busy ? (
            <button
              className="nx-send-btn nx-send-btn-stop"
              title={
                activeRun?.cancellation === "cancelling"
                  ? "停止当前任务：已受理，等待终态"
                  : activeRun?.cancellation === "unconfirmed"
                    ? "停止当前任务：停止未确认，点击重试"
                    : "停止当前任务（保留已完成的输出）"
              }
              disabled={activeRun?.cancellation === "cancelling"}
              onClick={() => void stop()}
            >
              <IconStopSquare />
            </button>
          ) : (
            <button
              className="nx-send-btn"
              title={editing ? "编辑并重新发送" : "发送 (Enter)"}
              disabled={(!input.trim() && images.length === 0) || editSubmitting}
              onClick={() => (editing ? void submitEditResend() : void send())}
            >
              <IconSendArrow />
            </button>
          )}
        </div>
      </div>

      {modelPanelOpen && <ModelPanel onClose={() => setModelPanelOpen(false)} />}
    </>
  );
}


function IconSendArrow() {
  return (
    <svg width="15" height="15" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path
        d="M8 12.5V3.5M8 3.5L4 7.5M8 3.5l4 4"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function IconStopSquare() {
  return (
    <svg width="15" height="15" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <rect x="4.5" y="4.5" width="7" height="7" rx="1.5" fill="currentColor" />
    </svg>
  );
}
