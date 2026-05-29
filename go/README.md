# mviewer/go

A native Go decoder for the Marmoset `.mview` archive format.

Initially a port of the Rust crate at the root of this repository, the
Go module has grown into a friendlier API surface: streaming reader
interfaces, `context.Context` cancellation, functional options,
memory-bomb guards, native fuzz coverage, and a small CLI. The
library is fine to vendor on its own — the Rust crate isn't in the
import path.

## Status

| Capability                                | Status     |
|-------------------------------------------|------------|
| Archive parser (named entries)            | ✅          |
| LZW decompression (12-bit, 4k dict)       | ✅          |
| `scene.json` parser                       | ✅          |
| Mesh decoder (positions, UVs, normals)    | ✅          |
| Material / texture extraction             | ✅          |
| Channel merging (albedo+α, refl+gloss→MR) | ✅          |
| glTF / GLB writer                         | ✅          |
| `KHR_mesh_quantization` extension         | ✅          |
| `context.Context` cancellation everywhere | ✅          |
| Concurrent mesh + material decode         | ✅          |
| Memory-bomb caps (entry + aggregate)      | ✅          |
| `Validate()` / `IsMview()` sniffers       | ✅          |
| `fs.FS` integration                       | ✅          |
| Native fuzz tests + benchmarks            | ✅          |
| Standalone CLI (`cmd/mview`)              | ✅          |
| Skinning + animation                      | ✅ baked-TRS path |

## Install

```
go get github.com/mscrnt/mviewer/go
go install github.com/mscrnt/mviewer/go/cmd/mview@latest
```

## Library

```go
import mview "github.com/mscrnt/mviewer/go"

// Simple path.
glb, err := mview.ConvertBytesToGLB(in)

// Hardened path for an HTTP handler.
err := mview.ConvertToGLBContext(ctx, body, w,
    mview.WithMaxTotalSize(64<<20),       // refuse > 64 MiB aggregate
    mview.WithMaterialChannelMerge(true), // proper PBR base color + MR
    mview.WithQuantizedMesh(true),        // 6 % smaller GLB
    mview.WithConcurrency(4),             // decode meshes in parallel
)

// Cheap upload-time gate.
if err := mview.Validate(body); err != nil {
    return http.StatusBadRequest
}

// fs.FS integration.
err := mview.ConvertFromFS(ctx, embeddedFS, "models/x.mview", w)
```

## CLI

```
mview convert in.mview out.glb [--merge-channels] [--quantize] [--max-size N]
mview validate in.mview [--max-size N]
mview info in.mview
mview thumb in.mview out.jpg
```

## Why a Go port?

The upstream Rust crate is excellent, but multi-language stacks pay
twice — once to ship the binary, once to learn its CLI. A Go library
imports at the module boundary with no subprocess hop, ports cleanly
into HTTP middleware, fans out across cores via `WithConcurrency`, and
brings native `context.Context` + fuzzing that the upstream doesn't
have.

## Format reference

See [`docs/reverse-engineering/marmoset-js-spec.md`](../docs/reverse-engineering/marmoset-js-spec.md)
at the root of the repo for the binary layout the parsers depend on.

## License

Tracks whatever the upstream `mviewer` repo eventually adopts. The
Go module is otherwise distributed under the same terms as the
artist-alley project that drove its first release.
