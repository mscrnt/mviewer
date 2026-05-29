package mview

import (
	"context"
	"fmt"
	"io"
	"io/fs"
)

// OpenFS opens an .mview archive from any fs.FS and returns its
// io.ReadCloser. Idiomatic Go layer over the byte-stream API so
// embed.FS, archive/zip, os.DirFS, and testfs all just work.
func OpenFS(fsys fs.FS, name string) (io.ReadCloser, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, fmt.Errorf("mview: open %q: %w", name, err)
	}
	return f, nil
}

// ConvertFromFS reads `name` from fsys and writes the GLB to w.
// Closes the source automatically; the caller owns w.
func ConvertFromFS(ctx context.Context, fsys fs.FS, name string, w io.Writer, opts ...Option) error {
	f, err := OpenFS(fsys, name)
	if err != nil {
		return err
	}
	defer f.Close()
	return ConvertToGLBContext(ctx, f, w, opts...)
}

// ValidateFS is the fs.FS-aware variant of Validate.
func ValidateFS(ctx context.Context, fsys fs.FS, name string, opts ...Option) error {
	f, err := OpenFS(fsys, name)
	if err != nil {
		return err
	}
	defer f.Close()
	return ValidateContext(ctx, f, opts...)
}

// ExtractThumbnailFS reads `name` from fsys and returns the embedded
// thumbnail.jpg bytes.
func ExtractThumbnailFS(fsys fs.FS, name string) ([]byte, error) {
	f, err := OpenFS(fsys, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ExtractThumbnail(f)
}
