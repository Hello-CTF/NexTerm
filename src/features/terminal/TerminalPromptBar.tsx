import { useState } from "react";
import { useTabAwareness } from "../../app/sessionAwarenessStore";
import { IconClose } from "../../ui/icons";
import { ChoiceButtons } from "./ChoiceButtons";
import { promptIdentity } from "./promptChoices";

// TerminalPromptBar is the terminal pane's compact card for a prompt the helper detected while the session needs
// the user. It overlays the top of the screen: the prompt itself sits at the bottom, and taking layout height would
// resize the PTY and redraw the prompt being answered. It stays while the state was seen (the user is looking at it)
// and goes when the prompt does. Closing it hides only this prompt instance.
export function TerminalPromptBar({ tabId }: { tabId: string | null }) {
  const awareness = useTabAwareness(tabId ?? undefined);
  const [dismissed, setDismissed] = useState<string | null>(null);
  const prompt = awareness?.state === "needs_me" ? awareness.prompt : undefined;
  if (!tabId || !prompt) return null;
  const identity = promptIdentity(prompt);
  if (dismissed === identity) return null;
  return (
    <div
      role="region"
      aria-label="终端提示"
      className="nx-terminal-prompt-bar pointer-events-auto flex w-full max-w-[640px] items-start gap-2 rounded-md border border-neutral-700/70 bg-neutral-900/95 px-2.5 py-1.5 shadow-lg"
      data-terminal-prompt-bar={tabId}
    >
      <div className="min-w-0 flex-1">
        <ChoiceButtons tabId={tabId} prompt={prompt} compact />
      </div>
      <button
        type="button"
        className="nx-icon-btn nx-icon-btn-sm shrink-0"
        aria-label="隐藏这个提示"
        title="隐藏"
        onClick={() => setDismissed(identity)}
      >
        <IconClose size={12} />
      </button>
    </div>
  );
}
