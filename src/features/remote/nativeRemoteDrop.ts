// Native OS file drops into a remote desktop. On macOS and Linux the desktop shell takes over file drops, so the page
// sees no DOM drop event, only the shell's files-dropped event with local paths. The files are read through the
// desktop's local file commands and handed to the remote client the same way a DOM drop is.

import { localFileApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { displayFileName } from "../terminal/imagePaste";
import { baseName } from "../files/pathUtils";
import type { NativeDroppedPath } from "../../ipc/events";
import { t } from "../../i18n";

// Each file crosses the IPC bridge as one base64 string, so a dropped file is capped well below what the RDP file
// channel itself allows.
export const NATIVE_DROP_MAX_BYTES = 128 * 1024 * 1024;

const MIME_BY_EXTENSION: Record<string, string> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  bmp: "image/bmp",
};

function mimeOf(name: string): string {
  const dot = name.lastIndexOf(".");
  return dot < 0 ? "" : (MIME_BY_EXTENSION[name.slice(dot + 1).toLowerCase()] ?? "");
}

function base64ToBytes(base64: string): Uint8Array<ArrayBuffer> {
  const binary = atob(base64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

export interface NativeDropFiles {
  files: File[];
  // A user-facing description of every dropped item that was not read.
  skipped: string[];
}

// readNativeDrop reads the dropped regular files. Folders are not sent: the remote file channel takes a flat list.
export async function readNativeDrop(paths: NativeDroppedPath[]): Promise<NativeDropFiles> {
  const files: File[] = [];
  const skipped: string[] = [];
  for (const entry of paths) {
    const name = baseName(entry.path);
    if (entry.dir) {
      skipped.push(t("文件夹「{value}」", { value: displayFileName(name) }));
      continue;
    }
    try {
      const read = await localFileApi.read(entry.path, NATIVE_DROP_MAX_BYTES);
      files.push(new File([base64ToBytes(read.contentBase64)], name, { type: mimeOf(name) }));
    } catch (error) {
      skipped.push(t("「{name}」（{reason}）", { name: displayFileName(name), reason: describeError(error) }));
    }
  }
  return { files, skipped };
}

// filesTransfer wraps files in the part of the DataTransfer interface the remote clients read, so a native drop takes
// the same path as a DOM drop.
export function filesTransfer(files: File[]): DataTransfer {
  return {
    files,
    items: [],
    types: files.length > 0 ? ["Files"] : [],
    getData: () => "",
  } as unknown as DataTransfer;
}
