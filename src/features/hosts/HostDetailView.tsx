// Host detail page: one host's sessions, containers, databases and metrics on one page, for assets and fleet devices.
// It reads existing APIs only. Opening the page never connects; "打开工作区" opens or switches to the host's workspace
// without creating a terminal, like selecting the host in the asset tree.

import { useEffect, useMemo, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { connectAsset, connectDevice, DB_KIND_LABEL, dbKindOf, useUi } from "../../app/store";
import { copyHostDeepLink } from "../../app/deepLink";
import { assetApi, type Asset } from "../../ipc/commands";
import { isMobileShell } from "../../app/shell";
import { WEB } from "../../ipc/env";
import type { FleetDevice } from "../../ipc/fleetApi";
import { describeError } from "../../ui/errorText";
import { IconActivity, IconGlobe, IconInfo, IconMonitor, IconServer, IconTerminal } from "../../ui/icons";
import { useAssetProductionClassification } from "../production/productionSettings";
import { fleetClient } from "../fleet/clients";
import { deviceOpenBlockReason } from "../fleet/DeviceTreeSection";
import { formatTime, isOnline, relativeTime } from "../fleet/format";
import { MetricsPanel } from "../fleet/MetricsPanel";
import { useFleetAccount } from "../fleet/useFleetAccount";
import { showSettingsSection } from "../settings/settingsNavigation";
import {
  databaseRelation,
  DATABASE_RELATION_LABEL,
  deviceIdOfHost,
  hasJump,
  hostAddress,
  hostDetailTitle,
  hostWorkspaceOf,
  isHostAssetKind,
  openHostDetail,
  relatedDatabases,
  setHostDetailTitle,
  tunnelHostOf,
} from "./hostDetail";
import { ContainersSection, DatabasesSection, HostSection, SectionHint, SessionsSection, SshHealthSection } from "./HostSections";

const ASSET_KIND_LABEL: Record<string, string> = {
  ssh: "SSH",
  winrm: "WinRM",
  local: "本机",
  device: "受管设备",
};

const SECTION_GRID = "grid grid-cols-[repeat(auto-fill,minmax(min(100%,22rem),1fr))] items-start gap-3";

function Page({ children }: { children: ReactNode }) {
  return (
    <div className="nx-pane" data-testid="host-detail">
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-[1100px] flex-col gap-3 px-3 py-3 sm:px-4">{children}</div>
      </div>
    </div>
  );
}

// Notice is the full-page state for a host that cannot be shown: loading, missing, no permission or an error.
function Notice({ title, hint, action }: { title: string; hint?: string; action?: ReactNode }) {
  return (
    <Page>
      <div className="flex flex-col items-center gap-2 px-4 py-10 text-center" role="status">
        <div className="nx-empty-icon">
          <IconServer size={18} />
        </div>
        <div className="text-[13.5px] font-semibold text-neutral-100">{title}</div>
        {hint && <div className="max-w-[420px] text-[12px] leading-relaxed text-neutral-500">{hint}</div>}
        {action}
      </div>
    </Page>
  );
}

function Header({
  icon,
  name,
  badges,
  subtitle,
  actions,
}: {
  icon: ReactNode;
  name: string;
  badges?: ReactNode;
  subtitle?: ReactNode;
  actions: ReactNode;
}) {
  return (
    <header className="flex min-w-0 flex-col gap-2">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <span className="shrink-0 text-neutral-400">{icon}</span>
        <h2 className="m-0 min-w-0 break-all text-[15px] font-semibold text-neutral-100" data-testid="host-detail-name">
          {name}
        </h2>
        {badges}
      </div>
      {subtitle && <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-[12px] text-neutral-400">{subtitle}</div>}
      <div className="flex flex-wrap items-center gap-1.5">{actions}</div>
    </header>
  );
}

function CopyLinkButton({ assetId }: { assetId: string }) {
  // nexterm:// links open the desktop app; the browser has no handler for them and mobile registers no URL scheme.
  if (WEB || isMobileShell()) return null;
  return (
    <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => copyHostDeepLink(assetId)}>
      <IconGlobe size={11} />
      复制链接
    </button>
  );
}

export function HostDetailView({ assetId, visible }: { assetId: string | undefined; visible: boolean }) {
  if (!assetId) return <Notice title="没有选择主机" hint="在资产树中右键主机，选择「主机详情」。" />;
  const deviceId = deviceIdOfHost(assetId);
  return deviceId ? (
    <DeviceDetail key={assetId} assetId={assetId} deviceId={deviceId} visible={visible} />
  ) : (
    <AssetDetail key={assetId} assetId={assetId} visible={visible} />
  );
}

function AssetDetail({ assetId, visible }: { assetId: string; visible: boolean }) {
  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list() });
  const assets = assetsQuery.data;
  const asset = assets?.find((item) => item.id === assetId && !item.deletedAt);
  if (!assets) {
    if (assetsQuery.isError) {
      return (
        <Notice
          title="主机信息加载失败"
          hint={describeError(assetsQuery.error)}
          action={
            <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => void assetsQuery.refetch()}>
              重试
            </button>
          }
        />
      );
    }
    return <Notice title="正在加载主机信息…" />;
  }
  if (!asset) return <Notice title="资产不存在或已删除" hint="它可能已被删除、未同步到这台设备，或你没有访问权限。" />;
  if (dbKindOf(asset.kind)) return <DatabaseAssetDetail asset={asset} assets={assets} visible={visible} />;
  return <HostAssetDetail asset={asset} assets={assets} visible={visible} />;
}

