# mviewer/go

A native Go decoder for the Marmoset `.mview` archive format.

This is a port of the Rust crate at the root of the repository,
maintained as a separate Go library so projects that need an
mview → glTF pipeline without spawning a CLI can vendor it directly.

## Status

| Capability                              | Status      |
|-----------------------------------------|-------------|
| Archive parser (named entries)          | ✅ Working  |
| LZW decompression (12-bit, 4k dict)     | ✅ Working  |
| `scene.json` parser                     | ✅ Working  |
| Mesh decoder (positions, UVs, normals)  | 🚧 Planned  |
| Material / texture extraction           | 🚧 Planned  |
| Skinning + animation                    | 🚧 Planned  |
| glTF / GLB writer                       | 🚧 Planned  |

## Why a Go port?

The upstream Rust crate is excellent, but distributing it as a CLI in
a multi-language stack means shelling out to a subprocess and shipping
a ~10 MB binary in the container. Importing a Go library at the module
boundary is friction-free for Go consumers.

## License

Matches the upstream license once it lands. Until then this branch
preserves both implementations for the maintainer's convenience.

## Format reference

See [`docs/reverse-engineering/marmoset-js-spec.md`](../docs/reverse-engineering/marmoset-js-spec.md)
at the root of the repo for the binary layout the parsers depend on.
