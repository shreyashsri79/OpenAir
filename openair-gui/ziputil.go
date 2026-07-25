package main

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// zipDir compresses srcDir into a temp .zip and returns its path.
// The archive contains the folder itself as its top-level entry.
func zipDir(srcDir string) (string, error) {
	base := filepath.Base(filepath.Clean(srcDir))
	out := filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d.zip", base, time.Now().Unix()))

	f, err := os.Create(out)
	if err != nil {
		return "", err
	}

	zw := zip.NewWriter(f)
	walkErr := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(filepath.Join(base, rel))
		hdr.Method = zip.Deflate

		wtr, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(wtr, src)
		return err
	})

	if cerr := zw.Close(); walkErr == nil {
		walkErr = cerr
	}
	if cerr := f.Close(); walkErr == nil {
		walkErr = cerr
	}
	if walkErr != nil {
		os.Remove(out)
		return "", walkErr
	}
	return out, nil
}

// writeTextTemp saves pasted text to a temp .txt file for sending.
func writeTextTemp(text string) (string, error) {
	p := filepath.Join(os.TempDir(),
		fmt.Sprintf("openair-text-%s.txt", time.Now().Format("20060102-150405")))
	if err := os.WriteFile(p, []byte(text), 0644); err != nil {
		return "", err
	}
	return p, nil
}
