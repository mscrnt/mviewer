package mview

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"
)

// ConvertToGLB reads a .mview archive from r and writes a glTF 2.0
// binary (GLB) to w. The whole archive is buffered in memory — .mview
// files top out at a few hundred MB in practice (capped per-entry by
// MaxEntrySize) so streaming through is fine on any worker.
//
// Options control optional behaviour: total-size cap, concurrency,
// channel merging, and KHR_mesh_quantization. See ConvertToGLBContext
// for the cancellation-aware variant.
func ConvertToGLB(r io.Reader, w io.Writer, opts ...Option) error {
	return ConvertToGLBContext(context.Background(), r, w, opts...)
}

// ConvertToGLBContext is the context-aware variant. ctx cancellation
// stops the conversion at the next safe checkpoint (entry boundary,
// per-mesh, per-material). Use this for any path where the caller has
// a deadline or might want to abort mid-flight (HTTP handlers,
// background workers).
func ConvertToGLBContext(ctx context.Context, r io.Reader, w io.Writer, opts ...Option) error {
	o := resolveOptions(opts)

	entries, err := ReadAllContext(ctx, r, opts...)
	if err != nil {
		return fmt.Errorf("mview: read archive: %w", err)
	}

	sceneEntry, ok := entries["scene.json"]
	if !ok {
		return fmt.Errorf("mview: archive missing scene.json: %w", ErrMissingEntry)
	}
	scene, err := ParseScene(sceneEntry.Data)
	if err != nil {
		return fmt.Errorf("mview: parse scene.json: %w", err)
	}

	doc := gltf.NewDocument()
	doc.Asset.Generator = "github.com/mscrnt/mviewer/go"

	materialIndex, err := buildMaterials(ctx, doc, scene, entries, &o)
	if err != nil {
		return fmt.Errorf("mview: build materials: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := appendAllMeshes(ctx, doc, scene, entries, materialIndex, &o); err != nil {
		return err
	}

	// Animations are best-effort: an export with malformed AnimData
	// shouldn't fail the whole convert. The parser already enforces
	// archive-side invariants, so a real failure here means our
	// emitter needs a fix, not the source file.
	if scene.AnimData != nil {
		set, err := ParseAnimations(entries, scene)
		if err == nil && set != nil {
			_ = appendAnimations(doc, set)
		}
	}

	enc := gltf.NewEncoder(w)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("mview: encode glb: %w", err)
	}
	return nil
}

// appendAllMeshes decodes every scene mesh and registers them in the
// glTF doc. Mesh blobs decode in parallel up to Options.Concurrency,
// then serially flush into the doc (qmuntal/gltf's doc isn't safe to
// mutate concurrently).
func appendAllMeshes(
	ctx context.Context,
	doc *gltf.Document,
	scene *Scene,
	entries map[string]*Entry,
	materialIndex map[string]int,
	o *Options,
) error {
	type result struct {
		desc    *MeshDesc
		decoded *DecodedMesh
		err     error
	}
	n := len(scene.Meshes)
	results := make([]result, n)

	// Parallel decode. Tiny meshes don't benefit much, but cave-grade
	// 600k+ vertex blobs spread well across cores.
	if err := runParallel(ctx, n, o.Concurrency, func(i int) error {
		desc := &scene.Meshes[i]
		blob, ok := entries[desc.File]
		if !ok {
			results[i] = result{desc: desc, err: fmt.Errorf("mesh %q references missing entry %q: %w", desc.Name, desc.File, ErrMissingEntry)}
			return nil
		}
		decoded, err := DecodeMesh(blob.Data, desc)
		results[i] = result{desc: desc, decoded: decoded, err: err}
		return nil
	}); err != nil {
		return err
	}

	rootNodes := make([]int, 0, n)
	for i := range results {
		r := &results[i]
		if r.err != nil {
			return fmt.Errorf("mview: decode %q: %w", r.desc.Name, r.err)
		}
		meshIdx, err := appendMesh(doc, r.desc, r.decoded, materialIndex, o)
		if err != nil {
			return fmt.Errorf("mview: build glTF mesh for %q: %w", r.desc.Name, err)
		}

		node := &gltf.Node{Name: r.desc.Name, Mesh: gltf.Index(meshIdx)}
		if r.desc.Transform != nil {
			var m [16]float64
			for j, v := range *r.desc.Transform {
				m[j] = float64(v)
			}
			node.Matrix = m
		}
		doc.Nodes = append(doc.Nodes, node)
		rootNodes = append(rootNodes, len(doc.Nodes)-1)
	}
	doc.Scenes[0].Nodes = rootNodes
	return nil
}

