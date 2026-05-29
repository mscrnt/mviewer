package mview

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// Validate walks an .mview archive end-to-end and confirms it's
// structurally sound — every entry parses cleanly, no truncation, no
// per-entry or total-size cap violations, the LZW streams decode if
// flagged, and the required scene.json is present and parses.
//
// Useful for upload pipelines that want to reject malformed archives
// before queuing them for the heavier ConvertToGLB path.
func Validate(r io.Reader, opts ...Option) error {
	return ValidateContext(context.Background(), r, opts...)
}

// ValidateContext is the context-aware variant. Honours cancellation
// on each entry boundary.
func ValidateContext(ctx context.Context, r io.Reader, opts ...Option) error {
	entries, err := ReadAllContext(ctx, r, opts...)
	if err != nil {
		return err
	}
	sceneEntry, ok := entries["scene.json"]
	if !ok {
		return fmt.Errorf("%w: scene.json", ErrMissingEntry)
	}
	scene, err := ParseScene(sceneEntry.Data)
	if err != nil {
		return fmt.Errorf("scene.json: %w", err)
	}
	for i := range scene.Meshes {
		desc := &scene.Meshes[i]
		if _, ok := entries[desc.File]; !ok {
			return fmt.Errorf("%w: mesh %q references %q", ErrMissingEntry, desc.Name, desc.File)
		}
	}
	return nil
}

// IsMview reports whether the head of r looks like an .mview archive.
// Reads up to 512 bytes from r and rewinds via the returned remainder
// — so callers that want to consume the archive after the sniff
// should use that remainder rather than r directly.
//
// Detection logic: try to parse a single readEntry. A successful
// parse, or an ErrEntryTooLarge / ErrCStringTooLong failure (which
// imply we got far enough to identify the format), counts as a
// positive match. Truncation or an io.EOF from the first byte is
// negative.
func IsMview(r io.Reader) (bool, io.Reader, error) {
	buf := make([]byte, 0, 512)
	chunk := make([]byte, 512)
	n, err := io.ReadFull(r, chunk)
	buf = append(buf, chunk[:n]...)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, nil, err
	}

	rem := io.MultiReader(bytesReader(buf), r)
	if len(buf) == 0 {
		return false, rem, nil
	}
	_, perr := ReadEntry(bytesReader(buf))
	if perr == nil {
		return true, rem, nil
	}
	if errors.Is(perr, ErrEntryTooLarge) {
		return true, rem, nil
	}
	return false, rem, nil
}

func bytesReader(b []byte) io.Reader {
	return &byteSliceReader{b: b}
}

type byteSliceReader struct {
	b []byte
	i int
}

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
