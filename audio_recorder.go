package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gen2brain/malgo"
)

func main() {
	// Create output folder in user's home directory
	homeDir, _ := os.UserHomeDir()
	outputFolder := filepath.Join(homeDir, "AudioRecordings")
	os.MkdirAll(outputFolder, 0755)

	// Initialize audio context
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		fmt.Println(message)
	})
	if err != nil {
		fmt.Println("Failed to initialize audio context:", err)
		os.Exit(1)
	}
	defer ctx.Free()

	fmt.Println("Windows Audio Recorder")
	fmt.Println("---------------------")
	fmt.Println("Recording will be saved to:", outputFolder)
	fmt.Println("Press Ctrl+C to stop recording and save...")

	// Set up channels to store audio data
	micSamples := make([][]float32, 0)
	speakerSamples := make([][]float32, 0)

	// Audio settings
	sampleRate := 44100
	channels := 2

	// Set up microphone recording
	micConfig := malgo.DeviceConfig{
		DeviceType: malgo.Capture,
		SampleRate: uint32(sampleRate),
		Capture: malgo.SubConfig{
			Format:   malgo.FormatF32,
			Channels: uint32(channels),
		},
	}

	// Set up speaker recording (loopback)
	speakerConfig := malgo.DeviceConfig{
		DeviceType: malgo.Loopback,
		SampleRate: uint32(sampleRate),
		Capture: malgo.SubConfig{
			Format:   malgo.FormatF32,
			Channels: uint32(channels),
		},
	}

	// Start recording microphone
	micDevice, err := malgo.InitDevice(ctx.Context, micConfig, malgo.DeviceCallbacks{
		Data: func(output, input []byte, frameCount uint32) {
			// Convert input bytes to float32 slice
			samplesF32 := make([]float32, frameCount*uint32(channels))
			for i := 0; i < int(frameCount*uint32(channels)); i++ {
				if i*4+3 < len(input) {
					var value float32
					binary.Read(bytes.NewReader(input[i*4:i*4+4]), binary.LittleEndian, &value)
					samplesF32[i] = value
				}
			}
			micSamples = append(micSamples, samplesF32)
		},
	})
	if err != nil {
		fmt.Println("Failed to initialize microphone:", err)
		os.Exit(1)
	}

	if err = micDevice.Start(); err != nil {
		fmt.Println("Failed to start microphone:", err)
		micDevice.Uninit()
		os.Exit(1)
	}
	defer micDevice.Uninit()

	// Start recording speakers
	speakerDevice, err := malgo.InitDevice(ctx.Context, speakerConfig, malgo.DeviceCallbacks{
		Data: func(output, input []byte, frameCount uint32) {
			// Convert input bytes to float32 slice
			samplesF32 := make([]float32, frameCount*uint32(channels))
			for i := 0; i < int(frameCount*uint32(channels)); i++ {
				if i*4+3 < len(input) {
					var value float32
					binary.Read(bytes.NewReader(input[i*4:i*4+4]), binary.LittleEndian, &value)
					samplesF32[i] = value
				}
			}
			speakerSamples = append(speakerSamples, samplesF32)
		},
	})
	if err != nil {
		fmt.Println("Failed to initialize speaker:", err)
		micDevice.Stop()
		micDevice.Uninit()
		os.Exit(1)
	}

	if err = speakerDevice.Start(); err != nil {
		fmt.Println("Failed to start speaker:", err)
		micDevice.Stop()
		micDevice.Uninit()
		speakerDevice.Uninit()
		os.Exit(1)
	}
	defer speakerDevice.Uninit()

	// Print recording status
	startTime := time.Now()
	go func() {
		for {
			elapsed := time.Since(startTime)
			fmt.Printf("\rRecording... %02d:%02d:%02d",
				int(elapsed.Hours()),
				int(elapsed.Minutes())%60,
				int(elapsed.Seconds())%60)
			time.Sleep(1 * time.Second)
		}
	}()

	// Wait for Ctrl+C to stop recording
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	// Stop recording
	fmt.Println("\nStopping recording...")
	micDevice.Stop()
	speakerDevice.Stop()

	// Mix the audio and save to WAV file
	fmt.Println("Mixing audio...")
	mixedSamples := mixAudio(micSamples, speakerSamples)

	// Create filename with timestamp
	timestamp := time.Now().Format("20060102_150405")
	outputFile := filepath.Join(outputFolder, fmt.Sprintf("recording_%s.wav", timestamp))

	// Save to WAV file
	fmt.Println("Saving to", outputFile)
	err = saveWAV(outputFile, mixedSamples, sampleRate, channels)
	if err != nil {
		fmt.Println("Error saving WAV file:", err)
		os.Exit(1)
	}

	fmt.Println("Recording saved successfully!")
}

// mixAudio mixes microphone and speaker audio
func mixAudio(micSamples, speakerSamples [][]float32) []float32 {
	// Determine the shorter of the two
	minLength := len(micSamples)
	if len(speakerSamples) < minLength {
		minLength = len(speakerSamples)
	}

	// Calculate the total number of samples
	totalSamples := 0
	for i := 0; i < minLength; i++ {
		micLen := len(micSamples[i])
		speakerLen := len(speakerSamples[i])
		shorter := micLen
		if speakerLen < shorter {
			shorter = speakerLen
		}
		totalSamples += shorter
	}

	// Create the mixed samples array
	mixed := make([]float32, totalSamples)
	sampleIndex := 0

	// Mix the samples
	for i := 0; i < minLength; i++ {
		micLen := len(micSamples[i])
		speakerLen := len(speakerSamples[i])
		shorter := micLen
		if speakerLen < shorter {
			shorter = speakerLen
		}

		for j := 0; j < shorter; j++ {
			// Simple mix - average the mic and speaker samples
			mixed[sampleIndex] = (micSamples[i][j] + speakerSamples[i][j]) / 2.0
			sampleIndex++
		}
	}

	return mixed
}

// saveWAV saves audio data to a WAV file
func saveWAV(filePath string, samples []float32, sampleRate, channels int) error {
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	// WAV file parameters
	bitsPerSample := 16
	dataSize := len(samples) * (bitsPerSample / 8)

	// Write WAV header
	// RIFF header
	file.WriteString("RIFF")
	// File size (minus 8 bytes for "RIFF" and size)
	fileSize := 36 + dataSize
	binary.Write(file, binary.LittleEndian, uint32(fileSize))
	file.WriteString("WAVE")

	// Format chunk
	file.WriteString("fmt ")
	binary.Write(file, binary.LittleEndian, uint32(16)) // Format chunk size
	binary.Write(file, binary.LittleEndian, uint16(1))  // PCM format
	binary.Write(file, binary.LittleEndian, uint16(channels))
	binary.Write(file, binary.LittleEndian, uint32(sampleRate))

	// Bytes per second & block align
	bytesPerSample := bitsPerSample / 8
	blockAlign := channels * bytesPerSample
	bytesPerSec := sampleRate * blockAlign

	binary.Write(file, binary.LittleEndian, uint32(bytesPerSec))
	binary.Write(file, binary.LittleEndian, uint16(blockAlign))
	binary.Write(file, binary.LittleEndian, uint16(bitsPerSample))

	// Data chunk
	file.WriteString("data")
	binary.Write(file, binary.LittleEndian, uint32(dataSize))

	// Write audio data
	for _, sample := range samples {
		// Convert float32 (-1.0 to 1.0) to int16 range
		int16Sample := int16(sample * 32767)
		binary.Write(file, binary.LittleEndian, int16Sample)
	}

	return nil
}
