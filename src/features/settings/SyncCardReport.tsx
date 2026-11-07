import type { ImportReport, SkippedNewerEntry } from "../../ipc/types";
import { t } from "../../i18n";

const KIND_LABELS: Record<string, string> = {
  asset: t("资产"),
  credential: t("凭据"),
  snippet: t("片段"),
};

function revisionLabels(dir?: "push" | "pull"): { local: string; remote: string } {
  if (dir === "push") return { local: t("对端"), remote: t("本机") };
  if (dir === "pull") return { local: t("本机"), remote: t("对端") };
  return { local: t("本机"), remote: t("包内") };
}

function SkippedNewerLine({ entry, labels }: { entry: SkippedNewerEntry; labels: { local: string; remote: string } }) {
  return (
    <li>
      {entry.name || entry.id}
      <span className="nx-hint">（{KIND_LABELS[entry.kind] ?? entry.kind}）</span>
      {entry.equalRevision
        ? t(" — {local}与{remote}修订号相同（{localRevision}），按设备标识排序决定", { local: labels.local, remote: labels.remote, localRevision: entry.localRevision })
        : t(" — {local}修订 {localRevision} 较{remote}修订 {remoteRevision} 新，未覆盖", { local: labels.local, localRevision: entry.localRevision, remote: labels.remote, remoteRevision: entry.remoteRevision })}
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
        {t("资产：新建")} {data.assetsCreated} {t("/ 更新")} {data.assetsUpdated}{t("；凭据：新建")} {data.credsCreated} {t("/ 更新")}{" "}
        {data.credsUpdated}
        {data.credsDeleted > 0 ? t(" / 删除 {credsDeleted}", { credsDeleted: data.credsDeleted }) : ""}{t("；分组：新建")} {data.groupsCreated} {t("/ 更新")}{" "}
        {data.groupsUpdated}
        {data.snippetsCreated + data.snippetsUpdated > 0
          ? t("；片段：新建 {snippetsCreated} / 更新 {snippetsUpdated}", { snippetsCreated: data.snippetsCreated, snippetsUpdated: data.snippetsUpdated })
          : ""}
        {t("；跳过（")}{dir === "push" ? t("对端较新") : t("本机较新")}） {data.skippedNewer}{t("；已拒绝")} {data.refused}
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
          {t("冲突按最后修改时间判定，请保持各设备时钟准确，否则对「较新」的判断可能有误。如需覆盖较新的版本，请在导入时勾选「强制覆盖较新的本机条目」。")}
        </p>
      )}
      {data.warnings.length > 0 && (
        <ul className="mt-2 list-disc pl-4 text-[11.5px]">
          {data.warnings.map((w, i) => (
            <li key={i}>{w}</li>
          ))}
        </ul>
      )}
    </div>
  );
}
