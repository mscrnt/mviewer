package mview

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Scene mirrors the top-level `scene.json` payload inside an .mview
// archive. It's the runtime contract Marmoset's viewer consumes:
// where every mesh / material / animation / light comes from, plus
// the camera + post settings the scene was authored against.
//
// Schema borrowed wholesale from the Rust crate's src/scene.rs at
// commit master — see also docs/reverse-engineering/marmoset-js-spec.md
// for the field-by-field source it was reverse-engineered from.
//
// Go conventions deviate from the Rust source in two ways:
//   - All optional scalar fields use *T so we can tell "absent from
//     JSON" from "present with zero value." Slices and maps use the
//     standard "nil means absent" idiom.
//   - The boolish fields (Marmoset sometimes serialises bool as 1/0
//     or "true"/"false") use the Boolish helper type rather than a
//     custom UnmarshalJSON per field.
type Scene struct {
	MetaData    *MetaData             `json:"metaData,omitempty"`
	MainCamera  *Camera               `json:"mainCamera,omitempty"`
	Cameras     map[string]Camera     `json:"Cameras,omitempty"`
	Lights      *Lights               `json:"lights,omitempty"`
	Meshes      []MeshDesc            `json:"meshes"`
	Materials   []MaterialDesc        `json:"materials"`
	Fog         *FogDesc              `json:"fog,omitempty"`
	Sky         *SkyDesc              `json:"sky,omitempty"`
	ShadowFloor *ShadowFloorDesc      `json:"shadowFloor,omitempty"`
	AnimData    *AnimData             `json:"AnimData,omitempty"`
}

// MetaData carries the human-readable provenance Marmoset Toolbag
// stamps into every export.
type MetaData struct {
	TbVersion *uint32 `json:"tbVersion,omitempty"`
	Title     *string `json:"title,omitempty"`
	Author    *string `json:"author,omitempty"`
}

// Camera bundles a view direction + post-processing chain. The main
// camera lives at the top level; alternate cameras land in
// `Scene.Cameras` keyed by name.
type Camera struct {
	View *ViewDesc `json:"view,omitempty"`
	Post *PostDesc `json:"post,omitempty"`
}

// ViewDesc — how the camera is oriented at scene load.
type ViewDesc struct {
	Angles      *[2]float32 `json:"angles,omitempty"`
	FOV         *float32    `json:"fov,omitempty"`
	OrbitRadius *float32    `json:"orbitRadius,omitempty"`
	Pivot       *[3]float32 `json:"pivot,omitempty"`
}

// PostDesc — the runtime post-process chain (sharpen, bloom, vignette,
// color grading, LUT, etc). Out of scope for a glTF export but kept
// in the parser for symmetry with the format.
type PostDesc struct {
	Sharpen        *float32    `json:"sharpen,omitempty"`
	SharpenLimit   *float32    `json:"sharpenLimit,omitempty"`
	BloomColor     *[4]float32 `json:"bloomColor,omitempty"`
	BloomSize      *float32    `json:"bloomSize,omitempty"`
	Vignette       *[4]float32 `json:"vignette,omitempty"`
	VignetteCurve  *float32    `json:"vignetteCurve,omitempty"`
	Saturation     *[4]float32 `json:"saturation,omitempty"`
	Contrast       *[4]float32 `json:"contrast,omitempty"`
	Brightness     *[4]float32 `json:"brightness,omitempty"`
	Bias           *[4]float32 `json:"bias,omitempty"`
	Grain          *float32    `json:"grain,omitempty"`
	GrainSharpness *float32    `json:"grainSharpness,omitempty"`
	ToneMap        *uint32     `json:"toneMap,omitempty"`
	ColorLUT       []byte      `json:"colorLUT,omitempty"`
}

