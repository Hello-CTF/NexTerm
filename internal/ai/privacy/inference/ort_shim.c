//go:build cgo

#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif
#include "ort_shim.h"

#include <stdlib.h>
#include <string.h>

#include "ort_api.h"

#ifdef _WIN32
#include <windows.h>
#else
#include <dlfcn.h>
#endif
#if defined(__APPLE__)
#include <TargetConditionals.h>
#endif
#if defined(__APPLE__) && TARGET_OS_OSX
#include <pthread.h>
#include <pthread/qos.h>
#endif

static const void *const *ng_api;
static OrtEnv *ng_env;
static OrtMemoryInfo *ng_memory;

#define NG_CALL(slot, type) ((type)ng_api[ORT_SLOT_##slot])

static void ng_release_status(OrtStatus *status) {
  if (status) NG_CALL(ReleaseStatus, void(ORT_API_CALL *)(OrtStatus *))(status);
}

// Returns 1 when the call failed; the status message is discarded.
static int ng_failed(OrtStatus *status) {
  if (!status) return 0;
  ng_release_status(status);
  return 1;
}

static void ORT_API_CALL ng_discard_log(void *param, OrtLoggingLevel severity, const char *category, const char *logid,
                                        const char *code_location, const char *message) {
  (void)param, (void)severity, (void)category, (void)logid, (void)code_location, (void)message;
}

#ifdef _WIN32
static wchar_t *ng_wide(const char *path) {
  int count = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, path, -1, NULL, 0);
  if (!count) return NULL;
  wchar_t *wide = malloc((size_t)count * sizeof(wchar_t));
  if (!wide) return NULL;
  if (!MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, path, -1, wide, count)) {
    free(wide);
    return NULL;
  }
  return wide;
}

// The ONNX Runtime DLL's bundled VC runtime resolves beside the DLL, not
// from the application directory or PATH.
static void *ng_library(const char *path) {
  wchar_t *wide = ng_wide(path);
  if (!wide) return NULL;
  HMODULE handle = LoadLibraryExW(wide, NULL, LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR | LOAD_LIBRARY_SEARCH_DEFAULT_DIRS);
  free(wide);
  return handle;
}
#define NG_SYMBOL(handle, name) ((void *)GetProcAddress((HMODULE)(handle), name))
#else
static void *ng_library(const char *path) { return dlopen(path, RTLD_NOW | RTLD_LOCAL); }
#define NG_SYMBOL(handle, name) dlsym(handle, name)
#endif

// The library stays loaded for the process lifetime: ONNX Runtime owns
// threads and a global environment that must never outlive its code.
int ng_ort_init(const char *path) {
  if (ng_api) return NG_OK;
  void *library = ng_library(path);
  if (!library) return NG_UNAVAILABLE;
  OrtGetApiBaseFn get_base = (OrtGetApiBaseFn)NG_SYMBOL(library, "OrtGetApiBase");
  if (!get_base) return NG_UNAVAILABLE;
  const OrtApiBase *base = get_base();
  if (!base || !base->GetApi) return NG_UNAVAILABLE;
  const void *const *api = (const void *const *)base->GetApi(ORT_API_VERSION);
  if (!api) return NG_UNAVAILABLE;
  ng_api = api;
  OrtEnv *env = NULL;
  if (ng_failed(NG_CALL(CreateEnvWithCustomLogger,
                        OrtStatus *(ORT_API_CALL *)(OrtLoggingFunction, void *, OrtLoggingLevel, const char *,
                                                    OrtEnv **))(ng_discard_log, NULL, ORT_LOGGING_LEVEL_WARNING,
                                                                "nexterm-privacy", &env))) {
    ng_api = NULL;
    return NG_UNAVAILABLE;
  }
  ng_release_status(NG_CALL(DisableTelemetryEvents, OrtStatus *(ORT_API_CALL *)(const OrtEnv *))(env));
  OrtMemoryInfo *memory = NULL;
  if (ng_failed(NG_CALL(CreateCpuMemoryInfo, OrtStatus *(ORT_API_CALL *)(OrtAllocatorType, OrtMemType,
                                                                       OrtMemoryInfo **))(OrtArenaAllocator,
                                                                                          OrtMemTypeDefault, &memory))) {
    NG_CALL(ReleaseEnv, void(ORT_API_CALL *)(OrtEnv *))(env);
    ng_api = NULL;
    return NG_UNAVAILABLE;
  }
  ng_env = env;
  ng_memory = memory;
  return NG_OK;
}

#if defined(__APPLE__) && TARGET_OS_OSX
// macOS starts each new thread at the default QoS class instead of its
// creator's priority, so pool threads lower themselves before taking work:
// relative priority -15 keeps the default class and yields to the user's
// interactive work.
struct ng_thread_start {
  OrtThreadWorkerFn work;
  void *param;
};

