import { useEffect, useRef, useState } from "react";
import { aiCommandApi, type AiCommandGenerateRequest } from "../../ipc/aiCommandApi";
import { modelApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { copyText } from "../../ui/clipboard";
import { insertRisk } from "../explorer/snippetInsert";
import { IconClose, IconLoader, IconSparkles } from "../../ui/icons";
import type { CommandInsertTarget } from "./insertCommand";
import { t } from "../../i18n";

export type AiCommandContext = Omit<AiCommandGenerateRequest, "prompt">;

type Phase =
  | { kind: "idle" }
  | { kind: "loading" }
  | { kind: "result"; command: string }
  | { kind: "error"; text: string; unavailable: boolean };

function errorCode(cause: unknown): string {
  return cause && typeof cause === "object" && typeof (cause as { code?: unknown }).code === "string"
    ? (cause as { code: string }).code
    : "";
}

// AiCommandPrompt is the Warp-style "generate a command" popover: the user describes a task in plain words, the
// active AI model proposes one command for this terminal, and inserting it types it at the prompt without Enter.
export function AiCommandPrompt({
  context,
  target,
  onInsert,
  onClose,
}: {
  context: AiCommandContext;
  // The terminal the command would go to; null while it cannot take input.
  target: CommandInsertTarget | null;
  onInsert: (command: string) => Promise<boolean>;
  onClose: () => void;
}) {
  const [prompt, setPrompt] = useState("");
  const [phase, setPhase] = useState<Phase>({ kind: "idle" });
  const [noModel, setNoModel] = useState(false);
  const [inserting, setInserting] = useState(false);
  const requestRef = useRef(0);

  useEffect(() => {
    let alive = true;
    Promise.resolve(modelApi.overview()).then(
      (view) => {
        if (alive) setNoModel((view?.profiles?.length ?? 1) === 0);
      },
      () => undefined,
    );
    return () => {
      alive = false;
      // A reply that arrives after the popover closed is dropped.
      requestRef.current += 1;
    };
  }, []);

  const generate = () => {
    const text = prompt.trim();
    if (!text || phase.kind === "loading") return;
    const request = ++requestRef.current;
    setPhase({ kind: "loading" });
    aiCommandApi.generate({ ...context, prompt: text }).then(
      (result) => {
        if (request === requestRef.current) setPhase({ kind: "result", command: result.command });
      },
      (cause: unknown) => {
        if (request !== requestRef.current) return;
        const unavailable = errorCode(cause) === "unsupported";
        setPhase({
          kind: "error",
          unavailable,
          text: unavailable
            ? t("AI 不可用：请先在 AI 侧栏添加并选择模型。")
            : t("生成命令失败：{value}", { value: describeError(cause) }),
        });
      },
    );
  };

  const insert = async () => {
    if (phase.kind !== "result" || !target || inserting) return;
    setInserting(true);
    try {
      if (await onInsert(phase.command)) onClose();
    } finally {
      setInserting(false);
    }
  };

  // A multi-line or control-character command is never typed into the terminal (it would run), only copied.
  const insertable = phase.kind === "result" && insertRisk(phase.command) === "none";
  const copyResult = () => {
    if (phase.kind !== "result") return;
    const pushToast = useUi.getState().pushToast;
    void copyText(phase.command).then(
      () => pushToast("success", t("已复制到剪贴板")),
      (cause: unknown) => pushToast("error", t("复制失败：{value}", { value: describeError(cause) })),
    );
  };

  const openAiSidebar = () => {
    useUi.getState().setRightOpen(true);
    onClose();
  };

  return (
    <div
      role="dialog"
      aria-label={t("用 AI 生成命令")}
      data-testid="ai-command-prompt"
      className="absolute inset-x-2 bottom-2 z-20 mx-auto flex max-w-[560px] flex-col gap-1.5 rounded-lg border border-neutral-700/80 bg-neutral-900/95 p-2 shadow-[0_8px_24px_rgba(0,0,0,0.5)]"
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          onClose();
        }
      }}
    >
      <div className="flex items-center gap-1.5">
        <IconSparkles size={13} className="shrink-0 text-blue-300" />
        <input
          autoFocus
          className="nx-input nx-input-sm min-w-0 flex-1"
          aria-label={t("描述要执行的操作")}
          placeholder={t("描述要做的事，例如：找出 7 天内修改过的大于 100MB 的文件")}
          value={prompt}
          disabled={noModel}
          onChange={(e) => {
            setPrompt(e.target.value);
            if (phase.kind !== "loading") setPhase({ kind: "idle" });
          }}
          onKeyDown={(e) => {
            if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
            e.preventDefault();
            if (phase.kind !== "result") generate();
            else if (insertable) void insert();
            else copyResult();
          }}
        />
        <button className="nx-icon-btn nx-icon-btn-sm" aria-label={t("关闭")} title={t("关闭（Esc）")} onClick={onClose}>
          <IconClose size={11} />
        </button>
      </div>

      {noModel ? (
        <div className="flex items-center gap-2 px-0.5 text-[11.5px] text-amber-300">
          {t("尚未配置模型，AI 暂时无法生成命令。")}
          <button className="nx-btn nx-btn-primary nx-btn-xs" onClick={openAiSidebar}>
            {t("打开 AI 侧栏")}
          </button>
        </div>
      ) : phase.kind === "loading" ? (
        <div role="status" className="flex items-center gap-1.5 px-0.5 text-[11.5px] text-neutral-400">
          <IconLoader size={12} className="animate-spin" />
          {t("正在生成命令…")}
        </div>
      ) : phase.kind === "error" ? (
        <div role="alert" className="flex items-center gap-2 px-0.5 text-[11.5px] text-red-300">
          <span className="min-w-0 flex-1">{phase.text}</span>
          {phase.unavailable && (
            <button className="nx-btn nx-btn-ghost nx-btn-xs" onClick={openAiSidebar}>
              {t("打开 AI 侧栏")}
            </button>
          )}
        </div>
      ) : phase.kind === "result" ? (
        <div className="flex flex-col gap-1.5">
          <pre
            data-testid="ai-command-result"
            className="max-h-40 overflow-auto rounded-md border border-neutral-800 bg-term px-2 py-1.5 font-mono text-[11.5px] whitespace-pre-wrap text-neutral-100"
          >
            {phase.command}
          </pre>
          {target?.productionEnvironment && (
            <div className="px-0.5 text-[11px] text-amber-300">
              {t("这是生产环境「{environment}」的终端，填入前会再次确认。", { environment: target.productionEnvironment })}
            </div>
          )}
          <div className="flex items-center gap-1.5">
            <span className="min-w-0 flex-1 px-0.5 text-[10.5px] text-neutral-500">
              {!insertable
                ? t("命令有多行或包含控制字符，填入会直接执行，只能复制后手动粘贴。")
                : target
                  ? t("填入后不会自动执行，请检查后再按回车。")
                  : t("当前终端已断开或已结束")}
            </span>
            <button className="nx-btn nx-btn-ghost nx-btn-xs" onClick={generate}>
              {t("重新生成")}
            </button>
            {insertable ? (
              <button className="nx-btn nx-btn-primary nx-btn-xs" disabled={!target || inserting} onClick={() => void insert()}>
                {t("填入终端")}
              </button>
            ) : (
              <button className="nx-btn nx-btn-primary nx-btn-xs" onClick={copyResult}>
                {t("复制命令")}
              </button>
            )}
          </div>
        </div>
      ) : (
        <div className="px-0.5 text-[10.5px] text-neutral-500">
          {t("按回车生成；AI 只会看到描述、当前目录、主机名和 Shell 类型。")}
        </div>
      )}
    </div>
  );
}
