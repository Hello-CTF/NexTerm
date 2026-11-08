// Saving files a remote desktop copied. The desktop app writes them into a folder the user picks, keeping the copied
// folder structure; the browser offers each file as a download.

import { localFileApi } from "../../ipc/commands";
import { saveBrowserBlob } from "../../ipc/webFiles";
import { WEB } from "../../ipc/env";
import { bytesToBase64 } from "../../ui/base64";
import { pickLocalFolder } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { displayFileName } from "../terminal/imagePaste";
import { NATIVE_DROP_MAX_BYTES } from "./nativeRemoteDrop";
import type { ClipboardOutcome } from "./remoteTypes";
import { t } from "../../i18n";

// One entry of a remote file copy, as the RDP file channel lists it.
export interface RemoteFileEntry {
  name: string;
  // Folder within the copied collection, with `\` separators; absent at the top level.
  path?: string;
  size: number;
  isDirectory?: boolean;
}

// relativeSegments turns an entry into the path segments below the save folder, or null when the remote named a
// path that would leave it.
export function relativeSegments(entry: RemoteFileEntry): string[] | null {
  const segments = [...(entry.path ?? "").split(/[\\/]/), entry.name].filter((part) => part !== "" && part !== ".");
  if (segments.length === 0 || segments.some((part) => part === ".." || /[\0]/.test(part) || /^[A-Za-z]:$/.test(part))) return null;
  return segments;
}

function joinLocal(dir: string, segments: string[]): string {
  const sep = dir.includes("\\") && !dir.includes("/") ? "\\" : "/";
  return [dir.replace(/[\\/]+$/, ""), ...segments].join(sep);
}

// saveRemoteFiles downloads each file through `download` (given the entry's index in the copied list) and saves it.
export async function saveRemoteFiles(entries: RemoteFileEntry[], download: (index: number) => Promise<Blob>): Promise<ClipboardOutcome> {
  const files = entries.map((entry, index) => ({ entry, index })).filter(({ entry }) => !entry.isDirectory);
  if (files.length === 0) return { status: "empty" };
  const dir = WEB ? "" : await pickLocalFolder();
  if (dir === null) return { status: "refused", note: t("已取消保存远程文件") };
  const skipped: string[] = [];
  let saved = 0;
  for (const { entry, index } of files) {
    const label = displayFileName(entry.name);
    const segments = relativeSegments(entry);
    if (!segments) {
      skipped.push(t("「{name}」（路径无效）", { name: label }));
      continue;
    }
    if (!WEB && entry.size > NATIVE_DROP_MAX_BYTES) {
      skipped.push(t("「{name}」（超过 {size} MB）", { name: label, size: NATIVE_DROP_MAX_BYTES / 1024 / 1024 }));
      continue;
    }
    try {
      const blob = await download(index);
      if (WEB) {
        if (await saveBrowserBlob(entry.name, blob)) saved += 1;
        else skipped.push(t("「{name}」（已取消）", { name: label }));
        continue;
      }
      const result = await localFileApi.write(joinLocal(dir, segments), bytesToBase64(new Uint8Array(await blob.arrayBuffer())));
      if (result.exists) skipped.push(t("「{name}」（同名文件已存在）", { name: label }));
      else saved += 1;
    } catch (error) {
      skipped.push(t("「{name}」（{reason}）", { name: label, reason: describeError(error) }));
    }
  }
  const skippedNote = skipped.length > 0 ? t("未保存：{value}", { value: skipped.join(t("、")) }) : "";
  if (saved === 0) return { status: "refused", note: skippedNote || t("远程文件未能保存") };
  const savedNote = WEB ? t("已保存 {count} 个远程文件", { count: saved }) : t("已保存 {count} 个远程文件到 {dir}", { count: saved, dir });
  return { status: "done", note: [savedNote, skippedNote].filter(Boolean).join(t("；")) };
}
