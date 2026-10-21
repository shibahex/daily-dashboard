package vault

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"dashboard/internal/jsonfile"
)

// Mirror keeps a plain folder (in memory, while the user is signed in) and its encrypted copy
// on disk in step. The copy is one encrypted blob per file, under a random name, and an
// encrypted manifest of paths, so the disk shows neither contents nor names nor how files
// relate. Save writes only what changed since the last Save; blobs no longer used are deleted
// after the new manifest is in place, so a crash leaves the old copy or the new one, whole.
type Mirror struct {
	dir   string // the encrypted copy: manifest + blobs/
	plain string // the plain folder
	key   *Key

	mu     sync.Mutex
	files  map[string]entry // the last saved manifest, by path relative to plain
	closed bool             // locked: the plain folder is being wiped, so nothing more is saved
}

// entry is one file, folder or symlink in the manifest.
type entry struct {
	Blob  string      `json:"blob,omitempty"` // file: its blob's name
	Size  int64       `json:"size,omitempty"`
	MTime int64       `json:"mtime,omitempty"` // file: unix nanoseconds, to spot changes
	Mode  fs.FileMode `json:"mode"`            // permission bits plus fs.ModeDir or fs.ModeSymlink
	Link  string      `json:"link,omitempty"`  // symlink: its target
}

// skipped are folders not worth keeping: Chromium's caches in the MyFitnessPal MCP's browser
// profile, which it rebuilds. Also skipped: mfp/data/uv (a Python uv once downloaded there by
// mistake), agent-tools (the AI tools' empty, throwaway workspace) and agent-workspace (the same
// for the server-side AI there used to be).
var skipped = []string{"Cache", "Code Cache", "GPUCache", "GrShaderCache", "ShaderCache", "DawnCache",
	"DawnGraphiteCache", "DawnWebGPUCache", "component_crx_cache", "Crashpad"}

func NewMirror(dir, plain string, key *Key) *Mirror {
	return &Mirror{dir: dir, plain: plain, key: key}
}

func (m *Mirror) manifestFile() string        { return filepath.Join(m.dir, "manifest") }
func (m *Mirror) blobFile(name string) string { return filepath.Join(m.dir, "blobs", name) }
func (m *Mirror) label(what string) []byte {
	return []byte("dashboard-vault/v1/" + m.key.userID + "/" + what)
}

// Exists reports whether an encrypted copy has been saved.
func (m *Mirror) Exists() bool {
	_, err := os.Stat(m.manifestFile())
	return err == nil
}

