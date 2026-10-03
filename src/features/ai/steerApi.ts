// `ai_steer` 的前端出口。
//
// 唯一 IPC 出海口是 `src/ipc/commands.ts` 的 `call()`（desktop/web/demo 三分支
// 都在那里），这里绝不复制那套 plumbing。`aiApi.steer` 的 typed wrapper 落在
// commands.ts —— 该文件正被 M36 占用，合并后 wrapper 一到，这里优先转发它；
// 在此之前调用得到的是明确的「不支持」错误，而不是一条假的通路。
import { aiApi } from "../../ipc/commands";

type SteerCapableAiApi = {
  steer?: (jobId: string, message: string) => Promise<void>;
};

/** 把一条补充指令交给正在运行的 job：内核在下一个模型调用边界注入。 */
export function steerJob(jobId: string, message: string): Promise<void> {
  const steer = (aiApi as SteerCapableAiApi).steer;
  if (!steer) {
    return Promise.reject(new Error("当前版本不支持运行中补充指令"));
  }
  return steer(jobId, message);
}
