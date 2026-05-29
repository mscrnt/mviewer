package mview

import (
	"encoding/json"
	"strings"
	"testing"
)

// minimal happy-path: the smallest scene that still has a mesh and a
// material. Confirms required-field decoding works and that absent
// optional sections (lights, fog, sky, anim) leave their pointers nil.
func TestParseScene_Minimal(t *testing.T) {
	raw := []byte(`{
		"meshes":[{
			"name":"Mesh0","indexCount":12,"indexTypeSize":2,
			"wireCount":24,"vertexCount":8,"file":"mesh0.dat",
			"subMeshes":[{"material":"Mat0","firstIndex":0,"indexCount":12}]
		}],
		"materials":[{"name":"Mat0","albedoTex":"albedo.png"}]
	}`)
	got, err := ParseScene(raw)
	if err != nil {
		t.Fatalf("ParseScene: %v", err)
	}
	if len(got.Meshes) != 1 {
		t.Fatalf("expected 1 mesh, got %d", len(got.Meshes))
	}
	if got.Meshes[0].Name != "Mesh0" {
		t.Errorf("mesh name: got %q", got.Meshes[0].Name)
	}
	if got.Meshes[0].VertexCount != 8 {
		t.Errorf("vertex count: got %d", got.Meshes[0].VertexCount)
	}
	if len(got.Meshes[0].SubMeshes) != 1 || got.Meshes[0].SubMeshes[0].Material != "Mat0" {
		t.Errorf("submesh decode wrong: %+v", got.Meshes[0].SubMeshes)
	}
	if got.Lights != nil || got.Fog != nil || got.Sky != nil || got.AnimData != nil {
		t.Errorf("optional sections should be nil when absent")
	}
}

// Optional scalar fields must round-trip as pointers — caller has to
// be able to distinguish "absent" (nil) from "present with 0".
func TestParseScene_OptionalScalarsArePointers(t *testing.T) {
	raw := []byte(`{
		"metaData":{"title":"X","tbVersion":3080},
		"mainCamera":{"view":{"fov":45,"orbitRadius":0}},
		"meshes":[],"materials":[]
	}`)
	got, err := ParseScene(raw)
	if err != nil {
		t.Fatalf("ParseScene: %v", err)
	}
	if got.MetaData == nil || got.MetaData.Title == nil || *got.MetaData.Title != "X" {
		t.Fatalf("metaData.title decode: %+v", got.MetaData)
	}
	if got.MetaData.Author != nil {
		t.Errorf("metaData.author should be nil when absent, got %q", *got.MetaData.Author)
	}
	if got.MainCamera == nil || got.MainCamera.View == nil {
		t.Fatalf("mainCamera.view absent")
	}
	if got.MainCamera.View.FOV == nil || *got.MainCamera.View.FOV != 45 {
		t.Errorf("fov decode: %+v", got.MainCamera.View.FOV)
	}
	// Present-with-zero must NOT collapse to nil — that's the whole
	// point of the *T optional pattern.
	if got.MainCamera.View.OrbitRadius == nil || *got.MainCamera.View.OrbitRadius != 0 {
		t.Errorf("orbitRadius=0 must round-trip non-nil")
	}
}

// Boolish — the lossy-bool type Marmoset uses across material flags
// and animation toggles. Every form Toolbag has been observed to emit
// must coerce correctly.
func TestBoolish_AllForms(t *testing.T) {
	cases := []struct {
		json string
		want bool
	}{
		{`true`, true},
		{`false`, false},
		{`1`, true},
		{`0`, false},
		{`1.0`, true},
		{`0.0`, false},
		{`-1`, true},
		{`"true"`, true},
		{`"false"`, false},
		{`"True"`, true},
		{`"FALSE"`, false},
		{`"1"`, true},
		{`"0"`, false},
		{`" 1 "`, true},
		{`null`, false},
		{`""`, false},
		{`"banana"`, false},
	}
	for _, c := range cases {
		var b Boolish
		if err := json.Unmarshal([]byte(c.json), &b); err != nil {
			t.Errorf("Unmarshal %s: %v", c.json, err)
			continue
		}
		if bool(b) != c.want {
			t.Errorf("Boolish(%s) = %v, want %v", c.json, bool(b), c.want)
		}
	}
}

