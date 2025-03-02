package audio

import (
	"encoding/binary"
	"io"
	"os"
)

// WAVHeader holds information for a WAV file
type WAVHeader struct {
	SampleRate    int
	Channels      int
	BitsPerSample int
	DataSize      int
}

// WriteWAVHeader writes a WAV header to the file
func WriteWAVHeader(file *os.File, header WAVHeader) error {
	// RIFF header
	if _, err := file.WriteString("RIFF"); err != nil {
		return err
	}

	// File size (minus 8 bytes for "RIFF" and size)
	fileSize := 36 + header.DataSize
	if err := binary.Write(file, binary.LittleEndian, uint32(fileSize)); err != nil {
		return err
	}

	if _, err := file.WriteString("WAVE"); err != nil {
		return err
	}

	// Format chunk
	if _, err := file.WriteString("fmt "); err != nil {
		return err
	}

	if err := binary.Write(file, binary.LittleEndian, uint32(16)); err != nil { // Format chunk size
		return err
	}

	if err := binary.Write(file, binary.LittleEndian, uint16(1)); err != nil { // PCM format
		return err
	}

	if err := binary.Write(file, binary.LittleEndian, uint16(header.Channels)); err != nil {
		return err
	}

	if err := binary.Write(file, binary.LittleEndian, uint32(header.SampleRate)); err != nil {
		return err
	}

	// Bytes per second & block align
	bytesPerSample := header.BitsPerSample / 8
	blockAlign := header.Channels * bytesPerSample
	bytesPerSec := header.SampleRate * blockAlign

	if err := binary.Write(file, binary.LittleEndian, uint32(bytesPerSec)); err != nil {
		return err
	}

	if err := binary.Write(file, binary.LittleEndian, uint16(blockAlign)); err != nil {
		return err
	}

	if err := binary.Write(file, binary.LittleEndian, uint16(header.BitsPerSample)); err != nil {
		return err
	}

	// Data chunk
	if _, err := file.WriteString("data"); err != nil {
		return err
	}

	if err := binary.Write(file, binary.LittleEndian, uint32(header.DataSize)); err != nil {
		return err
	}

	return nil
}

// UpdateWAVHeader updates the size information in the WAV header
func UpdateWAVHeader(file *os.File, dataSize int) error {
	// Update the RIFF chunk size (file size - 8)
	fileSize := 36 + dataSize
	file.Seek(4, io.SeekStart)
	if err := binary.Write(file, binary.LittleEndian, uint32(fileSize)); err != nil {
		return err
	}

	// Update the data chunk size
	file.Seek(40, io.SeekStart)
	if err := binary.Write(file, binary.LittleEndian, uint32(dataSize)); err != nil {
		return err
	}

	return nil
}

// WriteFloatSamples writes float32 samples as 16-bit PCM to a WAV file
func WriteFloatSamples(file *os.File, samples []float32) (int, error) {
	bytesWritten := 0

	for _, sample := range samples {
		// Convert float32 (-1.0 to 1.0) to int16 range
		int16Sample := int16(sample * 32767)
		err := binary.Write(file, binary.LittleEndian, int16Sample)
		if err != nil {
			return bytesWritten, err
		}
		bytesWritten += 2 // 2 bytes per sample (16-bit)
	}

	return bytesWritten, nil
}

// InitializeWAVFile creates a new WAV file with header
func InitializeWAVFile(filePath string, sampleRate, channels int) error {
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	header := WAVHeader{
		SampleRate:    sampleRate,
		Channels:      channels,
		BitsPerSample: 16,
		DataSize:      0, // Initial data size is zero
	}

	return WriteWAVHeader(file, header)
}

// MixAudioSamples mixes two float32 sample arrays with a simple 50/50 mix
func MixAudioSamples(samples1, samples2 []float32) []float32 {
	// If one array is empty, return the other
	if len(samples1) == 0 {
		return samples2
	}
	if len(samples2) == 0 {
		return samples1
	}

	// Use the longer array for the result
	resultLength := len(samples1)
	if len(samples2) > resultLength {
		resultLength = len(samples2)
	}

	// Create the mixed result
	mixed := make([]float32, resultLength)

	// Copy samples1 (up to its length)
	for i := 0; i < len(samples1); i++ {
		mixed[i] = samples1[i]
	}

	// Mix in samples2 (up to its length)
	for i := 0; i < len(samples2); i++ {
		if i < len(samples1) {
			// If we have both samples, mix them 50/50
			mixed[i] = (mixed[i] + samples2[i]) * 0.5
		} else {
			// If only samples2 has values here, use those
			mixed[i] = samples2[i]
		}
	}

	return mixed
}
