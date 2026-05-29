package mview

import "math"

// SampledTransform — the TRS triple at a specific frame. Rotation is
// stored as Euler angles in degrees (matches Toolbag's authoring
// model); call EvaluateMatrix to fold into a 4×4.
type SampledTransform struct {
	Translation [3]float32
	RotationDeg [3]float32
	Scale       [3]float32
}

// Sample evaluates this track's value at the given (fractional) frame
// position, falling back to `defaultValue` when the track is empty.
// Honours per-key interpolation overrides (linear = 0, stepped = 2,
// hermite/bezier curve = anything else).
func (k *KeyframePropertyTrack) Sample(frame, defaultValue float32) float32 {
	switch {
	case k.Data.Packed0 != nil:
		return samplePacked0(k.Data.Packed0, frame, defaultValue)
	case k.Data.Packed1 != nil:
		return samplePacked1(k.Data.Packed1, frame, defaultValue)
	case k.Data.Packed2 != nil:
		return samplePacked2(k.Data.Packed2, frame, defaultValue)
	}
	return defaultValue
}

// KeyframeTimes returns the frame positions every keyframe in this
// track is anchored to. For packed2 (dense) tracks this is [0..N-1].
func (k *KeyframePropertyTrack) KeyframeTimes() []float32 {
	switch {
	case k.Data.Packed0 != nil:
		out := make([]float32, len(k.Data.Packed0))
		for i, f := range k.Data.Packed0 {
			out[i] = float32(f.FrameIndex)
		}
		return out
	case k.Data.Packed1 != nil:
		out := make([]float32, len(k.Data.Packed1))
		for i, f := range k.Data.Packed1 {
			out[i] = float32(f.FrameIndex)
		}
		return out
	case k.Data.Packed2 != nil:
		out := make([]float32, len(k.Data.Packed2))
		for i := range k.Data.Packed2 {
			out[i] = float32(i)
		}
		return out
	}
	return nil
}

// SampleTRS evaluates the standard nine Toolbag TRS properties on
// this object at the given frame. Missing channels get sensible
// defaults (translation/rotation = 0, scale = 1).
func (o *ParsedAnimatedObject) SampleTRS(frame float32) SampledTransform {
	out := SampledTransform{Scale: [3]float32{1, 1, 1}}
	if o.Keyframes == nil {
		return out
	}
	out.Translation[0] = o.sampleNamed("Translation X", frame, 0)
	out.Translation[1] = o.sampleNamed("Translation Y", frame, 0)
	out.Translation[2] = o.sampleNamed("Translation Z", frame, 0)
	out.RotationDeg[0] = o.sampleNamed("Rotation X", frame, 0)
	out.RotationDeg[1] = o.sampleNamed("Rotation Y", frame, 0)
	out.RotationDeg[2] = o.sampleNamed("Rotation Z", frame, 0)
	out.Scale[0] = o.sampleNamed("Scale X", frame, 1)
	out.Scale[1] = o.sampleNamed("Scale Y", frame, 1)
	out.Scale[2] = o.sampleNamed("Scale Z", frame, 1)
	return out
}

func (o *ParsedAnimatedObject) sampleNamed(name string, frame, def float32) float32 {
	if o.Keyframes == nil {
		return def
	}
	for i := range o.Keyframes.Properties {
		if o.Keyframes.Properties[i].Name == name {
			return o.Keyframes.Properties[i].Sample(frame, def)
		}
	}
	return def
}

// EvaluateMatrix folds the sampled TRS into a 4×4 column-major
// transform. `sceneSpaceOrder` picks YXZ vs ZYX rotation composition
// (matches Toolbag's scene vs object space convention).
func (s SampledTransform) EvaluateMatrix(sceneSpaceOrder bool) [16]float32 {
	rx := rotationMatrix(deg2rad(s.RotationDeg[0]), 0)
	ry := rotationMatrix(deg2rad(s.RotationDeg[1]), 1)
	rz := rotationMatrix(deg2rad(s.RotationDeg[2]), 2)

	var m [16]float32
	if sceneSpaceOrder {
		m = mulMatrix4(ry, mulMatrix4(rx, rz))
	} else {
		m = mulMatrix4(rz, mulMatrix4(ry, rx))
	}
	m[12] = s.Translation[0]
	m[13] = s.Translation[1]
	m[14] = s.Translation[2]
	for j := 0; j < 4; j++ {
		m[j] *= s.Scale[0]
		m[4+j] *= s.Scale[1]
		m[8+j] *= s.Scale[2]
	}
	return m
}

