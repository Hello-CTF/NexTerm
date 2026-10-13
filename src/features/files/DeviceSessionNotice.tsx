// 设备会话的能力降级提示: 设备终端经 supervisor v2 桥接运行, 协议只有终端
// 会话帧, 没有文件与转发通道, 文件浏览/磁盘挂载/端口转发的面板统一给显式
// 降级说明, 不让后端 unsupported 错误裸漏到查询行。

export function DeviceSessionNotice({ feature }: { feature: string }) {
  return (
    <div className="flex h-full min-h-0 items-center justify-center px-6 py-8">
      <div className="max-w-[420px] text-center">
        <div className="text-[13px] font-semibold text-neutral-200">设备会话不支持{feature}</div>
        <p className="mt-2 text-[12px] leading-relaxed text-neutral-500">
          设备终端经 supervisor v2 桥接运行，协议只携带终端会话，没有文件与转发通道。
        </p>
      </div>
    </div>
  );
}
