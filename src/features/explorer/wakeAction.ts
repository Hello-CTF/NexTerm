import { wakeApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import { t } from "../../i18n";

export const WAKE_DISCLAIMER = t("数据包已发出，不代表主机已经唤醒。请稍等片刻再连接；主机不响应时检查 BIOS/网卡的 Wake-on-LAN 设置和网络。");

// wakeAsset asks the backend to send the magic packet for a stored asset. The destination is read from the stored
// asset by the backend; only the id travels. Success and failure are both shown to the user.
export async function wakeAsset(assetId: string, name: string): Promise<boolean> {
  const { pushToast } = useUi.getState();
  try {
    const result = await wakeApi.wake(assetId);
    const route = result.route === "device" ? t("经受管设备发出") : t("由本机发出");
    const note = result.note ? ` ${result.note}。` : "";
    pushToast("success", t("已向「{name}」（{mac}）发送唤醒包，{route}，目标 {target}。{note}{WAKE_DISCLAIMER}", { name, mac: result.mac, route, target: result.target, note, WAKE_DISCLAIMER }));
    return true;
  } catch (error) {
    pushToast("error", t("唤醒「{name}」失败：{value}", { name, value: describeError(error) }));
    return false;
  }
}
