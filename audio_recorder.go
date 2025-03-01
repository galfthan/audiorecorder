package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/gen2brain/malgo"
)

// AudioChunk represents a chunk of audio data with a timestamp
type AudioChunk struct {
	Samples    []float32
	Timestamp  time.Time
	SampleRate int
	Channels   int
}

func main() {
	// Create output folder in user's home directory
	homeDir, _ := os.UserHomeDir()
	outputFolder := filepath.Join(homeDir, "AudioRecordings")
	os.MkdirAll(outputFolder, 0755)

	// Initialize audio context
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		fmt.Println("AUDIO:", message)
	})
	if err != nil {
		fmt.Println("Failed to initialize audio context:", err)
		fmt.Println("Press Enter to exit...")
		fmt.Scanln()
		return
	}
	defer ctx.Free()

	fmt.Println("Windows Audio Recorder (Clean Audio + Sync)")
	fmt.Println("----------------------------------------")

	// List available audio devices
	fmt.Println("\nAVAILABLE MICROPHONES:")
	captureDevices, err := ctx.Devices(malgo.Capture)
	if err != nil {
		fmt.Println("Error listing capture devices:", err)
	} else if len(captureDevices) == 0 {
		fmt.Println("No capture devices found!")
	} else {
		for i, device := range captureDevices {
			fmt.Printf("%d: %s\n", i, device.Name())
		}
	}

	fmt.Println("\nAVAILABLE SPEAKERS (LOOPBACK):")
	loopbackDevices, err := ctx.Devices(malgo.Loopback)
	if err != nil {
		fmt.Println("Error listing loopback devices:", err)
	} else if len(loopbackDevices) == 0 {
		fmt.Println("No loopback devices found!")
	} else {
		for i, device := range loopbackDevices {
			fmt.Printf("%d: %s\n", i, device.Name())
		}
	}

	// Ask user to select microphone device
	var micDeviceIndex int
	if len(captureDevices) > 1 {
		fmt.Print("\nSelect microphone by number (or press Enter for default): ")
		input := ""
		fmt.Scanln(&input)
		if input != "" {
			fmt.Sscanf(input, "%d", &micDeviceIndex)
			if micDeviceIndex < 0 || micDeviceIndex >= len(captureDevices) {
				fmt.Println("Invalid selection, using default device.")
				micDeviceIndex = 0
			}
		}
	}

	fmt.Println("\nRecording will be saved to:", outputFolder)
	fmt.Println("Press Ctrl+C to stop recording and save...")

	// Audio settings - use original parameters
	sampleRate := 44100
	channels := 2

	// Create channels to store audio data
	var micChunks []AudioChunk
	var speakerChunks []AudioChunk
	var audioMutex sync.Mutex

	// Record the exact recording start time
	recordingStartTime := time.Now()

	// Set up microphone recording with specific device
	micConfig := malgo.DeviceConfig{
		DeviceType: malgo.Capture,
		SampleRate: uint32(sampleRate),
		Capture: malgo.SubConfig{
			Format:   malgo.FormatF32,
			Channels: uint32(channels),
		},
	}

	// Set specific device if user selected one
	if len(captureDevices) > 0 {
		selectedDevice := captureDevices[micDeviceIndex]
		fmt.Printf("Using microphone: %s\n", selectedDevice.Name())
		micConfig.Capture.DeviceID = selectedDevice.ID.Pointer()
	}

	// Variables for microphone level monitoring
	var micLevel float32
	var micMutex sync.Mutex

	// Start recording microphone
	micDevice, err := malgo.InitDevice(ctx.Context, micConfig, malgo.DeviceCallbacks{
		Data: func(output, input []byte, frameCount uint32) {
			// Get the current time for this chunk
			chunkTime := time.Now()

			// Calculate audio level from this batch
			level := float32(0)

			// Convert input bytes to float32 slice - simple, direct conversion
			samplesF32 := make([]float32, frameCount*uint32(channels))
			for i := 0; i < int(frameCount*uint32(channels)); i++ {
				if i*4+3 < len(input) {
					var value float32
					binary.Read(bytes.NewReader(input[i*4:i*4+4]), binary.LittleEndian, &value)
					samplesF32[i] = value

					// Calculate level (absolute value)
					absValue := float32(0)
					if value < 0 {
						absValue = -value
					} else {
						absValue = value
					}
					level += absValue
				}
			}

			// Normalize level
			if frameCount > 0 {
				level = level / float32(frameCount*uint32(channels))
			}

			// Update level safely
			micMutex.Lock()
			micLevel = level
			micMutex.Unlock()

			// Store the chunk with its timestamp
			audioMutex.Lock()
			micChunks = append(micChunks, AudioChunk{
				Samples:    samplesF32,
				Timestamp:  chunkTime,
				SampleRate: sampleRate,
				Channels:   int(channels),
			})
			audioMutex.Unlock()
		},
	})
	if err != nil {
		fmt.Println("Failed to initialize microphone:", err)
		fmt.Println("Press Enter to exit...")
		fmt.Scanln()
		return
	}

	if err = micDevice.Start(); err != nil {
		fmt.Println("Failed to start microphone:", err)
		micDevice.Uninit()
		fmt.Println("Press Enter to exit...")
		fmt.Scanln()
		return
	}
	defer micDevice.Uninit()

	// Set up speaker recording (loopback)
	speakerConfig := malgo.DeviceConfig{
		DeviceType: malgo.Loopback,
		SampleRate: uint32(sampleRate),
		Capture: malgo.SubConfig{
			Format:   malgo.FormatF32,
			Channels: uint32(channels),
		},
	}

	// Try to start recording speakers
	var speakerActive bool
	speakerDevice, err := malgo.InitDevice(ctx.Context, speakerConfig, malgo.DeviceCallbacks{
		Data: func(output, input []byte, frameCount uint32) {
			// Get the current time for this chunk
			chunkTime := time.Now()

			// Convert input bytes to float32 slice - simple, direct conversion
			samplesF32 := make([]float32, frameCount*uint32(channels))
			for i := 0; i < int(frameCount*uint32(channels)); i++ {
				if i*4+3 < len(input) {
					var value float32
					binary.Read(bytes.NewReader(input[i*4:i*4+4]), binary.LittleEndian, &value)
					samplesF32[i] = value
				}
			}

			// Store the chunk with its timestamp
			audioMutex.Lock()
			speakerChunks = append(speakerChunks, AudioChunk{
				Samples:    samplesF32,
				Timestamp:  chunkTime,
				SampleRate: sampleRate,
				Channels:   int(channels),
			})
			audioMutex.Unlock()
		},
	})
	if err != nil {
		fmt.Println("Failed to initialize speaker:", err)
		fmt.Println("Will continue with microphone only.")
	} else {
		if err = speakerDevice.Start(); err != nil {
			fmt.Println("Failed to start speaker:", err)
			speakerDevice.Uninit()
			fmt.Println("Will continue with microphone only.")
		} else {
			defer speakerDevice.Uninit()
			speakerActive = true
		}
	}

	// Print recording status with microphone level indicator
	startTime := time.Now()
	stopRecording := make(chan bool)

	go func() {
		for {
			select {
			case <-stopRecording:
				return
			default:
				elapsed := time.Since(startTime)

				// Get current mic level safely
				micMutex.Lock()
				currentLevel := micLevel
				micMutex.Unlock()

				// Create audio level meter
				const meterWidth = 20
				level := int(currentLevel * 100)
				if level > 100 {
					level = 100
				}
				bar := int(level * meterWidth / 100)

				meter := "["
				for i := 0; i < meterWidth; i++ {
					if i < bar {
						meter += "#"
					} else {
						meter += " "
					}
				}
				meter += "]"

				// Show recording stats
				audioMutex.Lock()
				micCount := len(micChunks)
				speakerCount := len(speakerChunks)
				audioMutex.Unlock()

				fmt.Printf("\rRecording... %02d:%02d:%02d  Mic: %s %d%%  Chunks: Mic=%d Spk=%d",
					int(elapsed.Hours()),
					int(elapsed.Minutes())%60,
					int(elapsed.Seconds())%60,
					meter, level,
					micCount, speakerCount)

				time.Sleep(100 * time.Millisecond)
			}
		}
	}()

	// Wait for Ctrl+C to stop recording
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	// Stop recording
	fmt.Println("\nStopping recording...")
	close(stopRecording)
	micDevice.Stop()
	if speakerActive {
		speakerDevice.Stop()
	}

	// Save both mic and speaker recordings separately by flattening chunks
	timestamp := time.Now().Format("20060102_150405")

	// Copy chunks safely
	audioMutex.Lock()
	micChunksCopy := make([]AudioChunk, len(micChunks))
	copy(micChunksCopy, micChunks)

	speakerChunksCopy := make([]AudioChunk, len(speakerChunks))
	copy(speakerChunksCopy, speakerChunks)
	audioMutex.Unlock()

	// Save microphone recording (flattened, not synchronized)
	if len(micChunksCopy) > 0 {
		micFile := filepath.Join(outputFolder, fmt.Sprintf("mic_%s.wav", timestamp))
		fmt.Println("Saving microphone recording to:", micFile)
		flattenedMicSamples := flattenChunks(micChunksCopy)
		saveWAV(micFile, flattenedMicSamples, sampleRate, channels)
	}

	// Save speaker recording if available (flattened, not synchronized)
	if speakerActive && len(speakerChunksCopy) > 0 {
		speakerFile := filepath.Join(outputFolder, fmt.Sprintf("speaker_%s.wav", timestamp))
		fmt.Println("Saving speaker recording to:", speakerFile)
		flattenedSpeakerSamples := flattenChunks(speakerChunksCopy)
		saveWAV(speakerFile, flattenedSpeakerSamples, sampleRate, channels)
	}

	// Create synchronized mixed recording
	mixedFile := filepath.Join(outputFolder, fmt.Sprintf("mixed_%s.wav", timestamp))
	fmt.Println("Creating synchronized mixed recording...")
	createSynchronizedMix(mixedFile, micChunksCopy, speakerChunksCopy, recordingStartTime, sampleRate, channels)

	fmt.Println("All recordings saved successfully!")
	fmt.Println("Press Enter to exit...")
	fmt.Scanln()
}

