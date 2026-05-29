package mview

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/qmuntal/gltf"
)

// buildArchive packages a scene.json + one mesh blob into a synthetic
// .mview byte stream so the GLB converter can be exercised without a
// real Toolbag export.
func buildArchive(sceneJSON, meshFile string, meshBytes []byte) []byte {
	var buf bytes.Buffer
	buf.Write(buildEntry("scene.json", "application/json", 0, []byte(sceneJSON)))
	buf.Write(buildEntry(meshFile, "application/octet-stream", 0, meshBytes))
	return buf.Bytes()
}

// buildMeshBytes lays out one triangle in the mesh.dat shape — three
// u16 indices, no wireframe, then three vertices of base stride 32B.
func buildMeshBytes(t *testing.T) []byte {
	t.Helper()
	rawX, rawY := unitVecRawXAxis()
	var blob bytes.Buffer
	_ = binary.Write(&blob, binary.LittleEndian, uint16(0))
	_ = binary.Write(&blob, binary.LittleEndian, uint16(1))
	_ = binary.Write(&blob, binary.LittleEndian, uint16(2))
	for i := 0; i < 3; i++ {
		_ = binary.Write(&blob, binary.LittleEndian, float32(i))
		_ = binary.Write(&blob, binary.LittleEndian, float32(0))
		_ = binary.Write(&blob, binary.LittleEndian, float32(0))
		// UV
		_ = binary.Write(&blob, binary.LittleEndian, float32(0.5))
		_ = binary.Write(&blob, binary.LittleEndian, float32(0.5))
		// tangent / bitangent / normal
		for j := 0; j < 3; j++ {
			_ = binary.Write(&blob, binary.LittleEndian, rawX)
			_ = binary.Write(&blob, binary.LittleEndian, rawY)
		}
	}
	return blob.Bytes()
}

// End-to-end: build a synthetic archive, convert it, re-parse the GLB
// the converter wrote, and confirm the round trip preserves mesh shape
// and material slot references.
func TestConvertToGLB_RoundTrip(t *testing.T) {
	sceneJSON := `{
		"meshes":[{
			"name":"Tri","indexCount":3,"indexTypeSize":2,
			"wireCount":0,"vertexCount":3,"file":"mesh0.dat",
			"subMeshes":[{"material":"Mat","firstIndex":0,"indexCount":3}]
		}],
		"materials":[{"name":"Mat","albedoTex":"albedo.png"}]
	}`
	archive := buildArchive(sceneJSON, "mesh0.dat", buildMeshBytes(t))

	var out bytes.Buffer
	if err := ConvertToGLB(bytes.NewReader(archive), &out); err != nil {
		t.Fatalf("ConvertToGLB: %v", err)
	}
	if out.Len() < 100 {
		t.Fatalf("GLB output suspiciously small: %d bytes", out.Len())
	}
	// GLB magic = "glTF" at byte 0.
	if string(out.Bytes()[:4]) != "glTF" {
		t.Fatalf("output not a GLB (bad magic %q)", out.Bytes()[:4])
	}

	var doc gltf.Document
	if err := gltf.NewDecoder(bytes.NewReader(out.Bytes())).Decode(&doc); err != nil {
		t.Fatalf("re-decode GLB: %v", err)
	}
}

// Decode the round-trip GLB and confirm the structural pieces survived.
func TestConvertToGLB_StructurePreserved(t *testing.T) {
	sceneJSON := `{
		"meshes":[{
			"name":"Tri","indexCount":3,"indexTypeSize":2,
			"wireCount":0,"vertexCount":3,"file":"mesh0.dat",
			"subMeshes":[{"material":"Mat","firstIndex":0,"indexCount":3}]
		}],
		"materials":[{"name":"Mat","albedoTex":"albedo.png"}]
	}`
	archive := buildArchive(sceneJSON, "mesh0.dat", buildMeshBytes(t))

	glb, err := ConvertBytesToGLB(archive)
	if err != nil {
		t.Fatalf("ConvertBytesToGLB: %v", err)
	}

	var doc gltf.Document
	if err := gltf.NewDecoder(bytes.NewReader(glb)).Decode(&doc); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if len(doc.Meshes) != 1 || doc.Meshes[0].Name != "Tri" {
		t.Errorf("expected one mesh named Tri, got %+v", doc.Meshes)
	}
	if len(doc.Meshes[0].Primitives) != 1 {
		t.Errorf("expected one primitive per submesh, got %d", len(doc.Meshes[0].Primitives))
	}
	prim := doc.Meshes[0].Primitives[0]
	for _, want := range []string{gltf.POSITION, gltf.NORMAL, gltf.TANGENT, gltf.TEXCOORD_0} {
		if _, ok := prim.Attributes[want]; !ok {
			t.Errorf("primitive missing required attribute %q", want)
		}
	}
	if prim.Material == nil {
		t.Errorf("primitive missing material binding")
	}
	if len(doc.Materials) != 1 || doc.Materials[0].Name != "Mat" {
		t.Errorf("expected one material named Mat, got %+v", doc.Materials)
	}
	if len(doc.Nodes) != 1 || doc.Nodes[0].Name != "Tri" {
		t.Errorf("expected one root node named Tri, got %+v", doc.Nodes)
	}
}

func TestConvertToGLB_MissingMeshBlob(t *testing.T) {
	sceneJSON := `{
		"meshes":[{
			"name":"Ghost","indexCount":3,"indexTypeSize":2,
			"wireCount":0,"vertexCount":3,"file":"NOT_IN_ARCHIVE.dat",
			"subMeshes":[{"material":"Mat","firstIndex":0,"indexCount":3}]
		}],
		"materials":[{"name":"Mat","albedoTex":"a.png"}]
	}`
	// Archive contains only scene.json — no mesh blob.
	archive := buildEntry("scene.json", "application/json", 0, []byte(sceneJSON))
	if _, err := ConvertBytesToGLB(archive); err == nil {
		t.Fatal("expected error when referenced mesh blob is absent")
	}
}

func TestConvertToGLB_MissingSceneJSON(t *testing.T) {
	archive := buildEntry("only.bin", "application/octet-stream", 0, []byte("nope"))
	if _, err := ConvertBytesToGLB(archive); err == nil {
		t.Fatal("expected error when scene.json is absent")
	}
}

// packTangent should emit w=+1 when (t,b,n) form a right-handed basis
// (b == cross(n,t)), and w=-1 otherwise. Picks two simple unit-basis
// cases to lock the sign convention down.
func TestPackTangent_HandednessSign(t *testing.T) {
	rh := packTangent(
		[3]float32{1, 0, 0}, // tangent  +X
		[3]float32{0, 1, 0}, // bitangent +Y
		[3]float32{0, 0, 1}, // normal   +Z
	)
	// cross(N=+Z, T=+X) = +Y, so b matches +cross(N,T) → w = +1.
	if rh[3] != 1 {
		t.Errorf("right-handed basis: got w=%v want +1", rh[3])
	}
	lh := packTangent(
		[3]float32{1, 0, 0},  // T = +X
		[3]float32{0, -1, 0}, // B = -Y (flipped)
		[3]float32{0, 0, 1},  // N = +Z
	)
	if lh[3] != -1 {
		t.Errorf("left-handed basis: got w=%v want -1", lh[3])
	}
	// Tangent components are passed through untouched.
	if math.Abs(float64(rh[0]-1)) > 1e-6 || rh[1] != 0 || rh[2] != 0 {
		t.Errorf("packTangent xyz mangled: %v", rh)
	}
}
