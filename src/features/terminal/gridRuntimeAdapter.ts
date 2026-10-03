import { terminalApi } from "../../ipc/commands";
import type { TerminalGridRuntimeAdapter } from "./terminalGrid";

export const productionGridRuntime: TerminalGridRuntimeAdapter = {
  resize: (tabId, grid) => terminalApi.resize(tabId, grid.cols, grid.rows),
  flush: (tabId) => terminalApi.resizeFlush(tabId),
};
