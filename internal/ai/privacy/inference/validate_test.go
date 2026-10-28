package inference

import "testing"

func TestValidateMetadata(t *testing.T) {
	config := labelConfig(t, guardLabels(), nil)
	if err := ValidateMetadata(config, []byte(syntheticTokenizer)); err != nil {
		t.Fatal(err)
	}
	if ValidateMetadata(labelConfig(t, []string{"O", "B-unknown", "I-unknown"}, nil), []byte(syntheticTokenizer)) == nil {
		t.Fatal("labels outside the known set accepted")
	}
	if ValidateMetadata(config, []byte(`{}`)) == nil {
		t.Fatal("invalid tokenizer accepted")
	}
}
