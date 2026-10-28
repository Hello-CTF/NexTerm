package inference

// ValidateMetadata applies the load-time checks that depend only on a
// release's config.json and tokenizer.json, so an incompatible release can be
// recognised before its weights are downloaded. It mirrors newDetector.
func ValidateMetadata(config, tokenizerJSON []byte) error {
	t, err := loadTokenizer(tokenizerJSON)
	if err != nil {
		return err
	}
	offsetConsistency, err := configuredContract(config, decoderContractField, decoderContract, "invalid_decoder_contract")
	if err != nil {
		return err
	}
	spanEnabled, err := configuredContract(config, spanContractField, spanContract, "invalid_span_contract")
	if err != nil {
		return err
	}
	mapping := map[string]*string{}
	for _, kind := range guardKinds {
		mapping[kind] = &kind
	}
	d, err := newHFDecoder(config, schemeBIO, mapping)
	if err != nil {
		return err
	}
	if offsetConsistency {
		if err := d.withOffsetConsistency(); err != nil {
			return err
		}
	}
	if spanEnabled {
		if _, err := loadSpanPatterns(); err != nil {
			return err
		}
	}
	if modelWindow-t.specialTokenCount() <= modelStride {
		return errCode("invalid_model_window")
	}
	return nil
}
