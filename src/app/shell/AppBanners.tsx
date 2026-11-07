import { useEffect, useState } from "react";
import { useUi, type Workspace } from "../store";
import { layoutRestoreStatus, reviewPendingLayoutRestore, subscribeLayoutRestore } from "../layout";
import { useNewerLayoutNotice } from "../newerLayoutNotice";
import { TakeoverBanner } from "../TakeoverBanner";
import { UpdateBanner } from "../UpdateBanner";
import { DekUnlockBanner } from "../DekUnlockBanner";
import { WorkspaceRestoreBanner } from "../../features/terminal/WorkspaceRestoreBanner";
import { t } from "../../i18n";

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
  const newerLayout = useNewerLayoutNotice();
  return (
    <>
      <TakeoverBanner />
      <UpdateBanner onOpenSettings={onOpenSettings} />
      <DekUnlockBanner />
      {layoutRestore.pending && !layoutRestore.preview && (
        <div role="status" className="flex shrink-0 flex-wrap items-center gap-2 border-b border-blue-500/30 bg-blue-950/30 px-4 py-2 text-xs text-blue-100">
          <span>{t("账号中有")} {layoutRestore.count} {t("个工作区待确认恢复；本机暂不覆盖服务端布局。")}</span>
          <button type="button" className="nx-btn-ghost" onClick={() => void reviewPendingLayoutRestore()}>
            {t("查看并恢复")}
          </button>
        </div>
      )}
      {newerLayout !== null && (
        <div role="alert" className="flex shrink-0 flex-wrap items-center gap-2 border-b border-amber-500/30 bg-amber-950/30 px-4 py-2 text-xs text-amber-300">
          <span>
            {t("账号布局由更新版本保存（格式 v")}{newerLayout}{t("），布局同步已暂停；请升级 NexTerm。")}
          </span>
        </div>
      )}
      <WorkspaceRestoreBanner />
      {ws?.deletedTarget && (
        <div role="alert" className="flex shrink-0 flex-wrap items-center gap-2 border-b border-amber-500/30 bg-amber-950/30 px-4 py-2 text-xs text-amber-300">
          <span>「{ws.title}{t("」的资产已删除。现有标签页可继续查看，但此工作区无法重新连接。")}</span>
          <button type="button" className="nx-btn-ghost" onClick={onPickOtherAsset}>
            {t("选择其他资产")}
          </button>
          <button type="button" className="nx-btn-ghost" onClick={() => void closeWorkspace(ws.id)}>
            {t("关闭工作区")}
          </button>
        </div>
      )}
    </>
  );
}
