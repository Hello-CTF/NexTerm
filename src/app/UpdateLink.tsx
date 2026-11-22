import type { ReactNode } from "react";
import { DESKTOP } from "../ipc/env";
import { openExternalURL } from "../ipc/wails";
import { useUi } from "./store";
import { describeError } from "../ui/errorText";
import { t } from "../i18n";

// UpdateLink opens a release page or package in the system browser: through the desktop runtime on desktop, as a
// regular new-tab link in a browser.
export function UpdateLink({ href, children, className }: { href: string; children: ReactNode; className?: string }) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className={className ?? "underline hover:text-neutral-100"}
      onClick={(event) => {
        if (!DESKTOP) return;
        event.preventDefault();
        void openExternalURL(href).catch((e: unknown) =>
          useUi.getState().pushToast("error", t("无法打开链接：{value}", { value: describeError(e) })),
        );
      }}
    >
      {children}
    </a>
  );
}
