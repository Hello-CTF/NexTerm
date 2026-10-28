//go:build ios && cgo

#import <Foundation/Foundation.h>
int ng_ios_supported(void) {
    NSOperatingSystemVersion version = [[NSProcessInfo processInfo] operatingSystemVersion];
    return version.majorVersion > 15 || (version.majorVersion == 15 && version.minorVersion >= 1);
}
