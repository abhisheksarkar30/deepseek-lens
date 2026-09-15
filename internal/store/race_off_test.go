//go:build !race

package store

// raceEnabled is false in a normal build. See race_on_test.go.
const raceEnabled = false
