package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
	"bytes"
	"encoding/binary"

	"github.com/gen2brain/malgo"
	"github.com/getlantern/systray"
	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"github.com/lxn/walk"
)

type AudioRecorder struct {
	isRecording      bool
	startTime        time.Time
	outputFolder     string
	ctx              *malgo.Context
	recordingDevices []malgo.DeviceInfo
	playbackDevices  []malgo.DeviceInfo
	capturedSamples  [][]float32
	mainWindow       *walk.MainWindow
	recordButton     *walk.PushButton
	statusLabel      *walk.Label
	timeLabel        *walk.Label
	autoStartCheck   *walk.CheckBox
}

func main() {
	// Initialize OLE for COM operations
	ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED)
	defer ole.CoUninitialize()

	// Create recorder instance
	recorder := NewAudioRecorder()

	// Start the UI in the main thread
	recorder.createAndShowMainWindow()

	// Start the systray after the window is created
	go systray.Run(recorder.onSystrayReady, recorder.onSystrayExit)

	// Start the main window loop
	recorder.mainWindow.Run()
}

// NewAudioRecorder creates and initializes a new AudioRecorder instance
func NewAudioRecorder() *AudioRecorder {
	// Create output folder in user's home directory
	homeDir, _ := os.UserHomeDir()
	outputFolder := filepath.Join(homeDir, "AudioRecordings")
	os.MkdirAll(outputFolder, 0755)

	// Create audio context
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		fmt.Println(message)
	})
	if err != nil {
		fmt.Println("Failed to initialize audio context:", err)
		os.Exit(1)
	}

	// Get available audio devices
	recordingDevices, err := getAudioDevices(ctx, malgo.Capture)
	if err != nil {
		fmt.Println("Failed to get recording devices:", err)
	}

	playbackDevices, err := getAudioDevices(ctx, malgo.Playback)
	if err != nil {
		fmt.Println("Failed to get playback devices:", err)
	}

	recorder := &AudioRecorder{
		isRecording:      false,
		outputFolder:     outputFolder,
		ctx:              ctx,
		recordingDevices: recordingDevices,
		playbackDevices:  playbackDevices,
		capturedSamples:  make([][]float32, 0),
	}

	return recorder
}

// getAudioDevices retrieves available audio devices
func getAudioDevices(ctx *malgo.Context, deviceType malgo.DeviceType) ([]malgo.DeviceInfo, error) {
	// Get device info
	deviceInfos, err := ctx.Devices(deviceType)
	if err != nil {
		return nil, err
	}
	return deviceInfos, nil
}

// onSystrayReady initializes the system tray icon and menu
func (ar *AudioRecorder) onSystrayReady() {
	// Set system tray icon and menu
	systray.SetIcon(getIconData())
	systray.SetTitle("Audio Recorder")
	systray.SetTooltip("Windows Audio Recorder")

	mShow := systray.AddMenuItem("Show", "Show main window")
	systray.AddSeparator()
	mStartRecording := systray.AddMenuItem("Start Recording", "Start recording audio")
	mStopRecording := systray.AddMenuItem("Stop Recording", "Stop recording audio")
	mStopRecording.Disable()
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit the app")

	// Handle system tray menu actions
	go func() {
		for {
			select {
			case <-mShow.ClickedCh:
				ar.showMainWindow()
			case <-mStartRecording.ClickedCh:
				if !ar.isRecording {
					ar.startRecording()
					mStartRecording.Disable()
					mStopRecording.Enable()
				}
			case <-mStopRecording.ClickedCh:
				if ar.isRecording {
					ar.stopRecording()
					mStartRecording.Enable()
					mStopRecording.Disable()
				}
			case <-mQuit.ClickedCh:
				if ar.isRecording {
					ar.stopRecording()
				}
				systray.Quit()
				ar.mainWindow.Close()
				return
			}
		}
	}()
}

// onSystrayExit handles cleanup when the system tray is exited
func (ar *AudioRecorder) onSystrayExit() {
	ar.ctx.Uninit()
	os.Exit(0)
}

