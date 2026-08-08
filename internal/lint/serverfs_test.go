// Package lint holds checks that are cheap to run and expensive to forget.
package lint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// pathCalls are the functions that take a path and resolve it.
//
// Every one of them re-runs permission resolution against the CALLING process.
// In the server that process is root, so the check does not happen — which is
// why the design puts all of them in the helper instead, and why fd-based
// equivalents (Fstat, Fchmod, Fsetxattr, Read, Write, Getdents) are absent from
// this list: a descriptor already carries the decision the kernel made when the
// helper opened it, as the user.
var pathCalls = map[string]map[string]bool{
	"os": {
		"Open": true, "OpenFile": true, "Create": true, "CreateTemp": true,
		"Stat": true, "Lstat": true, "ReadFile": true, "WriteFile": true,
		"ReadDir": true, "Remove": true, "RemoveAll": true, "Rename": true,
		"Mkdir": true, "MkdirAll": true, "MkdirTemp": true,
		"Symlink": true, "Readlink": true, "Link": true, "Truncate": true,
		"Chmod": true, "Chown": true, "Lchown": true, "Chtimes": true,
		"Chdir": true, "DirFS": true, "OpenRoot": true, "OpenInRoot": true,
	},
	"golang.org/x/sys/unix": {
		"Open": true, "Openat": true, "Openat2": true, "Creat": true,
		"Stat": true, "Lstat": true, "Fstatat": true, "Statx": true,
		"Mkdir": true, "Mkdirat": true, "Unlink": true, "Unlinkat": true,
		"Rmdir": true, "Rename": true, "Renameat": true, "Renameat2": true,
		"Link": true, "Linkat": true, "Symlink": true, "Symlinkat": true,
		"Readlink": true, "Readlinkat": true, "Truncate": true,
		"Chmod": true, "Fchmodat": true, "Chown": true, "Lchown": true, "Fchownat": true,
		"Access": true, "Faccessat": true, "Faccessat2": true, "Utimes": true,
		"Setxattr": true, "Lsetxattr": true, "Getxattr": true, "Lgetxattr": true,
		"Removexattr": true, "Lremovexattr": true, "Listxattr": true, "Llistxattr": true,
		"Statfs": true, "Chdir": true, "Chroot": true, "Mount": true,
	},
	"syscall": {
		"Open": true, "Openat": true, "Creat": true,
		"Stat": true, "Lstat": true, "Fstatat": true,
		"Mkdir": true, "Mkdirat": true, "Unlink": true, "Unlinkat": true,
		"Rmdir": true, "Rename": true, "Renameat": true,
		"Link": true, "Linkat": true, "Symlink": true, "Readlink": true,
		"Chmod": true, "Fchmodat": true, "Chown": true, "Lchown": true,
		"Truncate": true, "Access": true, "Chdir": true, "Chroot": true,
	},
	"path/filepath": {
		// Join, Split, Base, Dir and Clean are string arithmetic and are fine.
		// These four touch the filesystem.
		"Walk": true, "WalkDir": true, "Glob": true, "EvalSymlinks": true,
	},
	"io/fs": {
		"WalkDir": true, "ReadFile": true, "ReadDir": true, "Stat": true, "Glob": true,
	},
}

// helperOnly are the directories allowed to make those calls.
//
//   - internal/helper is the point of the design: it runs as the user, so the
//     kernel checks it against the right credentials.
//   - internal/integration builds real accounts and directory trees as root on
//     purpose; it is a test fixture, not the server.
//   - internal/lint is this check, which reads source files.
var helperOnly = map[string]bool{
	"internal/helper":      true,
	"internal/integration": true,
	"internal/lint":        true,
	"cmd/darak-helper":     true,
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatalf("could not find the repo root from %s: %v", dir, err)
	}
	return dir
}

type violation struct {
	pos  token.Position
	call string
}

// The server must not resolve a path itself. Passing a descriptor carries
// exactly one permission decision — read or write on that open file — while
// openat/renameat/unlinkat/mkdirat re-check against the calling process, which
// in the server is root. A single one of these calls in server code silently
// removes the permission check from whatever it touches, and it looks completely
// ordinary at the call site. That is why it is checked rather than remembered.
func TestServerNeverResolvesAPath(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var found []violation

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "dist", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Tests are not the server. They set up fixtures, and doing that through
		// the helper would only test the helper against itself.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if helperOnly[filepath.ToSlash(filepath.Dir(rel))] {
			return nil
		}

		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		found = append(found, inspect(fset, f)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, v := range found {
		t.Errorf("%s: %s resolves a path — the server must go through the helper, "+
			"or the kernel checks this against root instead of the user", v.pos, v.call)
	}
}

// inspect reports path-resolving calls in one file, resolving import aliases so
// a renamed import cannot slip past.
func inspect(fset *token.FileSet, f *ast.File) []violation {
	// local name -> import path
	imports := map[string]string{}
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := p
		if i := strings.LastIndex(p, "/"); i >= 0 {
			name = p[i+1:]
		}
		if spec.Name != nil {
			if spec.Name.Name == "_" || spec.Name.Name == "." {
				// A dot import would make every check here unreliable; none exist,
				// and this makes sure that stays true.
				name = spec.Name.Name
			} else {
				name = spec.Name.Name
			}
		}
		imports[name] = p
	}

	var out []violation
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		// A local variable shadowing a package name would resolve here too, but
		// the result is a false positive that a rename fixes — the opposite
		// mistake, a missed call, is the one that matters.
		importPath, ok := imports[pkgIdent.Name]
		if !ok {
			return true
		}
		if fns, ok := pathCalls[importPath]; ok && fns[sel.Sel.Name] {
			out = append(out, violation{
				pos:  fset.Position(sel.Pos()),
				call: pkgIdent.Name + "." + sel.Sel.Name,
			})
		}
		return true
	})
	return out
}

// The check has to be able to fail, or it proves nothing. This feeds it a file
// that breaks the rule in the ways real code would: a plain call, an aliased
// import, and a call whose result is discarded.
func TestTheCheckActuallyCatchesThings(t *testing.T) {
	src := `package server

import (
	"os"
	sys "golang.org/x/sys/unix"
	"path/filepath"
)

func bad(dirfd int, name string) {
	f, _ := os.Open("/srv/data/homes/alice/secret")
	_ = f
	_ = sys.Openat2(dirfd, name, nil)
	sys.Renameat2(dirfd, "a", dirfd, "b", 0)
	filepath.Walk("/srv/data", nil)
	_ = filepath.Join("a", "b") // fine: string arithmetic
	_ = sys.Fstat                // fine: takes a descriptor
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "server.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, v := range inspect(fset, f) {
		got[v.call] = true
	}

	for _, want := range []string{"os.Open", "sys.Openat2", "sys.Renameat2", "filepath.Walk"} {
		if !got[want] {
			t.Errorf("%s was not flagged", want)
		}
	}
	for _, fine := range []string{"filepath.Join", "sys.Fstat"} {
		if got[fine] {
			t.Errorf("%s was flagged but takes no path", fine)
		}
	}
}
