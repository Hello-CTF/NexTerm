package session

import (
	"reflect"
	"testing"
)

func TestOutputClassesMergeTrimAndClip(t *testing.T) {
	var classes outputClasses
	classes.add(0, 4, classReplay, "")
	classes.add(4, 8, classReplay, "client-a")
	classes.add(8, 9, "", "client-a")
	classes.add(9, 12, classAnswered, "")
	if got, want := classes.within(0, 12, "client-a"), []classSpan{span(0, 8, classReplay), span(9, 12, classAnswered)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("spans for the answering client = %+v, want %+v", got, want)
	}
	if got, want := classes.within(6, 10, "client-a"), []classSpan{span(6, 8, classReplay), span(9, 10, classAnswered)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("within = %+v, want %+v", got, want)
	}
	// Another client did not answer the live range: it must not answer it
	// when the range reaches it.
	if got, want := classes.within(6, 10, "client-b"), []classSpan{span(6, 8, classReplay), span(8, 10, classAnswered)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("within for another client = %+v, want %+v", got, want)
	}
	classes.trim(9)
	if got, want := classes.within(0, 12, "client-b"), []classSpan{span(9, 12, classAnswered)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("trimmed spans = %+v, want %+v", got, want)
	}
}

// Live output answered by different clients is kept apart, so each client
// learns exactly the ranges another one answered.
func TestOutputClassesTrackTheAnsweringClient(t *testing.T) {
	var classes outputClasses
	classes.add(0, 4, "", "client-a")
	classes.add(4, 6, "", "client-a")
	classes.add(6, 10, "", "client-b")
	if got, want := classes.within(0, 10, "client-a"), []classSpan{span(6, 10, classAnswered)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("client-a = %+v, want %+v", got, want)
	}
	if got, want := classes.within(0, 10, "client-b"), []classSpan{span(0, 6, classAnswered)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("client-b = %+v, want %+v", got, want)
	}
	if len(classes.spans) != 2 {
		t.Fatalf("stored %d spans, want 2", len(classes.spans))
	}
}

// Trimming one span at a time keeps the list bounded without copying it on
// every drop.
func TestOutputClassesTrimCompactsOccasionally(t *testing.T) {
	var classes outputClasses
	for i := 0; i < 1000; i++ {
		kind := classReplay
		if i%2 == 1 {
			kind = classAnswered
		}
		classes.add(uint64(i*2), uint64(i*2+1), kind, "")
		classes.trim(uint64(i*2 - min(i*2, 40)))
		if cap(classes.spans) > 64 {
			t.Fatalf("iteration %d: capacity %d for %d spans", i, cap(classes.spans), len(classes.spans))
		}
	}
	if len(classes.spans) != 21 {
		t.Fatalf("kept %d spans, want 21", len(classes.spans))
	}
}
