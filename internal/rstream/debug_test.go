package rstream

import "fmt"

func (s *Stream) debugState() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("err=%v carrier=%v closing=%v finSent=%v finDelivered=%v peerFin=%v acked=%d written=%d sendNext=%d received=%d consumed=%d ackC=%d ackR=%d needAck=%v lost=%v migrating=%v recv=%d",
		s.err, s.carrier != nil, s.closing, s.finSent, s.finDelivered, s.peerFin, s.acked, s.written, s.sendNext, s.received, s.consumed, s.ackConsumed, s.ackReceived, s.needAck, s.lostPending, s.migrating, len(s.recv))
}
