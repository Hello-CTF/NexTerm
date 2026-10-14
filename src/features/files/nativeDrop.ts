import { useEffect, useRef, type RefObject } from "react";
import {
  EVENTS,
  listenEvent,
  type NativeDroppedPath,
  type NativeFilesDroppedEvent,
} from "../../ipc/events";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import type { FileEntryDto } from "../../ipc/types";
import { baseName } from "./pathUtils";
import { dropNativeFolderUpload } from "./folderUpload";
import { enqueueUploadWithConflicts } from "./transferActions";

// WebView2 delivers one OS drop twice: the DOM drop (HTML5 fallback) and the wails
// WindowFilesDropped event. The HTML5 handler always runs first, so a native event
// arriving within this window belongs to the drop the fallback already handled.
const HTML5_DROP_DEDUPE_MS = 750;

let lastHtml5DropAt = 0;

export function noteHtml5Drop(): void {
  lastHtml5DropAt = Date.now();
}

export function resetNativeDropDedupeForTest(): void {
  lastHtml5DropAt = 0;
}

function asNativeDropEvent(raw: unknown): NativeFilesDroppedEvent | null {
  if (typeof raw !== "object" || raw === null) return null;
  const { paths, x, y } = raw as Partial<NativeFilesDroppedEvent>;
  if (!Array.isArray(paths) || paths.length === 0) return null;
  if (
    !paths.every(
      (p) =>
        typeof (p as NativeDroppedPath | null)?.path === "string" &&
        typeof (p as NativeDroppedPath | null)?.dir === "boolean",
    )
  ) {
    return null;
  }
  if (typeof x !== "number" || typeof y !== "number") return null;
  return { paths, x, y };
}

export interface NativeDropSurface {
  surface: RefObject<HTMLElement | null>;
  fallbackDir: () => string;
  knownSiblings: (dir: string) => FileEntryDto[] | undefined;
  onSettled: (dir: string) => void;
}

async function handleNativeDrop(
  sessionId: string,
  dir: string,
  paths: NativeDroppedPath[],
  surface: NativeDropSurface,
): Promise<void> {
  const files = paths.filter((p) => !p.dir);
  if (files.length > 0) {
    try {
      await enqueueUploadWithConflicts({
        sessionId,
        dir,
        items: files.map((f) => ({ name: baseName(f.path), localPath: f.path })),
        knownSiblings: surface.knownSiblings(dir),
        refreshDir: dir,
      });
    } catch (e) {
      useUi.getState().pushToast("error", `上传失败：${describeError(e)}`);
    }
  }
  for (const folder of paths.filter((p) => p.dir)) {
    await dropNativeFolderUpload({
      sessionId,
      dir,
      folderPath: folder.path,
      knownSiblings: surface.knownSiblings(dir),
      onSettled: () => surface.onSettled(dir),
    });
  }
}

// Both file panels can be mounted at once, so the event is handled only by the panel
// whose data-file-drop-target surface actually sits under the drop point.
export function useNativeFileDrop(sessionId: string, surface: NativeDropSurface): void {
  const surfaceRef = useRef(surface);
  surfaceRef.current = surface;
  useEffect(() => {
    let off: (() => void) | null = null;
    let disposed = false;
    void listenEvent<unknown>(EVENTS.filesDropped, (raw) => {
      const event = asNativeDropEvent(raw);
      if (!event) return;
      if (Date.now() - lastHtml5DropAt < HTML5_DROP_DEDUPE_MS) return;
      const current = surfaceRef.current;
      const el = current.surface.current;
      if (!el) return;
      const point = document.elementFromPoint?.(event.x, event.y) ?? null;
      if (point?.closest("[data-file-drop-target]") !== el) return;
      const row = point?.closest<HTMLElement>("[data-drop-path]");
      const dir = row?.dataset.dropPath ?? current.fallbackDir();
      void handleNativeDrop(sessionId, dir, event.paths, current);
    }).then((unlisten) => {
      if (disposed) unlisten();
      else off = unlisten;
    });
    return () => {
      disposed = true;
      off?.();
    };
  }, [sessionId]);
}
