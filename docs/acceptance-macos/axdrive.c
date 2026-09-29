// axdrive - 用原生 Accessibility API 枚举并驱动 macOS GUI
// 所有子命令在同一次枚举快照上执行，避免索引漂移。
//
//   axdrive <pid>                               枚举 AX 树
//   axdrive <pid> find <role> [titleSub]        列出匹配元素
//   axdrive <pid> press <role> <titleSub>       按 role+文案定位并 AXPress
//   axdrive <pid> set <role> <titleSub> <text>  按定位设置值
//   axdrive <pid> attrs <idx>                   打印元素全部属性
//   axdrive <pid> mouseclick <x> <y>            屏幕坐标点击（CGEvent，单位=点）
//   axdrive <pid> typetext <text>               向焦点输入 Unicode 文本
//   axdrive <pid> key <keycode>                 发送按键（36=Return 48=Tab 53=Esc）
//   axdrive <pid> focus <role> <titleSub>       把键盘焦点设到该元素
#include <ApplicationServices/ApplicationServices.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#define MAXN 6000
#define MAXDEPTH 60
static AXUIElementRef g_list[MAXN];
static int g_n = 0;

static CFStringRef copyStrAttr(AXUIElementRef el, CFStringRef attr) {
    CFTypeRef v = NULL;
    if (AXUIElementCopyAttributeValue(el, attr, &v) != kAXErrorSuccess || !v) return NULL;
    if (CFGetTypeID(v) == CFStringGetTypeID()) return (CFStringRef)v;
    if (CFGetTypeID(v) == CFNumberGetTypeID()) {
        int i = 0; CFNumberGetValue((CFNumberRef)v, kCFNumberIntType, &i);
        CFRelease(v);
        return CFStringCreateWithFormat(NULL, NULL, CFSTR("%d"), i);
    }
    CFRelease(v);
    return NULL;
}

static void cprint(CFStringRef s, int width) {
    if (!s) { printf("%-*s", width, "-"); return; }
    char buf[1024];
    if (!CFStringGetCString(s, buf, sizeof(buf), kCFStringEncodingUTF8)) { printf("%-*s", width, "<?>"); return; }
    // 单行化，避免折行打乱表格
    for (char *p = buf; *p; p++) if (*p == '\n' || *p == '\r' || *p == '\t') *p = ' ';
    printf("%-*s", width, buf);
}

static void dumpEl(AXUIElementRef el, int depth) {
    if (g_n >= MAXN || depth > MAXDEPTH) return;
    int idx = g_n++;
    g_list[idx] = (AXUIElementRef)CFRetain(el);

    CFStringRef role  = copyStrAttr(el, kAXRoleAttribute);
    CFStringRef sub   = copyStrAttr(el, kAXSubroleAttribute);
    CFStringRef title = copyStrAttr(el, kAXTitleAttribute);
    CFStringRef desc  = copyStrAttr(el, kAXDescriptionAttribute);
    CFStringRef val   = copyStrAttr(el, kAXValueAttribute);

    printf("[%4d] ", idx);
    for (int i = 0; i < depth && i < 20; i++) printf("  ");
    cprint(role, 22); printf(" ");
    cprint(sub, 14);  printf(" ");
    cprint(title, 20); printf(" ");
    cprint(desc, 20);  printf(" ");
    cprint(val, 34);   printf("\n");

    if (role) CFRelease(role);
    if (sub) CFRelease(sub);
    if (title) CFRelease(title);
    if (desc) CFRelease(desc);
    if (val) CFRelease(val);

    CFTypeRef children = NULL;
    if (AXUIElementCopyAttributeValue(el, kAXChildrenAttribute, &children) == kAXErrorSuccess && children) {
        if (CFGetTypeID(children) == CFArrayGetTypeID()) {
            CFIndex n = CFArrayGetCount((CFArrayRef)children);
            for (CFIndex i = 0; i < n; i++)
                dumpEl((AXUIElementRef)CFArrayGetValueAtIndex((CFArrayRef)children, i), depth + 1);
        }
        CFRelease(children);
    }
}

static Boolean strEq(CFStringRef a, const char *b) {
    if (!a || !b) return false;
    char buf[1024];
    if (!CFStringGetCString(a, buf, sizeof(buf), kCFStringEncodingUTF8)) return false;
    return strcmp(buf, b) == 0;
}
static Boolean strHas(CFStringRef a, const char *b) {
    if (!a || !b) return false;
    char buf[1024];
    if (!CFStringGetCString(a, buf, sizeof(buf), kCFStringEncodingUTF8)) return false;
    return strstr(buf, b) != NULL;
}

// 匹配：role 精确（"-" 或 "*" 表示任意），titleSub 在 title/desc/value 任一命中
static Boolean matches(AXUIElementRef el, const char *role, const char *titleSub) {
    if (role && strcmp(role, "-") != 0 && strcmp(role, "*") != 0) {
        CFStringRef r = copyStrAttr(el, kAXRoleAttribute);
        Boolean ok = strEq(r, role);
        if (r) CFRelease(r);
        if (!ok) return false;
    }
    if (titleSub && strcmp(titleSub, "-") != 0) {
        CFStringRef t = copyStrAttr(el, kAXTitleAttribute);
        CFStringRef d = copyStrAttr(el, kAXDescriptionAttribute);
        CFStringRef v = copyStrAttr(el, kAXValueAttribute);
        Boolean ok = strHas(t, titleSub) || strHas(d, titleSub) || strHas(v, titleSub);
        if (t) CFRelease(t); if (d) CFRelease(d); if (v) CFRelease(v);
        if (!ok) return false;
    }
    return true;
}

