package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/kennygrant/sanitize"
	"github.com/olekukonko/tablewriter"
)

var (
	preferredMimeTypes    = []string{"video/webm", "video/mp4", "video/ogg", "audio/ogg", "audio/opus", "audio/mpeg", "application/x-subrip"}
	extensionForMimeTypes = make(map[string]string)

	downloadPath string
	name         string
	language     string
	source       string
)

// Conference represents a single conference
type Conference struct {
	Acronym             string `json:"acronym"`
	AspectRatio         string `json:"aspect_ratio"`
	EventLastReleasedAt string `json:"event_last_released_at"`
	ImagesURL           string `json:"images_url"`
	LogoURL             string `json:"logo_url"`
	RecordingsURL       string `json:"recordings_url"`
	ScheduleURL         string `json:"schedule_url"`
	Slug                string `json:"slug"`
	Title               string `json:"title"`
	UpdatedAt           string `json:"updated_at"`
	URL                 string `json:"url"`
	WebgenLocation      string `json:"webgen_location"`
}

// Conferences is a list of conferences
type Conferences struct {
	Conferences []Conference `json:"conferences"`
}

// ByTitle implements sort.Interface based on the Title field.
type ByTitle []Conference

func (a ByTitle) Len() int           { return len(a) }
func (a ByTitle) Less(i, j int) bool { return a[i].Title < a[j].Title }
func (a ByTitle) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// ByDate implements sort.Interface based on the EventLastReleasedAt field (Descending).
type ByDate []Conference

func (a ByDate) Len() int           { return len(a) }
func (a ByDate) Less(i, j int) bool { return a[i].EventLastReleasedAt > a[j].EventLastReleasedAt }
func (a ByDate) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

func priorityForMimeType(mime string) int {
	for i, v := range preferredMimeTypes {
		if strings.ToLower(mime) == v {
			return i
		}
	}

	return -1
}

func findConferences(url string) (Conferences, error) {
	ci := Conferences{}

	r, err := http.Get(url)
	if err != nil {
		return ci, err
	}
	defer r.Body.Close()

	err = json.NewDecoder(r.Body).Decode(&ci)
	return ci, err
}

func listConferences() {
	ci, err := findConferences(fmt.Sprintf("https://api.%s/public/conferences", source))
	if err != nil {
		panic(err)
	}

	sort.Sort(ByTitle(ci.Conferences))

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Conference", "Title"})
	table.SetBorders(tablewriter.Border{Left: false, Top: false, Right: false, Bottom: false})
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetAutoWrapText(false)

	for _, v := range ci.Conferences {
		table.Append([]string{v.Acronym, v.Title})
	}
	table.Render()
}

func selectBestRecording(e Event, media Media) (Recording, error) {
	bestMatch := Recording{}
	highestPriority := -1
	if len(media.Recordings) == 0 {
		return bestMatch, fmt.Errorf("no recordings found")
	}
	for _, m := range media.Recordings {
		if (len(language) == 0 || language != strings.ToLower(m.Language)) &&
			m.Language != e.OriginalLanguage {
			continue
		}

		prio := priorityForMimeType(m.MimeType)
		pick := false
		if highestPriority == -1 {
			pick = true
		}
		if strings.ToLower(bestMatch.Language) != language && strings.ToLower(m.Language) == language {
			pick = true
		} else {
			if prio < highestPriority || (prio == highestPriority && m.Width > bestMatch.Width) {
				pick = true
			}
		}

		if pick {
			highestPriority = prio
			bestMatch = m
		}
	}
	return bestMatch, nil
}