static void *ng_thread_main(void *argument) {
  struct ng_thread_start start = *(struct ng_thread_start *)argument;
  free(argument);
  pthread_set_qos_class_self_np(QOS_CLASS_DEFAULT, -15);
  start.work(start.param);
  return NULL;
}

static OrtCustomThreadHandle ng_create_thread(void *options, OrtThreadWorkerFn work, void *param) {
  (void)options;
  struct ng_thread_start *start = malloc(sizeof *start);
  if (!start) return NULL;
  start->work = work;
  start->param = param;
  pthread_attr_t attributes;
  pthread_attr_init(&attributes);
  pthread_attr_setstacksize(&attributes, 2 << 20);
  pthread_t thread;
  int result = pthread_create(&thread, &attributes, ng_thread_main, start);
  pthread_attr_destroy(&attributes);
  if (result != 0) {
    free(start);
    return NULL;
  }
  return (OrtCustomThreadHandle)thread;
}

static void ng_join_thread(OrtCustomThreadHandle handle) { pthread_join((pthread_t)handle, NULL); }
#endif

int ng_ort_session_open(const char *model_path, int threads, void **session) {
  *session = NULL;
  if (!ng_api) return NG_UNAVAILABLE;
  OrtSessionOptions *options = NULL;
  if (ng_failed(NG_CALL(CreateSessionOptions, OrtStatus *(ORT_API_CALL *)(OrtSessionOptions **))(&options)))
    return NG_MODEL;
  int failed = 0;
  failed |= ng_failed(NG_CALL(SetSessionLogSeverityLevel, OrtStatus *(ORT_API_CALL *)(OrtSessionOptions *, int))(
      options, ORT_LOGGING_LEVEL_ERROR));
  failed |= ng_failed(
      NG_CALL(SetIntraOpNumThreads, OrtStatus *(ORT_API_CALL *)(OrtSessionOptions *, int))(options, threads));
  // Without spinning, idle pool threads sleep between operators instead of
  // holding their cores while Go tokenizes and decodes.
  failed |= ng_failed(
      NG_CALL(AddSessionConfigEntry, OrtStatus *(ORT_API_CALL *)(OrtSessionOptions *, const char *, const char *))(
          options, "session.intra_op.allow_spinning", "0"));
  failed |= ng_failed(NG_CALL(SetInterOpNumThreads, OrtStatus *(ORT_API_CALL *)(OrtSessionOptions *, int))(options, 1));
  failed |= ng_failed(NG_CALL(SetSessionExecutionMode, OrtStatus *(ORT_API_CALL *)(OrtSessionOptions *,
                                                                                   ExecutionMode))(options,
                                                                                                   ORT_SEQUENTIAL));
  failed |= ng_failed(
      NG_CALL(SetSessionGraphOptimizationLevel,
              OrtStatus *(ORT_API_CALL *)(OrtSessionOptions *, GraphOptimizationLevel))(options, ORT_ENABLE_ALL));
#if defined(__APPLE__) && TARGET_OS_OSX
  failed |= ng_failed(
      NG_CALL(SessionOptionsSetCustomCreateThreadFn,
              OrtStatus *(ORT_API_CALL *)(OrtSessionOptions *, OrtCustomCreateThreadFn))(options, ng_create_thread));
  failed |= ng_failed(
      NG_CALL(SessionOptionsSetCustomJoinThreadFn,
              OrtStatus *(ORT_API_CALL *)(OrtSessionOptions *, OrtCustomJoinThreadFn))(options, ng_join_thread));
#endif
  OrtSession *created = NULL;
  if (!failed) {
#ifdef _WIN32
    wchar_t *path = ng_wide(model_path);
    if (!path) {
      failed = 1;
    } else {
      failed = ng_failed(NG_CALL(CreateSession, OrtStatus *(ORT_API_CALL *)(const OrtEnv *, const ORTCHAR_T *,
                                                                          const OrtSessionOptions *, OrtSession **))(
          ng_env, path, options, &created));
      free(path);
    }
#else
    failed = ng_failed(NG_CALL(CreateSession, OrtStatus *(ORT_API_CALL *)(const OrtEnv *, const ORTCHAR_T *,
                                                                        const OrtSessionOptions *, OrtSession **))(
        ng_env, model_path, options, &created));
#endif
  }
  NG_CALL(ReleaseSessionOptions, void(ORT_API_CALL *)(OrtSessionOptions *))(options);
  if (failed) return NG_MODEL;
  *session = created;
  return NG_OK;
}

