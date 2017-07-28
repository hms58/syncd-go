// Copyright (C) 2014 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

// Package config implements reading and writing of the syncthing configuration file.
package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"github.com/syncthing/syncthing/lib/util"
)


func New(myID string) Configuration {
	var cfg Configuration

	util.SetDefaults(&cfg)

	// Can't happen.
	if err := cfg.prepare(myID); err != nil {
		panic("bug: error in preparing new folder: " + err.Error())
	}

	return cfg
}

func ReadXML(r io.Reader, myID string) (Configuration, error) {
	var cfg Configuration

	util.SetDefaults(&cfg)

	if err := xml.NewDecoder(r).Decode(&cfg); err != nil {
		return Configuration{}, err
	}

	if err := cfg.prepare(myID); err != nil {
		return Configuration{}, err
	}
	return cfg, nil
}

func ReadJSON(r io.Reader, myID string) (Configuration, error) {
	var cfg Configuration

	util.SetDefaults(&cfg)

	bs, err := ioutil.ReadAll(r)
	if err != nil {
		return Configuration{}, err
	}

	if err := json.Unmarshal(bs, &cfg); err != nil {
		return Configuration{}, err
	}
	// cfg.OriginalVersion = cfg.Version

	if err := cfg.prepare(myID); err != nil {
		return Configuration{}, err
	}
	return cfg, nil
}

type Configuration struct {
	Version        int                   `xml:"version,attr" json:"version"`
	Folders        []FolderConfiguration `xml:"folder" json:"folders"`
	Rsync          RsyncConfiguration    `xml:"rsync"  json:"folders"`
	Filters        FilterConfiguration   `xml:"filter" json:"filters"`
	IgnoredFolders []string              `xml:"ignoredFolder" json:"ignoredFolders"`
	XMLName        xml.Name              `xml:"configuration" json:"-"`

	MyID            string               `xml:"-" json:"-"` // Provided by the instantiator.
	OriginalVersion int                  `xml:"-" json:"-"` // The version we read from disk, before any conversion
}

type FilterConfiguration struct {
	Items []string `xml:"item"  json:"items"`
}

func (cfg Configuration) Copy() Configuration {
	newCfg := cfg

	// Deep copy FolderConfigurations
	newCfg.Folders = make([]FolderConfiguration, len(cfg.Folders))
	for i := range newCfg.Folders {
		newCfg.Folders[i] = cfg.Folders[i].Copy()
	}

	// FolderConfiguraion.ID is type string
	newCfg.IgnoredFolders = make([]string, len(cfg.IgnoredFolders))
	copy(newCfg.IgnoredFolders, cfg.IgnoredFolders)

	newCfg.Rsync = cfg.Rsync.Copy()
	newCfg.Filters = cfg.Filters.Copy()

	return newCfg
}

func (cfg *Configuration) WriteXML(w io.Writer) error {
	e := xml.NewEncoder(w)
	e.Indent("", "    ")
	err := e.Encode(cfg)
	if err != nil {
		return err
	}
	_, err = w.Write([]byte("\n"))
	return err
}

func (cfg *Configuration) prepare(myID string) error {

	var myName string

	cfg.MyID = myID

	if cfg.MyID == "" {
		myName, _ = os.Hostname()
		cfg.MyID = myName
	}

	for f := range cfg.Folders {
		cfg.Folders[f].prepare(cfg.Filters.Items)
	}

	cfg.Rsync.prepare()

	return nil
}

func (cfg *Configuration) clean() error {
	// util.FillNilSlices(&cfg.Options)

	// Initialize any empty slices
	if cfg.Folders == nil {
		cfg.Folders = []FolderConfiguration{}
	}

	if cfg.IgnoredFolders == nil {
		cfg.IgnoredFolders = []string{}
	}


	// Prepare folders and check for duplicates. Duplicates are bad and
	// dangerous, can't currently be resolved in the GUI, and shouldn't
	// happen when configured by the GUI. We return with an error in that
	// situation.
	seenFolders := make(map[string]struct{})
	for i := range cfg.Folders {
		folder := &cfg.Folders[i]
		folder.prepare(cfg.Filters.Items)

		if _, ok := seenFolders[folder.ID]; ok {
			return fmt.Errorf("duplicate folder ID %q in configuration", folder.ID)
		}
		seenFolders[folder.ID] = struct{}{}
	}

	// Remove ignored folders that are anyway part of the configuration.
	for i := 0; i < len(cfg.IgnoredFolders); i++ {
		if _, ok := seenFolders[cfg.IgnoredFolders[i]]; ok {
			cfg.IgnoredFolders = append(cfg.IgnoredFolders[:i], cfg.IgnoredFolders[i+1:]...)
			i-- // IgnoredFolders[i] now points to something else, so needs to be rechecked
		}
	}

	return nil
}

func (cfg FilterConfiguration) Copy() FilterConfiguration {
	newCfg := cfg
	newCfg.Items = make([]string, len(cfg.Items))
	copy(newCfg.Items, cfg.Items)
	return newCfg
}
