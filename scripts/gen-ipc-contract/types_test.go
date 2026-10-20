package main

import (
	"go/token"
	"go/types"
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
