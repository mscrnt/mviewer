package mview

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// DecodedMesh is the unpacked binary mesh blob — one slice per
// vertex stream + the merged index buffer. Secondary UVs and per-
// vertex colors are nil-when-absent so callers can skip them in
// downstream encoders without re-checking flags from the MeshDesc.
type DecodedMesh struct {
	Positions  [][3]float32
	Normals    [][3]float32
	Tangents   [][3]float32
	Bitangents [][3]float32
	TexCoords  [][2]float32
	// Secondary UVs are present when MeshDesc.SecondaryTexCoord is
	// set and non-zero. Nil otherwise.
	SecondaryTexCoords [][2]float32
	// Per-vertex colors are present when MeshDesc.VertexColor is
	// set and non-zero. Nil otherwise. RGBA in 0..1.
	Colors  [][4]float32
	Indices []uint32
}

// DecodeMesh unpacks a `mesh*.dat` blob into the typed vertex
// streams. Layout, in order:
//
//   - IndexCount × (u16 or u32 LE based on MeshDesc.IndexTypeSize)
//   - WireCount × IndexTypeSize bytes (wireframe index buffer, skipped)
//   - VertexCount × {
//   - position: 3 × f32
//   - primary UV: 2 × f32 (V is negated to match glTF convention)
//   - optional secondary UV: 2 × f32 (V is negated)
//   - tangent / bitangent / normal: each 2 × u16, packed unit vec
//   - optional RGBA u8 vertex color
//     }
//
// Base stride (no optional streams) is 32 bytes/vertex.
// Reference: ../src/mesh.rs::decode_mesh.
func DecodeMesh(blob []byte, mesh *MeshDesc) (*DecodedMesh, error) {
	if mesh.IndexTypeSize != 2 && mesh.IndexTypeSize != 4 {
		return nil, fmt.Errorf("mesh %q: unsupported indexTypeSize %d", mesh.Name, mesh.IndexTypeSize)
	}

	r := &cursor{buf: blob}

	indices := make([]uint32, mesh.IndexCount)
	for i := 0; i < mesh.IndexCount; i++ {
		switch mesh.IndexTypeSize {
		case 2:
			v, err := r.u16()
			if err != nil {
				return nil, fmt.Errorf("mesh %q index[%d]: %w", mesh.Name, i, err)
			}
			indices[i] = uint32(v)
		case 4:
			v, err := r.u32()
			if err != nil {
				return nil, fmt.Errorf("mesh %q index[%d]: %w", mesh.Name, i, err)
			}
			indices[i] = v
		}
	}

	wireBytes := mesh.WireCount * mesh.IndexTypeSize
	if err := r.skip(wireBytes); err != nil {
		return nil, fmt.Errorf("mesh %q wireframe skip: %w", mesh.Name, err)
	}

	hasSecondaryUV := mesh.SecondaryTexCoord != nil && *mesh.SecondaryTexCoord > 0
	hasVertexColor := mesh.VertexColor != nil && *mesh.VertexColor > 0

	out := &DecodedMesh{
		Positions:  make([][3]float32, mesh.VertexCount),
		Normals:    make([][3]float32, mesh.VertexCount),
		Tangents:   make([][3]float32, mesh.VertexCount),
		Bitangents: make([][3]float32, mesh.VertexCount),
		TexCoords:  make([][2]float32, mesh.VertexCount),
		Indices:    indices,
	}
	if hasSecondaryUV {
		out.SecondaryTexCoords = make([][2]float32, mesh.VertexCount)
	}
	if hasVertexColor {
		out.Colors = make([][4]float32, mesh.VertexCount)
	}

	for i := 0; i < mesh.VertexCount; i++ {
		px, err := r.f32()
		if err != nil {
			return nil, fmt.Errorf("mesh %q vertex[%d] position: %w", mesh.Name, i, err)
		}
		py, _ := r.f32()
		pz, _ := r.f32()
		out.Positions[i] = [3]float32{px, py, pz}

		u, err := r.f32()
		if err != nil {
			return nil, fmt.Errorf("mesh %q vertex[%d] uv: %w", mesh.Name, i, err)
		}
		v, _ := r.f32()
		out.TexCoords[i] = [2]float32{u, -v}

		if hasSecondaryUV {
			u2, err := r.f32()
			if err != nil {
				return nil, fmt.Errorf("mesh %q vertex[%d] uv2: %w", mesh.Name, i, err)
			}
			v2, _ := r.f32()
			out.SecondaryTexCoords[i] = [2]float32{u2, -v2}
		}

		tx, err := r.u16()
		if err != nil {
			return nil, fmt.Errorf("mesh %q vertex[%d] tangent: %w", mesh.Name, i, err)
		}
		ty, _ := r.u16()
		out.Tangents[i] = unpackUnitVector(tx, ty)

		bx, _ := r.u16()
		by, _ := r.u16()
		out.Bitangents[i] = unpackUnitVector(bx, by)

		nx, _ := r.u16()
		ny, _ := r.u16()
		out.Normals[i] = unpackUnitVector(nx, ny)

		if hasVertexColor {
			cr, err := r.u8()
			if err != nil {
				return nil, fmt.Errorf("mesh %q vertex[%d] color: %w", mesh.Name, i, err)
			}
			cg, _ := r.u8()
			cb, _ := r.u8()
			ca, _ := r.u8()
			out.Colors[i] = [4]float32{
				float32(cr) / 255.0,
				float32(cg) / 255.0,
				float32(cb) / 255.0,
				float32(ca) / 255.0,
			}
		}
	}

	return out, nil
}

