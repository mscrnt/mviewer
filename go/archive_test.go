package mview

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

// buildEntry synthesises one entry in the archive shape so tests can
// round-trip the parser without a real .mview sample. flags=0 means
// uncompressed (data is verbatim).
func buildEntry(name, typ string, flags uint32, data []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString(name)
	buf.WriteByte(0)
	buf.WriteString(typ)
	buf.WriteByte(0)
	_ = binary.Write(&buf, binary.LittleEndian, flags)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(data)))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(data)))
	buf.Write(data)
	return buf.Bytes()
}

func TestExtractThumbnail_FoundFirst(t *testing.T) {
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 'a', 'b', 'c'}
	archive := buildEntry("thumbnail.jpg", "image/jpeg", 0, jpeg)
	got, err := ExtractThumbnail(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, jpeg) {
		t.Fatalf("thumb mismatch: got %x want %x", got, jpeg)
	}
}

func TestExtractThumbnail_SkipsPriorEntries(t *testing.T) {
	thumb := []byte("the-real-thumbnail")
	archive := bytes.Join([][]byte{
		buildEntry("scene.json", "application/json", 0, []byte(`{"name":"foo"}`)),
		buildEntry("preview.png", "image/png", 0, []byte("decoy-png")),
		buildEntry("thumbnail.jpg", "image/jpeg", 0, thumb),
	}, nil)
	got, err := ExtractThumbnail(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, thumb) {
		t.Fatalf("thumb mismatch: got %q want %q", got, thumb)
	}
}

func TestExtractThumbnail_NotFound(t *testing.T) {
	archive := buildEntry("scene.json", "application/json", 0, []byte(`{}`))
	_, err := ExtractThumbnail(bytes.NewReader(archive))
	if err == nil {
		t.Fatal("expected error when thumbnail entry is absent")
	}
	if !errors.Is(err, ErrMissingEntry) {
		t.Fatalf("expected ErrMissingEntry, got %v", err)
	}
}

func TestExtractThumbnail_CStringGuard(t *testing.T) {
	// Name field has no null terminator within the cap → rejection.
	huge := bytes.Repeat([]byte{'a'}, MaxStringLen+1)
	_, err := ExtractThumbnail(bytes.NewReader(huge))
	if !errors.Is(err, ErrCStringTooLong) {
		t.Fatalf("expected ErrCStringTooLong, got %v", err)
	}
}

func TestExtractThumbnail_EntrySizeGuard(t *testing.T) {
	// Header claims 2 GB of data — should refuse before allocating.
	var buf bytes.Buffer
	buf.WriteString("thumbnail.jpg")
	buf.WriteByte(0)
	buf.WriteString("image/jpeg")
	buf.WriteByte(0)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(2_000_000_000))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(2_000_000_000))
	_, err := ExtractThumbnail(bytes.NewReader(buf.Bytes()))
	if !errors.Is(err, ErrEntryTooLarge) {
		t.Fatalf("expected ErrEntryTooLarge, got %v", err)
	}
}

func TestExtractThumbnail_CompressedTruncated(t *testing.T) {
	// 1 byte of compressed input claiming 100 bytes of output —
	// decoder produces just the literal first byte and trips the
	// length-mismatch guard.
	var buf bytes.Buffer
	buf.WriteString("thumbnail.jpg")
	buf.WriteByte(0)
	buf.WriteString("image/jpeg")
	buf.WriteByte(0)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(FlagCompressed))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(100))
	buf.WriteByte(0x42)
	_, err := ExtractThumbnail(bytes.NewReader(buf.Bytes()))
	if !errors.Is(err, ErrDecompress) {
		t.Fatalf("expected ErrDecompress, got %v", err)
	}
}

func TestDecompress_LiteralByteStream(t *testing.T) {
	// Encoding [0x41, 0x42, 0x43] as a literal-only LZW stream:
	//   - out[0] = input[0] = 0x41
	//   - r=1: code 0x042 at packedIdx=1 → input[2]=0x04, input[1]=0x20
	//   - r=2: code 0x043 at packedIdx=3 → input[3]=0x43, input[4]=0x00
	in := []byte{0x41, 0x20, 0x04, 0x43, 0x00}
	out, err := Decompress(in, 3)
	if err != nil {
		t.Fatalf("decompress failed: %v", err)
	}
	want := []byte{0x41, 0x42, 0x43}
	if !bytes.Equal(out, want) {
		t.Fatalf("decompress mismatch: got %x want %x", out, want)
	}
}

func TestDecompress_EmptyInputRejected(t *testing.T) {
	_, err := Decompress(nil, 4)
	if err == nil || !strings.Contains(err.Error(), "empty stream") {
		t.Fatalf("expected empty-stream error, got %v", err)
	}
}