// flattenChunks combines multiple audio chunks into a single sample array
func flattenChunks(chunks []AudioChunk) []float32 {
	if len(chunks) == 0 {
		return nil
	}

	// Calculate total samples
	totalSamples := 0
	for _, chunk := range chunks {
		totalSamples += len(chunk.Samples)
	}

	// Create combined sample array
	allSamples := make([]float32, 0, totalSamples)

	// Append all samples in order
	for _, chunk := range chunks {
		allSamples = append(allSamples, chunk.Samples...)
	}

	return allSamples
}

// createSynchronizedMix creates a synchronized mix of microphone and speaker audio
// This version uses a simpler approach with minimal processing to maintain quality
func createSynchronizedMix(filePath string, micChunks, speakerChunks []AudioChunk,
	startTime time.Time, sampleRate, channels int) error {

	// If we don't have both types of chunks, fall back to just one
	if len(micChunks) == 0 && len(speakerChunks) == 0 {
		return fmt.Errorf("no audio data available")
	} else if len(micChunks) == 0 {
		fmt.Println("Using speaker audio only for mix (no microphone data)")
		flattenedSpeakerSamples := flattenChunks(speakerChunks)
		return saveWAV(filePath, flattenedSpeakerSamples, sampleRate, channels)
	} else if len(speakerChunks) == 0 {
		fmt.Println("Using microphone audio only for mix (no speaker data)")
		flattenedMicSamples := flattenChunks(micChunks)
		return saveWAV(filePath, flattenedMicSamples, sampleRate, channels)
	}

	// Simplified synchronization approach:
	// 1. Sort chunks by timestamp
	// 2. Calculate offset between first mic and speaker chunk
	// 3. Apply this offset to create properly synchronized mix

	// Find the earliest timestamp for each stream
	var firstMicTime, firstSpeakerTime time.Time

	if len(micChunks) > 0 {
		firstMicTime = micChunks[0].Timestamp
	}

	if len(speakerChunks) > 0 {
		firstSpeakerTime = speakerChunks[0].Timestamp
	}

	// Calculate time offset between streams in samples
	var offsetSamples int
	samplesPerMs := float64(sampleRate*channels) / 1000.0

	if firstMicTime.Before(firstSpeakerTime) {
		// Mic started first
		offsetMs := firstSpeakerTime.Sub(firstMicTime).Milliseconds()
		offsetSamples = int(float64(offsetMs) * samplesPerMs)
		fmt.Printf("Speaker started %.1f ms after microphone\n", float64(offsetMs))
	} else {
		// Speaker started first
		offsetMs := firstMicTime.Sub(firstSpeakerTime).Milliseconds()
		offsetSamples = -int(float64(offsetMs) * samplesPerMs)
		fmt.Printf("Microphone started %.1f ms after speaker\n", float64(offsetMs))
	}

	// Flatten both streams
	flatMicSamples := flattenChunks(micChunks)
	flatSpeakerSamples := flattenChunks(speakerChunks)

	// Calculate total length needed
	totalLength := len(flatMicSamples)
	if len(flatSpeakerSamples)+abs(offsetSamples) > totalLength {
		totalLength = len(flatSpeakerSamples) + abs(offsetSamples)
	}

	// Create mixed buffer
	mixedSamples := make([]float32, totalLength)

	// Copy microphone samples
	micStart := 0
	if offsetSamples < 0 {
		// If mic started after speaker, adjust start position
		micStart = abs(offsetSamples)
	}

	for i := 0; i < len(flatMicSamples); i++ {
		pos := micStart + i
		if pos < len(mixedSamples) {
			mixedSamples[pos] = flatMicSamples[i]
		}
	}

	// Mix in speaker samples
	speakerStart := 0
	if offsetSamples > 0 {
		// If speaker started after mic, adjust start position
		speakerStart = offsetSamples
	}

	for i := 0; i < len(flatSpeakerSamples); i++ {
		pos := speakerStart + i
		if pos < len(mixedSamples) {
			// Simple mix - add half of each (avoid clipping)
			mixedSamples[pos] = (mixedSamples[pos] + flatSpeakerSamples[i]) * 0.5
		}
	}

	// Save the mixed file
	return saveWAV(filePath, mixedSamples, sampleRate, channels)
}

// abs returns the absolute value of an integer
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
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
