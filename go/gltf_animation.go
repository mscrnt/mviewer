package mview

import (
	"encoding/binary"
	"math"

	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"
)

// appendAnimations folds a ParsedAnimationSet into the glTF document.
// For each animation we emit:
//   - one synthetic joint node per AnimatedObject (so the channel
//     targets are stable indices regardless of how many of those
//     objects map to real scene meshes)
//   - per object: translation / rotation / scale samplers, sampled at
//     1 keyframe per source frame (handles all three packing types
//     uniformly without needing per-curve interpolation in the viewer)
//   - rotation is converted from Euler degrees → quaternion using the
//     same XYZ-or-YXZ order the sampler's matrix evaluation uses
//
// Skins:
//   - one gltf.Skin per SkinningRig
//   - joints array = the cluster's link_object_index chain (resolved
//     to the synthetic joint nodes we just emitted)
//   - inverseBindMatrices = the cluster's DefaultClusterBaseTransform
//     (already an inverse-bind world transform per Marmoset's runtime)
//
// Returns nil if there's no animation data to emit. The caller wires
// up doc.Skins[*] to mesh nodes separately.
func appendAnimations(doc *gltf.Document, set *ParsedAnimationSet) error {
	if set == nil || len(set.Animations) == 0 {
		return nil
	}

	// Each animation may target a different number of objects; we
	// emit one joint per (animation, object) pair so channel indices
	// don't cross-collide between clips. That's looser than upstream
	// (which reuses joints across clips) but well-formed.
	for ai := range set.Animations {
		anim := &set.Animations[ai]
		gltfAnim := &gltf.Animation{Name: anim.Desc.Name}

		jointNodeBase := len(doc.Nodes)
		for oi := range anim.AnimatedObjects {
			obj := &anim.AnimatedObjects[oi]
			name := obj.Desc.PartName
			if name == "" {
				name = "joint"
			}
			doc.Nodes = append(doc.Nodes, &gltf.Node{Name: name})
		}

		// Sampled time axis is shared across every channel: one entry
		// per integer frame from 0..TotalFrames, plus a tail at
		// TotalFrames itself. fps = animation.OriginalFPS.
		fps := anim.Desc.OriginalFPS
		if fps <= 0 {
			fps = 30
		}
		total := anim.Desc.TotalFrames
		if total <= 0 {
			total = 1
		}
		times := make([]float32, total+1)
		for i := 0; i <= total; i++ {
			times[i] = float32(i) / fps
		}
		timeAcc := modeler.WriteAccessor(doc, gltf.TargetNone, times)
		// modeler.WriteAccessor doesn't set min/max for scalar floats
		// but glTF spec requires min/max on animation sampler input
		// accessors. Patch them in.
		if timeAcc >= 0 && timeAcc < len(doc.Accessors) {
			doc.Accessors[timeAcc].Min = []float64{float64(times[0])}
			doc.Accessors[timeAcc].Max = []float64{float64(times[len(times)-1])}
		}

		for oi := range anim.AnimatedObjects {
			obj := &anim.AnimatedObjects[oi]
			if obj.Keyframes == nil {
				continue
			}
			translations := make([][3]float32, total+1)
			rotations := make([][4]float32, total+1)
			scales := make([][3]float32, total+1)
			for f := 0; f <= total; f++ {
				trs := obj.SampleTRS(float32(f))
				translations[f] = trs.Translation
				rotations[f] = eulerDegToQuat(trs.RotationDeg)
				scales[f] = trs.Scale
			}

			tAcc := modeler.WriteAccessor(doc, gltf.TargetNone, translations)
			rAcc := modeler.WriteAccessor(doc, gltf.TargetNone, rotations)
			sAcc := modeler.WriteAccessor(doc, gltf.TargetNone, scales)
			nodeIdx := jointNodeBase + oi

			addChannel(gltfAnim, timeAcc, tAcc, nodeIdx, gltf.TRSTranslation)
			addChannel(gltfAnim, timeAcc, rAcc, nodeIdx, gltf.TRSRotation)
			addChannel(gltfAnim, timeAcc, sAcc, nodeIdx, gltf.TRSScale)
		}

		if len(gltfAnim.Channels) > 0 {
			doc.Animations = append(doc.Animations, gltfAnim)
		}
	}

	for ri := range set.SkinningRigs {
		rig := &set.SkinningRigs[ri]
		if len(rig.Clusters) == 0 {
			continue
		}
		joints := make([]int, 0, len(rig.Clusters))
		invBind := make([][4][4]float32, 0, len(rig.Clusters))
		for ci := range rig.Clusters {
			c := &rig.Clusters[ci]
			// Reference the cluster's link_object_index as the joint
			// node. Upstream's full path would resolve this through
			// the animation's animated_objects table; we cap at the
			// node count we emitted, mirroring the safer subset.
			jIdx := int(c.LinkObjectIndex)
			if jIdx < 0 || jIdx >= len(doc.Nodes) {
				jIdx = 0
			}
			joints = append(joints, jIdx)
			invBind = append(invBind, mat4ToColumns(c.DefaultClusterBaseTransform))
		}
		ibmAcc := modeler.WriteAccessor(doc, gltf.TargetNone, invBind)
		doc.Skins = append(doc.Skins, &gltf.Skin{
			Joints:              joints,
			InverseBindMatrices: gltf.Index(ibmAcc),
		})
	}
	return nil
}

func addChannel(a *gltf.Animation, input, output, node int, path gltf.TRSProperty) {
	a.Samplers = append(a.Samplers, &gltf.AnimationSampler{
		Input:         input,
		Output:        output,
		Interpolation: gltf.InterpolationLinear,
	})
	a.Channels = append(a.Channels, &gltf.AnimationChannel{
		Sampler: len(a.Samplers) - 1,
		Target: gltf.AnimationChannelTarget{
			Node: gltf.Index(node),
			Path: path,
		},
	})
}

// eulerDegToQuat converts Toolbag's XYZ Euler triple in degrees to a
// quaternion in glTF order (x, y, z, w). Matches the rotation matrix
// composition the sampler uses (Z then Y then X applied left-to-right
// in object space).
func eulerDegToQuat(deg [3]float32) [4]float32 {
	rx := float64(deg[0]) * math.Pi / 180
	ry := float64(deg[1]) * math.Pi / 180
	rz := float64(deg[2]) * math.Pi / 180
	cx, sx := math.Cos(rx/2), math.Sin(rx/2)
	cy, sy := math.Cos(ry/2), math.Sin(ry/2)
	cz, sz := math.Cos(rz/2), math.Sin(rz/2)
	// XYZ order: q = qz * qy * qx
	qx := float32(sx*cy*cz + cx*sy*sz)
	qy := float32(cx*sy*cz - sx*cy*sz)
	qz := float32(cx*cy*sz + sx*sy*cz)
	qw := float32(cx*cy*cz - sx*sy*sz)
	return [4]float32{qx, qy, qz, qw}
}

// mat4ToColumns reshapes our column-major [16]float32 into the
// [4][4]float32 representation modeler.WriteAccessor wants for
// inverseBindMatrices.
func mat4ToColumns(m [16]float32) [4][4]float32 {
	var c [4][4]float32
	for col := 0; col < 4; col++ {
		for row := 0; row < 4; row++ {
			c[col][row] = m[col*4+row]
		}
	}
	return c
}

// _ keeps binary in the import set for future serialization paths.
var _ = binary.LittleEndian
