// secinput - 检测 macOS「安全键盘输入」(Secure Event Input) 是否开启
// 该状态下系统会屏蔽所有合成键盘事件（CGEventPost），常用于密码框与远控软件。
#include <stdio.h>
#include <dlfcn.h>

typedef int (*fn_t)(void);

int main(void) {
    void *h = dlopen("/System/Library/Frameworks/Carbon.framework/Carbon", RTLD_LAZY);
    if (!h) h = dlopen("/System/Library/Frameworks/Carbon.framework/Versions/A/Carbon", RTLD_LAZY);
    if (!h) { printf("Carbon.framework 加载失败\n"); return 2; }
    fn_t f = (fn_t)dlsym(h, "IsSecureEventInputEnabled");
    if (!f) { printf("IsSecureEventInputEnabled 符号未找到\n"); return 2; }
    int on = f();
    printf("SecureEventInputEnabled = %s\n", on ? "true (键盘事件被系统屏蔽)" : "false");
    return on ? 1 : 0;
}
