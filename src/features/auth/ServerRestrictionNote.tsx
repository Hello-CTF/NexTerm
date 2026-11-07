import { t } from "../../i18n";
// ServerRestrictionNote explains why a field is unavailable on a multi-user server; the optional clear
// button lets the user remove an old value even though the input itself is disabled.
export function ServerRestrictionNote({
  text,
  onClear,
  clearLabel = t("清除"),
}: {
  text: string;
  onClear?: () => void;
  clearLabel?: string;
}) {
  return (
    <div className="nx-hint mt-1.5 flex flex-wrap items-center gap-2 text-amber-400" role="note">
      <span>↳ {text}</span>
      {onClear && (
        <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" onClick={onClear}>
          {clearLabel}
        </button>
      )}
    </div>
  );
}
