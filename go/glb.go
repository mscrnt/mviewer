package mview

import (
	"bytes"
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
// Materials in this version are placeholder PBR opaque gray surfaces —
// texture extraction lands in a follow-up commit. Mesh geometry
// (positions, normals, tangents, primary + secondary UVs, vertex
// colors) round-trips faithfully.
//
// Animations and skinning are out of scope for this commit; see
// scene.AnimData and src/gltf/animated.rs for the upstream patterns to
// port next.
func ConvertToGLB(r io.Reader, w io.Writer) error {
	entries, err := ReadAll(r)
	if err != nil {
		return fmt.Errorf("mview: read archive: %w", err)
	}

	sceneEntry, ok := entries["scene.json"]
	if !ok {
		return fmt.Errorf("mview: archive missing scene.json")
	}
	scene, err := ParseScene(sceneEntry.Data)
	if err != nil {
		return fmt.Errorf("mview: parse scene.json: %w", err)
	}

	doc := gltf.NewDocument()
	doc.Asset.Generator = "github.com/mscrnt/mviewer/go"

	// Placeholder material table — one slot per scene material so
	// submesh references resolve. Real PBR + textures land in B-12f-5.
	materialIndex := make(map[string]int, len(scene.Materials))
	for _, m := range scene.Materials {
		idx := len(doc.Materials)
		doc.Materials = append(doc.Materials, &gltf.Material{
			Name: m.Name,
			PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
				BaseColorFactor: &[4]float64{0.8, 0.8, 0.8, 1},
				MetallicFactor:  gltf.Float(0),
				RoughnessFactor: gltf.Float(1),
			},
		})
		materialIndex[m.Name] = idx
	}

	rootNodes := make([]int, 0, len(scene.Meshes))
	for i := range scene.Meshes {
		meshDesc := &scene.Meshes[i]
		blob, ok := entries[meshDesc.File]
		if !ok {
			return fmt.Errorf("mview: mesh %q references missing entry %q", meshDesc.Name, meshDesc.File)
		}
		decoded, err := DecodeMesh(blob.Data, meshDesc)
		if err != nil {
			return fmt.Errorf("mview: decode %q: %w", meshDesc.Name, err)
		}
		meshIdx, err := appendMesh(doc, meshDesc, decoded, materialIndex)
		if err != nil {
			return fmt.Errorf("mview: build glTF mesh for %q: %w", meshDesc.Name, err)
		}

		node := &gltf.Node{Name: meshDesc.Name, Mesh: gltf.Index(meshIdx)}
		if meshDesc.Transform != nil {
			var m [16]float64
			for i, v := range *meshDesc.Transform {
				m[i] = float64(v)
			}
			node.Matrix = m
		}
		doc.Nodes = append(doc.Nodes, node)
		rootNodes = append(rootNodes, len(doc.Nodes)-1)
	}

	doc.Scenes[0].Nodes = rootNodes

	enc := gltf.NewEncoder(w)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("mview: encode glb: %w", err)
	}
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
) (int, error) {
	positionAcc := modeler.WritePosition(doc, decoded.Positions)
	normalAcc := modeler.WriteNormal(doc, decoded.Normals)
	uvAcc := modeler.WriteTextureCoord(doc, decoded.TexCoords)

	// Tangents in glTF are vec4 (xyz + bitangent sign). We have the
	// bitangent vector already, so we derive the handedness sign from
	// (tangent × normal) · bitangent — +1 if right-handed, -1 if not.
	tangents4 := make([][4]float32, len(decoded.Tangents))
	for i := range decoded.Tangents {
		tangents4[i] = packTangent(decoded.Tangents[i], decoded.Bitangents[i], decoded.Normals[i])
	}
	tangentAcc := modeler.WriteTangent(doc, tangents4)

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
func ConvertBytesToGLB(in []byte) ([]byte, error) {
	var out bytes.Buffer
	if err := ConvertToGLB(bytes.NewReader(in), &out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
