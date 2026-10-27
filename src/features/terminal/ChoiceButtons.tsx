import { useEffect, useId, useLayoutEffect, useRef, useState, type MouseEvent, type PointerEvent } from "react";
import { terminalApi } from "../../ipc/commands";
import type { AttentionPrompt } from "../../ipc/generated/types";
import { refreshAwareness } from "../../app/sessionAwareness";
import type { SessionAwareness } from "../../app/sessionAwarenessStore";
import { describeError } from "../../ui/errorText";
import {
  ANSWER_TAKES_SIZE_NOTE,
  CHOICE_ARM_MS,
  OPEN_TO_ANSWER_LABEL,
  OPEN_TO_ANSWER_NOTE,
  promptBlockedHint,
  promptIdentity,
  promptQuestion,
  separateEnterKey,
} from "./promptChoices";

export interface ChoiceButtonsProps {
  // The backend terminal tab whose prompt this is.
  tabId: string;
  prompt: AttentionPrompt;
  // The attention version that carried the prompt; a newer one ends the separate Enter of an answered prompt.
  version: number;
  // Offer the prompt's separate Enter after a tap; only the terminal pane does, where the screen is in view.
  allowEnter?: boolean;
  // An explanation shown under the buttons.
  note?: string;
  // The one-line layout of the terminal pane's bar.
  compact?: boolean;
}

type Phase = "idle" | "sending" | "sent" | "stale" | "failed";

let clock = (): number => performance.now();

// setChoiceClockForTest replaces the clock the arming delay reads; null restores it.
export function setChoiceClockForTest(next: (() => number) | null): void {
  clock = next ?? (() => performance.now());
}

function errorCode(error: unknown): string {
  if (typeof error !== "object" || error === null) return "";
  const code = (error as { code?: unknown }).code;
  return typeof code === "string" ? code : "";
}

function rectKey(element: Element): string {
  const rect = element.getBoundingClientRect();
  return `${rect.left},${rect.top},${rect.width},${rect.height}`;
}

// ChoiceButtons shows a detected prompt's question with its options as buttons. A tap names the prompt instance
// (generation, epoch, fingerprint); the helper refuses it when the screen moved on, and the view then says so and
// reloads the states instead of trying again. Only one tap or Enter is in flight at a time.
export function ChoiceButtons(props: ChoiceButtonsProps) {
  // A new prompt instance starts with fresh buttons: what was sent or refused belongs to the previous one.
  return <PromptChoices key={promptIdentity(props.prompt)} {...props} />;
}

