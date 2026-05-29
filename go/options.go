package mview

import "runtime"

// Options bundles the tunables a caller can pass into ConvertToGLB,
// Validate, ReadAll, and the *FS variants. Built via functional
// options (WithMaxTotalSize, WithConcurrency, etc) — direct
// instantiation is fine too if you want every field at once.
//
// The zero value is the conservative default: no aggregate-size cap,
// no channel merging, no mesh quantization, single-goroutine decode.
type Options struct {
	// MaxTotalSize caps the aggregate decompressed payload across
	// every entry in an archive. Zero = no cap.
	//
	// MaxEntrySize already bounds individual entries, but a
	// crafted archive with N legitimate-looking entries can still
	// demand N × MaxEntrySize bytes of memory. MaxTotalSize closes
	// that hole for any caller exposing the decoder to user uploads.
	MaxTotalSize int64

	// Concurrency is the goroutine fan-out for mesh + texture work.
	// Zero defaults to runtime.GOMAXPROCS(0). Set to 1 to force
	// sequential processing (useful for repeatable benchmarks).
	Concurrency int

	// MergeMaterialChannels triggers the image-merge path that
	// packs:
	//   - albedo + alpha       → RGBA BaseColorTexture
	//   - reflectivity + gloss → glTF metallic-roughness (B = metal,
	//                              G = 1 - gloss)
	// Doubles peak memory per material but produces a single PBR
	// glTF render that matches what Marmoset shows.
	MergeMaterialChannels bool

	// QuantizedMesh keeps normals + tangents as int16-encoded vec3
	// in the glTF buffer (the format they were already stored in
	// inside .mview) and declares KHR_mesh_quantization on the
	// document. Saves ~50% of the normal/tangent buffer size for
	// viewers that support the extension.
	QuantizedMesh bool
}

// Option is the functional-options helper type used by the public
// entry points. Each option mutates an *Options in place.
type Option func(*Options)

// WithMaxTotalSize caps the aggregate decompressed archive size in
// bytes. Useful when accepting user uploads: zero means unbounded.
func WithMaxTotalSize(n int64) Option {
	return func(o *Options) { o.MaxTotalSize = n }
}

// WithConcurrency sets the goroutine fan-out for mesh + texture
// decode. Zero or negative falls back to GOMAXPROCS.
func WithConcurrency(n int) Option {
	return func(o *Options) { o.Concurrency = n }
}

// WithMaterialChannelMerge enables RGBA + metallic-roughness texture
// packing (off by default — costs extra memory + CPU).
func WithMaterialChannelMerge(b bool) Option {
	return func(o *Options) { o.MergeMaterialChannels = b }
}

// WithQuantizedMesh keeps normals + tangents in their packed int16
// form and declares KHR_mesh_quantization on the glTF document.
func WithQuantizedMesh(b bool) Option {
	return func(o *Options) { o.QuantizedMesh = b }
}

func resolveOptions(opts []Option) Options {
	var o Options
	for _, f := range opts {
		if f != nil {
			f(&o)
		}
	}
	if o.Concurrency <= 0 {
		o.Concurrency = runtime.GOMAXPROCS(0)
	}
	return o
}
