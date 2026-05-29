// Package mview decodes Marmoset Viewer (.mview) archives.
//
// A .mview file is a TAR-like container: every entry is a sequential
// header (name, type, flags, sizes) followed by raw bytes. Marmoset's
// own runtime exploits this layout by reading just the first ~64 KB
// to grab the embedded thumbnail.jpg — there's no central directory.
//
// This package implements the parser + LZW decompressor. Higher-level
// decoders (mesh / material / animation → glTF) live in sibling files
// and build on top.
package mview

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Tunable safety caps. These exist to refuse pathological inputs
// before they can hurt the decoder process — sane .mview files stay
// well under each ceiling.
const (
	// MaxStringLen caps null-terminated string reads so a malformed
	// file can't get the parser to walk indefinitely looking for a
	// terminator. 256 bytes covers any sane filename or MIME-ish
	// type field.
	MaxStringLen = 256

	// MaxEntrySize caps the data length of a single entry. 32 MB
	// covers the largest textures we've seen + leaves headroom; a
	// future format change that legitimately needs more can bump it.
	MaxEntrySize = 32 * 1024 * 1024

	// FlagCompressed is the bit in an entry's `flags` field that
	// says the data is LZW-compressed and needs Decompress() to be
	// run on it.
	FlagCompressed = 0x1
)

// Entry is one named blob in an archive.
type Entry struct {
	Name             string
	Type             string
	Flags            uint32
	CompressedSize   uint32
	UncompressedSize uint32

	// Data is the entry's payload AFTER decompression (if Flags &
	// FlagCompressed was set, the LZW was already expanded by the
	// reader; callers see the decoded bytes).
	Data []byte
}

// ErrEntryTooLarge is returned when an entry's declared
// CompressedSize exceeds MaxEntrySize. The cap blocks memory-bomb
// inputs before any allocation happens.
var ErrEntryTooLarge = errors.New("mview: entry exceeds size cap")

// ErrDecompress signals corrupt or truncated LZW data. Clean .mview
// files from Marmoset never trip this path; the usual cause is a
// damaged source archive.
var ErrDecompress = errors.New("mview: lzw decompression failed")

// ErrCStringTooLong is returned when a null-terminated string field
// exceeds MaxStringLen without a terminator.
var ErrCStringTooLong = errors.New("mview: c-string exceeded length cap")

// ReadEntry reads a single archive entry header + body from r. It
// returns io.EOF cleanly when r has no more entries.
//
// Use the higher-level Find / Extract helpers if you only need a
// specific named entry — those stop scanning as soon as the entry
// is found, which lets them work against a partial download.
func ReadEntry(r io.Reader) (*Entry, error) {
	br := newReader(r)
	return readEntry(br)
}

// Find scans r entry-by-entry and returns the first whose name
// matches `want`. Returns io.EOF (wrapped) if the entry isn't in the
// archive. Compressed entries are decompressed transparently.
func Find(r io.Reader, want string) (*Entry, error) {
	br := newReader(r)
	for {
		entry, err := readEntry(br)
		if err != nil {
			return nil, err
		}
		if entry.Name == want {
			return entry, nil
		}
	}
}

// ExtractThumbnail is a convenience for the very common case of
// reading just the embedded thumbnail.jpg.
func ExtractThumbnail(r io.Reader) ([]byte, error) {
	e, err := Find(r, "thumbnail.jpg")
	if err != nil {
		return nil, err
	}
	return e.Data, nil
}

// newReader wraps r in a bufio.Reader if it isn't already one. This
// keeps ReadByte / Read calls cheap when the caller passes us a raw
// io.Reader.
func newReader(r io.Reader) *bufio.Reader {
	if br, ok := r.(*bufio.Reader); ok {
		return br
	}
	return bufio.NewReader(r)
}

