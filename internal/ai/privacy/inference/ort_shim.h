#ifndef NEXTERM_ORT_SHIM_H
#define NEXTERM_ORT_SHIM_H

#include <stddef.h>
#include <stdint.h>

// Status codes are content free: ONNX Runtime messages never cross into Go.
enum {
  NG_OK = 0,
  NG_UNAVAILABLE = 2,
  NG_MODEL = 3,
  NG_FAILED = 5,
  NG_SHAPE = 7,
};

int ng_ort_init(const char *path);
int ng_ort_session_open(const char *model_path, int threads, void **session);
void ng_ort_session_close(void *session);
int ng_ort_session_io_count(void *session, int output, size_t *count);
int ng_ort_session_io(void *session, int output, size_t index, char *name, size_t name_capacity, int *element_type,
                      int64_t *dims, size_t dims_capacity, size_t *dims_count);
void *ng_ort_run_options_new(void);
void ng_ort_run_options_terminate(void *options);
void ng_ort_run_options_free(void *options);
int ng_ort_run(void *session, void *options, const char *ids_name, const char *mask_name, const char *types_name,
               int64_t *ids, int64_t *mask, int64_t *types, int64_t length, const char *output_name, float *output,
               int64_t labels);
const char *ng_self_path(void);

#endif
