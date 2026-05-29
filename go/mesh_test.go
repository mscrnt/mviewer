package mview

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// vec2 / vec3 / vec4 builders so the test fixtures are readable.
func packIndices16(idx ...uint16) []byte {
	var buf bytes.Buffer
	for _, v := range idx {
		_ = binary.Write(&buf, binary.LittleEndian, v)
	}
	return buf.Bytes()
}

func packF32(values ...float32) []byte {
	var buf bytes.Buffer
	for _, v := range values {
		_ = binary.Write(&buf, binary.LittleEndian, v)
	}
	return buf.Bytes()
}

func packU16(values ...uint16) []byte {
	var buf bytes.Buffer
	for _, v := range values {
		_ = binary.Write(&buf, binary.LittleEndian, v)
	}
	return buf.Bytes()
}

// packUnitVecHalfZero produces the raw uint16 pair that decodes to
// the (1, 0, 0) axis — useful as a calibration anchor in the tests.
func unitVecRawXAxis() (uint16, uint16) {
	// x = rawX/32767.4*2 - 1 → rawX such that x≈1 → rawX≈32767
	// y = 0 → rawY such that y'=0.5*32767.4 ≈ 16384 (decodes to 0)
	return 32767, uint16(math.Round(0.5 * 32767.4))
}

// Round-trip the smallest possible mesh: 3 indices (one triangle),
// 3 vertices, no secondary UV, no vertex color.
func TestDecodeMesh_MinimalTriangle(t *testing.T) {
	rawX, rawY := unitVecRawXAxis()
	var blob bytes.Buffer

	// Indices (3 × u16).
	blob.Write(packIndices16(0, 1, 2))

	// Three vertices, base stride (32B each):
	//   3 floats position + 2 floats UV + 3×(2×u16 = 4B) = 12+8+12 = 32
	for i := 0; i < 3; i++ {
		blob.Write(packF32(float32(i), float32(i+1), float32(i+2))) // position
		blob.Write(packF32(0.25, 0.75))                             // UV (V will be negated)
		blob.Write(packU16(rawX, rawY))                             // tangent
		blob.Write(packU16(rawX, rawY))                             // bitangent
		blob.Write(packU16(rawX, rawY))                             // normal
	}

	mesh := &MeshDesc{
		Name:          "T",
		IndexCount:    3,
		IndexTypeSize: 2,
		WireCount:     0,
		VertexCount:   3,
	}

	got, err := DecodeMesh(blob.Bytes(), mesh)
	if err != nil {
		t.Fatalf("DecodeMesh: %v", err)
	}
	if !equalU32Slice(got.Indices, []uint32{0, 1, 2}) {
		t.Errorf("indices: got %v", got.Indices)
	}
	wantPos := [][3]float32{{0, 1, 2}, {1, 2, 3}, {2, 3, 4}}
	for i, p := range got.Positions {
		if p != wantPos[i] {
			t.Errorf("position[%d]: got %v want %v", i, p, wantPos[i])
		}
	}
	for i, uv := range got.TexCoords {
		// V channel is flipped on read, so the stored 0.75 should
		// emerge as -0.75.
		if uv[0] != 0.25 || uv[1] != -0.75 {
			t.Errorf("uv[%d]: got %v want {0.25, -0.75}", i, uv)
		}
	}
	for i, n := range got.Normals {
		// Approximately (1, 0, 0). The 16-bit-per-component encoding
		// loses ~1% on the recovered Z when X is near unit length —
		// that's intrinsic to the format, not our decoder.
		if math.Abs(float64(n[0]-1)) > 1e-2 || math.Abs(float64(n[1])) > 1e-2 || math.Abs(float64(n[2])) > 1e-2 {
			t.Errorf("normal[%d]: got %v want ~{1,0,0}", i, n)
		}
	}
	if got.SecondaryTexCoords != nil {
		t.Errorf("secondary UVs should be nil when absent")
	}
	if got.Colors != nil {
		t.Errorf("colors should be nil when absent")
	}
}

// Wireframe indices should be skipped, not included in the index
// buffer. Encoder writes a 6-index wireframe block after the real
// indices that must be consumed before vertices begin.
func TestDecodeMesh_SkipsWireframe(t *testing.T) {
	rawX, rawY := unitVecRawXAxis()
	var blob bytes.Buffer

	blob.Write(packIndices16(0, 1, 2))             // real indices
	blob.Write(packIndices16(99, 98, 97, 96, 95, 94)) // wireframe — should be skipped

	for i := 0; i < 3; i++ {
		blob.Write(packF32(float32(i), 0, 0))
		blob.Write(packF32(0, 0))
		blob.Write(packU16(rawX, rawY))
		blob.Write(packU16(rawX, rawY))
		blob.Write(packU16(rawX, rawY))
	}

	got, err := DecodeMesh(blob.Bytes(), &MeshDesc{
		Name: "W", IndexCount: 3, IndexTypeSize: 2,
		WireCount: 6, VertexCount: 3,
	})
	if err != nil {
		t.Fatalf("DecodeMesh: %v", err)
	}
	if !equalU32Slice(got.Indices, []uint32{0, 1, 2}) {
		t.Fatalf("wireframe leaked into index buffer: %v", got.Indices)
	}
	if got.Positions[0][0] != 0 || got.Positions[1][0] != 1 || got.Positions[2][0] != 2 {
		t.Errorf("positions misaligned after wire skip: %v", got.Positions)
	}
}

