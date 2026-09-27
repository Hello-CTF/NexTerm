// 模型选择器（P0-3）：下拉展示所有模型档案，点一条即切换激活。
//
// 「自己管自己」：自己拉档案列表、自己处理切换，外部只给一个 onManage
// （打开配置面板的回调）。因此可以直接丢进 AI 功能行替换原来的模型胶囊。
//
// 每次展开都重新拉一次列表 —— 配置面板里刚改完/刚切过的结果要立刻反映出来，
// 缓存一份旧列表只会让用户以为「改了没生效」。
import { useEffect, useRef, useState } from "react";
import { modelApi, type ModelProfilesView } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { IconCheck, IconChevronUp, IconLoader, IconPlus, IconSettings } from "../../ui/icons";

export function ModelSelector({ onManage }: { onManage: () => void }) {
  const pushToast = useUi((s) => s.pushToast);
  const [view, setView] = useState<ModelProfilesView | null>(null);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const boxRef = useRef<HTMLDivElement>(null);

  const refresh = async () => {
    try {
      setView(await modelApi.overview());
    } catch (e) {
      pushToast("error", `读取模型档案失败：${describeError(e)}`);
    }
  };

  useEffect(() => {
    void refresh();
    // 只在挂载时拉一次；展开下拉时会再刷新
  }, []);

  // 点外部 / Esc 收起
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (boxRef.current && !boxRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
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
    if (next) void refresh();
  };

  const choose = async (id: string, name: string) => {
    setOpen(false);
    setBusy(true);
    try {
      await modelApi.activate(id);
      await refresh();
      pushToast("info", `当前模型已切换为「${name}」`);
    } catch (e) {
      pushToast("error", `切换失败：${describeError(e)}`);
    } finally {
      setBusy(false);
    }
  };

  const active = view?.profiles.find((p) => p.id === view.activeId) ?? null;

  return (
    <div className="relative" ref={boxRef}>
      <button
        className="nx-chip max-w-[180px]"
        title="模型配置（BYOK：可保存多份档案）"
        onClick={toggle}
      >
        {busy ? (
          <IconLoader size={11} className="animate-spin" />
        ) : (
          <IconPlus size={11} />
        )}
        <span className="truncate">{active?.name || "添加模型"}</span>
        <IconChevronUp size={10} className="shrink-0 opacity-60" />
      </button>

      {open && (
        <div className="absolute bottom-full left-0 z-[80] mb-1 max-h-72 w-[248px] overflow-y-auto rounded-md border border-neutral-700 bg-neutral-900 p-1 shadow-lg">
          <div className="nx-menu-title">选择模型档案</div>
          {view && view.profiles.length > 0 ? (
            view.profiles.map((p) => (
              <button
                key={p.id}
                className="nx-menu-item w-full"
                title={p.model || p.baseUrl}
                onClick={() => void choose(p.id, p.name)}
              >
                <span className="nx-menu-icon">
                  {view.activeId === p.id ? (
                    <IconCheck size={12} className="text-green-300" />
                  ) : (
                    <span className="nx-dot" />
                  )}
                </span>
                <span className="nx-menu-label">{p.name}</span>
                <span className="nx-menu-hint">{p.model}</span>
              </button>
            ))
          ) : (
            <div className="nx-hint px-2 py-2 text-[11px]">还没有模型档案</div>
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
            <span className="nx-menu-label">管理模型…</span>
          </button>
        </div>
      )}
    </div>
  );
}
