package streamtest

import "math/rand"

// Fragment returns deterministic transport chunks. A zero or negative chunk
// size uses one byte per fragment; callers can use the same input with every
// split point and seeded random chunk sizes.
func Fragment(data []byte, chunkSize int) [][]byte {
	if chunkSize <= 0 {
		chunkSize = 1
	}
	result := make([][]byte, 0, (len(data)+chunkSize-1)/chunkSize)
	for len(data) > 0 {
		n := chunkSize
		if n > len(data) {
			n = len(data)
		}
		result = append(result, append([]byte(nil), data[:n]...))
		data = data[n:]
	}
	return result
}

// RandomFragment uses a local deterministic PRNG and never returns an empty
// chunk, making it suitable for repeatable decoder fuzz seeds.
func RandomFragment(data []byte, seed int64, maxChunk int) [][]byte {
	if maxChunk <= 0 {
		maxChunk = 16
	}
	random := rand.New(rand.NewSource(seed))
	result := make([][]byte, 0)
	for len(data) > 0 {
		n := 1 + random.Intn(maxChunk)
		if n > len(data) {
			n = len(data)
		}
		result = append(result, append([]byte(nil), data[:n]...))
		data = data[n:]
	}
	return result
}