// createAndShowMainWindow creates and displays the main application window
func (ar *AudioRecorder) createAndShowMainWindow() {
	// Create a new main window
	mainWindow, err := walk.NewMainWindow()
	if err != nil {
		fmt.Println("Error creating main window:", err)
		return
	}
	ar.mainWindow = mainWindow

	// Set window properties
	ar.mainWindow.SetTitle("Windows Audio Recorder")
	ar.mainWindow.SetSize(walk.Size{Width: 400, Height: 300})
	ar.mainWindow.SetMinMaxSize(walk.Size{Width: 300, Height: 200}, walk.Size{Width: 600, Height: 400})
	
	// Create vertical layout
	vbox, _ := walk.NewVBoxLayout(ar.mainWindow)
	ar.mainWindow.SetLayout(vbox)

	// Add record button
	recordButton, _ := walk.NewPushButton(ar.mainWindow)
	ar.recordButton = recordButton
	ar.recordButton.SetText("Start Recording")
	ar.recordButton.Clicked().Attach(func() {
		ar.toggleRecording()
	})

	// Add status group box
	statusGroupBox, _ := walk.NewGroupBox(ar.mainWindow)
	statusGroupBox.SetTitle("Status")
	vbox.AddWidget(statusGroupBox, false)
	
	statusLayout, _ := walk.NewVBoxLayout(statusGroupBox)
	statusGroupBox.SetLayout(statusLayout)
	
	statusLabel, _ := walk.NewLabel(statusGroupBox)
	ar.statusLabel = statusLabel
	ar.statusLabel.SetText("Ready")
	statusLayout.AddWidget(ar.statusLabel, false)

	// Add time label
	timeLabel, _ := walk.NewLabel(ar.mainWindow)
	ar.timeLabel = timeLabel
	ar.timeLabel.SetText("00:00:00")
	vbox.AddWidget(ar.timeLabel, false)

	// Add settings group box
	settingsGroupBox, _ := walk.NewGroupBox(ar.mainWindow)
	settingsGroupBox.SetTitle("Settings")
	vbox.AddWidget(settingsGroupBox, false)
	
	settingsLayout, _ := walk.NewVBoxLayout(settingsGroupBox)
	settingsGroupBox.SetLayout(settingsLayout)
	
	autoStartCheck, _ := walk.NewCheckBox(settingsGroupBox)
	ar.autoStartCheck = autoStartCheck
	ar.autoStartCheck.SetText("Start with Windows")
	ar.autoStartCheck.Clicked().Attach(func() {
		ar.toggleAutoStart()
	})
	settingsLayout.AddWidget(ar.autoStartCheck, false)

	// Check if app is in startup and update checkbox
	ar.updateAutoStartCheckbox()

	// Handle minimize to tray
	ar.mainWindow.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if reason == walk.CloseReasonUser {
			*canceled = true
			ar.mainWindow.Hide()
		}
	})
}

// showMainWindow makes the main window visible
func (ar *AudioRecorder) showMainWindow() {
	if ar.mainWindow != nil {
		ar.mainWindow.Synchronize(func() {
			ar.mainWindow.Show()
		})
	}
}

// toggleRecording starts or stops recording based on current state
func (ar *AudioRecorder) toggleRecording() {
	if !ar.isRecording {
		ar.startRecording()
	} else {
		ar.stopRecording()
	}
}

// startRecording begins the audio recording process
func (ar *AudioRecorder) startRecording() {
	ar.isRecording = true
	ar.recordButton.SetText("Stop Recording")
	ar.statusLabel.SetText("Recording...")

	// Reset captured samples
	ar.capturedSamples = make([][]float32, 0)
	ar.startTime = time.Now()

	// Start updating time display
	go ar.updateTimeDisplay()

	// Start recording in a goroutine
	go ar.recordAudio()
}

// stopRecording ends the audio recording process and saves the file
func (ar *AudioRecorder) stopRecording() {
	if !ar.isRecording {
		return
	}

	ar.isRecording = false
	ar.recordButton.SetText("Start Recording")
	ar.statusLabel.SetText("Saving recording...")

	// Allow some time for final data to be processed
	time.Sleep(500 * time.Millisecond)

	// Save the captured audio
	ar.saveRecording()

	ar.statusLabel.SetText("Ready")
}

