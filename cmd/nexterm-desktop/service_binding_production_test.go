//go:build production

package main

import (
	"go/types"
	"hash/fnv"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestServiceBindingFQNMatchesDesktopRuntime(t *testing.T) {
	loaded, err := packages.Load(&packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax}, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || len(loaded[0].Errors) > 0 {
		t.Fatalf("load desktop service: %v", loaded)
	}
	object, ok := loaded[0].Types.Scope().Lookup("Service").(*types.TypeName)
	if !ok {
		t.Fatal("desktop Service type is missing")
	}
	for _, test := range []struct {
		method string
		fqn    string
		id     uint32
	}{
		{method: "Call", fqn: "main.Service.Call", id: 2349242617},
		{method: "ConsumeDeepLinks", fqn: "main.Service.ConsumeDeepLinks", id: 3490772380},
	} {
		selection := types.NewMethodSet(types.NewPointer(object.Type())).Lookup(object.Pkg(), test.method)
		if selection == nil {
			t.Fatalf("Service.%s is missing", test.method)
		}
		path := object.Pkg().Path()
		if object.Pkg().Name() == "main" {
			path = "main"
		}
		fqn := path + "." + object.Name() + "." + selection.Obj().Name()
		if fqn != test.fqn {
			t.Fatalf("bound FQN = %q, want %q", fqn, test.fqn)
		}
		hash := fnv.New32a()
		_, _ = hash.Write([]byte(fqn))
		if id := hash.Sum32(); id != test.id {
			t.Fatalf("generated binding ID for %q = %d, want %d", fqn, id, test.id)
		}
	}
}
