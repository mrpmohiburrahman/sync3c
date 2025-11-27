package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

type eventTask struct {
	event     Event
	recording Recording
}

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

func gatherEventTasks(conf Conference) ([]eventTask, error) {
	events, err := findEvents(conf.URL)
	if err != nil {
		return nil, err
	}

	var tasks []eventTask
	for _, e := range events.Events {
		media, err := findMedia(e.URL)
		if err != nil {
			continue
		}
		rec, err := selectBestRecording(e, media)
		if err != nil {
			continue
		}
		if len(rec.RecordingURL) == 0 {
			continue
		}
		tasks = append(tasks, eventTask{
			event:     e,
			recording: rec,
		})
	}

	return tasks, nil
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

	// Load existing status JSON (if any)
	statusMap := make(map[string]map[string]VideoStatus)
	if data, err := os.ReadFile(statusFile); err == nil {
		_ = json.Unmarshal(data, &statusMap)
	}

	currentAcronyms := make(map[string]bool)
	for acronym := range statusMap {
		currentAcronyms[strings.ToLower(acronym)] = true
	}

	restrictToCurrent := len(name) == 0 && len(currentAcronyms) > 0
	var targetConfs []Conference
	var remainingConfs []Conference

	for _, conf := range ci.Conferences {
		acronym := strings.ToLower(conf.Acronym)

		if len(name) > 0 {
			if acronym == name {
				targetConfs = append(targetConfs, conf)
			} else {
				remainingConfs = append(remainingConfs, conf)
			}
			continue
		}

		if restrictToCurrent && !currentAcronyms[acronym] {
			remainingConfs = append(remainingConfs, conf)
			continue
		}

		targetConfs = append(targetConfs, conf)
	}

	if len(targetConfs) == 0 {
		targetConfs = ci.Conferences
		remainingConfs = nil
		restrictToCurrent = false
	}

	if restrictToCurrent {
		fmt.Printf("Resuming %d conferences with existing progress. Remaining conferences skipped for now: %d\n\n",
			len(targetConfs), len(remainingConfs))
	}

	found := false
	totalConferences := len(targetConfs)

	for idx, conf := range targetConfs {
		fmt.Printf("Conference [%d/%d]: %s (%s)\n", idx+1, totalConferences, conf.Acronym, conf.Title)

		tasks, err := gatherEventTasks(conf)
		if err != nil {
			fmt.Printf("  Error fetching events: %v\n\n", err)
			continue
		}

		if len(tasks) == 0 {
			fmt.Println("  No downloadable videos found.\n")
			continue
		}

		found = true

		fmt.Printf("  Videos queued: %d | Remaining current conferences: %d | Deferred conferences: %d\n",
			len(tasks), totalConferences-idx-1, len(remainingConfs))

		for videoIdx, task := range tasks {
			e := task.event
			rec := task.recording

			desc := strings.Replace(sanitize.HTML(e.Description), "\n", "", -1)
			if len(desc) > 48 {
				desc = desc[:45] + "..."
			}
			if len(desc) > 0 {
				desc = " - " + desc
			}

			alreadyProcessed := false
			if confStatus, ok := statusMap[conf.Acronym]; ok {
				if vidStatus, ok := confStatus[e.Title]; ok && vidStatus.Downloaded {
					alreadyProcessed = true
				}
			}

			author := ""
			subtitle := ""
			lang := ""
			if len(e.Persons) > 0 {
				author = sanitize.BaseName(e.Persons[0]) + " - "
			}
			if len(e.Subtitle) > 0 {
				subtitle = " (" + sanitize.BaseName(e.Subtitle) + ")"
			}
			if e.OriginalLanguage != rec.Language {
				lang = " [" + rec.Language + "]"
			}
			path := filepath.Join(downloadPath, sanitize.Path(conf.Title))
			basename := fmt.Sprintf("%s%s%s%s", author, sanitize.BaseName(e.Title), subtitle, lang) + "." + extensionForMimeTypes[rec.MimeType]
			fullVideoPath := filepath.Join(path, basename)
			txtPath := strings.TrimSuffix(fullVideoPath, filepath.Ext(fullVideoPath)) + ".txt"

			if !alreadyProcessed {
				if _, err := os.Stat(txtPath); err == nil {
					fmt.Printf("  Found existing transcript for %s. Updating ledger.\n", e.Title)
					if err := updateStatus(statusMap, conf.Acronym, e.Title, fullVideoPath, rec.URL); err != nil {
						fmt.Println("  Error updating status JSON:", err)
					}
					alreadyProcessed = true
				} else if _, err := os.Stat(fullVideoPath); err == nil {
					fmt.Printf("  Found existing video for %s. Transcribing.\n", e.Title)
				}
			}

			if alreadyProcessed {
				continue
			}

			fmt.Printf("  Video [%d/%d]: %s%s\n", videoIdx+1, len(tasks), e.Title, desc)
			fmt.Printf("    Remaining videos in this conference: %d | Remaining conferences (including deferred): %d\n",
				len(tasks)-(videoIdx+1), totalConferences-idx-1+len(remainingConfs))

			if rec.Width == 0 {
				fmt.Printf("\tFound other/audio (%s): %d minutes (HD: %t, %dMiB) %s\n", rec.MimeType, rec.Length/60, rec.HighQuality, rec.Size, rec.URL)
			} else {
				fmt.Printf("\tFound video (%s): %d minutes, %dx%d (HD: %t, %dMiB) %s\n", rec.MimeType, rec.Length/60, rec.Width, rec.Height, rec.HighQuality, rec.Size, rec.URL)
			}

			filename, err := download(conf, e, rec)
			if err != nil {
				fmt.Println("Error downloading:", err)
				continue
			}

			if err := transcribeAndCleanup(filename, e.Title, rec.URL, e.FrontendLink); err != nil {
				fmt.Println("Error processing video:", err)
			}

			if err := updateStatus(statusMap, conf.Acronym, e.Title, filename, rec.URL); err != nil {
				fmt.Println("Error updating status JSON:", err)
			}

			fmt.Println()
		}

		if totalConferences-idx-1 > 0 || len(remainingConfs) > 0 {
			fmt.Printf("  Remaining current conferences: %d | Deferred conferences: %d\n\n",
				totalConferences-idx-1, len(remainingConfs))
		} else {
			fmt.Println()
		}
	}

	if found {
		fmt.Println("Done.")
	} else {
		fmt.Println("Couldn't find any conference with acronym", name)
	}

	if len(remainingConfs) > 0 {
		fmt.Printf("\nRemaining conferences not scanned in this run (%d):\n", len(remainingConfs))
		maxPreview := 10
		for i, conf := range remainingConfs {
			if i >= maxPreview {
				fmt.Printf("  ...and %d more\n", len(remainingConfs)-maxPreview)
				break
			}
			fmt.Printf("  - %s (%s)\n", conf.Acronym, conf.Title)
		}
	}
}
