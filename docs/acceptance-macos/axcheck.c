// axcheck - 检测并触发 macOS 辅助功能（Accessibility）授权
// 用法: axcheck         -> 只检测
//       axcheck prompt  -> 检测并在未授权时弹出系统授权提示
#include <ApplicationServices/ApplicationServices.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/types.h>
#include <sys/sysctl.h>

// 私有 API：查出某个 pid 的「责任进程」（TCC 归属的那个进程）
extern pid_t responsibility_get_pid_responsible_for_pid(pid_t pid);

static void print_responsible(void) {
    pid_t me = getpid();
    pid_t resp = responsibility_get_pid_responsible_for_pid(me);
    printf("self pid            = %d\n", me);
    printf("responsible pid     = %d\n", resp);
    char buf[4096];
    // 取责任进程的可执行路径
    int mib[4] = {CTL_KERN, KERN_PROCARGS2, resp};
    size_t sz = sizeof(buf);
    if (sysctl(mib, 3, buf, &sz, NULL, 0) == 0) {
        // buf 前 4 字节是 argc，随后是 exec path
        int argc = *(int *)buf;
        char *p = buf + sizeof(int);
        size_t remain = sz - sizeof(int);
        size_t len = strnlen(p, remain);
        printf("responsible exe     = %s (argc=%d)\n", p, argc);
    } else {
        printf("responsible exe     = <sysctl failed>\n");
    }
}

int main(int argc, char **argv) {
    int do_prompt = (argc > 1 && strcmp(argv[1], "prompt") == 0);

    print_responsible();

    const void *keys[] = { kAXTrustedCheckOptionPrompt };
    const void *vals[] = { do_prompt ? kCFBooleanTrue : kCFBooleanFalse };
    CFDictionaryRef opts = CFDictionaryCreate(
        NULL, keys, vals, 1,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);

    Boolean trusted = AXIsProcessTrustedWithOptions(opts);
    printf("AXIsProcessTrusted  = %s\n", trusted ? "true" : "false");
    if (!trusted && do_prompt) {
        printf("已请求系统授权提示（请看屏幕 / 系统设置）\n");
    }
    CFRelease(opts);
    return trusted ? 0 : 1;
}
