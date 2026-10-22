package main

import (
	"go/token"
	"go/types"
	"strings"
	"testing"
)

func TestUnreliableTypesRemainUnknown(t *testing.T) {
	jsonPackage := types.NewPackage("encoding/json", "json")
	rawMessage := types.NewNamed(
		types.NewTypeName(token.NoPos, jsonPackage, "RawMessage", nil),
		types.NewSlice(types.Typ[types.Byte]),
		nil,
	)
	for _, value := range []types.Type{rawMessage, types.Universe.Lookup("any").Type()} {
		reference, err := newTypeRegistry().ref(value)
		if err != nil {
			t.Fatal(err)
		}
		if reference.TS != "unknown" || reference.Manifest != "unknown" {
			t.Fatalf("%s mapped to %+v, want unknown", typeIdentifier(value), reference)
		}
	}
}

func TestTimeTimeMapsToJSONString(t *testing.T) {
	timePackage := types.NewPackage("time", "time")
	timeType := types.NewNamed(
		types.NewTypeName(token.NoPos, timePackage, "Time", nil),
		types.NewStruct(nil, nil),
		nil,
	)
	reference, err := newTypeRegistry().ref(timeType)
	if err != nil {
		t.Fatal(err)
	}
	if reference.TS != "string" || reference.Manifest != "string" {
		t.Fatalf("time.Time mapped to %+v, want string", reference)
	}
}

func TestInvalidSelectedTypeIsRejected(t *testing.T) {
	if _, err := newTypeRegistry().ref(types.Typ[types.Invalid]); err == nil {
		t.Fatal("invalid Go type was silently mapped")
	}
}

func TestEmbeddedStructFieldsRemainTyped(t *testing.T) {
	pkg := types.NewPackage("example.com/sync", "sync")
	link := types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, "Link", nil),
		types.NewStruct([]*types.Var{
			types.NewField(token.NoPos, pkg, "URL", types.Typ[types.String], false),
		}, []string{`json:"url"`}),
		nil,
	)
	linkResult := types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, "LinkResult", nil),
		types.NewStruct([]*types.Var{
			types.NewField(token.NoPos, pkg, "Link", link, true),
			types.NewField(token.NoPos, pkg, "Connect", types.Typ[types.String], false),
			types.NewField(token.NoPos, pkg, "RecoveryKey", types.Typ[types.String], false),
		}, []string{"", `json:"connect"`, `json:"recoveryKey,omitempty"`}),
		nil,
	)
	registry := newTypeRegistry()
	reference, err := registry.ref(linkResult)
	if err != nil {
		t.Fatal(err)
	}
	if reference.TS != "LinkResult" || reference.Manifest != "contractTypes.LinkResult" {
		t.Fatalf("embedded result mapped to %+v", reference)
	}
	rendered := registry.render()
	for _, want := range []string{
		"export type Link = {\n  url: string;\n};",
		"export type LinkResult = Link & {\n  connect: string;\n  recoveryKey?: string;\n};",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("embedded struct declaration is missing %q in:\n%s", want, rendered)
		}
	}
}

func TestOptionalIPCValueMapsToNullableWireValue(t *testing.T) {
	pkg := types.NewPackage("github.com/Hello-CTF/NexTerm/internal/app/production", "production")
	typeParam := types.NewTypeParam(types.NewTypeName(token.NoPos, pkg, "T", nil), types.Universe.Lookup("any").Type())
	optional := types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, "optionalIPCValue", nil),
		types.NewStruct([]*types.Var{
			types.NewField(token.NoPos, pkg, "Set", types.Typ[types.Bool], false),
			types.NewField(token.NoPos, pkg, "Value", types.NewPointer(typeParam), false),
		}, []string{"", ""}),
		nil,
	)
	optional.SetTypeParams([]*types.TypeParam{typeParam})
	instantiated, err := types.Instantiate(nil, optional, []types.Type{types.Typ[types.String]}, false)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := newTypeRegistry().ref(instantiated)
	if err != nil {
		t.Fatal(err)
	}
	if reference.TS != "string | null" || reference.Manifest != "string | null" {
		t.Fatalf("optional IPC value mapped to %+v, want nullable string", reference)
	}
}

func TestUnresolvedEmbeddedPointerRemainsUnknown(t *testing.T) {
	pkg := types.NewPackage("example.com/sync", "sync")
	link := types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, "Link", nil),
		types.NewStruct(nil, nil),
		nil,
	)
	result := types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, "Result", nil),
		types.NewStruct([]*types.Var{
			types.NewField(token.NoPos, pkg, "Link", types.NewPointer(link), true),
		}, []string{""}),
		nil,
	)
	registry := newTypeRegistry()
	reference, err := registry.ref(result)
	if err != nil {
		t.Fatal(err)
	}
	if reference.TS != "Result" {
		t.Fatalf("embedded pointer result mapped to %+v", reference)
	}
	declaration := registry.declarations[typeIdentifier(result)]
	if declaration == nil || declaration.Body != "unknown" {
		t.Fatalf("embedded pointer declaration = %+v, want unknown body", declaration)
	}
}

