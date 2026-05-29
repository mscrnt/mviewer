package mview

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"github.com/qmuntal/gltf"
)

func solidColorPNG(w, h int, r, g, b, a uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Pix[(y*w+x)*4+0] = r
			img.Pix[(y*w+x)*4+1] = g
			img.Pix[(y*w+x)*4+2] = b
			img.Pix[(y*w+x)*4+3] = a
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestMergeAlpha_PacksLumaIntoRGBA(t *testing.T) {
	albedo := solidColorPNG(4, 4, 200, 100, 50, 255)
	alpha := solidColorPNG(4, 4, 64, 64, 64, 255) // luma=64 → alpha=64
	scn := []byte(`{
		"meshes":[],
		"materials":[{"name":"M","albedoTex":"a.png","alphaTex":"alpha.png"}]
	}`)
	archive := bytes.Join([][]byte{
		buildEntry("scene.json", "application/json", 0, scn),
		buildEntry("a.png", "image/png", 0, albedo),
		buildEntry("alpha.png", "image/png", 0, alpha),
	}, nil)

	glb, err := ConvertBytesToGLB(archive, WithMaterialChannelMerge(true))
	if err != nil {
		t.Fatalf("ConvertBytesToGLB: %v", err)
	}
	var doc gltf.Document
	if err := gltf.NewDecoder(bytes.NewReader(glb)).Decode(&doc); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if doc.Materials[0].PBRMetallicRoughness.BaseColorTexture == nil {
		t.Fatal("base color texture absent after merge")
	}
}

func TestMergeMetallicRoughness_PacksBothInputs(t *testing.T) {
	scn := []byte(`{
		"meshes":[],
		"materials":[{
			"name":"M","albedoTex":"a.png",
			"reflectivityTex":"refl.png","glossTex":"gloss.png"
		}]
	}`)
	a := bytes.Join([][]byte{
		buildEntry("scene.json", "application/json", 0, scn),
		buildEntry("a.png", "image/png", 0, solidColorPNG(2, 2, 200, 200, 200, 255)),
		buildEntry("refl.png", "image/png", 0, solidColorPNG(2, 2, 128, 128, 128, 255)),
		buildEntry("gloss.png", "image/png", 0, solidColorPNG(2, 2, 64, 64, 64, 255)),
	}, nil)
	glb, err := ConvertBytesToGLB(a, WithMaterialChannelMerge(true))
	if err != nil {
		t.Fatalf("ConvertBytesToGLB: %v", err)
	}
	var doc gltf.Document
	if err := gltf.NewDecoder(bytes.NewReader(glb)).Decode(&doc); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if doc.Materials[0].PBRMetallicRoughness.MetallicRoughnessTexture == nil {
		t.Fatal("metallic-roughness texture absent after merge")
	}
}

func TestMergeChannels_DisabledByDefault(t *testing.T) {
	scn := []byte(`{
		"meshes":[],
		"materials":[{"name":"M","albedoTex":"a.png","alphaTex":"alpha.png"}]
	}`)
	a := bytes.Join([][]byte{
		buildEntry("scene.json", "application/json", 0, scn),
		buildEntry("a.png", "image/png", 0, solidColorPNG(2, 2, 0, 0, 0, 255)),
		buildEntry("alpha.png", "image/png", 0, solidColorPNG(2, 2, 0, 0, 0, 255)),
	}, nil)
	glb, err := ConvertBytesToGLB(a) // no merge opt
	if err != nil {
		t.Fatalf("ConvertBytesToGLB: %v", err)
	}
	var doc gltf.Document
	if err := gltf.NewDecoder(bytes.NewReader(glb)).Decode(&doc); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	// Without merge there's no slot for the alpha texture in glTF
	// PBR — only the albedo lands as BaseColorTexture.
	if len(doc.Textures) != 1 {
		t.Errorf("expected 1 albedo texture (no merge, alpha has nowhere to go), got %d", len(doc.Textures))
	}
}
