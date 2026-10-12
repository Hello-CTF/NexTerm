#ifndef nexterm_biometric_ios_h
#define nexterm_biometric_ios_h

#include <stdbool.h>

bool nx_biometric_can_evaluate(void);
bool nx_biometric_evaluate(const char *reason, char **err_msg);

bool nx_keychain_set(const char *account, const char *value, char **err_msg);
bool nx_keychain_get(const char *account, char **out_value, char **err_msg);
bool nx_keychain_has(const char *account, char **err_msg);
bool nx_keychain_delete(const char *account, char **err_msg);

#endif
