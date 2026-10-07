import { useId, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { vaultApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";

export function VaultInitModal({
  onClose,
  onInitialized,
}: {
  onClose: () => void;
  onInitialized: () => void;
}) {
  const { pushToast } = useUi();
  const [password, setPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const modalRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const titleId = useId();
  const passwordId = useId();
  const layer = useOverlayFocus(true, modalRef, {
    initialFocus: () => inputRef.current,
  });
  const qc = useQueryClient();

  const submit = async () => {
    setSubmitting(true);
    try {
      await vaultApi.initMaster(password);
      void qc.invalidateQueries({ queryKey: ["vault-status"] });
      void qc.invalidateQueries({ queryKey: ["credentials"] });
      pushToast("success", "已开启密码保护");
      onInitialized();
    } catch (e) {
      pushToast("error", describeError(e));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="nx-overlay" onClick={onClose}>
      <div
        ref={modalRef}
        className="nx-modal flex max-w-[400px] flex-col"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (!layer.isTopmost()) return;
          if (e.key === "Escape" && !e.repeat && !isImeKeyEvent(e)) {
            e.preventDefault();
            onClose();
            return;
          }
          trapOverlayTab(e, modalRef.current);
        }}
      >
        <div className="nx-modal-header shrink-0">
          <span id={titleId} className="text-[13px] font-semibold text-neutral-100">初始化凭据保护</span>
        </div>
        <div className="nx-modal-body min-h-0 flex-1 overflow-y-auto">
          <div className="nx-form-row">
            <label className="nx-label" htmlFor={passwordId}>保护密码</label>
            <input
              id={passwordId}
              ref={inputRef}
              type="password"
              className="nx-input"
              placeholder="设置保护密码（至少 8 位）"
              value={password}
              autoComplete="off"
              onChange={(e) => setPassword(e.target.value)}
            />
            <div className="nx-hint mt-1.5">
              保存密码或私钥前，需要先给凭据库设置一个保护密码。密码只在本机使用，不会上传；
              忘记后无法找回，已保存的凭据将永远无法解密。
            </div>
          </div>
        </div>
        <div className="nx-modal-footer shrink-0">
          <button className="nx-btn nx-btn-ghost" onClick={onClose} disabled={submitting}>
            取消
          </button>
          <button
            className="nx-btn nx-btn-primary"
            disabled={password.length < 8 || submitting}
            onClick={() => void submit()}
          >
            {submitting ? "启用中…" : "启用保护"}
          </button>
        </div>
      </div>
    </div>
  );
}
