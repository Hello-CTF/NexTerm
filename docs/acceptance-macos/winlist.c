#include <CoreGraphics/CoreGraphics.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdio.h>

int main(void) {
    CFArrayRef list = CGWindowListCopyWindowInfo(
        kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements,
        kCGNullWindowID);
    if (!list) { printf("no window list\n"); return 1; }
    CFIndex n = CFArrayGetCount(list);
    for (CFIndex i = 0; i < n; i++) {
        CFDictionaryRef d = (CFDictionaryRef)CFArrayGetValueAtIndex(list, i);
        CFStringRef owner = (CFStringRef)CFDictionaryGetValue(d, kCGWindowOwnerName);
        CFStringRef name  = (CFStringRef)CFDictionaryGetValue(d, kCGWindowName);
        CFNumberRef layer = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowLayer);
        CFNumberRef num   = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowNumber);
        CFDictionaryRef bounds = (CFDictionaryRef)CFDictionaryGetValue(d, kCGWindowBounds);
        CGRect r = CGRectMake(0, 0, 0, 0);
        if (bounds) CGRectMakeWithDictionaryRepresentation(bounds, &r);
        int lv = 0, nn = 0;
        if (layer) CFNumberGetValue(layer, kCFNumberIntType, &lv);
        if (num)   CFNumberGetValue(num, kCFNumberIntType, &nn);
        char obuf[256] = "?", nbuf[256] = "";
        if (owner) CFStringGetCString(owner, obuf, sizeof obuf, kCFStringEncodingUTF8);
        if (name)  CFStringGetCString(name,  nbuf, sizeof nbuf, kCFStringEncodingUTF8);
        printf("layer=%3d num=%-8d owner=%-22s bounds=(%5d,%5d %5dx%5d) name=%s\n",
               lv, nn, obuf, (int)r.origin.x, (int)r.origin.y,
               (int)r.size.width, (int)r.size.height, nbuf);
    }
    return 0;
}
