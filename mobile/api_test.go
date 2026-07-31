package mobile

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExportedAPIIsGomobileLegal parses this package and checks every exported
// declaration against the subset gobind understands.
//
// It exists because `gomobile bind` needs an Android NDK, so CI cannot run it.
// Without this test, an exported method taking a map or returning two strings
// compiles, vets, tests and passes review, and is discovered only by whoever
// next has an NDK in front of them — with an error message that names a
// generated file rather than the offending signature. The rules encoded here
// are gobind's documented type restrictions, listed in this package's doc.go.
//
// It is deliberately stricter than gobind in one place: it rejects any exported
// struct field, because a field's type is bound too and it is easier to require
// accessors than to reimplement gobind's field rules here.
func TestExportedAPIIsGomobileLegal(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	pkg, ok := pkgs["mobile"]
	if !ok {
		t.Fatalf("package mobile not found in %v", keys(pkgs))
	}

	// Pass one: what types does this package declare, and which are usable as
	// values (interfaces) versus only as pointers (structs)?
	local := map[string]string{} // name -> "struct" | "interface" | "other"
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, s := range gd.Specs {
				ts := s.(*ast.TypeSpec)
				if !ts.Name.IsExported() {
					continue
				}
				switch ts.Type.(type) {
				case *ast.StructType:
					local[ts.Name.Name] = "struct"
				case *ast.InterfaceType:
					local[ts.Name.Name] = "interface"
				default:
					local[ts.Name.Name] = "other"
				}
			}
		}
	}

	c := &apiChecker{t: t, fset: fset, local: local}

	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				c.checkFunc(d)
			case *ast.GenDecl:
				if d.Tok == token.TYPE {
					c.checkTypes(d)
				}
			}
		}
	}

	// A sanity floor: if the parse silently matched nothing, the test would
	// pass while checking zero declarations.
	if c.checked < 20 {
		t.Errorf("only %d exported signatures were checked; the API scan is not finding the package", c.checked)
	}
}

type apiChecker struct {
	t       *testing.T
	fset    *token.FileSet
	local   map[string]string
	checked int
}

func (c *apiChecker) pos(n ast.Node) string { return c.fset.Position(n.Pos()).String() }

func (c *apiChecker) checkTypes(gd *ast.GenDecl) {
	for _, s := range gd.Specs {
		ts := s.(*ast.TypeSpec)
		if !ts.Name.IsExported() {
			continue
		}
		if ts.TypeParams != nil {
			c.t.Errorf("%s: exported type %s is generic; gobind cannot bind generics", c.pos(ts), ts.Name.Name)
		}
		switch tt := ts.Type.(type) {
		case *ast.StructType:
			for _, f := range tt.Fields.List {
				for _, name := range f.Names {
					if name.IsExported() {
						c.t.Errorf("%s: %s.%s is an exported struct field; expose it with an accessor method instead",
							c.pos(name), ts.Name.Name, name.Name)
					}
				}
			}
		case *ast.InterfaceType:
			for _, m := range tt.Methods.List {
				ft, ok := m.Type.(*ast.FuncType)
				if !ok {
					continue // embedded interface; nothing to check here
				}
				for _, name := range m.Names {
					c.checked++
					c.checkSignature(ts.Name.Name+"."+name.Name, ft, m)
				}
			}
		}
	}
}

func (c *apiChecker) checkFunc(fd *ast.FuncDecl) {
	if !fd.Name.IsExported() {
		return
	}
	name := fd.Name.Name
	if fd.Recv != nil {
		// Only methods on exported types are bound.
		recv := recvTypeName(fd.Recv)
		if _, ok := c.local[recv]; !ok || !ast.IsExported(recv) {
			return
		}
		name = recv + "." + name
	}
	if fd.Type.TypeParams != nil {
		c.t.Errorf("%s: %s is generic; gobind cannot bind generics", c.pos(fd), name)
	}
	c.checked++
	c.checkSignature(name, fd.Type, fd)
}

