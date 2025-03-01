package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gen2brain/malgo"
	"github.com/getlantern/systray"
	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/moutend/go-wav"
)

type AudioRecorder struct {
	isRecording      bool
	startTime        time.Time
	outputFolder     string
	ctx              *malgo.AllocatedContext
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

	// Start the UI
	systray.Run(recorder.onSystrayReady, recorder.onSystrayExit)
}

// NewAudioRecorder creates and initializes a new AudioRecorder instance
func NewAudioRecorder() *AudioRecorder {
	// Create output folder in user's home directory
	homeDir, _ := os.UserHomeDir()
	outputFolder := filepath.Join(homeDir, "AudioRecordings")
	os.MkdirAll(outputFolder, 0755)

	// Create audio context
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
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
func getAudioDevices(ctx *malgo.AllocatedContext, deviceType malgo.DeviceType) ([]malgo.DeviceInfo, error) {
	infos, err := ctx.Devices(deviceType)
	if err != nil {
		return nil, err
	}
	return infos, nil
}

// onSystrayReady initializes the system tray icon and user interface
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

	// Launch the UI in a separate goroutine
	go ar.createAndShowMainWindow()

	// Handle system tray menu actions
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
			return
		}
	}
}

// onSystrayExit handles cleanup when the system tray is exited
func (ar *AudioRecorder) onSystrayExit() {
	if ar.ctx != nil {
		ar.ctx.Uninit()
		ar.ctx.Free()
	}
}

// createAndShowMainWindow creates and displays the main application window
func (ar *AudioRecorder) createAndShowMainWindow() {
	var err error

	// Create the main window
	ar.mainWindow, err = MainWindow{
		AssignTo: &ar.mainWindow,
		Title:     "Windows Audio Recorder",
		MinSize:   Size{Width: 400, Height: 300},
		Layout:    VBox{},
		Children: []Widget{
			PushButton{
				AssignTo: &ar.recordButton,
				Text:     "Start Recording",
				OnClicked: func() {
					ar.toggleRecording()
				},
			},
			GroupBox{
				Title:  "Status",
				Layout: VBox{},
				Children: []Widget{
					Label{
						AssignTo: &ar.statusLabel,
						Text:     "Ready",
					},
				},
			},
			Label{
				AssignTo: &ar.timeLabel,
				Text:     "00:00:00",
			},
			GroupBox{
				Title:  "Settings",
				Layout: VBox{},
				Children: []Widget{
					CheckBox{
						AssignTo: &ar.autoStartCheck,
						Text:     "Start with Windows",
						OnCheckedChanged: func() {
							ar.toggleAutoStart()
						},
					},
				},
			},
		},
		OnSizeChanged: func() {
			// Check if window is minimized
			if ar.mainWindow.Visible() && ar.mainWindow.WindowState() == walk.WindowMinimized {
				ar.mainWindow.Hide()
			}
		},
	}.Create()

	if err != nil {
		fmt.Println("Error creating main window:", err)
		return
	}

	// Check if app is in startup and update checkbox
	ar.updateAutoStartCheckbox()

	// Show the window and run the message loop
	ar.mainWindow.Show()
	ar.mainWindow.Run()
}

