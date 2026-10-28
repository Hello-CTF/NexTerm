import { openFileTab, useUi } from "../../app/store";
import { fsApi, sessionApi } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { openExternalURL } from "../../ipc/wails";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { isEditableFile } from "../files/fileTypes";
import type { TerminalLinkDeps } from "./terminalLinks";

// Local sessions run on a known machine: its name comes from the backend, so a
// nested ssh that reports its own host through OSC 7 cannot redirect file links.
const localHostnames = new Map<string, Promise<string | null>>();

function localHostname(sessionId: string): Promise<string | null> {
  let pending = localHostnames.get(sessionId);
  if (!pending) {
    pending = sessionApi.localHostname(sessionId).then(
      (name) => name || null,
      () => {
        // Unknown host: file links fall back to asking. A later link retries.
        localHostnames.delete(sessionId);
        return null;
      },
    );
    localHostnames.set(sessionId, pending);
  }
  return pending;
}

// The application side of the terminal link policy: external targets go to
// the system browser (desktop) or a new noopener tab (web), confirmations use
// the in-app dialog, and session files open in the file editor or browser.
// reportedHost is the host the terminal's shell reported (OSC 7); it is used
// for remote sessions.
export function terminalLinkDeps(
  sessionId: string,
  containerId: string | undefined,
  reportedHost: () => string | null,
): TerminalLinkDeps {
  return {
    sessionId,
    containerId,
    sessionHost: () => {
      const kind = useUi.getState().sessions.find((session) => session.id === sessionId)?.kind;
      return kind === "local" ? localHostname(sessionId) : reportedHost();
    },
    openExternal: (url) => {
      if (WEB) {
        window.open(url, "_blank", "noopener,noreferrer");
        return;
      }
      return openExternalURL(url);
    },
    confirm: (message, title) => ask(message, { title, kind: "warning" }),
    notify: (kind, text) => useUi.getState().pushToast(kind, text),
    listDirectory: (id, dir) => fsApi.list(id, dir),
    isEditableFile,
    openFile: openFileTab,
    // Directories are browsed in the host workspace's file tree sidebar; there is no file browser tab to open.
    openDirectory: (_id, path) => useUi.getState().pushToast("info", `「${path}」是目录，可在左侧文件树中浏览`),
    describeError,
  };
}
