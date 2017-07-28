package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	// "log"
	"regexp"
	"runtime"
	"path/filepath"
	// "reflect"
)


type RsyncConfiguration struct {
	Path       string `xml:"path,attr"     json:"path"`
	Opt      []string `xml:"opt"           json:"opts"`
}

func (cfg RsyncConfiguration) Copy() RsyncConfiguration {
	newCfg := cfg

	newCfg.Opt = make([]string, len(cfg.Opt))
	copy(newCfg.Opt, cfg.Opt)

	return newCfg
}

func (cfg *RsyncConfiguration) prepare() {

	if cfg.Path != "" {

		cfg.Path = filepath.Dir(cfg.Path + string(filepath.Separator))

		if runtime.GOOS != "windows" && cfg.Path[len(cfg.Path)-1] != filepath.Separator {
			cfg.Path = cfg.Path + string(filepath.Separator)
		}
	}

	if cfg.Opt == nil {
		cfg.Opt = []string{}
	}

	for k, v := range cfg.Opt {
		cfg.Opt[k] = strings.Replace(v, "\\-", "-", -1)
	}
}

func (rsync RsyncConfiguration) SyncDir(folder FolderConfiguration) (bool, string) {

	// getFullFilter([]string{"folder1/folder2/test.sh"})

	// opts := make([]string, len(rsync.Opt))
	opts := []string {}
	// exclude

	for _, i := range folder.Opt {
		opts = append(opts, i)
	}

	for _, i := range rsync.Opt {
		opts = append(opts, i)
	}

	opts = append(opts, "--exclude-from=-")

	opts = append(opts, "-r")
	if folder.Delete == "true" {
		opts = append(opts, "--delete")
		opts = append(opts, "--ignore-errors")
	}

	// opts = append(opts, "--existing")

	// input := strings.Join(folder.Filter, "\n")
	input := folder.RsyncExcludes

	return Spawn(rsync.Path, opts, input, folder.SrcCygdrivePath, folder.DstCygdrivePath)
}

func (rsync RsyncConfiguration) Sync(folder FolderConfiguration, action string, files []string) (bool, string) {

	trSrcFile := folder.SrcCygdrivePath
	trDstFile := folder.DstCygdrivePath

	// opts := make([]string, len(rsync.Opt))
	opts := []string {}

	for _, i := range folder.Opt {
		opts = append(opts, i)
	}

	for _, i := range rsync.Opt {
		opts = append(opts, i)
	}

	opts = append(opts, "-r")
	if folder.Delete == "true" || folder.Delete == "running" {
		opts = append(opts, "--delete")
		opts = append(opts, "--ignore-errors")
	}

	opts = append(opts, "--force")
	opts = append(opts, "--include-from=-")
	opts = append(opts, "--exclude=*")

	filterpath := getFullFilter(files)

	Debug.Println("getFullFilter %v", filterpath)

	input := strings.Join(filterpath, "\n")

	return Spawn(rsync.Path, opts, input, trSrcFile, trDstFile)
}

func Spawn(exefile string, opts []string, input string, srcPath string, dstPath string) (bool, string) {

	if _, err := os.Stat(exefile); os.IsNotExist(err) {

		Trace.Println("rsync exe file not exist: ", exefile)
		return false, "rsync exe file not exist: " + exefile
	}

	opts = append(opts, srcPath)
	opts = append(opts, dstPath)
	// opts = append(opts, "<")

	os.Setenv("PATH", "$PATH:"+getCurrentDirectory())

	Debug.Printf("rsync opts: %v", opts)
	Debug.Printf("input file: %v", input)

	cmd := exec.Command(exefile, opts...)

	var stderr bytes.Buffer
	var stdout bytes.Buffer
	// cmd.Stdin = os.Stdin
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		Trace.Println(stderr.String() + "\n" + err.Error())
		return false, err.Error()
	}

	stdoutString := stdout.String()
	trmString := strings.TrimSpace(stdoutString)
	if len(trmString) > 0 {
		Trace.Println("rsync ", trmString)
	}

	return true, ""
}


func TransformCygDrivePath(path string) string {

	if runtime.GOOS == "windows" {

		reg := regexp.MustCompile(`(^[c-k]):`)

		str := strings.TrimSpace(path)
		str  = strings.ToLower(str)

		if str[len(str)-1] != filepath.Separator {
			str = str + string(filepath.Separator)
		}

		flags := string(filepath.Separator) + "cygdrive" + string(filepath.Separator)

		path = reg.ReplaceAllString(str, flags+"$1")

		return strings.Replace(path, "\\", "/", -1)
	}

	if path[len(path)-1] != filepath.Separator {
		return path + string(filepath.Separator)
	}

	return path
}


// adds one path to the filter
func addToFilter(paths *[]string, filterPathM map[string]bool, path string) [] string {

	if filterPathM[path] {
		return *paths
	}

	filterPathM[path] = true

	*paths = append(*paths, path)

	return *paths
}

// adds a path to the filter.
// rsync needs to have entries for all steps in the path,
// so the file for example d1/d2/d3/f1 needs following filters:
// 'd1/', 'd1/d2/', 'd1/d2/d3/' and 'd1/d2/d3/f1'

func getFullFilter(paths []string) []string {

	var filters []string
	filterM := make(map[string]bool)

	// reg := regexp.MustCompile(`(^.+)(/)([^/]*)`)
	// reg := regexp.MustCompile(`^(.*/)[^/]+/?`)
	// reg.Longest() // 切换到“贪婪模式”

	for _, path := range paths {

		if len(path) > 0 {

			path = strings.Replace(path, "\\", "/", -1)

			// addToFilter(&filters, filterM, path)

			s := strings.SplitAfter(path, "/")

			var item string
			for _, v := range s {
				if len(v) > 0 {
					item = item + v
					addToFilter(&filters, filterM, item)
				}
			}
		}

		// addToFilter(&filters, filterM, "/")
	}

	// OK.Printf(">>> filters %v", filters)
	// OK.Printf(">>> filterM %v", filterM)

	return filters
}
