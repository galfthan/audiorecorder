package audio

import (
	"sync"
	"time"
)

// AudioChunk represents a chunk of audio data with a timestamp
type AudioChunk struct {
	Samples    []float32
	Timestamp  time.Time
	SampleRate int
	Channels   int
}

// Buffer is a thread-safe audio buffer
type Buffer struct {
	samples    []float32
	sampleRate int
	channels   int
	timestamp  time.Time
	mutex      sync.Mutex
}

// NewBuffer creates a new audio buffer
func NewBuffer(sampleRate, channels int) *Buffer {
	return &Buffer{
		samples:    make([]float32, 0),
		sampleRate: sampleRate,
		channels:   channels,
		mutex:      sync.Mutex{},
	}
}

// Add adds samples to the buffer
func (b *Buffer) Add(samples []float32, timestamp time.Time) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	if len(b.samples) == 0 {
		b.timestamp = timestamp
	}

	b.samples = append(b.samples, samples...)
}

// Get returns a copy of the samples and clears the buffer
func (b *Buffer) Get() ([]float32, time.Time, int, int) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	// Make a copy of the samples
	samplesCopy := make([]float32, len(b.samples))
	copy(samplesCopy, b.samples)

	// Get timestamp and specs
	timestamp := b.timestamp
	sampleRate := b.sampleRate
	channels := b.channels

	// Clear the buffer
	b.samples = make([]float32, 0)

	return samplesCopy, timestamp, sampleRate, channels
}

// IsEmpty checks if the buffer is empty
func (b *Buffer) IsEmpty() bool {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return len(b.samples) == 0
}

// Size returns the number of samples in the buffer
func (b *Buffer) Size() int {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return len(b.samples)
}
