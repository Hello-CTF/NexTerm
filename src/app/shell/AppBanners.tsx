import { useEffect, useState } from "react";
import { useUi, type Workspace } from "../store";
import { layoutRestoreStatus, reviewPendingLayoutRestore, subscribeLayoutRestore } from "../layout";
import { TakeoverBanner } from "../TakeoverBanner";
import { UpdateBanner } from "../UpdateBanner";
import { DekUnlockBanner } from "../DekUnlockBanner";
import { WorkspaceRestoreBanner } from "../../features/terminal/WorkspaceRestoreBanner";

export interface AppBannersProps {
  ws: Workspace | null | undefined;
  onOpenSettings: () => void;
  onPickOtherAsset: () => void;
}

// AppBanners stacks the account, update, restore and deleted-target notices under the workspace strip.
export function AppBanners({ ws, onOpenSettings, onPickOtherAsset }: AppBannersProps) {
  const closeWorkspace = useUi((s) => s.closeWorkspace);
  const [layoutRestore, setLayoutRestore] = useState(layoutRestoreStatus);
  useEffect(() => subscribeLayoutRestore(() => setLayoutRestore(layoutRestoreStatus())), []);
  return (
    <>
      <TakeoverBanner />
      <UpdateBanner onOpenSettings={onOpenSettings} />
      <DekUnlockBanner />
      {layoutRestore.pending && !layoutRestore.preview && (
        <div role="status" className="flex shrink-0 flex-wrap items-center gap-2 border-b border-blue-500/30 bg-blue-950/30 px-4 py-2 text-xs text-blue-100">
          <span>账号中有 {layoutRestore.count} 个工作区待确认恢复；本设备暂不覆盖服务端布局。</span>
          <button type="button" className="nx-btn-ghost" onClick={() => void reviewPendingLayoutRestore()}>
            查看并恢复
          </button>
        </div>
      )}
      <WorkspaceRestoreBanner />
      {ws?.deletedTarget && (
        <div role="alert" className="flex shrink-0 flex-wrap items-center gap-2 border-b border-amber-500/30 bg-amber-950/30 px-4 py-2 text-xs text-amber-300">
          <span>「{ws.title}」的资产已删除。现有标签可继续查看，但此工作区无法重新连接。</span>
          <button type="button" className="nx-btn-ghost" onClick={onPickOtherAsset}>
            选择其他资产
          </button>
          <button type="button" className="nx-btn-ghost" onClick={() => void closeWorkspace(ws.id)}>
            关闭工作区
          </button>
        </div>
      )}
    </>
  );
}
