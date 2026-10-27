import type { StateRuleIntent } from "../../ipc/stateRuleApi";

// A request to mark the state a terminal tab shows. The dialog host is mounted once with the app dialogs; menus
// anywhere ask it to open through requestStateMark.
export interface StateMarkRequest {
  // The workspace tab id; the dialog closes when the tab goes away.
  appTabId: string;
  // The backend terminal tab id the screen is read from.
  tabId: string;
  assetId?: string;
  title: string;
  intent: StateRuleIntent;
  // The state the tab shows now.
  current: string;
}

type Listener = (request: StateMarkRequest) => void;

const listeners = new Set<Listener>();

export function requestStateMark(request: StateMarkRequest): void {
  for (const listener of listeners) listener(request);
}

export function onStateMarkRequest(listener: Listener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

// The marking choices a tab offers for the state it shows. Unknown and exited states are not from the screen, so
// there is nothing to correct; a tab showing needs_me is already what "this is it" asks for.
export function stateMarkIntents(current: string | undefined): StateRuleIntent[] {
  if (!current || current === "unknown" || current === "exited") return [];
  if (current === "needs_me") return ["not_this"];
  return ["not_this", "this_is_it"];
}
