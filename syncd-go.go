// syncwatcher.go
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/cenkalti/backoff"
	"github.com/syncthing/syncthing/lib/ignore"
	"github.com/zillode/notify"
)


// Event holds full event data coming from Syncthing REST API
type Event struct {
	ID   int         `json:"id"`
	Time time.Time   `json:"time"`
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

type FSEvent struct {
	Path     string
	Action   string
	// Type     string
}

type folderSlice []string

type progressTime struct {
	fsEvent bool // true - event was triggered by filesystem, false - by Syncthing
	time    time.Time
	// action string
}

func (fs *folderSlice) String() string {
	return fmt.Sprint(*fs)
}
func (fs *folderSlice) Set(value string) error {
	for _, f := range strings.Split(value, ",") {
		*fs = append(*fs, f)
	}
	return nil
}



// HTTP Debounce
var (
	debounceTimeout   = 500 * time.Millisecond
	configSyncTimeout = 5 * time.Second
	fsEventTimeout    = 500 * time.Millisecond
	dirVsFiles        = 128
	maxFiles          = 512
)

type funcExcludeFrom func(string) bool

// Main
var (
	stop          = make(chan int)
	versionFolder = ".syversions"
	logFd         = os.Stdout
	Version       = "unknown-dev"
	Discard       = log.New(ioutil.Discard, "", log.Ldate)
	Warning       = Discard // verbosity=1
	OK            = Discard // 2
	Monitor       = Discard // 2
	Local    	  = Discard // 2
	Remote        = Discard // 2
	Trace         = Discard // 3
	Debug         = Discard // 4
	watchFolders  folderSlice
	skipFolders   folderSlice
	delayScan     = 3600
	excludeFrom   = ""

	excludeFromFilter  funcExcludeFrom
	configFile         string
	gCfg               Configuration
	gHome               string
	gRsync               string
)

const (
	pathSeparator = string(os.PathSeparator)
	usage         = "syncthing-inotify [options]"
	extraUsage    = `
The -logflags value is a sum of the following:

   1  Date
   2  Time
   4  Microsecond time
   8  Long filename
  16  Short filename

I.e. to prefix each log line with date and time, set -logflags=3 (1 + 2 from
above). The value 0 is used to disable all of the above. The default is to
show time only (2).`
)

func setupLogging(verbosity int, logflags int, monitor int) {
	if verbosity >= 1 {
		Warning = log.New(logFd, "[WARNING] ", logflags)
	}
	if verbosity >= 2 {
		OK = log.New(logFd, "[OK] ", logflags)
	}
	if verbosity >= 3 {
		Trace = log.New(logFd, "[TRACE] ", logflags)
	}
	if verbosity >= 4 {
		Debug = log.New(logFd, "[DEBUG] ", logflags)
	}

	if monitor == 1  {
		Remote  = log.New(logFd, "[REMOTE] ", logflags)
		Monitor = log.New(logFd, "[CONFIG] ", logflags)
	} else if monitor == 2  {
		Local   = log.New(logFd, "[LOCAL] ", logflags)
	} else if monitor == 3  {
		Remote  = log.New(logFd, "[REMOTE] ", logflags)
		Local   = log.New(logFd, "[LOCAL] ", logflags)
	}
}

func init() {

	var logFile string
	var verbosity int
	var monitor int
	var logflags int
	// var apiKeyStdin bool
	// var authPassStdin bool
	var showVersion bool

	flag.DurationVar(&debounceTimeout, "interval", debounceTimeout,
		"Accumulation interval, e.g. 5s or 1m")
	flag.StringVar(&logFile, "logfile", "", "Log file")
	flag.StringVar(&configFile, "configfile", "config.xml", "Config file")
	flag.IntVar(&verbosity, "verbosity", 2, "Logging level [1..4]")
	flag.IntVar(&monitor, "monitor", 3, "monitor file change level [1..3]")
	flag.IntVar(&logflags, "logflags", 2, "Select information in log line prefix")
	flag.StringVar(&gHome, "home", "", "Specify the home Syncthing dir to sniff configuration settings")
	flag.StringVar(&gRsync, "rsync", "", "rsync exe path")

	flag.Var(&watchFolders, "folders", "A comma-separated list of folder labels or IDs to watch (all by default)")
	flag.Var(&skipFolders, "skip-folders", "A comma-separated list of folder labels or IDs to skip inotify watching")
	flag.StringVar(&excludeFrom, "excludeFrom", "", "Ignoring Files, refer .syignore")
	flag.IntVar(&delayScan, "delay-scan", delayScan, "Automatically delay next scan interval (in seconds)")
	flag.BoolVar(&showVersion, "version", false, "Show version")

	flag.Usage = usageFor(flag.CommandLine, usage, fmt.Sprintf(extraUsage))
	flag.Parse()

	if showVersion {
		fmt.Printf("syncthing-inotify %s (%s %s-%s)\n", Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	if len(gHome) > 0 {

		if logFile == filepath.Base(logFile) {
			logFile = filepath.Join(gHome, configFile)
		}

		if configFile == filepath.Base(configFile) {
			configFile = filepath.Join(gHome, configFile)
		}
	}

	if len(logFile) > 0 {
		var err error
		logFd, err = os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			log.Fatalln(err)
		}
	}

	setupLogging(verbosity, logflags, monitor)

	if len(watchFolders) != 0 && len(skipFolders) != 0 {
		log.Fatalln("Either provide a list of folders to be watched or to be ignored, not both.")
	}
	if delayScan > 0 && delayScan < 60 {
		log.Fatalln("A delay scan interval shorter than 60 is not supported.")
	}

	if len(excludeFrom) > 0 && excludeFrom == filepath.Base(excludeFrom) {
		excludeFrom = filepath.Join(getCurrentDirectory(), excludeFrom)
	}
}


// main reads configs, starts all gouroutines and waits until a message is in channel stop.
func main() {
	backoff.Retry(testWebGuiPost, backoff.NewExponentialBackOff())
	// Attempt to increase the limit on number of open files to the maximum allowed.
	MaximizeOpenFileLimit()

	if configFile == filepath.Base(configFile) {
		configFile = filepath.Join(getCurrentDirectory(), configFile)
	}

	OK.Printf("Beginnig: %s", configFile)
	// gCfg =
	allFolders, cfg, err := getFolders(configFile)
	gCfg = cfg.Copy()

	OK.Printf("Version: %v, MyID: %v, rsync: %s", gCfg.Version, gCfg.MyID, gCfg.Rsync.Path)

	if err != nil {

		// Fatalln 程序会退出
		log.Fatalf("Read config failed: %s, %s", configFile, err.Error())
	}

	folders := filterFolders(allFolders)

	for k, folder := range folders {
		Debug.Println("Installing watch for " + folder.Label)

		// go watchFolder(folder)
		go watchFolder(&folders[k])
	}

	code := <-stop

	for _, folder := range folders {

		filterFile := filepath.Join(folder.SrcRealPath, ".syignore")
		Debug.Println("Remove .syignore: ", filterFile)
		os.Remove(filterFile)
	}
	OK.Println("Exiting: ", code)
	os.Exit(code)
}

// Restart uses path to itself and copy of environment to start new process.
// Then it sends message to stop channel to shutdown itself.
func restart() bool {
	pgm, err := exec.LookPath(os.Args[0])
	if err != nil {
		Warning.Println("Cannot restart:", err)
		return false
	}
	env := os.Environ()
	newEnv := make([]string, 0, len(env))
	for _, s := range env {
		newEnv = append(newEnv, s)
	}
	proc, err := os.StartProcess(pgm, os.Args, &os.ProcAttr{
		Env:   newEnv,
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	})
	if err != nil {
		Warning.Println("Cannot restart:", err)
		return false
	}
	proc.Release()
	stop <- 3
	// OK.Println("Restart successfully.")
	return true
}

func getCurrentDirectory() string {
	dir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		log.Fatal(err)
	}
	// return strings.Replace(dir, "\\", "/", -1)
	return dir
}

func createFolder(path string) (bool, string) {

	path = expandTilde(path)

	info, err := os.Stat(path);

	if err == nil && info.IsDir() {

		return true, ""
	} else if err == nil  {
		return false, "path isn't a dir"
	} else if err != nil && os.IsNotExist(err) {
		err := os.MkdirAll(path, os.ModePerm)
		if err != nil {
			return false, err.Error()
		}
		return true, "create path"
	}
	return false, err.Error()
}

// filterFolders refines folders list using global vars watchFolders and skipFolders
func filterFolders(folders []FolderConfiguration) []FolderConfiguration {
	if len(watchFolders) > 0 {
		var fs []FolderConfiguration
		for _, f := range folders {
			for _, watch := range watchFolders {
				if f.ID == watch || f.Label == watch {
					fs = append(fs, f)
					break
				}
			}
		}
		return fs
	}
	if len(skipFolders) > 0 {
		var fs []FolderConfiguration
		for _, f := range folders {
			keep := true
			for _, skip := range skipFolders {
				if f.ID == skip || f.Label == skip {
					keep = false
					break
				}
			}
			if keep {
				fs = append(fs, f)
			}
		}
		return fs
	}
	return folders
}

// getFolders returns the list of folders configured in Syncthing. Blocks until ST responded successfully.
func getFolders(path string) ([]FolderConfiguration, Configuration, error) {
	Trace.Println("Getting Folders")

	fd, err := os.Open(path)
	if err != nil {
		return []FolderConfiguration{}, Configuration{}, err
	}

	defer fd.Close()

	cfg, err := ReadXML(fd, "")
	if err != nil {
		return []FolderConfiguration{}, Configuration{}, err
	}

	var fs []FolderConfiguration

	for k, f := range cfg.Folders {

		if f.Monitor == "yes" {

			if len(f.Label) == 0 {
				f.Label = f.ID
			}

			fs = append(fs, f)

			OK.Printf("folder[%d] id: %s, Source: %s, Target: %s, label: %s, filter: %d", k, f.ID, f.Source, f.Target, f.Label, len(f.Filter))

		}
	}

	if len(gRsync) > 0 {
		cfg.Rsync.Path = gRsync
	}

	// if _, err = os.Stat(cfg.Rsync.Path); os.IsNotExist(err) {
	// 	cfg.Rsync.Path = filepath.Join(gHome, cfg.Rsync.Path)
	// }

	return fs, cfg, nil
}

// watchFolder installs inotify watcher for a folder, launches
// goroutine which receives changed items. It never exits.
func watchFolder(folder *FolderConfiguration) {

	CreateDirectory(folder.Source)
	CreateDirectory(folder.Target)

	folderPath, err := realPath(expandTilde(folder.Source))

	if err != nil {
		Warning.Println("Failed to install inotify handler for "+folder.Label+".", err)
		informError("Failed to install inotify handler for " + folder.Label + ": " + err.Error())
		return
	}

	dstFolderPath, err := realPath(expandTilde(folder.Target))
	if err != nil {
		Warning.Println("Failed to create dest directory for "+folder.Label+".", err)
		informError("Failed to create dest directory for " + folder.Label + ": " + err.Error())
		return
	}

	folder.SrcRealPath     = folderPath
	folder.DstRealPath     = dstFolderPath
	folder.SrcCygdrivePath = TransformCygDrivePath(folder.SrcRealPath)
	folder.DstCygdrivePath = TransformCygDrivePath(folder.DstRealPath)

	gCfg.Rsync.SyncDir(*folder)

	Trace.Println("Getting ignore patterns for " + folder.Label)
	ignoreFilter := createIgnoreFilter(folderPath, folder.Filter)
	absIgnoreFilter := func(absPath string) bool {
		return ignoreFilter(relativePath(absPath, folderPath))
	}
	//
	excludeFromFilter = createExcludeFromIgnoreFilter(excludeFrom)

	// fsInput := make(chan string)
	fsInput := make(chan FSEvent)
	c := make(chan notify.EventInfo, maxFiles)
	if err := notify.WatchWithFilter(filepath.Join(folderPath, "..."), c,
		absIgnoreFilter, notify.All); err != nil {
		if strings.Contains(err.Error(), "too many open files") || strings.Contains(err.Error(), "no space left on device") {
			msg := "Failed to install inotify handler for " + folder.Label + ". Please increase inotify limits, see http://bit.ly/1PxkdUC for more information."
			Warning.Println(msg, err)
			informError(msg)
			return
		} else {
			Warning.Println("Failed to install inotify handler for "+folder.Label+".", err)
			informError("Failed to install inotify handler for " + folder.Label + ": " + err.Error())
			return
		}
	}
	defer notify.Stop(c)

	go accumulateChanges(debounceTimeout, folderPath, folderPath, *folder, dirVsFiles, fsInput, informChange)
	OK.Println("Watching " + folder.Label + ": " + folderPath)
	if folder.RescanIntervalS < 1800 && delayScan <= 0 {
		OK.Printf("The rescan interval of folder %s can be increased to 3600 (an hour) or even 86400 (a day) as changes should be observed immediately while syncthing-inotify is running.", folder.Label)
	}
	// will we ever get out of this loop?
	for {
		evAbsolutePath, aEvent := waitForEvent(c)
		Debug.Println("Change detected in: " + evAbsolutePath + " (could still be ignored)")
		evRelPath := relativePath(evAbsolutePath, folderPath)
		if ignoreFilter(evRelPath) || excludeFromFilter(evRelPath) {
			Debug.Println("Ignoring", evAbsolutePath)
			continue
		}
		Trace.Printf("[%v] Change detected in: %v", aEvent, evAbsolutePath)
		aEvent = strings.ToLower(strings.Replace(aEvent, "notify.", "", 1))
		fsInput <- FSEvent{Path: evRelPath, Action: aEvent}
	}
}

func accumulateChanges(debounceTimeout time.Duration,
	folder string,
	folderPath string,
	// dstFolderPath string,
	folderCfg FolderConfiguration,
	dirVsFiles int,
	fsInput chan FSEvent,
	callback InformCallback) func(string) {
	var delayScanInterval time.Duration
	if delayScan > 0 {
		delayScanInterval = time.Duration(delayScan-5) * time.Second
		Debug.Printf("Delay scan reminder interval for %s set to %.0f seconds\n", folder, delayScanInterval.Seconds())
	} else {
		// If delayScan is set to 0, then we never send requests to delay full scans.
		// "9999 * time.Hour" here is an approximation of "forever".
		delayScanInterval = 9999 * time.Hour
		Debug.Println("Delay scan reminders are disabled")
	}
	inProgress := make(map[string]progressTime) // [path string]{fs, start}
	currInterval := delayScanInterval           // Timeout of the timer
	if delayScan > 0 {
		askToDelayScan(folder, callback, folderCfg)
	}
	nextScanTime := time.Now().Add(delayScanInterval) // Time to remind Syncthing to delay scan
	flushTimer := time.NewTimer(0)
	flushTimerNeedsReset := true

	for {
		if flushTimerNeedsReset {
			flushTimerNeedsReset = false
			if !flushTimer.Stop() {
				select {
				case <-flushTimer.C:
				default:
				}
			}
			flushTimer.Reset(currInterval)
		}
		select {
		case item2 := <-fsInput:
			item := item2.Path
			if currInterval != debounceTimeout {
				currInterval = debounceTimeout
				flushTimerNeedsReset = true
				Debug.Println("[FS] Incoming Changes for "+folder+", speeding up inotify timeout parameters to", debounceTimeout)
			} else {
				Debug.Println("[FS] Incoming Changes for " + folder)
			}
			p, ok := inProgress[item]
			if ok && !p.fsEvent {
				// Change originated from ST
				delete(inProgress, item)
				Debug.Println("[FS] Removed tracking for " + item)
				continue
			}
			if len(inProgress) > maxFiles {
				Debug.Println("[FS] Tracking too many files, aggregating FSEvent: " + item)
				continue
			}
			// Debug.Printf("[FS] [%v] Tracking: %v", item2.Action, item)
			Trace.Printf("[FS] [%v] Tracking: %v", item2.Action, item)
			inProgress[item] = progressTime{true, time.Now()}
			// add by simon 2017.07.12
			srcFilePath := folderPath + string(filepath.Separator) + item

			Local.Printf("%v %v", item2.Action, srcFilePath)

			if folderCfg.SyncType != "more" {
				ok, strerr := gCfg.Rsync.Sync(folderCfg, item2.Action, []string{item})

				if ! ok {
					Warning.Printf("sync failed %s, file: %s", strerr, srcFilePath)
				}
			}
			if srcFilePath == configFile {
				// status := restart()
				if restart() {
					OK.Println("Restart successfully.")
				} else {
					OK.Println("Restart failed.")
				}
			}
			// if IsConfigFile(item) {
			// 	Monitor.Printf("%v %v", item2.Action, srcFilePath)
			// }
		case <-flushTimer.C:
			flushTimerNeedsReset = true
			if delayScan > 0 && nextScanTime.Before(time.Now()) {
				nextScanTime = time.Now().Add(delayScanInterval)
				askToDelayScan(folder, callback, folderCfg)
			}
			if len(inProgress) == 0 {
				if currInterval != delayScanInterval {
					Debug.Println("Slowing down inotify timeout parameters for " + folder)
					currInterval = delayScanInterval
				}
				continue
			}
			Debug.Println("Timeout AccumulateChanges")
			var err error
			var paths []string
			expiry := time.Now().Add(-debounceTimeout * 10)
			if len(inProgress) < maxFiles {
				for path, progress := range inProgress {
					// Clean up invalid and expired paths
					if path == "" || (!progress.fsEvent && progress.time.Before(expiry)) {
						delete(inProgress, path)
						continue
					}
					if progress.fsEvent && time.Now().Sub(progress.time) > fsEventTimeout {
						paths = append(paths, path)
						Debug.Println("Informing about " + path)
					} else {
						Debug.Println("Waiting for " + path)
					}
				}
				if len(paths) == 0 {
					Debug.Println("Empty paths")
					continue
				}

				// Try to inform changes to syncthing and if succeeded, clean up
				err = callback(folder, aggregateChanges(folderPath, dirVsFiles, paths, currentPathStatus), folderCfg)
				if err == nil {
					for _, path := range paths {
						delete(inProgress, path)
						Debug.Println("[INFORMED] Removed tracking for " + path)
					}
				}
			} else {
				// Do not track more than maxFiles changes, inform syncthing to rescan entire folder
				err = callback(folder, []string{""}, folderCfg)
				if err == nil {
					for path, progress := range inProgress {
						if progress.fsEvent {
							delete(inProgress, path)
							Debug.Println("[INFORMED] Removed tracking for " + path)
						}
					}
				}
			}

			if err == nil {
				nextScanTime = time.Now().Add(delayScanInterval) // Scan was delayed
			} else {
				Warning.Println("Syncthing failed to index changes for ", folder, err)
				time.Sleep(configSyncTimeout)
			}
		}
	}
}

func realPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

func relativePath(path string, folderPath string) string {
	path = expandTilde(path)
	path = strings.TrimPrefix(path, folderPath)
	if len(path) == 0 {
		return "."
	}
	if os.IsPathSeparator(path[0]) {
		if len(path) == 1 {
			return "."
		}
		return path[1:len(path)]
	}
	return path
}


////////////////////////////////////////////////////////////////////////////////


func IsConfigFile(file string) bool {
	internals := []string{".syignore"}
	pathSep := string(os.PathSeparator)
	for _, internal := range internals {
		if file == internal {
			return true
		}
		if strings.HasPrefix(file, internal+pathSep) {
			return true
		}
	}
	return false
}

func IsInternal2(file string) bool {
	// internals := []string{".syfolder", ".syignore", ".syversions"}
	internals := []string{".syfolder", ".syversions"}
	pathSep := string(os.PathSeparator)
	for _, internal := range internals {
		if file == internal {
			return true
		}
		if strings.HasPrefix(file, internal+pathSep) {
			return true
		}
	}
	return false
}

type Matcher2 struct {
	Matcher *ignore.Matcher
}

func (m *Matcher2) ShouldIgnore(filename string) bool {
	switch {
	case ignore.IsTemporary(filename):
		return true

	case IsInternal2(filename):
		return true

	case m.Matcher.Match(filename).IsIgnored():
		return true
	}

	return false
}

////////////////////////////////////////////////////////////////////////////////

// Returns a function to test whether a path should be ignored.
// The directory given by the absolute path "folderPath" must contain the
// ".syignore" file. The returned function expects the path of the file to be
// tested relative to its folders root.
func createIgnoreFilter(folderPath string, filters []string) func(relPath string) bool {

	filterFile := filepath.Join(folderPath, ".syignore")

	if len(filters) > 0 {
	// if len(filters) > 0 || len (gCfg.Filters.Items) > 0 {
	// 	totalfilters := make([]string, len(filters))
	// 	copy(totalfilters, filters)

	// 	for _, v := range gCfg.Filters.Items {
	// 		totalfilters = append(totalfilters, v)
	// 	}
		// ignore.WriteIgnores(filterFile, totalfilters)
		os.Remove(filterFile)
		ignore.WriteIgnores(filterFile, filters)
	}
	ignores := ignore.New(false)  // ignores := ignore.New(false)
	ignores.Load(filterFile)
	// os.Remove(filterFile)
	ignorest := Matcher2{Matcher: ignores}
	return ignorest.ShouldIgnore
}

func createExcludeFromIgnoreFilter(filepath string) func(relPath string) bool {

	ignores := ignore.New(false)  // ignores := ignore.New(false)
	ignores.Load(filepath)

	ignorest := Matcher2{Matcher: ignores}
	return ignorest.ShouldIgnore
}

// waitForEvent waits for an event in a channel c and returns event.Path().
// When channel c is closed then it returns path for default event (not sure if this is used at all?)
func waitForEvent(c chan notify.EventInfo) (string,string) {
	select {
	case ev, ok := <-c:
		if !ok {
			// this is never reached b/c c is never closed
			Warning.Println("Error: channel closed")
		}
		return ev.Path(), ev.Event().String()
	}
}

func testWebGuiPost() error {
	Trace.Println("Testing WebGUI")

	return nil
}
// informError sends a msg error to Syncthing
func informError(msg string) error {
	Trace.Printf("Informing ST about inotify error: %v", msg)

	return nil
}

// informChange sends a request to rescan folder and subs to Syncthing
func informChange(folder string, subs []string, folderCfg FolderConfiguration) error {

	if folderCfg.SyncType == "more" {

		ok, strerr := gCfg.Rsync.Sync(folderCfg, "modifyall", subs)

		if ! ok {
			Warning.Printf("sync failed %s, file: %s", strerr, folder)
		}
	}

	OK.Printf("Syncthing is indexing change in %v: %v", folder, subs)

	return nil
}


type InformCallback func(folder string, subs []string, folderCfg FolderConfiguration) error

func askToDelayScan(folder string, callback InformCallback, folderCfg FolderConfiguration) {
	Trace.Println("Asking to delay full scanning of " + folder)
	if err := callback(folder, []string{".syfolder"}, folderCfg); err != nil {
		Warning.Printf("Request to delay scanning of " + folder + " failed")
	}
}

func cleanPaths(paths []string) {
	for i := range paths {
		paths[i] = filepath.Clean(paths[i])
	}
}

func sortedUniqueAndCleanPaths(paths []string) []string {
	cleanPaths(paths)
	sort.Strings(paths)
	var new_paths []string
	previousPath := ""
	for _, path := range paths {
		if path == "." {
			path = ""
		}
		if path != previousPath {
			new_paths = append(new_paths, path)
		}
		previousPath = path
	}
	return new_paths
}

type PathStatus int

const (
	deletedPath PathStatus = iota
	directoryPath
	filePath
)

func currentPathStatus(path string) PathStatus {
	fileinfo, _ := os.Stat(path)
	if fileinfo == nil {
		return deletedPath
	} else if fileinfo.IsDir() {
		return directoryPath
	}
	return filePath
}

type statPathFunc func(name string) PathStatus

// AggregateChanges optimises tracking in two ways:
// - If there are more than `dirVsFiles` changes in a directory, we inform Syncthing to scan the entire directory
// - Directories with parent directory changes are aggregated. If A/B has 3 changes and A/C has 8, A will have 11 changes and if this is bigger than dirVsFiles we will scan A.
func aggregateChanges(folderPath string, dirVsFiles int, paths []string, pathStatus statPathFunc) []string {
	// Map paths to scores; if score == -1 the path is a filename
	trackedPaths := make(map[string]int)
	// Map of directories
	trackedDirs := make(map[string]bool)
	// Make sure parent paths are processed first
	paths = sortedUniqueAndCleanPaths(paths)
	// First we collect all paths and calculate scores for them
	for _, path := range paths {
		pathstatus := pathStatus(path)
		path = strings.TrimPrefix(path, folderPath)
		path = strings.TrimPrefix(path, pathSeparator)
		var dir string
		if pathstatus == deletedPath {
			// Definitely inform if the path does not exist anymore
			dir = path
			trackedPaths[path] = dirVsFiles
			Debug.Println("[AG] Not found:", path)
		} else if pathstatus == directoryPath {
			// Definitely inform if a directory changed
			dir = path
			trackedPaths[path] = dirVsFiles
			trackedDirs[dir] = true
			Debug.Println("[AG] Is a dir:", dir)
		} else {
			Debug.Println("[AG] Is file:", path)
			// Files are linked to -1 scores
			// Also increment the parent path with 1
			dir = filepath.Dir(path)
			if dir == "." {
				dir = ""
			}
			trackedPaths[path] = -1
			trackedPaths[dir]++
			trackedDirs[dir] = true
		}
		// Search for existing parent directory relations in the map
		for trackedPath := range trackedPaths {
			if trackedDirs[trackedPath] && strings.HasPrefix(dir, trackedPath+pathSeparator) {
				// Increment score of tracked parent directory for each file
				trackedPaths[trackedPath]++
				Debug.Println("[AG] Increment:", trackedPath, trackedPaths, trackedPaths[trackedPath])
			}
		}
	}
	var keys []string
	for k := range trackedPaths {
		keys = append(keys, k)
	}
	sort.Strings(keys) // Sort directories before their own files
	previousPath := ""
	var scans []string
	// Decide if we should inform about particular path based on dirVsFiles
	for i := range keys {
		trackedPath := keys[i]
		trackedPathScore := trackedPaths[trackedPath]
		if strings.HasPrefix(trackedPath, previousPath+pathSeparator) {
			// Already informed parent directory change
			continue
		}
		if trackedPathScore < dirVsFiles && trackedPathScore != -1 {
			// Not enough files for this directory or it is a file
			continue
		}
		previousPath = trackedPath
		Debug.Println("[AG] Appending path:", trackedPath, previousPath)
		scans = append(scans, trackedPath)
		if trackedPath == "" {
			// If we need to scan everything, skip the rest
			break
		}
	}
	return scans
}

// waitForSyncAndExitIfNeeded performs restart of itself if folders has different configuration in syncthing.
func waitForSyncAndExitIfNeeded(folders []FolderConfiguration) {
	// waitForSync()
	// newFolders, gCfg, _:= getFolders(configFile)
	newFolders, _, _:= getFolders(configFile)
	same := len(folders) == len(newFolders)
	for _, newF := range newFolders {
		seen := false
		for _, f := range folders {
			if f.ID == newF.ID && f.Source == newF.Source {
				seen = true
			}
		}
		if !seen {
			Warning.Println("Folder " + newF.Label + " changed")
			same = false
		}
	}
	if !same {
		// Simply exit as folders:
		// - can be added (still ok)
		// - can be removed as well (requires informing tons of goroutines...)
		OK.Println("Syncthing folder configuration updated, restarting")
		if !restart() {
			log.Fatalln("Cannot restart syncthing-inotify, exiting")
		}
	}
}

func getHomeDir() string {
	var home string
	switch runtime.GOOS {
	case "windows":
		home = filepath.Join(os.Getenv("HomeDrive"), os.Getenv("HomePath"))
		if home == "" {
			home = os.Getenv("UserProfile")
		}
	default:
		home = os.Getenv("HOME")
	}
	return home
}

func expandTilde(p string) string {
	if p == "~" {
		return getHomeDir()
	}
	p = filepath.FromSlash(p)
	if !strings.HasPrefix(p, fmt.Sprintf("~%c", os.PathSeparator)) {
		return p
	}
	return filepath.Join(getHomeDir(), p[2:])
}

func optionTable(w io.Writer, rows [][]string) {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	for _, row := range rows {
		for i, cell := range row {
			if i > 0 {
				tw.Write([]byte("\t"))
			}
			tw.Write([]byte(cell))
		}
		tw.Write([]byte("\n"))
	}
	tw.Flush()
}

func usageFor(fs *flag.FlagSet, usage string, extra string) func() {
	return func() {
		var b bytes.Buffer
		b.WriteString("Usage:\n  " + usage + "\n")

		var options [][]string
		fs.VisitAll(func(f *flag.Flag) {
			var opt = "  -" + f.Name

			if f.DefValue == "[]" {
				f.DefValue = ""
			}
			if f.DefValue != "false" {
				opt += "=" + fmt.Sprintf(`"%s"`, f.DefValue)
			}
			options = append(options, []string{opt, f.Usage})
		})

		if len(options) > 0 {
			b.WriteString("\nOptions:\n")
			optionTable(&b, options)
		}

		fmt.Println(b.String())

		if len(extra) > 0 {
			fmt.Println(extra)
		}
	}
}


// inspired by https://github.com/syncthing/syncthing/blob/03bbf273b3614d97a4c642e466e8c5bfb39ef595/cmd/syncthing/main.go#L943
func getSTDefaultConfDir() string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("LocalAppData"), "Syncthing")

	case "darwin":
		return expandTilde("~/Library/Application Support/Syncthing")

	default:
		if xdgCfg := os.Getenv("XDG_CONFIG_HOME"); xdgCfg != "" {
			return filepath.Join(xdgCfg, "syncthing")
		}
		return expandTilde("~/.config/syncthing")
	}
}
