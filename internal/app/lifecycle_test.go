package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestLifecycleStartsAndStopsInOrder(t *testing.T) {
	var lifecycle Lifecycle
	var calls []string
	component := func(name string) Component {
		return ComponentFuncs{
			StartFunc: func(context.Context) error {
				calls = append(calls, name+":start")
				return nil
			},
			ShutdownFunc: func(context.Context) error {
				calls = append(calls, name+":stop")
				return nil
			},
		}
	}
	if err := lifecycle.Add("a", component("a")); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Add("b", component("b")); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"a:start", "b:start", "b:stop", "a:stop"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestLifecycleRollsBackPartialStartup(t *testing.T) {
	var lifecycle Lifecycle
	failure := errors.New("start failed")
	var calls []string
	if err := lifecycle.Add("a", ComponentFuncs{
		StartFunc: func(context.Context) error {
			calls = append(calls, "a:start")
			return nil
		},
		ShutdownFunc: func(context.Context) error {
			calls = append(calls, "a:stop")
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Add("b", ComponentFuncs{StartFunc: func(context.Context) error {
		calls = append(calls, "b:start")
		return failure
	}}); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Start(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("Start error = %v", err)
	}
	if want := []string{"a:start", "b:start", "a:stop"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	if err := lifecycle.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleRejectsLateOrDuplicateComponents(t *testing.T) {
	var lifecycle Lifecycle
	component := ComponentFuncs{}
	if err := lifecycle.Add("same", component); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Add("same", component); err == nil {
		t.Fatal("duplicate component succeeded")
	}
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Add("late", component); err == nil {
		t.Fatal("late component succeeded")
	}
}
