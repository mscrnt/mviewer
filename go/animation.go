package mview

import (
	"encoding/binary"
	"fmt"
	"math"
)

// ParsedAnimationSet is the resolved animation payload of a scene:
// matrix table + skinning rigs + per-clip keyframes. Built by
// ParseAnimations() once the archive + scene are in memory.
//
// The shape mirrors the Rust crate's ParsedAnimationSet so the
// upstream's binary fixtures (vivfox, natnephilim) act as a ground
// truth — see TestParseAnimations_VivFox / _NatNephilim.
type ParsedAnimationSet struct {
	SceneScale    float32
	NumMatrices   int
	MatrixTable   MatrixTable
	SkinningRigs  []SkinningRig
	Animations    []ParsedAnimation
}

// MatrixTable is the flat 4×4 lookup that animations + skinning rigs
// reference by index. Stored in the archive as `MatTable.bin`.
type MatrixTable struct {
	Matrices [][16]float32
}

// SkinningRig — one resolved rig (one mesh's bind pose + per-vertex
// influence map). Read from a `SkinRig*.dat` blob; the matrix
// references inside have already been resolved against MatrixTable.
type SkinningRig struct {
	SourceFile             string
	ExpectedNumClusters    int
	ExpectedNumVertices    int
	NumClusterLinks        int
	OriginalObjectIndex    int
	IsRigidSkin            bool
	TangentMethod          uint32
	Clusters               []SkinningCluster
	LinkMapCount           []uint8
	LinkMapClusterIndices  []uint16
	LinkMapWeights         []float32
}

// SkinningCluster — one joint inside a rig. World/base transforms
// resolve to matrices in the parent MatrixTable.
type SkinningCluster struct {
	LinkMode                              uint32
	LinkObjectIndex                       uint32
	AssociateObjectIndex                  uint32
	DefaultClusterWorldTransformIndex     uint32
	DefaultClusterBaseTransformIndex      uint32
	DefaultAssociateWorldTransformIndex   uint32
	DefaultClusterWorldTransform          [16]float32
	DefaultClusterBaseTransform           [16]float32
	DefaultAssociateWorldTransform        *[16]float32 // nil unless LinkMode == 1
}

// ParsedAnimation — one clip with the per-object keyframe blobs
// resolved against the archive.
type ParsedAnimation struct {
	Desc            AnimationDesc
	AnimatedObjects []ParsedAnimatedObject
}

// ParsedAnimatedObject — one mesh / light / material's animated
// properties for a single clip. Keyframes is nil when the object
// has no associated keyframe file (a static parent in the hierarchy).
type ParsedAnimatedObject struct {
	Desc      AnimatedObjectDesc
	Keyframes *KeyframeFile
}

// KeyframeFile — the contents of one Anim<N>Obj<M>at blob.
type KeyframeFile struct {
	SharedHeaderWords uint32
	Properties        []KeyframePropertyTrack
}

// KeyframePropertyTrack — one animated channel inside a keyframe file
// (e.g. "Translation X", "Rotation Y"). Data discriminates on
// PackingType (0 = bicubic keys with in/out weights, 1 = linear /
// stepped keys, 2 = dense per-frame samples).
type KeyframePropertyTrack struct {
	Name              string
	PackingType       uint8
	InterpolationType uint8
	NumKeyframes      int
	Data              KeyframeTrackData
}

// KeyframeTrackData is the union of the three packing layouts. Exactly
// one of the Packed* fields is non-nil after parsing.
type KeyframeTrackData struct {
	Packed0 []Packed0Keyframe
	Packed1 []Packed1Keyframe
	Packed2 []float32
}

// Packed0Keyframe is the richest format — value + bicubic curve
// weights + per-key interpolation override.
type Packed0Keyframe struct {
	Value         float32
	WeighIn       float32
	WeighOut      float32
	FrameIndex    uint16
	Interpolation uint16
}

