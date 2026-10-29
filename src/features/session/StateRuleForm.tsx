import { useEffect, useId, useState } from "react";
import { stateRuleApi, type StateRuleScreenDTO, type StateRuleValidateResult } from "../../ipc/stateRuleApi";
import { resolveHostLabel } from "../hosts/hostLabel";
import { describeError } from "../../ui/errorText";
import {
  STATE_RULE_MAX_LINES,
  STATE_RULE_TARGETS,
  patternWarning,
  saveRequestFromForm,
  type StateRuleDraftForm,
  type StateRuleTarget,
} from "./stateRuleDraft";

// How long typing settles before the rule is checked again.
export const RULE_VALIDATE_DELAY_MS = 250;

export interface RuleValidation {
  result: StateRuleValidateResult | null;
  // The check itself failed (the backend was unreachable); the rule may still be valid.
  error: string | null;
  pending: boolean;
}

// useRuleValidation checks the rule with the backend's own validation (state_rule_validate) once typing settles and,
// given a screen, tests it there. Replies for an older form are dropped.
export function useRuleValidation(form: StateRuleDraftForm, screen: StateRuleScreenDTO | null): RuleValidation {
  const [state, setState] = useState<RuleValidation>({ result: null, error: null, pending: true });
  const request = JSON.stringify([saveRequestFromForm(form), screen]);
  useEffect(() => {
    let current = true;
    setState((previous) => ({ ...previous, pending: true }));
    const [rule, testScreen] = JSON.parse(request) as [ReturnType<typeof saveRequestFromForm>, StateRuleScreenDTO | null];
    const timer = setTimeout(() => {
      stateRuleApi.validate(rule, testScreen ?? undefined).then(
        (result) => {
          if (current) setState({ result, error: null, pending: false });
        },
        (cause: unknown) => {
          if (current) setState({ result: null, error: describeError(cause), pending: false });
        },
      );
    }, RULE_VALIDATE_DELAY_MS);
    return () => {
      current = false;
      clearTimeout(timer);
    };
  }, [request]);
  return state;
}

export interface HostOption {
  id: string;
  name: string;
}

interface StateRuleFormProps {
  form: StateRuleDraftForm;
  onChange: (form: StateRuleDraftForm) => void;
  hosts: readonly HostOption[];
  // Names a host id that is not in `hosts` (managed device, deleted asset, ...).
  hostLabel?: (id: string) => string;
  validation: RuleValidation;
  // A save the backend rejected; shown with the field errors.
  saveError?: string | null;
  // Limits the target states offered (a marking may not keep the state the user rejected).
  targets?: readonly StateRuleTarget[];
}

function FieldError({ id, message }: { id: string; message: string | null }) {
  if (!message) return null;
  return (
    <div id={id} className="mt-1 text-[11.5px] text-red-400">
      {message}
    </div>
  );
}

const fallbackHostLabel = (id: string) => resolveHostLabel(id, { assets: undefined, devices: undefined, loading: false });