// updateTimeDisplay updates the time label with the recording duration
func (ar *AudioRecorder) updateTimeDisplay() {
	for ar.isRecording {
		elapsed := time.Since(ar.startTime)
		hours := int(elapsed.Hours())
		minutes := int(elapsed.Minutes()) % 60
		seconds := int(elapsed.Seconds()) % 60
		timeStr := fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)

		// Update the time label
		ar.mainWindow.Synchronize(func() {
			if ar.timeLabel != nil {
				ar.timeLabel.SetText(timeStr)
			}
		})

		// Update every second
		time.Sleep(1 * time.Second)
	}

	// Reset time display
	ar.mainWindow.Synchronize(func() {
		if ar.timeLabel != nil {
			ar.timeLabel.SetText("00:00:00")
		}
	})
}

// recordAudio captures audio from both microphone and speakers
func (ar *AudioRecorder) recordAudio() {
	// Configure audio capture settings
	sampleRate := 44100
	channels := 2

	// Buffer for microphone data
	micBuffer := make([][]float32, 0)

	// Buffer for speaker data
	speakerBuffer := make([][]float32, 0)

	// Callback for microphone data
	onRecvFramesMic := func(pSample []byte, frameCount uint32) {
		if !ar.isRecording {
			return
		}

		// Convert bytes to float32 samples
		floatData := make([]float32, frameCount*uint32(channels))
		for i := 0; i < len(pSample); i += 4 {
			if i+3 < len(pSample) {
				// Convert 4 bytes to float32
				var val float32
				binary.Read(bytes.NewReader(pSample[i:i+4]), binary.LittleEndian, &val)
				floatData[i/4] = val
			}
		}

		// Add to buffer
		micBuffer = append(micBuffer, floatData)
	}

	// Callback for speaker data
	onRecvFramesSpeaker := func(pSample []byte, frameCount uint32) {
		if !ar.isRecording {
			return
		}

		// Convert bytes to float32 samples
		floatData := make([]float32, frameCount*uint32(channels))
		for i := 0; i < len(pSample); i += 4 {
			if i+3 < len(pSample) {
				// Convert 4 bytes to float32
				var val float32
				binary.Read(bytes.NewReader(pSample[i:i+4]), binary.LittleEndian, &val)
				floatData[i/4] = val
			}
		}

		// Add to buffer
		speakerBuffer = append(speakerBuffer, floatData)
	}

	// Set up microphone recording
	var micConfig malgo.DeviceConfig
	micConfig.DeviceType = malgo.Capture
	micConfig.SampleRate = uint32(sampleRate)
	micConfig.ChannelCount = uint32(channels)
	micConfig.Format = malgo.FormatF32
	micConfig.Alsa.NoMMap = 1

	// Set up speaker recording (loopback)
	var speakerConfig malgo.DeviceConfig
	speakerConfig.DeviceType = malgo.Loopback
	speakerConfig.SampleRate = uint32(sampleRate)
	speakerConfig.ChannelCount = uint32(channels)
	speakerConfig.Format = malgo.FormatF32
	speakerConfig.Alsa.NoMMap = 1

	// Choose default devices
	if len(ar.recordingDevices) > 0 {
		micConfig.DeviceID = ar.recordingDevices[0].ID
	}

	if len(ar.playbackDevices) > 0 {
		speakerConfig.DeviceID = ar.playbackDevices[0].ID
	}

	// Size for sample frames
	var sizeInBytes uint32 = 4 * uint32(channels) // 4 bytes per sample (float32) * channel count

	// Create and start microphone device
	micDevice, err := malgo.InitDevice(ar.ctx, micConfig, malgo.DeviceCallbacks{
		Data: func(output, input []byte, frameCount uint32) {
			if !ar.isRecording {
				return
			}

			// Copy the input data
			onRecvFramesMic(input, frameCount)
		},
	})

	if err != nil {
		fmt.Println("Failed to initialize microphone device:", err)
		ar.isRecording = false
		return
	}

	err = micDevice.Start()
	if err != nil {
		fmt.Println("Failed to start microphone device:", err)
		micDevice.Uninit()
		ar.isRecording = false
		return
	}

	// Create and start speaker device
	speakerDevice, err := malgo.InitDevice(ar.ctx, speakerConfig, malgo.DeviceCallbacks{
		Data: func(output, input []byte, frameCount uint32) {
			if !ar.isRecording {
				return
			}

			// Copy the input data
			onRecvFramesSpeaker(input, frameCount)
		},
	})

	if err != nil {
		fmt.Println("Failed to initialize speaker device:", err)
		micDevice.Stop()
		micDevice.Uninit()
		ar.isRecording = false
		return
	}

	err = speakerDevice.Start()
	if err != nil {
		fmt.Println("Failed to start speaker device:", err)
		micDevice.Stop()
		micDevice.Uninit()
		speakerDevice.Uninit()
		ar.isRecording = false
		return
	}

	// Keep recording until isRecording becomes false
	for ar.isRecording {
		time.Sleep(100 * time.Millisecond)
	}

	// Stop and uninitialize devices
	micDevice.Stop()
	speakerDevice.Stop()
	micDevice.Uninit()
	speakerDevice.Uninit()

	// Process and mix the captured audio
	ar.capturedSamples = mixAudioBuffers(micBuffer, speakerBuffer)
}

