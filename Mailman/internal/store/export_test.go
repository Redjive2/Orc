package store

import (
	"io/fs"
	"os"
)

// Ops is the filesystem operation set the store performs.
type Ops = ops

// TempFile is the file interface an atomic write goes through.
type TempFile = tempFile

// RealOps returns the operations Open would have used.
func RealOps() Ops { return realOps() }

// WithOps replaces the store's filesystem operations.
//
// Every write path claims to leave the store untouched when it fails partway.
// That claim can only be tested by making each individual call fail on demand,
// which is what this is for. The pattern is Anno's, in edit.
func (s *Store) WithOps(o Ops) { s.ops = o }

// SetStat replaces the stat step.
func (o *Ops) SetStat(f func(string) (fs.FileInfo, error)) { o.stat = f }

// SetReadFile replaces the read step.
func (o *Ops) SetReadFile(f func(string) ([]byte, error)) { o.readFile = f }

// SetReadDir replaces the directory listing step.
func (o *Ops) SetReadDir(f func(string) ([]fs.DirEntry, error)) { o.readDir = f }

// SetMkdirAll replaces directory creation.
func (o *Ops) SetMkdirAll(f func(string, fs.FileMode) error) { o.mkdirAll = f }

// SetCreateTemp replaces temporary-file creation.
func (o *Ops) SetCreateTemp(f func(dir, pattern string) (TempFile, error)) { o.createTemp = f }

// SetOpenAppend replaces opening a journal for appending.
func (o *Ops) SetOpenAppend(f func(string) (*os.File, error)) { o.openAppend = f }

// SetRename replaces the rename step.
func (o *Ops) SetRename(f func(from, to string) error) { o.rename = f }

// SetRemove replaces file removal.
func (o *Ops) SetRemove(f func(string) error) { o.remove = f }

// JournalPathFor exposes a user's journal path, so a test can damage it
// deliberately and check that replay recovers the way it promises to.
func (s *Store) JournalPathFor(name string) string {
	return s.root + "/" + usersDir + "/" + name + "/" + journalFile
}

// Fold replays journal bytes without touching the filesystem.
//
// The recovery rules are a pure fold over bytes, and this is what lets them be
// fuzzed as one: a target that had to build a store per iteration explored a
// few hundred inputs a second.
func Fold(path string, data []byte) (State, error) { return fold(path, data) }