function PromptChoices({ tabId, prompt, version, allowEnter, note: extraNote, compact }: ChoiceButtonsProps) {
  const id = useId();
  const [phase, setPhase] = useState<Phase>("idle");
  const [message, setMessage] = useState("");
  const [armed, setArmed] = useState(false);
  const [enterUsed, setEnterUsed] = useState(false);
  // The attention version the answered tap was sent under.
  const [tapVersion, setTapVersion] = useState<number | null>(null);
  // The flag is set before React renders the disabled buttons, so a second tap in the same frame is dropped too.
  const inFlight = useRef(false);
  const groupRef = useRef<HTMLDivElement>(null);
  // Where the buttons are and since when they have been there; a tap counts only once they held still.
  const stable = useRef<{ rect: string; since: number } | null>(null);
  const armTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  // The press that the next click finishes: where the buttons were and whether they were armed then.
  const press = useRef<{ rect: string; armed: boolean } | null>(null);
  const options = prompt.options ?? [];
  const blocked = promptBlockedHint(prompt);
  const enterKey = allowEnter ? separateEnterKey(prompt) : null;
  const question = promptQuestion(prompt);
  const showOptions = blocked === null;
  const optionsDisabled = phase === "sending" || phase === "sent" || phase === "stale";
  // Enter answers only the prompt this view tapped, once, and only while no newer state of the tab arrived: it is an
  // ordinary write that no prompt epoch guards.
  const enterEnabled = phase === "sent" && !enterUsed && tapVersion === version;

  const restartArming = (rect: string) => {
    stable.current = { rect, since: clock() };
    setArmed(false);
    if (armTimer.current !== undefined) clearTimeout(armTimer.current);
    armTimer.current = setTimeout(() => {
      armTimer.current = undefined;
      setArmed(true);
    }, CHOICE_ARM_MS);
  };
  const isArmed = () => stable.current !== null && clock() - stable.current.since >= CHOICE_ARM_MS;

  // Every render may have moved the buttons (a row inserted above, a longer question); arming starts over then.
  useLayoutEffect(() => {
    const group = groupRef.current;
    if (!group) return;
    const rect = rectKey(group);
    if (stable.current?.rect !== rect) restartArming(rect);
  });
  useEffect(() => () => {
    if (armTimer.current !== undefined) clearTimeout(armTimer.current);
  }, []);

  // A press keeps the focus where it is (the terminal keeps the keyboard and any IME composition) and records whether
  // the buttons were armed and where they were when it began.
  const onPointerDown = (event: PointerEvent) => {
    event.preventDefault();
    const group = groupRef.current;
    if (!group) return;
    const rect = rectKey(group);
    if (stable.current?.rect !== rect) {
      restartArming(rect);
      press.current = { rect, armed: false };
      return;
    }
    press.current = { rect, armed: isArmed() };
  };

  // accepts reports whether a click is a deliberate answer: the buttons were armed when it was pressed and did not
  // move since. A keyboard click (detail 0) has no press; the buttons must be armed and in place.
  const accepts = (event: MouseEvent): boolean => {
    const pressed = press.current;
    press.current = null;
    const group = groupRef.current;
    if (!group || !isArmed()) return false;
    const rect = rectKey(group);
    if (rect !== stable.current?.rect) {
      restartArming(rect);
      return false;
    }
    if (event.detail === 0) return true;
    return pressed !== null && pressed.armed && pressed.rect === rect;
  };

  const run = async (label: string, send: () => Promise<void>, after: () => void) => {
    if (inFlight.current) return;
    inFlight.current = true;
    setPhase("sending");
    setMessage("正在发送…");
    try {
      await send();
      after();
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
      } else if (code === "forbidden") {
        setPhase("stale");
        setMessage("此端只在旁观这个终端，不能回答");
      } else {
        setPhase("failed");
        setMessage(`发送失败：${describeError(error)}`);
      }
    } finally {
      inFlight.current = false;
    }
  };

  const tap = (index: number, event: MouseEvent) => {
    if (!accepts(event)) return;
    const label = options[index]?.label ?? "";
    const tappedAt = version;
    void run(label, () => terminalApi.tapPrompt(tabId, prompt, index), () => setTapVersion(tappedAt));
  };
  const pressEnter = (event: MouseEvent) => {
    if (!enterKey || !enterEnabled || !accepts(event)) return;
    setEnterUsed(true);
    void run("回车", () => terminalApi.write(tabId, new TextEncoder().encode(enterKey)), () => undefined);
  };

  const note = blocked ?? extraNote ?? null;
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
          ref={groupRef}
          role="group"
          aria-labelledby={`${id}-q`}
          aria-describedby={note ? `${id}-note` : undefined}
          aria-busy={phase === "sending"}
          className="flex flex-wrap items-center gap-1.5"
          data-armed={armed ? "" : undefined}
        >
          {options.map((option, index) => (
            <button
              key={index}
              type="button"
              className={`nx-btn nx-btn-sm min-h-8 ${option.default ? "nx-btn-primary" : "nx-btn-ghost"} ${armed ? "" : "opacity-70"}`}
              disabled={optionsDisabled}
              data-prompt-option={index}
              aria-current={prompt.method === "arrows" && index === prompt.cursorIndex ? "true" : undefined}
              onPointerDown={onPointerDown}
              onClick={(event) => tap(index, event)}
            >
              {option.label}
              {option.default && <span className="sr-only">（默认）</span>}
            </button>
          ))}
          {enterKey && (
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-sm min-h-8"
              disabled={!enterEnabled}
              data-prompt-enter=""
              aria-label="发送回车"
              title="选项发送后没有自动确认时，用它按一次回车"
              onPointerDown={onPointerDown}
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
// needs the user and has a detected prompt. With a tab of this client on the session it answers in place and says
// that the answer moves the PTY size here. A session known only from the cross-host listing shows the question and
// one action that opens it, so the user sees the live screen and answers from the terminal pane.
export function SessionPromptChoices({
  tabId,
  awareness,
  attachedHere,
  onOpen,
  opening,
}: {
  tabId: string;
  awareness: SessionAwareness | undefined;
  attachedHere: boolean;
  // Opens or takes over the session; absent when this client cannot reach it.
  onOpen?: () => void;
  opening?: boolean;
}) {
  if (awareness?.state !== "needs_me" || !awareness.prompt) return null;
  const prompt = awareness.prompt;
  if (attachedHere) {
    return <ChoiceButtons tabId={tabId} prompt={prompt} version={awareness.version} note={ANSWER_TAKES_SIZE_NOTE} />;
  }
  return (
    <div className="nx-prompt-choices flex min-w-0 flex-col gap-1.5" data-prompt-choices="" data-prompt-kind={prompt.kind}>
      <p className="min-w-0 whitespace-pre-wrap break-words text-[12.5px] text-[var(--nx-fg-strong)]" data-testid="prompt-question">
        {promptQuestion(prompt)}
      </p>
      {onOpen ? (
        <div className="flex flex-wrap items-center gap-1.5">
          <button
            type="button"
            className="nx-btn nx-btn-primary nx-btn-sm min-h-8"
            disabled={opening}
            data-prompt-open=""
            onClick={onOpen}
          >
            {OPEN_TO_ANSWER_LABEL}
          </button>
          <span className="text-[11.5px] text-[var(--nx-fg-soft)]">{OPEN_TO_ANSWER_NOTE}</span>
        </div>
      ) : (
        <p className="text-[11.5px] text-[var(--nx-fg-soft)]" data-testid="prompt-note">此端无法打开这个终端，请在其他设备上回答</p>
      )}
    </div>
  );
}
