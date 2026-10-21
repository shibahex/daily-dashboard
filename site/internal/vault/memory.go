package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// tmpfsMagic is Linux's filesystem id for tmpfs (statfs f_type).
const tmpfsMagic = 0x01021994

// MemoryDir is where unlocked folders live: a private folder on a tmpfs, so decrypted data is
// never written to a disk (unless the system swaps it). It's $XDG_RUNTIME_DIR/dashboard-vaults
// when that's a tmpfs (it is in a desktop session), else /dev/shm/dashboard-vaults-<uid>.
func MemoryDir() (string, error) {
	var candidates []string
	if runtime := os.Getenv("XDG_RUNTIME_DIR"); runtime != "" {
		candidates = append(candidates, filepath.Join(runtime, "dashboard-vaults"))
	}
	candidates = append(candidates, fmt.Sprintf("/dev/shm/dashboard-vaults-%d", os.Getuid()))
	for _, dir := range candidates {
		var fs syscall.Statfs_t
		if syscall.Statfs(filepath.Dir(dir), &fs) != nil || fs.Type != tmpfsMagic {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			continue
		}
		// Someone else's folder (made before us, on a shared /dev/shm) mustn't be used.
		if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 || !ownedByMe(info) {
			continue
		}
		return dir, nil
	}
	return "", fmt.Errorf("found no private in-memory folder for decrypted data (tried %v)", candidates)
}

func ownedByMe(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}
