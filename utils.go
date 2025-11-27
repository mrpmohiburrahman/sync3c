package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Config constants - assuming whisper.cpp is in the current directory or a known location
var (
	whisperCliPath   = "/Users/mrp/Documents/1-Projects/huberman-lab-podcasts/hubernman-podcasts-scrape/whisper.cpp/build/bin/whisper-cli"
	whisperModelPath = "/Users/mrp/Documents/1-Projects/huberman-lab-podcasts/hubernman-podcasts-scrape/whisper.cpp/models/ggml-base.en.bin"
	statusFile       = "video_status.json"
)

var srtIndexPattern = regexp.MustCompile(`^\d+$`)

// VideoStatus represents the status of a video in the ledger
type VideoStatus struct {
	Downloaded        bool   `json:"downloaded"`
	SubtitleGenerated bool   `json:"subtitle_generated"`
	TxtPath           string `json:"txt_path"`
	VideoURL          string `json:"video_url"`
}

// transcribeAndCleanup handles the full workflow: extract audio, transcribe, convert to txt, cleanup
func transcribeAndCleanup(videoPath, title, apiURL, frontendURL string) error {
	fmt.Printf("Processing transcription for: %s\n", videoPath)

	// 1. Extract Audio
	wavPath := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".wav"
	if err := extractAudio(videoPath, wavPath); err != nil {
		return fmt.Errorf("failed to extract audio: %w", err)
	}
	defer os.Remove(wavPath) // Cleanup WAV

	// 2. Generate Subtitle (SRT)
	srtPath := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".srt"
	// Check if SRT already exists to avoid re-running whisper (optional optimization)
	if _, err := os.Stat(srtPath); os.IsNotExist(err) {
		if err := generateSubtitle(wavPath, srtPath); err != nil {
			return fmt.Errorf("failed to generate subtitle: %w", err)
		}
	} else {
		fmt.Printf("SRT already exists: %s\n", srtPath)
	}

	// 3. Convert SRT to TXT with header
	txtPath := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".txt"
	if err := srtToTxt(srtPath, txtPath, apiURL, frontendURL, title); err != nil {
		return fmt.Errorf("failed to convert srt to txt: %w", err)
	}

	// 4. Cleanup Original Video
	if err := os.Remove(videoPath); err != nil {
		fmt.Printf("Warning: failed to delete video file %s: %v\n", videoPath, err)
	} else {
		fmt.Printf("Deleted original video: %s\n", videoPath)
	}

	return nil
}

