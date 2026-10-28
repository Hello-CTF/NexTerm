//go:build (linux || darwin || windows) && (amd64 || arm64) && (cgo || !(android || ios))

package inference

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A library the loader rejects reports the loader diagnostic while staying
// ErrUnavailable. It runs before TestRuntimeSmoke loads a real runtime.
func TestRuntimeLoaderErrorIsReported(t *testing.T) {
	onnxRuntime.Lock()
	loaded := onnxRuntime.path != ""
	onnxRuntime.Unlock()
	if loaded {
		t.Skip("a runtime is already loaded in this process")
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, filepath.FromSlash(RuntimeFiles()[0])), []byte("not a library"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := runtimePath(directory)
	if err != nil {
		t.Fatal(err)
	}
	err = initializeRuntime(path)
	if !errors.Is(err, ErrUnavailable) || err.Error() == ErrUnavailable.Error() {
		t.Fatalf("got %v, want ErrUnavailable with loader detail", err)
	}
}

// TestRuntimeSmoke loads a staged ONNX Runtime through purego and runs
// a generated token-classification model end to end, so a wrong OrtApi slot
// or ABI mismatch fails here instead of crashing users. Set
// NEXTERM_PRIVACY_TEST_RUNTIME to the staged ai-runtime directory.
func TestRuntimeSmoke(t *testing.T) {
	runtimeDir := os.Getenv("NEXTERM_PRIVACY_TEST_RUNTIME")
	if runtimeDir == "" {
		t.Skip("runtime path not configured")
	}
	if err := Available(runtimeDir); err != nil {
		t.Fatal(err)
	}
	modelDir := writeSmokeModel(t)
	engine, err := Open(modelDir, runtimeDir, "int8")
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	version := runtimeVersion()
	if version == "" {
		t.Fatal("ONNX Runtime reported no version")
	}
	manifest, err := os.ReadFile(filepath.Join(runtimeDir, "runtime.json"))
	switch {
	case err == nil:
		var staged struct {
			ONNXRuntime string `json:"onnxRuntime"`
		}
		if err := json.Unmarshal(manifest, &staged); err != nil || staged.ONNXRuntime == "" {
			t.Fatalf("runtime.json has no onnxRuntime version: %v", err)
		}
		if version != staged.ONNXRuntime {
			t.Fatalf("loaded ONNX Runtime %s, staged runtime.json declares %s", version, staged.ONNXRuntime)
		}
	case errors.Is(err, os.ErrNotExist) && os.Getenv("CI") == "":
		t.Logf("runtime.json absent; loaded ONNX Runtime %s", version)
	default:
		t.Fatal(err)
	}

	text := "a secret a"
	spans, err := engine.Detect(t.Context(), text)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 1 || spans[0].Label != "email" || strings.TrimSpace(text[spans[0].Start:spans[0].End]) != "secret" {
		t.Fatalf("spans %+v", spans)
	}
	if spans, err := engine.Detect(t.Context(), "a a"); err != nil || len(spans) != 0 {
		t.Fatalf("spans %+v, %v", spans, err)
	}
}

// writeSmokeModel installs the synthetic tokenizer with a generated ONNX
// model whose logits select B-email for token id 5 ("▁secret") and O for
// every other token.
func writeSmokeModel(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	id2label := map[string]string{"0": "O"}
	for i, kind := range guardKinds {
		id2label[itoa(1+2*i)] = "B-" + kind
		id2label[itoa(2+2*i)] = "I-" + kind
	}
	config, err := json.Marshal(map[string]any{"id2label": id2label})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"model_int8.onnx":      smokeModel(1 + 2*len(guardKinds)),
		"model_int8.onnx_data": {0},
		"tokenizer.json":       []byte(syntheticTokenizer),
		"config.json":          config,
	}
	installed := map[string]any{}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		installed[name] = map[string]any{"size": len(data), "sha256": hex.EncodeToString(digest[:])}
	}
	installation, err := json.Marshal(map[string]any{
		"version": "smoke", "revision": strings.Repeat("0", 40), "precision": "int8", "files": installed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "installation.json"), installation, 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func itoa(value int) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// protobuf appends the wire encoding of the few ONNX message fields used here.
type protobuf []byte

func (p protobuf) varint(field int, value uint64) protobuf {
	p = binary.AppendUvarint(p, uint64(field)<<3)
	return binary.AppendUvarint(p, value)
}

func (p protobuf) bytes(field int, value []byte) protobuf {
	p = binary.AppendUvarint(p, uint64(field)<<3|2)
	p = binary.AppendUvarint(p, uint64(len(value)))
	return append(p, value...)
}

func (p protobuf) str(field int, value string) protobuf { return p.bytes(field, []byte(value)) }

const (
	onnxFloat = 1
	onnxInt64 = 7
)

func onnxTensor(name string, dataType int, dims []int64, raw []byte) protobuf {
	var p protobuf
	for _, d := range dims {
		p = p.varint(1, uint64(d))
	}
	return p.varint(2, uint64(dataType)).str(8, name).bytes(9, raw)
}

// onnxValue describes a tensor; a negative dimension is the symbolic "sequence".
func onnxValue(name string, dataType int, dims ...int64) protobuf {
	var shape protobuf
	for _, d := range dims {
		var dim protobuf
		if d < 0 {
			dim = dim.str(2, "sequence")
		} else {
			dim = dim.varint(1, uint64(d))
		}
		shape = shape.bytes(1, dim)
	}
	tensor := protobuf(nil).varint(1, uint64(dataType)).bytes(2, shape)
	return protobuf(nil).str(1, name).bytes(2, protobuf(nil).bytes(1, tensor))
}

func onnxNode(op string, inputs, outputs []string, attributes ...protobuf) protobuf {
	var p protobuf
	for _, input := range inputs {
		p = p.str(1, input)
	}
	for _, output := range outputs {
		p = p.str(2, output)
	}
	p = p.str(3, op+"_"+outputs[0]).str(4, op)
	for _, attribute := range attributes {
		p = p.bytes(5, attribute)
	}
	return p
}

func float32s(values []float32) []byte {
	raw := make([]byte, 0, 4*len(values))
	for _, v := range values {
		raw = binary.LittleEndian.AppendUint32(raw, math.Float32bits(v))
	}
	return raw
}

// smokeModel builds logits = Unsqueeze(Cast(input_ids == 5), 2) * scale + bias,
// an opset 13 graph that also declares the unused mask and type id inputs.
func smokeModel(labels int) []byte {
	scale, bias := make([]float32, labels), make([]float32, labels)
	scale[0], scale[1], bias[0] = -2, 10, 1
	var graph protobuf
	graph = graph.bytes(1, onnxNode("Equal", []string{"input_ids", "target"}, []string{"matched"}))
	graph = graph.bytes(1, onnxNode("Cast", []string{"matched"}, []string{"matched_float"},
		protobuf(nil).str(1, "to").varint(3, onnxFloat).varint(20, 2)))
	graph = graph.bytes(1, onnxNode("Unsqueeze", []string{"matched_float", "axes"}, []string{"column"}))
	graph = graph.bytes(1, onnxNode("Mul", []string{"column", "scale"}, []string{"scaled"}))
	graph = graph.bytes(1, onnxNode("Add", []string{"scaled", "bias"}, []string{"logits"}))
	graph = graph.str(2, "nexterm-privacy-smoke")
	graph = graph.bytes(5, onnxTensor("target", onnxInt64, nil, binary.LittleEndian.AppendUint64(nil, 5)))
	graph = graph.bytes(5, onnxTensor("axes", onnxInt64, []int64{1}, binary.LittleEndian.AppendUint64(nil, 2)))
	graph = graph.bytes(5, onnxTensor("scale", onnxFloat, []int64{int64(labels)}, float32s(scale)))
	graph = graph.bytes(5, onnxTensor("bias", onnxFloat, []int64{int64(labels)}, float32s(bias)))
	for _, input := range []string{"input_ids", "attention_mask", "token_type_ids"} {
		graph = graph.bytes(11, onnxValue(input, onnxInt64, 1, -1))
	}
	graph = graph.bytes(12, onnxValue("logits", onnxFloat, 1, -1, int64(labels)))
	opset := protobuf(nil).str(1, "").varint(2, 13)
	return protobuf(nil).varint(1, 8).str(2, "nexterm").bytes(7, graph).bytes(8, opset)
}
