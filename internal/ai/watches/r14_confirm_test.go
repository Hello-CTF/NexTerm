package watches

import "testing"

func TestNullableConsumingSuffixFiresWhenConfirmed(t *testing.T) {
	cases := []struct {
		pattern   string
		stream    string
		noEOFFrom int64
		noEOFTo   int64
		eofFrom   int64
		eofTo     int64
	}{
		{`ready\b.*`, "ready ", -1, -1, 0, 6},   // .* consumes the space; deferred, fires at EOF
		{`ready\b(?:x)?`, "ready ", 0, 5, 0, 5}, // (?:x)? empty; the space confirms mid-buffer
		{`ready$.*`, "ready ", -1, -1, -1, -1},  // $ refuted by the space
		{`ready\b.*`, "ready", -1, -1, 0, 5},    // deferred, fires at EOF
		{`ready\b(?:x)?`, "ready", -1, -1, 0, 5},
		{`ready$.*`, "ready", -1, -1, 0, 5},
	}
	for _, testCase := range cases {
		t.Run(testCase.pattern+"/"+testCase.stream, func(t *testing.T) {
			condition := Condition{Type: ConditionRegex, Pattern: testCase.pattern}
			observations := []chunkObservation{
				{condition: condition, chunks: stringChunks(testCase.stream), from: testCase.noEOFFrom, to: testCase.noEOFTo},
			}
			for split := 1; split < len(testCase.stream); split++ {
				chunks := stringChunks(testCase.stream[:split], testCase.stream[split:])
				observations = append(observations,
					chunkObservation{condition: condition, chunks: chunks, from: testCase.noEOFFrom, to: testCase.noEOFTo},
					chunkObservation{condition: condition, chunks: chunks, restart: true, from: testCase.noEOFFrom, to: testCase.noEOFTo},
				)
			}
			observations = append(observations, chunkObservation{
				condition: condition, chunks: stringChunks(testCase.stream), eof: true, from: testCase.eofFrom, to: testCase.eofTo,
			})
			assertChunkObservations(t, observations)
		})
	}
}
