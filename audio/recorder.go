package audio

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RecordingConfig contains configuration for the recorder
type RecordingConfig struct {
	ChunkDurationSeconds int    // Duration between saves in seconds
	OutputFolder         string // Where to save the recordings
	RecordingName        string // Base name for recordings
	SampleRate           int    // Audio sample rate
	Channels             int    // Number of audio channels
}

// Recorder manages the continuous recording process
type Recorder struct {
	config                RecordingConfig
	outputFilePath        string
	micBuffer             *Buffer
	speakerBuffer         *Buffer
	mixedBuffer           *Buffer
	currentFileSize       int64
	recordingActive       bool
	writingActive         bool
	writerWaitGroup       sync.WaitGroup
	startTime             time.Time
	currentChunkStartTime time.Time
	writeSignal           chan bool
	stopSignal            chan bool
	debugMode             bool
}

// NewRecorder creates a new continuous recorder
func NewRecorder(config RecordingConfig) *Recorder {
	// Create output directory if it doesn't exist
	os.MkdirAll(config.OutputFolder, 0755)

	// Generate a single output filename
	timestamp := time.Now().Format("2006_01_02_15_04_05")
	filename := fmt.Sprintf("%s_%s.wav", config.RecordingName, timestamp)
	filePath := filepath.Join(config.OutputFolder, filename)

	return &Recorder{
		config:          config,
		outputFilePath:  filePath,
		micBuffer:       NewBuffer(config.SampleRate, config.Channels),
		speakerBuffer:   NewBuffer(config.SampleRate, config.Channels),
		mixedBuffer:     NewBuffer(config.SampleRate, config.Channels),
		recordingActive: false,
		writingActive:   false,
		writeSignal:     make(chan bool, 1),
		stopSignal:      make(chan bool, 1),
		debugMode:       false,
	}
}

// SetDebugMode enables or disables debug outputs
func (r *Recorder) SetDebugMode(enabled bool) {
	r.debugMode = enabled
}

// StartRecording begins the continuous recording process
func (r *Recorder) StartRecording() {
	r.recordingActive = true
	r.writingActive = true
	r.startTime = time.Now()
	r.currentChunkStartTime = time.Now()

	// Initialize WAV file with header
	err := InitializeWAVFile(r.outputFilePath, r.config.SampleRate, r.config.Channels)
	if err != nil {
		fmt.Println("Error initializing WAV file:", err)
		return
	}

	// Get initial file size
	info, err := os.Stat(r.outputFilePath)
	if err == nil {
		r.currentFileSize = info.Size()
	}

	// Start the writer goroutine
	r.writerWaitGroup.Add(1)
	go r.audioWriterRoutine()

	// Start the timer for regular saving
	go r.saveTimerRoutine()

	fmt.Println("Recording to file:", r.outputFilePath)
}

// StopRecording stops the recording and finalizes the file
func (r *Recorder) StopRecording() {
	if !r.recordingActive {
		return // Already stopped
	}

	// Signal that recording is stopping
	r.recordingActive = false

	// Trigger one final write
	r.writeSignal <- true

	// Wait briefly to ensure the write signal is processed
	time.Sleep(100 * time.Millisecond)

	// Signal writer to stop and wait for it to complete
	r.stopSignal <- true
	r.writerWaitGroup.Wait()

	fmt.Println("Recording stopped and saved to:", r.outputFilePath)
}

// audioWriterRoutine handles writing audio data in a separate thread
func (r *Recorder) audioWriterRoutine() {
	defer r.writerWaitGroup.Done()

	for r.writingActive {
		select {
		case <-r.writeSignal:
			// Process any pending microphone and speaker data into mixed buffer
			r.processPendingAudio()

			// Get mixed samples from buffer
			samples, _, sampleRate, channels := r.mixedBuffer.Get()

			// Only write if we have samples
			if len(samples) > 0 {
				err := r.appendToWAVFile(samples, sampleRate, channels)
				if err != nil {
					fmt.Println("Error writing to WAV file:", err)
				} else if r.debugMode {
					seconds := float64(len(samples)) / float64(sampleRate*channels)
					fmt.Printf("Appended %.2f seconds of audio (total: %.2f MB)\n",
						seconds, float64(r.currentFileSize)/(1024*1024))
				}
			}

		case <-r.stopSignal:
			// Final write handled before this is triggered
			r.writingActive = false
			return
		}
	}
}

