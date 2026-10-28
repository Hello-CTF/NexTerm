//go:build cgo

#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif
#include "bridge.h"
#ifdef _WIN32
#include <windows.h>
static void *ng_library(const char *path) {
    int count = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, path, -1, NULL, 0);
    if (!count) return NULL;
    wchar_t *wide = malloc((size_t)count * sizeof(wchar_t));
    if (!wide) return NULL;
    if (!MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, path, -1, wide, count)) { free(wide); return NULL; }
    HMODULE handle = LoadLibraryExW(wide, NULL, LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR | LOAD_LIBRARY_SEARCH_DEFAULT_DIRS);
    free(wide);
    return handle;
}
#define NG_SYMBOL(handle, name) GetProcAddress((HMODULE)(handle), name)
#define NG_UNLOAD(handle) FreeLibrary((HMODULE)(handle))
#else
#include <dlfcn.h>
static void *ng_library(const char *path) { return dlopen(path, RTLD_NOW | RTLD_LOCAL); }
#define NG_SYMBOL(handle, name) dlsym(handle, name)
#define NG_UNLOAD(handle) dlclose(handle)
#endif

int ng_preload_runtime(const char *path) {
#ifdef _WIN32
    return ng_library(path) != NULL;
#else
    return 1;
#endif
}

struct ng_api {
    void *library;
    int (*open)(const char *, const char *, uint32_t, void **);
    int (*detect)(void *, const uint8_t *, size_t, uint8_t **, size_t *);
    void (*reset)(void *);
    void (*cancel)(void *);
    void (*free)(uint8_t *, size_t);
    void (*close)(void *);
};

ng_api *ng_load(const char *path) {
    void *library = ng_library(path);
    if (!library) return NULL;
    uint32_t (*abi)(void) = (uint32_t (*)(void))NG_SYMBOL(library, "nexterm_privacy_abi");
    if (!abi || abi() != 1) { NG_UNLOAD(library); return NULL; }
    ng_api *api = calloc(1, sizeof(ng_api));
    if (!api) { NG_UNLOAD(library); return NULL; }
    api->library = library;
    api->open = (int (*)(const char *,const char *,uint32_t,void **))NG_SYMBOL(library, "nexterm_privacy_open");
    api->detect = (int (*)(void *,const uint8_t *,size_t,uint8_t **,size_t *))NG_SYMBOL(library, "nexterm_privacy_detect");
    api->reset = (void (*)(void *))NG_SYMBOL(library, "nexterm_privacy_reset");
    api->cancel = (void (*)(void *))NG_SYMBOL(library, "nexterm_privacy_cancel");
    api->free = (void (*)(uint8_t *,size_t))NG_SYMBOL(library, "nexterm_privacy_free");
    api->close = (void (*)(void *))NG_SYMBOL(library, "nexterm_privacy_close");
    if (!api->open || !api->detect || !api->reset || !api->cancel || !api->free || !api->close) { NG_UNLOAD(library); free(api); return NULL; }
    // Kept for the process lifetime: ONNX Runtime owns threads and a global
    // environment whose callbacks must never point into an unloaded library.
    return api;
}
int ng_open(ng_api *api,const char *directory,const char *runtime,uint32_t threads,void **handle) { return api->open(directory,runtime,threads,handle); }
int ng_detect(ng_api *api,void *handle,const uint8_t *text,size_t length,uint8_t **output,size_t *output_length) { return api->detect(handle,text,length,output,output_length); }
void ng_reset(ng_api *api,void *handle) { api->reset(handle); }
void ng_cancel(ng_api *api,void *handle) { api->cancel(handle); }
void ng_free(ng_api *api,uint8_t *data,size_t length) { api->free(data,length); }
void ng_close(ng_api *api,void *handle) { api->close(handle); }
const char *ng_self_path(void) {
#ifdef _WIN32
    return NULL;
#else
    Dl_info info;
    if (!dladdr((void *)&ng_self_path, &info)) return NULL;
    return info.dli_fname;
#endif
}
