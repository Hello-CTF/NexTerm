import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { fsApi } from "../../ipc/commands";
import { ask, askChoice, promptText } from "../../ui/dialogs";
import { useUi, type AppTab } from "../../app/store";
import { describeError } from "../../ui/errorText";
import type { FileEntryDto } from "../../ipc/types";
import { joinPath, parentOf } from "./pathUtils";
import {
  CHECKSUM_ALGOS,
  DEFAULT_CHECKSUM_ALGO,
  formatMode,
  losesOwnerAccess,
  octalDefaultFromEntry,
  parseOctalMode,
  validateEntryName,
} from "./fileOps";
import {
  beginFileEditorRename,
  fileEditorCaseInsensitive,
  fileEditorPathMatches,
  finishFileEditorRename,
  unknownFileEditorRenameByToken,
  unknownFileEditorRenameConflict,
  unknownFileEditorRenameForRename,
} from "./editorGuards";
import { conflictCaseInsensitive, findConflict } from "./transferQueue";

export type FileOpKind = "rename" | "chmod" | "checksum";

const OP_LABEL: Record<FileOpKind, string> = {
  rename: "重命名",
  chmod: "修改权限",
  checksum: "计算校验值",
};

function editorTabsOf(
  sessionId: string,
  path: string,
  kind: string,
  caseInsensitive: boolean,
): AppTab[] {
  const out: AppTab[] = [];
  for (const w of useUi.getState().workspaces) {
    for (const p of w.panes) {
      for (const t of p.tabs) {
        if (
          t.kind === "files" &&
          t.sessionId === sessionId &&
          t.path &&
          fileEditorPathMatches(t.path, path, kind, caseInsensitive)
        ) {
          out.push(t);
        }
      }
    }
  }
  return out;
}

const DEFINITE_RENAME_CODES = new Set(["forbidden", "not_found", "conflict", "already_exists"]);
const UNKNOWN_RENAME_CODES = new Set(["disconnected", "timeout"]);
const UNKNOWN_RENAME_MESSAGE =
  /\beof\b|unexpected\s+eof|fetch|http\s*\d{3}|网络|连接(?:中断|重置|关闭|丢失)|断开|断流|响应|broken pipe|socket|transport|stream/i;
const DEFINITE_RENAME_MESSAGE =
  /permission denied|access denied|not permitted|no such file|not found|does not exist|already exists|file exists|target exists|权限不足|没有权限|拒绝访问|不存在|已存在|冲突/i;

