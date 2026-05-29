package mview

import (
	"bytes"
	"testing"

	"github.com/qmuntal/gltf"
)

// One-pixel PNGs / JPEGs synthesised at runtime so the tests don't
// need a binary fixture bundled in the repo. modeler.WriteImage just
// embeds the bytes verbatim and asks no questions about their shape,
// which is enough to round-trip texture wiring.
var (
	tinyPNG = []byte{
		// PNG sig + minimal IHDR + IDAT + IEND, 1×1 transparent pixel.
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
		0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
		0x42, 0x60, 0x82,
	}
)

func buildArchiveWithTextures(sceneJSON, meshFile string, meshBytes []byte, textures map[string][]byte) []byte {
	var buf bytes.Buffer
	buf.Write(buildEntry("scene.json", "application/json", 0, []byte(sceneJSON)))
	buf.Write(buildEntry(meshFile, "application/octet-stream", 0, meshBytes))
	for name, data := range textures {
		mime := guessMIME(name)
		buf.Write(buildEntry(name, mime, 0, data))
	}
	return buf.Bytes()
}

func TestBuildMaterials_BindsAlbedoAndNormal(t *testing.T) {
	sceneJSON := `{
		"meshes":[{
			"name":"M","indexCount":3,"indexTypeSize":2,
			"wireCount":0,"vertexCount":3,"file":"mesh0.dat",
			"subMeshes":[{"material":"Mat","firstIndex":0,"indexCount":3}]
		}],
		"materials":[{
			"name":"Mat","albedoTex":"albedo.png","normalTex":"normal.png"
		}]
	}`
	archive := buildArchiveWithTextures(sceneJSON, "mesh0.dat", buildMeshBytes(t),
		map[string][]byte{"albedo.png": tinyPNG, "normal.png": tinyPNG})

	glb, err := ConvertBytesToGLB(archive)
	if err != nil {
		t.Fatalf("ConvertBytesToGLB: %v", err)
	}
	var doc gltf.Document
	if err := gltf.NewDecoder(bytes.NewReader(glb)).Decode(&doc); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if len(doc.Materials) != 1 {
		t.Fatalf("expected one material, got %d", len(doc.Materials))
	}
	m := doc.Materials[0]
	if m.PBRMetallicRoughness == nil || m.PBRMetallicRoughness.BaseColorTexture == nil {
		t.Fatalf("base color texture not bound: %+v", m.PBRMetallicRoughness)
	}
	if m.NormalTexture == nil {
		t.Fatalf("normal texture not bound")
	}
	// Both textures should land in the glTF doc; cache means each
	// distinct name registers once.
	if len(doc.Textures) != 2 {
		t.Errorf("expected 2 textures, got %d", len(doc.Textures))
	}
	if len(doc.Images) != 2 {
		t.Errorf("expected 2 images, got %d", len(doc.Images))
	}
}

func TestBuildMaterials_SoftFailOnMissingTexture(t *testing.T) {
	sceneJSON := `{
		"meshes":[{
			"name":"M","indexCount":3,"indexTypeSize":2,
			"wireCount":0,"vertexCount":3,"file":"mesh0.dat",
			"subMeshes":[{"material":"Mat","firstIndex":0,"indexCount":3}]
		}],
		"materials":[{"name":"Mat","albedoTex":"absent.png"}]
	}`
	archive := buildArchiveWithTextures(sceneJSON, "mesh0.dat", buildMeshBytes(t), nil)
	glb, err := ConvertBytesToGLB(archive)
	if err != nil {
		t.Fatalf("missing texture should soft-fail, got hard error: %v", err)
	}
	var doc gltf.Document
	if err := gltf.NewDecoder(bytes.NewReader(glb)).Decode(&doc); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if len(doc.Materials) != 1 {
		t.Fatalf("expected one material, got %d", len(doc.Materials))
	}
	if doc.Materials[0].PBRMetallicRoughness != nil && doc.Materials[0].PBRMetallicRoughness.BaseColorTexture != nil {
		t.Error("base color texture should be nil when source is absent")
	}
}

func TestBuildMaterials_SharedTextureCached(t *testing.T) {
	sceneJSON := `{
		"meshes":[],
		"materials":[
			{"name":"A","albedoTex":"shared.png"},
			{"name":"B","albedoTex":"shared.png"}
		]
	}`
	archive := buildArchiveWithTextures(sceneJSON, "mesh0.dat", nil,
		map[string][]byte{"shared.png": tinyPNG})
	glb, err := ConvertBytesToGLB(archive)
	if err != nil {
		t.Fatalf("ConvertBytesToGLB: %v", err)
	}
	var doc gltf.Document
	if err := gltf.NewDecoder(bytes.NewReader(glb)).Decode(&doc); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if len(doc.Textures) != 1 {
		t.Errorf("two materials sharing a texture should land in one glTF texture, got %d", len(doc.Textures))
	}
	if len(doc.Images) != 1 {
		t.Errorf("expected one shared image, got %d", len(doc.Images))
	}
}

func TestResolveAlpha(t *testing.T) {
	blend := "alpha"
	add := "add"
	cutoff := float32(0.3)
	alphaTex := "a.png"

	cases := []struct {
		name       string
		mat        MaterialDesc
		wantMode   string
		wantCutoff *float32
	}{
		{"BLEND from blend=alpha", MaterialDesc{Blend: &blend}, "BLEND", nil},
		{"BLEND from blend=add", MaterialDesc{Blend: &add}, "BLEND", nil},
		{"MASK from alphaTex default cutoff", MaterialDesc{AlphaTex: &alphaTex}, "MASK", floatp(0.5)},
		{"MASK from alphaTex + alphaTest", MaterialDesc{AlphaTex: &alphaTex, AlphaTest: &cutoff}, "MASK", &cutoff},
		{"OPAQUE when nothing set", MaterialDesc{}, "", nil},
	}
	for _, c := range cases {
		gotMode, gotCutoff := resolveAlpha(&c.mat)
		if gotMode != c.wantMode {
			t.Errorf("%s: mode got %q want %q", c.name, gotMode, c.wantMode)
		}
		if (gotCutoff == nil) != (c.wantCutoff == nil) {
			t.Errorf("%s: cutoff nil-ness got %v want %v", c.name, gotCutoff, c.wantCutoff)
		} else if gotCutoff != nil && *gotCutoff != *c.wantCutoff {
			t.Errorf("%s: cutoff got %v want %v", c.name, *gotCutoff, *c.wantCutoff)
		}
	}
}

func TestGuessMIME(t *testing.T) {
	cases := map[string]string{
		"a.png":  "image/png",
		"b.jpg":  "image/jpeg",
		"c.JPEG": "image/jpeg",
		"d.webp": "image/webp",
		"e.tga":  "image/jpeg", // fallback
	}
	for name, want := range cases {
		if got := guessMIME(name); got != want {
			t.Errorf("guessMIME(%q): got %q want %q", name, got, want)
		}
	}
}

func floatp(v float32) *float32 { return &v }