// Lights is the packed-array light table — positions / directions /
// colors / spot params are flat float arrays the runtime unpacks by
// index. The shape is preserved here for round-trip fidelity even
// though our gltf exporter will likely emit individual KHR_lights.
type Lights struct {
	Count             *int      `json:"count,omitempty"`
	ShadowCount       *int      `json:"shadowCount,omitempty"`
	UseNewAttenuation Boolish   `json:"useNewAttenuation,omitempty"`
	Rotation          *float32  `json:"rotation,omitempty"`
	Positions         []float32 `json:"positions,omitempty"`
	Directions        []float32 `json:"directions,omitempty"`
	Colors            []float32 `json:"colors,omitempty"`
	Parameters        []float32 `json:"parameters,omitempty"`
	Spot              []float32 `json:"spot,omitempty"`
	MatrixWeights     []uint32  `json:"matrixWeights,omitempty"`
}

// MeshDesc — one mesh + the file it points at. `IndexCount` /
// `IndexTypeSize` / `VertexCount` are the keys the mesh decoder uses
// to walk the binary blob the .mview archive carries at `File`.
type MeshDesc struct {
	Name              string         `json:"name"`
	IndexCount        int            `json:"indexCount"`
	IndexTypeSize     int            `json:"indexTypeSize"`
	WireCount         int            `json:"wireCount"`
	VertexCount       int            `json:"vertexCount"`
	SecondaryTexCoord *uint32        `json:"secondaryTexCoord,omitempty"`
	VertexColor       *uint32        `json:"vertexColor,omitempty"`
	IsDynamicMesh     Boolish        `json:"isDynamicMesh,omitempty"`
	Transform         *[16]float32   `json:"transform,omitempty"`
	File              string         `json:"file"`
	SubMeshes         []SubMeshDesc  `json:"subMeshes"`
}

// SubMeshDesc — a (material, index range) slice inside a parent
// mesh. glTF translates this to one primitive per submesh.
type SubMeshDesc struct {
	Material   string `json:"material"`
	FirstIndex int    `json:"firstIndex"`
	IndexCount int    `json:"indexCount"`
}

// MaterialDesc — the slot table the runtime binds textures + render
// flags onto. The texture fields reference entries inside the same
// .mview archive by name; the gltf exporter resolves them via
// Archive.Find().
type MaterialDesc struct {
	Name                string                          `json:"name"`
	AlbedoTex           string                          `json:"albedoTex"`
	AlphaTex            *string                         `json:"alphaTex,omitempty"`
	NormalTex           *string                         `json:"normalTex,omitempty"`
	ReflectivityTex     *string                         `json:"reflectivityTex,omitempty"`
	GlossTex            *string                         `json:"glossTex,omitempty"`
	ExtrasTex           *string                         `json:"extrasTex,omitempty"`
	ExtrasTexA          *string                         `json:"extrasTexA,omitempty"`
	Blend               *string                         `json:"blend,omitempty"`
	AlphaTest           *float32                        `json:"alphaTest,omitempty"`
	UseSkin             Boolish                         `json:"useSkin,omitempty"`
	GgxSpecular         Boolish                         `json:"ggxSpecular,omitempty"`
	UnlitDiffuse        Boolish                         `json:"unlitDiffuse,omitempty"`
	Fresnel             *[3]float32                     `json:"fresnel,omitempty"`
	HorizonOcclude      *float32                        `json:"horizonOcclude,omitempty"`
	HorizonSmoothing    *float32                        `json:"horizonSmoothing,omitempty"`
	Aniso               Boolish                         `json:"aniso,omitempty"`
	Microfiber          Boolish                         `json:"microfiber,omitempty"`
	Refraction          Boolish                         `json:"refraction,omitempty"`
	EmissiveIntensity   *float32                        `json:"emissiveIntensity,omitempty"`
	EmissiveSecondaryUV Boolish                         `json:"emissiveSecondaryUV,omitempty"`
	AoSecondaryUV       Boolish                         `json:"aoSecondaryUV,omitempty"`
	VertexColor         Boolish                         `json:"vertexColor,omitempty"`
	VertexColorsRGB     Boolish                         `json:"vertexColorsRGB,omitempty"`
	VertexColorAlpha    Boolish                         `json:"vertexColorAlpha,omitempty"`
	TangentOrthogonalize  Boolish                       `json:"tangentOrthogonalize,omitempty"`
	TangentNormalize      Boolish                       `json:"tangentNormalize,omitempty"`
	TangentGenerateBitang Boolish                       `json:"tangentGenerateBitangent,omitempty"`
	ExtrasTexCoordRanges  map[string]TexCoordRangeDesc  `json:"extrasTexCoordRanges,omitempty"`
}