void ng_ort_session_close(void *session) {
  if (session && ng_api) NG_CALL(ReleaseSession, void(ORT_API_CALL *)(OrtSession *))((OrtSession *)session);
}

int ng_ort_session_io_count(void *session, int output, size_t *count) {
  *count = 0;
  OrtStatus *status =
      output ? NG_CALL(SessionGetOutputCount, OrtStatus *(ORT_API_CALL *)(const OrtSession *, size_t *))(session, count)
             : NG_CALL(SessionGetInputCount, OrtStatus *(ORT_API_CALL *)(const OrtSession *, size_t *))(session, count);
  return ng_failed(status) ? NG_MODEL : NG_OK;
}

int ng_ort_session_io(void *session, int output, size_t index, char *name, size_t name_capacity, int *element_type,
                      int64_t *dims, size_t dims_capacity, size_t *dims_count) {
  *element_type = -1;
  *dims_count = 0;
  OrtAllocator *allocator = NULL;
  if (ng_failed(NG_CALL(GetAllocatorWithDefaultOptions, OrtStatus *(ORT_API_CALL *)(OrtAllocator **))(&allocator)))
    return NG_MODEL;
  char *value = NULL;
  OrtStatus *status = output ? NG_CALL(SessionGetOutputName, OrtStatus *(ORT_API_CALL *)(const OrtSession *, size_t,
                                                                                         OrtAllocator *, char **))(
                                   session, index, allocator, &value)
                             : NG_CALL(SessionGetInputName, OrtStatus *(ORT_API_CALL *)(const OrtSession *, size_t,
                                                                                        OrtAllocator *, char **))(
                                   session, index, allocator, &value);
  if (ng_failed(status) || !value) return NG_MODEL;
  size_t length = strlen(value);
  int fits = length < name_capacity;
  if (fits) memcpy(name, value, length + 1);
  ng_release_status(NG_CALL(AllocatorFree, OrtStatus *(ORT_API_CALL *)(OrtAllocator *, void *))(allocator, value));
  if (!fits) return NG_MODEL;
  OrtTypeInfo *type_info = NULL;
  status = output ? NG_CALL(SessionGetOutputTypeInfo,
                            OrtStatus *(ORT_API_CALL *)(const OrtSession *, size_t, OrtTypeInfo **))(session, index,
                                                                                                    &type_info)
                  : NG_CALL(SessionGetInputTypeInfo,
                            OrtStatus *(ORT_API_CALL *)(const OrtSession *, size_t, OrtTypeInfo **))(session, index,
                                                                                                    &type_info);
  if (ng_failed(status)) return NG_MODEL;
  const OrtTensorTypeAndShapeInfo *tensor = NULL;
  int result = NG_OK;
  if (ng_failed(NG_CALL(CastTypeInfoToTensorInfo,
                        OrtStatus *(ORT_API_CALL *)(const OrtTypeInfo *, const OrtTensorTypeAndShapeInfo **))(
          type_info, &tensor))) {
    result = NG_MODEL;
  } else if (tensor) {
    ONNXTensorElementDataType type = ONNX_TENSOR_ELEMENT_DATA_TYPE_UNDEFINED;
    size_t count = 0;
    if (ng_failed(NG_CALL(GetTensorElementType, OrtStatus *(ORT_API_CALL *)(const OrtTensorTypeAndShapeInfo *,
                                                                          ONNXTensorElementDataType *))(tensor,
                                                                                                         &type)) ||
        ng_failed(NG_CALL(GetDimensionsCount,
                          OrtStatus *(ORT_API_CALL *)(const OrtTensorTypeAndShapeInfo *, size_t *))(tensor, &count)) ||
        count > dims_capacity ||
        ng_failed(NG_CALL(GetDimensions, OrtStatus *(ORT_API_CALL *)(const OrtTensorTypeAndShapeInfo *, int64_t *,
                                                                     size_t))(tensor, dims, count))) {
      result = NG_MODEL;
    } else {
      *element_type = (int)type;
      *dims_count = count;
    }
  }
  NG_CALL(ReleaseTypeInfo, void(ORT_API_CALL *)(OrtTypeInfo *))(type_info);
  return result;
}

void *ng_ort_run_options_new(void) {
  OrtRunOptions *options = NULL;
  if (!ng_api || ng_failed(NG_CALL(CreateRunOptions, OrtStatus *(ORT_API_CALL *)(OrtRunOptions **))(&options)))
    return NULL;
  return options;
}

void ng_ort_run_options_terminate(void *options) {
  if (options)
    ng_release_status(
        NG_CALL(RunOptionsSetTerminate, OrtStatus *(ORT_API_CALL *)(OrtRunOptions *))((OrtRunOptions *)options));
}

