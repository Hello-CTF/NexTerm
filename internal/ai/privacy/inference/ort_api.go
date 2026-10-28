//go:build (linux || darwin || windows) && (amd64 || arm64) && (cgo || !(android || ios))

// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.
//
// The subset of onnxruntime_c_api.h from ONNX Runtime v1.23.2 that NexTerm
// calls: enum values and OrtApi function-table slots. OrtApi is an
// append-only table of function pointers, so each slot is addressed by its
// index in the v1.23.2 struct declaration and newer runtimes that still
// provide API version 23 keep these indices.

package inference

const ortAPIVersion = 23

// OrtLoggingLevel.
const (
	ortLoggingLevelFatal = 4
)

// ONNXTensorElementDataType.
const (
	ortTensorFloat = 1
	ortTensorInt64 = 7
)

const (
	// ort 2.0.0-rc.11 GraphOptimizationLevel::Level3, as AstrLink configured it.
	ortEnableLayout   = 3
	ortSequential     = 0 // ExecutionMode
	ortArenaAllocator = 1 // OrtAllocatorType
	ortMemTypeDefault = 0 // OrtMemType
)

// OrtApi slot indices (ONNX Runtime v1.23.2).
const (
	slotCreateEnv                      = 3
	slotDisableTelemetryEvents         = 6
	slotCreateSession                  = 7
	slotRun                            = 9
	slotCreateSessionOptions           = 10
	slotSetSessionExecutionMode        = 13
	slotSetSessionLogSeverityLevel     = 22
	slotSetSessionGraphOptimization    = 23
	slotSetIntraOpNumThreads           = 24
	slotSetInterOpNumThreads           = 25
	slotSessionGetInputCount           = 30
	slotSessionGetOutputCount          = 31
	slotSessionGetInputTypeInfo        = 33
	slotSessionGetOutputTypeInfo       = 34
	slotSessionGetInputName            = 36
	slotSessionGetOutputName           = 37
	slotCreateRunOptions               = 39
	slotRunOptionsSetTerminate         = 46
	slotCreateTensorWithDataAsOrtValue = 49
	slotGetTensorMutableData           = 51
	slotCastTypeInfoToTensorInfo       = 55
	slotGetTensorElementType           = 60
	slotGetDimensionsCount             = 61
	slotGetDimensions                  = 62
	slotGetTensorTypeAndShape          = 65
	slotCreateCPUMemoryInfo            = 69
	slotAllocatorFree                  = 76
	slotGetAllocatorWithDefaultOptions = 78
	slotReleaseEnv                     = 92
	slotReleaseStatus                  = 93
	slotReleaseSession                 = 95
	slotReleaseValue                   = 96
	slotReleaseRunOptions              = 97
	slotReleaseTypeInfo                = 98
	slotReleaseTensorTypeAndShapeInfo  = 99
	slotReleaseSessionOptions          = 100
	slotAddSessionConfigEntry          = 130
)