// Packed1Keyframe is the middle tier — value + frame index + per-key
// interpolation, no weights (assumed 1.0 either side).
type Packed1Keyframe struct {
	Value         float32
	FrameIndex    uint16
	Interpolation uint16
}

// ParseAnimations resolves scene.AnimData against entries[] and
// returns the fully parsed set. Returns nil when scene.AnimData is
// absent (a static scene). All matrix references and keyframe file
// pointers are checked against the archive at parse time, so a
// malformed export trips here rather than at sample time.
func ParseAnimations(entries map[string]*Entry, scene *Scene) (*ParsedAnimationSet, error) {
	if scene == nil || scene.AnimData == nil {
		return nil, nil
	}
	anim := scene.AnimData

	matTable, err := parseMatrixTable(entries, anim.NumMatrices)
	if err != nil {
		return nil, err
	}

	rigs := make([]SkinningRig, 0, len(anim.SkinningRigs))
	for i := range anim.SkinningRigs {
		rig, err := parseSkinningRig(entries, &anim.SkinningRigs[i], matTable)
		if err != nil {
			return nil, err
		}
		rigs = append(rigs, rig)
	}

	animations := make([]ParsedAnimation, 0, len(anim.Animations))
	for i := range anim.Animations {
		a, err := parseAnimation(entries, &anim.Animations[i])
		if err != nil {
			return nil, err
		}
		animations = append(animations, a)
	}

	return &ParsedAnimationSet{
		SceneScale:   anim.SceneScale,
		NumMatrices:  anim.NumMatrices,
		MatrixTable:  matTable,
		SkinningRigs: rigs,
		Animations:   animations,
	}, nil
}

func parseMatrixTable(entries map[string]*Entry, expectedCount int) (MatrixTable, error) {
	entry, ok := entries["MatTable.bin"]
	if !ok {
		return MatrixTable{}, fmt.Errorf("%w: MatTable.bin (referenced by AnimData)", ErrMissingEntry)
	}
	data := entry.Data
	if len(data)%(16*4) != 0 {
		return MatrixTable{}, fmt.Errorf("MatTable.bin length %d is not aligned to 16 floats", len(data))
	}
	count := len(data) / (16 * 4)
	mats := make([][16]float32, count)
	for i := 0; i < count; i++ {
		off := i * 16 * 4
		for j := 0; j < 16; j++ {
			bits := binary.LittleEndian.Uint32(data[off+j*4:])
			mats[i][j] = math.Float32frombits(bits)
		}
	}
	if expectedCount != 0 && expectedCount != count {
		return MatrixTable{}, fmt.Errorf("matrix table count mismatch: scene says %d, binary has %d", expectedCount, count)
	}
	return MatrixTable{Matrices: mats}, nil
}

