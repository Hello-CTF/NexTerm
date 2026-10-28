import { useEffect, useId, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { assetApi, terminalApi, toAppError } from "../../ipc/commands";
import { stateRuleApi, type StateRuleScreenDTO } from "../../ipc/stateRuleApi";
import { useUi } from "../../app/store";
import { getTerminalTabTitle } from "../../app/terminalViewStore";
import { describeError } from "../../ui/errorText";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { IconRefresh, IconSparkles } from "../../ui/icons";
import { showSettingsSection } from "../settings/settingsNavigation";
import type { StateMarkRequest } from "./stateMarkRequests";
import {
  STATE_RULE_DRAFT_ROWS,
  STATE_RULE_REDACTED_NOTE,
  draftRegionText,
  emptyRuleForm,
  formFromDraft,
  heuristicDraft,
  saveRequestFromForm,
  screenFromSnapshot,
  stateLabel,
  stateRuleTargets,
  type StateRuleDraftForm,
} from "./stateRuleDraft";
import { StateRuleForm, useRuleValidation, type HostOption } from "./StateRuleForm";

type ScreenState = { status: "loading" } | { status: "error"; message: string } | { status: "ready"; screen: StateRuleScreenDTO };

export function StateMarkDialog({ request, onClose }: { request: StateMarkRequest; onClose: () => void }) {
  const titleId = useId();
  const modalRef = useRef<HTMLDivElement>(null);
  const layer = useOverlayFocus(true, modalRef);
  const pushToast = useUi((state) => state.pushToast);
  const [screenState, setScreenState] = useState<ScreenState>({ status: "loading" });
  const [form, setForm] = useState<StateRuleDraftForm>(() => ({ ...emptyRuleForm(), state: request.intent === "this_is_it" ? "needs_me" : "idle" }));
  // Set once the user edits the form, so a stray click outside no longer discards their work.
  const [dirty, setDirty] = useState(false);
  const [draftNote, setDraftNote] = useState<string | null>(null);
  const [aiState, setAiState] = useState<{ busy: boolean; error: string | null }>({ busy: false, error: null });
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const aliveRef = useRef(true);
  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
    };
  }, []);

  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), retry: false });
  const hosts: HostOption[] = (assetsQuery.data ?? []).map((asset) => ({ id: asset.id, name: asset.name }));

  const screen = screenState.status === "ready" ? screenState.screen : null;
  const sent = screen ? draftRegionText(screen) : null;
  const validation = useRuleValidation(form, screen);
  const targets = stateRuleTargets(request.intent, request.current);

  useEffect(() => {
    let current = true;
    terminalApi.snapshot(request.tabId).then(
      (snapshot) => {
        if (!current) return;
        const read = screenFromSnapshot(snapshot, getTerminalTabTitle(request.appTabId));
        setScreenState({ status: "ready", screen: read });
        const draft = heuristicDraft(read, request.intent, request.current);
        if (draft) {
          setForm(formFromDraft(draft, request.assetId));
          setDraftNote(
            `草稿引用了屏幕的最后一行文字，可让 AI 起草更通用的规则，或直接修改。${draft.redacted ? `（${STATE_RULE_REDACTED_NOTE}，请核对匹配模式。）` : ""}`,
          );
        } else {
          setForm((previous) => ({ ...previous, scope: request.assetId ? "host" : "global", assetId: request.assetId ?? "" }));
          setDraftNote("屏幕上没有文字，请手动填写匹配模式。");
        }
      },
      (cause: unknown) => {
        if (current) setScreenState({ status: "error", message: describeError(cause) });
      },
    );
    return () => {
      current = false;
    };
  }, [request]);

  const askAi = async () => {
    if (!screen || aiState.busy) return;
    setAiState({ busy: true, error: null });
    try {
      const draft = await stateRuleApi.draft(request.intent, request.current, screen);
      if (!aliveRef.current) return;
      // The draft proposes the match; the scope, keys and enabled flag stay as the user set them.
      setForm((previous) => ({
        ...formFromDraft(draft, request.assetId),
        scope: previous.scope,
        assetId: previous.assetId,
        keys: previous.keys,
        enabled: previous.enabled,
      }));
      const note = draft.explanation ? `AI：${draft.explanation}` : "AI 起草的规则，保存前请确认。";
      setDraftNote(draft.redacted ? `${note}（发送前${STATE_RULE_REDACTED_NOTE}。）` : note);
      setAiState({ busy: false, error: null });
    } catch (cause) {
      if (!aliveRef.current) return;
      const error = toAppError(cause);
      setAiState({
        busy: false,
        error: error.code === "unsupported" ? `${error.message}。可以使用上面的草稿，或手动编辑。` : error.message,
      });
    }
  };

  const save = async () => {
    if (saving || !validation.result?.valid || validation.pending) return;
    setSaving(true);
    setSaveError(null);
    try {
      await stateRuleApi.save(saveRequestFromForm(form));
      if (!aliveRef.current) return;
      pushToast("success", "已保存状态规则，之后同样的屏幕将按规则显示");
      onClose();
    } catch (cause) {
      if (aliveRef.current) setSaveError(describeError(cause));
    } finally {
      if (aliveRef.current) setSaving(false);
    }
  };

  const openRules = () => {
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
    showSettingsSection("settings-state-rules");
    onClose();
  };

  const heading = request.intent === "this_is_it" ? "标记为需要处理" : "状态有误";
  const explanation =
    request.intent === "this_is_it"
      ? `「${request.title}」显示为「${stateLabel(request.current)}」，但该屏幕正在等待回应。保存一条规则，以后同样的屏幕显示为「需要处理」。`
      : `「${request.title}」显示为「${stateLabel(request.current)}」，但该状态有误。保存一条规则，以后同样的屏幕按所选状态显示。`;
  const canSave = Boolean(validation.result?.valid) && !validation.pending && !saving;

  return (
    <div className="nx-overlay" onClick={() => !dirty && onClose()}>
      <div
        ref={modalRef}
        className="nx-modal max-w-[640px]"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={(event) => {
          event.stopPropagation();
          if (!layer.isTopmost()) return;
          if (event.key === "Escape" && !event.repeat && !isImeKeyEvent(event)) {
            event.preventDefault();
            onClose();
            return;
          }
          trapOverlayTab(event, modalRef.current);
        }}
      >
        <div className="nx-modal-body flex max-h-[75vh] flex-col gap-3 overflow-y-auto">
          <div>
            <h2 id={titleId} className="text-[14px] font-semibold text-neutral-100">
              {heading}
            </h2>
            <p className="nx-hint mt-1">{explanation}</p>
          </div>
          {screenState.status === "loading" && <div className="nx-hint">读取屏幕…</div>}
          {screenState.status === "error" && (
            <div className="nx-alert nx-alert-danger text-[12px]" role="alert">
              无法读取屏幕：{screenState.message}。仍可手动填写规则。
            </div>
          )}
          {screen && (
            <details>
              <summary className="cursor-pointer text-[12px] text-neutral-300">AI 起草会发送的内容（屏幕末尾 {STATE_RULE_DRAFT_ROWS} 行和标题）</summary>
              <div className="nx-hint mt-1.5 text-[11.5px]">窗口标题</div>
              <pre className="nx-code mt-1 max-h-12 overflow-auto whitespace-pre text-[11px]" data-ai-sent-title>
                {sent?.title || "（空）"}
              </pre>
              <div className="nx-hint mt-1.5 text-[11.5px]">屏幕末尾</div>
              <pre className="nx-code mt-1 max-h-60 overflow-auto whitespace-pre text-[11px]" data-ai-sent-rows>
                {sent?.rows || "（空）"}
              </pre>
            </details>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              className="nx-btn nx-btn-outline nx-btn-sm"
              disabled={!screen || aiState.busy}
              onClick={() => void askAi()}
            >
              {aiState.busy ? <IconRefresh size={11} className="animate-spin" /> : <IconSparkles size={11} />}
              AI 起草
            </button>
            <span className="nx-hint text-[11.5px]">会把上面的内容发送给当前 AI 模型，疑似密钥或密码会先脱敏。</span>
          </div>
          {aiState.error && (
            <div className="nx-alert nx-alert-danger text-[12px]" role="alert" data-ai-draft-error>
              AI 起草失败：{aiState.error}
            </div>
          )}
          {draftNote && <p className="nx-hint text-[11.5px]">{draftNote}</p>}
          <StateRuleForm
            form={form}
            onChange={(next) => {
              setDirty(true);
              setForm(next);
            }} hosts={hosts}
            validation={validation}
            saveError={saveError}
            targets={targets}
          />
        </div>
        <div className="nx-modal-footer">
          <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm mr-auto" onClick={openRules}>
            管理状态规则
          </button>
          <button type="button" className="nx-btn nx-btn-ghost" onClick={onClose}>
            取消
          </button>
          <button type="button" className="nx-btn nx-btn-primary" disabled={!canSave} onClick={() => void save()}>
            {saving ? "正在保存…" : "保存规则"}
          </button>
        </div>
      </div>
    </div>
  );
}
