import { click } from "./reactTestUtils";

// Probes for the shared Select (ui/Select), whose trigger shows the chosen label instead of exposing select.value.

export function selectTrigger(scope: ParentNode, ariaLabel: string): HTMLButtonElement {
  const trigger = scope.querySelector<HTMLButtonElement>(`button[aria-label="${ariaLabel}"][aria-haspopup="listbox"]`);
  if (!trigger) throw new Error(`Select trigger not found: ${ariaLabel}`);
  return trigger;
}

// selectedLabel is the text the trigger shows for the current value.
export function selectedLabel(scope: ParentNode, ariaLabel: string): string {
  return selectTrigger(scope, ariaLabel).textContent?.trim() ?? "";
}

// optionLabels opens the list, reads every option, and closes it again.
export function optionLabels(scope: ParentNode, ariaLabel: string): string[] {
  const trigger = selectTrigger(scope, ariaLabel);
  click(trigger);
  const labels = [...scope.querySelectorAll<HTMLElement>('[role="listbox"] [role="option"]')].map((item) => item.textContent?.trim() ?? "");
  click(trigger);
  return labels;
}

// selectedOptions are the labels the list marks as chosen.
export function chosenOptionLabels(scope: ParentNode, ariaLabel: string): string[] {
  const trigger = selectTrigger(scope, ariaLabel);
  click(trigger);
  const labels = [...scope.querySelectorAll<HTMLElement>('[role="listbox"] [role="option"][aria-selected="true"]')].map((item) => item.textContent?.trim() ?? "");
  click(trigger);
  return labels;
}
