package watches

import "testing"

func TestZeroMinRepeatDefersRightEdge(t *testing.T) {
	patterns := []string{`ready\bx{0,2}`, `ready$z{0,2}`, `ready\b(?:x{0,2})`}
	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			condition := Condition{Type: ConditionRegex, Pattern: pattern}
			observations := []chunkObservation{
				{condition: condition, chunks: stringChunks("readyx"), from: -1, to: -1},
				{condition: condition, chunks: stringChunks("readyx"), eof: true, from: -1, to: -1},
			}
			// Every split point defers and is then refuted: no fire, even
			// across a restart and with EOF.
			for split := 1; split < len("readyx"); split++ {
				chunks := stringChunks("readyx"[:split], "readyx"[split:])
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