function renameResultUnknown(error: unknown): boolean {
  const code =
    error && typeof error === "object" && typeof (error as { code?: unknown }).code === "string"
      ? (error as { code: string }).code
      : "";
  if (UNKNOWN_RENAME_CODES.has(code)) return true;
  if (DEFINITE_RENAME_CODES.has(code)) return false;
  const message = describeError(error);
  if (UNKNOWN_RENAME_MESSAGE.test(message)) return true;
  if (DEFINITE_RENAME_MESSAGE.test(message)) return false;
  return true;
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

  const confirmUnknownRename = async (token: number, path: string) => {
    const operation = unknownFileEditorRenameByToken(token);
    if (!operation) return null;
    const choice = await askChoice(
      `重命名结果未知，不会自动重试。请先在文件列表确认旧路径和新路径，再选择实际结果。当前文件路径：${path}`,
      {
        title: "确认文件位置",
        level: "warning",
        choices: [
          {
            key: "not-renamed",
            label: "重命名未发生",
            hint: "确认旧路径仍是要保存的位置，清除未知记录并恢复旧路径保存",
          },
          {
            key: "renamed",
            label: "重命名已发生",
            hint: "确认服务端已完成重命名，迁移匹配的标签和草稿，不重复执行重命名",
          },
        ],
      },
    );
    if (choice !== "not-renamed" && choice !== "renamed") return null;
    const current = unknownFileEditorRenameByToken(token);
    if (!current) return null;
    if (choice === "renamed") {
      const target = unknownFileEditorRenameConflict(useUi.getState(), current);
      if (target) {
        useUi.getState().setActiveTab(target.id);
        pushToast(
          "error",
          `无法确认重命名已发生：目标 ${current.to} 已有打开的编辑器，请先处理该标签，未迁移或合并草稿。`,
        );
        return null;
      }
    }
    useUi.getState().resolveUnknownFileEditorRename(
      current.sessionId,
      current.from,
      choice === "renamed",
    );
    void qc.invalidateQueries({ queryKey: ["fs", current.sessionId] });
    return { operation: current, renamed: choice === "renamed" };
  };

  const renameEntry = async (
    entry: FileEntryDto,
    siblings: FileEntryDto[],
    listKey: string,
    onRenamed?: (from: string, to: string) => void,
  ) => {
    const seq = begin("rename", entry.path);
    if (seq === null) return;
    let renameToken: number | null = null;
    try {
      const notes: string[] = [];
      if (entry.kind === "symlink") {
        notes.push(
          `「${entry.name}」是符号链接（→ ${entry.symlinkTarget ?? "未知目标"}）。重命名只改链接本身的名字，不影响它指向的目标。`,
        );
      }
      if (notes.length) {
        const go = await ask(`${notes.join("\n\n")}\n\n继续重命名？`, {
          title: "重命名",
          kind: "warning",
        });
        if (!go || !alive(seq)) return;
      }
      const name = await promptText(`重命名 ${entry.path}`, entry.name);
      if (name === null || !alive(seq)) return;
      const problem = validateEntryName(name);
      if (problem) {
        pushToast("error", `无法重命名：${problem}`);
        return;
      }
      if (name === entry.name) return;
      const to = joinPath(parentOf(entry.path) ?? "", name);
      const sessionKind = useUi.getState().sessions.find((s) => s.id === sessionId)?.kind ?? "";
      const caseInsensitive = fileEditorCaseInsensitive(sessionKind);
      const rejectOpenTarget = (): boolean => {
        const target = editorTabsOf(sessionId, to, entry.kind, caseInsensitive).find(
          (tab) =>
            tab.path !== undefined &&
            !fileEditorPathMatches(tab.path, entry.path, entry.kind, caseInsensitive),
        );
        if (!target) return false;
        useUi.getState().setActiveTab(target.id);
        pushToast("error", `无法重命名：目标 ${to} 已有打开的编辑器，请先处理该标签，未执行覆盖或合并。`);
        return true;
      };
      if (rejectOpenTarget()) return;
      const conflict = findConflict(
        name,
        siblings.filter((sibling) => sibling !== entry),
        conflictCaseInsensitive(sessionKind),
      );
      if (conflict) {
        const go = await ask(
          `「${name}」已存在。继续将替换原有内容，且无法恢复。\n\n替换「${name}」？`,
          { title: "覆盖确认", kind: "warning" },
        );
        if (!go || !alive(seq)) return;
        if (rejectOpenTarget()) return;
      }
      const guard = beginFileEditorRename(sessionId, entry.path, to, entry.kind, caseInsensitive);
      if (guard === "unknown") {
        pushToast("error", "无法重命名：上一次重命名结果未知，请先确认远端旧路径和新路径，不会自动重试。");
        const operation = unknownFileEditorRenameForRename(
          sessionId,
          entry.path,
          to,
          entry.kind,
          caseInsensitive,
        );
        if (operation) {
          const resolved = await confirmUnknownRename(operation.token, entry.path);
          if (resolved?.renamed && alive(seq)) {
            onRenamed?.(resolved.operation.from, resolved.operation.to);
          }
        }
        return;
      }
      if (guard === "busy") {
        pushToast("error", "无法重命名：相关编辑器正在保存、读取或重命名，请等待完成后重试。");
        return;
      }
      renameToken = guard;
      await fsApi.rename(sessionId, entry.path, to);
      useUi.getState().migrateFileEditorLocators(sessionId, entry.path, to, entry.kind);
      finishFileEditorRename(guard, false);
      renameToken = null;
      invalidateAfterRename(listKey, entry.path);
      if (!alive(seq)) return;
      onRenamed?.(entry.path, to);
      pushToast("success", `已重命名：${entry.name} → ${name}`);
    } catch (e) {
      const unknownToken = renameToken !== null && renameResultUnknown(e) ? renameToken : null;
      if (renameToken !== null) finishFileEditorRename(renameToken, unknownToken !== null);
      renameToken = null;
      if (unknownToken !== null) {
        if (alive(seq)) {
          pushToast(
            "error",
            "重命名结果未知：请重连后确认远端旧路径和新路径；确认前已禁止向旧路径保存，不会自动重试。",
          );
        }
        const resolved = await confirmUnknownRename(unknownToken, entry.path);
        if (resolved?.renamed && alive(seq)) {
          onRenamed?.(resolved.operation.from, resolved.operation.to);
        }
      } else if (alive(seq)) {
        pushToast("error", `重命名失败：${describeError(e)}`);
      }
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
          `「${entry.name}」是符号链接（→ ${entry.symlinkTarget ?? "未知目标"}）。chmod 会跟随链接，修改链接目标的权限。\n\n继续？`,
          { title: "权限", kind: "warning" },
        );
        if (!go || !alive(seq)) return;
      }
      const current = octalDefaultFromEntry(entry.mode);
      const input = await promptText(
        `权限（八进制 000-777，当前 ${entry.mode || "未知"}）· ${entry.path}`,
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
        danger ? "\n\n新权限会移除所有者的读取或写入权限，之后可能无法再打开或保存它" : ""
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
