package utils

import (
	"math/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

var (
	entropy *ulid.MonotonicEntropy
	once    sync.Once
)

// initEntropy memastikan entropy hanya di-init sekali
func initEntropy() {
	source := rand.New(rand.NewSource(time.Now().UnixNano()))
	entropy = ulid.Monotonic(source, 0)
}

// NewULID generate ULID string (26 chars)
func NewULID() string {
	once.Do(initEntropy)
	return ulid.MustNew(
		ulid.Timestamp(time.Now()),
		entropy,
	).String()
}
