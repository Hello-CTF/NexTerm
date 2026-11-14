import { useEffect, useRef, useState } from "react";
import { modelApi, type ModelProfilesView } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { IconCheck, IconChevronUp, IconPlus, IconSettings } from "../../ui/icons";
import { t } from "../../i18n";
import { setChatModelChoice, useChatModelChoice } from "./chatModelChoice";

// ModelSelector picks the model of one AI chat tab. The default model stays as the model settings set it; a tab
// without a choice of its own follows it.
export function ModelSelector({ onManage, tabKey = "" }: { onManage: () => void; tabKey?: string }) {
  const pushToast = useUi((s) => s.pushToast);
  const profilesRevision = useUi((s) => s.modelProfilesRevision);
  const [view, setView] = useState<ModelProfilesView | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const choice = useChatModelChoice(tabKey);
  const boxRef = useRef<HTMLDivElement>(null);
  const chipRef = useRef<HTMLButtonElement>(null);
  const [dropdownWidth, setDropdownWidth] = useState(248);

  const refresh = async () => {
    try {
      setView(await modelApi.overview());
      setLoadError(null);
    } catch (e) {
      setView(null);
      setLoadError(describeError(e));
    }
  };

  useEffect(() => {
    void refresh();
  }, [profilesRevision]);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (boxRef.current && !boxRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
        chipRef.current?.focus();
      }
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const toggle = () => {
    const next = !open;
    setOpen(next);
    if (next) {
      const rect = chipRef.current?.getBoundingClientRect();
      if (rect) {
        setDropdownWidth(Math.min(248, Math.max(0, window.innerWidth - rect.left - 8)));
      }
      void refresh();
    }
  };

  // A chosen profile that was deleted meanwhile no longer applies; the tab follows the default again.
  useEffect(() => {
    if (view && choice && !view.profiles.some((p) => p.id === choice)) setChatModelChoice(tabKey, null);
  }, [view, choice, tabKey]);

  const choose = (id: string, name: string) => {
    setOpen(false);
    const isDefault = id === view?.activeId;
    setChatModelChoice(tabKey, isDefault ? null : id);
    pushToast(
      "info",
      isDefault
        ? t("本对话改用默认模型「{name}」", { name })
        : t("本对话改用「{name}」，默认模型不变", { name }),
    );
  };

  const effectiveId = (choice && view?.profiles.some((p) => p.id === choice) ? choice : null) ?? view?.activeId ?? null;
  const active = view?.profiles.find((p) => p.id === effectiveId) ?? null;

  return (
    <div className="relative flex min-w-0 max-w-[220px]" ref={boxRef}>
      <button
        ref={chipRef}
        className="nx-chip pointer-coarse:min-h-6 max-w-full min-w-0"
        title={t("模型配置（可保存多份模型档案）")}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={toggle}
      >
        {active ? (
          <IconCheck size={11} className="text-green-300" />
        ) : (
          <IconPlus size={11} />
        )}
        <span className="min-w-0 truncate">
          {active?.name || (loadError ? t("未能读取模型档案") : t("添加模型"))}
        </span>
        <IconChevronUp size={10} className="shrink-0 opacity-60" />
      </button>

      {open && (
        <div
          className="absolute bottom-full left-0 z-[80] mb-1 max-h-[min(288px,calc(100dvh-24px))] overflow-y-auto rounded-md border border-neutral-700 bg-neutral-900 p-1 shadow-lg"
          style={{ width: dropdownWidth }}
        >
          <div className="nx-menu-title">{t("选择模型档案")}</div>
          {loadError ? (
            <div className="px-2 py-2 text-center">
              <div className="nx-hint text-red-300">{t("未能读取模型档案：{loadError}", { loadError })}</div>
              <button className="nx-btn nx-btn-ghost nx-btn-xs mt-1.5" onClick={() => void refresh()}>
                {t("重试")}
              </button>
            </div>
          ) : view && view.profiles.length > 0 ? (
            view.profiles.map((p) => (
              <button
                key={p.id}
                className="nx-menu-item w-full"
                title={p.model || p.baseUrl}
                onClick={() => choose(p.id, p.name)}
              >
                <span className="nx-menu-icon">
                  {effectiveId === p.id ? (
                    <IconCheck size={12} className="text-green-300" />
                  ) : (
                    <span className="nx-dot" />
                  )}
                </span>
                <span className="nx-menu-label">{p.name}</span>
                <span className="nx-menu-hint">
                  {view.activeId === p.id ? t("默认") : p.model}
                </span>
              </button>
            ))
          ) : (
            <div className="nx-hint px-2 py-2 text-[11px]">{t("暂无模型档案")}</div>
          )}
          <div className="nx-menu-sep" />
          <button
            className="nx-menu-item w-full"
            onClick={() => {
              setOpen(false);
              onManage();
            }}
          >
            <span className="nx-menu-icon">
              <IconSettings size={12} />
            </span>
            <span className="nx-menu-label">{t("管理模型…")}</span>
          </button>
        </div>
      )}
    </div>
  );
}