func samplePacked0(frames []Packed0Keyframe, frame, def float32) float32 {
	if len(frames) == 0 {
		return def
	}
	if len(frames) == 1 {
		return frames[0].Value
	}
	if frame <= float32(frames[0].FrameIndex) {
		return frames[0].Value
	}
	for i := 0; i < len(frames)-1; i++ {
		start, end := frames[i], frames[i+1]
		sf, ef := float32(start.FrameIndex), float32(end.FrameIndex)
		if frame <= ef {
			if ef <= sf {
				return end.Value
			}
			return curveSegmentPacked0(frames, i, frame)
		}
	}
	return frames[len(frames)-1].Value
}

func samplePacked1(frames []Packed1Keyframe, frame, def float32) float32 {
	if len(frames) == 0 {
		return def
	}
	if len(frames) == 1 {
		return frames[0].Value
	}
	if frame <= float32(frames[0].FrameIndex) {
		return frames[0].Value
	}
	for i := 0; i < len(frames)-1; i++ {
		start, end := frames[i], frames[i+1]
		sf, ef := float32(start.FrameIndex), float32(end.FrameIndex)
		if frame <= ef {
			if ef <= sf {
				return end.Value
			}
			interp := start.Interpolation
			if interp == 2 { // stepped
				if frame >= ef {
					return end.Value
				}
				return start.Value
			}
			if interp == 0 { // linear
				t := clamp01((frame - sf) / (ef - sf))
				return start.Value + (end.Value-start.Value)*t
			}
			return curveSegment(
				frame,
				curvePoint{frame: sf, value: start.Value, weighIn: 1, weighOut: 1, interp: uint8(interp)},
				curvePoint{frame: ef, value: end.Value, weighIn: 1, weighOut: 1, interp: uint8(end.Interpolation)},
				prevCurvePointPacked1(frames, i),
				nextCurvePointPacked1(frames, i+1),
			)
		}
	}
	return frames[len(frames)-1].Value
}

func samplePacked2(values []float32, frame, def float32) float32 {
	if len(values) == 0 {
		return def
	}
	lower := int(math.Max(math.Floor(float64(frame)), 0))
	upper := int(math.Max(math.Ceil(float64(frame)), 0))
	if lower >= len(values) {
		return values[len(values)-1]
	}
	if lower == upper || upper >= len(values) {
		return values[lower]
	}
	t := clamp01(frame - float32(lower))
	return values[lower] + (values[upper]-values[lower])*t
}

func curveSegmentPacked0(frames []Packed0Keyframe, startI int, frame float32) float32 {
	endI := startI + 1
	if endI >= len(frames) {
		endI = len(frames) - 1
	}
	start, end := frames[startI], frames[endI]
	interp := start.Interpolation
	if interp == 2 {
		if frame >= float32(end.FrameIndex) {
			return end.Value
		}
		return start.Value
	}
	if interp == 0 {
		t := clamp01((frame - float32(start.FrameIndex)) / (float32(end.FrameIndex) - float32(start.FrameIndex)))
		return start.Value + (end.Value-start.Value)*t
	}
	return curveSegment(
		frame,
		curvePoint{frame: float32(start.FrameIndex), value: start.Value, weighIn: start.WeighIn, weighOut: start.WeighOut, interp: uint8(start.Interpolation)},
		curvePoint{frame: float32(end.FrameIndex), value: end.Value, weighIn: end.WeighIn, weighOut: end.WeighOut, interp: uint8(end.Interpolation)},
		prevCurvePointPacked0(frames, startI),
		nextCurvePointPacked0(frames, endI),
	)
}

// curvePoint is the four-tuple the curve evaluator wants for each
// keyframe in (prev, start, end, next). All three packing formats
// fold down to this shape.
type curvePoint struct {
	frame, value, weighIn, weighOut float32
	interp                          uint8
}

func prevCurvePointPacked0(frames []Packed0Keyframe, startI int) *curvePoint {
	if startI == 0 {
		return nil
	}
	f := frames[startI-1]
	return &curvePoint{
		frame: float32(f.FrameIndex), value: f.Value,
		weighIn: f.WeighIn, weighOut: f.WeighOut,
		interp: uint8(f.Interpolation),
	}
}

func nextCurvePointPacked0(frames []Packed0Keyframe, endI int) *curvePoint {
	if endI+1 >= len(frames) {
		return nil
	}
	f := frames[endI+1]
	return &curvePoint{
		frame: float32(f.FrameIndex), value: f.Value,
		weighIn: f.WeighIn, weighOut: f.WeighOut,
		interp: uint8(f.Interpolation),
	}
}

