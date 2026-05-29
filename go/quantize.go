package mview

import (
	"encoding/binary"
	"math"

	"github.com/qmuntal/gltf"
)

// writeQuantizedNormal appends a NORMAL accessor that stores each
// component as a signed normalized int16 (glTF KHR_mesh_quantization).
// Halves the per-vertex normal cost compared to float32 vec3 with no
// perceptible precision loss — Marmoset's source format already
// stored them at 16-bit resolution.
func writeQuantizedNormal(doc *gltf.Document, normals [][3]float32) int {
	buf := encodeI16Vec3Normalized(normals)
	return appendVec3Int16Accessor(doc, buf, len(normals), gltf.NORMAL)
}

// writeQuantizedTangent appends a TANGENT accessor as signed
// normalized int16 vec4 (xyz tangent + w handedness sign).
func writeQuantizedTangent(doc *gltf.Document, tangents, bitangents, normals [][3]float32) int {
	packed := make([][4]float32, len(tangents))
	for i := range tangents {
		packed[i] = packTangent(tangents[i], bitangents[i], normals[i])
	}
	buf := encodeI16Vec4Normalized(packed)
	return appendVec4Int16Accessor(doc, buf, len(packed), gltf.TANGENT)
}

func encodeI16Vec3Normalized(in [][3]float32) []byte {
	out := make([]byte, len(in)*6)
	for i, v := range in {
		for j := 0; j < 3; j++ {
			binary.LittleEndian.PutUint16(out[i*6+j*2:], uint16(floatToI16Normalized(v[j])))
		}
	}
	return out
}

func encodeI16Vec4Normalized(in [][4]float32) []byte {
	out := make([]byte, len(in)*8)
	for i, v := range in {
		for j := 0; j < 4; j++ {
			binary.LittleEndian.PutUint16(out[i*8+j*2:], uint16(floatToI16Normalized(v[j])))
		}
	}
	return out
}

// floatToI16Normalized maps [-1, 1] → [-32767, 32767] with the
// rounding rule from the KHR_mesh_quantization spec (clamp + nearest
// even). -32768 is reserved.
func floatToI16Normalized(v float32) int16 {
	if v > 1 {
		v = 1
	}
	if v < -1 {
		v = -1
	}
	r := math.Round(float64(v) * 32767)
	return int16(r)
}

func appendVec3Int16Accessor(doc *gltf.Document, raw []byte, count int, _ string) int {
	return appendQuantizedAccessor(doc, raw, count, gltf.AccessorVec3, 6)
}

func appendVec4Int16Accessor(doc *gltf.Document, raw []byte, count int, _ string) int {
	return appendQuantizedAccessor(doc, raw, count, gltf.AccessorVec4, 8)
}

func appendQuantizedAccessor(doc *gltf.Document, raw []byte, count int, t gltf.AccessorType, stride int) int {
	bufIdx := ensureMainBuffer(doc)
	byteOffset := len(doc.Buffers[bufIdx].Data)
	doc.Buffers[bufIdx].Data = append(doc.Buffers[bufIdx].Data, raw...)
	doc.Buffers[bufIdx].ByteLength = len(doc.Buffers[bufIdx].Data)

	bv := &gltf.BufferView{
		Buffer:     bufIdx,
		ByteOffset: byteOffset,
		ByteLength: len(raw),
		ByteStride: stride,
		Target:     gltf.TargetArrayBuffer,
	}
	doc.BufferViews = append(doc.BufferViews, bv)
	bvIdx := len(doc.BufferViews) - 1

	acc := &gltf.Accessor{
		BufferView:    gltf.Index(bvIdx),
		ComponentType: gltf.ComponentShort,
		Normalized:    true,
		Count:         count,
		Type:          t,
	}
	doc.Accessors = append(doc.Accessors, acc)
	return len(doc.Accessors) - 1
}

// ensureMainBuffer returns the index of the doc's primary buffer,
// allocating it on first use. modeler.* helpers already create one
// when they run, so this only kicks in when our quantized writers
// are the first to touch buffers — keeps the layout consistent.
func ensureMainBuffer(doc *gltf.Document) int {
	if len(doc.Buffers) == 0 {
		doc.Buffers = append(doc.Buffers, &gltf.Buffer{})
	}
	return 0
}

const khrMeshQuantization = "KHR_mesh_quantization"

func markQuantizationExtension(doc *gltf.Document) {
	for _, e := range doc.ExtensionsUsed {
		if e == khrMeshQuantization {
			return
		}
	}
	doc.ExtensionsUsed = append(doc.ExtensionsUsed, khrMeshQuantization)
	doc.ExtensionsRequired = append(doc.ExtensionsRequired, khrMeshQuantization)
}
