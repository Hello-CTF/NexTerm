import { useState } from "react";
import type { AnswerNote, HostLive, Prompt } from "../../ipc/generated/types";
import { t } from "../../i18n";

// Everything in a prompt, message or step title is text printed by a remote
// script. It is only ever rendered as React text children (never as HTML,
// markdown or a link), inside a frame that names the script and the host, so a
// hostile host cannot pose as NexTerm's own interface.

const LEVEL_CLASS: Record<string, string> = {
  info: "text-neutral-300",
  warn: "text-amber-300",
  error: "text-red-300",
};
const LEVEL_LABEL: Record<string, string> = { info: t("信息"), warn: t("警告"), error: t("错误") };

export function ScriptActivity({ live, answers }: { live: readonly HostLive[]; answers: readonly AnswerNote[] }) {
  if (live.length === 0 && answers.length === 0) return null;
  return (
    <div className="mt-3 rounded border border-neutral-800 p-2.5" data-testid="runbook-script-activity">
      <div className="mb-2 text-[11px] font-medium text-neutral-400">{t("脚本上报的进度（内容由远端脚本提供）")}</div>
      {live.map((host) => (
        <div key={host.sessionId} className="mb-2 last:mb-0" data-testid={`runbook-script-host-${host.sessionId}`}>
          <div className="flex flex-wrap items-center gap-2 text-[11.5px]">
            <span className="text-neutral-300" title={host.sessionId}>{t("主机")} {host.hostName}</span>
            {host.subStep && <span className="text-neutral-400">{t("· 脚本步骤：")}{host.subStep}</span>}
          </div>
          {host.progress >= 0 && (
            <div className="mt-1 flex items-center gap-2">
              <div
                role="progressbar"
                aria-label={t("主机 {hostName} 的脚本进度", { hostName: host.hostName })}
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={host.progress}
                className="h-1.5 flex-1 overflow-hidden rounded bg-neutral-800"
              >
                <div className="h-full bg-blue-500" style={{ width: `${Math.min(100, Math.max(0, host.progress))}%` }} />
              </div>
              <span className="nx-mono text-[10.5px] text-neutral-400">{host.progress}%</span>
            </div>
          )}
          {(host.messages ?? []).map((message, index) => (
            <div key={index} className={`mt-1 whitespace-pre-wrap break-all text-[11px] ${LEVEL_CLASS[message.level] ?? "text-neutral-300"}`}>
              [{LEVEL_LABEL[message.level] ?? message.level}] {message.text}
            </div>
          ))}
        </div>
      ))}
      {answers.length > 0 && (
        <div className="mt-2 border-t border-neutral-800 pt-2" data-testid="runbook-script-answers">
          <div className="mb-1 text-[11px] text-neutral-500">{t("已提供的输入（不含保密输入）")}</div>
          {answers.map((answer, index) => (
            <div key={index} className="whitespace-pre-wrap break-all text-[11px] text-neutral-400">
              {t("主机")} {answer.hostName} · {answer.variable || (answer.kind === "confirm" ? t("确认") : t("输入"))} = {answer.value}
              {answer.auto ? t("（沿用应用到全部）") : ""}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

export function ScriptPrompts({
  prompts,
  disabled,
  onRespond,
}: {
  prompts: readonly Prompt[];
  disabled: boolean;
  onRespond: (prompt: Prompt, value: string, applyAll: boolean) => Promise<void>;
}) {
  if (prompts.length === 0) return null;
  return (
    <div className="mt-3 flex flex-col gap-2" data-testid="runbook-script-prompts">
      {prompts.map((prompt) => (
        <ScriptPromptCard
          key={prompt.id}
          prompt={prompt}
          disabled={disabled}
          shared={prompts.filter((other) => other.id !== prompt.id && other.kind === prompt.kind && other.variable === prompt.variable && other.text === prompt.text).length}
          onRespond={onRespond}
        />
      ))}
    </div>
  );
}

function ScriptPromptCard({
  prompt,
  disabled,
  shared,
  onRespond,
}: {
  prompt: Prompt;
  disabled: boolean;
  shared: number;
  onRespond: (prompt: Prompt, value: string, applyAll: boolean) => Promise<void>;
}) {
  const secret = prompt.kind === "secret";
  const confirm = prompt.kind === "confirm";
  const [value, setValue] = useState(secret ? "" : (prompt.default ?? ""));
  const [applyAll, setApplyAll] = useState(false);
  const [sending, setSending] = useState(false);
  const invalid = /[\r\n\0]/.test(value);

  const submit = async (answer: string) => {
    if (sending) return;
    setSending(true);
    try {
      await onRespond(prompt, answer, applyAll && prompt.canApplyAll);
    } finally {
      // A secret never outlives its submission in component state.
      if (secret) setValue("");
      setSending(false);
    }
  };

  return (
    <section
      className="rounded border border-amber-700/60 bg-amber-950/20 p-2.5"
      aria-label={t("来自主机 {hostName} 上脚本的{value}请求", { hostName: prompt.hostName, value: confirm ? t("确认") : t("输入") })}
      data-testid={`runbook-script-prompt-${prompt.id}`}
    >
      <div className="mb-1 text-[10.5px] text-amber-300">
        {t("以下内容由主机「")}{prompt.hostName}{t("」上运行的脚本提供，不是 NexTerm 的系统提示")}
        {secret ? t(" · 保密输入：不会被保存或记录") : ""}
      </div>
      <div className="whitespace-pre-wrap break-all text-[12px] text-neutral-100" data-testid="runbook-script-prompt-text">{prompt.text}</div>
      <div className="mt-0.5 text-[10.5px] text-neutral-500">
        {prompt.variable ? t("变量 {variable} · ", { variable: prompt.variable }) : ""}{t("将在")} {new Date(prompt.expiresAt).toLocaleTimeString()} {t("超时并使该主机的执行失败")}
      </div>
      {confirm ? (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <button type="button" className="nx-btn nx-btn-primary nx-btn-sm" disabled={disabled || sending} onClick={() => void submit("yes")}>{t("是")}</button>
          <button type="button" className="nx-btn nx-btn-sm" disabled={disabled || sending} onClick={() => void submit("no")}>{t("否")}</button>
        </div>
      ) : (
        <form
          className="mt-2 flex flex-wrap items-center gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            if (!invalid) void submit(value);
          }}
        >
          <input
            className="nx-input min-w-48 flex-1"
            type={secret ? "password" : "text"}
            autoComplete={secret ? "new-password" : "off"}
            spellCheck={false}
            maxLength={1024}
            aria-label={t("主机 {hostName} 的{value}输入", { hostName: prompt.hostName, value: secret ? t("保密") : "" })}
            value={value}
            onChange={(event) => setValue(event.target.value)}
          />
          <button type="submit" className="nx-btn nx-btn-primary nx-btn-sm" disabled={disabled || sending || invalid}>{t("提交")}</button>
        </form>
      )}
      {prompt.canApplyAll && shared > 0 && (
        <label className="mt-2 flex items-center gap-1.5 text-[11px] text-neutral-400">
          <input type="checkbox" className="accent-blue-500" checked={applyAll} onChange={(event) => setApplyAll(event.target.checked)} />
          {t("应用到全部（同一步骤中提示相同的其他主机沿用此答案）")}
        </label>
      )}
    </section>
  );
}

const HELP_EXAMPLE = t("sh -c 'printf \"@NEXTERM:STEP 准备环境\\n@NEXTERM:PROGRESS 10\\n\"; printf \"@NEXTERM:INPUT port 监听端口\\t8080\\n\"; read port; printf \"@NEXTERM:SECRET token 访问令牌\\n\"; read token; printf \"@NEXTERM:CONFIRM 现在重启服务？\\n\"; read answer; [ \"$answer\" = yes ] || exit 0; printf \"@NEXTERM:MESSAGE warn 即将重启\\n@NEXTERM:PROGRESS 100\\n\"; echo \"port=$port\"'");

export function ScriptDirectiveHelp() {
  return (
    <details className="mb-3 rounded border border-neutral-800 p-2 text-[11px] text-neutral-400" data-testid="runbook-directive-help">
      <summary className="cursor-pointer text-neutral-300">{t("脚本交互指令说明")}</summary>
      <div className="mt-2 flex flex-col gap-1.5">
        <p>{t("勾选「启用脚本交互指令」后，脚本打印以")} <code className="nx-mono">@NEXTERM:</code> {t("开头的单独一行即可显示进度或向操作员提问（提问仅 SSH）。")}</p>
        <ul className="list-disc pl-4">
          <li><code className="nx-mono">{t("@NEXTERM:STEP 标题")}</code>{t("：标记当前子步骤")}</li>
          <li><code className="nx-mono">@NEXTERM:PROGRESS 0-100</code>{t("：更新进度")}</li>
          <li><code className="nx-mono">{t("@NEXTERM:INPUT 变量名 提示语[Tab 默认值]")}</code>{t("：暂停并向操作员询问一个值，答案作为一行写入脚本的标准输入")}</li>
          <li><code className="nx-mono">{t("@NEXTERM:SECRET 变量名 提示语")}</code>{t("：同上，但使用遮蔽输入框，答案不会被保存或记录")}</li>
          <li><code className="nx-mono">{t("@NEXTERM:CONFIRM 提示语")}</code>{t("：询问是或否，标准输入收到")} <code className="nx-mono">yes</code> {t("或")} <code className="nx-mono">no</code></li>
          <li><code className="nx-mono">{t("@NEXTERM:MESSAGE info|warn|error 文本")}</code>{t("：显示一条消息")}</li>
        </ul>
        <p>{t("多主机执行时每步命令是单行的，多条语句可用")} <code className="nx-mono">sh -c '...'</code> {t("包装；下面的示例用")} <code className="nx-mono">printf</code> {t("打印指令，Tab 写作")} <code className="nx-mono">{"\\t"}</code>。</p>
        <pre className="nx-pre whitespace-pre-wrap break-all text-[11px]">{HELP_EXAMPLE}</pre>
        <details>
          <summary className="cursor-pointer text-neutral-300">{t("规则与限制")}</summary>
          <ul className="mt-1.5 list-disc pl-4">
            <li>{t("指令行不会出现在普通输出中；未勾选时输出原样显示，标准输入照常立即关闭。")}</li>
            <li>{t("勾选后标准输入保持打开直到脚本结束，只读取脚本自己请求的行，不要读到输入结束。")}</li>
            <li>{t("变量名只能包含字母、数字和下划线，且不以数字开头。")}</li>
            <li>{t("指令行最长 1024 字节；提示语、标题和消息最长 200 个字符，不能包含控制字符。")}</li>
            <li>{t("格式错误的指令作为普通输出显示并给出警告，不中断运行。")}</li>
            <li>{t("受管设备和其他连接类型只支持 STEP、PROGRESS 和 MESSAGE，提问会显示警告并被忽略。")}</li>
            <li>{t("每台主机最多提问 32 次，超出则该主机失败；步骤、进度和消息有频率限制，进度只保留最新值。")}</li>
            <li>{t("每台主机分别提问、分别回答；等待回答不计入命令超时，超过 5 分钟没有回答则该主机失败。")}</li>
            <li>{t("输出和提问来自远端主机，只以纯文本显示并标明来源；保密输入不回显，不要在脚本中打印它。")}</li>
          </ul>
        </details>
      </div>
    </details>
  );
}
