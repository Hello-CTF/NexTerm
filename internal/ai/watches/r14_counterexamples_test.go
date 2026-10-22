package watches

import "testing"

func TestNullableConsumingSuffixDefersRightEdge(t *testing.T) {
	patterns := []string{`ready\b.*`, `ready\b(?:x)?`, `ready$.*`}
	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			condition := Condition{Type: ConditionRegex, Pattern: pattern}
			observations := []chunkObservation{
				{condition: condition, chunks: stringChunks("readyx"), from: -1, to: -1},
			}
			// Every split point defers and is then refuted: no fire.
			for split := 1; split < len("readyx"); split++ {
				chunks := stringChunks("readyx"[:split], "readyx"[split:])
				observations = append(observations,
					chunkObservation{condition: condition, chunks: chunks, from: -1, to: -1},
					chunkObservation{condition: condition, chunks: chunks, restart: true, from: -1, to: -1},
				)
			}
			assertChunkObservations(t, observations)
		})
	}
}
