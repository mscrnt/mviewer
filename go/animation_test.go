package mview

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The upstream Rust crate ships two animated fixtures under
// ../test_data. Use them as ground truth so the Go parser must
// produce the same counts the Rust tests assert against.
//
// Skipped silently if the fixtures are missing (someone cloned just
// the go/ submodule).

func parseFixture(t *testing.T, name string) *ParsedAnimationSet {
	t.Helper()
	path := filepath.Join("..", "test_data", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("fixture %s unavailable: %v", name, err)
	}
	entries, err := ReadAll(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	scene, err := ParseScene(entries["scene.json"].Data)
	if err != nil {
		t.Fatalf("ParseScene: %v", err)
	}
	set, err := ParseAnimations(entries, scene)
	if err != nil {
		t.Fatalf("ParseAnimations: %v", err)
	}
	if set == nil {
		t.Fatalf("expected animation data in %s", name)
	}
	return set
}

func TestParseAnimations_VivFox(t *testing.T) {
	set := parseFixture(t, "vivfox.mview")
	if set.NumMatrices != 72 {
		t.Errorf("NumMatrices: got %d want 72", set.NumMatrices)
	}
	if len(set.SkinningRigs) != 2 {
		t.Errorf("SkinningRigs: got %d want 2", len(set.SkinningRigs))
	}
	if len(set.Animations) != 7 {
		t.Errorf("Animations: got %d want 7", len(set.Animations))
	}
	if len(set.MatrixTable.Matrices) != 72 {
		t.Errorf("MatrixTable: got %d want 72", len(set.MatrixTable.Matrices))
	}
}

func TestParseAnimations_NatNephilim(t *testing.T) {
	set := parseFixture(t, "natnephilim.mview")
	if set.NumMatrices != 380 {
		t.Errorf("NumMatrices: got %d want 380", set.NumMatrices)
	}
	if len(set.SkinningRigs) != 4 {
		t.Errorf("SkinningRigs: got %d want 4", len(set.SkinningRigs))
	}
	if len(set.Animations) != 1 {
		t.Errorf("Animations: got %d want 1", len(set.Animations))
	}
}

func TestParseAnimations_StaticSceneReturnsNil(t *testing.T) {
	a := buildEntry("scene.json", "application/json", 0, []byte(`{"meshes":[],"materials":[]}`))
	entries, err := ReadAll(bytes.NewReader(a))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	scene, err := ParseScene(entries["scene.json"].Data)
	if err != nil {
		t.Fatalf("ParseScene: %v", err)
	}
	set, err := ParseAnimations(entries, scene)
	if err != nil {
		t.Fatalf("ParseAnimations on static scene: %v", err)
	}
	if set != nil {
		t.Errorf("expected nil for static scene, got %+v", set)
	}
}

// Sampler must not panic / produce NaN on every frame of every
// keyframe track in the real fixtures.
func TestSampleTRS_NoNaNAcrossVivFox(t *testing.T) {
	set := parseFixture(t, "vivfox.mview")
	for ai := range set.Animations {
		anim := &set.Animations[ai]
		for oi := range anim.AnimatedObjects {
			obj := &anim.AnimatedObjects[oi]
			if obj.Keyframes == nil {
				continue
			}
			// Sample every property at fractional frames 0, .5, 1, ...
			// up to the animation length.
			total := anim.Desc.TotalFrames
			if total == 0 {
				continue
			}
			for f := 0.0; f <= float64(total); f += 0.5 {
				trs := obj.SampleTRS(float32(f))
				for _, v := range trs.Translation {
					if isNaNOrInf(v) {
						t.Fatalf("NaN/Inf translation at anim=%d obj=%d frame=%v", ai, oi, f)
					}
				}
				for _, v := range trs.RotationDeg {
					if isNaNOrInf(v) {
						t.Fatalf("NaN/Inf rotation at anim=%d obj=%d frame=%v", ai, oi, f)
					}
				}
				for _, v := range trs.Scale {
					if isNaNOrInf(v) {
						t.Fatalf("NaN/Inf scale at anim=%d obj=%d frame=%v", ai, oi, f)
					}
				}
			}
		}
	}
}

func isNaNOrInf(v float32) bool {
	if v != v {
		return true
	}
	if v > 3.4e38 || v < -3.4e38 {
		return true
	}
	return false
}

func TestParseKeyframeFile_Packed0Smoke(t *testing.T) {
	set := parseFixture(t, "vivfox.mview")
	// Find any animated object with keyframes and verify at least
	// one Translation X track came through populated.
	for i := range set.Animations {
		for j := range set.Animations[i].AnimatedObjects {
			ao := &set.Animations[i].AnimatedObjects[j]
			if ao.Keyframes == nil {
				continue
			}
			for _, tr := range ao.Keyframes.Properties {
				if tr.Name == "Translation X" && tr.NumKeyframes > 0 {
					if tr.PackingType == 0 && len(tr.Data.Packed0) == 0 {
						t.Errorf("packed0 track empty despite num=%d", tr.NumKeyframes)
					}
					if tr.PackingType == 1 && len(tr.Data.Packed1) == 0 {
						t.Errorf("packed1 track empty despite num=%d", tr.NumKeyframes)
					}
					if tr.PackingType == 2 && len(tr.Data.Packed2) == 0 {
						t.Errorf("packed2 track empty despite num=%d", tr.NumKeyframes)
					}
					return
				}
			}
		}
	}
	t.Skip("no Translation X track in vivfox to probe")
}
