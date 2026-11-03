import { isMobileShell } from "../../app/shell/shellMode";
import { registerTerminalIntegrations } from "../terminal/terminalIntegrations";
import { forwardAndOpen } from "../preview/forwardAndOpen";
import { openUrlInApp } from "./openInApp";

const createLocalForward = ({ sessionId, host, port }: { sessionId: string; host: string; port: number }) =>
  forwardAndOpen({ sessionId, host, port });

// Mobile always uses the system browser (a frame there can reach the native bridge), so only the forward action is registered.
export function registerPreviewIntegrations(): () => void {
  if (isMobileShell()) return registerTerminalIntegrations({ createLocalForward });
  return registerTerminalIntegrations({ openInApp: openUrlInApp, createLocalForward });
}