// showMainWindow makes the main window visible and restores it if minimized
func (ar *AudioRecorder) showMainWindow() {
	if ar.mainWindow != nil {
		ar.mainWindow.SetVisible(true)
		ar.mainWindow.SetWindowState(walk.WindowNormal)
		ar.mainWindow.SetForeground()
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
	// Configure audio device for microphone capture
	var inputDevice malgo.DeviceConfig
	var outputDevice malgo.DeviceConfig

	// Get default devices if available
	if len(ar.recordingDevices) > 0 {
		inputDevice = malgo.DefaultDeviceConfig(malgo.Capture)
		inputDevice.DeviceID = &ar.recordingDevices[0].ID
	} else {
		inputDevice = malgo.DefaultDeviceConfig(malgo.Capture)
	}

	if len(ar.playbackDevices) > 0 {
		outputDevice = malgo.DefaultDeviceConfig(malgo.Loopback)
		outputDevice.DeviceID = &ar.playbackDevices[0].ID
	} else {
		outputDevice = malgo.DefaultDeviceConfig(malgo.Loopback)
	}

	// Common configurations
	sampleRate := 44100
	channels := 2

	// Configure input device (microphone)
	inputConfig := malgo.DeviceConfig{
		DeviceType: malgo.Capture,
		SampleRate: uint32(sampleRate),
		Channels:   uint32(channels),
		Format:     malgo.FormatF32,
		DeviceID:   inputDevice.DeviceID,
	}

	// Configure output device (speaker loopback)
	outputConfig := malgo.DeviceConfig{
		DeviceType: malgo.Loopback,
		SampleRate: uint32(sampleRate),
		Channels:   uint32(channels),
		Format:     malgo.FormatF32,
		DeviceID:   outputDevice.DeviceID,
	}

	// Buffer for microphone data
	micBuffer := make([][]float32, 0)

	// Buffer for speaker data
	speakerBuffer := make([][]float32, 0)

	// Callback for microphone
	micCallback := malgo.DeviceCallbacks{
		Data: func(output, input []byte, frameCount uint32) {
			if !ar.isRecording {
				return
			}

			// Convert bytes to float32 samples
			floatData := make([]float32, frameCount*uint32(channels))
			samplesRead := 0
			for i := 0; i < len(input); i += 4 {
				if i+3 < len(input) {
					var sample float32
					// Convert 4 bytes to float32
					// This is a simplistic approach, might need adjustment based on endianness
					bytes := input[i : i+4]
					sample = float32(float32frombytes(bytes))
					if samplesRead < len(floatData) {
						floatData[samplesRead] = sample
						samplesRead++
					}
				}
			}

			// Add to buffer
			micBuffer = append(micBuffer, floatData)
		},
	}

	// Callback for speaker
	speakerCallback := malgo.DeviceCallbacks{
		Data: func(output, input []byte, frameCount uint32) {
			if !ar.isRecording {
				return
			}

			// Convert bytes to float32 samples
			floatData := make([]float32, frameCount*uint32(channels))
			samplesRead := 0
			for i := 0; i < len(input); i += 4 {
				if i+3 < len(input) {
					var sample float32
					bytes := input[i : i+4]
					sample = float32(float32frombytes(bytes))
					if samplesRead < len(floatData) {
						floatData[samplesRead] = sample
						samplesRead++
					}
				}
			}

			// Add to buffer
			speakerBuffer = append(speakerBuffer, floatData)
		},
	}

	// Create devices
	micDevice, err := malgo.InitDevice(ar.ctx.Context, inputConfig, micCallback)
	if err != nil {
		fmt.Println("Failed to initialize microphone device:", err)
		ar.isRecording = false
		return
	}

	speakerDevice, err := malgo.InitDevice(ar.ctx.Context, outputConfig, speakerCallback)
	if err != nil {
		fmt.Println("Failed to initialize speaker loopback device:", err)
		micDevice.Uninit()
		ar.isRecording = false
		return
	}

	// Start devices
	err = micDevice.Start()
	if err != nil {
		fmt.Println("Failed to start microphone device:", err)
		micDevice.Uninit()
		speakerDevice.Uninit()
		ar.isRecording = false
		return
	}

	err = speakerDevice.Start()
	if err != nil {
		fmt.Println("Failed to start speaker loopback device:", err)
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
			// Could use more sophisticated mixing algorithm
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
	waveFile, err := os.Create(outputFile)
	if err != nil {
		fmt.Println("Failed to create output file:", err)
		return
	}
	defer waveFile.Close()

	// WAV parameters
	numChannels := 2
	sampleRate := 44100
	bitsPerSample := 16

	// Create WAV writer
	wavWriter, err := wav.NewWriter(waveFile, uint32(totalSamples), uint16(numChannels), uint32(sampleRate), uint16(bitsPerSample))
	if err != nil {
		fmt.Println("Failed to create WAV writer:", err)
		return
	}

	// Convert float32 samples to int16 and write to file
	for _, buffer := range ar.capturedSamples {
		for _, sample := range buffer {
			// Convert float32 (-1.0 to 1.0) to int16 range
			int16Sample := int16(sample * 32767)
			err := wavWriter.WriteInt16(int16Sample)
			if err != nil {
				fmt.Println("Error writing to WAV file:", err)
				return
			}
		}
	}

	// Show message about saved file
	walk.MsgBox(ar.mainWindow, "Recording Saved", 
		fmt.Sprintf("Recording saved to:\n%s", outputFile), 
		walk.MsgBoxIconInformation)
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
	defer key.Close()

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
	defer key.Close()

	// Delete registry value
	oleutil.MustCallMethod(key, "DeleteValue", "AudioRecorder")
}

// checkIfInStartup checks if the app is set to autostart
func (ar *AudioRecorder) checkIfInStartup() bool {
	key, err := openStartupKey()
	if err != nil {
		return false
	}
	defer key.Close()

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

// float32frombytes converts 4 bytes to a float32
func float32frombytes(bytes []byte) float32 {
	bits := uint32(bytes[0]) | uint32(bytes[1])<<8 | uint32(bytes[2])<<16 | uint32(bytes[3])<<24
	return float32(bits)
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