// TexCoordRangeDesc is the scale/bias 4-vector Marmoset applies to a
// specific extra-texture UV channel.
type TexCoordRangeDesc struct {
	ScaleBias [4]float32 `json:"scaleBias"`
}

// FogDesc — distance-based volumetric fog parameters.
type FogDesc struct {
	Opacity    *float32    `json:"opacity,omitempty"`
	Distance   *float32    `json:"distance,omitempty"`
	Dispersion *float32    `json:"dispersion,omitempty"`
	SkyIllum   *float32    `json:"skyIllum,omitempty"`
	LightIllum *float32    `json:"lightIllum,omitempty"`
	Color      *[3]float32 `json:"color,omitempty"`
	FogType    *uint32     `json:"type,omitempty"`
}

// SkyDesc — environment / sky lighting configuration. Marmoset can
// resolve an HDR image URL or fall back to a solid colour + diffuse
// coefficients pre-baked for the runtime IBL.
type SkyDesc struct {
	ImageURL             *string   `json:"imageURL,omitempty"`
	BackgroundBrightness *float32  `json:"backgroundBrightness,omitempty"`
	BackgroundColor      []float32 `json:"backgroundColor,omitempty"`
	BackgroundMode       *uint32   `json:"backgroundMode,omitempty"`
	DiffuseCoefficients  []float32 `json:"diffuseCoefficients,omitempty"`
}

// ShadowFloorDesc — the soft ground-shadow plane Marmoset draws
// under the model.
type ShadowFloorDesc struct {
	Simple    Boolish      `json:"simple,omitempty"`
	Alpha     *float32     `json:"alpha,omitempty"`
	EdgeFade  Boolish      `json:"edgeFade,omitempty"`
	Transform *[16]float32 `json:"transform,omitempty"`
}

// AnimData — the top-level animation table. Rigs + clips + per-clip
// animated-object lists are children of this.
type AnimData struct {
	HasAnimData      Boolish            `json:"hasAnimData,omitempty"`
	NumAnimations    *int               `json:"numAnimations,omitempty"`
	NumMatrices      int                `json:"numMatrices"`
	NumSkinningRigs  *int               `json:"numSkinningRigs,omitempty"`
	SelectedAnim     *int               `json:"selectedAnimation,omitempty"`
	SelectedCamera   *int               `json:"selectedCamera,omitempty"`
	SceneScale       float32            `json:"sceneScale"`
	ShowPlayControls Boolish            `json:"showPlayControls,omitempty"`
	AutoPlayAnims    Boolish            `json:"autoPlayAnims,omitempty"`
	MeshIDs          []PartIndexRef     `json:"meshIDs,omitempty"`
	LightIDs         []PartIndexRef     `json:"lightIDs,omitempty"`
	MaterialIDs      []PartIndexRef     `json:"materialIDs,omitempty"`
	SkinningRigs     []SkinningRigDesc  `json:"skinningRigs,omitempty"`
	Animations       []AnimationDesc    `json:"animations,omitempty"`
}

// PartIndexRef is the partIndex pointer the AnimData runs through to
// resolve a mesh / light / material it animates. Stored as an object
// in the JSON for forward-compat with extra metadata.
type PartIndexRef struct {
	PartIndex int `json:"partIndex"`
}

// SkinningRigDesc — pointer to a SkinRig*.dat blob inside the
// archive. The skinning decoder (a later commit) reads that blob.
type SkinningRigDesc struct {
	File string `json:"file"`
}

