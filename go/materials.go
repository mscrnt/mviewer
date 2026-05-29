package mview

import (
	"bytes"
	"context"
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

// ensureFromBytes registers raw image bytes (typically a merged PNG
// from the channel-pack path) as a glTF texture. The name acts as
// both the cache key and the texture's glTF Name field. Returns the
// texture index, or (0, false) if the embed failed.
func (tb *textureBuilder) ensureFromBytes(name, mime string, data []byte) (int, bool) {
	if idx, ok := tb.cache[name]; ok {
		return idx, true
	}
	imageIdx, err := modeler.WriteImage(tb.doc, name, mime, bytes.NewReader(data))
	if err != nil {
		return 0, false
	}
	tb.doc.Textures = append(tb.doc.Textures, &gltf.Texture{
		Name:   name,
		Source: gltf.Index(imageIdx),
	})
	texIdx := len(tb.doc.Textures) - 1
	tb.cache[name] = texIdx
	return texIdx, true
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
// When Options.MergeMaterialChannels is true, this also runs the
// channel-merge path: albedo + alpha → RGBA base color, reflectivity +
// gloss → glTF metallic-roughness texture. The merge work happens in
// parallel across materials up to Options.Concurrency.
func buildMaterials(
	ctx context.Context,
	doc *gltf.Document,
	scene *Scene,
	entries map[string]*Entry,
	o *Options,
) (map[string]int, error) {
	merged := make([]*mergedTextures, len(scene.Materials))
	if o != nil && o.MergeMaterialChannels {
		if err := runParallel(ctx, len(scene.Materials), o.Concurrency, func(i int) error {
			m := &scene.Materials[i]
			merged[i] = mergeMaterialTextures(m, entries)
			return nil
		}); err != nil {
			return nil, err
		}
	}

	textures := newTextureBuilder(doc, entries)
	out := make(map[string]int, len(scene.Materials))

	for i := range scene.Materials {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m := &scene.Materials[i]
		pbr := &gltf.PBRMetallicRoughness{
			BaseColorFactor: &[4]float64{1, 1, 1, 1},
			MetallicFactor:  gltf.Float(1),
			RoughnessFactor: gltf.Float(1),
		}

		// Merged base color wins if available; otherwise use the raw
		// albedo as-is.
		if merged[i] != nil && merged[i].baseColorPNG != nil {
			if texIdx, ok := textures.ensureFromBytes(m.AlbedoTex+"+alpha.png", "image/png", merged[i].baseColorPNG); ok {
				pbr.BaseColorTexture = &gltf.TextureInfo{Index: texIdx}
			}
		} else if m.AlbedoTex != "" {
			if texIdx, ok := textures.ensureOptional(m.AlbedoTex); ok {
				pbr.BaseColorTexture = &gltf.TextureInfo{Index: texIdx}
			}
		}

		if merged[i] != nil && merged[i].metallicRoughnessPNG != nil {
			refl := ""
			if m.ReflectivityTex != nil {
				refl = *m.ReflectivityTex
			}
			if texIdx, ok := textures.ensureFromBytes(refl+"+mr.png", "image/png", merged[i].metallicRoughnessPNG); ok {
				pbr.MetallicRoughnessTexture = &gltf.TextureInfo{Index: texIdx}
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
