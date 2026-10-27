import { useId, useRef, useState } from "react";
import { terminalApi } from "../../ipc/commands";
import type { AttentionPrompt } from "../../ipc/generated/types";
import { refreshAwareness } from "../../app/sessionAwareness";
import type { SessionAwareness } from "../../app/sessionAwarenessStore";
import { describeError } from "../../ui/errorText";
import { OPEN_TO_ANSWER, promptBlockedHint, promptIdentity, promptQuestion, separateEnterKey } from "./promptChoices";

export interface ChoiceButtonsProps {
  // The backend terminal tab whose prompt this is.
  tabId: string;
  prompt: AttentionPrompt;
  // Set when this client cannot answer here: the buttons are shown disabled with this explanation.
  unavailable?: string;
  // The one-line layout of the terminal pane's bar.
  compact?: boolean;
}

type Phase = "idle" | "sending" | "sent" | "stale" | "failed";

function errorCode(error: unknown): string {
  if (typeof error !== "object" || error === null) return "";
  const code = (error as { code?: unknown }).code;
  return typeof code === "string" ? code : "";
}

// ChoiceButtons shows a detected prompt's question with its options as buttons. A tap names the prompt instance
// (generation, epoch, fingerprint); the helper refuses it when the screen moved on, and the view then says so and
// reloads the states instead of trying again. Only one tap or Enter is in flight at a time.
export function ChoiceButtons(props: ChoiceButtonsProps) {
  // A new prompt instance starts with fresh buttons: what was sent or refused belongs to the previous one.
  return <PromptChoices key={promptIdentity(props.prompt)} {...props} />;
}

function PromptChoices({ tabId, prompt, unavailable, compact }: ChoiceButtonsProps) {
  const id = useId();
  const [phase, setPhase] = useState<Phase>("idle");
  const [message, setMessage] = useState("");
  // The flag is set before React renders the disabled buttons, so a second tap in the same frame is dropped too.
  const inFlight = useRef(false);
  const options = prompt.options ?? [];
  const blocked = promptBlockedHint(prompt);
  const enterKey = separateEnterKey(prompt);
  const question = promptQuestion(prompt);
  const showOptions = blocked === null;
  const optionsDisabled = unavailable !== undefined || phase === "sending" || phase === "sent" || phase === "stale";
  const enterDisabled = unavailable !== undefined || phase === "sending" || phase === "stale";

  const run = async (label: string, send: () => Promise<void>) => {
    if (inFlight.current) return;
    inFlight.current = true;
    setPhase("sending");
    setMessage("正在发送…");
    try {
      await send();
      setPhase("sent");
      setMessage(`已发送「${label}」`);
    } catch (error) {
      const code = errorCode(error);
      if (code === "stale_prompt") {
        setPhase("stale");
        setMessage("提示已变化");
        // Show what the screen asks now; the user decides again.
        void refreshAwareness().catch(() => undefined);
      } else if (code === "unsupported") {
        setPhase("stale");
        setMessage("这个终端不支持按钮回答，请在终端里输入");
      } else {
        setPhase("failed");
        setMessage(`发送失败：${describeError(error)}`);
      }
    } finally {
      inFlight.current = false;
    }
  };

  const tap = (index: number) => {
    const label = options[index]?.label ?? "";
    void run(label, () => terminalApi.tapPrompt(tabId, prompt, index));
  };
  const pressEnter = () => {
    if (!enterKey) return;
    void run("回车", () => terminalApi.write(tabId, new TextEncoder().encode(enterKey)));
  };

  const note = blocked ?? unavailable ?? null;
  return (
    <div
      className={`nx-prompt-choices flex min-w-0 ${compact ? "flex-wrap items-center gap-x-2 gap-y-1" : "flex-col gap-1.5"}`}
      data-prompt-choices=""
      data-prompt-kind={prompt.kind}
    >
      <p id={`${id}-q`} className="min-w-0 whitespace-pre-wrap break-words text-[12.5px] text-[var(--nx-fg-strong)]" data-testid="prompt-question">
        {question}
      </p>
      {showOptions && (
        <div
          role="group"
          aria-labelledby={`${id}-q`}
          aria-describedby={note ? `${id}-note` : undefined}
          aria-busy={phase === "sending"}
          className="flex flex-wrap items-center gap-1.5"
        >
          {options.map((option, index) => (
            <button
              key={index}
              type="button"
              className={`nx-btn nx-btn-sm min-h-8 ${option.default ? "nx-btn-primary" : "nx-btn-ghost"}`}
              disabled={optionsDisabled}
              data-prompt-option={index}
              aria-current={prompt.method === "arrows" && index === prompt.cursorIndex ? "true" : undefined}
              onClick={() => tap(index)}
            >
              {option.label}
              {option.default && <span className="sr-only">（默认）</span>}
            </button>
          ))}
          {enterKey && (
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-sm min-h-8"
              disabled={enterDisabled}
              data-prompt-enter=""
              aria-label="发送回车"
              title="选项不会自动回车时，用它确认"
              onClick={pressEnter}
            >
              回车
            </button>
          )}
        </div>
      )}
      {note && (
        <p id={`${id}-note`} className="text-[11.5px] text-[var(--nx-fg-soft)]" data-testid="prompt-note">
          {note}
        </p>
      )}
      <p role="status" aria-live="polite" className={message ? "text-[11.5px] text-[var(--nx-fg-soft)]" : "sr-only"} data-testid="prompt-status" data-phase={phase}>
        {message}
      </p>
    </div>
  );
}

// SessionPromptChoices is the choice block of a list entry (status wall card, inbox row), shown while the session
// needs the user and has a detected prompt. A tap needs a tab of this client on the session; a session known only
// from the cross-host listing shows the question with disabled buttons and asks to open it first (OPEN_TO_ANSWER).
export function SessionPromptChoices({
  tabId,
  awareness,
  attachedHere,
}: {
  tabId: string;
  awareness: SessionAwareness | undefined;
  attachedHere: boolean;
}) {
  if (awareness?.state !== "needs_me" || !awareness.prompt) return null;
  return <ChoiceButtons tabId={tabId} prompt={awareness.prompt} unavailable={attachedHere ? undefined : OPEN_TO_ANSWER} />;
}
