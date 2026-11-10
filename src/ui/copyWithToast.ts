import { useUi } from "../app/store";
import { t } from "../i18n";
import { copyText } from "./clipboard";

// Copies text and reports a failure as a toast; the success message is optional.
export function copyTextWithToast(text: string, success?: string): Promise<boolean> {
  return copyText(text).then(
    () => {
      if (success) useUi.getState().pushToast("success", success);
      return true;
    },
    () => {
      useUi.getState().pushToast("error", t("复制失败，请重试"));
      return false;
    },
  );
}
