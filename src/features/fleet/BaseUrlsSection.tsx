// 接入地址 (fleet base URLs) 卡片: 超管可有序编辑 (添加/删除/上下移),
// 普通用户只读。改动经 PUT /fleet/base-urls 保存, 以服务端规范化后的返回为准。
// 保存只影响之后 enroll 的设备; 已接入设备持有 enroll 时下发的地址列表。

import { useEffect, useRef, useState } from "react";
import { fleetApi, type FleetBaseURLEntry } from "../../ipc/fleetApi";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { IconArrowDown, IconArrowUp, IconGlobe, IconPlus, IconSave, IconTrash, IconXCircle } from "../../ui/icons";
import { MAX_BASE_URLS, validateBaseURL } from "./baseUrlValidation";

interface BaseUrlsSectionProps {
  entries: FleetBaseURLEntry[];
  isAdmin: boolean;
  onSaved: (entries: FleetBaseURLEntry[]) => void;
}

export function BaseUrlsSection({ entries, isAdmin, onSaved }: BaseUrlsSectionProps) {
  const { pushToast } = useUi();
  const [draft, setDraft] = useState<FleetBaseURLEntry[]>(entries);
  const [dirty, setDirty] = useState(false);
  const [input, setInput] = useState("");
  const [insecure, setInsecure] = useState(false);
  const [inputError, setInputError] = useState<string | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  // 保存成功后 draft 已是服务端权威列表, 跳过紧接着的那次 prop 同步,
  // 避免 effect 用保存前的 entries prop 把服务端结果覆盖回去。
  const skipPropSync = useRef(false);

  useEffect(() => {
    if (dirty) return;
    if (skipPropSync.current) {
      skipPropSync.current = false;
      return;
    }
    setDraft(entries);
  }, [entries, dirty]);

  const add = () => {
    const result = validateBaseURL(input, insecure);
    if (result.error) {
      setInputError(result.error);
      return;
    }
    if (draft.some((e) => e.url === result.url)) {
      setInputError("该地址已在列表中");
      return;
    }
    if (draft.length >= MAX_BASE_URLS) {
      setInputError(`接入地址最多 ${MAX_BASE_URLS} 个`);
      return;
    }
    setDraft([...draft, { url: result.url, ...(insecure ? { insecure: true } : {}) }]);
    setDirty(true);
    setInput("");
    setInsecure(false);
    setInputError(null);
  };

  const remove = (index: number) => {
    setDraft(draft.filter((_, i) => i !== index));
    setDirty(true);
  };

  const move = (index: number, delta: -1 | 1) => {
    const next = index + delta;
    if (next < 0 || next >= draft.length) return;
    const copy = [...draft];
    [copy[index], copy[next]] = [copy[next], copy[index]];
    setDraft(copy);
    setDirty(true);
  };

  const save = async () => {
    setSaving(true);
    setSaveError(null);
    try {
      const saved = await fleetApi.putBaseUrls(draft);
      onSaved(saved.base_urls);
      setDraft(saved.base_urls);
      setDirty(false);
      skipPropSync.current = true;
      pushToast("success", "接入地址已保存");
    } catch (e) {
      setSaveError(describeError(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconGlobe size={15} className="text-neutral-400" />
        <span className="nx-card-title">接入地址</span>
        <span className="nx-hint">设备按顺序尝试, 第一个可达的即当前接入点</span>
      </div>
      <p className="nx-hint mb-3">
        接入地址是设备用来连接这台 NexTerm 服务器的地址 (如 https://nexterm.example.com); 新设备接入时按此顺序拿到地址列表, 保存只影响之后接入的设备, 已接入设备仍使用注册时拿到的列表。
        {!isAdmin && " 仅超级管理员可修改。"}
      </p>

      {draft.length === 0 && (
        <div className="nx-hint py-1 text-[12px]">
          {isAdmin ? "还没有配置接入地址: 新设备无法接入。请在下方添加设备能访问到的服务器地址。" : "还没有配置接入地址: 新设备无法接入, 请联系超级管理员配置。"}
        </div>
      )}

      {draft.length > 0 && (
        <div className="mb-3 flex flex-col gap-1">
          {draft.map((entry, index) => (
            <div
              key={entry.url}
              className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded border border-neutral-800/60 px-2.5 py-1.5"
            >
              <span className="nx-hint shrink-0 text-[11px]">#{index + 1}</span>
              <code className="nx-code min-w-0 flex-1 break-all font-mono text-[12px]" title={entry.url}>
                {entry.url}
              </code>
              {entry.insecure && (
                <span className="nx-badge nx-badge-amber shrink-0" title="允许明文 HTTP 或跳过 TLS 校验, 仅限内网/自签名环境">
                  不安全传输
                </span>
              )}
              {isAdmin && (
                <span className="flex shrink-0 items-center gap-0.5">
                  <button
                    type="button"
                    className="nx-btn nx-btn-ghost nx-btn-xs"
                    disabled={saving || index === 0}
                    title="上移"
                    aria-label={`上移 ${entry.url}`}
                    onClick={() => move(index, -1)}
                  >
                    <IconArrowUp size={11} />
                  </button>
                  <button
                    type="button"
                    className="nx-btn nx-btn-ghost nx-btn-xs"
                    disabled={saving || index === draft.length - 1}
                    title="下移"
                    aria-label={`下移 ${entry.url}`}
                    onClick={() => move(index, 1)}
                  >
                    <IconArrowDown size={11} />
                  </button>
                  <button
                    type="button"
                    className="nx-btn nx-btn-ghost nx-btn-xs"
                    disabled={saving}
                    title="删除"
                    aria-label={`删除 ${entry.url}`}
                    onClick={() => remove(index)}
                  >
                    <IconTrash size={11} />
                  </button>
                </span>
              )}
            </div>
          ))}
        </div>
      )}

      {isAdmin && (
        <div className="flex flex-col gap-2 border-t border-neutral-800/60 pt-3">
          <div className="flex flex-wrap items-center gap-2">
            <input
              className="nx-input min-w-0 flex-1"
              style={{ maxWidth: 420 }}
              placeholder="https://nexterm.example.com"
              value={input}
              aria-label="新接入地址"
              disabled={saving}
              onChange={(e) => {
                setInput(e.target.value);
                setInputError(null);
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  add();
                }
              }}
            />
            <label className="flex shrink-0 items-center gap-1.5 text-[12px] text-neutral-300">
              <input
                type="checkbox"
                checked={insecure}
                disabled={saving}
                onChange={(e) => {
                  setInsecure(e.target.checked);
                  setInputError(null);
                }}
              />
              允许明文 HTTP / 跳过 TLS 校验
            </label>
            <button
              type="button"
              className="nx-btn nx-btn-outline nx-btn-sm shrink-0"
              disabled={saving || !input.trim() || draft.length >= MAX_BASE_URLS}
              onClick={add}
            >
              <IconPlus size={12} />
              添加
            </button>
          </div>
          {inputError && (
            <div className="nx-alert nx-alert-danger flex items-start gap-2">
              <IconXCircle size={13} className="mt-0.5 shrink-0" />
              <span>{inputError}</span>
            </div>
          )}
          {saveError && (
            <div className="nx-alert nx-alert-danger flex items-start gap-2">
              <IconXCircle size={13} className="mt-0.5 shrink-0" />
              <span className="min-w-0 flex-1 break-words">保存失败 · {saveError}</span>
            </div>
          )}
          <div className="flex items-center gap-2">
            <button
              type="button"
              className="nx-btn nx-btn-primary nx-btn-sm"
              disabled={!dirty || saving}
              onClick={() => void save()}
            >
              <IconSave size={12} />
              {saving ? "保存中…" : "保存顺序与修改"}
            </button>
            {saving ? (
              <span className="nx-hint text-[11px]">保存中, 编辑已锁定…</span>
            ) : (
              dirty && <span className="nx-hint text-[11px]">有未保存的修改</span>
            )}
          </div>
        </div>
      )}
    </section>
  );
}
