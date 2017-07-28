// Copyright (C) 2014 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package main

import (
	"os"
	// "sync/atomic"

	// "github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/osutil"
	// "github.com/syncthing/syncthing/lib/protocol"
	// "github.com/syncthing/syncthing/lib/rand"
	"github.com/syncthing/syncthing/lib/sync"
	// "github.com/syncthing/syncthing/lib/util"
)


// A wrapper around a Configuration that manages loads, saves and published
// notifications of changes to registered Handlers

type Wrapper struct {
	cfg  Configuration
	path string

	folderMap map[string]FolderConfiguration
	replaces  chan Configuration

	mut       sync.Mutex

	requiresRestart uint32 // an atomic bool
}

// Wrap wraps an existing Configuration structure and ties it to a file on
// disk.
func Wrap(path string, cfg Configuration) *Wrapper {
	w := &Wrapper{
		cfg:  cfg,
		path: path,
		mut:  sync.NewMutex(),
	}
	w.replaces = make(chan Configuration)
	return w
}

// Load loads an existing file on disk and returns a new configuration
// wrapper.
func Load(path string) (*Wrapper, error) {
	fd, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fd.Close()

	cfg, err := ReadXML(fd, "")
	if err != nil {
		return nil, err
	}

	return Wrap(path, cfg), nil
}

func (w *Wrapper) ConfigPath() string {
	return w.path
}

// Stop stops the Serve() loop. Set and Replace operations will panic after a
// Stop.
func (w *Wrapper) Stop() {
	close(w.replaces)
}


// RawCopy returns a copy of the currently wrapped Configuration object.
func (w *Wrapper) RawCopy() Configuration {
	w.mut.Lock()
	defer w.mut.Unlock()
	return w.cfg.Copy()
}

// Replace swaps the current configuration object for the given one.
func (w *Wrapper) Replace(cfg Configuration) error {
	w.mut.Lock()
	defer w.mut.Unlock()

	return w.replaceLocked(cfg)
}

func (w *Wrapper) replaceLocked(to Configuration) error {
	// from := w.cfg

	if err := to.clean(); err != nil {
		return err
	}

	w.cfg = to

	w.folderMap = nil

	// w.notifyListeners(from, to)

	return nil
}

// Folders returns a map of folders. Folder structures should not be changed,
// other than for the purpose of updating via SetFolder().
func (w *Wrapper) Folders() map[string]FolderConfiguration {
	w.mut.Lock()
	defer w.mut.Unlock()
	if w.folderMap == nil {
		w.folderMap = make(map[string]FolderConfiguration, len(w.cfg.Folders))
		for _, fld := range w.cfg.Folders {
			w.folderMap[fld.ID] = fld
		}
	}
	return w.folderMap
}

// SetFolder adds a new folder to the configuration, or overwrites an existing
// folder with the same ID.
func (w *Wrapper) SetFolder(fld FolderConfiguration) error {
	w.mut.Lock()
	defer w.mut.Unlock()

	newCfg := w.cfg.Copy()
	replaced := false
	for i := range newCfg.Folders {
		if newCfg.Folders[i].ID == fld.ID {
			newCfg.Folders[i] = fld
			replaced = true
			break
		}
	}
	if !replaced {
		newCfg.Folders = append(w.cfg.Folders, fld)
	}

	return w.replaceLocked(newCfg)
}


// IgnoredFolder returns whether or not share attempts for the given
// folder should be silently ignored.
func (w *Wrapper) IgnoredFolder(folder string) bool {
	w.mut.Lock()
	defer w.mut.Unlock()
	for _, nfolder := range w.cfg.IgnoredFolders {
		if folder == nfolder {
			return true
		}
	}
	return false
}

// Folder returns the configuration for the given folder and an "ok" bool.
func (w *Wrapper) Folder(id string) (FolderConfiguration, bool) {
	w.mut.Lock()
	defer w.mut.Unlock()
	for _, folder := range w.cfg.Folders {
		if folder.ID == id {
			return folder, true
		}
	}
	return FolderConfiguration{}, false
}

// Save writes the configuration to disk, and generates a ConfigSaved event.
func (w *Wrapper) Save() error {
	fd, err := osutil.CreateAtomic(w.path)
	if err != nil {
		// l.Debugln("CreateAtomic:", err)
		return err
	}

	if err := w.cfg.WriteXML(fd); err != nil {
		// l.Debugln("WriteXML:", err)
		fd.Close()
		return err
	}

	if err := fd.Close(); err != nil {
		// l.Debugln("Close:", err)
		return err
	}

	// events.Default.Log(events.ConfigSaved, w.cfg)
	return nil
}

func (w *Wrapper) MyName() string {
	w.mut.Lock()
	myID := w.cfg.MyID
	w.mut.Unlock()

	return myID
}
