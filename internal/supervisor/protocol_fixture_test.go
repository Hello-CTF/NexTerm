package supervisor

// fakeHelloAck is the smallest valid helper acknowledgement for hand-driven
// test servers: every hello acknowledgement carries the daemon activity.
func fakeHelloAck() []byte {
	payload, err := marshalFrame(frameHelloAck, helloMsg{Version: ProtocolVersion, Activity: &ActivitySnapshot{}})
	if err != nil {
		panic(err)
	}
	return payload
}
