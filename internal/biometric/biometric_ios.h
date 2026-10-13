#ifndef nexterm_biometric_ios_h
#define nexterm_biometric_ios_h

#include <stdbool.h>

bool nx_biometric_can_evaluate(void);
bool nx_biometric_evaluate(const char *reason, char **err_msg);

// 材料状态: 0 不存在, 1 存量(无访问控制), 2 受 SecAccessControl 保护
int nx_keychain_state(const char *account, char **err_msg);
bool nx_keychain_set(const char *account, const char *value, char **err_msg);
bool nx_keychain_get(const char *account, const char *prompt, char **out_value, char **err_msg);
bool nx_keychain_delete(const char *account, char **err_msg);

#endif