func parseSkinningRig(entries map[string]*Entry, desc *SkinningRigDesc, matTable MatrixTable) (SkinningRig, error) {
	entry, ok := entries[desc.File]
	if !ok {
		return SkinningRig{}, fmt.Errorf("%w: skinning rig %q", ErrMissingEntry, desc.File)
	}
	bytes := entry.Data
	if len(bytes) < 24 {
		return SkinningRig{}, fmt.Errorf("skinning rig %q is too short (%d bytes)", desc.File, len(bytes))
	}

	headerWords := len(bytes) / 4
	words := make([]uint32, headerWords)
	for i := 0; i < headerWords; i++ {
		words[i] = binary.LittleEndian.Uint32(bytes[i*4:])
	}

	rig := SkinningRig{
		SourceFile:          desc.File,
		ExpectedNumClusters: int(words[0]),
		ExpectedNumVertices: int(words[1]),
		NumClusterLinks:     int(words[2]),
		OriginalObjectIndex: int(words[3]),
		IsRigidSkin:         words[4] != 0,
		TangentMethod:       words[5],
	}

	expectedHeaderWords := 6 + 7*rig.ExpectedNumClusters
	if len(words) < expectedHeaderWords {
		return SkinningRig{}, fmt.Errorf("skinning rig %q truncated cluster header", desc.File)
	}

	rig.Clusters = make([]SkinningCluster, 0, rig.ExpectedNumClusters)
	for ci := 0; ci < rig.ExpectedNumClusters; ci++ {
		base := 6 + 7*ci
		cluster := SkinningCluster{
			LinkMode:                            words[base+1],
			LinkObjectIndex:                     words[base+2],
			AssociateObjectIndex:                words[base+3],
			DefaultClusterWorldTransformIndex:   words[base+4],
			DefaultClusterBaseTransformIndex:    words[base+5],
			DefaultAssociateWorldTransformIndex: words[base+6],
		}
		w, err := matrixByIndex(matTable, cluster.DefaultClusterWorldTransformIndex)
		if err != nil {
			return SkinningRig{}, fmt.Errorf("rig %q cluster %d world: %w", desc.File, ci, err)
		}
		b, err := matrixByIndex(matTable, cluster.DefaultClusterBaseTransformIndex)
		if err != nil {
			return SkinningRig{}, fmt.Errorf("rig %q cluster %d base: %w", desc.File, ci, err)
		}
		cluster.DefaultClusterWorldTransform = w
		cluster.DefaultClusterBaseTransform = b
		if cluster.LinkMode == 1 {
			a, err := matrixByIndex(matTable, cluster.DefaultAssociateWorldTransformIndex)
			if err != nil {
				return SkinningRig{}, fmt.Errorf("rig %q cluster %d associate: %w", desc.File, ci, err)
			}
			cluster.DefaultAssociateWorldTransform = &a
		}
		rig.Clusters = append(rig.Clusters, cluster)
	}

	headerBytes := expectedHeaderWords * 4
	linkCountBytes := rig.ExpectedNumVertices
	clusterIdxBytes := rig.NumClusterLinks * 2
	weightBytes := rig.NumClusterLinks * 4
	expectedTotal := headerBytes + linkCountBytes + clusterIdxBytes + weightBytes
	if len(bytes) < expectedTotal {
		return SkinningRig{}, fmt.Errorf("skinning rig %q truncated link map (have %d, need %d)", desc.File, len(bytes), expectedTotal)
	}

	rig.LinkMapCount = append([]uint8{}, bytes[headerBytes:headerBytes+linkCountBytes]...)

	idxOff := headerBytes + linkCountBytes
	rig.LinkMapClusterIndices = make([]uint16, rig.NumClusterLinks)
	for i := 0; i < rig.NumClusterLinks; i++ {
		rig.LinkMapClusterIndices[i] = binary.LittleEndian.Uint16(bytes[idxOff+i*2:])
	}

	wOff := idxOff + clusterIdxBytes
	rig.LinkMapWeights = make([]float32, rig.NumClusterLinks)
	for i := 0; i < rig.NumClusterLinks; i++ {
		bits := binary.LittleEndian.Uint32(bytes[wOff+i*4:])
		rig.LinkMapWeights[i] = math.Float32frombits(bits)
	}
	return rig, nil
}

func parseAnimation(entries map[string]*Entry, anim *AnimationDesc) (ParsedAnimation, error) {
	objs := make([]ParsedAnimatedObject, 0, len(anim.AnimatedObjects))
	for i := range anim.AnimatedObjects {
		obj := &anim.AnimatedObjects[i]
		var kf *KeyframeFile
		if obj.File != "" {
			entry, ok := entries[obj.File]
			if !ok {
				return ParsedAnimation{}, fmt.Errorf("%w: keyframe file %q", ErrMissingEntry, obj.File)
			}
			parsed, err := parseKeyframeFile(entry.Data, obj.AnimatedProperties)
			if err != nil {
				return ParsedAnimation{}, fmt.Errorf("keyframe %q: %w", obj.File, err)
			}
			kf = parsed
		}
		objs = append(objs, ParsedAnimatedObject{Desc: *obj, Keyframes: kf})
	}
	return ParsedAnimation{Desc: *anim, AnimatedObjects: objs}, nil
}

