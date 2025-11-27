package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/kennygrant/sanitize"
	"github.com/muesli/goprogressbar"
)

// WriteProgressBar is an io.Writer that updates a download progress-bar
type WriteProgressBar struct {
	ProgressBar *goprogressbar.ProgressBar
}

// SizeToString returns a human-readable string for file-sizes
func SizeToString(size uint64) (str string) {
	b := float64(size)

	switch {
	case size >= 1<<60:
		str = fmt.Sprintf("%.2f EiB", b/(1<<60))
	case size >= 1<<50:
		str = fmt.Sprintf("%.2f PiB", b/(1<<50))
	case size >= 1<<40:
		str = fmt.Sprintf("%.2f TiB", b/(1<<40))
	case size >= 1<<30:
		str = fmt.Sprintf("%.2f GiB", b/(1<<30))
	case size >= 1<<20:
		str = fmt.Sprintf("%.2f MiB", b/(1<<20))
	case size >= 1<<10:
		str = fmt.Sprintf("%.2f KiB", b/(1<<10))
	default:
		str = fmt.Sprintf("%dB", size)
	}

	return
}

// Write updates the progress-bar
func (wc *WriteProgressBar) Write(p []byte) (int, error) {
	n := len(p)
	wc.ProgressBar.Current += int64(n)
	wc.ProgressBar.LazyPrint()

	return n, nil
}

func download(v Conference, e Event, m Recording) error {
	author := ""
	subtitle := ""
	lang := ""
	if len(e.Persons) > 0 {
		author = sanitize.BaseName(e.Persons[0]) + " - "
	}
	if len(e.Subtitle) > 0 {
		subtitle = " (" + sanitize.BaseName(e.Subtitle) + ")"
	}
	if e.OriginalLanguage != m.Language {
		lang = " [" + m.Language + "]"
	}

	path := filepath.Join(downloadPath, sanitize.Path(v.Title))
	basename := fmt.Sprintf("%s%s%s%s", author, sanitize.BaseName(e.Title), subtitle, lang) + "." + extensionForMimeTypes[m.MimeType]
	filename := filepath.Join(path, basename)

	var startByte int64
	if info, err := os.Stat(filename); err == nil {
		startByte = info.Size()
		// We don't trust m.Size (it's in MiB), so we always try to resume.
		// If the server returns 416, we know we are done.
		fmt.Printf("Checking resume for %s (local: %s)...\n", filename, SizeToString(uint64(startByte)))
	} else {
		os.MkdirAll(path, 0755)
	}

	var out *os.File
	var err error
	if startByte > 0 {
		out, err = os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0644)
	} else {
		fmt.Println("Downloading:", m.RecordingURL)
		out, err = os.Create(filename)
	}
	if err != nil {
		return err
	}
	defer out.Close()

	req, err := http.NewRequest("GET", m.RecordingURL, nil)
	if err != nil {
		return err
	}
	if startByte > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", startByte))
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		// 416 means we are already at the end of the file (or past it).
		// This implies the file is fully downloaded.
		fmt.Println("File already complete - skipping.")
		return nil
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	// If we got 200 OK but asked for Range, it means server ignored Range or file changed.
	// We should overwrite the file.
	if startByte > 0 && resp.StatusCode == http.StatusOK {
		fmt.Println("Server returned 200 OK (Range ignored/mismatch) - restarting download.")
		out.Close()
		out, err = os.Create(filename)
		if err != nil {
			return err
		}
		defer out.Close()
		startByte = 0
	}

	pb := &goprogressbar.ProgressBar{
		Text:    filename,
		Total:   startByte + resp.ContentLength,
		Current: startByte,
		Width:   60,
		PrependTextFunc: func(p *goprogressbar.ProgressBar) string {
			return fmt.Sprintf("%s / %s",
				SizeToString(uint64(p.Current)),
				SizeToString(uint64(p.Total)))
		},
	}

	src := io.TeeReader(resp.Body, &WriteProgressBar{ProgressBar: pb})
	_, err = io.Copy(out, src)
	if err != nil {
		return err
	}

	fmt.Println()
	return nil
}