// processPendingAudio processes and mixes microphone and speaker data
func (r *Recorder) processPendingAudio() {
	// Get microphone samples
	micSamples, micTimestamp, _, _ := r.micBuffer.Get()

	// Get speaker samples
	speakerSamples, speakerTimestamp, _, _ := r.speakerBuffer.Get()

	// Mix the samples with proper time synchronization
	mixedSamples, mixedTimestamp := TimeSyncMixAudioSamples(
		micSamples, micTimestamp,
		speakerSamples, speakerTimestamp,
		r.config.SampleRate, r.config.Channels)

	// Add to mixed buffer using the correctly synchronized timestamp
	if len(mixedSamples) > 0 {
		r.mixedBuffer.Add(mixedSamples, mixedTimestamp)
	}

	if r.debugMode {
		// Show time difference between mic and speaker for debugging
		if !micTimestamp.IsZero() && !speakerTimestamp.IsZero() {
			var diff int64
			if micTimestamp.Before(speakerTimestamp) {
				diff = speakerTimestamp.Sub(micTimestamp).Milliseconds()
				fmt.Printf("\nSync info: Speaker is %dms behind mic\n", diff)
			} else {
				diff = micTimestamp.Sub(speakerTimestamp).Milliseconds()
				fmt.Printf("\nSync info: Mic is %dms behind speaker\n", diff)
			}
		}
	}
}

// saveTimerRoutine triggers periodic saves
func (r *Recorder) saveTimerRoutine() {
	for r.recordingActive {
		// Sleep for the chunk duration
		time.Sleep(time.Duration(r.config.ChunkDurationSeconds) * time.Second)

		// Skip if not recording anymore
		if !r.recordingActive {
			break
		}

		// Reset chunk start time
		r.currentChunkStartTime = time.Now()

		// Signal the writer to save data
		select {
		case r.writeSignal <- true:
			// Signal sent successfully
		default:
			// Channel is full, which means a write is already pending
			if r.debugMode {
				fmt.Println("Save signal dropped - writer busy")
			}
		}
	}
}

// appendToWAVFile safely appends audio data to the WAV file
func (r *Recorder) appendToWAVFile(samples []float32, sampleRate, channels int) error {
	if len(samples) == 0 {
		return nil
	}

	// Open file for appending
	file, err := os.OpenFile(r.outputFilePath, os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	// Seek to the end of the file (after header and existing data)
	_, err = file.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}

	// Write audio data
	bytesWritten, err := WriteFloatSamples(file, samples)
	if err != nil {
		return err
	}

	// Update file size
	r.currentFileSize += int64(bytesWritten)

	// Update the WAV header with new size
	dataSize := int(r.currentFileSize - 44) // 44 bytes is the WAV header size
	err = UpdateWAVHeader(file, dataSize)
	if err != nil {
		return err
	}

	return nil
}

// AddMicSamples adds microphone samples to the recorder
func (r *Recorder) AddMicSamples(samples []float32, timestamp time.Time) {
	if !r.recordingActive || len(samples) == 0 {
		return
	}

	// Add samples to the buffer
	r.micBuffer.Add(samples, timestamp)
}

// AddSpeakerSamples adds speaker samples to the recorder
func (r *Recorder) AddSpeakerSamples(samples []float32, timestamp time.Time) {
	if !r.recordingActive || len(samples) == 0 {
		return
	}

	// Add samples to the buffer
	r.speakerBuffer.Add(samples, timestamp)
}

// GetCurrentChunkStartTime returns when the current chunk started saving
func (r *Recorder) GetCurrentChunkStartTime() time.Time {
	return r.currentChunkStartTime
}

// GetStartTime returns when the recording started
func (r *Recorder) GetStartTime() time.Time {
	return r.startTime
}

// GetOutputFilePath returns the current output file path
func (r *Recorder) GetOutputFilePath() string {
	return r.outputFilePath
}

// GetRecordingDuration returns the current recording duration
func (r *Recorder) GetRecordingDuration() time.Duration {
	return time.Since(r.startTime)
}

// IsRecording returns whether recording is active
func (r *Recorder) IsRecording() bool {
	return r.recordingActive
}

// GetMicBuffer returns the microphone buffer for external processing
func (r *Recorder) GetMicBuffer() *Buffer {
	return r.micBuffer
}

// GetMicBuffer returns the speaker buffer for external processing
func (r *Recorder) GetSpeakerBuffer() *Buffer {
	return r.speakerBuffer
}

// GetMicBuffer returns the speaker buffer for external processing
func (r *Recorder) GetMixedBuffer() *Buffer {
	return r.mixedBuffer
}
