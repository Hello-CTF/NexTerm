package watches

import "testing"

func TestZeroWidthBeginAssertionFiresWhenConfirmed(t *testing.T) {
	cases := []struct {
		pattern   string
		stream    string
		noEOFFrom int64
		noEOFTo   int64
		eofFrom   int64
		eofTo     int64
	}{
		{`(?m)ready\n$^`, "ready\n\n", 0, 6, 0, 6},       // the second \n confirms $ and ^ mid-buffer
		{`(?m)ready\n$^x?`, "ready\n\n", 0, 6, 0, 6},     // x? empty; confirmed mid-buffer
		{`(?m)ready\n$^x{0,2}`, "ready\n\n", 0, 6, 0, 6}, // x{0,2} empty; confirmed mid-buffer
		{`(?m)ready\n$^`, "ready\n", -1, -1, 0, 6},       // deferred, fires at EOF
		{`(?m)ready\n$^x?`, "ready\n", -1, -1, 0, 6},     // deferred, fires at EOF
		{`(?m)ready\n$^x{0,2}`, "ready\n", -1, -1, 0, 6}, // deferred, fires at EOF
		{`ready\b(\b){1,2}`, "ready", -1, -1, 0, 5},      // Min>0 over a nullable assertion still defers
		{`ready\b(x{0,0})`, "ready", -1, -1, 0, 5},       // zero-max repeat vanishes; deferred, fires at EOF
		{`ready\b(\b){1,2}`, "ready ", 0, 5, 0, 5},       // the space confirms mid-buffer
		{`ready\b(x{0,0})`, "ready ", 0, 5, 0, 5},        // the space confirms mid-buffer
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
