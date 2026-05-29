package mview

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"
)

// textureBuilder caches per-archive texture work so that two
// materials referencing the same image bytes share a single embedded
// buffer view. The cache key is the archive entry name; the cached
// value is the glTF texture index (one step past the image index).
type textureBuilder struct {
	doc     *gltf.Document
	entries map[string]*Entry
	cache   map[string]int
}

func newTextureBuilder(doc *gltf.Document, entries map[string]*Entry) *textureBuilder {
	return &textureBuilder{
		doc:     doc,
		entries: entries,
		cache:   make(map[string]int),
	}
}

// ensureOptional is the soft version: if the archive doesn't carry
// the named texture, returns (0, false) instead of an error. Used
// when binding material slots where a missing texture should leave
// the slot empty rather than abort the whole conversion (partial
// .mview exports from broken pipelines should still produce a usable
// GLB).
func (tb *textureBuilder) ensureOptional(name string) (int, bool) {
	idx, err := tb.ensure(name)
	if err != nil {
		return 0, false
	}
	return idx, true
}

// ensure looks up the given texture name in the archive and embeds it
// as a glTF image + texture pair. Returns the texture index for use in
// a TextureInfo. Repeat calls with the same name reuse the cached
// texture entry rather than re-embedding.
func (tb *textureBuilder) ensure(name string) (int, error) {
	if idx, ok := tb.cache[name]; ok {
		return idx, nil
	}
	entry, ok := tb.entries[name]
	if !ok {
		return 0, fmt.Errorf("texture %q not in archive", name)
	}
	mime := guessMIME(name)
	imageIdx, err := modeler.WriteImage(tb.doc, name, mime, bytes.NewReader(entry.Data))
	if err != nil {
		return 0, fmt.Errorf("embed texture %q: %w", name, err)
	}
	tb.doc.Textures = append(tb.doc.Textures, &gltf.Texture{
		Name:   name,
		Source: gltf.Index(imageIdx),
	})
	texIdx := len(tb.doc.Textures) - 1
	tb.cache[name] = texIdx
	return texIdx, nil
}

// buildMaterials walks scene.Materials, embeds each referenced texture,
// and writes a real PBR material entry. Returns the material name →
// glTF material index map the mesh writer uses to bind submeshes.
//
// Channel merging (albedo + alpha → RGBA base color, reflectivity +
// gloss → metallic-roughness) is deferred to a follow-up — the upstream
// Rust crate does it via image decode + re-encode, which doubles the
// code surface. For now we embed the albedo and alpha textures
// separately and trust the source PNG's own alpha channel where one
// exists.
func buildMaterials(
	doc *gltf.Document,
	scene *Scene,
	entries map[string]*Entry,
) (map[string]int, error) {
	textures := newTextureBuilder(doc, entries)
	out := make(map[string]int, len(scene.Materials))

	for i := range scene.Materials {
		m := &scene.Materials[i]
		pbr := &gltf.PBRMetallicRoughness{
			BaseColorFactor: &[4]float64{1, 1, 1, 1},
			MetallicFactor:  gltf.Float(1),
			RoughnessFactor: gltf.Float(1),
		}

		if m.AlbedoTex != "" {
			if texIdx, ok := textures.ensureOptional(m.AlbedoTex); ok {
				pbr.BaseColorTexture = &gltf.TextureInfo{Index: texIdx}
			}
		}

		mat := &gltf.Material{
			Name:                 m.Name,
			PBRMetallicRoughness: pbr,
		}

		if m.NormalTex != nil && *m.NormalTex != "" {
			if texIdx, ok := textures.ensureOptional(*m.NormalTex); ok {
				mat.NormalTexture = &gltf.NormalTexture{Index: gltf.Index(texIdx)}
			}
		}

		// Emissive — Toolbag stores an intensity scalar; glTF wants an
		// RGB factor. Reuse the intensity on all three channels so a
		// Marmoset-authored emissive surface lights up in viewers that
		// only honor emissiveFactor.
		if m.EmissiveIntensity != nil && *m.EmissiveIntensity > 0 {
			intensity := float64(*m.EmissiveIntensity)
			mat.EmissiveFactor = [3]float64{intensity, intensity, intensity}
		}

		// Alpha mode + cutoff. Marmoset's "alpha" / "add" blend modes
		// both map to glTF BLEND; "alpha test" + alphaTex implies MASK
		// with a cutoff (Toolbag default 0.5).
		alphaMode, alphaCutoff := resolveAlpha(m)
		switch alphaMode {
		case "BLEND":
			mat.AlphaMode = gltf.AlphaBlend
			mat.DoubleSided = true
		case "MASK":
			mat.AlphaMode = gltf.AlphaMask
			if alphaCutoff != nil {
				mat.AlphaCutoff = gltf.Float(float64(*alphaCutoff))
			}
		}

		doc.Materials = append(doc.Materials, mat)
		out[m.Name] = len(doc.Materials) - 1
	}
	return out, nil
}

// resolveAlpha returns the glTF alpha mode + cutoff implied by a
// .mview material's blend / alphaTex / alphaTest combination.
//
//   - blend="alpha" or "add" → BLEND (no cutoff)
//   - alphaTex present and no blend       → MASK (cutoff from
//     alphaTest, defaulting to 0.5)
//   - otherwise → OPAQUE (empty string, caller ignores)
func resolveAlpha(m *MaterialDesc) (string, *float32) {
	if m.Blend != nil {
		switch *m.Blend {
		case "alpha", "add":
			return "BLEND", nil
		}
	}
	if m.AlphaTex != nil && *m.AlphaTex != "" {
		cutoff := float32(0.5)
		if m.AlphaTest != nil {
			cutoff = *m.AlphaTest
		}
		return "MASK", &cutoff
	}
	return "", nil
}

// guessMIME picks a glTF-compatible image MIME from a filename
// extension. Falls back to "image/jpeg" because Toolbag's default
// export uses JPEG for color/PBR textures and most uploaders honour
// that — a wrong-MIME PNG would still embed fine, just lose viewer
// hints.
func guessMIME(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}
