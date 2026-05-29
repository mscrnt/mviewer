package mview

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"testing/fstest"
	"time"
)

func TestReadAll_TotalSizeCap(t *testing.T) {
	// Two entries × 1000 bytes each = 2000. Cap at 1500 → fail.
	a := bytes.Join([][]byte{
		buildEntry("a.bin", "application/octet-stream", 0, bytes.Repeat([]byte{1}, 1000)),
		buildEntry("b.bin", "application/octet-stream", 0, bytes.Repeat([]byte{2}, 1000)),
	}, nil)
	if _, err := ReadAll(bytes.NewReader(a), WithMaxTotalSize(1500)); !errors.Is(err, ErrTotalSizeExceeded) {
		t.Fatalf("expected ErrTotalSizeExceeded, got %v", err)
	}
	if _, err := ReadAll(bytes.NewReader(a), WithMaxTotalSize(3000)); err != nil {
		t.Fatalf("unexpected error under cap: %v", err)
	}
}

func TestReadAllContext_CancellationHonoured(t *testing.T) {
	a := buildEntry("a.bin", "application/octet-stream", 0, []byte("payload"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel
	_, err := ReadAllContext(ctx, bytes.NewReader(a))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestReadAll_TruncatedClassifiesAsErrTruncated(t *testing.T) {
	// Build a complete entry then chop off the last 5 bytes of its
	// payload. The parser commits to a CompressedSize and io.ReadFull
	// trips on the short payload.
	a := buildEntry("a.bin", "application/octet-stream", 0, bytes.Repeat([]byte{1}, 100))
	truncated := a[:len(a)-5]
	_, err := ReadAll(bytes.NewReader(truncated))
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("expected ErrTruncated, got %v", err)
	}
}

func TestValidate_ArchiveMissingSceneJSON(t *testing.T) {
	a := buildEntry("only.bin", "application/octet-stream", 0, []byte("x"))
	err := Validate(bytes.NewReader(a))
	if !errors.Is(err, ErrMissingEntry) {
		t.Fatalf("expected ErrMissingEntry, got %v", err)
	}
}

func TestValidate_MeshFileReferenceMissing(t *testing.T) {
	scn := []byte(`{"meshes":[{"name":"M","indexCount":0,"indexTypeSize":2,"wireCount":0,"vertexCount":0,"file":"missing.dat","subMeshes":[]}],"materials":[]}`)
	a := bytes.Join([][]byte{
		buildEntry("scene.json", "application/json", 0, scn),
	}, nil)
	err := Validate(bytes.NewReader(a))
	if !errors.Is(err, ErrMissingEntry) {
		t.Fatalf("expected ErrMissingEntry, got %v", err)
	}
}

func TestValidate_HappyPath(t *testing.T) {
	scn := []byte(`{"meshes":[],"materials":[]}`)
	a := buildEntry("scene.json", "application/json", 0, scn)
	if err := Validate(bytes.NewReader(a)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestConvertFromFS_RoundTrip(t *testing.T) {
	scn := []byte(`{"meshes":[],"materials":[]}`)
	a := buildEntry("scene.json", "application/json", 0, scn)
	fsys := fstest.MapFS{
		"models/example.mview": &fstest.MapFile{Data: a},
	}
	var out bytes.Buffer
	if err := ConvertFromFS(context.Background(), fsys, "models/example.mview", &out); err != nil {
		t.Fatalf("ConvertFromFS: %v", err)
	}
	if out.Len() < 20 || string(out.Bytes()[:4]) != "glTF" {
		t.Fatalf("expected GLB output, got %d bytes %q", out.Len(), out.Bytes()[:min(8, out.Len())])
	}
}

func TestConvertToGLBContext_Deadline(t *testing.T) {
	a := buildEntry("scene.json", "application/json", 0, []byte(`{"meshes":[],"materials":[]}`))
	ctx, cancel := context.WithTimeout(context.Background(), -1*time.Second)
	defer cancel()
	var out bytes.Buffer
	err := ConvertToGLBContext(ctx, bytes.NewReader(a), &out)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
}

func TestIsMview_PositiveAndNegative(t *testing.T) {
	a := buildEntry("scene.json", "application/json", 0, []byte("{}"))
	ok, _, err := IsMview(bytes.NewReader(a))
	if err != nil {
		t.Fatalf("IsMview: %v", err)
	}
	if !ok {
		t.Errorf("expected positive detection for a real-looking archive")
	}

	noise := bytes.Repeat([]byte{0xff}, 512)
	ok, _, err = IsMview(bytes.NewReader(noise))
	if err != nil {
		t.Fatalf("IsMview noise: %v", err)
	}
	if ok {
		t.Errorf("expected negative detection for pure noise")
	}
}

func TestWithConcurrency_ZeroDefaultsToCores(t *testing.T) {
	o := resolveOptions([]Option{WithConcurrency(0)})
	if o.Concurrency < 1 {
		t.Fatalf("WithConcurrency(0) should default to >=1, got %d", o.Concurrency)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