// mixAudioBuffers combines microphone and speaker audio
func mixAudioBuffers(micBuffer, speakerBuffer [][]float32) [][]float32 {
	// Determine the minimum length
	minLength := len(micBuffer)
	if len(speakerBuffer) < minLength {
		minLength = len(speakerBuffer)
	}

	// Create a mixed buffer
	mixed := make([][]float32, minLength)

	// Mix the audio
	for i := 0; i < minLength; i++ {
		micSamples := micBuffer[i]
		speakerSamples := speakerBuffer[i]

		// Get minimum samples length
		samplesLen := len(micSamples)
		if len(speakerSamples) < samplesLen {
			samplesLen = len(speakerSamples)
		}

		// Mix samples
		mixedSamples := make([]float32, samplesLen)
		for j := 0; j < samplesLen; j++ {
			// Simple mixing - average the samples
			mixedSamples[j] = (micSamples[j] + speakerSamples[j]) / 2.0
		}

		mixed[i] = mixedSamples
	}

	return mixed
}

// saveRecording saves the captured audio to a WAV file
func (ar *AudioRecorder) saveRecording() {
	if len(ar.capturedSamples) == 0 {
		fmt.Println("No audio data to save")
		return
	}

	// Create a timestamp for the filename
	timestamp := time.Now().Format("20060102_150405")
	outputFile := filepath.Join(ar.outputFolder, fmt.Sprintf("recording_%s.wav", timestamp))

	// Determine total number of samples
	totalSamples := 0
	for _, buffer := range ar.capturedSamples {
		totalSamples += len(buffer)
	}

	// Create WAV file
	file, err := os.Create(outputFile)
	if err != nil {
		fmt.Println("Failed to create output file:", err)
		return
	}
	defer file.Close()

	// WAV header parameters
	numChannels := 2
	sampleRate := 44100
	bitsPerSample := 16
	
	// Calculate data size
	dataSize := totalSamples * numChannels * (bitsPerSample / 8)
	
	// Write WAV header
	writeWavHeader(file, numChannels, sampleRate, bitsPerSample, dataSize)
	
	// Write audio data
	for _, buffer := range ar.capturedSamples {
		for _, sample := range buffer {
			// Convert float32 (-1.0 to 1.0) to int16 range
			int16Sample := int16(sample * 32767)
			binary.Write(file, binary.LittleEndian, int16Sample)
		}
	}

	// Show message about saved file
	walk.MsgBox(ar.mainWindow, "Recording Saved", 
		fmt.Sprintf("Recording saved to:\n%s", outputFile), 
		walk.MsgBoxIconInformation)
}