func main() {
	flag.StringVar(&name, "name", "", "download media of a specific conference only (e.g. '33c3')")
	flag.StringVar(&downloadPath, "destination", "./downloads/", "where to store downloaded media")
	flag.StringVar(&language, "language", "", "preferred language if available (eng, deu or fra)")
	flag.StringVar(&source, "source", "media.ccc.de", "source of conferences")
	flag.Parse()

	var listOnly bool
	if len(flag.Args()) > 0 {
		arg := strings.ToLower(flag.Args()[0])
		listOnly = arg == "list"
	}

	if listOnly {
		listConferences()
		return
	}

	name = strings.ToLower(name)
	language = strings.ToLower(language)
	source = strings.ToLower(source)

	extensionForMimeTypes["video/webm"] = "webm"
	extensionForMimeTypes["video/mp4"] = "mp4"
	extensionForMimeTypes["video/ogg"] = "ogm"
	extensionForMimeTypes["audio/ogg"] = "ogg"
	extensionForMimeTypes["audio/opus"] = "opus"
	extensionForMimeTypes["audio/mpeg"] = "mp3"

	ci, err := findConferences(fmt.Sprintf("https://api.%s/public/conferences", source))
	if err != nil {
		panic(err)
	}

	// Sort conferences by date (newest first)
	sort.Sort(ByDate(ci.Conferences))

	// Pre-scan to count total videos and size
	fmt.Println("Scanning conferences to count total videos and size... (this WILL take a while)")
	totalVideos := 0
	var totalSize int64 = 0
	conferenceEvents := make(map[string]Events)
	eventRecordings := make(map[string]Recording)
	totalConferences := len(ci.Conferences)

	for i, v := range ci.Conferences {
		if len(name) > 0 && name != strings.ToLower(v.Acronym) {
			continue
		}
		fmt.Printf("\rScanning conference [%d/%d]: %s", i+1, totalConferences, v.Acronym)
		events, err := findEvents(v.URL)
		if err != nil {
			continue
		}
		conferenceEvents[v.Acronym] = events
		
		for _, e := range events.Events {
			media, err := findMedia(e.URL)
			if err != nil {
				continue
			}
			rec, err := selectBestRecording(e, media)
			if err == nil && len(rec.RecordingURL) > 0 {
				totalVideos++
				totalSize += rec.Size * 1024 * 1024 
				
				eventRecordings[e.URL] = rec
			}
		}
	}
	
	// Convert total size to bytes for display if it was in MiB
	// Actually, let's keep totalSize in MiB for now to match m.Size, 
	// but for "Storage consumed" we might want bytes.
	// Let's normalize everything to Bytes for the progress display.
	// If m.Size is MiB, then totalSizeBytes = totalSize * 1024 * 1024.
	totalSizeBytes := totalSize // totalSize is ALREADY bytes because we multiplied above
	
	fmt.Printf("\nTotal videos: %d, Total Size: %s\n\n", totalVideos, SizeToString(uint64(totalSizeBytes)))

	found := false
	currentVideo := 0
	var currentConsumedBytes int64 = 0
	
	for i, v := range ci.Conferences {
		if len(name) > 0 && name != strings.ToLower(v.Acronym) {
			continue
		}
		fmt.Printf("Conference [%d/%d]: %s (%s)\n", i+1, totalConferences, v.Acronym, v.Title)
		found = true

		events, ok := conferenceEvents[v.Acronym]
		if !ok {
			continue
		}

		for _, e := range events.Events {
			rec, ok := eventRecordings[e.URL]
			if !ok {
				continue
			}
			
			currentVideo++
			desc := strings.Replace(sanitize.HTML(e.Description), "\n", "", -1)
			if len(desc) > 48 {
				desc = desc[:45] + "..."
			}
			if len(desc) > 0 {
				desc = " - " + desc
			}
			
			// Calculate remaining
			remainingBytes := totalSizeBytes - currentConsumedBytes
			
			fmt.Printf("Video [%d/%d]: %s%s\n", currentVideo, totalVideos, e.Title, desc)
			fmt.Printf("Storage: Downloaded: %s | Total: %s | Remaining: %s\n", 
				SizeToString(uint64(currentConsumedBytes)), 
				SizeToString(uint64(totalSizeBytes)), 
				SizeToString(uint64(remainingBytes)))

			if rec.Width == 0 {
				fmt.Printf("\tFound other/audio (%s): %d minutes (HD: %t, %dMiB) %s\n", rec.MimeType, rec.Length/60, rec.HighQuality, rec.Size, rec.URL)
			} else {
				fmt.Printf("\tFound video (%s): %d minutes, %dx%d (HD: %t, %dMiB) %s\n", rec.MimeType, rec.Length/60, rec.Width, rec.Height, rec.HighQuality, rec.Size, rec.URL)
			}

			err = download(v, e, rec)
			if err != nil {
				fmt.Println("Error downloading:", err)
			} else {
				// Only increment consumed if download successful (or skipped)
				currentConsumedBytes += rec.Size * 1024 * 1024
			}

			fmt.Println()
		}
	}

	if found {
		fmt.Println("Done.")
	} else {
		fmt.Println("Couldn't find any conference with acronym", name)
	}
}
