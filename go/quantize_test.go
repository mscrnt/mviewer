package mview

import (
	"bytes"
	"testing"

	"github.com/qmuntal/gltf"
)

func TestQuantizedMesh_EmitsKHRExtension(t *testing.T) {
	sceneJSON := `{
		"meshes":[{
			"name":"Tri","indexCount":3,"indexTypeSize":2,
			"wireCount":0,"vertexCount":3,"file":"mesh0.dat",
			"subMeshes":[{"material":"Mat","firstIndex":0,"indexCount":3}]
		}],
		"materials":[{"name":"Mat","albedoTex":""}]
	}`
	archive := buildArchive(sceneJSON, "mesh0.dat", buildMeshBytes(t))

	glb, err := ConvertBytesToGLB(archive, WithQuantizedMesh(true))
	if err != nil {
		t.Fatalf("ConvertBytesToGLB: %v", err)
	}
	var doc gltf.Document
	if err := gltf.NewDecoder(bytes.NewReader(glb)).Decode(&doc); err != nil {
		t.Fatalf("re-decode: %v", err)
	}

	foundUsed := false
	for _, e := range doc.ExtensionsUsed {
		if e == "KHR_mesh_quantization" {
			foundUsed = true
		}
	}
	if !foundUsed {
		t.Errorf("KHR_mesh_quantization not in extensionsUsed: %v", doc.ExtensionsUsed)
	}
	foundReq := false
	for _, e := range doc.ExtensionsRequired {
		if e == "KHR_mesh_quantization" {
			foundReq = true
		}
	}
	if !foundReq {
		t.Errorf("KHR_mesh_quantization not in extensionsRequired: %v", doc.ExtensionsRequired)
	}

	// At least one accessor should be ComponentShort + Normalized
	// — that's the marker of a quantized normal/tangent.
	hasQuantized := false
	for _, a := range doc.Accessors {
		if a.ComponentType == gltf.ComponentShort && a.Normalized {
			hasQuantized = true
			break
		}
	}
	if !hasQuantized {
		t.Error("no quantized accessor emitted")
	}
}

func TestFloatToI16Normalized_BoundaryClamp(t *testing.T) {
	if floatToI16Normalized(2) != 32767 {
		t.Errorf("clamp +overshoot failed: %d", floatToI16Normalized(2))
	}
	if floatToI16Normalized(-2) != -32767 {
		t.Errorf("clamp -overshoot failed: %d", floatToI16Normalized(-2))
	}
	if floatToI16Normalized(0) != 0 {
		t.Errorf("zero mapping failed: %d", floatToI16Normalized(0))
	}
}
