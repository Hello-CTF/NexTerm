import { useCallback, useEffect, useState } from "react";
import { syncApi } from "../../ipc/commands";
import type { SyncForeignCronJob } from "../../ipc/types";
import { describeError } from "../../ui/errorText";

// ForeignCronSection lists synced scheduled jobs bound to other devices. They never run here;
// removing one deletes it on every device, which is how a job of a retired device is cleaned up.
export function ForeignCronSection() {
  const [jobs, setJobs] = useState<SyncForeignCronJob[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setJobs(await syncApi.cronForeignList());
      setError(null);
    } catch (e) {
      setError(describeError(e));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const remove = async (job: SyncForeignCronJob) => {
    const { ask } = await import("../../ui/dialogs");
    const label = job.name || job.prompt;
    if (!(await ask(`删除定时任务「${label}」？它会从所有设备上删除，包括运行它的设备 ${job.deviceName || job.deviceId}。`, {
      title: "删除其他设备的定时任务", kind: "warning",
    }))) return;
    setBusy(job.id);
    try {
      await syncApi.cronForeignRemove(job.id);
      await load();
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(null);
    }
  };

  if (error) return <p className="nx-hint mb-3 text-[12px]">其他设备的定时任务读取失败：{error}</p>;
  if (!jobs || jobs.length === 0) return null;
  return (
    <div className="mb-3">
      <div className="text-[12px] text-neutral-400">其他设备的定时任务（只在所属设备上运行）</div>
      <ul className="mt-1 flex flex-col gap-1">
        {jobs.map((job) => (
          <li key={job.id} className="flex items-center gap-2 text-[12px] text-neutral-300">
            <span className="min-w-0 flex-1 truncate" title={job.prompt}>
              {job.name || job.prompt}
              <span className="nx-hint ml-2">
                {job.schedule} · {job.enabled ? "已启用" : "已停用"} · 运行于 {job.deviceName || job.deviceId}
              </span>
            </span>
            <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" disabled={busy !== null} onClick={() => void remove(job)}>
              删除
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
