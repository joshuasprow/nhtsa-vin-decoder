package bake

import (
	"fmt"
	"os"
	"syscall"
	"testing/fstest"

	"github.com/klauspost/compress/zstd"
)

func Decompress(z []byte) ([]byte, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	return dec.DecodeAll(z, nil)
}

// OpenBytes opens an artifact held in memory: an embedded []byte (raw) or
// zstd bytes that are decompressed to the heap first.
func OpenBytes(format string, b []byte, compressed bool) (Store, error) {
	if compressed {
		var err error
		if b, err = Decompress(b); err != nil {
			return nil, err
		}
	}
	switch format {
	case "custom":
		return OpenCustom(b)
	case "fb":
		return OpenFB(b)
	case "sqlite":
		return OpenSQLiteFS(fstest.MapFS{"artifact": {Data: b}}, "artifact")
	}
	return nil, fmt.Errorf("unknown format %q", format)
}

// OpenFile is the --data shape: mmap for the read-in-place formats, SQLite's
// own file I/O for SQLite; a .zst file is read and decompressed instead.
func OpenFile(format, path string, compressed bool) (Store, error) {
	if compressed {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return OpenBytes(format, b, true)
	}
	if format == "sqlite" {
		return OpenSQLiteFile(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	b, err := syscall.Mmap(int(f.Fd()), 0, int(st.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	return OpenBytes(format, b, false)
}
