import { useCallback } from "react";

const CWD_DISPLAY_LIMIT = 28;

export function shortenCwd(cwd: string): string {
  if (cwd.length <= CWD_DISPLAY_LIMIT) return cwd;
  const segments = cwd.split("/").filter(Boolean);
  const tail = segments.slice(-2).join("/");
  return tail ? `…/${tail}` : cwd.slice(-CWD_DISPLAY_LIMIT);
}

export function Cwd({ cwd }: { cwd: string | null }) {
  const copy = useCallback(() => {
    void navigator.clipboard?.writeText(cwd ?? "").catch(() => undefined);
  }, [cwd]);
  if (!cwd) return null;
  return (
    <button
      className="nx-badge max-w-[220px] truncate font-mono text-neutral-400 hover:text-neutral-100"
      title={`工作目录：${cwd}\n点击复制完整路径`}
      onClick={copy}
    >
      {shortenCwd(cwd)}
    </button>
  );
}
