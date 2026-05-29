// Command mview is a thin CLI over the github.com/mscrnt/mviewer/go
// library. It exists to give the library a verification tool and
// makes one-off .mview → .glb conversion possible from any shell
// without a Go runtime in the loop.
//
//	mview convert in.mview out.glb [--merge-channels] [--quantize]
//	mview validate in.mview
//	mview info in.mview            (list entries + sizes)
//	mview thumb in.mview out.jpg
//
// Build:
//	go install github.com/mscrnt/mviewer/go/cmd/mview@latest
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"syscall"

	mview "github.com/mscrnt/mviewer/go"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "convert":
		err = runConvert(ctx, args)
	case "validate":
		err = runValidate(ctx, args)
	case "info":
		err = runInfo(ctx, args)
	case "thumb":
		err = runThumb(ctx, args)
	case "version":
		fmt.Println("mview (github.com/mscrnt/mviewer/go)")
		return
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mview:", err)
		os.Exit(exitCode(err))
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: mview <command> [options]

Commands:
  convert <in.mview> <out.glb> [--merge-channels] [--quantize] [--max-size N]
        Convert a .mview archive to glTF binary (GLB).
  validate <in.mview> [--max-size N]
        Walk an archive end-to-end and confirm it's structurally sound.
        Exits 0 on success, 1 on a parse/structure failure, 2 on usage error.
  info <in.mview>
        List every entry with its compressed + uncompressed sizes.
  thumb <in.mview> <out.jpg>
        Extract the embedded thumbnail.jpg.
  version
        Print the build identifier.
`)
}

func runConvert(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("convert", flag.ExitOnError)
	merge := fs.Bool("merge-channels", false, "merge albedo+alpha and reflectivity+gloss textures")
	quantize := fs.Bool("quantize", false, "emit KHR_mesh_quantization (smaller GLB)")
	concurrency := fs.Int("j", 0, "decode concurrency (0 = GOMAXPROCS)")
	maxSize := fs.Int64("max-size", 0, "cap on aggregate decompressed payload in bytes")
	_ = fs.Parse(args)
	if fs.NArg() != 2 {
		return errUsage("convert: need <in.mview> <out.glb>")
	}
	in, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(fs.Arg(1))
	if err != nil {
		return err
	}
	defer out.Close()

	opts := []mview.Option{
		mview.WithMaterialChannelMerge(*merge),
		mview.WithQuantizedMesh(*quantize),
		mview.WithConcurrency(*concurrency),
	}
	if *maxSize > 0 {
		opts = append(opts, mview.WithMaxTotalSize(*maxSize))
	}
	return mview.ConvertToGLBContext(ctx, in, out, opts...)
}

func runValidate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	maxSize := fs.Int64("max-size", 0, "cap on aggregate decompressed payload in bytes")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return errUsage("validate: need <in.mview>")
	}
	in, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer in.Close()
	var opts []mview.Option
	if *maxSize > 0 {
		opts = append(opts, mview.WithMaxTotalSize(*maxSize))
	}
	if err := mview.ValidateContext(ctx, in, opts...); err != nil {
		return err
	}
	fmt.Println("ok")
	return nil
}

func runInfo(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errUsage("info: need <in.mview>")
	}
	in, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer in.Close()
	entries, err := mview.ReadAllContext(ctx, in)
	if err != nil {
		return err
	}
	type row struct {
		Name, Type             string
		Compressed, Decoded    uint32
		Compressed64, Decoded64 int64
	}
	rows := make([]row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, row{
			Name: e.Name, Type: e.Type,
			Compressed: e.CompressedSize, Decoded: e.UncompressedSize,
			Compressed64: int64(e.CompressedSize), Decoded64: int64(e.UncompressedSize),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	fmt.Printf("%-30s %-24s %12s %12s\n", "name", "type", "compressed", "decoded")
	for _, r := range rows {
		fmt.Printf("%-30s %-24s %12d %12d\n", trim(r.Name, 30), trim(r.Type, 24), r.Compressed64, r.Decoded64)
	}
	return nil
}

func runThumb(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return errUsage("thumb: need <in.mview> <out.jpg>")
	}
	in, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer in.Close()
	thumb, err := mview.ExtractThumbnail(in)
	if err != nil {
		return err
	}
	out, err := os.Create(args[1])
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, ioReaderOf(thumb))
	return err
}

func ioReaderOf(b []byte) io.Reader {
	return &byteReader{b: b}
}

type byteReader struct {
	b []byte
	i int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

func trim(s string, w int) string {
	if len(s) <= w {
		return s
	}
	return s[:w-1] + "…"
}

type usageErr struct{ msg string }

func (e *usageErr) Error() string { return e.msg }

func errUsage(msg string) error { return &usageErr{msg: msg} }

func exitCode(err error) int {
	var ue *usageErr
	if errors.As(err, &ue) {
		return 2
	}
	return 1
}
