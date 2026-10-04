
import { DEMO, TRANSPORT, WEB } from "../ipc/env";

if (DEMO) {
  console.warn(
    "[NexTerm] 演示模式：所有数据都是内存里的假数据，不会连接任何真实服务器。\n" +
      "URL 加 ?demo=0 可关闭（需要真实 Tauri 环境或 nexterm-server）。",
  );
}

export { DEMO, WEB, TRANSPORT };
export { emit, subscribe, pushText, pushEvent, later } from "./bus";
