package mview

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
)

// mergedTextures holds the channel-packed PNG bytes a material wants
// to use instead of the raw archive textures. nil entries mean "no
// merge happened — fall back to raw".
type mergedTextures struct {
	// baseColorPNG is RGBA: RGB from albedoTex, A from alphaTex (if
	// present, otherwise nil).
	baseColorPNG []byte

	// metallicRoughnessPNG is glTF metallic-roughness layout:
	// G = 1 - gloss (roughness), B = reflectivity intensity (metal).
	// Empty when either reflectivityTex or glossTex is missing.
	metallicRoughnessPNG []byte
}

// mergeMaterialTextures performs the (optional) image-merge passes
// for one material. Decodes the source PNG/JPEGs, repacks the
// channels, re-encodes as PNG. All paths soft-fail: a decode error
// on any input leaves the corresponding output nil, and the caller
// falls back to using the raw textures.
func mergeMaterialTextures(m *MaterialDesc, entries map[string]*Entry) *mergedTextures {
	out := &mergedTextures{}

	// Base color: albedo + alpha → RGBA.
	if m.AlphaTex != nil && *m.AlphaTex != "" {
		albedoImg := decodeArchiveImage(entries, m.AlbedoTex)
		alphaImg := decodeArchiveImage(entries, *m.AlphaTex)
		if albedoImg != nil && alphaImg != nil {
			rgba := mergeAlpha(albedoImg, alphaImg)
			out.baseColorPNG = encodePNG(rgba)
		}
	}

	// Metallic-roughness: reflectivity + gloss → packed PNG.
	if m.ReflectivityTex != nil && *m.ReflectivityTex != "" &&
		m.GlossTex != nil && *m.GlossTex != "" {
		reflImg := decodeArchiveImage(entries, *m.ReflectivityTex)
		glossImg := decodeArchiveImage(entries, *m.GlossTex)
		if reflImg != nil && glossImg != nil {
			mr := mergeMetallicRoughness(reflImg, glossImg)
			out.metallicRoughnessPNG = encodePNG(mr)
		}
	}

	return out
}

func decodeArchiveImage(entries map[string]*Entry, name string) image.Image {
	if name == "" {
		return nil
	}
	e, ok := entries[name]
	if !ok {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(e.Data))
	if err != nil {
		return nil
	}
	return img
}

// mergeAlpha overlays the luma of alphaImg into albedoImg's alpha
// channel. Resamples by nearest-neighbour if the two source images
// differ in size — Marmoset usually authors them at matching
// resolutions but we shouldn't crash if a user packs mismatched ones.
func mergeAlpha(albedoImg, alphaImg image.Image) *image.RGBA {
	b := albedoImg.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	ab := alphaImg.Bounds()
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			cr, cg, cb, _ := albedoImg.At(b.Min.X+x, b.Min.Y+y).RGBA()
			// Sample alpha by relative position so a 1024² alpha
			// pairs with a 2048² albedo without aborting.
			ax := ab.Min.X + (x * ab.Dx() / b.Dx())
			ay := ab.Min.Y + (y * ab.Dy() / b.Dy())
			ar, ag, abl, _ := alphaImg.At(ax, ay).RGBA()
			lum := (uint32(ar) + uint32(ag) + uint32(abl)) / 3
			out.SetRGBA(x, y, color.RGBA{
				R: uint8(cr >> 8),
				G: uint8(cg >> 8),
				B: uint8(cb >> 8),
				A: uint8(lum >> 8),
			})
		}
	}
	return out
}

// mergeMetallicRoughness packs reflectivity + gloss into a glTF
// metallic-roughness texture:
//   - R: unused (zero)
//   - G: roughness = 1 - gloss luma
//   - B: metallic  = reflectivity luma
//   - A: 255
func mergeMetallicRoughness(reflImg, glossImg image.Image) *image.RGBA {
	b := reflImg.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	gb := glossImg.Bounds()
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			rr, rg, rb, _ := reflImg.At(b.Min.X+x, b.Min.Y+y).RGBA()
			metal := (uint32(rr) + uint32(rg) + uint32(rb)) / 3

			gx := gb.Min.X + (x * gb.Dx() / b.Dx())
			gy := gb.Min.Y + (y * gb.Dy() / b.Dy())
			gr, gg, gbb, _ := glossImg.At(gx, gy).RGBA()
			gloss := (uint32(gr) + uint32(gg) + uint32(gbb)) / 3
			rough := 65535 - gloss

			out.SetRGBA(x, y, color.RGBA{
				R: 0,
				G: uint8(rough >> 8),
				B: uint8(metal >> 8),
				A: 255,
			})
		}
	}
	return out
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

// init wires image/jpeg + image/png decoders into the stdlib
// image.Decode registry. Without this, decodeArchiveImage would
// only handle PNG even when an entry is JPEG (Toolbag's default).
func init() {
	// referencing the package var forces import side effects without
	// the underscore-import idiom (which golangci-lint complains
	// about in some configs).
	_ = jpeg.DefaultQuality
}
