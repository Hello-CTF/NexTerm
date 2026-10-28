import { useEffect, useState } from "react";
import { modelApi } from "../../../ipc/commands";
import { IconAlert, IconRefresh, IconBot, IconShield, IconInfo, IconList } from "../../../ui/icons";
import { useUi } from "../../../app/store";
import { PLAN_EXECUTE_MESSAGE, confirmTitle, toolLabel, CONFIRM_HELP, RULE_CAPABLE_TOOLS, permanentScopeText, interceptionRuleFromCard } from "./aiSidebarShared";
import { ChatBubble, ConfirmBody, ProductionEnvironmentChallenge } from "./AiMessageBubbles";
import type { AiChatCtx } from "./aiChatCtx";

// Onboarding: with no model profile the composer cannot work, so the empty
// conversation says so and offers the next step.
function NoModelHint({ onConfigure }: { onConfigure: () => void }) {
  const revision = useUi((s) => s.modelProfilesRevision);
  const [none, setNone] = useState(false);
  useEffect(() => {
    let alive = true;
    Promise.resolve(modelApi.overview()).then(
      (view) => {
        if (alive) setNone((view?.profiles?.length ?? 1) === 0);
      },
      () => {},
    );
    return () => {
      alive = false;
    };
  }, [revision]);
  if (!none) return null;
  return (
    <div className="flex flex-col items-center gap-2">
      <div className="text-xs text-amber-300">尚未配置模型，AI 暂时无法回答。</div>
      <button className="nx-btn nx-btn-primary nx-btn-xs" onClick={onConfigure}>
        添加模型
      </button>
    </div>
  );
}