func readEntry(br *bufio.Reader) (*Entry, error) {
	name, err := readCString(br, MaxStringLen)
	if err != nil {
		return nil, fmt.Errorf("entry name: %w", err)
	}
	typ, err := readCString(br, MaxStringLen)
	if err != nil {
		return nil, fmt.Errorf("entry type: %w", err)
	}
	var hdr struct {
		Flags            uint32
		CompressedSize   uint32
		UncompressedSize uint32
	}
	if err := binary.Read(br, binary.LittleEndian, &hdr); err != nil {
		return nil, fmt.Errorf("entry header: %w", err)
	}
	if hdr.CompressedSize > MaxEntrySize {
		return nil, fmt.Errorf("%w: %q claims %d bytes", ErrEntryTooLarge, name, hdr.CompressedSize)
	}
	data := make([]byte, hdr.CompressedSize)
	if _, err := io.ReadFull(br, data); err != nil {
		return nil, fmt.Errorf("entry data for %q: %w", name, err)
	}
	if hdr.Flags&FlagCompressed != 0 {
		decoded, err := Decompress(data, int(hdr.UncompressedSize))
		if err != nil {
			return nil, fmt.Errorf("decompress %q: %w", name, err)
		}
		data = decoded
	}
	return &Entry{
		Name:             name,
		Type:             typ,
		Flags:            hdr.Flags,
		CompressedSize:   hdr.CompressedSize,
		UncompressedSize: hdr.UncompressedSize,
		Data:             data,
	}, nil
}

func readCString(br *bufio.Reader, maxLen int) (string, error) {
	var buf bytes.Buffer
	for buf.Len() <= maxLen {
		b, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if b == 0 {
			return buf.String(), nil
		}
		_ = buf.WriteByte(b)
	}
	return "", ErrCStringTooLong
}

// Decompress expands a Marmoset-flavoured LZW stream into output of
// outputLen bytes. Ported from the Rust crate's archive.rs
// (decompress fn) — see ../src/archive.rs for the reference impl.
//
// The algorithm:
//   - 12-bit codes packed two-per-three-bytes, with the parity of
//     the code index r deciding which nybble lives where.
//   - Dictionary starts at 256 (literals cover all single bytes);
//     each step adds an entry pointing at (prev_offset, prev_length+1).
//   - The classic LZW "code == nextCode" corner case is handled
//     explicitly: new entry = prev sequence + first byte of prev.
//   - Dictionary capacity is 4096; reset to 256 once full.
func Decompress(input []byte, outputLen int) ([]byte, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("%w: empty stream", ErrDecompress)
	}
	out := make([]byte, outputLen)
	var (
		tableOffsets [4096]int
		tableLengths [4096]int
		nextCode     = 256
		writeIdx     = 0
		prevOffset   = 0
		prevLength   = 1
	)

	ensureRoom := func(n int) error {
		if writeIdx+n > outputLen {
			return fmt.Errorf("%w: overflow", ErrDecompress)
		}
		return nil
	}

	out[writeIdx] = input[0]
	writeIdx++

	r := 1
	for {
		packedIdx := r + (r >> 1)
		if packedIdx+1 >= len(input) {
			break
		}
		m := int(input[packedIdx+1])
		n := int(input[packedIdx])
		var code int
		if r&1 == 1 {
			code = (m << 4) | (n >> 4)
		} else {
			code = ((m & 15) << 8) | n
		}

		var entryOffset, entryLength int
		switch {
		case code < nextCode:
			if code < 256 {
				if err := ensureRoom(1); err != nil {
					return nil, err
				}
				out[writeIdx] = byte(code)
				entryOffset = writeIdx
				writeIdx++
				entryLength = 1
			} else {
				entryOffset = writeIdx
				length := tableLengths[code]
				src := tableOffsets[code]
				end := src + length
				if err := ensureRoom(length); err != nil {
					return nil, err
				}
				for src < end {
					out[writeIdx] = out[src]
					writeIdx++
					src++
				}
				entryLength = length
			}
		case code == nextCode:
			entryOffset = writeIdx
			length := prevLength + 1
			src := prevOffset
			end := prevOffset + prevLength
			if err := ensureRoom(length); err != nil {
				return nil, err
			}
			for src < end {
				out[writeIdx] = out[src]
				writeIdx++
				src++
			}
			out[writeIdx] = out[prevOffset]
			writeIdx++
			entryLength = length
		default:
			return nil, fmt.Errorf("%w: code %d out of range (next=%d)", ErrDecompress, code, nextCode)
		}

		tableOffsets[nextCode] = prevOffset
		tableLengths[nextCode] = prevLength + 1
		nextCode++
		prevOffset = entryOffset
		prevLength = entryLength
		if nextCode >= 4096 {
			nextCode = 256
		}
		r++
	}

	if writeIdx != outputLen {
		return nil, fmt.Errorf("%w: expected %d bytes, got %d", ErrDecompress, outputLen, writeIdx)
	}
	return out, nil
}
