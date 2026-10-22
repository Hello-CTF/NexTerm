package watches

import "testing"

func TestZeroWidthBeginAssertionDefersRightEdge(t *testing.T) {
	cases := []struct {
		pattern string
		stream  string
	}{
		{`(?m)ready\n$^`, "ready\ny"},
		{`(?m)ready\n$^x?`, "ready\ny"},
		{`(?m)ready\n$^x{0,2}`, "ready\ny"},
		{`ready\b(\b){1,2}`, "readyx"},
		{`ready\b(x{0,0})`, "readyx"},
	}
	for _, testCase := range cases {
		t.Run(testCase.pattern, func(t *testing.T) {
			condition := Condition{Type: ConditionRegex, Pattern: testCase.pattern}
			observations := []chunkObservation{
				{condition: condition, chunks: stringChunks(testCase.stream), from: -1, to: -1},
				{condition: condition, chunks: stringChunks(testCase.stream), eof: true, from: -1, to: -1},
			}
			// Every split point defers and is then refuted: no fire, even
			// across a restart and with EOF.
			for split := 1; split < len(testCase.stream); split++ {
				chunks := stringChunks(testCase.stream[:split], testCase.stream[split:])
				observations = append(observations,
					chunkObservation{condition: condition, chunks: chunks, from: -1, to: -1},
					chunkObservation{condition: condition, chunks: chunks, restart: true, from: -1, to: -1},
					chunkObservation{condition: condition, chunks: chunks, restart: true, eof: true, from: -1, to: -1},
				)
			}
			assertChunkObservations(t, observations)
		})
	}
}
