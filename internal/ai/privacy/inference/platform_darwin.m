//go:build darwin && !ios && cgo

#import <Foundation/Foundation.h>
int ng_macos_supported(void) {
    NSOperatingSystemVersion version = [[NSProcessInfo processInfo] operatingSystemVersion];
    return version.majorVersion > 13 || (version.majorVersion == 13 && version.minorVersion >= 4);
}
