package mview

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"testing"
)

// Synthetic benchmark: build a large in-memory archive (no disk
// dependency, no upstream sample) so the benchmark is reproducible on
// any developer's machine and CI runner.
//
// MVIEW_BENCH_SAMPLE can point at a real .mview for absolute numbers
// when available. The synthetic benchmark below covers the regression-
// detection use case.

func benchSyntheticArchive(b *testing.B, vertexCount int) []byte {
	b.Helper()
	scn := []byte(`{
		"meshes":[{
			"name":"Bench","indexCount":` + intStr(vertexCount) + `,
			"indexTypeSize":2,"wireCount":0,"vertexCount":` + intStr(vertexCount) + `,
			"file":"mesh0.dat",
			"subMeshes":[{"material":"Mat","firstIndex":0,"indexCount":` + intStr(vertexCount) + `}]
		}],
		"materials":[{"name":"Mat","albedoTex":""}]
	}`)

	rawX := uint16(32767)
	rawY := uint16(16384)
	var blob bytes.Buffer
	for i := 0; i < vertexCount; i++ {
		_ = binary.Write(&blob, binary.LittleEndian, uint16(i%vertexCount))
	}
	for i := 0; i < vertexCount; i++ {
		_ = binary.Write(&blob, binary.LittleEndian, float32(i)*0.001)
		_ = binary.Write(&blob, binary.LittleEndian, float32(i)*0.002)
		_ = binary.Write(&blob, binary.LittleEndian, float32(i)*0.003)
		_ = binary.Write(&blob, binary.LittleEndian, float32(i)*0.01)
		_ = binary.Write(&blob, binary.LittleEndian, float32(i)*0.02)
		for j := 0; j < 3; j++ {
			_ = binary.Write(&blob, binary.LittleEndian, rawX)
			_ = binary.Write(&blob, binary.LittleEndian, rawY)
		}
	}

	return bytes.Join([][]byte{
		buildEntry("scene.json", "application/json", 0, scn),
		buildEntry("mesh0.dat", "application/octet-stream", 0, blob.Bytes()),
	}, nil)
}

func intStr(n int) string {
	// tiny inline itoa so the helper doesn't depend on strconv just
	// for this template-substitution.
	if n == 0 {
		return "0"
	}
	digits := make([]byte, 0, 12)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func BenchmarkConvertToGLB_Synthetic1K(b *testing.B) {
	archive := benchSyntheticArchive(b, 1024)
	b.ResetTimer()
	b.SetBytes(int64(len(archive)))
	for i := 0; i < b.N; i++ {
		if _, err := ConvertBytesToGLB(archive); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkConvertToGLB_Synthetic50K(b *testing.B) {
	archive := benchSyntheticArchive(b, 50_000)
	b.ResetTimer()
	b.SetBytes(int64(len(archive)))
	for i := 0; i < b.N; i++ {
		if _, err := ConvertBytesToGLB(archive); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkConvertToGLB_Synthetic50K_Quantized(b *testing.B) {
	archive := benchSyntheticArchive(b, 50_000)
	b.ResetTimer()
	b.SetBytes(int64(len(archive)))
	for i := 0; i < b.N; i++ {
		if _, err := ConvertBytesToGLB(archive, WithQuantizedMesh(true)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkValidate_Synthetic50K(b *testing.B) {
	archive := benchSyntheticArchive(b, 50_000)
	b.ResetTimer()
	b.SetBytes(int64(len(archive)))
	for i := 0; i < b.N; i++ {
		if err := Validate(bytes.NewReader(archive)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeMesh_Synthetic50K(b *testing.B) {
	archive := benchSyntheticArchive(b, 50_000)
	entries, err := ReadAll(bytes.NewReader(archive))
	if err != nil {
		b.Fatal(err)
	}
	scene, err := ParseScene(entries["scene.json"].Data)
	if err != nil {
		b.Fatal(err)
	}
	blob := entries["mesh0.dat"].Data
	desc := &scene.Meshes[0]
	b.ResetTimer()
	b.SetBytes(int64(len(blob)))
	for i := 0; i < b.N; i++ {
		if _, err := DecodeMesh(blob, desc); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExtractThumbnail_Embedded(b *testing.B) {
	thumb := bytes.Repeat([]byte{0xff}, 32*1024)
	archive := bytes.Join([][]byte{
		buildEntry("scene.json", "application/json", 0, []byte("{}")),
		buildEntry("thumbnail.jpg", "image/jpeg", 0, thumb),
	}, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ExtractThumbnail(bytes.NewReader(archive)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkConvertToGLB_RealSample is opt-in: set MVIEW_BENCH_SAMPLE
// to a .mview path. Skipped silently otherwise so CI doesn't depend
// on a bundled artefact.
func BenchmarkConvertToGLB_RealSample(b *testing.B) {
	path := os.Getenv("MVIEW_BENCH_SAMPLE")
	if path == "" {
		b.Skip("set MVIEW_BENCH_SAMPLE=/path/to/sample.mview to enable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		if _, err := ConvertBytesToGLBContext(context.Background(), data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkConvertToGLB_RealSampleParallel(b *testing.B) {
	path := os.Getenv("MVIEW_BENCH_SAMPLE")
	if path == "" {
		b.Skip("set MVIEW_BENCH_SAMPLE=/path/to/sample.mview to enable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		if _, err := ConvertBytesToGLB(data, WithConcurrency(4)); err != nil {
			b.Fatal(err)
		}
	}
}

