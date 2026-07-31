package host

import (
	"os"
	"path/filepath"
)

// ScanSymlinks lists every symlink at or under root, in the same order
// `find root -type l -print0` would produce: for each directory, entries are
// visited in readdir order, and a real subdirectory is descended into
// immediately -- before its later siblings are visited, not after the whole
// directory has been read.
//
// scan_shared_skill_symlink_tree (claude-contained:1716-1743) feeds this order
// straight into --mount arguments, and the golden tests compare runtime argv
// byte for byte, so two symlinks in one directory have to come out in the
// same order find's does. os.ReadDir would not do: it sorts. A symlink
// to a directory is never descended into, matching find's default (non -L)
// behavior -- exactly the property the caller relies on to avoid an infinite
// walk through a self-referential symlink.
func ScanSymlinks(root string) ([]string, error) {
	var out []string
	if err := scanSymlinksDir(root, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func scanSymlinksDir(dir string, out *[]string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	names, err := f.Readdirnames(-1)
	if err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	for _, name := range names {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil {
			// Gone between readdir and lstat: find would simply not report it.
			continue
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			*out = append(*out, path)
		case info.IsDir():
			if err := scanSymlinksDir(path, out); err != nil {
				return err
			}
		}
	}
	return nil
}
