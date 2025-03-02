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

	"github.com/galfthan/audiorecorder/audio" // Audio package
	"github.com/gen2brain/malgo"
)

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

	fmt.Println("Continuous Audio Recorder")
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
	fmt.Print("\nEnter duration between saves (in seconds, default 30): ")
	chunkDuration := 30 // Default 30 seconds
	var input string
	fmt.Scanln(&input)
	if input != "" {
		fmt.Sscanf(input, "%d", &chunkDuration)
		if chunkDuration < 5 {
			fmt.Println("Duration too short, using minimum of 5 seconds.")
			chunkDuration = 5
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
	fmt.Printf("- Saving every %d seconds\n", chunkDuration)
	fmt.Println("- Recordings will be saved to:", outputFolder)
	fmt.Println("Press Ctrl+C to stop recording and save...")

	// Audio settings
	sampleRate := 16000
	channels := 1

	// Create recorder configuration
	config := audio.RecordingConfig{
		ChunkDurationSeconds: chunkDuration,
		OutputFolder:         outputFolder,
		RecordingName:        recordingName,
		SampleRate:           sampleRate,
		Channels:             channels,
	}

	// Create continuous recorder
	recorder := audio.NewRecorder(config)

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
			recorder.AddMicSamples(samplesF32, chunkTime)
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
			recorder.AddSpeakerSamples(samplesF32, chunkTime)
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
	stopDisplaying := make(chan bool)

	go func() {
		for {
			select {
			case <-stopDisplaying:
				return
			default:
				elapsed := time.Since(recorder.GetStartTime())
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
				fmt.Printf("\rRecording... %02d:%02d:%02d  Mic: %s %d%%  Next save: %02d:%02d  File: %s",
					int(elapsed.Hours()),
					int(elapsed.Minutes())%60,
					int(elapsed.Seconds())%60,
					meter, level,
					int(nextSaveIn.Minutes())%60,
					int(nextSaveIn.Seconds())%60,
					filepath.Base(recorder.GetOutputFilePath()))

				time.Sleep(100 * time.Millisecond)
			}
		}
	}()

	// Wait for Ctrl+C to stop recording
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	// Stop status display
	close(stopDisplaying)
	fmt.Println("\nStopping recording...")

	// Stop audio devices
	micDevice.Stop()
	if speakerActive {
		speakerDevice.Stop()
	}

	// Stop and finalize the recording
	recorder.StopRecording()

	fmt.Println("Recording saved successfully to:", recorder.GetOutputFilePath())
	fmt.Println("Press Enter to exit...")
	fmt.Scanln()
}