func parseKeyframeFile(bytes []byte, properties []AnimatedPropertyDesc) (*KeyframeFile, error) {
	if len(bytes) < 4 {
		return nil, fmt.Errorf("keyframe file too short")
	}
	floatCount := len(bytes) / 4
	u16Count := len(bytes) / 2

	floats := make([]float32, floatCount)
	u32s := make([]uint32, floatCount)
	for i := 0; i < floatCount; i++ {
		bits := binary.LittleEndian.Uint32(bytes[i*4:])
		u32s[i] = bits
		floats[i] = math.Float32frombits(bits)
	}
	u16s := make([]uint16, u16Count)
	for i := 0; i < u16Count; i++ {
		u16s[i] = binary.LittleEndian.Uint16(bytes[i*2:])
	}

	headerWords := u32s[0]
	nextFloat := 1 + int(headerWords)

	tracks := make([]KeyframePropertyTrack, 0, len(properties))
	for pi, prop := range properties {
		u16Base := 2 + 2*pi
		if u16Base+1 >= len(u16s) {
			return nil, fmt.Errorf("property %d header out of u16 bounds", pi)
		}
		byteBase := 2 * u16Base
		if byteBase+3 >= len(bytes) {
			return nil, fmt.Errorf("property %d header out of byte bounds", pi)
		}
		num := int(u16s[u16Base])
		packing := bytes[byteBase+2]
		interp := bytes[byteBase+3]

		var data KeyframeTrackData
		switch packing {
		case 0:
			frames := make([]Packed0Keyframe, num)
			for k := 0; k < num; k++ {
				fi := nextFloat + k*4
				u16i := fi * 2
				if fi+2 >= len(floats) || u16i+7 >= len(u16s) {
					return nil, fmt.Errorf("property %d packed0 frame %d out of range", pi, k)
				}
				frames[k] = Packed0Keyframe{
					Value:         floats[fi],
					WeighIn:       floats[fi+1],
					WeighOut:      floats[fi+2],
					FrameIndex:    u16s[u16i+6],
					Interpolation: u16s[u16i+7],
				}
			}
			nextFloat += num * 4
			data.Packed0 = frames
		case 1:
			frames := make([]Packed1Keyframe, num)
			for k := 0; k < num; k++ {
				fi := nextFloat + k*2
				u16i := fi * 2
				if fi >= len(floats) || u16i+3 >= len(u16s) {
					return nil, fmt.Errorf("property %d packed1 frame %d out of range", pi, k)
				}
				frames[k] = Packed1Keyframe{
					Value:         floats[fi],
					FrameIndex:    u16s[u16i+2],
					Interpolation: u16s[u16i+3],
				}
			}
			nextFloat += num * 2
			data.Packed1 = frames
		case 2:
			end := nextFloat + num
			if end > len(floats) {
				return nil, fmt.Errorf("property %d packed2 values out of range", pi)
			}
			values := make([]float32, num)
			copy(values, floats[nextFloat:end])
			nextFloat = end
			data.Packed2 = values
		default:
			return nil, fmt.Errorf("property %d: unsupported packing type %d", pi, packing)
		}

		tracks = append(tracks, KeyframePropertyTrack{
			Name:              prop.Name,
			PackingType:       packing,
			InterpolationType: interp,
			NumKeyframes:      num,
			Data:              data,
		})
	}

	return &KeyframeFile{
		SharedHeaderWords: headerWords,
		Properties:        tracks,
	}, nil
}

func matrixByIndex(t MatrixTable, idx uint32) ([16]float32, error) {
	if int(idx) >= len(t.Matrices) {
		return [16]float32{}, fmt.Errorf("matrix index %d out of range (have %d)", idx, len(t.Matrices))
	}
	return t.Matrices[idx], nil
}