func prevCurvePointPacked1(frames []Packed1Keyframe, startI int) *curvePoint {
	if startI == 0 {
		return nil
	}
	f := frames[startI-1]
	return &curvePoint{
		frame: float32(f.FrameIndex), value: f.Value,
		weighIn: 1, weighOut: 1,
		interp: uint8(f.Interpolation),
	}
}

func nextCurvePointPacked1(frames []Packed1Keyframe, endI int) *curvePoint {
	if endI+1 >= len(frames) {
		return nil
	}
	f := frames[endI+1]
	return &curvePoint{
		frame: float32(f.FrameIndex), value: f.Value,
		weighIn: 1, weighOut: 1,
		interp: uint8(f.Interpolation),
	}
}

// curveSegment is the four-point hermite/bezier blender Toolbag
// uses. Mirrors src/animation.rs::sample_curve_segment +
// evaluate_curve.
func curveSegment(frame float32, start, end curvePoint, prev, next *curvePoint) float32 {
	kf0 := curvePoint{frame: start.frame, value: start.value, weighIn: 1, weighOut: 1, interp: start.interp}
	if prev != nil {
		kf0 = *prev
	}
	kf1 := start
	kf2 := end
	kf3 := curvePoint{frame: end.frame, value: end.value, weighIn: 1, weighOut: 1, interp: end.interp}
	if next != nil {
		kf3 = *next
	}

	if prev == nil || next == nil {
		kf0.frame, kf0.value = kf1.frame, kf1.value
		kf3.frame, kf3.value = kf2.frame, kf2.value
	}
	if prev == nil {
		kf1.frame++
		kf2.frame++
		kf3.frame++
	}
	if next == nil {
		kf0.frame++
		kf1.frame++
		kf2.frame++
	}
	return evaluateCurve(frame, kf0, kf1, kf2, kf3)
}

