import { click, flush, setInputValue } from "./reactTestUtils";

type PasswordField = "当前密码" | "新密码" | "确认新密码";

export function passwordField(root: ParentNode, label: PasswordField): HTMLInputElement | null {
  return root.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`);
}

export function buttonWithText(root: ParentNode, text: string): HTMLButtonElement | undefined {
  return [...root.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent === text);
}

// submitPasswordForm fills PasswordChangeForm and presses its submit button.
// The confirmation defaults to the new password.
export async function submitPasswordForm(
  root: ParentNode,
  values: { current?: string; next?: string; confirm?: string },
  submitLabel: string,
): Promise<void> {
  if (values.current !== undefined) setInputValue(passwordField(root, "当前密码")!, values.current);
  if (values.next !== undefined) setInputValue(passwordField(root, "新密码")!, values.next);
  if (values.next !== undefined || values.confirm !== undefined) {
    setInputValue(passwordField(root, "确认新密码")!, values.confirm ?? values.next ?? "");
  }
  await flush();
  const submit = buttonWithText(root, submitLabel);
  if (!submit) throw new Error(`submit button not found: ${submitLabel}`);
  click(submit);
  await flush();
}
