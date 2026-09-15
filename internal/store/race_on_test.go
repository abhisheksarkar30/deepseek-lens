//go:build race

package store

// raceEnabled is true under `go test -race`, which the toolchain marks with
// the implicit "race" build tag. The race detector instruments every memory
// access, which can slow a 10,000-row scan by an order of magnitude or
// more — TestListRequestsPerformanceAndLimits relaxes its wall-clock bound
// accordingly rather than asserting a timing that instrumentation itself
// falsifies.
const raceEnabled = true
