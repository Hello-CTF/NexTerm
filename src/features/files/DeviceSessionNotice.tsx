// Capability degradation notice for device sessions: device terminals run bridged via supervisor v2, whose protocol has only terminal
// session frames - no file or forward channels - so the file browsing/disk mount/port forwarding panels uniformly show an explicit
// degradation notice instead of letting backend unsupported errors leak raw into query rows.

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
