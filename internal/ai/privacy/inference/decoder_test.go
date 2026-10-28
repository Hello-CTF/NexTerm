package inference

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
)

func labelConfig(t *testing.T, labels []string, extra map[string]any) []byte {
	t.Helper()
	id2label := map[string]string{}
	for i, label := range labels {
		id2label[strconv.Itoa(i)] = label
	}
	document := map[string]any{"id2label": id2label}
	for key, value := range extra {
		document[key] = value
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func ptr(value string) *string { return &value }

func genericDecoder(t *testing.T, scheme tagScheme, labels []string) *decoder {
	t.Helper()
	d, err := newHFDecoder(labelConfig(t, labels, nil), scheme, map[string]*string{"EMAIL": ptr("email"), "IGNORED": nil})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func filled(count int, value float32) []float32 {
	values := make([]float32, count)
	for i := range values {
		values[i] = value
	}
	return values
}

func TestGenericBIOMapsSourceLabelsToCanonicalKinds(t *testing.T) {
	d := genericDecoder(t, schemeBIO, []string{"O", "B_EMAIL", "I_EMAIL", "B_IGNORED", "I_IGNORED"})
	count := d.labelCount()
	logits := filled(3*count, -10)
	logits[1], logits[count+2], logits[2*count] = 10, 10, 10
	spans, err := d.decode(logits, [][2]int{{0, 2}, {2, 4}, {4, 6}})
	if err != nil || len(spans) != 1 || spans[0].Label != "email" || spans[0].Start != 0 || spans[0].End != 4 {
		t.Fatalf("got %+v %v", spans, err)
	}
}

func TestGenericBIOESIgnoresNullMappedEntity(t *testing.T) {
	d := genericDecoder(t, schemeBIOES, []string{"O", "B-EMAIL", "I-EMAIL", "E-EMAIL", "S-EMAIL", "B-IGNORED", "I-IGNORED", "E-IGNORED", "S-IGNORED"})
	logits := filled(d.labelCount(), -10)
	logits[8] = 10
	if spans, err := d.decode(logits, [][2]int{{0, 4}}); err != nil || len(spans) != 0 {
		t.Fatalf("got %+v %v", spans, err)
	}
}

func TestDecoderRejectsIncompleteMappingAndSchemeMismatch(t *testing.T) {
	mapping := map[string]*string{"EMAIL": ptr("email")}
	if _, err := newHFDecoder(labelConfig(t, []string{"O", "B-EMAIL", "I-EMAIL", "B-PHONE", "I-PHONE"}, nil), schemeBIO, mapping); err == nil || err.Error() != "invalid_model_mapping" {
		t.Fatalf("got %v", err)
	}
	if _, err := newHFDecoder(labelConfig(t, []string{"O", "B-EMAIL", "I-EMAIL", "E-EMAIL", "S-EMAIL"}, nil), schemeBIO, mapping); err == nil || err.Error() != "invalid_model_labels" {
		t.Fatalf("got %v", err)
	}
	if _, err := newHFDecoder([]byte(`{}`), schemeBIO, mapping); err == nil || err.Error() != "invalid_model_config" {
		t.Fatalf("got %v", err)
	}
	if _, err := newHFDecoder(labelConfig(t, []string{"O", "B-EMAIL", "I-EMAIL"}, nil), schemeBIO, map[string]*string{"EMAIL": ptr("private_email")}); err == nil {
		t.Fatal("non-canonical kind accepted")
	}
}

func guardLabels() []string {
	labels := []string{"O"}
	for _, kind := range guardKinds {
		labels = append(labels, "B-"+kind, "I-"+kind)
	}
	return labels
}

func guardMapping() map[string]*string {
	mapping := map[string]*string{}
	for _, kind := range guardKinds {
		mapping[kind] = ptr(kind)
	}
	return mapping
}

func guardDecoder(t *testing.T, contract bool) *decoder {
	t.Helper()
	d, err := newHFDecoder(labelConfig(t, guardLabels(), nil), schemeBIO, guardMapping())
	if err != nil {
		t.Fatal(err)
	}
	if contract {
		if err := d.withOffsetConsistency(); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestOffsetContractConfigurationIsOptionalAndStrict(t *testing.T) {
	for _, tc := range []struct {
		config string
		want   bool
		err    string
	}{
		{`{"id2label":{}}`, false, ""},
		{`{"astrlink_guard_decoder_contract":null}`, false, ""},
		{`{"astrlink_guard_decoder_contract":"bio-offset-consistency-v1"}`, true, ""},
		{`{"astrlink_guard_decoder_contract":"bio-offset-consistency-v2"}`, false, "invalid_decoder_contract"},
		{`{"astrlink_guard_decoder_contract":""}`, false, "invalid_decoder_contract"},
		{`{"astrlink_guard_decoder_contract":true}`, false, "invalid_decoder_contract"},
		{`{"astrlink_guard_decoder_contract":["bio-offset-consistency-v1"]}`, false, "invalid_decoder_contract"},
		{`not json`, false, "invalid_model_config"},
	} {
		got, err := configuredContract([]byte(tc.config), decoderContractField, decoderContract, "invalid_decoder_contract")
		if got != tc.want || (err == nil) != (tc.err == "") || (err != nil && err.Error() != tc.err) {
			t.Errorf("%s: got %v %v", tc.config, got, err)
		}
	}
}

func TestOffsetContractCorrectsSharedCharacter(t *testing.T) {
	scores := filled(42, -40)
	scores[13], scores[21], scores[21+14] = 10, 4, 3
	offsets := [][2]int{{0, 1}, {0, 3}}
	legacy, err := guardDecoder(t, false).decode(scores, offsets)
	if err != nil || legacy[0].End != 1 {
		t.Fatalf("legacy: %+v %v", legacy, err)
	}
	consistent, err := guardDecoder(t, true).decode(scores, offsets)
	if err != nil || consistent[0].End != 3 {
		t.Fatalf("contract: %+v %v", consistent, err)
	}
}

func TestOffsetContractStrongBackgroundRemovesFragment(t *testing.T) {
	scores := filled(42, -40)
	scores[0], scores[13], scores[21], scores[21+14] = 0, 2, 10, 0
	if spans, err := guardDecoder(t, true).decode(scores, [][2]int{{0, 1}, {0, 4}}); err != nil || len(spans) != 0 {
		t.Fatalf("got %+v %v", spans, err)
	}
}

func TestOffsetContractRejectsInvalidOffsetsAndLogits(t *testing.T) {
	d := guardDecoder(t, true)
	if _, err := d.decode(filled(63, 0), [][2]int{{0, 3}, {1, 1}, {2, 3}}); err == nil || err.Error() != "invalid_offsets" {
		t.Fatalf("empty offset: %v", err)
	}
	if _, err := d.decode(filled(42, 0), [][2]int{{2, 3}, {1, 4}}); err == nil || err.Error() != "invalid_offsets" {
		t.Fatalf("non-monotonic offset: %v", err)
	}
	if _, err := d.decode(filled(21, float32(math.NaN())), [][2]int{{0, 1}}); err == nil {
		t.Fatal("NaN accepted")
	}
}

func TestOffsetContractUnionsNestedEndsAndSeparatesAdjacentEntities(t *testing.T) {
	scores := filled(84, -40)
	for token, label := range []int{5, 6, 6, 5} {
		scores[token*21+label] = 10
	}
	spans, err := guardDecoder(t, true).decode(scores, [][2]int{{0, 4}, {1, 2}, {3, 4}, {4, 5}})
	if err != nil || len(spans) != 2 || spans[0] != (Span{0, 4, "account", spans[0].Score}) || spans[1] != (Span{4, 5, "account", spans[1].Score}) {
		t.Fatalf("got %+v %v", spans, err)
	}
}

func TestOffsetContractScoreIsMeanAndFinalTiesTakeLastLabel(t *testing.T) {
	scores := filled(42, -40)
	scores[1], scores[21], scores[21+2] = 0, 0, 0
	spans, err := guardDecoder(t, true).decode(scores, [][2]int{{0, 2}, {2, 4}})
	if err != nil || len(spans) != 1 || spans[0].Start != 0 || spans[0].End != 4 || spans[0].Label != "email" || math.Abs(float64(spans[0].Score)-0.75) > 1e-6 {
		t.Fatalf("got %+v %v", spans, err)
	}
}

func TestOffsetContractRejectsBIOESAndChangedLabelMap(t *testing.T) {
	bioes, err := newHFDecoder(labelConfig(t, []string{"O", "B-email", "I-email", "E-email", "S-email"}, nil), schemeBIOES, map[string]*string{"email": ptr("email")})
	if err != nil {
		t.Fatal(err)
	}
	if err := bioes.withOffsetConsistency(); err == nil || err.Error() != "invalid_decoder_contract" {
		t.Fatalf("BIOES: %v", err)
	}
	for _, change := range []func(map[string]*string){
		func(m map[string]*string) { m["common_secret"] = nil },
		func(m map[string]*string) { m["email"] = ptr("url") },
	} {
		mapping := guardMapping()
		change(mapping)
		d, err := newHFDecoder(labelConfig(t, guardLabels(), nil), schemeBIO, mapping)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.withOffsetConsistency(); err == nil || err.Error() != "invalid_decoder_contract" {
			t.Fatalf("changed mapping: %v", err)
		}
	}
}
