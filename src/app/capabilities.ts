/**
 * 后端能力开关（编译期常量，首帧渲染前由 `main.tsx` 拉取）。
 *
 * 为什么放后端：一项功能能不能用，取决于**本机依赖**而非操作系统本身
 * （macOS 的磁盘挂载要 macFUSE 系统扩展 + sshfs，Linux 只 sshfs）。
 * 前端自己 `isMac()` 猜的话，等适配完成还得改两处、且一定会有人漏改。
 *
 * 职责边界：这里只负责**把入口置灰 + 把理由显示出来**；
 * 真正的拒绝在内核命令层（`AppError::Unsupported`），UI 只是提示。
 *
 * 浏览器演示模式（无 Tauri IPC）拉不到值 → 保持「可用」，
 * 让 demo 里的假数据照常渲染。
 */
let mountUnavailableReason_: string | null = null;

/** 磁盘挂载在本机是否暂不可用；返回原因字符串表示不可用，`null` 表示可用。 */
export function mountUnavailableReason(): string | null {
  return mountUnavailableReason_;
}

/** 由 `main.tsx` 在渲染前调用。 */
export function setMountUnavailableReason(reason: string | null | undefined): void {
  mountUnavailableReason_ = reason ?? null;
}
