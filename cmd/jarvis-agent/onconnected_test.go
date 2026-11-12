package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// The runner has one OnConnected, and the wiring builds it as a chain: each step captures what
// is already there and calls it (or starts its own work beside it). Assigning a bare function
// over it silently drops every earlier step. That happened: `r.OnConnected = sw.Serve` threw
// away wirePackages' ShipTestEvents, so a Forge test install's agent runs, log lines and scans
// never reached jarvisd on any node where voice is supported (Linux and macOS desktops), and
// Send to Forge reported nothing the node did. Only a func literal may be assigned: it has
// somewhere to call the captured previous handler from.
func TestOnConnectedIsAlwaysWrapped(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for i, lhs := range as.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "OnConnected" || i >= len(as.Rhs) {
						continue
					}
					checked++
					if _, ok := as.Rhs[i].(*ast.FuncLit); !ok {
						t.Errorf("%s: OnConnected is assigned a bare function; it must be a func literal "+
							"that calls the handler already wired, or that step is dropped",
							fset.Position(as.Pos()))
					}
				}
				return true
			})
		}
	}
	if checked == 0 {
		t.Fatal("no OnConnected assignment found: the guard is looking in the wrong place")
	}
}
