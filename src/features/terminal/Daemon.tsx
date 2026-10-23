import { IconLoader } from "../../ui/icons";

export type DaemonHelperState =
  | { state: "installing"; stage: string }
  | { state: "attention"; message?: string }
  | { state: "ready" };

export interface DaemonProps {
  durable: boolean;
  sessionKind?: string;
  helper?: DaemonHelperState | null;
  onOpenHelper?: () => void;
}

export function Daemon({ durable, sessionKind, helper, onOpenHelper }: DaemonProps) {
  if (sessionKind === "winrm" || sessionKind === "docker") return null;
  let className = "nx-badge";
  let label = "普通终端";
  let title = "普通终端：断开连接后进程即结束，无法转入后台";
  let busy = false;
  if (durable) {
    className = "nx-badge nx-badge-green";
    label = "持久终端";
    title = "持久终端：断开连接或重启应用后进程继续运行，可在「后台会话」接管";
  } else if (helper?.state === "installing") {
    className = "nx-badge nx-badge-blue";
    label = "正在启用持久终端";
    title = `${helper.stage}…当前标签是普通终端，完成后新建的标签将是持久终端`;
    busy = true;
  } else if (helper?.state === "attention") {
    className = "nx-badge nx-badge-amber";
    label = "持久终端需处理";
    title = helper.message ? `持久终端暂时不可用：${helper.message}` : "持久终端暂时不可用，点击查看选项";
  } else if (helper?.state === "ready") {
    className = "nx-badge nx-badge-blue";
    label = "普通终端 · 持久已就绪";
    title = "持久终端已就绪。当前标签仍是普通终端，新建的标签将是持久终端";
  }
  const content = (
    <>
      {busy && <IconLoader size={11} className="animate-spin" />}
      {label}
    </>
  );
  if (!onOpenHelper || sessionKind !== "ssh") {
    return <span className={className} title={title}>{content}</span>;
  }
  return (
    <button
      type="button"
      className={`${className} cursor-pointer`}
      title={`${title}（点击查看持久终端助手）`}
      aria-label={`${label}，查看持久终端助手`}
      onClick={onOpenHelper}
    >
      {content}
    </button>
  );
}
