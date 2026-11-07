import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { useUi } from "../../app/store";
import { copyText } from "../../ui/clipboard";
import { describeError } from "../../ui/errorText";
import { cleanTerminalLink, displayTerminalLink, planTerminalLink } from "./terminalLinks";
import { terminalIntegrations } from "./terminalIntegrations";
import type { ExtractedLink } from "./terminalLinkProvider";
import "./terminalLinks.css";
import { t } from "../../i18n";

interface TerminalLinkPanelProps {
  /** Newest first. */
  links: ExtractedLink[];
  onOpen: (url: string) => void;
  onClose: () => void;
}

export function TerminalLinkPanel({ links, onOpen, onClose }: TerminalLinkPanelProps) {
  const [selected, setSelected] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);
  const openInApp = terminalIntegrations().openInApp;
  const last = links.length - 1;

  useEffect(() => {
    listRef.current?.focus();
  }, []);

  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>('[aria-selected="true"]');
    el?.scrollIntoView?.({ block: "nearest" });
  }, [selected]);

  const copy = (url: string) => {
    const pushToast = useUi.getState().pushToast;
    void copyText(cleanTerminalLink(url)).then(
      () => pushToast("success", t("已复制链接")),
      (e: unknown) => pushToast("error", t("复制链接失败：{value}", { value: describeError(e) })),
    );
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.nativeEvent.isComposing) return;
    const current = links[selected];
    let handled = true;
    if (e.key === "ArrowDown") setSelected((i) => Math.min(last, i + 1));
    else if (e.key === "ArrowUp") setSelected((i) => Math.max(0, i - 1));
    else if (e.key === "Home") setSelected(0);
    else if (e.key === "End") setSelected(Math.max(0, last));
    else if (e.key === "Escape") onClose();
    else if (e.key === "Enter" && e.shiftKey) {
      if (openInApp && current) {
        const plan = planTerminalLink(current.url);
        if (plan.kind === "external" && /^https?:/i.test(plan.url)) {
          openInApp(plan.url);
          onClose();
        } else {
          const why = plan.kind === "blocked" ? `（${plan.reason}）` : "";
          useUi.getState().pushToast("info", t("只能在 NexTerm 中打开 http/https 链接{why}", { why }));
        }
      }
    } else if (e.key === "Enter") {
      if (current) onOpen(current.url);
    } else if ((e.key === "c" || e.key === "C") && !e.ctrlKey && !e.metaKey && !e.altKey) {
      if (current) copy(current.url);
    } else handled = false;
    if (handled) {
      e.preventDefault();
      e.stopPropagation();
    }
  };

  return (
    <div className="nx-term-link-panel" role="dialog" aria-label={t("终端链接面板")}>
      <div
        ref={listRef}
        className="nx-term-link-list"
        role="listbox"
        aria-label={t("终端链接")}
        tabIndex={0}
        aria-activedescendant={links[selected] ? `nx-term-link-${selected}` : undefined}
        onKeyDown={onKeyDown}
      >
        {links.length === 0 ? (
          <div className="nx-term-link-empty">{t("当前终端没有可识别的链接")}</div>
        ) : (
          links.map((link, i) => {
            const context = cleanTerminalLink(link.context);
            return (
            <div
              key={link.url}
              id={`nx-term-link-${i}`}
              role="option"
              aria-selected={i === selected}
              className="nx-term-link-row"
              onMouseMove={() => i !== selected && setSelected(i)}
              onClick={() => onOpen(link.url)}
            >
              <span className="nx-term-link-url">{displayTerminalLink(link.url)}</span>
              {context && <span className="nx-term-link-context">{context}</span>}
            </div>
            );
          })
        )}
      </div>
      <div className="nx-term-link-footer">
        {t("↑↓ 选择 · Enter 打开 · C 复制{value} · Esc 关闭", { value: openInApp ? t(" · Shift+Enter 在 NexTerm 中打开") : "" })}
      </div>
    </div>
  );
}
