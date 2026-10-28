//go:build android && cgo

package inference

/*
#include <stdlib.h>
#include <sys/system_properties.h>
static int ng_android_api(void) {
    char value[PROP_VALUE_MAX] = {0};
    if (__system_property_get("ro.build.version.sdk", value) <= 0) return 0;
    return atoi(value);
}
*/
import "C"

func CheckPlatform() error {
	if C.ng_android_api() < 24 {
		return &PlatformError{Message: "本地 AI 脱敏需要 Android 7（API 24）或更新版本"}
	}
	return nil
}
