// Package atomicfile replaces files on disk without ever leaving a torn
// half-written document behind: data lands in a sibling temporary file which
// is flushed, closed, and only then swapped onto the target in one
// filesystem-supported replace. A crash at any point leaves either the old
// bytes or the new bytes, never a mix.
//
// Two entry points:
//
//   - Write                — best-effort permissions: the mode argument is
//     applied to the replacement (and to a new file).
//   - WriteKeepingPermissions — for files whose access rules must survive a
//     rewrite (config.json with its admin-only ACL, for instance): the
//     existing file's mode bits and Windows DACL are copied onto the
//     replacement before the swap, so a save can never widen access. If the
//     file does not exist yet, this behaves exactly like Write.
//
// The threat model is crash-safety and permission stability, not a hostile
// writer racing us; the hardened pinned-directory replacement the file
// editor needs (Phase 4) will build on the same primitives.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// tempPattern is the glob our sibling temporary files are created with. It
// starts with a dot so a crash left-over never shows up as user data.
const tempPattern = ".kitodock-swap-*.tmp"

// Write atomically replaces path with data. mode applies to the replacement
// file; parent directories are created as needed.
func Write(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), tempPattern)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replace(tmpName, path)
}

// WriteKeepingPermissions atomically replaces path while preserving the
// permission bits (and, on Windows, the DACL) of an existing file. mode is
// only used when the file does not exist yet.
//
// The temporary file goes through three opens, in order: reserved and
// chmod'ed via CreateTemp (handle closed immediately — the ACL copy below
// needs exclusive access), stamped with the original's access rules, then
// reopened for the data itself.
func WriteKeepingPermissions(path string, data []byte, mode os.FileMode) error {
	info, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
		return Write(path, data, mode)
	case err != nil:
		return err
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file; refusing to replace it", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	reserve, err := os.CreateTemp(filepath.Dir(path), tempPattern)
	if err != nil {
		return err
	}
	tmpName := reserve.Name()
	defer os.Remove(tmpName)
	if err := reserve.Chmod(info.Mode().Perm()); err != nil {
		reserve.Close()
		return err
	}
	if err := reserve.Close(); err != nil {
		return err
	}
	if err := copyAccessRules(path, tmpName); err != nil {
		return err
	}
	staged, err := os.OpenFile(tmpName, os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := staged.Write(data); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Sync(); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	return replace(tmpName, path)
}