// AnimationDesc — one clip with its length, fps, and the animated
// objects (per-part keyframe blobs) it drives.
type AnimationDesc struct {
	Name               string                `json:"name"`
	Length             float32               `json:"length"`
	OriginalFPS        float32               `json:"originalFPS"`
	TotalFrames        int                   `json:"totalFrames"`
	NumAnimatedObjects int                   `json:"numAnimatedObjects"`
	AnimatedObjects    []AnimatedObjectDesc  `json:"animatedObjects,omitempty"`
}

// AnimatedObjectDesc — one mesh / light / material's keyframe blob
// inside a clip.
type AnimatedObjectDesc struct {
	PartName              string                  `json:"partName"`
	SceneObjectType       string                  `json:"sceneObjectType"`
	SkinningRigIndex      int                     `json:"skinningRigIndex"`
	ModelPartIndex        int                     `json:"modelPartIndex"`
	ModelPartFPS          float32                 `json:"modelPartFPS"`
	ModelPartScale        float32                 `json:"modelPartScale"`
	ParentIndex           int                     `json:"parentIndex"`
	StartTime             float32                 `json:"startTime"`
	EndTime               float32                 `json:"endTime"`
	TotalFrames           int                     `json:"totalFrames"`
	File                  string                  `json:"file"`
	NumAnimatedProperties *int                    `json:"numAnimatedProperties,omitempty"`
	AnimatedProperties    []AnimatedPropertyDesc  `json:"animatedProperties,omitempty"`
	PivotX                *float32                `json:"pivotx,omitempty"`
	PivotY                *float32                `json:"pivoty,omitempty"`
	PivotZ                *float32                `json:"pivotz,omitempty"`
}

// AnimatedPropertyDesc — name of a property animated within an
// AnimatedObject.
type AnimatedPropertyDesc struct {
	Name string `json:"name"`
}

// ParseScene unmarshals the scene.json bytes into a Scene. Same
// fallback as the Rust crate: if strict UTF-8 parsing fails it tries
// again with bytes-to-string lossy conversion, which catches the
// rare case where Toolbag emits a stray invalid byte inside a string
// field.
func ParseScene(data []byte) (*Scene, error) {
	var s Scene
	if err := json.Unmarshal(data, &s); err != nil {
		// Lossy fallback: re-encode bytes as a UTF-8 string with the
		// standard replacement character, then re-parse. Mirrors the
		// behaviour in src/scene.rs's Scene::from_bytes.
		cleaned := strings.ToValidUTF8(string(data), "\uFFFD")
		if err2 := json.Unmarshal([]byte(cleaned), &s); err2 != nil {
			return nil, fmt.Errorf("scene: %w (lossy retry: %v)", err, err2)
		}
	}
	return &s, nil
}

// Boolish is a JSON-flexible bool. Marmoset Toolbag serialises some
// boolean settings as `true`/`false`, others as `1`/`0`, and a few
// (older exports) as string literals like "true"/"0". The runtime
// coerces all of these to a single bool at load time; we mirror that.
//
// Absent fields decode as the zero value (false). Treat the field
// like a normal bool once Unmarshal returns.
type Boolish bool

// UnmarshalJSON accepts: true, false, "true", "false", "1", "0",
// 1, 0, 1.0, 0.0. Anything else decodes as false.
func (b *Boolish) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*b = false
		return nil
	}
	// Fast path for the common literal cases.
	switch string(data) {
	case "true":
		*b = true
		return nil
	case "false":
		*b = false
		return nil
	}
	// Numeric: accept any number that parses to non-zero as true.
	if data[0] != '"' {
		if f, err := strconv.ParseFloat(string(data), 64); err == nil {
			*b = Boolish(f != 0)
			return nil
		}
	}
	// String: trim + lowercase, then check the same vocabulary as the
	// Rust crate's deserialize_boolish_option.
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		*b = false
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1":
		*b = true
	case "false", "0":
		*b = false
	default:
		*b = false
	}
	return nil
}

// Bool returns the underlying primitive bool for ergonomic use at
// call sites that want a plain `bool`.
func (b Boolish) Bool() bool { return bool(b) }
