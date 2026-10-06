import type { ImportReport, SkippedNewerEntry, SyncReport } from "../../ipc/types";

const KIND_LABELS: Record<string, string> = {
  asset: "资产",
  credential: "凭据",
  snippet: "片段",
};

// SyncReportView 展示 v2 对象协议的一轮同步结果(拉取应用 + 推送)。
export function SyncReportView({ data }: { data: SyncReport }) {
  const failed = data.decryptFailed > 0 || data.conflicts > 0;
  return (
    <div className={`mt-3 nx-alert ${failed ? "nx-alert-danger" : ""}`}>
      <div className="mb-1 font-semibold">同步结果</div>
      <div className="font-mono text-[11px]">
        拉取 {data.pulled} · 应用 {data.applied} · 推送 {data.pushed} · 跳过 {data.pullSkipped} ·
        解密失败 {data.decryptFailed} · 冲突 {data.conflicts} · 游标 seq {data.seq}
      </div>
      {data.warnings && data.warnings.length > 0 && (
        <ul className="mt-2 list-disc pl-4 text-[11.5px] text-amber-200">
          {data.warnings.map((w, i) => (
            <li key={i}>{w}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

function revisionLabels(dir?: "push" | "pull"): { local: string; remote: string } {
  if (dir === "push") return { local: "对端", remote: "本机" };
  if (dir === "pull") return { local: "本机", remote: "对端" };
  return { local: "本机", remote: "包内" };
}

function SkippedNewerLine({ entry, labels }: { entry: SkippedNewerEntry; labels: { local: string; remote: string } }) {
  return (
    <li>
      {entry.name || entry.id}
      <span className="nx-hint">（{KIND_LABELS[entry.kind] ?? entry.kind}）</span>
      {entry.equalRevision
        ? ` — ${labels.local}与${labels.remote}修订号相同（${entry.localRevision}），按 Origin 字典序裁决`
        : ` — ${labels.local}修订 ${entry.localRevision} 较${labels.remote}修订 ${entry.remoteRevision} 新，未覆盖`}
    </li>
  );
}

export function ImportReportView({
  title,
  dir,
  data,
}: {
  title: string;
  dir?: "push" | "pull";
  data: ImportReport;
}) {
  const touched =
    data.assetsCreated +
    data.assetsUpdated +
    data.groupsCreated +
    data.groupsUpdated +
    data.credsCreated +
    data.credsUpdated +
    data.snippetsCreated +
    data.snippetsUpdated;
  const details = data.skippedNewerDetails ?? [];
  const labels = revisionLabels(dir);
  return (
    <div className={`mt-3 nx-alert ${touched > 0 ? "" : "nx-alert-danger"}`}>
      <div className="mb-1 font-semibold">{title}</div>
      <div className="font-mono text-[11px]">
        资产 新建 {data.assetsCreated} / 更新 {data.assetsUpdated}； 凭据 新建 {data.credsCreated} / 更新{" "}
        {data.credsUpdated}
        {data.credsDeleted > 0 ? ` / 删除 ${data.credsDeleted}` : ""}； 分组 新建 {data.groupsCreated} / 更新{" "}
        {data.groupsUpdated}
        {data.snippetsCreated + data.snippetsUpdated > 0
          ? `； 片段 新建 ${data.snippetsCreated} / 更新 ${data.snippetsUpdated}`
          : ""}
        ； 跳过（{dir === "push" ? "对端较新" : "本机较新"}） {data.skippedNewer}； 被拒 {data.refused}
      </div>
      {details.length > 0 && (
        <ul className="mt-2 list-disc pl-4 text-[11.5px] text-neutral-300">
          {details.map((entry, i) => (
            <SkippedNewerLine key={`${entry.kind}:${entry.id}:${i}`} entry={entry} labels={labels} />
          ))}
        </ul>
      )}
      {data.skippedNewer > 0 && (
        <p className="nx-hint mt-2 text-[11px]">
          冲突按修订号（最后修改时间）裁决，协议假设各设备时钟已经 NTP 同步，不检测也不校正时钟偏移；
          时钟不准时「较新」判定可能不符合预期。要覆盖较新的一份，请用强制同步。
        </p>
      )}
      {data.warnings.length > 0 && (
        <ul className="mt-2 list-disc pl-4 text-[11.5px] text-amber-200">
          {data.warnings.map((w, i) => (
            <li key={i}>{w}</li>
          ))}
        </ul>
      )}
    </div>
  );
}
