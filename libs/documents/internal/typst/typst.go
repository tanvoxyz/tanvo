package typst

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed templates fonts
var assets embed.FS

// Options configure an [Engine].
type Options struct {
	// Binary is the typst executable; "typst" (looked up in PATH) when
	// empty.
	Binary string
	// Dir is the parent of the private directory; the system temporary
	// directory when empty.
	Dir string
	// FontPaths are extra font directories, searched after the embedded
	// fonts (for scripts Inter does not cover, such as CJK). Nil means
	// [SystemFontDirs]; an empty non-nil slice adds none.
	FontPaths []string
	// Logger receives warnings; slog.Default() when nil.
	Logger *slog.Logger
}

// Engine compiles Typst documents.
type Engine struct {
	bin      string
	root     string
	fonts    []string
	families map[string]bool
	logger   *slog.Logger
}

// ErrNotFound reports that the typst executable is missing.
var ErrNotFound = errors.New("typst: executable not found")

// New writes the embedded templates and fonts to a new private directory
// and returns an engine using them. Close removes the directory.
func New(opts Options) (*Engine, error) {
	bin := opts.Binary
	if bin == "" {
		bin = "typst"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	root, err := os.MkdirTemp(opts.Dir, "tanvo-typst-")
	if err != nil {
		return nil, fmt.Errorf("typst: create directory: %w", err)
	}
	if err := writeAssets(root); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	extra := opts.FontPaths
	if extra == nil {
		extra = SystemFontDirs()
	}
	fonts := append([]string{filepath.Join(root, "fonts")}, extra...)
	e := &Engine{bin: path, root: root, fonts: fonts, logger: logger}
	if e.families, err = e.listFamilies(); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	return e, nil
}

// SystemFontDirs returns the operating system's font directories that
// exist: those of fontconfig on Linux (/usr/share/fonts,
// /usr/local/share/fonts, ~/.local/share/fonts, ~/.fonts) and of macOS
// (/System/Library/Fonts, /Library/Fonts, ~/Library/Fonts).
func SystemFontDirs() []string {
	dirs := []string{
		"/usr/share/fonts", "/usr/local/share/fonts",
		"/System/Library/Fonts", "/Library/Fonts",
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs,
			filepath.Join(home, ".local", "share", "fonts"), filepath.Join(home, ".fonts"),
			filepath.Join(home, "Library", "Fonts"))
	}
	var out []string
	for _, d := range dirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			out = append(out, d)
		}
	}
	return out
}

// listFamilies runs typst fonts over the engine's font paths.
func (e *Engine) listFamilies() (map[string]bool, error) {
	args := []string{"fonts", "--ignore-system-fonts", "--ignore-embedded-fonts"}
	for _, f := range e.fonts {
		args = append(args, "--font-path", f)
	}
	cmd := exec.Command(e.bin, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + e.root}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("typst: list fonts: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	families := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.TrimSpace(line); f != "" {
			families[f] = true
		}
	}
	return families, nil
}

// HasFamily reports whether a font family is available to the engine.
func (e *Engine) HasFamily(family string) bool { return e.families[family] }

func writeAssets(root string) error {
	return fs.WalkDir(assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(root, filepath.FromSlash(p))
		if d.IsDir() {
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return fmt.Errorf("typst: create %s: %w", p, err)
			}
			return nil
		}
		b, err := assets.ReadFile(p)
		if err != nil {
			return fmt.Errorf("typst: read %s: %w", p, err)
		}
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return fmt.Errorf("typst: write %s: %w", p, err)
		}
		return nil
	})
}

// Close removes the engine's private directory.
func (e *Engine) Close() error {
	if err := os.RemoveAll(e.root); err != nil {
		return fmt.Errorf("typst: remove directory: %w", err)
	}
	return nil
}

// Job is one compilation.
type Job struct {
	// Template is the template's file name, such as "document.typ".
	Template string
	// Data is the document's JSON data (data.json in the job directory).
	Data []byte
	// Files are further files of the job directory, by name.
	Files map[string][]byte
	// Inputs are further sys.inputs; "dir" is set to the job directory.
	Inputs map[string]string
	// PDFA requests PDF/A-3b output.
	PDFA bool
}

// Result is a compiled document.
type Result struct {
	// PDF is the document.
	PDF []byte
	// PDFA reports whether PDF is PDF/A-3b: false when PDF/A was not
	// requested or could not be met (missing glyphs) and the document was
	// compiled as plain PDF instead.
	PDFA bool
}

// Compile compiles job. When PDF/A fails because a glyph is missing from
// every font, it logs a warning and compiles a plain PDF.
func (e *Engine) Compile(ctx context.Context, job Job) (Result, error) {
	id, err := jobID()
	if err != nil {
		return Result{}, err
	}
	rel := "jobs/" + id
	dir := filepath.Join(e.root, "jobs", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, fmt.Errorf("typst: create job: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.WriteFile(filepath.Join(dir, "data.json"), job.Data, 0o600); err != nil {
		return Result{}, fmt.Errorf("typst: write data: %w", err)
	}
	for name, b := range job.Files {
		if name != filepath.Base(name) || name == "data.json" || name == "out.pdf" {
			return Result{}, fmt.Errorf("typst: invalid job file name %q", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return Result{}, fmt.Errorf("typst: write %s: %w", name, err)
		}
	}
	if job.PDFA {
		out, err := e.run(ctx, dir, rel, job, true)
		if err == nil {
			return Result{PDF: out, PDFA: true}, nil
		}
		var ce *CompileError
		if !errors.As(err, &ce) || !ce.missingGlyph() {
			return Result{}, err
		}
		e.logger.WarnContext(ctx, "typst: PDF/A not met, compiling a plain PDF", slog.String("reason", ce.Stderr))
	}
	out, err := e.run(ctx, dir, rel, job, false)
	if err != nil {
		return Result{}, err
	}
	return Result{PDF: out}, nil
}

// CompileError is a failed typst run.
type CompileError struct {
	// Stderr is typst's diagnostics.
	Stderr string
	// Err is the process error.
	Err error
}

func (e *CompileError) Error() string {
	return "typst: compile: " + strings.TrimSpace(e.Stderr) + ": " + e.Err.Error()
}

func (e *CompileError) Unwrap() error { return e.Err }

func (e *CompileError) missingGlyph() bool {
	return strings.Contains(e.Stderr, "could not be displayed") || strings.Contains(e.Stderr, "missing glyph")
}

func (e *Engine) run(ctx context.Context, dir, rel string, job Job, pdfa bool) ([]byte, error) {
	out := filepath.Join(dir, "out.pdf")
	args := []string{
		"compile",
		"--root", e.root,
		"--ignore-system-fonts", "--ignore-embedded-fonts",
		"--creation-timestamp", "0",
		"--input", "dir=/" + rel,
	}
	for _, f := range e.fonts {
		args = append(args, "--font-path", f)
	}
	for k, v := range job.Inputs {
		args = append(args, "--input", k+"="+v)
	}
	if pdfa {
		args = append(args, "--pdf-standard", "a-3b")
	}
	args = append(args, filepath.Join(e.root, "templates", job.Template), out)
	cmd := exec.CommandContext(ctx, e.bin, args...)
	cmd.Dir = e.root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + e.root, "TYPST_PACKAGE_CACHE_PATH=" + filepath.Join(e.root, "packages")}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("typst: compile: %w", ctx.Err())
		}
		return nil, &CompileError{Stderr: stderr.String(), Err: err}
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("typst: read output: %w", err)
	}
	return b, nil
}

func jobID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("typst: job id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