func evaluateCurve(frame float32, kf0, kf1, kf2, kf3 curvePoint) float32 {
	g := kf1.frame - (kf2.frame - kf0.frame)
	h := kf2.frame - (kf1.frame - kf3.frame)
	k := kf1.value - (kf2.value-kf0.value)*kf1.weighOut
	n := kf2.value - (kf1.value-kf3.value)*kf2.weighIn
	if kf1.interp == 3 {
		g = kf1.frame - (kf2.frame - kf1.frame)
		k = kf1.value - kf1.weighOut
	}
	if kf2.interp == 3 {
		h = kf2.frame - (kf1.frame - kf2.frame)
		n = kf2.value + kf2.weighIn
	}
	gN := (frame - g) / (kf1.frame - g)
	b := (frame - kf1.frame) / (kf2.frame - kf1.frame)
	d := (frame - kf2.frame) / (h - kf2.frame)
	hmid := kf1.value*(1-b) + kf2.value*b
	return ((k*(1-gN)+kf1.value*gN)*(1-b)+hmid*b)*(1-b) +
		((kf2.value*(1-d)+n*d)*b+hmid*(1-b))*b
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func deg2rad(d float32) float32 {
	return d * float32(math.Pi/180)
}

func rotationMatrix(angle float32, axis int) [16]float32 {
	s := float32(math.Sin(float64(angle)))
	c := float32(math.Cos(float64(angle)))
	switch axis {
	case 0:
		return [16]float32{1, 0, 0, 0, 0, c, s, 0, 0, -s, c, 0, 0, 0, 0, 1}
	case 1:
		return [16]float32{c, 0, -s, 0, 0, 1, 0, 0, s, 0, c, 0, 0, 0, 0, 1}
	default:
		return [16]float32{c, s, 0, 0, -s, c, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}
	}
}

// identityMatrix returns the 4×4 identity in column-major order.
func identityMatrix() [16]float32 {
	return [16]float32{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}
}

func mulMatrix4(a, b [16]float32) [16]float32 {
	var r [16]float32
	for col := 0; col < 4; col++ {
		for row := 0; row < 4; row++ {
			r[col*4+row] = a[row]*b[col*4] +
				a[4+row]*b[col*4+1] +
				a[8+row]*b[col*4+2] +
				a[12+row]*b[col*4+3]
		}
	}
	return r
}

func lerpMatrix4(a, b [16]float32, t float32) [16]float32 {
	var r [16]float32
	for i := 0; i < 16; i++ {
		r[i] = a[i]*(1-t) + b[i]*t
	}
	return r
}

// invertMatrix4 returns the inverse of the given 4×4 matrix and
// `true`, or the zero matrix and `false` if it's singular.
// Direct port of the Rust crate's invert_matrix4.
func invertMatrix4(m [16]float32) ([16]float32, bool) {
	var inv [16]float32
	inv[0] = m[5]*m[10]*m[15] - m[5]*m[11]*m[14] - m[9]*m[6]*m[15] + m[9]*m[7]*m[14] + m[13]*m[6]*m[11] - m[13]*m[7]*m[10]
	inv[4] = -m[4]*m[10]*m[15] + m[4]*m[11]*m[14] + m[8]*m[6]*m[15] - m[8]*m[7]*m[14] - m[12]*m[6]*m[11] + m[12]*m[7]*m[10]
	inv[8] = m[4]*m[9]*m[15] - m[4]*m[11]*m[13] - m[8]*m[5]*m[15] + m[8]*m[7]*m[13] + m[12]*m[5]*m[11] - m[12]*m[7]*m[9]
	inv[12] = -m[4]*m[9]*m[14] + m[4]*m[10]*m[13] + m[8]*m[5]*m[14] - m[8]*m[6]*m[13] - m[12]*m[5]*m[10] + m[12]*m[6]*m[9]
	inv[1] = -m[1]*m[10]*m[15] + m[1]*m[11]*m[14] + m[9]*m[2]*m[15] - m[9]*m[3]*m[14] - m[13]*m[2]*m[11] + m[13]*m[3]*m[10]
	inv[5] = m[0]*m[10]*m[15] - m[0]*m[11]*m[14] - m[8]*m[2]*m[15] + m[8]*m[3]*m[14] + m[12]*m[2]*m[11] - m[12]*m[3]*m[10]
	inv[9] = -m[0]*m[9]*m[15] + m[0]*m[11]*m[13] + m[8]*m[1]*m[15] - m[8]*m[3]*m[13] - m[12]*m[1]*m[11] + m[12]*m[3]*m[9]
	inv[13] = m[0]*m[9]*m[14] - m[0]*m[10]*m[13] - m[8]*m[1]*m[14] + m[8]*m[2]*m[13] + m[12]*m[1]*m[10] - m[12]*m[2]*m[9]
	inv[2] = m[1]*m[6]*m[15] - m[1]*m[7]*m[14] - m[5]*m[2]*m[15] + m[5]*m[3]*m[14] + m[13]*m[2]*m[7] - m[13]*m[3]*m[6]
	inv[6] = -m[0]*m[6]*m[15] + m[0]*m[7]*m[14] + m[4]*m[2]*m[15] - m[4]*m[3]*m[14] - m[12]*m[2]*m[7] + m[12]*m[3]*m[6]
	inv[10] = m[0]*m[5]*m[15] - m[0]*m[7]*m[13] - m[4]*m[1]*m[15] + m[4]*m[3]*m[13] + m[12]*m[1]*m[7] - m[12]*m[3]*m[5]
	inv[14] = -m[0]*m[5]*m[14] + m[0]*m[6]*m[13] + m[4]*m[1]*m[14] - m[4]*m[2]*m[13] - m[12]*m[1]*m[6] + m[12]*m[2]*m[5]
	inv[3] = -m[1]*m[6]*m[11] + m[1]*m[7]*m[10] + m[5]*m[2]*m[11] - m[5]*m[3]*m[10] - m[9]*m[2]*m[7] + m[9]*m[3]*m[6]
	inv[7] = m[0]*m[6]*m[11] - m[0]*m[7]*m[10] - m[4]*m[2]*m[11] + m[4]*m[3]*m[10] + m[8]*m[2]*m[7] - m[8]*m[3]*m[6]
	inv[11] = -m[0]*m[5]*m[11] + m[0]*m[7]*m[9] + m[4]*m[1]*m[11] - m[4]*m[3]*m[9] - m[8]*m[1]*m[7] + m[8]*m[3]*m[5]
	inv[15] = m[0]*m[5]*m[10] - m[0]*m[6]*m[9] - m[4]*m[1]*m[10] + m[4]*m[2]*m[9] + m[8]*m[1]*m[6] - m[8]*m[2]*m[5]

	det := m[0]*inv[0] + m[1]*inv[4] + m[2]*inv[8] + m[3]*inv[12]
	if math.Abs(float64(det)) <= math.SmallestNonzeroFloat32 {
		return [16]float32{}, false
	}
	invDet := 1 / det
	for i := range inv {
		inv[i] *= invDet
	}
	return inv, true
}