// readManifest decrypts the saved manifest (empty when there's none yet).
func (m *Mirror) readManifest() (map[string]entry, error) {
	sealed, err := os.ReadFile(m.manifestFile())
	if os.IsNotExist(err) {
		return map[string]entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := open(m.key.data, sealed, m.label("manifest"))
	if err != nil {
		return nil, errors.New("the encrypted data doesn't open with this key")
	}
	files := map[string]entry{}
	return files, json.Unmarshal(plain, &files)
}

// Load reads the manifest, so Save knows what's already saved. Restore does it too.
func (m *Mirror) Load() error {
	files, err := m.readManifest()
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.files = files
	m.mu.Unlock()
	return nil
}

// Restore decrypts the saved copy into the plain folder, which must be empty or missing.
func (m *Mirror) Restore() error {
	if err := m.Load(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.MkdirAll(m.plain, 0o700); err != nil {
		return err
	}
	paths := make([]string, 0, len(m.files))
	for path := range m.files {
		paths = append(paths, path)
	}
	slices.Sort(paths) // folders before what's in them
	for _, path := range paths {
		e := m.files[path]
		target := filepath.Join(m.plain, filepath.FromSlash(path))
		switch {
		case e.Mode&fs.ModeDir != 0:
			if err := os.MkdirAll(target, e.Mode.Perm()|0o700); err != nil {
				return err
			}
		case e.Mode&fs.ModeSymlink != 0:
			os.MkdirAll(filepath.Dir(target), 0o700)
			if err := os.Symlink(e.Link, target); err != nil {
				return err
			}
		default:
			sealed, err := os.ReadFile(m.blobFile(e.Blob))
			if err != nil {
				return err
			}
			data, err := open(m.key.data, sealed, m.label("blob/"+e.Blob))
			if err != nil {
				return errors.New("a file in the encrypted data is damaged")
			}
			os.MkdirAll(filepath.Dir(target), 0o700)
			if err := os.WriteFile(target, data, e.Mode.Perm()|0o600); err != nil {
				return err
			}
			// Its saved time, so the next Save sees it unchanged.
			mtime := time.Unix(0, e.MTime)
			os.Chtimes(target, mtime, mtime)
		}
	}
	return nil
}

// Save encrypts what changed in the plain folder since the last Save (or Load).
func (m *Mirror) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	if m.files == nil {
		return errors.New("vault: Save before Load")
	}
	// A missing folder isn't "everything was deleted": saving it would empty the copy.
	if info, err := os.Stat(m.plain); err != nil || !info.IsDir() {
		return errors.New("vault: the open folder is missing, so it wasn't saved")
	}
	if err := os.MkdirAll(filepath.Join(m.dir, "blobs"), 0o700); err != nil {
		return err
	}
	now := map[string]entry{}
	changed := false
	err := filepath.WalkDir(m.plain, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // deleted while walking
			}
			return err
		}
		rel, _ := filepath.Rel(m.plain, path)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() && (slices.Contains(skipped, d.Name()) || rel == "mfp/data/uv" || rel == "agent-workspace" || rel == "agent-tools") {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		old, known := m.files[rel]
		switch {
		case d.IsDir():
			now[rel] = entry{Mode: fs.ModeDir | info.Mode().Perm()}
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return nil
			}
			now[rel] = entry{Mode: fs.ModeSymlink, Link: link}
		case info.Mode().IsRegular():
			e := entry{Size: info.Size(), MTime: info.ModTime().UnixNano(), Mode: info.Mode().Perm()}
			if known && old.Blob != "" && old.Size == e.Size && old.MTime == e.MTime {
				e.Blob = old.Blob
			} else {
				data, err := os.ReadFile(path)
				if err != nil {
					return nil // gone, or being replaced: the next Save gets it
				}
				e.Blob = newBlobName()
				if err := jsonfile.WriteBytes(m.blobFile(e.Blob), seal(m.key.data, data, m.label("blob/"+e.Blob))); err != nil {
					return err
				}
			}
			now[rel] = e
		default:
			return nil // sockets, pipes: not data
		}
		if !known || old != now[rel] {
			changed = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !changed && len(now) == len(m.files) {
		return nil
	}
	manifest, _ := json.Marshal(now)
	if err := jsonfile.WriteBytes(m.manifestFile(), seal(m.key.data, manifest, m.label("manifest"))); err != nil {
		return err
	}
	m.files = now
	m.dropUnusedBlobs()
	return nil
}

// dropUnusedBlobs deletes blobs the manifest no longer names (and half-written ones). It needs
// m.mu held.
func (m *Mirror) dropUnusedBlobs() {
	used := map[string]bool{}
	for _, e := range m.files {
		if e.Blob != "" {
			used[e.Blob] = true
		}
	}
	blobs, _ := os.ReadDir(filepath.Join(m.dir, "blobs"))
	for _, b := range blobs {
		if !used[b.Name()] {
			os.Remove(m.blobFile(b.Name()))
		}
	}
}

func newBlobName() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Wipe deletes a plain folder, for when its user is locked. Files are overwritten with zeros
// first; in memory (tmpfs) that leaves nothing behind.
func Wipe(plain string) error {
	filepath.WalkDir(plain, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil && info.Size() > 0 {
				if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
					f.Write(make([]byte, info.Size()))
					f.Close()
				}
			}
		}
		return nil
	})
	return os.RemoveAll(plain)
}

// Close saves one last time and stops any later Save, for locking: once the plain folder is
// being wiped, a Save from elsewhere must not record its files as deleted.
func (m *Mirror) Close() error {
	if err := m.Save(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}
