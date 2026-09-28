// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package file

import (
	"errors"
	"io/fs"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// RecursiveWatcher wraps an fsnotify Watcher to recursively watch all files in a directory.
type RecursiveWatcher struct {
	fsnotifyWatcher *fsnotify.Watcher
	done            chan struct{}
	closed          bool
	events          chan fsnotify.Event
	errors          chan error
}

// NewRecursiveWatcher returns a RecursiveWatcher which notifies when changes are made to files inside a recursive directory tree.
func NewRecursiveWatcher(buffer uint) (*RecursiveWatcher, error) {
	watcher, err := fsnotify.NewBufferedWatcher(buffer)
	if err != nil {
		return nil, err
	}

	rw := &RecursiveWatcher{
		events:          make(chan fsnotify.Event, buffer),
		errors:          make(chan error),
		fsnotifyWatcher: watcher,
		done:            make(chan struct{}),
		closed:          false,
	}

	go rw.start()

	return rw, nil
}

// Add recursively adds a directory tree to the list of watched files.
func (rw *RecursiveWatcher) Add(path string) error {
	if rw.closed {
		return fsnotify.ErrClosed
	}
	return filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// swallow error from WalkDir, don't attempt to add to watcher.
			return nil
		}
		if d.IsDir() {
			return rw.fsnotifyWatcher.Add(p)
		}
		return nil
	})
}

// Events returns the events channel.
func (rw *RecursiveWatcher) Events() <-chan fsnotify.Event {
	return rw.events
}

// Errors returns the errors channel.
func (rw *RecursiveWatcher) Errors() <-chan error {
	return rw.errors
}

// Close closes the RecursiveWatcher.
func (rw *RecursiveWatcher) Close() error {
	if rw.closed {
		return nil
	}
	rw.closed = true
	close(rw.done)
	return rw.fsnotifyWatcher.Close()
}

func (rw *RecursiveWatcher) start() {
	for {
		select {
		case <-rw.done:
			close(rw.events)
			close(rw.errors)
			return
		case event := <-rw.fsnotifyWatcher.Events:
			// handle recursive watch
			switch {
			case event.Op.Has(fsnotify.Rename):
				// On Linux, inotify watches follow the inode: when a watched
				// directory is renamed, the kernel keeps the same watch descriptor.
				// fsnotify removes that watch asynchronously when it sees
				// IN_MOVE_SELF, which races with the Add below for the new name.
				// If Add wins, inotify returns the existing descriptor, fsnotify
				// keeps the stale path, and the subsequent removal drops the watch
				// for the new name too. Removing the old path synchronously here
				// ensures the Add for the new name always registers a fresh watch.
				if err := rw.fsnotifyWatcher.Remove(event.Name); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
					rw.errors <- err
				}
			case event.Op.Has(fsnotify.Create):
				if err := rw.Add(event.Name); err != nil {
					rw.errors <- err
				}
			}

			rw.events <- event
		case err := <-rw.fsnotifyWatcher.Errors:
			rw.errors <- err
		}
	}
}