// extractAudio extracts 16kHz mono WAV using ffmpeg, preferring English audio
func extractAudio(videoPath, wavPath string) error {
	// Select preferred audio stream
	streamSelector, err := selectPreferredAudioStream(videoPath)
	if err != nil {
		fmt.Printf("Warning: could not determine preferred audio stream, defaulting to 0:a:0. Error: %v\n", err)
		streamSelector = "0:a:0"
	}

	cmd := exec.Command("ffmpeg", "-y",
		"-i", videoPath,
		"-map", streamSelector,
		"-ac", "1",
		"-ar", "16000",
		"-sample_fmt", "s16",
		wavPath,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// selectPreferredAudioStream returns an ffmpeg stream selector string preferring English audio
func selectPreferredAudioStream(videoPath string) (string, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "a",
		"-show_entries", "stream=index:stream_tags=language,title",
		"-of", "json",
		videoPath,
	)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	var result struct {
		Streams []struct {
			Index int               `json:"index"`
			Tags  map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return "", err
	}

	if len(result.Streams) == 0 {
		return "", fmt.Errorf("no audio streams found")
	}

	isEnglish := func(tags map[string]string) bool {
		for _, v := range tags {
			val := strings.ToLower(v)
			if strings.Contains(val, "english") || val == "eng" || val == "en" {
				return true
			}
		}
		return false
	}

	for i, stream := range result.Streams {
		if isEnglish(stream.Tags) {
			return fmt.Sprintf("0:a:%d", i), nil
		}
	}

	return "0:a:0", nil // Fallback
}

// generateSubtitle runs whisper-cli to generate SRT
func generateSubtitle(wavPath, srtPath string) error {
	// whisper-cli output filename logic is a bit specific, it appends extension.
	// If we pass -of /path/to/file, it might write /path/to/file.srt
	// Let's use the base name and let it write to the same directory.

	// Ensure whisper-cli exists
	if _, err := os.Stat(whisperCliPath); os.IsNotExist(err) {
		return fmt.Errorf("whisper-cli not found at %s", whisperCliPath)
	}
	if _, err := os.Stat(whisperModelPath); os.IsNotExist(err) {
		return fmt.Errorf("whisper model not found at %s", whisperModelPath)
	}

	// We want the output filename to match srtPath (without extension, as whisper adds it)
	outputBase := strings.TrimSuffix(srtPath, ".srt")

	cmd := exec.Command(whisperCliPath,
		"-m", whisperModelPath,
		"-f", wavPath,
		"-osrt", // Output SRT
		"-of", outputBase,
		"--print-colors",
	)

	// Set working directory to the file's directory to avoid path issues if needed,
	// but absolute paths should work.

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// srtToTxt converts SRT to TXT with a custom header
func srtToTxt(srtPath, txtPath, apiURL, frontendURL, title string) error {
	srtContent, err := os.ReadFile(srtPath)
	if err != nil {
		return err
	}
	cleaned := cleanSRTContent(string(srtContent))

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("API URL: %s\n", apiURL))
	if frontendURL != "" {
		buf.WriteString(fmt.Sprintf("Frontend URL: %s\n", frontendURL))
	}
	buf.WriteString(fmt.Sprintf("Title: %s\n\n", title))
	if cleaned != "" {
		buf.WriteString(cleaned)
		buf.WriteString("\n")
	}

	if err := os.WriteFile(txtPath, buf.Bytes(), 0644); err != nil {
		return err
	}

	if err := os.Remove(srtPath); err != nil && !os.IsNotExist(err) {
		fmt.Printf("Warning: failed to delete SRT file %s: %v\n", srtPath, err)
	}

	return nil
}

func cleanSRTContent(content string) string {
	lines := strings.Split(content, "\n")
	cleaned := make([]string, 0, len(lines))

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if srtIndexPattern.MatchString(trimmed) {
			continue
		}
		if isTimecodeLine(trimmed) {
			continue
		}
		cleaned = append(cleaned, trimmed)
	}

	return strings.Join(cleaned, "\n")
}

func isTimecodeLine(line string) bool {
	start, end, found := strings.Cut(line, " --> ")
	if !found {
		return false
	}

	start = strings.TrimSpace(start)
	end = strings.TrimSpace(end)

	if _, err := parseSRTTimestamp(start); err != nil {
		return false
	}
	if _, err := parseSRTTimestamp(end); err != nil {
		return false
	}

	return true
}

func parseSRTTimestamp(value string) (float64, error) {
	timePart, msPart, ok := strings.Cut(value, ",")
	if !ok {
		return 0, fmt.Errorf("invalid timestamp: %s", value)
	}

	fields := strings.Split(timePart, ":")
	if len(fields) != 3 {
		return 0, fmt.Errorf("invalid time part: %s", value)
	}

	hours, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, err
	}
	minutes, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, err
	}
	seconds, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, err
	}
	milliseconds, err := strconv.Atoi(msPart)
	if err != nil {
		return 0, err
	}

	total := float64(hours*3600+minutes*60+seconds) + float64(milliseconds)/1000.0
	return total, nil
}

// updateStatus updates the JSON ledger
func updateStatus(status map[string]map[string]VideoStatus, conference, title, videoPath, videoURL string) error {
	if status[conference] == nil {
		status[conference] = make(map[string]VideoStatus)
	}

	existing := status[conference][title]
	if videoURL != "" {
		existing.VideoURL = videoURL
	}

	if videoPath != "" {
		txtPath := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".txt"
		existing.TxtPath = txtPath
		existing.SubtitleGenerated = true
	}

	existing.Downloaded = true
	status[conference][title] = existing

	return saveStatus(status)
}

func saveStatus(status map[string]map[string]VideoStatus) error {
	updatedContent, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(statusFile, updatedContent, 0644)
}