// unpackUnitVector recovers a 3-component unit vector from two
// little-endian uint16 values. Marmoset's encoder maps each component
// to [-1, 1] via the magic 32767.4 scale, and stuffs the Z sign bit
// into the high bit of the Y channel — saving 16 bits per vertex
// compared to storing Z explicitly.
//
//	x  = rawX/32767.4 * 2 - 1
//	y' = rawY with high bit cleared
//	zNeg = (rawY high bit set)
//	y  = y'/32767.4 * 2 - 1
//	z  = sqrt(max(0, 1 - x² - y²))  (negate if zNeg)
//
// Reference: ../src/mesh.rs::unpack_unit_vector.
func unpackUnitVector(rawX, rawY uint16) [3]float32 {
	y := int32(rawY)
	zNegative := y >= 32768
	if zNegative {
		y -= 32768
	}

	x := float32(rawX)/32767.4*2.0 - 1.0
	yf := float32(y)/32767.4*2.0 - 1.0
	zSq := 1.0 - (x*x + yf*yf)
	if zSq < 0 {
		zSq = 0
	}
	z := float32(math.Sqrt(float64(zSq)))
	if zNegative {
		z = -z
	}
	return [3]float32{x, yf, z}
}

// cursor is a thin little-endian byte reader. Lighter than bytes.Reader
// + binary.Read for the inner hot loop of the vertex decoder.
type cursor struct {
	buf []byte
	pos int
}

func (c *cursor) u8() (uint8, error) {
	if c.pos+1 > len(c.buf) {
		return 0, io.ErrUnexpectedEOF
	}
	v := c.buf[c.pos]
	c.pos++
	return v, nil
}

func (c *cursor) u16() (uint16, error) {
	if c.pos+2 > len(c.buf) {
		return 0, io.ErrUnexpectedEOF
	}
	v := binary.LittleEndian.Uint16(c.buf[c.pos:])
	c.pos += 2
	return v, nil
}

func (c *cursor) u32() (uint32, error) {
	if c.pos+4 > len(c.buf) {
		return 0, io.ErrUnexpectedEOF
	}
	v := binary.LittleEndian.Uint32(c.buf[c.pos:])
	c.pos += 4
	return v, nil
}

func (c *cursor) f32() (float32, error) {
	bits, err := c.u32()
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(bits), nil
}

func (c *cursor) skip(n int) error {
	if c.pos+n > len(c.buf) {
		return io.ErrUnexpectedEOF
	}
	c.pos += n
	return nil
}
