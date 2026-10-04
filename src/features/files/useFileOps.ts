import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { fsApi } from "../../ipc/commands";
import { ask, askChoice, promptText } from "../../ui/dialogs";
import { useUi, type AppTab } from "../../app/store";
import { describeError } from "../../ui/errorText";
import type { FileEntryDto } from "../../ipc/types";
import { isDirtyFileEditor } from "./editorGuards";
import { joinPath, norm, parentOf } from "./pathUtils";
import {
  CHECKSUM_ALGOS,
  DEFAULT_CHECKSUM_ALGO,
  findSibling,
  formatMode,
  losesOwnerAccess,
  octalDefaultFromEntry,
  parseOctalMode,
  validateEntryName,
} from "./fileOps";

export type FileOpKind = "rename" | "chmod" | "checksum";

const OP_LABEL: Record<FileOpKind, string> = {
  rename: "重命名",
  chmod: "修改权限",
  checksum: "计算校验值",
};

function editorTabsOf(sessionId: string, path: string, kind: string): AppTab[] {
  const target = norm(path);
  const hit = (p: string) => {
    const pn = norm(p);
    return kind === "dir" ? pn === target || pn.startsWith(`${target}/`) : pn === target;
  };
  const out: AppTab[] = [];
  for (const w of useUi.getState().workspaces) {
    for (const p of w.panes) {
      for (const t of p.tabs) {
        if (t.kind === "files" && t.sessionId === sessionId && t.path && hit(t.path)) {
          out.push(t);
        }
      }
    }
  }
  return out;
}