static void postKeyTo(CGKeyCode code, CGEventFlags flags, pid_t target) {
    CGEventRef down = CGEventCreateKeyboardEvent(NULL, code, true);
    CGEventRef up   = CGEventCreateKeyboardEvent(NULL, code, false);
    if (flags) { CGEventSetFlags(down, flags); CGEventSetFlags(up, flags); }
    if (target > 0) {
        CGEventPostToPid(target, down);
        CGEventPostToPid(target, up);
    } else {
        CGEventPost(kCGHIDEventTap, down);
        CGEventPost(kCGHIDEventTap, up);
    }
    CFRelease(down); CFRelease(up);
}

static void postKey(CGKeyCode code, CGEventFlags flags) { postKeyTo(code, flags, 0); }

static void typeTextTo(const char *text, pid_t target) {
    CFStringRef s = CFStringCreateWithCString(NULL, text, kCFStringEncodingUTF8);
    CFIndex len = CFStringGetLength(s);
    for (CFIndex i = 0; i < len; i++) {
        UniChar ch = CFStringGetCharacterAtIndex(s, i);
        CGEventRef dn = CGEventCreateKeyboardEvent(NULL, 0, true);
        CGEventRef up = CGEventCreateKeyboardEvent(NULL, 0, false);
        CGEventKeyboardSetUnicodeString(dn, 1, &ch);
        CGEventKeyboardSetUnicodeString(up, 1, &ch);
        if (target > 0) {
            CGEventPostToPid(target, dn);
            CGEventPostToPid(target, up);
        } else {
            CGEventPost(kCGHIDEventTap, dn);
            CGEventPost(kCGHIDEventTap, up);
        }
        CFRelease(dn); CFRelease(up);
        usleep(15000);
    }
    CFRelease(s);
}

