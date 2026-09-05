package pveclient

import (
	"math/rand/v2"
)

// jitterFactor returns a random multiplier in [0.8, 1.2) to reduce
// thundering-herd retries across nodes or requests.
func jitterFactor() float64 {
	return 0.8 + 0.4*rand.Float64()
}