func (c *apiChecker) checkSignature(name string, ft *ast.FuncType, at ast.Node) {
	if ft.Params != nil {
		for _, p := range ft.Params.List {
			if _, isVariadic := p.Type.(*ast.Ellipsis); isVariadic {
				c.t.Errorf("%s: %s is variadic; gobind cannot bind variadic functions", c.pos(at), name)
				continue
			}
			c.checkType(name+" parameter", p.Type, at)
		}
	}

	if ft.Results == nil {
		return
	}
	n := 0
	for _, r := range ft.Results.List {
		if len(r.Names) == 0 {
			n++
		} else {
			n += len(r.Names)
		}
	}
	switch {
	case n > 2:
		c.t.Errorf("%s: %s returns %d values; gobind allows at most (T, error)", c.pos(at), name, n)
	case n == 2:
		last := ft.Results.List[len(ft.Results.List)-1].Type
		if id, ok := last.(*ast.Ident); !ok || id.Name != "error" {
			c.t.Errorf("%s: %s returns two values and the last is not error", c.pos(at), name)
		}
		c.checkType(name+" result", ft.Results.List[0].Type, at)
	case n == 1:
		c.checkType(name+" result", ft.Results.List[0].Type, at)
	}
}

// basics is every scalar gobind carries. Unsigned integers are absent on
// purpose: gobind has no unsigned types, which is why this package converts the
// core's uint64 byte counts to int64 at the boundary.
var basics = map[string]bool{
	"bool": true, "string": true, "error": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"float32": true, "float64": true,
}

func (c *apiChecker) checkType(what string, e ast.Expr, at ast.Node) {
	switch t := e.(type) {
	case *ast.Ident:
		if basics[t.Name] {
			return
		}
		switch c.local[t.Name] {
		case "interface":
			return
		case "struct":
			c.t.Errorf("%s: %s uses struct type %s by value; gobind binds structs only as pointers",
				c.pos(at), what, t.Name)
		default:
			c.t.Errorf("%s: %s uses unsupported type %s", c.pos(at), what, t.Name)
		}

	case *ast.StarExpr:
		id, ok := t.X.(*ast.Ident)
		if !ok || c.local[id.Name] != "struct" {
			c.t.Errorf("%s: %s is a pointer to something other than a struct declared in this package", c.pos(at), what)
		}

	case *ast.ArrayType:
		id, ok := t.Elt.(*ast.Ident)
		if t.Len != nil || !ok || (id.Name != "byte" && id.Name != "uint8") {
			c.t.Errorf("%s: %s is a slice or array; gobind carries []byte and nothing else", c.pos(at), what)
		}

	case *ast.MapType:
		c.t.Errorf("%s: %s is a map; gobind cannot bind maps", c.pos(at), what)
	case *ast.ChanType:
		c.t.Errorf("%s: %s is a channel; gobind cannot bind channels", c.pos(at), what)
	case *ast.FuncType:
		c.t.Errorf("%s: %s is a func value; use a callback interface instead", c.pos(at), what)
	case *ast.SelectorExpr:
		c.t.Errorf("%s: %s names a type from another package; only types declared here can cross the boundary", c.pos(at), what)
	case *ast.InterfaceType:
		c.t.Errorf("%s: %s is an anonymous interface; declare a named one", c.pos(at), what)
	default:
		c.t.Errorf("%s: %s has a type this check does not recognise (%T)", c.pos(at), what, e)
	}
}

func recvTypeName(fl *ast.FieldList) string {
	if len(fl.List) == 0 {
		return ""
	}
	switch t := fl.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.Ident:
		return t.Name
	}
	return ""
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestPackageHasNoCgo guards a property that is easy to lose and expensive to
// discover: the core is pure Go, which is what let D-3 cross-compile it for
// Android in the first place. A cgo import here would also break the CGO_ENABLED=0
// desktop builds.
func TestPackageHasNoCgo(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Clean(e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == `"C"` {
				t.Errorf("%s imports C; the binding must stay pure Go", e.Name())
			}
		}
	}
}