int main(int argc, char **argv) {
    if (argc < 2) { fprintf(stderr, "usage: axdrive <pid> [cmd ...]\n"); return 2; }
    pid_t pid = (pid_t)atoi(argv[1]);
    AXUIElementRef app = AXUIElementCreateApplication(pid);
    const char *cmd = argc >= 3 ? argv[2] : NULL;

    // 关键：让 WKWebView 进入「增强辅助模式」（VoiceOver 同款），
    // 否则 Web 内容不暴露完整语义，AXSetValue / AXPress 也会静默失效。
    AXUIElementSetAttributeValue(app, CFSTR("AXEnhancedUserInterface"), kCFBooleanTrue);

    // 鼠标/键盘类命令不需要 AX 树
    if (cmd && strcmp(cmd, "mouseclick") == 0) {
        double x = atof(argv[3]), y = atof(argv[4]);
        CGPoint pt = CGPointMake(x, y);
        CGEventRef move = CGEventCreateMouseEvent(NULL, kCGEventMouseMoved, pt, kCGMouseButtonLeft);
        CGEventPost(kCGHIDEventTap, move); CFRelease(move);
        usleep(120000);
        CGEventRef down = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseDown, pt, kCGMouseButtonLeft);
        CGEventPost(kCGHIDEventTap, down);
        usleep(60000);
        CGEventRef up = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseUp, pt, kCGMouseButtonLeft);
        CGEventPost(kCGHIDEventTap, up);
        CFRelease(down); CFRelease(up);
        printf("mouseclick(%.0f,%.0f) posted\n", x, y);
        return 0;
    }
    if (cmd && strcmp(cmd, "typetext") == 0) {
        CFStringRef s = CFStringCreateWithCString(NULL, argv[3], kCFStringEncodingUTF8);
        CFIndex len = CFStringGetLength(s);
        for (CFIndex i = 0; i < len; i++) {
            UniChar ch = CFStringGetCharacterAtIndex(s, i);
            CGEventRef dn = CGEventCreateKeyboardEvent(NULL, 0, true);
            CGEventRef up = CGEventCreateKeyboardEvent(NULL, 0, false);
            CGEventKeyboardSetUnicodeString(dn, 1, &ch);
            CGEventKeyboardSetUnicodeString(up, 1, &ch);
            CGEventPost(kCGHIDEventTap, dn);
            CGEventPost(kCGHIDEventTap, up);
            CFRelease(dn); CFRelease(up);
            usleep(12000);
        }
        CFRelease(s);
        printf("typetext posted (%ld chars)\n", (long)len);
        return 0;
    }
    if (cmd && strcmp(cmd, "key") == 0) {
        postKey((CGKeyCode)atoi(argv[3]), 0);
        printf("key %s posted\n", argv[3]);
        return 0;
    }
    if (cmd && strcmp(cmd, "keymod") == 0) {
        // keymod <keycode> <mask>   mask: 1=shift 2=ctrl 4=alt 8=cmd
        int mask = atoi(argv[4]);
        CGEventFlags f = 0;
        if (mask & 1) f |= kCGEventFlagMaskShift;
        if (mask & 2) f |= kCGEventFlagMaskControl;
        if (mask & 4) f |= kCGEventFlagMaskAlternate;
        if (mask & 8) f |= kCGEventFlagMaskCommand;
        postKey((CGKeyCode)atoi(argv[3]), f);
        printf("keymod %s mask=%d posted\n", argv[3], mask);
        return 0;
    }

    // 其余命令先枚举 AX 树
    CFTypeRef wins = NULL;
    AXError e = AXUIElementCopyAttributeValue(app, kAXWindowsAttribute, &wins);
    if (e != kAXErrorSuccess) {
        printf("AXWindows error = %d ; trusted=%s\n", (int)e, AXIsProcessTrusted() ? "true" : "false");
        return 1;
    }
    if (wins && CFGetTypeID(wins) == CFArrayGetTypeID()) {
        CFIndex n = CFArrayGetCount((CFArrayRef)wins);
        if (!cmd || strcmp(cmd, "find") != 0) printf("windows = %ld\n", (long)n);
        for (CFIndex i = 0; i < n; i++)
            dumpEl((AXUIElementRef)CFArrayGetValueAtIndex((CFArrayRef)wins, i), 0);
    }

    if (!cmd) return 0;

    if (strcmp(cmd, "find") == 0) {
        const char *role = argc >= 4 ? argv[3] : "*";
        const char *ttl  = argc >= 5 ? argv[4] : "-";
        printf("=== find role=%s text=%s ===\n", role, ttl);
        for (int i = 0; i < g_n; i++) {
            if (!matches(g_list[i], role, ttl)) continue;
            CFStringRef r = copyStrAttr(g_list[i], kAXRoleAttribute);
            CFStringRef t = copyStrAttr(g_list[i], kAXTitleAttribute);
            CFStringRef d = copyStrAttr(g_list[i], kAXDescriptionAttribute);
            CFStringRef v = copyStrAttr(g_list[i], kAXValueAttribute);
            printf("[%4d] role=", i); cprint(r, 22);
            printf(" title="); cprint(t, 26);
            printf(" desc="); cprint(d, 22);
            printf(" value="); cprint(v, 40);
            printf("\n");
            if (r) CFRelease(r); if (t) CFRelease(t); if (d) CFRelease(d); if (v) CFRelease(v);
        }
        return 0;
    }

    if (strcmp(cmd, "press") == 0) {
        const char *role = argv[3];
        const char *ttl  = argv[4];
        int hits = 0;
        for (int i = 0; i < g_n; i++) {
            if (!matches(g_list[i], role, ttl)) continue;
            hits++;
            AXError pe = AXUIElementPerformAction(g_list[i], kAXPressAction);
            printf("AXPress [%d] %s / %s -> %d (%s)\n", i, role, ttl, (int)pe,
                   pe == kAXErrorSuccess ? "OK" : "FAIL");
            if (pe == kAXErrorSuccess) return 0;
        }
        printf("no pressable match for %s / %s (hits=%d)\n", role, ttl, hits);
        return 1;
    }

    if (strcmp(cmd, "set") == 0) {
        const char *role = argv[3];
        const char *ttl  = argv[4];
        const char *text = argv[5];
        for (int i = 0; i < g_n; i++) {
            if (!matches(g_list[i], role, ttl)) continue;
            CFStringRef s = CFStringCreateWithCString(NULL, text, kCFStringEncodingUTF8);
            AXError se = AXUIElementSetAttributeValue(g_list[i], kAXValueAttribute, s);
            printf("AXSetValue [%d] %s / %s -> %d (%s)\n", i, role, ttl, (int)se,
                   se == kAXErrorSuccess ? "OK" : "FAIL");
            CFRelease(s);
            if (se == kAXErrorSuccess) return 0;
        }
        printf("no settable match for %s / %s\n", role, ttl);
        return 1;
    }

    if (strcmp(cmd, "focus") == 0) {
        const char *role = argv[3];
        const char *ttl  = argv[4];
        for (int i = 0; i < g_n; i++) {
            if (!matches(g_list[i], role, ttl)) continue;
            AXError fe = AXUIElementSetAttributeValue(g_list[i], kAXFocusedAttribute, kCFBooleanTrue);
            printf("AXFocus [%d] %s / %s -> %d (%s)\n", i, role, ttl, (int)fe,
                   fe == kAXErrorSuccess ? "OK" : "FAIL");
            if (fe == kAXErrorSuccess) return 0;
        }
        printf("no focusable match for %s / %s\n", role, ttl);
        return 1;
    }

    if (strcmp(cmd, "attrs") == 0) {
        int idx = atoi(argv[3]);
        if (idx < 0 || idx >= g_n) { printf("idx out of range (0..%d)\n", g_n - 1); return 1; }
        CFArrayRef names = NULL;
        AXUIElementCopyAttributeNames(g_list[idx], &names);
        if (names) {
            CFIndex n = CFArrayGetCount(names);
            for (CFIndex i = 0; i < n; i++) {
                CFStringRef k = (CFStringRef)CFArrayGetValueAtIndex(names, i);
                CFTypeRef v = NULL;
                if (AXUIElementCopyAttributeValue(g_list[idx], k, &v) == kAXErrorSuccess && v) {
                    char kb[256]; CFStringGetCString(k, kb, sizeof(kb), kCFStringEncodingUTF8);
                    if (CFGetTypeID(v) == CFStringGetTypeID()) {
                        char vb[1024]; CFStringGetCString((CFStringRef)v, vb, sizeof(vb), kCFStringEncodingUTF8);
                        printf("  %-32s = %s\n", kb, vb);
                    } else if (CFGetTypeID(v) == CFNumberGetTypeID()) {
                        int iv = 0; CFNumberGetValue((CFNumberRef)v, kCFNumberIntType, &iv);
                        printf("  %-32s = %d\n", kb, iv);
                    } else if (CFGetTypeID(v) == CFBooleanGetTypeID()) {
                        printf("  %-32s = %s\n", kb, CFBooleanGetValue((CFBooleanRef)v) ? "true" : "false");
                    } else if (CFGetTypeID(v) == CFArrayGetTypeID()) {
                        printf("  %-32s = <array %ld>\n", kb, (long)CFArrayGetCount((CFArrayRef)v));
                    } else {
                        printf("  %-32s = <obj>\n", kb);
                    }
                    CFRelease(v);
                }
            }
            CFRelease(names);
        }
        return 0;
    }

    if (strcmp(cmd, "setidx") == 0) {
        int idx = atoi(argv[3]);
        if (idx < 0 || idx >= g_n) { printf("idx out of range (0..%d)\n", g_n - 1); return 1; }
        CFStringRef s = CFStringCreateWithCString(NULL, argv[4], kCFStringEncodingUTF8);
        AXError se = AXUIElementSetAttributeValue(g_list[idx], kAXValueAttribute, s);
        printf("AXSetValue [%d] -> %d (%s)\n", idx, (int)se, se == kAXErrorSuccess ? "OK" : "FAIL");
        // 回读确认
        CFStringRef back = copyStrAttr(g_list[idx], kAXValueAttribute);
        printf("  readback = "); cprint(back, 60); printf("\n");
        if (back) CFRelease(back);
        CFRelease(s);
        return se == kAXErrorSuccess ? 0 : 1;
    }

    if (strcmp(cmd, "pressidx") == 0) {
        int idx = atoi(argv[3]);
        if (idx < 0 || idx >= g_n) { printf("idx out of range (0..%d)\n", g_n - 1); return 1; }
        AXError pe = AXUIElementPerformAction(g_list[idx], kAXPressAction);
        printf("AXPress [%d] -> %d (%s)\n", idx, (int)pe, pe == kAXErrorSuccess ? "OK" : "FAIL");
        return pe == kAXErrorSuccess ? 0 : 1;
    }

    if (strcmp(cmd, "selidx") == 0) {
        // 列出 popup 的所有选项，并选中第 nth 项（1-based）
        int idx = atoi(argv[3]);
        int nth = atoi(argv[4]);
        if (idx < 0 || idx >= g_n) { printf("idx out of range (0..%d)\n", g_n - 1); return 1; }
        CFTypeRef opts = NULL;
        AXError oe = AXUIElementCopyAttributeValue(g_list[idx], kAXChildrenAttribute, &opts);
        if (oe != kAXErrorSuccess || !opts) { printf("no children (%d)\n", (int)oe); return 1; }
        CFIndex n = CFArrayGetCount((CFArrayRef)opts);
        printf("popup children = %ld\n", (long)n);
        AXUIElementRef pick = NULL;
        for (CFIndex i = 0; i < n; i++) {
            AXUIElementRef c = (AXUIElementRef)CFArrayGetValueAtIndex((CFArrayRef)opts, i);
            CFStringRef t = copyStrAttr(c, kAXTitleAttribute);
            CFStringRef v = copyStrAttr(c, kAXValueAttribute);
            printf("  child[%ld] title=", (long)i); cprint(t, 26);
            printf(" value="); cprint(v, 26); printf("\n");
            if ((int)i == nth - 1) pick = c;
            if (t) CFRelease(t); if (v) CFRelease(v);
        }
        if (pick) {
            AXError pe = AXUIElementPerformAction(pick, kAXPressAction);
            printf("select child[%d] -> %d (%s)\n", nth - 1, (int)pe, pe == kAXErrorSuccess ? "OK" : "FAIL");
        } else {
            printf("child[%d] 不存在\n", nth - 1);
        }
        CFRelease(opts);
        return 0;
    }

    if (strcmp(cmd, "activate") == 0) {
        AXUIElementSetAttributeValue(app, kAXFrontmostAttribute, kCFBooleanTrue);
        printf("activate ok\n");
        return 0;
    }

    if (strcmp(cmd, "focusidx") == 0) {
        int idx = atoi(argv[3]);
        if (idx < 0 || idx >= g_n) { printf("idx out of range (0..%d)\n", g_n - 1); return 1; }
        AXError fe = AXUIElementSetAttributeValue(g_list[idx], kAXFocusedAttribute, kCFBooleanTrue);
        printf("AXFocus [%d] -> %d (%s)\n", idx, (int)fe, fe == kAXErrorSuccess ? "OK" : "FAIL");
        if (fe != kAXErrorSuccess) {
            // 回退：先让父元素聚焦
            AXError pe = AXUIElementPerformAction(g_list[idx], kAXPressAction);
            printf("  fallback press -> %d\n", (int)pe);
        }
        return 0;
    }

    if (strcmp(cmd, "retype") == 0) {
        // focus idx + Cmd+A + Delete + 向该 pid 直接投递按键
        int idx = atoi(argv[3]);
        const char *text = argv[4];
        if (idx < 0 || idx >= g_n) { printf("idx out of range (0..%d)\n", g_n - 1); return 1; }
        AXError fe = AXUIElementSetAttributeValue(g_list[idx], kAXFocusedAttribute, kCFBooleanTrue);
        printf("AXFocus [%d] -> %d (%s)\n", idx, (int)fe, fe == kAXErrorSuccess ? "OK" : "FAIL");
        usleep(300000);
        // 校验焦点确实落位
        CFTypeRef fv = NULL;
        if (AXUIElementCopyAttributeValue(g_list[idx], kAXFocusedAttribute, &fv) == kAXErrorSuccess && fv) {
            printf("  focused_now = %s\n", CFBooleanGetValue((CFBooleanRef)fv) ? "true" : "false");
            CFRelease(fv);
        }
        postKeyTo((CGKeyCode)0, kCGEventFlagMaskCommand, pid);   // Cmd+A
        usleep(150000);
        postKeyTo((CGKeyCode)51, 0, pid);                        // Delete
        usleep(150000);
        typeTextTo(text, pid);
        usleep(300000);
        CFStringRef back = copyStrAttr(g_list[idx], kAXValueAttribute);
        printf("readback = "); cprint(back, 70); printf("\n");
        if (back) CFRelease(back);
        return 0;
    }

    if (strcmp(cmd, "typepid") == 0) { typeTextTo(argv[3], pid); printf("typepid ok\n"); return 0; }
    if (strcmp(cmd, "keypid") == 0) { postKeyTo((CGKeyCode)atoi(argv[3]), 0, pid); printf("keypid ok\n"); return 0; }
    if (strcmp(cmd, "keymodpid") == 0) {
        int mask = atoi(argv[4]);
        CGEventFlags f = 0;
        if (mask & 1) f |= kCGEventFlagMaskShift;
        if (mask & 2) f |= kCGEventFlagMaskControl;
        if (mask & 4) f |= kCGEventFlagMaskAlternate;
        if (mask & 8) f |= kCGEventFlagMaskCommand;
        postKeyTo((CGKeyCode)atoi(argv[3]), f, pid);
        printf("keymodpid ok\n");
        return 0;
    }

    if (strcmp(cmd, "rect") == 0) {
        int idx = atoi(argv[3]);
        if (idx < 0 || idx >= g_n) { printf("idx out of range (0..%d)\n", g_n - 1); return 1; }
        CFTypeRef pv = NULL, sv = NULL;
        CGPoint pt = CGPointZero; CGSize sz = CGSizeZero;
        if (AXUIElementCopyAttributeValue(g_list[idx], kAXPositionAttribute, &pv) == kAXErrorSuccess && pv) {
            AXValueGetValue((AXValueRef)pv, kAXValueCGPointType, &pt);
            CFRelease(pv);
        }
        if (AXUIElementCopyAttributeValue(g_list[idx], kAXSizeAttribute, &sv) == kAXErrorSuccess && sv) {
            AXValueGetValue((AXValueRef)sv, kAXValueCGSizeType, &sz);
            CFRelease(sv);
        }
        printf("rect idx=%d pos=(%.0f,%.0f) size=(%.0f,%.0f) center=(%.0f,%.0f)\n",
               idx, pt.x, pt.y, sz.width, sz.height,
               pt.x + sz.width / 2, pt.y + sz.height / 2);
        return 0;
    }

    if (strcmp(cmd, "clicktype") == 0) {
        // 取元素中心 -> 鼠标点击激活窗口 -> 全选清空 -> 输入
        int idx = atoi(argv[3]);
        const char *text = argc >= 5 ? argv[4] : NULL;
        if (idx < 0 || idx >= g_n) { printf("idx out of range (0..%d)\n", g_n - 1); return 1; }
        CFTypeRef pv = NULL, sv = NULL;
        CGPoint pt = CGPointZero; CGSize sz = CGSizeZero;
        AXUIElementCopyAttributeValue(g_list[idx], kAXPositionAttribute, &pv);
        if (pv) { AXValueGetValue((AXValueRef)pv, kAXValueCGPointType, &pt); CFRelease(pv); }
        AXUIElementCopyAttributeValue(g_list[idx], kAXSizeAttribute, &sv);
        if (sv) { AXValueGetValue((AXValueRef)sv, kAXValueCGSizeType, &sz); CFRelease(sv); }
        CGPoint c = CGPointMake(pt.x + sz.width / 2, pt.y + sz.height / 2);
        printf("click at (%.0f,%.0f)\n", c.x, c.y);

        CGEventRef mv = CGEventCreateMouseEvent(NULL, kCGEventMouseMoved, c, kCGMouseButtonLeft);
        CGEventPost(kCGHIDEventTap, mv); CFRelease(mv);
        usleep(150000);
        CGEventRef dn = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseDown, c, kCGMouseButtonLeft);
        CGEventPost(kCGHIDEventTap, dn);
        usleep(80000);
        CGEventRef up = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseUp, c, kCGMouseButtonLeft);
        CGEventPost(kCGHIDEventTap, up);
        CFRelease(dn); CFRelease(up);
        usleep(500000);

        // 清空（Cmd+A + Delete）后再补一次单击，确保插入点就位
        postKey((CGKeyCode)0, kCGEventFlagMaskCommand);
        usleep(150000);
        postKey((CGKeyCode)51, 0);
        usleep(200000);

        if (text) typeTextTo(text, 0);
        usleep(400000);
        CFStringRef back = copyStrAttr(g_list[idx], kAXValueAttribute);
        printf("readback = "); cprint(back, 70); printf("\n");
        if (back) CFRelease(back);
        return 0;
    }

    if (strcmp(cmd, "setlabel") == 0) {
        // setlabel <label> <text> [fieldRole]
        // 找到 value/title/desc == label 的 AXStaticText，再向后找第一个输入框并赋值。
        // 这样定位只依赖 DOM 顺序，不受 AX 索引漂移影响。
        const char *label = argv[3];
        const char *text  = argv[4];
        const char *frole = argc >= 6 ? argv[5] : "AXTextField";
        int anchor = -1;
        for (int i = 0; i < g_n; i++) {
            CFStringRef r = copyStrAttr(g_list[i], kAXRoleAttribute);
            Boolean isStatic = strEq(r, "AXStaticText");
            if (r) CFRelease(r);
            if (!isStatic) continue;
            CFStringRef t = copyStrAttr(g_list[i], kAXTitleAttribute);
            CFStringRef d = copyStrAttr(g_list[i], kAXDescriptionAttribute);
            CFStringRef v = copyStrAttr(g_list[i], kAXValueAttribute);
            Boolean hit = strEq(t, label) || strEq(d, label) || strEq(v, label);
            if (t) CFRelease(t); if (d) CFRelease(d); if (v) CFRelease(v);
            if (hit) { anchor = i; break; }
        }
        if (anchor < 0) { printf("label '%s' not found\n", label); return 1; }
        for (int i = anchor + 1; i < g_n; i++) {
            CFStringRef r = copyStrAttr(g_list[i], kAXRoleAttribute);
            Boolean ok = strEq(r, frole);
            if (r) CFRelease(r);
            if (!ok) continue;
            CFStringRef s = CFStringCreateWithCString(NULL, text, kCFStringEncodingUTF8);
            AXError se = AXUIElementSetAttributeValue(g_list[i], kAXValueAttribute, s);
            CFRelease(s);
            printf("setlabel '%s' -> anchor=%d field=%d err=%d (%s)\n", label, anchor, i, (int)se,
                   se == kAXErrorSuccess ? "OK" : "FAIL");
            CFStringRef back = copyStrAttr(g_list[i], kAXValueAttribute);
            printf("  readback = "); cprint(back, 60); printf("\n");
            if (back) CFRelease(back);
            return se == kAXErrorSuccess ? 0 : 1;
        }
        printf("no %s after label '%s'\n", frole, label);
        return 1;
    }

    if (strcmp(cmd, "presslabel") == 0) {
        // presslabel <label> [role]  —— 找到标签后的第一个指定控件并按下
        const char *label = argv[3];
        const char *role  = argc >= 5 ? argv[4] : "AXButton";
        for (int i = 0; i < g_n; i++) {
            CFStringRef r = copyStrAttr(g_list[i], kAXRoleAttribute);
            Boolean isStatic = strEq(r, "AXStaticText");
            if (r) CFRelease(r);
            if (!isStatic) continue;
            CFStringRef t = copyStrAttr(g_list[i], kAXTitleAttribute);
            CFStringRef d = copyStrAttr(g_list[i], kAXDescriptionAttribute);
            CFStringRef v = copyStrAttr(g_list[i], kAXValueAttribute);
            Boolean hit = strEq(t, label) || strEq(d, label) || strEq(v, label);
            if (t) CFRelease(t); if (d) CFRelease(d); if (v) CFRelease(v);
            if (!hit) continue;
            for (int j = i + 1; j < g_n; j++) {
                CFStringRef rj = copyStrAttr(g_list[j], kAXRoleAttribute);
                Boolean ok = (strcmp(role, "*") == 0) || strEq(rj, role);
                if (rj) CFRelease(rj);
                if (!ok) continue;
                AXError pe = AXUIElementPerformAction(g_list[j], kAXPressAction);
                printf("presslabel '%s' -> anchor=%d target=%d err=%d (%s)\n", label, i, j, (int)pe,
                       pe == kAXErrorSuccess ? "OK" : "FAIL");
                return pe == kAXErrorSuccess ? 0 : 1;
            }
        }
        printf("label '%s' not found (or no %s after)\n", label, role);
        return 1;
    }

    if (strcmp(cmd, "getlabel") == 0) {
        const char *label = argv[3];
        const char *frole = argc >= 5 ? argv[4] : "AXTextField";
        for (int i = 0; i < g_n; i++) {
            CFStringRef r = copyStrAttr(g_list[i], kAXRoleAttribute);
            Boolean isStatic = strEq(r, "AXStaticText");
            if (r) CFRelease(r);
            if (!isStatic) continue;
            CFStringRef t = copyStrAttr(g_list[i], kAXTitleAttribute);
            CFStringRef d = copyStrAttr(g_list[i], kAXDescriptionAttribute);
            CFStringRef v = copyStrAttr(g_list[i], kAXValueAttribute);
            Boolean hit = strEq(t, label) || strEq(d, label) || strEq(v, label);
            if (t) CFRelease(t); if (d) CFRelease(d); if (v) CFRelease(v);
            if (!hit) continue;
            for (int j = i + 1; j < g_n; j++) {
                CFStringRef rj = copyStrAttr(g_list[j], kAXRoleAttribute);
                Boolean ok = strEq(rj, frole) ||
                             (strcmp(frole, "AXTextField") == 0 && strEq(rj, "AXSecureTextField")) ||
                             (strcmp(frole, "AXTextField") == 0 && strEq(rj, "AXTextArea"));
                if (rj) CFRelease(rj);
                if (!ok) continue;
                CFStringRef val = copyStrAttr(g_list[j], kAXValueAttribute);
                printf("getlabel '%s' field=%d value=", label, j); cprint(val, 80); printf("\n");
                if (val) CFRelease(val);
                return 0;
            }
        }
        printf("label '%s' not found\n", label);
        return 1;
    }

    if (strcmp(cmd, "getfocused") == 0) {
        CFTypeRef fe = NULL;
        AXError ge = AXUIElementCopyAttributeValue(app, kAXFocusedUIElementAttribute, &fe);
        if (ge != kAXErrorSuccess || !fe) { printf("no focused element (%d)\n", (int)ge); return 1; }
        AXUIElementRef el = (AXUIElementRef)fe;
        CFStringRef r = copyStrAttr(el, kAXRoleAttribute);
        CFStringRef v = copyStrAttr(el, kAXValueAttribute);
        CFStringRef d = copyStrAttr(el, kAXDescriptionAttribute);
        printf("focused role="); cprint(r, 22);
        printf(" value="); cprint(v, 60);
        printf(" desc="); cprint(d, 30); printf("\n");
        if (r) CFRelease(r); if (v) CFRelease(v); if (d) CFRelease(d);
        CFRelease(fe);
        return 0;
    }

    if (strcmp(cmd, "setfocused") == 0) {
        // 直接写当前焦点元素（WebKit 只认焦点元素的 setAccessibilityValue）
        CFTypeRef fe = NULL;
        AXError ge = AXUIElementCopyAttributeValue(app, kAXFocusedUIElementAttribute, &fe);
        if (ge != kAXErrorSuccess || !fe) { printf("no focused element (%d)\n", (int)ge); return 1; }
        AXUIElementRef el = (AXUIElementRef)fe;
        CFStringRef r = copyStrAttr(el, kAXRoleAttribute);
        printf("focused role="); cprint(r, 22); printf("\n");
        if (r) CFRelease(r);
        CFStringRef s = CFStringCreateWithCString(NULL, argv[3], kCFStringEncodingUTF8);
        AXError se = AXUIElementSetAttributeValue(el, kAXValueAttribute, s);
        printf("setfocused -> %d (%s)\n", (int)se, se == kAXErrorSuccess ? "OK" : "FAIL");
        CFRelease(s);
        usleep(300000);
        CFStringRef back = copyStrAttr(el, kAXValueAttribute);
        printf("  readback = "); cprint(back, 70); printf("\n");
        if (back) CFRelease(back);
        CFRelease(fe);
        return 0;
    }

    if (strcmp(cmd, "setfocusedidx") == 0) {
        // 先给 idx 设焦点，再写值——两步同一进程内完成
        int idx = atoi(argv[3]);
        if (idx < 0 || idx >= g_n) { printf("idx out of range (0..%d)\n", g_n - 1); return 1; }
        AXUIElementSetAttributeValue(g_list[idx], kAXFocusedAttribute, kCFBooleanTrue);
        usleep(200000);
        CFTypeRef fe = NULL;
        AXError ge = AXUIElementCopyAttributeValue(app, kAXFocusedUIElementAttribute, &fe);
        if (ge != kAXErrorSuccess || !fe) { printf("no focused element after set (%d)\n", (int)ge); return 1; }
        AXUIElementRef el = (AXUIElementRef)fe;
        // 判断焦点是否真落在目标上（或其后代）
        printf("target=%d focused=%p target=%p %s\n", idx, (void *)el, (void *)g_list[idx],
               CFEqual(el, g_list[idx]) ? "SAME" : "DIFF");
        CFStringRef s = CFStringCreateWithCString(NULL, argv[4], kCFStringEncodingUTF8);
        AXError se = AXUIElementSetAttributeValue(el, kAXValueAttribute, s);
        printf("setfocusedidx -> %d (%s)\n", (int)se, se == kAXErrorSuccess ? "OK" : "FAIL");
        CFRelease(s);
        CFRelease(fe);
        return 0;
    }

    if (strcmp(cmd, "filllabel") == 0) {
        // filllabel <label> <text> [fieldRole]
        // 1) 按标签锚定目标输入框  2) 鼠标点击其中心聚焦  3) 写当前焦点元素
        // WebKit 的 setAccessibilityValue 只对焦点元素生效，所以必须走这条链。
        const char *label = argv[3];
        const char *text  = argv[4];
        const char *frole = argc >= 6 ? argv[5] : "AXTextField";
        int target = -1;
        for (int i = 0; i < g_n; i++) {
            CFStringRef r = copyStrAttr(g_list[i], kAXRoleAttribute);
            Boolean isStatic = strEq(r, "AXStaticText");
            if (r) CFRelease(r);
            if (!isStatic) continue;
            CFStringRef t = copyStrAttr(g_list[i], kAXTitleAttribute);
            CFStringRef d = copyStrAttr(g_list[i], kAXDescriptionAttribute);
            CFStringRef v = copyStrAttr(g_list[i], kAXValueAttribute);
            Boolean hit = strEq(t, label) || strEq(d, label) || strEq(v, label);
            if (t) CFRelease(t); if (d) CFRelease(d); if (v) CFRelease(v);
            if (!hit) continue;
            for (int j = i + 1; j < g_n; j++) {
                CFStringRef rj = copyStrAttr(g_list[j], kAXRoleAttribute);
                Boolean ok = strEq(rj, frole);
                if (rj) CFRelease(rj);
                if (ok) { target = j; break; }
            }
            break;
        }
        if (target < 0) { printf("filllabel: no %s after '%s'\n", frole, label); return 1; }

        CFTypeRef pv = NULL, sv = NULL;
        CGPoint pt = CGPointZero; CGSize sz = CGSizeZero;
        AXUIElementCopyAttributeValue(g_list[target], kAXPositionAttribute, &pv);
        if (pv) { AXValueGetValue((AXValueRef)pv, kAXValueCGPointType, &pt); CFRelease(pv); }
        AXUIElementCopyAttributeValue(g_list[target], kAXSizeAttribute, &sv);
        if (sv) { AXValueGetValue((AXValueRef)sv, kAXValueCGSizeType, &sz); CFRelease(sv); }
        CGPoint c = CGPointMake(pt.x + sz.width / 2, pt.y + sz.height / 2);
        printf("filllabel '%s': target=%d click=(%.0f,%.0f)\n", label, target, c.x, c.y);

        CGEventRef mv = CGEventCreateMouseEvent(NULL, kCGEventMouseMoved, c, kCGMouseButtonLeft);
        CGEventPost(kCGHIDEventTap, mv); CFRelease(mv);
        usleep(120000);
        CGEventRef dn = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseDown, c, kCGMouseButtonLeft);
        CGEventPost(kCGHIDEventTap, dn);
        usleep(80000);
        CGEventRef up = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseUp, c, kCGMouseButtonLeft);
        CGEventPost(kCGHIDEventTap, up);
        CFRelease(dn); CFRelease(up);
        usleep(500000);

        CFTypeRef fe = NULL;
        AXError ge = AXUIElementCopyAttributeValue(app, kAXFocusedUIElementAttribute, &fe);
        if (ge != kAXErrorSuccess || !fe) { printf("  no focused element (%d)\n", (int)ge); return 1; }
        AXUIElementRef el = (AXUIElementRef)fe;
        CFStringRef fr = copyStrAttr(el, kAXRoleAttribute);
        printf("  focused role="); cprint(fr, 22); printf("\n");
        if (fr) CFRelease(fr);

        CFStringRef s = CFStringCreateWithCString(NULL, text, kCFStringEncodingUTF8);
        AXError se = AXUIElementSetAttributeValue(el, kAXValueAttribute, s);
        printf("  set -> %d (%s)\n", (int)se, se == kAXErrorSuccess ? "OK" : "FAIL");
        CFRelease(s);
        usleep(300000);
        CFStringRef back = copyStrAttr(el, kAXValueAttribute);
        printf("  readback = "); cprint(back, 70); printf("\n");
        if (back) CFRelease(back);
        CFRelease(fe);
        return se == kAXErrorSuccess ? 0 : 1;
    }

    if (cmd && strcmp(cmd, "dblclick") == 0) {
        double x = atof(argv[3]), y = atof(argv[4]);
        CGPoint pt = CGPointMake(x, y);
        CGEventRef mv = CGEventCreateMouseEvent(NULL, kCGEventMouseMoved, pt, kCGMouseButtonLeft);
        CGEventPost(kCGHIDEventTap, mv); CFRelease(mv);
        usleep(150000);
        for (int k = 1; k <= 2; k++) {
            CGEventRef dn = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseDown, pt, kCGMouseButtonLeft);
            CGEventSetIntegerValueField(dn, kCGMouseEventClickState, k);
            CGEventPost(kCGHIDEventTap, dn);
            usleep(45000);
            CGEventRef up = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseUp, pt, kCGMouseButtonLeft);
            CGEventSetIntegerValueField(up, kCGMouseEventClickState, k);
            CGEventPost(kCGHIDEventTap, up);
            CFRelease(dn); CFRelease(up);
            usleep(70000);
        }
        printf("dblclick(%.0f,%.0f) posted\n", x, y);
        return 0;
    }

    if (strcmp(cmd, "setwin") == 0) {
        // setwin <x> <y> <w> <h>  —— 调整窗口位置与尺寸（针对 pid 的第一个窗口）
        double x = atof(argv[3]), y = atof(argv[4]);
        double w = atof(argv[5]), h = atof(argv[6]);
        // 找到窗口元素
        CFTypeRef wins = NULL;
        if (AXUIElementCopyAttributeValue(app, kAXWindowsAttribute, &wins) != kAXErrorSuccess || !wins) {
            printf("no windows\n"); return 1;
        }
        AXUIElementRef win = NULL;
        double best = -1;
        CFIndex wn = CFArrayGetCount((CFArrayRef)wins);
        for (CFIndex i = 0; i < wn; i++) {
            AXUIElementRef c = (AXUIElementRef)CFArrayGetValueAtIndex((CFArrayRef)wins, i);
            CFStringRef sub = copyStrAttr(c, kAXSubroleAttribute);
            Boolean std = strEq(sub, "AXStandardWindow");
            if (sub) CFRelease(sub);
            CFTypeRef szv = NULL;
            CGSize sz = CGSizeZero;
            if (AXUIElementCopyAttributeValue(c, kAXSizeAttribute, &szv) == kAXErrorSuccess && szv) {
                AXValueGetValue((AXValueRef)szv, kAXValueCGSizeType, &sz);
                CFRelease(szv);
            }
            double area = sz.width * sz.height;
            if (std && area > best) { best = area; win = c; }
        }
        if (!win) { printf("no AXStandardWindow\n"); CFRelease(wins); return 1; }
        AXValueRef pv = AXValueCreate(kAXValueCGPointType, &(CGPoint){x, y});
        AXValueRef sv = AXValueCreate(kAXValueCGSizeType, &(CGSize){w, h});
        AXError e1 = AXUIElementSetAttributeValue(win, kAXPositionAttribute, pv);
        AXError e2 = AXUIElementSetAttributeValue(win, kAXSizeAttribute, sv);
        printf("setwin pos->%d size->%d\n", (int)e1, (int)e2);
        CFRelease(pv); CFRelease(sv); CFRelease(wins);
        return 0;
    }

    printf("unknown cmd: %s\n", cmd);
    return 2;
}