export function AiChatMessageStream({ ctx }: { ctx: AiChatCtx }) {
  const { setScrollEl, messageWindowStart, loadOlderMessages, conv, conversationError, conversationIdRef, restoreConversation, virtual, activeMatchIndex, busy, handleApprovePlan, handleRetryRun, handleEditResend, agentToolCallFor, submitAgentApproval, submittingCardId, confirmCard, confirmCardRef, confirm, productionNames, handleProductionState, productionBlocked, questionCard, answer, questionInput, setQuestionInput, follow, setModelPanelOpen } = ctx;
  return (
    <>
      <div className="relative min-h-0 flex-1 overflow-hidden">
        <div
          ref={setScrollEl}
          role="log"
          aria-label="AI 对话记录"
          className="h-full overflow-y-auto p-3"
        >
          {messageWindowStart > 0 && (
            <div className="mb-2 flex justify-center">
              <button className="nx-btn nx-btn-ghost nx-btn-xs" onClick={loadOlderMessages}>
                加载更早消息
              </button>
            </div>
          )}
          {conv.items.length === 0 && conversationError && (
            <div className="flex flex-col items-center gap-3 px-2 pt-10 text-center">
              <span className="nx-empty-icon">
                <IconAlert size={19} />
              </span>
              <div className="text-xs text-red-300">未能打开此会话：{conversationError}。请检查网络后重试</div>
              <button
                className="nx-btn nx-btn-outline nx-btn-xs"
                onClick={() => {
                  const id = conversationIdRef.current;
                  if (id) void restoreConversation(id);
                }}
              >
                <IconRefresh size={11} />
                重试
              </button>
            </div>
          )}
          {conv.items.length === 0 && !conversationError && (
            <div className="flex flex-col items-center gap-3 px-2 pt-10 text-center">
              <span className="nx-empty-icon">
                <IconBot size={19} />
              </span>
              <div className="text-xs text-neutral-400">在下方输入内容，开始新的对话。</div>
              <NoModelHint onConfigure={() => setModelPanelOpen(true)} />
            </div>
          )}
          {virtual.range.padTop > 0 && (
            <div style={{ height: virtual.range.padTop }} aria-hidden="true" />
          )}
          {conv.items.slice(virtual.range.start, virtual.range.end).map((item, i) => {
            const index = virtual.range.start + i;
            const planApproved =
              item.role === "plan" &&
              conv.items.some((candidate, candidateIndex) => candidateIndex > index && candidate.role === "user" && candidate.text.trim() === PLAN_EXECUTE_MESSAGE);
            return (
              <div
                key={item.id}
                data-conversation-index={index}
                data-index={index}
                ref={virtual.measureRef}
                className={
                  [
                    i > 0 ? "mt-2.5" : null,
                    index === activeMatchIndex ? "rounded-xl ring-2 ring-amber-400/70" : null,
                  ]
                    .filter(Boolean)
                    .join(" ") || undefined
                }
              >
                <ChatBubble
                  item={item}
                  streaming={busy && index === conv.items.length - 1}
                  planApproved={planApproved}
                  onApprovePlan={handleApprovePlan}
                  onRetry={handleRetryRun}
                  onEdit={handleEditResend}
                  agentToolCall={item.role === "tool" ? agentToolCallFor(item) : null}
                  onAgentApproval={submitAgentApproval}
                  agentApprovalBusy={submittingCardId !== null}
                />
              </div>
            );
          })}
          {virtual.range.padBottom > 0 && (
            <div style={{ height: virtual.range.padBottom }} aria-hidden="true" />
          )}
          {confirmCard && (
            <div
              ref={confirmCardRef}
              tabIndex={-1}
              role="group"
              aria-label={`${confirmTitle(confirmCard.risk)}：${toolLabel(confirmCard.tool)}`}
              className={`${confirmCard.risk === "danger" ? "nx-alert nx-alert-danger" : "nx-alert"} mt-2.5 outline-none`}
              onKeyDown={(event) => {
                if (event.key === "Escape") {
                  event.preventDefault();
                  event.stopPropagation();
                  void confirm("deny");
                }
              }}
            >
              <div className="mb-1.5 flex items-center gap-1.5 font-semibold">
                <IconShield size={13} className="shrink-0" />
                <span className="min-w-0 truncate">{confirmTitle(confirmCard.risk)}</span>
                <span className="nx-spacer" />
                <span className={confirmCard.risk === "danger" ? "nx-badge nx-badge-red" : "nx-badge nx-badge-amber"}>
                  {toolLabel(confirmCard.tool)}
                </span>
                <span
                  className="shrink-0 cursor-help text-neutral-500"
                  title={CONFIRM_HELP}
                  aria-label={CONFIRM_HELP}
                  role="img"
                >
                  <IconInfo size={12} />
                </span>
              </div>
              <ConfirmBody card={confirmCard} />
              {productionNames && (
                <ProductionEnvironmentChallenge
                  key={confirmCard.id}
                  names={productionNames}
                  onStateChange={handleProductionState}
                />
              )}
              <div className="flex flex-wrap items-center gap-1.5">
                <button
                  className={`nx-btn ${confirmCard.risk === "danger" ? "nx-btn-danger-solid" : "nx-btn-primary"} nx-btn-xs`}
                  title="只允许这一次"
                  disabled={submittingCardId === confirmCard.id || productionBlocked}
                  onClick={() => void confirm("allow")}
                >
                  允许一次
                </button>
                {confirmCard.sessionScope && (
                  <button
                    className="nx-btn nx-btn-outline nx-btn-xs"
                    title={`本次对话中不再询问：${confirmCard.sessionScope}`}
                    disabled={submittingCardId === confirmCard.id || productionBlocked}
                    onClick={() => void confirm("allow_session")}
                  >
                    本会话允许
                  </button>
                )}
                {confirmCard.risk === "needs_confirm" && RULE_CAPABLE_TOOLS.has(confirmCard.tool) && (
                  <button
                    className="nx-btn nx-btn-outline nx-btn-xs"
                    title={`${permanentScopeText(confirmCard)}；高危或无法判断的操作仍会询问，可随时在授权管理中撤销`}
                    disabled={submittingCardId === confirmCard.id || productionBlocked}
                    onClick={() => void confirm("allow_persistent")}
                  >
                    永久允许
                  </button>
                )}
                <button
                  className="nx-btn nx-btn-ghost nx-btn-xs"
                  title="拒绝本次操作，AI 将收到拒绝通知（Esc）"
                  disabled={submittingCardId === confirmCard.id}
                  onClick={() => void confirm("deny")}
                >
                  拒绝
                </button>
                <span className="nx-spacer" />
                {interceptionRuleFromCard(confirmCard) && (
                  <button
                    className="text-[10.5px] text-neutral-500 underline-offset-2 hover:text-neutral-300 hover:underline"
                    title="打开设置中的拦截规则，并预填当前操作"
                    onClick={() => {
                      const rule = interceptionRuleFromCard(confirmCard);
                      if (!rule) return;
                      const ui = useUi.getState();
                      ui.setAiRulePrefill(rule);
                      ui.addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
                    }}
                  >
                    始终拦截
                  </button>
                )}
              </div>
              {confirmCard.sessionScope && (
                <div className="mt-1.5 text-[10.5px] leading-relaxed text-neutral-500">
                  「本会话允许」后，本次对话内不再询问：{confirmCard.sessionScope}
                </div>
              )}
            </div>
          )}

          {questionCard && (
            <form
              className="nx-alert mt-2.5"
              onSubmit={(event) => {
                event.preventDefault();
                void answer();
              }}
            >
              <div className="mb-1.5 flex items-center gap-1.5 font-semibold">
                <IconAlert size={13} />
                AI 需要你回答
              </div>
              <div className="mb-2 whitespace-pre-wrap text-[12px] leading-relaxed text-neutral-200">
                {questionCard.question}
              </div>
              {questionCard.options.length > 0 && (
                <div className="mb-2 flex flex-wrap gap-1.5">
                  {questionCard.options.map((option) => (
                    <button
                      key={option}
                      type="button"
                      className="nx-btn nx-btn-outline nx-btn-xs"
                      disabled={submittingCardId === questionCard.id}
                      onClick={() => void answer(option)}
                    >
                      {option}
                    </button>
                  ))}
                </div>
              )}
              <div className="flex items-end gap-1.5">
                <textarea
                  className="nx-textarea pointer-coarse:text-[16px] min-h-[36px] flex-1"
                  rows={2}
                  value={questionInput}
                  placeholder="输入回答…"
                  aria-label="回答 AI 的问题"
                  onChange={(event) => setQuestionInput(event.target.value)}
                />
                <button
                  type="submit"
                  className="nx-btn nx-btn-primary nx-btn-xs shrink-0"
                  disabled={!questionInput.trim() || submittingCardId === questionCard.id}
                >
                  回答
                </button>
              </div>
            </form>
          )}

          {conv.todos.length > 0 && (
            <div className="mt-2.5 rounded-lg border border-neutral-800/60 bg-neutral-900/50 px-2.5 py-2">
              <div className="mb-1 flex items-center gap-1.5 text-[10.5px] text-neutral-400">
                <IconList size={11} />
                任务清单
                <span className="text-[var(--nx-fg-tertiary)]">
                  {conv.todos.filter((t) => t.status === "completed").length}/{conv.todos.length}
                </span>
              </div>
              <div className="flex max-h-28 flex-col gap-0.5 overflow-y-auto">
                {conv.todos.map((t, i) => (
                  <div key={i} className="flex items-start gap-1.5 text-[11px] leading-snug">
                    <span
                      className={
                        t.status === "completed"
                          ? "text-green-300"
                          : t.status === "in_progress"
                            ? "text-blue-300"
                            : "text-[var(--nx-fg-tertiary)]"
                      }
                    >
                      {t.status === "completed" ? "✓" : t.status === "in_progress" ? "▸" : "○"}
                    </span>
                    <span
                      className={
                        t.status === "completed"
                          ? "text-neutral-500 line-through"
                          : "text-neutral-300"
                      }
                    >
                      {t.content}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
        {follow.newOutput && (
          <button
            type="button"
            className="absolute bottom-2 left-1/2 z-10 -translate-x-1/2 rounded-full border border-neutral-700 bg-neutral-800/95 px-3 py-1 text-[11px] text-neutral-200 shadow-lg hover:bg-neutral-700"
            aria-label="回到最新输出"
            onClick={follow.jumpToLatest}
          >
            ↓ 新输出
          </button>
        )}
      </div>
    </>
  );
}
