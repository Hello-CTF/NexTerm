import { openDirectoryTab, openFileTab, useUi } from "../../app/store";
import { fsApi } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { openExternalURL } from "../../ipc/wails";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { isEditableFile } from "../files/fileTypes";
import type { TerminalLinkDeps } from "./terminalLinks";

// The application side of the terminal link policy: external targets go to
// the system browser (desktop) or a new noopener tab (web), confirmations use
// the in-app dialog, and session files open in the file editor or browser.
export function terminalLinkDeps(
  sessionId: string,
  containerId: string | undefined,
  sessionHost: () => string | null,
): TerminalLinkDeps {
  return {
    sessionId,
    containerId,
    sessionHost,
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
    openDirectory: openDirectoryTab,
    describeError,
  };
}
