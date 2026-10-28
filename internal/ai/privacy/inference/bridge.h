#ifndef NEXTERM_PRIVACY_BRIDGE_H
#define NEXTERM_PRIVACY_BRIDGE_H
#include <stdint.h>
#include <stdlib.h>
typedef struct ng_api ng_api;
int ng_preload_runtime(const char *path);
ng_api *ng_load(const char *path);
int ng_open(ng_api *api, const char *directory, const char *runtime, uint32_t threads, void **handle);
int ng_detect(ng_api *api, void *handle, const uint8_t *text, size_t length, uint8_t **output, size_t *output_length);
void ng_reset(ng_api *api, void *handle);
void ng_cancel(ng_api *api, void *handle);
void ng_free(ng_api *api, uint8_t *data, size_t length);
void ng_close(ng_api *api, void *handle);
const char *ng_self_path(void);
#endif