// Secondary UV + vertex color flags should grow the stride and
// populate the optional slices. Confirms both branches.
func TestDecodeMesh_SecondaryUVAndVertexColor(t *testing.T) {
	rawX, rawY := unitVecRawXAxis()
	var blob bytes.Buffer

	blob.Write(packIndices16(0)) // one index, one vertex
	blob.Write(packF32(7, 8, 9)) // position
	blob.Write(packF32(0.1, 0.2)) // primary UV
	blob.Write(packF32(0.3, 0.4)) // secondary UV
	blob.Write(packU16(rawX, rawY))
	blob.Write(packU16(rawX, rawY))
	blob.Write(packU16(rawX, rawY))
	blob.Write([]byte{255, 128, 0, 255}) // RGBA color

	one := uint32(1)
	got, err := DecodeMesh(blob.Bytes(), &MeshDesc{
		Name: "C", IndexCount: 1, IndexTypeSize: 2,
		VertexCount: 1, SecondaryTexCoord: &one, VertexColor: &one,
	})
	if err != nil {
		t.Fatalf("DecodeMesh: %v", err)
	}
	if got.SecondaryTexCoords == nil || len(got.SecondaryTexCoords) != 1 {
		t.Fatalf("secondary UV slice missing")
	}
	if got.SecondaryTexCoords[0] != [2]float32{0.3, -0.4} {
		t.Errorf("secondary uv: got %v want {0.3,-0.4}", got.SecondaryTexCoords[0])
	}
	if got.Colors == nil || len(got.Colors) != 1 {
		t.Fatalf("color slice missing")
	}
	c := got.Colors[0]
	if c[0] != 1 {
		t.Errorf("color R: got %v want 1", c[0])
	}
	if math.Abs(float64(c[1]-128.0/255.0)) > 1e-6 {
		t.Errorf("color G: got %v want %v", c[1], 128.0/255.0)
	}
	if c[2] != 0 {
		t.Errorf("color B: got %v want 0", c[2])
	}
	if c[3] != 1 {
		t.Errorf("color A: got %v want 1", c[3])
	}
}

// Probe the unit-vector unpacker at canonical axis values. The encoder
// stores each component as 16 bits scaled to [-1, 1], which loses ~1%
// of unit-vector accuracy near the axes — the reconstructed Z gains a
// small residual whenever X or Y are already near ±1. Allow for it.
func TestUnpackUnitVector_Axes(t *testing.T) {
	const eps = 1e-2
	// (1, 0, 0) — rawX max, rawY mid (so y≈0, Z sign positive)
	xx, xy := unitVecRawXAxis()
	v := unpackUnitVector(xx, xy)
	checkAxis(t, "+X", v, 1, 0, 0, eps)

	// (-1, 0, 0) — rawX min, rawY mid
	v = unpackUnitVector(0, xy)
	checkAxis(t, "-X", v, -1, 0, 0, eps)

	// (0, 0, 1) — both x and y mid, no z sign bit
	mid := uint16(math.Round(0.5 * 32767.4))
	v = unpackUnitVector(mid, mid)
	checkAxis(t, "+Z", v, 0, 0, 1, eps)

	// (0, 0, -1) — same as +Z but with the high bit of y set
	v = unpackUnitVector(mid, mid|0x8000)
	checkAxis(t, "-Z", v, 0, 0, -1, eps)
}

func TestDecodeMesh_UnsupportedIndexSize(t *testing.T) {
	_, err := DecodeMesh([]byte{0, 0}, &MeshDesc{Name: "X", IndexCount: 1, IndexTypeSize: 1, VertexCount: 0})
	if err == nil {
		t.Fatal("expected error for indexTypeSize=1")
	}
}

func TestDecodeMesh_U32Indices(t *testing.T) {
	rawX, rawY := unitVecRawXAxis()
	var blob bytes.Buffer
	// Two u32 indices.
	_ = binary.Write(&blob, binary.LittleEndian, uint32(0))
	_ = binary.Write(&blob, binary.LittleEndian, uint32(1))
	for i := 0; i < 2; i++ {
		blob.Write(packF32(0, 0, 0))
		blob.Write(packF32(0, 0))
		blob.Write(packU16(rawX, rawY))
		blob.Write(packU16(rawX, rawY))
		blob.Write(packU16(rawX, rawY))
	}
	got, err := DecodeMesh(blob.Bytes(), &MeshDesc{
		Name: "U32", IndexCount: 2, IndexTypeSize: 4, VertexCount: 2,
	})
	if err != nil {
		t.Fatalf("DecodeMesh: %v", err)
	}
	if !equalU32Slice(got.Indices, []uint32{0, 1}) {
		t.Errorf("u32 indices: got %v", got.Indices)
	}
}

func equalU32Slice(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func checkAxis(t *testing.T, name string, got [3]float32, wx, wy, wz float32, eps float64) {
	t.Helper()
	if math.Abs(float64(got[0]-wx)) > eps ||
		math.Abs(float64(got[1]-wy)) > eps ||
		math.Abs(float64(got[2]-wz)) > eps {
		t.Errorf("%s: got %v want (%v, %v, %v) ±%v", name, got, wx, wy, wz, eps)
	}
}
