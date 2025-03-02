package transcription

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/galfthan/audiorecorder/audio"
	"github.com/ggerganov/whisper.cpp/bindings/go/pkg/whisper"
)

// TranscriptionConfig contains configuration for the transcription
type TranscriptionConfig struct {
	ModelPath      string  // Path to Whisper model file
	Language       string  // Optional language hint (e.g., "en" for English)
	BatchSeconds   float64 // How many seconds of audio to process at once
	OutputFolder   string  // Where to save transcripts
	TranscriptName string  // Base name for transcript files
	SaveTimestamps bool    // Whether to include timestamps
}

// AudioSource identifies which audio source a transcript came from
type AudioSource int

const (
	SourceMic     AudioSource = iota // Microphone audio
	SourceSpeaker                    // Speaker/loopback audio
)

// TranscriptSegment represents a segment of transcribed text
type TranscriptSegment struct {
	Text      string
	StartTime float64
	EndTime   float64
	Source    AudioSource
	Timestamp time.Time // Real clock time when this was captured
}

// Transcriber manages the transcription process
type Transcriber struct {
	config          TranscriptionConfig
	model           whisper.Model
	context         whisper.Context
	transcriptFile  *os.File
	segments        []TranscriptSegment // Buffer of segments waiting to be written
	segmentsMutex   sync.Mutex
	processingMutex sync.Mutex
	isRunning       bool
	stopSignal      chan bool
	writeSignal     chan bool
	lastWriteTime   time.Time
}

// NewTranscriber creates a new transcription manager
func NewTranscriber(config TranscriptionConfig) (*Transcriber, error) {
	// Check if model file exists
	if _, err := os.Stat(config.ModelPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("Whisper model file not found: %s", config.ModelPath)
	}

	// Load Whisper model
	fmt.Println("Loading Whisper model from:", config.ModelPath)
	model, err := whisper.New(config.ModelPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load Whisper model: %v", err)
	}

	// Create Whisper context
	context, err := model.NewContext()
	if err != nil {
		model.Close()
		return nil, fmt.Errorf("failed to create Whisper context: %v", err)
	}

	// Generate transcript filename
	timestamp := time.Now().Format("2006_01_02_15_04_05")
	filename := fmt.Sprintf("%s_transcript_%s.txt", config.TranscriptName, timestamp)
	filePath := filepath.Join(config.OutputFolder, filename)

	// Create/open transcript file
	file, err := os.Create(filePath)
	if err != nil {
		context.Free()
		model.Close()
		return nil, fmt.Errorf("failed to create transcript file: %v", err)
	}

	// Write header to transcript file
	headerText := fmt.Sprintf("Transcript: %s\nStarted: %s\nModel: %s\n\n",
		config.TranscriptName,
		timestamp,
		filepath.Base(config.ModelPath))

	if _, err := file.WriteString(headerText); err != nil {
		file.Close()
		context.Free()
		model.Close()
		return nil, fmt.Errorf("failed to write to transcript file: %v", err)
	}

	return &Transcriber{
		config:         config,
		model:          model,
		context:        context,
		transcriptFile: file,
		segments:       make([]TranscriptSegment, 0),
		isRunning:      false,
		stopSignal:     make(chan bool, 1),
		writeSignal:    make(chan bool, 1),
		lastWriteTime:  time.Now(),
	}, nil
}

// Close frees resources used by the transcriber
func (t *Transcriber) Close() {
	t.Stop()

	// Force final write of any pending segments
	t.writeSegments()

	if t.transcriptFile != nil {
		t.transcriptFile.Close()
	}

	t.context.Free()
	t.model.Close()
}

// Start begins the transcription process
func (t *Transcriber) Start(micBuffer, speakerBuffer *audio.Buffer) error {
	t.processingMutex.Lock()
	defer t.processingMutex.Unlock()

	if t.isRunning {
		return fmt.Errorf("transcription already running")
	}

	// Set whisper parameters
	if t.config.Language != "" {
		t.context.SetLanguage(t.config.Language)
	}

	// Start the writer goroutine for synchronized output
	go t.writeRoutine()

	// Start processing in background - one goroutine per source
	t.isRunning = true
	go t.processAudioLoop(micBuffer, SourceMic)
	go t.processAudioLoop(speakerBuffer, SourceSpeaker)

	return nil
}

// Stop ends the transcription process
func (t *Transcriber) Stop() {
	t.processingMutex.Lock()
	defer t.processingMutex.Unlock()

	if !t.isRunning {
		return
	}

	t.stopSignal <- true
	t.isRunning = false

	// Signal the writer to do a final write
	t.writeSignal <- true
}

// writeRoutine periodically writes collected segments in chronological order
func (t *Transcriber) writeRoutine() {
	writeInterval := time.Duration(t.config.BatchSeconds) * time.Second

	for t.isRunning {
		select {
		case <-t.stopSignal:
			return
		case <-t.writeSignal:
			// Explicit signal to write segments
			t.writeSegments()
		case <-time.After(writeInterval):
			// Regular interval write
			if time.Since(t.lastWriteTime) >= writeInterval {
				t.writeSegments()
			}
		}
	}
}