function ProductionBadge({ assetId }: { assetId: string }) {
  const classification = useAssetProductionClassification(assetId);
  if (!classification?.production) return null;
  return (
    <span className="nx-badge nx-badge-amber shrink-0" title={`环境：${classification.environment}`}>
      生产
    </span>
  );
}

function HostAssetDetail({ asset, assets, visible }: { asset: Asset; assets: readonly Asset[]; visible: boolean }) {
  const workspace = useUi((state) => hostWorkspaceOf(state.workspaces, asset.id));
  const databases = useMemo(() => relatedDatabases(asset, assets), [asset, assets]);
  const relations = useMemo(() => {
    const labels: Record<string, string> = {};
    for (const database of databases) {
      const relation = databaseRelation(asset, database);
      if (relation) labels[database.id] = DATABASE_RELATION_LABEL[relation];
    }
    return labels;
  }, [asset, databases]);
  useEffect(() => setHostDetailTitle(asset.id, hostDetailTitle(asset.name, false)), [asset.id, asset.name]);
  const address = hostAddress(asset);
  const kindLabel = ASSET_KIND_LABEL[asset.kind] ?? asset.kind;
  const hostKind = isHostAssetKind(asset.kind);
  const dockerCapable = asset.kind === "ssh" || asset.kind === "local";
  return (
    <Page>
      <Header
        icon={<IconServer size={16} />}
        name={asset.name}
        badges={
          <>
            <span className="nx-badge shrink-0">{kindLabel}</span>
            <ProductionBadge assetId={asset.id} />
          </>
        }
        subtitle={
          <>
            {address && <span className="min-w-0 break-all font-mono">{address}</span>}
            {asset.note && <span className="min-w-0 break-words">{asset.note}</span>}
          </>
        }
        actions={
          <>
            {hostKind && (
              <button type="button" className="nx-btn nx-btn-primary nx-btn-sm" onClick={() => void connectAsset(asset)}>
                <IconTerminal size={11} />
                {workspace ? "切换到工作区" : "打开工作区"}
              </button>
            )}
            <CopyLinkButton assetId={asset.id} />
          </>
        }
      />
      <div className={SECTION_GRID}>
        <SessionsSection workspace={workspace} />
        {dockerCapable && <ContainersSection workspace={workspace} visible={visible} />}
        <DatabasesSection
          databases={databases}
          relations={relations}
          visible={visible}
          emptyHint="没有经由这台主机或指向它的地址的数据库资产。"
        />
        {asset.kind === "ssh" ? (
          <SshHealthSection assetId={asset.id} workspace={workspace} viaJump={hasJump(asset)} />
        ) : (
          <HostSection title="指标" icon={<IconActivity size={14} />} testId="host-metrics">
            <SectionHint>{asset.kind === "local" ? "本机不采集指标。" : "这类主机不采集指标。"}</SectionHint>
          </HostSection>
        )}
      </div>
    </Page>
  );
}

function DatabaseAssetDetail({ asset, assets, visible }: { asset: Asset; assets: readonly Asset[]; visible: boolean }) {
  const tunnel = tunnelHostOf(asset, assets);
  const dbKind = dbKindOf(asset.kind);
  const viaAssetId = typeof asset.options?.viaAssetId === "string" ? asset.options.viaAssetId : "";
  useEffect(() => setHostDetailTitle(asset.id, hostDetailTitle(asset.name, true)), [asset.id, asset.name]);
  return (
    <Page>
      <Header
        icon={<IconServer size={16} />}
        name={asset.name}
        badges={
          <>
            {dbKind && <span className="nx-badge shrink-0">{DB_KIND_LABEL[dbKind]}</span>}
            <ProductionBadge assetId={asset.id} />
          </>
        }
        subtitle={
          tunnel ? (
            <span>
              经由{" "}
              <button type="button" className="nx-link" onClick={() => openHostDetail(tunnel.id)}>
                {tunnel.name}
              </button>{" "}
              连接
            </span>
          ) : viaAssetId ? (
            <span className="text-amber-300">经由的主机已删除</span>
          ) : undefined
        }
        actions={<CopyLinkButton assetId={asset.id} />}
      />
      <div className={SECTION_GRID}>
        <DatabasesSection databases={[asset]} visible={visible} emptyHint="" />
      </div>
    </Page>
  );
}

const PLATFORM_LABELS: Record<string, string> = { darwin: "macOS", linux: "Linux", windows: "Windows" };

function openAccountSyncSettings(): void {
  useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
  showSettingsSection("settings-account");
}

function openDevicesTab(): void {
  useUi.getState().addTab({ id: "devices", kind: "devices", title: "设备管理", closable: true });
}

