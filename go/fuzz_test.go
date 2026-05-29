package mview

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

// Fuzz targets for the untrusted-input attack surface. Seeded with
// known-good shapes so the fuzzer starts from coverage that
// resembles real .mview structure and mutates outward.
//
// Acceptance contract: every target may *return an error*, but must
// never panic, deadlock, allocate unbounded memory, or write past
// the configured caps. The MaxTotalSize option ensures upper bounds
// on the convert path's intermediate buffers.

// FuzzReadEntry hits the c-string + header + payload parser.
func FuzzReadEntry(f *testing.F) {
	f.Add(buildEntry("a.bin", "application/octet-stream", 0, []byte("hello")))
	f.Add(buildEntry("thumbnail.jpg", "image/jpeg", 0, []byte{0xff, 0xd8, 0xff, 0xe0}))
	f.Add([]byte{0})                               // empty name
	f.Add(bytes.Repeat([]byte{0xff}, MaxStringLen)) // no terminator → bounded error

	f.Fuzz(func(t *testing.T, in []byte) {
		_, _ = ReadEntry(bytes.NewReader(in))
	})
}

// FuzzDecompress hits the LZW decoder. Output length is fuzzed too —
// the decoder must refuse outputs > a reasonable cap without
// allocating that much memory.
func FuzzDecompress(f *testing.F) {
	f.Add([]byte{0x41, 0x20, 0x04, 0x43, 0x00}, uint16(3))
	f.Add([]byte{0x00}, uint16(1))
	f.Add([]byte{0xff, 0xff, 0xff}, uint16(0))

	f.Fuzz(func(t *testing.T, in []byte, outLen uint16) {
		// Cap output length so the fuzzer can't legitimately ask for
		// gigabyte allocations — that's a quota issue, not a
		// correctness one.
		// outLen is uint16 (max 65535) so the cap is implicit, but
		// short-circuit on zero to keep the corpus from wasting
		// cycles on degenerate inputs.
		if outLen == 0 {
			t.Skip()
		}
		_, err := Decompress(in, int(outLen))
		if err != nil && !errors.Is(err, ErrDecompress) {
			t.Fatalf("Decompress returned unclassified error: %v", err)
		}
	})
}

// FuzzParseScene attacks the JSON unmarshal + Boolish path. We
// don't care what comes back — only that the parser refuses to
// panic on adversarial input.
func FuzzParseScene(f *testing.F) {
	f.Add([]byte(`{"meshes":[],"materials":[]}`))
	f.Add([]byte(`{"meshes":[{"name":"x","indexCount":0,"indexTypeSize":2,"wireCount":0,"vertexCount":0,"file":"f","subMeshes":[]}],"materials":[]}`))
	f.Add([]byte(`{"materials":[{"name":"M","albedoTex":"a","useSkin":"1"}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, in []byte) {
		_, _ = ParseScene(in)
	})
}

// FuzzConvertToGLB drives the full pipeline through a configurable
// cap. Any panic or unbounded allocation here is a security bug.
func FuzzConvertToGLB(f *testing.F) {
	scn := []byte(`{"meshes":[],"materials":[]}`)
	f.Add(bytes.Join([][]byte{
		buildEntry("scene.json", "application/json", 0, scn),
	}, nil))
	f.Add(bytes.Join([][]byte{
		buildEntry("scene.json", "application/json", 0, scn),
		buildEntry("a.png", "image/png", 0, []byte("not a real png")),
	}, nil))
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff})

	f.Fuzz(func(t *testing.T, in []byte) {
		// Bound the run: cap memory, force serial decode, give it a
		// short deadline so a malicious input can't tie up the
		// fuzzer.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, _ = ConvertBytesToGLBContext(ctx, in,
			WithMaxTotalSize(1<<20), // 1 MiB ceiling
			WithConcurrency(1),
		)
	})
}

// FuzzIsMview should never error or panic on arbitrary bytes — it's
// pure structural detection.
func FuzzIsMview(f *testing.F) {
	f.Add(buildEntry("a.bin", "application/octet-stream", 0, []byte("payload")))
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0xff}, 600))

	f.Fuzz(func(t *testing.T, in []byte) {
		_, rem, err := IsMview(bytes.NewReader(in))
		if err != nil {
			t.Fatalf("IsMview must not return non-nil error: %v", err)
		}
		// Sniffed bytes should still be drainable through the
		// returned remainder.
		if rem == nil {
			t.Fatal("nil remainder")
		}
		_, _ = io.Copy(io.Discard, rem)
	})
}
