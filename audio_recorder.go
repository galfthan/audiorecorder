package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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

// Configuration for continuous recording
type RecordingConfig struct {
	ChunkDurationSeconds int    // Duration of each WAV file in seconds
	OutputFolder         string // Where to save the recordings
	RecordingName        string // Base name for recordings
	SampleRate           int    // Audio sample rate
	Channels             int    // Number of audio channels
}

// Manages the continuous recording process
type ContinuousRecorder struct {
	config                RecordingConfig
	micChunks             []AudioChunk
	speakerChunks         []AudioChunk
	audioMutex            sync.Mutex
	currentFileNum        int
	recordingActive       bool
	startTime             time.Time
	currentChunkStartTime time.Time
	// Place for future whisper integration
	transcriptionCh chan []float32 // Channel to send audio for transcription
}

func main() {
	// Get custom filename from command line arguments
	recordingName := "recording" // Default name
	if len(os.Args) > 1 {
		// Use the first argument as the recording name
		recordingName = os.Args[1]
		// Replace spaces with underscores for filename
		recordingName = strings.ReplaceAll(recordingName, " ", "_")
	}

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

	fmt.Println("Windows Audio Recorder (Continuous Recording)")
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

	// Show current recording name
	fmt.Printf("\nRecording name: %s\n", recordingName)

	// Ask user for continuous recording settings
	fmt.Print("\nEnter duration for each WAV file (in seconds, default 300): ")
	chunkDuration := 300 // Default 5 minutes
	var input string
	fmt.Scanln(&input)
	if input != "" {
		fmt.Sscanf(input, "%d", &chunkDuration)
		if chunkDuration < 10 {
			fmt.Println("Duration too short, using minimum of 10 seconds.")
			chunkDuration = 10
		}
	}

	// Ask user to select microphone device
	var micDeviceIndex int
	if len(captureDevices) > 1 {
		fmt.Print("\nSelect microphone by number (or press Enter for default): ")
		input = ""
		fmt.Scanln(&input)
		if input != "" {
			fmt.Sscanf(input, "%d", &micDeviceIndex)
			if micDeviceIndex < 0 || micDeviceIndex >= len(captureDevices) {
				fmt.Println("Invalid selection, using default device.")
				micDeviceIndex = 0
			}
		}
	}

	fmt.Println("\nContinuous recording settings:")
	fmt.Printf("- Each WAV file will be %d seconds long\n", chunkDuration)
	fmt.Println("- Recordings will be saved to:", outputFolder)
	fmt.Println("Press Ctrl+C to stop recording and save...")

	// Audio settings
	sampleRate := 44100
	channels := 2

	// Create recorder configuration
	config := RecordingConfig{
		ChunkDurationSeconds: chunkDuration,
		OutputFolder:         outputFolder,
		RecordingName:        recordingName,
		SampleRate:           sampleRate,
		Channels:             channels,
	}

	// Create continuous recorder
	recorder := NewContinuousRecorder(config)

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

			// Add audio chunk to recorder
			recorder.AddMicChunk(AudioChunk{
				Samples:    samplesF32,
				Timestamp:  chunkTime,
				SampleRate: sampleRate,
				Channels:   int(channels),
			})
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

			// Add audio chunk to recorder
			recorder.AddSpeakerChunk(AudioChunk{
				Samples:    samplesF32,
				Timestamp:  chunkTime,
				SampleRate: sampleRate,
				Channels:   int(channels),
			})
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

	// Start the continuous recording process
	recorder.StartRecording()

	// Print recording status with microphone level indicator
	stopRecording := make(chan bool)

	go func() {
		for {
			select {
			case <-stopRecording:
				return
			default:
				elapsed := time.Since(recorder.startTime)
				nextSaveIn := time.Duration(config.ChunkDurationSeconds)*time.Second -
					time.Since(recorder.GetCurrentChunkStartTime())

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
				fmt.Printf("\rRecording... %02d:%02d:%02d  Mic: %s %d%%  Next save: %02d:%02d  Files: %d",
					int(elapsed.Hours()),
					int(elapsed.Minutes())%60,
					int(elapsed.Seconds())%60,
					meter, level,
					int(nextSaveIn.Minutes())%60,
					int(nextSaveIn.Seconds())%60,
					recorder.currentFileNum)

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

	// Finalize the recording (save any remaining chunks)
	recorder.StopRecording()

	fmt.Println("All recordings saved successfully to:", outputFolder)
	fmt.Println("Press Enter to exit...")
	fmt.Scanln()
}

// NewContinuousRecorder creates a new continuous recorder with the given configuration
func NewContinuousRecorder(config RecordingConfig) *ContinuousRecorder {
	return &ContinuousRecorder{
		config:          config,
		micChunks:       []AudioChunk{},
		speakerChunks:   []AudioChunk{},
		audioMutex:      sync.Mutex{},
		currentFileNum:  0,
		recordingActive: false,
		transcriptionCh: make(chan []float32, 10), // Buffer for transcription
	}
}

// StartRecording begins the continuous recording process
func (r *ContinuousRecorder) StartRecording() {
	r.recordingActive = true
	r.startTime = time.Now()
	r.currentChunkStartTime = time.Now()

	// Start a goroutine to handle saving chunks at regular intervals
	go r.saveChunksRoutine()

	// This is where we would start the transcription process in the future
	// go r.transcriptionRoutine()
}

// StopRecording stops the recording and saves any remaining chunks
func (r *ContinuousRecorder) StopRecording() {
	r.recordingActive = false
	r.saveCurrentChunks()

	// Close transcription channel to signal shutdown
	close(r.transcriptionCh)
}

// AddMicChunk adds a microphone chunk to the recorder
func (r *ContinuousRecorder) AddMicChunk(chunk AudioChunk) {
	r.audioMutex.Lock()
	r.micChunks = append(r.micChunks, chunk)
	r.audioMutex.Unlock()

	// Here's where we could send samples to the transcription channel
	// if we wanted to do live transcription
	// r.transcriptionCh <- chunk.Samples
}

// AddSpeakerChunk adds a speaker chunk to the recorder
func (r *ContinuousRecorder) AddSpeakerChunk(chunk AudioChunk) {
	r.audioMutex.Lock()
	r.speakerChunks = append(r.speakerChunks, chunk)
	r.audioMutex.Unlock()
}

// GetCurrentChunkStartTime returns when the current chunk started

// GetCurrentChunkStartTime returns when the current chunk started
func (r *ContinuousRecorder) GetCurrentChunkStartTime() time.Time {
	return r.currentChunkStartTime
}

// saveChunksRoutine continuously saves chunks at regular intervals
func (r *ContinuousRecorder) saveChunksRoutine() {
	for r.recordingActive {
		// Sleep until it's time to save
		time.Sleep(time.Duration(r.config.ChunkDurationSeconds) * time.Second)
		if !r.recordingActive {
			break
		}

		// Save current chunks to a file
		r.saveCurrentChunks()

		// Reset chunk start time
		r.currentChunkStartTime = time.Now()
	}
}

// saveCurrentChunks saves the current audio chunks to a WAV file and clears the buffers
func (r *ContinuousRecorder) saveCurrentChunks() {
	r.audioMutex.Lock()
	// Make copies of current chunks
	micChunksCopy := make([]AudioChunk, len(r.micChunks))
	copy(micChunksCopy, r.micChunks)

	speakerChunksCopy := make([]AudioChunk, len(r.speakerChunks))
	copy(speakerChunksCopy, r.speakerChunks)

	// Clear current chunks
	r.micChunks = []AudioChunk{}
	r.speakerChunks = []AudioChunk{}
	r.audioMutex.Unlock()

	// Skip if we don't have any audio
	if len(micChunksCopy) == 0 && len(speakerChunksCopy) == 0 {
		return
	}

	// Increment file number
	r.currentFileNum++

	// Generate filename with timestamp and number
	timestamp := time.Now().Format("2006_01_02_15_04_05")
	filename := fmt.Sprintf("%s_%s_part%03d.wav",
		r.config.RecordingName, timestamp, r.currentFileNum)
	filePath := filepath.Join(r.config.OutputFolder, filename)

	// Create synchronized mixed recording
	fmt.Println("\nSaving file:", filename)
	createSynchronizedMix(filePath, micChunksCopy, speakerChunksCopy,
		r.startTime, r.config.SampleRate, r.config.Channels)
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
	} else {
		// Speaker started first
		offsetMs := firstMicTime.Sub(firstSpeakerTime).Milliseconds()
		offsetSamples = -int(float64(offsetMs) * samplesPerMs)
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

// Future whisper integration function
/*
func (r *ContinuousRecorder) transcriptionRoutine() {
    // This is where the whisper integration would happen
    // It would receive audio from the transcriptionCh channel
    // and pass it to whisper for processing
    for samples := range r.transcriptionCh {
        // Future: Process samples with whisper
        // ...
    }
}
*/