func TestConsumerCommandInputProjectsDBQueryWithoutLimit(t *testing.T) {
	pkg := types.NewPackage("github.com/Hello-CTF/NexTerm/internal/db", "db")
	request := types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, "queryRequest", nil),
		types.NewStruct([]*types.Var{
			types.NewField(token.NoPos, pkg, "ConnID", types.Typ[types.String], false),
			types.NewField(token.NoPos, pkg, "SQL", types.Typ[types.String], false),
			types.NewField(token.NoPos, pkg, "Limit", types.NewPointer(types.Typ[types.Uint64]), false),
			types.NewField(token.NoPos, pkg, "TimeoutMS", types.NewPointer(types.Typ[types.Uint64]), false),
		}, []string{`json:"connId"`, `json:"sql"`, `json:"limit,omitempty"`, `json:"timeoutMs,omitempty"`}),
		nil,
	)
	projected, err := projectConsumerCommandInput("db_query", request)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := newTypeRegistry().ref(projected)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  connId: string;\n  sql: string;\n  timeoutMs?: number | null;\n}"
	if reference.TS != want {
		t.Fatalf("projected db_query input = %q, want %q", reference.TS, want)
	}
	if strings.Contains(reference.TS, "limit") {
		t.Fatalf("db_query acquired a facade-inaccessible limit: %s", reference.TS)
	}
}

func TestRedisScanResultUsesExactWireTuple(t *testing.T) {
	for _, test := range []struct {
		name        string
		packagePath string
		typeName    string
		tuple       bool
	}{
		{name: "redis scan result", packagePath: "github.com/Hello-CTF/NexTerm/internal/db", typeName: "RedisScanResult", tuple: true},
		{name: "same name elsewhere", packagePath: "example.com/db", typeName: "RedisScanResult"},
		{name: "same package other type", packagePath: "github.com/Hello-CTF/NexTerm/internal/db", typeName: "OtherScanResult"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pkg := types.NewPackage(test.packagePath, "db")
			named := types.NewNamed(
				types.NewTypeName(token.NoPos, pkg, test.typeName, nil),
				types.NewStruct([]*types.Var{
					types.NewField(token.NoPos, pkg, "Cursor", types.Typ[types.Uint64], false),
					types.NewField(token.NoPos, pkg, "Keys", types.NewSlice(types.Typ[types.String]), false),
				}, []string{"", ""}),
				nil,
			)
			registry := newTypeRegistry()
			reference, err := registry.ref(named)
			if err != nil {
				t.Fatal(err)
			}
			if test.tuple {
				if reference.TS != "[number, string[]]" || reference.Manifest != "[number, string[]]" {
					t.Fatalf("RedisScanResult mapped to %+v, want wire tuple", reference)
				}
				if len(registry.declarations) != 0 {
					t.Fatalf("RedisScanResult acquired a struct declaration: %+v", registry.declarations)
				}
			} else if reference.TS == "[number, string[]]" || reference.Manifest == "[number, string[]]" {
				t.Fatalf("unrelated named type acquired the Redis wire tuple: %+v", reference)
			}
		})
	}
}

func TestConsumerCommandInputProjectsOnlyRealFSReadFields(t *testing.T) {
	pkg := types.NewPackage("github.com/Hello-CTF/NexTerm/internal/app/production", "production")
	request := types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, "fsRequest", nil),
		types.NewStruct([]*types.Var{
			types.NewField(token.NoPos, pkg, "SessionID", types.Typ[types.String], false),
			types.NewField(token.NoPos, pkg, "Path", types.Typ[types.String], false),
			types.NewField(token.NoPos, pkg, "MaxBytes", types.Typ[types.Int64], false),
			types.NewField(token.NoPos, pkg, "LocalPath", types.Typ[types.String], false),
			types.NewField(token.NoPos, pkg, "ContentBase64", types.Typ[types.String], false),
		}, []string{`json:"sessionId"`, `json:"path"`, `json:"maxBytes"`, `json:"localPath"`, `json:"contentBase64"`}),
		nil,
	)
	projected, err := projectConsumerCommandInput("fs_read", request)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := newTypeRegistry().ref(projected)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  sessionId: string;\n  path: string;\n  maxBytes?: number;\n}"
	if reference.TS != want {
		t.Fatalf("projected fs_read input = %q, want %q", reference.TS, want)
	}
	if strings.Contains(reference.TS, "localPath") || strings.Contains(reference.TS, "contentBase64") {
		t.Fatalf("fs_read acquired unrelated transfer fields: %s", reference.TS)
	}
	if _, err := projectConsumerCommandInput("fs_read", types.NewStruct(nil, nil)); err == nil {
		t.Fatal("request projection accepted a struct without its declared fields")
	}
}
