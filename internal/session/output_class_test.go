package session

import (
	"reflect"
	"testing"
)

func TestOutputClassesMergeTrimAndClip(t *testing.T) {
	var classes outputClasses
	classes.add(0, 4, classReplay)
	classes.add(4, 8, classReplay)
	classes.add(8, 9, "")
	classes.add(9, 12, classAnswered)
	if want := []classSpan{span(0, 8, classReplay), span(9, 12, classAnswered)}; !reflect.DeepEqual(classes.spans, want) {
		t.Fatalf("spans = %+v, want %+v", classes.spans, want)
	}
	if got, want := classes.within(6, 10), []classSpan{span(6, 8, classReplay), span(9, 10, classAnswered)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("within = %+v, want %+v", got, want)
	}
	classes.trim(8)
	if want := []classSpan{span(9, 12, classAnswered)}; !reflect.DeepEqual(classes.spans, want) {
		t.Fatalf("trimmed spans = %+v, want %+v", classes.spans, want)
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
		classes.add(uint64(i*2), uint64(i*2+1), kind)
		classes.trim(uint64(i*2 - min(i*2, 40)))
		if cap(classes.spans) > 64 {
			t.Fatalf("iteration %d: capacity %d for %d spans", i, cap(classes.spans), len(classes.spans))
		}
	}
	if len(classes.spans) != 21 {
		t.Fatalf("kept %d spans, want 21", len(classes.spans))
	}
}
