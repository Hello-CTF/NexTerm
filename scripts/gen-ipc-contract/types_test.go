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
