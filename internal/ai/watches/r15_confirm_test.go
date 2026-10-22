package watches

import "testing"

func TestZeroMinRepeatFiresWhenConfirmed(t *testing.T) {
	cases := []struct {
		pattern   string
		stream    string
		noEOFFrom int64
		noEOFTo   int64
		eofFrom   int64
		eofTo     int64
	}{
		{`ready\bx{0,2}`, "ready ", 0, 5, 0, 5},      // x{0,2} empty; the space confirms mid-buffer
		{`ready$z{0,2}`, "ready ", -1, -1, -1, -1},   // $ refuted by the space
		{`ready\b(?:x{0,2})`, "ready ", 0, 5, 0, 5},  // capture of a nullable repeat stays nullable
		{`ready\bx{0,2}`, "ready", -1, -1, 0, 5},     // deferred, fires at EOF
		{`ready$z{0,2}`, "ready", -1, -1, 0, 5},      // deferred, fires at EOF
		{`ready\b(?:x{0,2})`, "ready", -1, -1, 0, 5}, // deferred, fires at EOF
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
