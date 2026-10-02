import { terminalApi } from "../../ipc/commands";
import type { TerminalGridRuntimeAdapter } from "./terminalGrid";

// M44 的最终 flush IPC 只需在这里补一项，视图与测量生命周期保持不变。
export const productionGridRuntime: TerminalGridRuntimeAdapter = {
  resize: (tabId, grid) => terminalApi.resize(tabId, grid.cols, grid.rows),
};