// A MaterialDesc with a mix of bool-shaped fields should decode all
// of them through the Boolish path.
func TestParseScene_BoolishOnMaterial(t *testing.T) {
	raw := []byte(`{
		"meshes":[],
		"materials":[{
			"name":"M","albedoTex":"a.png",
			"useSkin":1,"ggxSpecular":"true","unlitDiffuse":"0",
			"aniso":false,"microfiber":1.0,"refraction":"FALSE",
			"emissiveSecondaryUV":"True"
		}]
	}`)
	got, err := ParseScene(raw)
	if err != nil {
		t.Fatalf("ParseScene: %v", err)
	}
	m := got.Materials[0]
	want := map[string]bool{
		"useSkin": true, "ggxSpecular": true, "unlitDiffuse": false,
		"aniso": false, "microfiber": true, "refraction": false,
		"emissiveSecondaryUV": true,
	}
	for k, w := range want {
		var got bool
		switch k {
		case "useSkin":
			got = bool(m.UseSkin)
		case "ggxSpecular":
			got = bool(m.GgxSpecular)
		case "unlitDiffuse":
			got = bool(m.UnlitDiffuse)
		case "aniso":
			got = bool(m.Aniso)
		case "microfiber":
			got = bool(m.Microfiber)
		case "refraction":
			got = bool(m.Refraction)
		case "emissiveSecondaryUV":
			got = bool(m.EmissiveSecondaryUV)
		}
		if got != w {
			t.Errorf("%s: got %v want %v", k, got, w)
		}
	}
}

// Lossy UTF-8 fallback: a scene.json containing a stray invalid byte
// inside a string field must still parse via the second-chance path.
// (Marmoset Toolbag occasionally emits one of these from authors with
// non-Latin display names.)
func TestParseScene_LossyUTF8Fallback(t *testing.T) {
	raw := []byte(`{"metaData":{"title":"abc` + "\xff" + `def"},"meshes":[],"materials":[]}`)
	got, err := ParseScene(raw)
	if err != nil {
		t.Fatalf("ParseScene with bad UTF-8: %v", err)
	}
	if got.MetaData == nil || got.MetaData.Title == nil {
		t.Fatalf("metaData.title not parsed: %+v", got)
	}
	if !strings.Contains(*got.MetaData.Title, "abc") || !strings.Contains(*got.MetaData.Title, "def") {
		t.Errorf("title lost data on lossy retry: %q", *got.MetaData.Title)
	}
}

// Garbage in -> error out. Confirms we surface a non-nil error rather
// than silently returning an empty Scene.
func TestParseScene_HardFailure(t *testing.T) {
	if _, err := ParseScene([]byte(`{not json at all`)); err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}

// Extra/unknown fields must be tolerated — Toolbag adds new keys
// across versions, and we don't want a new release of Marmoset to
// break our decoder.
func TestParseScene_UnknownFieldsIgnored(t *testing.T) {
	raw := []byte(`{
		"meshes":[{
			"name":"Mesh0","indexCount":3,"indexTypeSize":2,
			"wireCount":6,"vertexCount":3,"file":"m.dat",
			"subMeshes":[{"material":"X","firstIndex":0,"indexCount":3}],
			"futureField":"please don't break me"
		}],
		"materials":[],
		"newTopLevelSectionInToolbag99":{"anything":42}
	}`)
	if _, err := ParseScene(raw); err != nil {
		t.Fatalf("strict-mode decode broke on unknown fields: %v", err)
	}
}