// writeWavHeader writes a WAV file header to the given file
func writeWavHeader(file *os.File, numChannels, sampleRate, bitsPerSample, dataSize int) {
	// RIFF header
	file.WriteString("RIFF")
	// File size (minus 8 bytes for "RIFF" and size)
	fileSize := 36 + dataSize
	binary.Write(file, binary.LittleEndian, uint32(fileSize))
	file.WriteString("WAVE")
	
	// Format chunk
	file.WriteString("fmt ")
	binary.Write(file, binary.LittleEndian, uint32(16)) // Format chunk size
	binary.Write(file, binary.LittleEndian, uint16(1)) // PCM format
	binary.Write(file, binary.LittleEndian, uint16(numChannels))
	binary.Write(file, binary.LittleEndian, uint32(sampleRate))
	
	// Bytes per second & block align
	bytesPerSample := bitsPerSample / 8
	blockAlign := numChannels * bytesPerSample
	bytesPerSec := sampleRate * blockAlign
	
	binary.Write(file, binary.LittleEndian, uint32(bytesPerSec))
	binary.Write(file, binary.LittleEndian, uint16(blockAlign))
	binary.Write(file, binary.LittleEndian, uint16(bitsPerSample))
	
	// Data chunk
	file.WriteString("data")
	binary.Write(file, binary.LittleEndian, uint32(dataSize))
}

// toggleAutoStart sets or removes the app from Windows startup
func (ar *AudioRecorder) toggleAutoStart() {
	// Use Windows registry for autostart
	if ar.autoStartCheck.Checked() {
		ar.addToStartup()
	} else {
		ar.removeFromStartup()
	}
}

// updateAutoStartCheckbox checks if app is in startup and updates checkbox
func (ar *AudioRecorder) updateAutoStartCheckbox() {
	isInStartup := ar.checkIfInStartup()
	ar.autoStartCheck.SetChecked(isInStartup)
}

// addToStartup adds the application to Windows startup
func (ar *AudioRecorder) addToStartup() {
	key, err := openStartupKey()
	if err != nil {
		fmt.Println("Failed to open startup registry key:", err)
		return
	}
	defer key.Release()

	execPath, err := os.Executable()
	if err != nil {
		fmt.Println("Failed to get executable path:", err)
		return
	}

	// Set registry value
	oleutil.MustPutProperty(key, "AudioRecorder", execPath)
}

// removeFromStartup removes the application from Windows startup
func (ar *AudioRecorder) removeFromStartup() {
	key, err := openStartupKey()
	if err != nil {
		fmt.Println("Failed to open startup registry key:", err)
		return
	}
	defer key.Release()

	// Delete registry value
	oleutil.MustCallMethod(key, "DeleteValue", "AudioRecorder")
}

// checkIfInStartup checks if the app is set to autostart
func (ar *AudioRecorder) checkIfInStartup() bool {
	key, err := openStartupKey()
	if err != nil {
		return false
	}
	defer key.Release()

	// Try to get the value
	_, err = oleutil.GetProperty(key, "AudioRecorder")
	return err == nil
}

// openStartupKey opens the Windows registry startup key
func openStartupKey() (*ole.IDispatch, error) {
	// Get WScript.Shell object
	unknown, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return nil, err
	}
	defer unknown.Release()

	wshell, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return nil, err
	}

	// Open HKCU\Software\Microsoft\Windows\CurrentVersion\Run
	regPath := "HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Run"
	key, err := oleutil.CallMethod(wshell, "RegRead", regPath)
	if err != nil {
		// If key doesn't exist, create it
		return wshell, nil
	}
	key.Clear()

	return wshell, nil
}

// getIconData returns a simple icon for the system tray
func getIconData() []byte {
	// This is a 16x16 pixel icon in ICO format (simplified version)
	// In a real application, you'd want to use a proper icon file
	return []byte{
		0, 0, 1, 0, 1, 0, 16, 16, 0, 0, 1, 0, 24, 0, 104, 4,
		0, 0, 22, 0, 0, 0, 40, 0, 0, 0, 16, 0, 0, 0, 32, 0,
		0, 0, 1, 0, 24, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 255, 0, 0, 255, 0, 0, 255, 0, 0,
		255, 0, 0, 255, 0, 0, 255, 0, 0, 255, 0, 0, 255, 0, 0,
		255, 0, 0, 255, 0, 0, 255, 0, 0, 255, 0, 0,
	}
}