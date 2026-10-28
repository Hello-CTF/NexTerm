// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.
//
// Minimal subset of onnxruntime_c_api.h from ONNX Runtime v1.23.2: the opaque
// types, enums, callback types and OrtApi function-table slots NexTerm calls.
// OrtApi is an append-only table of function pointers, so each slot is
// addressed by its index in the v1.23.2 struct declaration.

#ifndef NEXTERM_ORT_API_H
#define NEXTERM_ORT_API_H

#include <stddef.h>
#include <stdint.h>

#ifdef _WIN32
#define ORT_API_CALL _stdcall
#define ORTCHAR_T wchar_t
#else
#define ORT_API_CALL
#define ORTCHAR_T char
#endif

#define ORT_API_VERSION 23

typedef struct OrtApi OrtApi;
typedef struct OrtEnv OrtEnv;
typedef struct OrtStatus OrtStatus;
typedef struct OrtMemoryInfo OrtMemoryInfo;
typedef struct OrtSession OrtSession;
typedef struct OrtValue OrtValue;
typedef struct OrtRunOptions OrtRunOptions;
typedef struct OrtTypeInfo OrtTypeInfo;
typedef struct OrtTensorTypeAndShapeInfo OrtTensorTypeAndShapeInfo;
typedef struct OrtSessionOptions OrtSessionOptions;
typedef struct OrtAllocator OrtAllocator;

typedef enum OrtLoggingLevel {
  ORT_LOGGING_LEVEL_VERBOSE,
  ORT_LOGGING_LEVEL_INFO,
  ORT_LOGGING_LEVEL_WARNING,
  ORT_LOGGING_LEVEL_ERROR,
  ORT_LOGGING_LEVEL_FATAL,
} OrtLoggingLevel;

typedef enum ONNXTensorElementDataType {
  ONNX_TENSOR_ELEMENT_DATA_TYPE_UNDEFINED,
  ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT,
  ONNX_TENSOR_ELEMENT_DATA_TYPE_UINT8,
  ONNX_TENSOR_ELEMENT_DATA_TYPE_INT8,
  ONNX_TENSOR_ELEMENT_DATA_TYPE_UINT16,
  ONNX_TENSOR_ELEMENT_DATA_TYPE_INT16,
  ONNX_TENSOR_ELEMENT_DATA_TYPE_INT32,
  ONNX_TENSOR_ELEMENT_DATA_TYPE_INT64,
} ONNXTensorElementDataType;

// ort 2.0.0-rc.11 GraphOptimizationLevel::Level3, as AstrLink configured it.
typedef enum GraphOptimizationLevel { ORT_ENABLE_LAYOUT = 3 } GraphOptimizationLevel;
typedef enum ExecutionMode { ORT_SEQUENTIAL = 0 } ExecutionMode;
typedef enum OrtAllocatorType { OrtArenaAllocator = 1 } OrtAllocatorType;
typedef enum OrtMemType { OrtMemTypeDefault = 0 } OrtMemType;

typedef void(ORT_API_CALL *OrtLoggingFunction)(void *param, OrtLoggingLevel severity, const char *category,
                                              const char *logid, const char *code_location, const char *message);
typedef void (*OrtThreadWorkerFn)(void *ort_worker_fn_param);
typedef const struct OrtCustomHandleType {
  char __place_holder;
} *OrtCustomThreadHandle;
typedef OrtCustomThreadHandle (*OrtCustomCreateThreadFn)(void *ort_custom_thread_creation_options,
                                                         OrtThreadWorkerFn ort_thread_worker_fn,
                                                         void *ort_worker_fn_param);
typedef void (*OrtCustomJoinThreadFn)(OrtCustomThreadHandle ort_custom_thread_handle);

typedef struct OrtApiBase {
  const OrtApi *(ORT_API_CALL *GetApi)(uint32_t version);
  const char *(ORT_API_CALL *GetVersionString)(void);
} OrtApiBase;

typedef const OrtApiBase *(ORT_API_CALL *OrtGetApiBaseFn)(void);

// OrtApi slot indices (ONNX Runtime v1.23.2).
enum {
  ORT_SLOT_CreateEnvWithCustomLogger = 4,
  ORT_SLOT_DisableTelemetryEvents = 6,
  ORT_SLOT_CreateSession = 7,
  ORT_SLOT_Run = 9,
  ORT_SLOT_CreateSessionOptions = 10,
  ORT_SLOT_SetSessionExecutionMode = 13,
  ORT_SLOT_SetSessionLogSeverityLevel = 22,
  ORT_SLOT_SetSessionGraphOptimizationLevel = 23,
  ORT_SLOT_SetIntraOpNumThreads = 24,
  ORT_SLOT_SetInterOpNumThreads = 25,
  ORT_SLOT_SessionGetInputCount = 30,
  ORT_SLOT_SessionGetOutputCount = 31,
  ORT_SLOT_SessionGetInputTypeInfo = 33,
  ORT_SLOT_SessionGetOutputTypeInfo = 34,
  ORT_SLOT_SessionGetInputName = 36,
  ORT_SLOT_SessionGetOutputName = 37,
  ORT_SLOT_CreateRunOptions = 39,
  ORT_SLOT_RunOptionsSetTerminate = 46,
  ORT_SLOT_CreateTensorWithDataAsOrtValue = 49,
  ORT_SLOT_GetTensorMutableData = 51,
  ORT_SLOT_CastTypeInfoToTensorInfo = 55,
  ORT_SLOT_GetTensorElementType = 60,
  ORT_SLOT_GetDimensionsCount = 61,
  ORT_SLOT_GetDimensions = 62,
  ORT_SLOT_GetTensorTypeAndShape = 65,
  ORT_SLOT_CreateCpuMemoryInfo = 69,
  ORT_SLOT_AllocatorFree = 76,
  ORT_SLOT_GetAllocatorWithDefaultOptions = 78,
  ORT_SLOT_ReleaseEnv = 92,
  ORT_SLOT_ReleaseStatus = 93,
  ORT_SLOT_ReleaseMemoryInfo = 94,
  ORT_SLOT_ReleaseSession = 95,
  ORT_SLOT_ReleaseValue = 96,
  ORT_SLOT_ReleaseRunOptions = 97,
  ORT_SLOT_ReleaseTypeInfo = 98,
  ORT_SLOT_ReleaseTensorTypeAndShapeInfo = 99,
  ORT_SLOT_ReleaseSessionOptions = 100,
  ORT_SLOT_AddSessionConfigEntry = 130,
  ORT_SLOT_SessionOptionsSetCustomCreateThreadFn = 196,
  ORT_SLOT_SessionOptionsSetCustomJoinThreadFn = 198,
};

#endif