export function useFileOps(sessionId: string) {
  const qc = useQueryClient();
  const pushToast = useUi((s) => s.pushToast);
  const [busy, setBusy] = useState<{ op: FileOpKind; path: string } | null>(null);
  const busyRef = useRef<{ op: FileOpKind; path: string } | null>(null);
  const mountedRef = useRef(true);
  const seqRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      seqRef.current++;
    };
  }, []);

  const alive = (seq: number) => mountedRef.current && seqRef.current === seq;

  const begin = (op: FileOpKind, path: string): number | null => {
    if (busyRef.current) {
      pushToast("info", `上一个文件操作（${OP_LABEL[busyRef.current.op]}）还在进行，请等它完成`);
      return null;
    }
    busyRef.current = { op, path };
    setBusy(busyRef.current);
    return ++seqRef.current;
  };

  const end = (seq: number) => {
    if (seqRef.current !== seq) return;
    busyRef.current = null;
    if (mountedRef.current) setBusy(null);
  };

  const invalidateAfterRename = (listKey: string, from: string) => {
    void qc.invalidateQueries({ queryKey: ["fs", sessionId, listKey] });
    void qc.invalidateQueries({ queryKey: ["fs", sessionId, from] });
  };

  const renameEntry = async (
    entry: FileEntryDto,
    siblings: FileEntryDto[],
    listKey: string,
    onRenamed?: (from: string, to: string) => void,
  ) => {
    const seq = begin("rename", entry.path);
    if (seq === null) return;
    try {
      const notes: string[] = [];
      if (entry.kind === "symlink") {
        notes.push(
          `「${entry.name}」是符号链接（→ ${entry.symlinkTarget ?? "未知目标"}）。重命名只改链接本身的名字，不影响它指向的目标。`,
        );
      }
      const tabs = editorTabsOf(sessionId, entry.path, entry.kind);
      const dirty = tabs.some(isDirtyFileEditor);
      if (dirty) {
        notes.push(
          entry.kind === "dir"
            ? "该目录（含其子目录）内有文件正在编辑器中打开且有未保存的修改。重命名后，已打开的标签仍指向旧路径，未保存的内容仍会写到旧文件。"
            : "该文件正在编辑器中打开且有未保存的修改。重命名后，已打开的标签仍指向旧路径，未保存的内容仍会写到旧文件。",
        );
      }
      if (notes.length) {
        const go = await ask(`${notes.join("\n\n")}\n\n继续重命名？`, {
          title: "重命名",
          kind: "warning",
        });
        if (!go || !alive(seq)) return;
      }
      const openNote = tabs.length && !dirty ? "（编辑器中已打开；重命名后标签仍指向旧路径）" : "";
      const name = await promptText(`重命名 ${entry.path}${openNote}`, entry.name);
      if (name === null || !alive(seq)) return;
      const problem = validateEntryName(name);
      if (problem) {
        pushToast("error", `无法重命名：${problem}`);
        return;
      }
      if (name === entry.name) return;
      const to = joinPath(parentOf(entry.path) ?? "", name);
      const conflict = findSibling(siblings, name);
      if (conflict) {
        const go = await ask(
          `「${name}」已存在（${conflict.kind === "dir" ? "目录" : "文件"}）。继续将请求后端用重命名后的项替换已存在的「${name}」—— 旧「${name}」的内容将丢失；能否覆盖由后端最终决定，部分后端（如 SFTP）会拒绝。\n\n替换「${name}」？`,
          { title: "覆盖确认", kind: "warning" },
        );
        if (!go || !alive(seq)) return;
      }
      await fsApi.rename(sessionId, entry.path, to);
      if (!alive(seq)) return;
      invalidateAfterRename(listKey, entry.path);
      onRenamed?.(entry.path, to);
      pushToast("success", `已重命名：${entry.name} → ${name}`);
    } catch (e) {
      if (alive(seq)) pushToast("error", `重命名失败：${describeError(e)}`);
    } finally {
      end(seq);
    }
  };

  const chmodEntry = async (entry: FileEntryDto, listKey: string) => {
    const seq = begin("chmod", entry.path);
    if (seq === null) return;
    try {
      if (entry.kind === "symlink") {
        const go = await ask(
          `「${entry.name}」是符号链接（→ ${entry.symlinkTarget ?? "未知目标"}）。chmod 会跟随链接，修改它指向的**目标**的权限。\n\n继续？`,
          { title: "权限", kind: "warning" },
        );
        if (!go || !alive(seq)) return;
      }
      const current = octalDefaultFromEntry(entry.mode);
      const input = await promptText(
        `权限（八进制 000–777，当前 ${entry.mode || "未知"}）· ${entry.path}`,
        current,
      );
      if (input === null || !alive(seq)) return;
      const parsed = parseOctalMode(input);
      if (!parsed.ok) {
        pushToast("error", `无法修改权限：${parsed.reason}`);
        return;
      }
      const next = parsed.mode;
      const cur = current ? parseOctalMode(current) : null;
      const curMode = cur && cur.ok ? cur.mode : null;
      const danger = curMode !== null && losesOwnerAccess(curMode, next);
      const confirmText = `把 ${entry.path} 的权限从 ${
        curMode !== null ? formatMode(curMode) : (entry.mode || "未知")
      } 改为 ${formatMode(next)}？${
        danger ? "\n\n⚠️ 新权限移除了所有者的读取或写入，之后可能无法再打开或保存它。" : ""
      }`;
      const go = await ask(confirmText, { title: "权限", kind: danger ? "warning" : "info" });
      if (!go || !alive(seq)) return;
      await fsApi.chmod(sessionId, entry.path, next);
      if (!alive(seq)) return;
      void qc.invalidateQueries({ queryKey: ["fs", sessionId, listKey] });
      pushToast("success", `权限已更新：${entry.path} → ${formatMode(next)}`);
    } catch (e) {
      if (alive(seq)) pushToast("error", `修改权限失败：${describeError(e)}`);
    } finally {
      end(seq);
    }
  };

  const checksumEntry = async (entry: FileEntryDto) => {
    const seq = begin("checksum", entry.path);
    if (seq === null) return;
    try {
      const algo = await askChoice(`计算 ${entry.path} 的校验值：`, {
        title: "校验",
        level: "info",
        choices: CHECKSUM_ALGOS.map((a) => ({
          key: a.key,
          label: a.label,
          hint: a.hint,
          primary: a.key === DEFAULT_CHECKSUM_ALGO,
        })),
      });
      if (algo === null || !alive(seq)) return;
      const label = CHECKSUM_ALGOS.find((a) => a.key === algo)?.label ?? algo;
      pushToast("info", `正在计算 ${label}…`);
      const hash = await fsApi.checksum(sessionId, entry.path, algo);
      if (!alive(seq)) return;
      await promptText(`${label} · ${entry.path}（已全选，可直接复制）`, hash, {
        multiLine: false,
      });
      if (!alive(seq)) return;
      pushToast("success", `校验完成：${entry.name}`);
    } catch (e) {
      if (alive(seq)) pushToast("error", `计算校验值失败：${describeError(e)}`);
    } finally {
      end(seq);
    }
  };

  return { busy, renameEntry, chmodEntry, checksumEntry };
}