function DeviceDetail({ assetId, deviceId, visible }: { assetId: string; deviceId: string; visible: boolean }) {
  const account = useFleetAccount();
  const userId = account.userId;
  // Same query as the asset tree's device rows, so both share one cache and refresh cadence.
  const devicesQuery = useQuery({
    queryKey: ["fleet-devices", userId],
    queryFn: () => fleetClient().devices(),
    enabled: Boolean(userId),
    refetchOnWindowFocus: false,
    refetchInterval: WEB ? 60_000 : 30_000,
  });
  if (account.loading) return <Notice title="正在加载设备信息…" />;
  if (!userId) {
    return (
      <Notice
        title="登录服务端账号后可查看设备"
        hint="受管设备属于服务端账号。在「设置 → 账号同步」登录后即可查看这台设备。"
        action={
          <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" onClick={openAccountSyncSettings}>
            去设置 → 账号同步登录
          </button>
        }
      />
    );
  }
  if (devicesQuery.isError && !devicesQuery.data) {
    return (
      <Notice
        title="设备信息加载失败"
        hint={describeError(devicesQuery.error)}
        action={
          <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => void devicesQuery.refetch()}>
            重试
          </button>
        }
      />
    );
  }
  if (!devicesQuery.data) return <Notice title="正在加载设备信息…" />;
  const device = devicesQuery.data.devices.find((item) => item.id === deviceId);
  if (!device) {
    return <Notice title="无法查看这台设备" hint="设备不存在，或它不属于当前账号。只有设备的所有者和超级管理员可以查看。" />;
  }
  return <DeviceHostDetail assetId={assetId} device={device} isAdmin={account.isAdmin} visible={visible} />;
}

function DeviceHostDetail({
  assetId,
  device,
  isAdmin,
  visible,
}: {
  assetId: string;
  device: FleetDevice;
  isAdmin: boolean;
  visible: boolean;
}) {
  const workspace = useUi((state) => hostWorkspaceOf(state.workspaces, assetId));
  useEffect(() => setHostDetailTitle(assetId, hostDetailTitle(device.name, false)), [assetId, device.name]);
  const now = Date.now();
  const agent = device.agent;
  const revoked = device.revoked_at !== 0;
  const lastSeenAt = agent?.last_seen_at || device.last_seen_at;
  const online = !revoked && Boolean(agent) && isOnline(lastSeenAt, now);
  const blockReason = deviceOpenBlockReason(device, online);
  const status = revoked ? "已吊销" : !agent ? "未接入 Agent" : online ? "在线" : "离线";
  const statusTone = revoked ? "text-red-300" : online ? "text-green-300" : "text-neutral-400";
  return (
    <Page>
      <Header
        icon={<IconMonitor size={16} />}
        name={device.name}
        badges={
          <>
            <span className="nx-badge shrink-0">{ASSET_KIND_LABEL.device}</span>
            <span className={`inline-flex shrink-0 items-center gap-1.5 text-[12px] ${statusTone}`} data-testid="host-device-status">
              <span className="nx-dot" aria-hidden="true" />
              {status}
            </span>
            {isAdmin && device.owner && <span className="nx-badge nx-badge-blue shrink-0">{device.owner.username}</span>}
          </>
        }
        subtitle={
          <>
            {agent?.platform && <span>{PLATFORM_LABELS[agent.platform.toLowerCase()] ?? agent.platform}</span>}
            {agent?.app_version && <span>· v{agent.app_version.replace(/^v/i, "")}</span>}
            {revoked ? (
              <span className="text-red-300">· 撤销于 {formatTime(device.revoked_at)}</span>
            ) : lastSeenAt ? (
              <span title={formatTime(lastSeenAt)}>· {relativeTime(lastSeenAt, now)}上报</span>
            ) : (
              <span>· 尚未上报</span>
            )}
            {agent && (
              <span className={agent.terminal_enabled ? "" : "text-amber-300"}>
                · 远程终端{agent.terminal_enabled ? "已开启" : "已关闭"}
              </span>
            )}
          </>
        }
        actions={
          <>
            <button
              type="button"
              className="nx-btn nx-btn-primary nx-btn-sm"
              disabled={blockReason !== null}
              title={blockReason ?? undefined}
              onClick={() => void connectDevice(device)}
            >
              <IconTerminal size={11} />
              {workspace ? "切换到工作区" : "打开工作区"}
            </button>
            <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={openDevicesTab}>
              <IconInfo size={11} />
              设备管理
            </button>
            <CopyLinkButton assetId={assetId} />
          </>
        }
      />
      {blockReason && !revoked && <p className="nx-hint m-0 text-[12px]" role="note">{blockReason}</p>}
      <div className={SECTION_GRID}>
        <SessionsSection workspace={workspace} />
        <ContainersSection workspace={workspace} visible={visible} />
        <HostSection title="指标" icon={<IconActivity size={14} />} testId="host-metrics">
          {revoked ? (
            <SectionHint>设备已吊销，不再上报指标。</SectionHint>
          ) : visible ? (
            <MetricsPanel deviceId={device.id} online={online} />
          ) : null}
        </HostSection>
      </div>
    </Page>
  );
}
