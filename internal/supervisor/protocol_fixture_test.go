package supervisor

// fakeGeneration is the helper generation hand-driven test servers report.
const fakeGeneration = "fake-generation"

// inputData strips the expectation header of a frameInput payload.
func inputData(payload []byte) []byte {
	_, _, data, err := parseInput(payload)
	if err != nil {
		panic(err)
	}
	return data
}

// fakeHelloAck is the smallest valid helper acknowledgement for hand-driven
// test servers: every hello acknowledgement carries the daemon activity and
// the state rule revision.
func fakeHelloAck() []byte {
	payload, err := marshalFrame(frameHelloAck, helloMsg{Version: ProtocolVersion, Generation: fakeGeneration, Activity: &ActivitySnapshot{}, StateRules: &stateRulesAck{}})
	if err != nil {
		panic(err)
	}
	return payload
}