// appendMesh registers one .mview mesh (with its submeshes) as a glTF
// mesh entry and returns the new mesh's index in doc.Meshes.
//
// Attribute sharing: positions / normals / tangents / UVs / colors are
// written once and pointed at by every primitive — submeshes only
// differ in their index slice + material binding.
func appendMesh(
	doc *gltf.Document,
	desc *MeshDesc,
	decoded *DecodedMesh,
	materialIndex map[string]int,
	o *Options,
) (int, error) {
	positionAcc := modeler.WritePosition(doc, decoded.Positions)

	var normalAcc, tangentAcc int
	if o != nil && o.QuantizedMesh {
		normalAcc = writeQuantizedNormal(doc, decoded.Normals)
		tangentAcc = writeQuantizedTangent(doc, decoded.Tangents, decoded.Bitangents, decoded.Normals)
		markQuantizationExtension(doc)
	} else {
		normalAcc = modeler.WriteNormal(doc, decoded.Normals)
		tangents4 := make([][4]float32, len(decoded.Tangents))
		for i := range decoded.Tangents {
			tangents4[i] = packTangent(decoded.Tangents[i], decoded.Bitangents[i], decoded.Normals[i])
		}
		tangentAcc = modeler.WriteTangent(doc, tangents4)
	}

	uvAcc := modeler.WriteTextureCoord(doc, decoded.TexCoords)

	attrs := gltf.PrimitiveAttributes{
		gltf.POSITION:   positionAcc,
		gltf.NORMAL:     normalAcc,
		gltf.TEXCOORD_0: uvAcc,
		gltf.TANGENT:    tangentAcc,
	}
	if decoded.SecondaryTexCoords != nil {
		attrs[gltf.TEXCOORD_1] = modeler.WriteTextureCoord(doc, decoded.SecondaryTexCoords)
	}
	if decoded.Colors != nil {
		// glTF accepts vec4 floats directly for COLOR_0.
		attrs[gltf.COLOR_0] = modeler.WriteColor(doc, decoded.Colors)
	}

	primitives := make([]*gltf.Primitive, 0, len(desc.SubMeshes))
	for _, sm := range desc.SubMeshes {
		if sm.FirstIndex < 0 || sm.FirstIndex+sm.IndexCount > len(decoded.Indices) {
			return 0, fmt.Errorf(
				"submesh %q indices out of range (first=%d count=%d total=%d)",
				sm.Material, sm.FirstIndex, sm.IndexCount, len(decoded.Indices),
			)
		}
		slice := decoded.Indices[sm.FirstIndex : sm.FirstIndex+sm.IndexCount]
		indexAcc := modeler.WriteIndices(doc, slice)
		prim := &gltf.Primitive{
			Mode:       gltf.PrimitiveTriangles,
			Indices:    gltf.Index(indexAcc),
			Attributes: attrs,
		}
		if matIdx, ok := materialIndex[sm.Material]; ok {
			prim.Material = gltf.Index(matIdx)
		}
		primitives = append(primitives, prim)
	}

	doc.Meshes = append(doc.Meshes, &gltf.Mesh{
		Name:       desc.Name,
		Primitives: primitives,
	})
	return len(doc.Meshes) - 1, nil
}

// packTangent recovers glTF's vec4 tangent encoding (xyz + handedness
// sign in w) from the .mview triplet (tangent, bitangent, normal).
//
// glTF expects the bitangent to be reconstructable as cross(N, T) * w
// where w ∈ {-1, +1}. Compare the source bitangent against +cross(N,T)
// to pick the sign.
func packTangent(t, b, n [3]float32) [4]float32 {
	cx := n[1]*t[2] - n[2]*t[1]
	cy := n[2]*t[0] - n[0]*t[2]
	cz := n[0]*t[1] - n[1]*t[0]
	dot := cx*b[0] + cy*b[1] + cz*b[2]
	w := float32(1)
	if dot < 0 {
		w = -1
	}
	return [4]float32{t[0], t[1], t[2], w}
}

// ConvertBytesToGLB is a convenience wrapper that takes a fully
// buffered .mview blob and returns the GLB bytes. Cheap for callers
// that already have the archive in memory (HTTP request bodies etc).
func ConvertBytesToGLB(in []byte, opts ...Option) ([]byte, error) {
	return ConvertBytesToGLBContext(context.Background(), in, opts...)
}

// ConvertBytesToGLBContext is the context-aware variant.
func ConvertBytesToGLBContext(ctx context.Context, in []byte, opts ...Option) ([]byte, error) {
	var out bytes.Buffer
	if err := ConvertToGLBContext(ctx, bytes.NewReader(in), &out, opts...); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