export function StateRuleForm({ form, onChange, hosts, hostLabel, validation, saveError, targets }: StateRuleFormProps) {
  const id = useId();
  const result = validation.result;
  const invalid = result && !result.valid ? result : null;
  const fieldError = (...fields: string[]) => (invalid && fields.includes(invalid.field ?? "") ? invalid.error ?? null : null);
  const generalError = invalid && !["name", "scope", "region", "lines", "pattern", "state", "keys"].includes(invalid.field ?? "") ? invalid.error ?? null : null;
  const set = (patch: Partial<StateRuleDraftForm>) => onChange({ ...form, ...patch });
  const offered = STATE_RULE_TARGETS.filter((target) => !targets || targets.includes(target.value) || target.value === form.state);
  const patternError = fieldError("pattern");
  const scopeError = fieldError("scope");
  const linesError = fieldError("lines", "region");
  const stateError = fieldError("state");
  const nameError = fieldError("name");
  const keysError = fieldError("keys");
  const warning = patternWarning(form.pattern);
  const describedBy = (error: string | null, errorId: string, ...extra: string[]) => {
    const ids = [...(error ? [errorId] : []), ...extra];
    return ids.length > 0 ? ids.join(" ") : undefined;
  };

  return (
    <div className="flex flex-col gap-3">
      <div>
        <label className="nx-setting-label mb-1 block" htmlFor={`${id}-pattern`}>
          匹配模式（RE2 正则）
        </label>
        <textarea
          id={`${id}-pattern`}
          className="nx-input nx-mono min-h-[56px] w-full"
          rows={2}
          spellCheck={false}
          aria-invalid={patternError ? true : undefined}
          aria-describedby={describedBy(patternError, `${id}-pattern-error`, ...(warning ? [`${id}-pattern-warning`] : []))}
          value={form.pattern}
          onChange={(event) => set({ pattern: event.target.value })}
        />
        <FieldError id={`${id}-pattern-error`} message={patternError} />
        {warning && (
          <div id={`${id}-pattern-warning`} className="mt-1 text-[11.5px] text-amber-400" data-pattern-warning>
            {warning}
          </div>
        )}
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div>
          <label className="nx-setting-label mb-1 block" htmlFor={`${id}-region`}>
            匹配区域
          </label>
          <div className="flex gap-2">
            <select
              id={`${id}-region`}
              className="nx-select min-w-0 flex-1"
              aria-invalid={linesError ? true : undefined}
              aria-describedby={describedBy(linesError, `${id}-lines-error`)}
              value={form.region}
              onChange={(event) => set({ region: event.target.value === "title" ? "title" : "bottom" })}
            >
              <option value="bottom">屏幕底部</option>
              <option value="title">窗口标题</option>
            </select>
            {form.region === "bottom" && (
              <input
                className="nx-input w-20"
                type="number"
                min={1}
                max={STATE_RULE_MAX_LINES}
                aria-label="底部行数"
                aria-invalid={linesError ? true : undefined}
                aria-describedby={describedBy(linesError, `${id}-lines-error`)}
                value={form.lines}
                onChange={(event) => set({ lines: Math.trunc(Number(event.target.value)) || 0 })}
              />
            )}
          </div>
          <FieldError id={`${id}-lines-error`} message={linesError} />
        </div>
        <div>
          <label className="nx-setting-label mb-1 block" htmlFor={`${id}-state`}>
            匹配时视为
          </label>
          <select
            id={`${id}-state`}
            className="nx-select w-full"
            aria-invalid={stateError ? true : undefined}
            aria-describedby={describedBy(stateError, `${id}-state-error`)}
            value={form.state}
            onChange={(event) => set({ state: event.target.value as StateRuleTarget })}
          >
            {offered.map((target) => (
              <option key={target.value} value={target.value}>
                {target.label}
              </option>
            ))}
          </select>
          <FieldError id={`${id}-state-error`} message={stateError} />
        </div>
        <div>
          <label className="nx-setting-label mb-1 block" htmlFor={`${id}-scope`}>
            适用范围
          </label>
          <select
            id={`${id}-scope`}
            className="nx-select w-full"
            aria-invalid={scopeError ? true : undefined}
            aria-describedby={describedBy(scopeError, `${id}-scope-error`)}
            value={form.scope === "host" ? form.assetId || "host:" : "global"}
            onChange={(event) => {
              const value = event.target.value;
              if (value === "global") set({ scope: "global", assetId: "" });
              else set({ scope: "host", assetId: value === "host:" ? "" : value });
            }}
          >
            <option value="global">所有主机</option>
            {form.scope === "host" && !form.assetId && <option value="host:">选择主机…</option>}
            {form.scope === "host" && form.assetId && !hosts.some((host) => host.id === form.assetId) && (
              <option value={form.assetId}>{hostLabel?.(form.assetId) || fallbackHostLabel(form.assetId)}</option>
            )}
            {hosts.map((host) => (
              <option key={host.id} value={host.id}>
                仅 {host.name}
              </option>
            ))}
          </select>
          <FieldError id={`${id}-scope-error`} message={scopeError} />
        </div>
        <div>
          <label className="nx-setting-label mb-1 block" htmlFor={`${id}-name`}>
            名称
          </label>
          <input
            id={`${id}-name`}
            className="nx-input w-full"
            placeholder="留空时使用匹配模式"
            aria-invalid={nameError ? true : undefined}
            aria-describedby={describedBy(nameError, `${id}-name-error`)}
            value={form.name}
            onChange={(event) => set({ name: event.target.value })}
          />
          <FieldError id={`${id}-name-error`} message={nameError} />
        </div>
      </div>
      {form.state === "needs_me" && (
        <div>
          <label className="nx-setting-label mb-1 block" htmlFor={`${id}-keys`}>
            回应按键（可选）
          </label>
          <input
            id={`${id}-keys`}
            className="nx-input nx-mono w-full"
            placeholder="例如 y"
            aria-invalid={keysError ? true : undefined}
            aria-describedby={describedBy(keysError, `${id}-keys-error`, `${id}-keys-hint`)}
            value={form.keys}
            onChange={(event) => set({ keys: event.target.value })}
          />
          <p id={`${id}-keys-hint`} className="nx-hint mt-1">状态为「需要处理」时显示为按钮，仅在单击后发送。</p>
          <FieldError id={`${id}-keys-error`} message={keysError} />
        </div>
      )}
      <label className="flex items-center gap-2 text-[12.5px] text-neutral-200">
        <input type="checkbox" checked={form.enabled} onChange={(event) => set({ enabled: event.target.checked })} />
        启用
      </label>
      <RuleCheckStatus
        validation={validation}
        generalError={generalError}
        saveError={saveError ?? null}
        fieldMessage={invalid && !generalError ? invalid.error ?? null : null}
      />
    </div>
  );
}

function RuleCheckStatus({
  validation,
  generalError,
  saveError,
  fieldMessage,
}: {
  validation: RuleValidation;
  generalError: string | null;
  saveError: string | null;
  fieldMessage: string | null;
}) {
  const result = validation.result;
  return (
    <div className="flex flex-col gap-2" aria-live="polite">
      {fieldMessage && <span className="sr-only">规则有误：{fieldMessage}</span>}
      {generalError && (
        <div className="nx-alert nx-alert-danger text-[12px]" role="alert">
          {generalError}
        </div>
      )}
      {validation.error && (
        <div className="nx-alert nx-alert-danger text-[12px]" role="alert">
          无法检查规则：{validation.error}
        </div>
      )}
      {saveError && (
        <div className="nx-alert nx-alert-danger text-[12px]" role="alert">
          保存失败：{saveError}
        </div>
      )}
      {result?.valid && result.tested && (
        <div className={`nx-alert text-[12px] ${result.matched ? "nx-alert-success" : ""}`} data-rule-test={result.matched ? "match" : "miss"}>
          <div>{result.matched ? "匹配当前屏幕。" : "不匹配当前屏幕。"}</div>
          {result.region !== undefined && (
            <pre className="nx-code mt-1.5 max-h-32 overflow-auto whitespace-pre-wrap break-all text-[11px]">{result.region || "（区域为空）"}</pre>
          )}
        </div>
      )}
      {validation.pending && <div className="nx-hint text-[11.5px]">正在检查…</div>}
    </div>
  );
}
