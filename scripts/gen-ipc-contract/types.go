package main

import (
	"fmt"
	"go/types"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type typeRef struct {
	Go       string
	TS       string
	Manifest string
}

type tsDeclaration struct {
	Name string
	Body string
}

type typeRegistry struct {
	declarations map[string]*tsDeclaration
	names        map[string]string
}

var tsNameOverrides = map[string]string{
	"github.com/Hello-CTF/NexTerm/internal/app/production.fsCancelRequest": "FSCancelRequest",
	"github.com/Hello-CTF/NexTerm/internal/execbatch.Request":              "ExecBatchRequest",
	"github.com/Hello-CTF/NexTerm/internal/execbatch.Batch":                "ExecBatch",
	"github.com/Hello-CTF/NexTerm/internal/execbatch.Result":               "ExecBatchResult",
	"github.com/Hello-CTF/NexTerm/internal/execbatch.CancelResult":         "ExecBatchCancelResult",
	"github.com/Hello-CTF/NexTerm/internal/forward.Rule":                   "ForwardRule",
	"github.com/Hello-CTF/NexTerm/internal/forward.ruleRequest":            "ForwardRuleRequest",
	"github.com/Hello-CTF/NexTerm/internal/forward.startRuleRequest":       "ForwardRuleStartRequest",
	"github.com/Hello-CTF/NexTerm/internal/store.ForwardRuleInput":         "ForwardRuleInput",
}

func newTypeRegistry() *typeRegistry {
	return &typeRegistry{declarations: make(map[string]*tsDeclaration), names: make(map[string]string)}
}

func (r *typeRegistry) ref(value types.Type) (typeRef, error) {
	ts, _, err := r.mapType(value, "")
	if err != nil {
		return typeRef{}, err
	}
	manifest, _, err := r.mapType(value, "contractTypes.")
	if err != nil {
		return typeRef{}, err
	}
	return typeRef{Go: typeIdentifier(value), TS: ts, Manifest: manifest}, nil
}

func (r *typeRegistry) mapType(value types.Type, namespace string) (string, bool, error) {
	value = types.Unalias(value)
	switch current := value.(type) {
	case *types.Basic:
		if current.Kind() == types.Invalid {
			return "", false, fmt.Errorf("invalid Go type in selected IPC contract")
		}
		return basicType(current), false, nil
	case *types.Pointer:
		mapped, _, err := r.mapType(current.Elem(), namespace)
		return unionWithNull(mapped), false, err
	case *types.Slice:
		if isByteSlice(current) {
			return "string | number[]", false, nil
		}
		mapped, _, err := r.mapType(current.Elem(), namespace)
		return arrayType(mapped), false, err
	case *types.Array:
		mapped, _, err := r.mapType(current.Elem(), namespace)
		return arrayType(mapped), false, err
	case *types.Map:
		key, ok := types.Unalias(current.Key()).(*types.Basic)
		if !ok || key.Kind() != types.String {
			return "unknown", false, nil
		}
		mapped, _, err := r.mapType(current.Elem(), namespace)
		return "Record<string, " + mapped + ">", false, err
	case *types.Struct:
		body, err := r.structBody(current, namespace)
		return body, false, err
	case *types.Named:
		if isRawMessage(current) {
			return "unknown", false, nil
		}
		if structure, ok := types.Unalias(current.Underlying()).(*types.Struct); ok {
			name, err := r.namedStruct(current, structure)
			return namespace + name, true, err
		}
		mapped, _, err := r.mapType(current.Underlying(), namespace)
		return mapped, false, err
	default:
		return "unknown", false, nil
	}
}

func (r *typeRegistry) namedStruct(named *types.Named, structure *types.Struct) (string, error) {
	identifier := typeIdentifier(named)
	if declaration, exists := r.declarations[identifier]; exists {
		return declaration.Name, nil
	}
	name := tsNameOverrides[identifier]
	if name == "" {
		name = exportedName(named.Obj().Name())
	}
	if owner, exists := r.names[name]; exists && owner != identifier {
		return "", fmt.Errorf("TypeScript name %q is used by both %s and %s", name, owner, identifier)
	}
	declaration := &tsDeclaration{Name: name, Body: "unknown"}
	r.declarations[identifier] = declaration
	r.names[name] = identifier
	body, err := r.structBody(structure, "")
	if err != nil {
		return "", err
	}
	declaration.Body = body
	return name, nil
}

func (r *typeRegistry) structBody(structure *types.Struct, namespace string) (string, error) {
	if structure.NumFields() == 0 {
		return "Record<string, never>", nil
	}
	var fields []string
	for index := 0; index < structure.NumFields(); index++ {
		field := structure.Field(index)
		if !field.Exported() {
			continue
		}
		tag := reflect.StructTag(structure.Tag(index)).Get("json")
		if tag == "-" {
			continue
		}
		if field.Embedded() && strings.Split(tag, ",")[0] == "" {
			return "unknown", nil
		}
		parts := strings.Split(tag, ",")
		name := parts[0]
		if name == "" {
			name = field.Name()
		}
		mapped, _, err := r.mapType(field.Type(), namespace)
		if err != nil {
			return "", err
		}
		optional := false
		stringEncoded := false
		for _, option := range parts[1:] {
			switch option {
			case "omitempty", "omitzero":
				optional = true
			case "string":
				stringEncoded = true
			}
		}
		if stringEncoded && (mapped == "number" || mapped == "boolean" || mapped == "string") {
			mapped = "string"
		}
		separator := ":"
		if optional {
			separator = "?:"
		}
		fields = append(fields, "  "+propertyName(name)+separator+" "+mapped+";")
	}
	if len(fields) == 0 {
		return "Record<string, never>", nil
	}
	return "{\n" + strings.Join(fields, "\n") + "\n}", nil
}

func (r *typeRegistry) render() string {
	declarations := make([]*tsDeclaration, 0, len(r.declarations))
	for _, declaration := range r.declarations {
		declarations = append(declarations, declaration)
	}
	sort.Slice(declarations, func(i, j int) bool { return declarations[i].Name < declarations[j].Name })
	var output strings.Builder
	output.WriteString("// Code generated by go run ./scripts/gen-ipc-contract; DO NOT EDIT.\n\n")
	for _, declaration := range declarations {
		fmt.Fprintf(&output, "export type %s = %s;\n\n", declaration.Name, declaration.Body)
	}
	return strings.TrimRight(output.String(), "\n") + "\n"
}

func typeIdentifier(value types.Type) string {
	return types.TypeString(types.Unalias(value), func(pkg *types.Package) string {
		if pkg == nil {
			return ""
		}
		return pkg.Path()
	})
}

func basicType(value *types.Basic) string {
	switch value.Kind() {
	case types.Bool:
		return "boolean"
	case types.String:
		return "string"
	case types.Int, types.Int8, types.Int16, types.Int32, types.Int64,
		types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64, types.Uintptr,
		types.Float32, types.Float64:
		return "number"
	default:
		return "unknown"
	}
}

func isRawMessage(named *types.Named) bool {
	return named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "encoding/json" && named.Obj().Name() == "RawMessage"
}

func isByteSlice(slice *types.Slice) bool {
	element, ok := types.Unalias(slice.Elem()).(*types.Basic)
	return ok && element.Kind() == types.Uint8
}

func arrayType(element string) string {
	if strings.Contains(element, " | ") {
		return "(" + element + ")[]"
	}
	return element + "[]"
}

func unionWithNull(value string) string {
	if value == "unknown" || value == "null" {
		return value
	}
	return value + " | null"
}

func exportedName(value string) string {
	for index, character := range value {
		return string(unicode.ToUpper(character)) + value[index+len(string(character)):]
	}
	return "Anonymous"
}

func propertyName(value string) string {
	if value != "" && (value[0] == '_' || value[0] == '$' || unicode.IsLetter(rune(value[0]))) {
		valid := true
		for _, character := range value[1:] {
			if character != '_' && character != '$' && !unicode.IsLetter(character) && !unicode.IsDigit(character) {
				valid = false
				break
			}
		}
		if valid {
			return value
		}
	}
	return strconv.Quote(value)
}