void ng_ort_run_options_free(void *options) {
  if (options) NG_CALL(ReleaseRunOptions, void(ORT_API_CALL *)(OrtRunOptions *))((OrtRunOptions *)options);
}

static OrtValue *ng_tensor(int64_t *data, int64_t length) {
  int64_t shape[2] = {1, length};
  OrtValue *value = NULL;
  if (ng_failed(NG_CALL(CreateTensorWithDataAsOrtValue,
                        OrtStatus *(ORT_API_CALL *)(const OrtMemoryInfo *, void *, size_t, const int64_t *, size_t,
                                                    ONNXTensorElementDataType, OrtValue **))(
          ng_memory, data, (size_t)length * sizeof(int64_t), shape, 2, ONNX_TENSOR_ELEMENT_DATA_TYPE_INT64, &value)))
    return NULL;
  return value;
}

// Runs one window whose tensors wrap caller memory for the duration of the
// call, and copies float32 logits shaped [1, length, labels] into output.
int ng_ort_run(void *session, void *options, const char *ids_name, const char *mask_name, const char *types_name,
               int64_t *ids, int64_t *mask, int64_t *types, int64_t length, const char *output_name, float *output,
               int64_t labels) {
  if (!ng_api || !session || length <= 0 || labels <= 0) return NG_FAILED;
  const char *names[3] = {ids_name, mask_name, types_name};
  OrtValue *inputs[3] = {ng_tensor(ids, length), ng_tensor(mask, length), types ? ng_tensor(types, length) : NULL};
  size_t count = types ? 3 : 2;
  OrtValue *result = NULL;
  int code = NG_FAILED;
  if (inputs[0] && inputs[1] && (!types || inputs[2])) {
    const char *output_names[1] = {output_name};
    if (!ng_failed(NG_CALL(Run, OrtStatus *(ORT_API_CALL *)(OrtSession *, const OrtRunOptions *, const char *const *,
                                                            const OrtValue *const *, size_t, const char *const *,
                                                            size_t, OrtValue **))(
            (OrtSession *)session, (const OrtRunOptions *)options, names, (const OrtValue *const *)inputs, count,
            output_names, 1, &result)) &&
        result) {
      OrtTensorTypeAndShapeInfo *info = NULL;
      code = NG_SHAPE;
      if (!ng_failed(NG_CALL(GetTensorTypeAndShape,
                             OrtStatus *(ORT_API_CALL *)(const OrtValue *, OrtTensorTypeAndShapeInfo **))(result,
                                                                                                         &info))) {
        ONNXTensorElementDataType type = ONNX_TENSOR_ELEMENT_DATA_TYPE_UNDEFINED;
        size_t dims_count = 0;
        int64_t dims[3] = {0, 0, 0};
        void *data = NULL;
        if (!ng_failed(NG_CALL(GetTensorElementType, OrtStatus *(ORT_API_CALL *)(const OrtTensorTypeAndShapeInfo *,
                                                                               ONNXTensorElementDataType *))(info,
                                                                                                              &type)) &&
            type == ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT &&
            !ng_failed(NG_CALL(GetDimensionsCount,
                               OrtStatus *(ORT_API_CALL *)(const OrtTensorTypeAndShapeInfo *, size_t *))(info,
                                                                                                         &dims_count)) &&
            dims_count == 3 &&
            !ng_failed(NG_CALL(GetDimensions, OrtStatus *(ORT_API_CALL *)(const OrtTensorTypeAndShapeInfo *,
                                                                          int64_t *, size_t))(info, dims, 3)) &&
            dims[0] == 1 && dims[1] == length && dims[2] == labels &&
            !ng_failed(NG_CALL(GetTensorMutableData, OrtStatus *(ORT_API_CALL *)(OrtValue *, void **))(result,
                                                                                                       &data)) &&
            data) {
          memcpy(output, data, (size_t)(length * labels) * sizeof(float));
          code = NG_OK;
        }
        NG_CALL(ReleaseTensorTypeAndShapeInfo, void(ORT_API_CALL *)(OrtTensorTypeAndShapeInfo *))(info);
      }
    }
  }
  if (result) NG_CALL(ReleaseValue, void(ORT_API_CALL *)(OrtValue *))(result);
  for (size_t i = 0; i < 3; i++)
    if (inputs[i]) NG_CALL(ReleaseValue, void(ORT_API_CALL *)(OrtValue *))(inputs[i]);
  return code;
}

const char *ng_self_path(void) {
#ifdef _WIN32
  return NULL;
#else
  Dl_info info;
  if (!dladdr((void *)&ng_self_path, &info)) return NULL;
  return info.dli_fname;
#endif
}
