package inference

import (
	"context"
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

// TestEngineSmoke runs a generated token-classification model end to end
// through the pure-Go backend: parsing, weight loading, compilation with a
// dynamic sequence axis, logits shape checks and decoding.
func TestEngineSmoke(t *testing.T) {
	modelDir := writeSmokeModel(t)
	if _, err := Open(modelDir, "int8"); !errors.Is(err, ErrUnsupportedPrecision) {
		t.Fatalf("int8: got %v, want ErrUnsupportedPrecision", err)
	}
	engine, err := Open(modelDir, Precision)
	if err != nil {
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
	long := strings.Repeat("a ", 1200) + "secret"
	if spans, err := engine.Detect(t.Context(), long); err != nil || len(spans) != 1 || strings.TrimSpace(long[spans[0].Start:spans[0].End]) != "secret" {
		t.Fatalf("multi-window spans %+v, %v", spans, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := engine.Detect(ctx, text); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled detection: %v", err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Detect(t.Context(), text); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed detection: %v", err)
	}
}

// A graph whose output is not the manifest's logits is rejected at Open.
func TestEngineRejectsModelWithoutLogits(t *testing.T) {
	modelDir := writeSmokeModel(t)
	data := smokeModel(1 + 2*len(guardKinds))
	data = []byte(strings.Replace(string(data), "logits", "output", -1))
	if err := os.WriteFile(filepath.Join(modelDir, "model.onnx"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	rewriteSmokeInstallation(t, modelDir)
	if engine, err := Open(modelDir, Precision); err == nil {
		engine.Close()
		t.Fatal("model without logits accepted")
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
		"model.onnx":      smokeModel(1 + 2*len(guardKinds)),
		"model.onnx_data": {0},
		"tokenizer.json":  []byte(syntheticTokenizer),
		"config.json":     config,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rewriteSmokeInstallation(t, directory)
	return directory
}

// rewriteSmokeInstallation records the current smoke files in installation.json.
func rewriteSmokeInstallation(t *testing.T, directory string) {
	t.Helper()
	installed := map[string]any{}
	for _, name := range []string{"model.onnx", "model.onnx_data", "tokenizer.json", "config.json"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		installed[name] = map[string]any{"size": len(data), "sha256": hex.EncodeToString(digest[:])}
	}
	installation, err := json.Marshal(map[string]any{
		"version": "smoke", "revision": strings.Repeat("0", 40), "precision": "fp32", "files": installed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "installation.json"), installation, 0o600); err != nil {
		t.Fatal(err)
	}
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
