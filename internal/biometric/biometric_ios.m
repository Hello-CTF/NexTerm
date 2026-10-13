// iOS 生物识别(LAContext)与 Keychain 桥接; 由 biometric_ios.go 经 cgo 调用。
#import <Foundation/Foundation.h>
#import <LocalAuthentication/LocalAuthentication.h>
#import <Security/Security.h>
#import <dispatch/dispatch.h>

#include "biometric_ios.h"

static char *nxDup(NSString *s) {
    return s ? strdup(s.UTF8String) : NULL;
}

static NSString *nxBiometricMessage(NSError *error) {
    if (!error) return @"面容验证未通过";
    switch (error.code) {
        case LAErrorUserCancel:
        case LAErrorSystemCancel:
        case LAErrorAppCancel:
            return @"已取消面容验证";
        case LAErrorBiometryLockout:
            return @"面容验证已锁定，请使用密码解锁";
        default:
            return error.localizedDescription ?: @"面容验证未通过";
    }
}

bool nx_biometric_can_evaluate(void) {
    LAContext *ctx = [[LAContext alloc] init];
    return [ctx canEvaluatePolicy:LAPolicyDeviceOwnerAuthenticationWithBiometrics error:nil];
}

// evaluatePolicy 的回调在私有队列触发, 用信号量把异步结果同步回 Go 调用方。
bool nx_biometric_evaluate(const char *creason, char **err_msg) {
    NSString *reason = creason ? [NSString stringWithUTF8String:creason] : @"Authenticate";
    LAContext *ctx = [[LAContext alloc] init];
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    __block BOOL ok = NO;
    __block NSString *message = nil;
    [ctx evaluatePolicy:LAPolicyDeviceOwnerAuthenticationWithBiometrics
         localizedReason:reason
                   reply:^(BOOL success, NSError *error) {
        ok = success;
        if (!success) message = nxBiometricMessage(error);
        dispatch_semaphore_signal(sem);
    }];
    dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
    if (!ok && err_msg) *err_msg = nxDup(message ?: @"面容验证未通过");
    return ok;
}

static NSString *nxKeychainService(void) {
    return [NSBundle mainBundle].bundleIdentifier ?: @"com.nexterm.desktop";
}

static NSMutableDictionary *nxKeychainQuery(NSString *account) {
    return [@{
        (__bridge id)kSecClass: (__bridge id)kSecClassGenericPassword,
        (__bridge id)kSecAttrService: nxKeychainService(),
        (__bridge id)kSecAttrAccount: account,
    } mutableCopy];
}

static char *nxKeychainStatusError(OSStatus status, NSString *action) {
    NSString *message;
    switch (status) {
        case errSecUserCanceled:
            message = @"已取消面容验证";
            break;
        case errSecInvalidOwnerEdit:
        case errSecAuthFailed:
            // 面容集变更(新增/删除面容)使 BiometryCurrentSet 约束失效, 按验证失败处理
            message = @"面容验证未通过，请使用密码解锁";
            break;
        default:
            message = [NSString stringWithFormat:@"%@失败(钥匙串状态 %d)", action, (int)status];
            break;
    }
    return nxDup(message);
}

// 写入统一走"先删后建": item 携带绑定当前面容集的 SecAccessControl,
// 新增/删除面容后旧材料即刻不可读; 约束与可访问性只能经 access control 表达,
// 同设 kSecAttrAccessible 会报 errSecParam。
bool nx_keychain_set(const char *caccount, const char *cvalue, char **err_msg) {
    NSString *account = caccount ? [NSString stringWithUTF8String:caccount] : @"";
    NSString *value = cvalue ? [NSString stringWithUTF8String:cvalue] : @"";
    if (account.length == 0 || value.length == 0) {
        if (err_msg) *err_msg = nxDup(@"解锁材料为空");
        return false;
    }
    NSData *data = [value dataUsingEncoding:NSUTF8StringEncoding];
    CFErrorRef cfError = NULL;
    SecAccessControlRef acl = SecAccessControlCreateWithFlags(
        kCFAllocatorDefault,
        kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
        kSecAccessControlBiometryCurrentSet,
        &cfError);
    if (acl == NULL) {
        if (cfError) CFRelease(cfError);
        if (err_msg) *err_msg = nxDup(@"创建钥匙串访问控制失败");
        return false;
    }
    NSMutableDictionary *query = nxKeychainQuery(account);
    SecItemDelete((__bridge CFDictionaryRef)query);
    query[(__bridge id)kSecValueData] = data;
    query[(__bridge id)kSecAttrAccessControl] = (__bridge id)acl;
    OSStatus status = SecItemAdd((__bridge CFDictionaryRef)query, NULL);
    CFRelease(acl);
    if (status != errSecSuccess) {
        if (err_msg) *err_msg = nxKeychainStatusError(status, @"保存解锁材料");
        return false;
    }
    return true;
}

int nx_keychain_state(const char *caccount, char **err_msg) {
    NSString *account = caccount ? [NSString stringWithUTF8String:caccount] : @"";
    NSMutableDictionary *query = nxKeychainQuery(account);
    query[(__bridge id)kSecReturnAttributes] = @YES;
    query[(__bridge id)kSecMatchLimit] = (__bridge id)kSecMatchLimitOne;
    CFTypeRef result = NULL;
    OSStatus status = SecItemCopyMatching((__bridge CFDictionaryRef)query, &result);
    if (status == errSecItemNotFound) return 0;
    if (status != errSecSuccess) {
        if (err_msg) *err_msg = nxKeychainStatusError(status, @"检查解锁材料");
        return 0;
    }
    NSDictionary *attrs = (__bridge_transfer NSDictionary *)result;
    return attrs[(__bridge id)kSecAttrAccessControl] != nil ? 2 : 1;
}

// 读取受 ACL 保护的 item 时系统自行弹出面容验证: 传入未评估的 LAContext,
// 由钥匙串按需评估并以 localizedReason 作为弹窗用途说明;
// 存量无 ACL item 不弹窗(调用方需先经 LAContext 显式验证)。
bool nx_keychain_get(const char *caccount, const char *cprompt, char **out_value, char **err_msg) {
    NSString *account = caccount ? [NSString stringWithUTF8String:caccount] : @"";
    NSMutableDictionary *query = nxKeychainQuery(account);
    query[(__bridge id)kSecReturnData] = @YES;
    query[(__bridge id)kSecMatchLimit] = (__bridge id)kSecMatchLimitOne;
    if (cprompt) {
        LAContext *ctx = [[LAContext alloc] init];
        ctx.localizedReason = [NSString stringWithUTF8String:cprompt];
        query[(__bridge id)kSecUseAuthenticationContext] = ctx;
    }
    CFTypeRef result = NULL;
    OSStatus status = SecItemCopyMatching((__bridge CFDictionaryRef)query, &result);
    if (status == errSecItemNotFound) return false;
    if (status != errSecSuccess) {
        if (err_msg) *err_msg = nxKeychainStatusError(status, @"读取解锁材料");
        return false;
    }
    NSData *data = (__bridge_transfer NSData *)result;
    NSString *s = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
    if (out_value) *out_value = nxDup(s ?: @"");
    return true;
}

bool nx_keychain_delete(const char *caccount, char **err_msg) {
    NSString *account = caccount ? [NSString stringWithUTF8String:caccount] : @"";
    OSStatus status = SecItemDelete((__bridge CFDictionaryRef)nxKeychainQuery(account));
    if (status != errSecSuccess && status != errSecItemNotFound) {
        if (err_msg) *err_msg = nxKeychainStatusError(status, @"删除解锁材料");
        return false;
    }
    return true;
}
