import { isMobileShell } from "../../app/shell/shellMode";
import { registerTerminalIntegrations } from "../terminal/terminalIntegrations";
import { openUrlInApp } from "./openInApp";

// Mobile always uses the system browser (a frame there can reach the native bridge), so nothing is registered.
export function registerPreviewIntegrations(): () => void {
  if (isMobileShell()) return () => undefined;
  return registerTerminalIntegrations({ openInApp: openUrlInApp });
}
