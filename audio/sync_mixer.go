package audio

import (
	"time"
)

// TimeSyncMixAudioSamples mixes two audio sample arrays with proper time synchronization
func TimeSyncMixAudioSamples(samples1 []float32, timestamp1 time.Time,
	samples2 []float32, timestamp2 time.Time,
	sampleRate, channels int) ([]float32, time.Time) {
	// If one array is empty, return the other
	if len(samples1) == 0 {
		return samples2, timestamp2
	}
	if len(samples2) == 0 {
		return samples1, timestamp1
	}

	// Determine which sample set started first (this will be our reference)
	var refSamples, laterSamples []float32
	var refTimestamp, laterTimestamp time.Time

	if timestamp1.Before(timestamp2) {
		refSamples = samples1
		refTimestamp = timestamp1
		laterSamples = samples2
		laterTimestamp = timestamp2
	} else {
		refSamples = samples2
		refTimestamp = timestamp2
		laterSamples = samples1
		laterTimestamp = timestamp1
	}

	// Calculate time offset in milliseconds
	timeDiffMs := laterTimestamp.Sub(refTimestamp).Milliseconds()

	// Calculate offset in samples
	samplesPerMs := float64(sampleRate*channels) / 1000.0
	offsetSamples := int(float64(timeDiffMs) * samplesPerMs)

	// For very small offsets (less than 1ms), just do a simple mix
	if offsetSamples <= 0 {
		return MixAudioSamples(samples1, samples2), refTimestamp
	}

	// Calculate total length needed
	totalLength := len(refSamples)
	if offsetSamples+len(laterSamples) > totalLength {
		totalLength = offsetSamples + len(laterSamples)
	}

	// Create mixed buffer
	mixed := make([]float32, totalLength)

	// Copy reference samples
	copy(mixed, refSamples)

	// Mix in later samples at the correct offset
	for i := 0; i < len(laterSamples); i++ {
		pos := offsetSamples + i
		if pos < len(mixed) {
			if pos < len(refSamples) {
				// If we have both samples, mix them 50/50
				mixed[pos] = (mixed[pos] + laterSamples[i]) * 0.5
			} else {
				// If only laterSamples has values here, use those
				mixed[pos] = laterSamples[i]
			}
		}
	}

	return mixed, refTimestamp
}
