import { DESKTOP } from "../../../ipc/env";
import { localFileApi } from "../../../ipc/commands";
import { openWailsFiles, openWailsFolder } from "../../../ipc/wails";
import { saveBrowserBlob } from "../../../ipc/webFiles";
import { ask } from "../../../ui/dialogs";
import { useUi } from "../../../app/store";
import { isWindows } from "../../../app/platform";
import { installTrzszPickers } from "./trzszFiles";
import { currentTrzszRequestContext } from "./trzszSession";

function pickInputFiles(directory: boolean): Promise<File[] | null> {
  return new Promise((resolve) => {
    const input = document.createElement("input");
    input.type = "file";
    input.multiple = true;
    if (directory) input.setAttribute("webkitdirectory", "");
    input.style.position = "fixed";
    input.style.left = "-9999px";
    document.body.appendChild(input);
    let settled = false;
    const finish = (files: File[] | null) => {
      if (settled) return;
      settled = true;
      window.removeEventListener("focus", onFocus);
      input.remove();
      resolve(files);
    };
    input.addEventListener("change", () => finish(input.files && input.files.length > 0 ? Array.from(input.files) : null));
    input.addEventListener("cancel", () => finish(null));
    const onFocus = () => {
      window.setTimeout(() => {
        if (!input.files || input.files.length === 0) finish(null);
      }, 800);
    };
    window.addEventListener("focus", onFocus);
    input.click();
  });
}

export function installDefaultTrzszPickers(): void {
  installTrzszPickers(window as never, {
    desktop: DESKTOP,
    windows: isWindows(),
    ask: (message, options) => ask(message, options),
    notify: (kind, message) => useUi.getState().pushToast(kind, message),
    context: currentTrzszRequestContext,
    openFiles: openWailsFiles,
    openFolder: openWailsFolder,
    listFolder: (path) => localFileApi.listFolder(path),
    readRange: (path, offset, maxBytes) => localFileApi.readRange(path, offset, maxBytes),
    writeFile: (path, content) => localFileApi.write(path, content),
    pickBrowserFiles: pickInputFiles,
    saveBlob: saveBrowserBlob,
  });
}
