import type { Terminal } from "@xterm/xterm";

/**
 * Takes over committed non-composition text (IME punctuation, emoji pickers, dictation) before xterm sees it.
 *
 * xterm only accepts such an `input` event while no key is down, and otherwise expects a keyCode 229 keydown
 * that precedes the insertion so it can diff the helper textarea afterwards. WKWebView delivers the insertion
 * first and the keydown after it, so with Shift still held (as when typing a full-width question mark) xterm
 * drops the character: the input event is gated by the Shift keydown and the later diff sees no change.
 * The text is sent here instead and the insertion is taken back out of the textarea, so xterm's diff sees the
 * same value whichever order the keydown arrives in and sends nothing twice. Composition input stays with xterm.
 */
export function attachImeTextInput(term: Pick<Terminal, "input" | "textarea">, host: HTMLElement): () => void {
  const onInput = (event: Event) => {
    const textarea = term.textarea;
    if (!textarea || event.target !== textarea) return;
    const input = event as InputEvent;
    if (input.inputType !== "insertText" || !input.data || input.isComposing) return;
    const value = textarea.value;
    const end = textarea.selectionEnd ?? value.length;
    const start = end - input.data.length;
    // Only an insertion that can be located exactly is taken over; anything else keeps xterm's handling.
    if (start < 0 || value.slice(start, end) !== input.data) return;
    event.stopPropagation();
    textarea.value = value.slice(0, start) + value.slice(end);
    textarea.setSelectionRange(start, start);
    term.input(input.data, true);
  };
  host.addEventListener("input", onInput, true);
  return () => host.removeEventListener("input", onInput, true);
}