// processAudioLoop continuously processes audio from a specific buffer
func (t *Transcriber) processAudioLoop(buffer *audio.Buffer, source AudioSource) {
	sourceLabel := "Microphone"
	if source == SourceSpeaker {
		sourceLabel = "Speaker"
	}

	fmt.Printf("Transcription processing started for %s\n", sourceLabel)

	// Track when we last processed audio
	lastProcessTime := time.Now()

	for t.isRunning {
		select {
		case <-t.stopSignal:
			fmt.Printf("Transcription processing stopped for %s\n", sourceLabel)
			return
		default:
			// Stagger processing a bit between mic and speaker to avoid CPU spikes
			processingDelay := time.Duration(t.config.BatchSeconds / 2 * float64(time.Second))
			if source == SourceSpeaker {
				processingDelay = time.Duration(t.config.BatchSeconds * 0.6 * float64(time.Second))
			}

			if time.Since(lastProcessTime) < processingDelay {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// Skip if buffer is empty
			if buffer.IsEmpty() {
				time.Sleep(500 * time.Millisecond)
				continue
			}

			// Get audio batch for transcription (without clearing buffer)
			audioData, timestamp := buffer.Peek(t.config.BatchSeconds)

			// Skip if not enough audio data
			if len(audioData) < 1000 { // Arbitrary small number to avoid processing tiny chunks
				time.Sleep(500 * time.Millisecond)
				continue
			}

			// Process with Whisper
			segments, err := t.processAudioBatch(audioData, source, timestamp)
			if err != nil {
				fmt.Printf("Transcription error (%s): %v\n", sourceLabel, err)
			} else if len(segments) > 0 {
				// Add segments to the buffer
				t.addSegments(segments)

				// Signal writer if we have enough segments or enough time has passed
				t.segmentsMutex.Lock()
				numSegments := len(t.segments)
				t.segmentsMutex.Unlock()

				if numSegments > 10 || time.Since(t.lastWriteTime) > time.Duration(t.config.BatchSeconds*float64(time.Second)) {
					select {
					case t.writeSignal <- true:
						// Signal sent successfully
					default:
						// Channel full, which means a write is already pending
					}
				}

				lastProcessTime = time.Now()
			}

			// Sleep briefly to prevent excessive CPU usage
			time.Sleep(200 * time.Millisecond)
		}
	}
}

// processAudioBatch sends audio data to Whisper and returns transcript segments
func (t *Transcriber) processAudioBatch(audioData []float32, source AudioSource, timestamp time.Time) ([]TranscriptSegment, error) {
	// Process with Whisper
	if err := t.context.Process(audioData, nil); err != nil {
		return nil, fmt.Errorf("whisper processing failed: %v", err)
	}

	// Extract segments
	n := t.context.SegmentCount()
	segments := make([]TranscriptSegment, 0, n)

	for i := 0; i < n; i++ {
		segment := t.context.Segment(i)
		if len(strings.TrimSpace(segment.Text)) > 0 {
			segments = append(segments, TranscriptSegment{
				Text:      segment.Text,
				StartTime: float64(segment.Start) / 100.0, // Convert to seconds
				EndTime:   float64(segment.End) / 100.0,   // Convert to seconds
				Source:    source,
				Timestamp: timestamp,
			})
		}
	}

	return segments, nil
}

// addSegments adds new transcript segments to the buffer
func (t *Transcriber) addSegments(newSegments []TranscriptSegment) {
	t.segmentsMutex.Lock()
	defer t.segmentsMutex.Unlock()

	t.segments = append(t.segments, newSegments...)
}

// writeSegments sorts and writes segments to the transcript file
func (t *Transcriber) writeSegments() {
	t.segmentsMutex.Lock()
	if len(t.segments) == 0 {
		t.segmentsMutex.Unlock()
		return
	}

	// Get a copy of all current segments and clear the buffer
	segments := make([]TranscriptSegment, len(t.segments))
	copy(segments, t.segments)
	t.segments = t.segments[:0]
	t.segmentsMutex.Unlock()

	// Sort segments by real timestamp first, then by segment start time
	sort.Slice(segments, func(i, j int) bool {
		// If timestamps are very close (within 1 second), sort by start time
		if segments[i].Timestamp.Sub(segments[j].Timestamp) < time.Second &&
			segments[j].Timestamp.Sub(segments[i].Timestamp) < time.Second {
			return segments[i].StartTime < segments[j].StartTime
		}
		return segments[i].Timestamp.Before(segments[j].Timestamp)
	})

	// Write segments to file
	for _, segment := range segments {
		sourceLabel := "MIC"
		if segment.Source == SourceSpeaker {
			sourceLabel = "SPK"
		}

		// Format timestamp
		timeStr := segment.Timestamp.Format("15:04:05")

		var line string
		if t.config.SaveTimestamps {
			startMin := int(segment.StartTime) / 60
			startSec := int(segment.StartTime) % 60

			line = fmt.Sprintf("[%s | %s | +%02d:%02d] %s\n",
				timeStr, sourceLabel, startMin, startSec, segment.Text)
		} else {
			line = fmt.Sprintf("[%s | %s] %s\n",
				timeStr, sourceLabel, segment.Text)
		}

		if _, err := t.transcriptFile.WriteString(line); err != nil {
			fmt.Printf("Error writing to transcript file: %v\n", err)
			continue
		}
	}

	// Flush to disk
	if err := t.transcriptFile.Sync(); err != nil {
		fmt.Printf("Error flushing transcript to disk: %v\n", err)
	}

	t.lastWriteTime = time.Now()

	// Print progress indicator
	fmt.Printf("\nWrote %d transcript segments\n", len(segments))
}

// GetTranscriptFilePath returns the path to the current transcript file
func (t *Transcriber) GetTranscriptFilePath() string {
	if t.transcriptFile == nil {
		return ""
	}
	return t.transcriptFile.Name()
}
