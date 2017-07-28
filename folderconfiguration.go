// Copyright (C) 2014 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package main

import (
	"fmt"
	"os"
	"strings"
	"runtime"
	"path/filepath"
	"github.com/syncthing/syncthing/lib/osutil"
)

type FolderConfiguration struct {
	ID       string `xml:"id,attr"      json:"id"`
	Label    string `xml:"label,attr"   json:"label"`
	Delete   string `xml:"delete,attr"  json:"delete"`
	Source   string `xml:"source"       json:"source"`
	Target   string `xml:"target"       json:"target"`
	Filter []string `xml:"filter"       json:"filters"`
	// rsync opts
	Opt    []string `xml:"opt"          json:"opts"`
	Monitor  string `xml:"monitor,attr" json:"monitor"`
	SyncType string `xml:"type,attr"    json:"type"`

	RescanIntervalS int

	SrcRealPath string
	DstRealPath string
	// cwRsync 路径转换
	SrcCygdrivePath  string
	DstCygdrivePath  string

	RsyncExcludes   string
}


func (cfg FolderConfiguration) Copy() FolderConfiguration {
	newCfg := cfg

	newCfg.Filter = make([]string, len(cfg.Filter))
	copy(newCfg.Filter, cfg.Filter)

	newCfg.Opt = make([]string, len(cfg.Opt))
	copy(newCfg.Opt, cfg.Opt)
	return newCfg
}

func (f *FolderConfiguration) CreateMarker() error {
	if !f.HasMarker() {
		marker := filepath.Join(f.Source, ".syfolder")
		fd, err := os.Create(marker)
		if err != nil {
			return err
		}
		fd.Close()
		if err := osutil.SyncDir(filepath.Dir(marker)); err != nil {
			Warning.Println("fsync %q failed: %v", filepath.Dir(marker), err)
		}
		osutil.HideFile(marker)
	}

	return nil
}

func (f FolderConfiguration) HasMarker() bool {
	_, err := os.Stat(filepath.Join(f.Source, ".syfolder"))
	return err == nil
}

func CreateDirectory(path string) (err error) {
	// Directory permission bits. Will be filtered down to something
	// sane by umask on Unixes.
	permBits := os.FileMode(0777)
	if runtime.GOOS == "windows" {
		// Windows has no umask so we must chose a safer set of bits to
		// begin with.
		permBits = 0700
	}

	if _, err = os.Stat(path); os.IsNotExist(err) {
		if err = osutil.MkdirAll(path, permBits); err != nil {
			Warning.Println("Creating directory for %v: %v", path, err)
		}
	}

	return err
}

func (f FolderConfiguration) Description() string {
	if f.Label == "" {
		return f.ID
	}
	return fmt.Sprintf("%q (%s)", f.Label, f.ID)
}

func (f *FolderConfiguration) prepare(globalFilters []string) {
	if f.Source != "" {
		// The reason it's done like this:
		// C:          ->  C:\            ->  C:\        (issue that this is trying to fix)
		// C:\somedir  ->  C:\somedir\    ->  C:\somedir
		// C:\somedir\ ->  C:\somedir\\   ->  C:\somedir
		// This way in the tests, we get away without OS specific separators
		// in the test configs.
		f.Source = filepath.Dir(f.Source + string(filepath.Separator))

		// If we're not on Windows, we want the path to end with a slash to
		// penetrate symlinks. On Windows, paths must not end with a slash.
		if runtime.GOOS != "windows" && f.Source[len(f.Source)-1] != filepath.Separator {
			f.Source = f.Source + string(filepath.Separator)
		}
	}

	if f.Target != "" {

		f.Target = filepath.Dir(f.Target + string(filepath.Separator))

		if runtime.GOOS != "windows" && f.Target[len(f.Target)-1] != filepath.Separator {
			f.Target = f.Target + string(filepath.Separator)
		}
	}

	if f.Opt == nil {
		f.Opt = []string{}
	}

	for k, v := range f.Opt {
		f.Opt[k] = strings.Replace(v, "\\-", "-", -1)
	}

	if len(f.Monitor) == 0 {
		f.Monitor = "yes"
	}

	if len(f.SyncType) == 0 {
		f.SyncType = "one"
	}
	// f.CreateMarker()

	for _, v := range globalFilters {

		f.Filter = append(f.Filter, v)
	}

	if f.Filter != nil && len(f.Filter) > 0 {

		var items []string

		for _, v := range f.Filter {

			// if v == "**" || v == "*" {
			// 	continue
			// }

			item := strings.Replace(v, "**", "*", -1)

			if len(item) > 0 {
			// if len(item) > 0 && item[0] != '!' {

				if item[0] == '!' {
					item = strings.Replace(item, "!", "--include=", 1)
					f.Opt = append(f.Opt, item)
					continue
				}

				items = append(items, item)
			}
		}

		f.RsyncExcludes = strings.Join(items, "\n")
	}
}
