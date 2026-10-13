// Capability degradation notice for device sessions, now mount-only: file browsing and port forwarding ride
// purpose-typed bridges (supervisor exec/file protocols), so only the disk mount panel shows this explicit notice
// instead of letting backend unsupported errors leak raw into query rows.
// The root always fills its mount container (a definite-height tab panel) and clips,
// never sizing itself from content, so showing it can never force the container larger.

export function DeviceSessionNotice({ feature }: { feature: string }) {
  return (
    <div className="flex h-full min-h-0 w-full items-center justify-center overflow-hidden px-6 py-8">
      <div className="max-w-[420px] text-center">
        <div className="text-[13px] font-semibold text-neutral-200">设备会话不支持{feature}</div>
        <p className="mt-2 text-[12px] leading-relaxed text-neutral-500">
          磁盘挂载由本机 sshfs / net use 直连远端实现，设备代理没有对应的数据通道，设备会话暂无法使用。
        </p>
      </div>
    </div>
  );
}
