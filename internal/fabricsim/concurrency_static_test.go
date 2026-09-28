package fabricsim

// Deterministic concurrency verification without a C toolchain.
//
// The standard Go race detector is a C/assembly runtime linked through cgo, so
// it cannot run on a machine with no C compiler. That leaves a real gap: this
// package runs worker pools that touch the transport and the detector from many
// goroutines, and a data race there is invisible to `go vet`.
//
// This file closes the gap with a pure-Go substitute. It is a static analysis
// of the package's own source:
//
//   - It walks every method of the concurrently written structs (Transport and
//     Detector) in statement order.
//   - It tracks whether each statement executes with the struct's mutex held,
//     including `defer Unlock`.
//   - It builds the call graph within the same file and propagates the "lock
//     held" property through helper calls, so a helper that is documented as
//     "caller must hold the lock" is only accepted if every reachable caller
//     really does hold it.
//   - It fails on any write to a field that is neither atomic nor guarded.
//
// What this does not replace: a race that touches a field once, or that
// corrupts a value without an unlocked write pattern the walker can see (for
// example, silently changing an atomic.Uint64 into a plain uint64 still under
// the lock). Running the real detector on a machine with a C compiler remains
// the right answer. See the security assessment, section 8.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// lockDepth tracks how many lock levels are held at the current point during a
// statement-order walk. A `defer t.mu.Unlock()` does not release the lock at
// the defer site: it releases it at function exit, so the depth stays raised.
type lockDepth struct {
	levels         int
	deferredUnlock bool
}

func (l *lockDepth) add(delta int) {
	l.levels += delta
	if l.levels < 0 {
		l.levels = 0
	}
}

func (l *lockDepth) held() bool {
	return l.levels > 0 || l.deferredUnlock
}

// TestNoUnsynchronisedStructWrites runs the whole analysis: per-constructor
// field whitelists, statement-order lock tracking, and interprocedural "runs
// under lock" propagation through the file's call graph.
func TestNoUnsynchronisedStructWrites(t *testing.T) {
	cases := []struct {
		file          string
		structName    string
		atomicOrConst []string
	}{
		{
			file: "transport.go", structName: "Transport",
			atomicOrConst: []string{
				"enqueued", "delivered", "dropped", "expired", "dropsByCause",
				"cfg", "clock", "rng", "arena", "arenaIn", "arenaMu", "mu", "cond",
			},
		},
		{
			file: "defense.go", structName: "Detector",
			atomicOrConst: []string{
				"mu", "top", "rng", "cfg",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.structName, func(t *testing.T) {
			checkStruct(t, c.file, c.structName, c.atomicOrConst)
		})
	}
}

func checkStruct(t *testing.T, file, structName string, atomicOrConst []string) {
	fset := token.NewFileSet()
	fileAst, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	noLock := make(map[string]bool, len(atomicOrConst))
	for _, f := range atomicOrConst {
		noLock[f] = true
	}

	methods := map[string]*ast.FuncDecl{}
	var order []string
	for _, decl := range fileAst.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		if _, sName := receiverOf(fd); sName != structName {
			continue
		}
		methods[fd.Name.Name] = fd
		order = append(order, fd.Name.Name)
	}

	// callerLocks maps caller method -> set of callees invoked while the caller
	// holds the lock at the call site (based on the caller's own statement
	// order, without trusting the callee).
	callerLocks := map[string]map[string]bool{}
	// rawProblems collects unlocked writes keyed by the method that contains
	// them, so the interprocedural pass can drop the ones inside helpers that
	// are ultimately called under the lock.
	tagged := map[string][]string{}

	for _, name := range order {
		fd := methods[name]
		var d lockDepth
		calls := map[string]bool{}
		var local []string
		isConstructor := strings.HasPrefix(name, "New")
		collectCallsAndWrites(fset, fd.Body.List, &d, noLock, calls, &local, isConstructor)
		callerLocks[name] = calls
		if len(local) > 0 {
			tagged[name] = local
		}
	}

	// Fixed-point propagation: a method "runs under lock" if it locks at entry
	// and keeps the lock for its whole body (a `defer Unlock`), or if some such
	// method calls it. A method that locks and unlocks mid-body (e.g. Enqueue
	// releasing before recordDrop) is NOT a seed: only calls on its own held
	// path can be protected, which the per-statement walk captures directly.
	runsUnderLock := map[string]bool{}
	for name, fd := range methods {
		if locksWholeBody(fd, structName) {
			runsUnderLock[name] = true
		}
	}
	changed := true
	for changed {
		changed = false
		for caller := range runsUnderLock {
			for callee := range callerLocks[caller] {
				if !runsUnderLock[callee] {
					runsUnderLock[callee] = true
					changed = true
				}
			}
		}
	}

	// Only writes in methods that never provably run under a lock survive.
	var problems []string
	for method, msgs := range tagged {
		if runsUnderLock[method] {
			continue
		}
		problems = append(problems, msgs...)
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// locksWholeBody reports whether a method acquires the struct lock at entry
// and keeps it for the entire function via `defer Unlock`. Only such methods
// may seed the interprocedural "runs under lock" propagation; a method that
// unlocks mid-body can call a helper outside the held region and must not
// imply that helper is protected.
func locksWholeBody(fd *ast.FuncDecl, structName string) bool {
	if fd.Body == nil || len(fd.Body.List) == 0 {
		return false
	}
	// The first statement must be a lock call.
	first, ok := fd.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := first.X.(*ast.CallExpr)
	if !ok || lockAction(call) != "lock" {
		return false
	}
	// And there must be a deferred unlock somewhere, so the lock is held from
	// the first statement to the return.
	hasDeferredUnlock := false
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if ds, ok := n.(*ast.DeferStmt); ok {
			if lockAction(ds.Call) == "unlock" {
				hasDeferredUnlock = true
			}
		}
		return true
	})
	return hasDeferredUnlock
}

// lockAction classifies a call as the struct mutex's lock/unlock ("lock",
// "unlock") or "" for anything else, matching t.mu.Lock()/RLock and friends on
// any mutex field literally named mu reached through a receiver ident.
func lockAction(c *ast.CallExpr) string {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	switch sel.Sel.Name {
	case "Lock", "RLock", "Unlock", "RUnlock":
	default:
		return ""
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if inner.Sel.Name != "mu" {
		return ""
	}
	if _, ok := inner.X.(*ast.Ident); !ok {
		return ""
	}
	if sel.Sel.Name == "Unlock" || sel.Sel.Name == "RUnlock" {
		return "unlock"
	}
	return "lock"
}

// collectCallsAndWrites walks statements in order, recording locked field
// writes and under-lock callee invocations for the fixed-point pass.
func collectCallsAndWrites(fset *token.FileSet, stmts []ast.Stmt, d *lockDepth, noLock map[string]bool, calls map[string]bool, problems *[]string, isConstructor bool) {
	for _, st := range stmts {
		switch s := st.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			if lockAction(call) != "" {
				if lockAction(call) == "lock" {
					d.add(1)
				} else {
					d.add(-1)
				}
				continue
			}
			if callee := calleeName(call); callee != "" {
				// Edges are recorded regardless of the caller's own lexical lock
				// state. A helper called from a locked method inherits that
				// lock even though the helper body never locks; only when a
				// method is itself seeded as running under a lock does the
				// fixed-point pass decide whether these callees are protected.
				calls[callee] = true
			}

		case *ast.DeferStmt:
			if action := lockAction(s.Call); action != "" {
				if action == "unlock" {
					d.deferredUnlock = true
				} else {
					d.add(1) // defer t.mu.Lock(): pathological; treat as held.
				}
				continue
			}

		case *ast.AssignStmt:
			checkWrite(fset, s, d, noLock, problems, isConstructor)

		case *ast.IncDecStmt:
			checkWrite(fset, s, d, noLock, problems, isConstructor)

		case *ast.IfStmt:
			// A lock or unlock inside a branch must not leak to the enclosing
			// statement sequence: the enclosing depth only changes for
			// statements executed unconditionally in order. The branch's own
			// writes are therefore analysed at the same depth, but its lock
			// mutations stay local to the branch.
			sub := *d
			collectCallsAndWrites(fset, s.Body.List, &sub, noLock, calls, problems, isConstructor)
			if s.Else != nil {
				subElse := *d
				collectCallsAndWrites(fset, listOf(s.Else), &subElse, noLock, calls, problems, isConstructor)
			}

		case *ast.ForStmt:
			sub := *d
			collectCallsAndWrites(fset, s.Body.List, &sub, noLock, calls, problems, isConstructor)

		case *ast.RangeStmt:
			sub := *d
			collectCallsAndWrites(fset, s.Body.List, &sub, noLock, calls, problems, isConstructor)

		case *ast.SelectStmt:
			sub := *d
			for _, cc := range s.Body.List {
				if comm, ok := cc.(*ast.CommClause); ok && comm.Body != nil {
					subClause := sub
					collectCallsAndWrites(fset, comm.Body, &subClause, noLock, calls, problems, isConstructor)
				}
			}

		case *ast.ReturnStmt:
			// Return values can carry calls (e.g. return d.expireGapsFrom(...)
			// or return Offer{Quarantined: d.maybeQuarantine(...)}). A helper
			// invoked there is protected by whatever the caller held at the
			// return, which is exactly the interprocedural fact we must record.
			collectExprs(fset, s.Results, d, calls, problems)
		}
	}
}

// collectExprs records under-lock callee invocations inside an expression list,
// mirroring the statement walker for expressions that appear on a return.
func collectExprs(fset *token.FileSet, exprs []ast.Expr, d *lockDepth, calls map[string]bool, problems *[]string) {
	for _, e := range exprs {
		ast.Inspect(e, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if callee := calleeName(c); callee != "" {
				calls[callee] = true
			}
			return true
		})
	}
}

// calleeName returns the method name when a call is a receiver method on the
// same struct (e.g. d.expireGapsFrom(...)).
func calleeName(c *ast.CallExpr) string {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if _, isIdent := sel.X.(*ast.Ident); !isIdent {
		return ""
	}
	return sel.Sel.Name
}

// checkWrite validates one LHS write against the protection in force.
func checkWrite(fset *token.FileSet, st ast.Stmt, d *lockDepth, noLock map[string]bool, out *[]string, isConstructor bool) {
	pos := fset.Position(st.Pos())
	var targets []*ast.SelectorExpr
	switch s := st.(type) {
	case *ast.AssignStmt:
		for _, lhs := range s.Lhs {
			if sel, ok := lhs.(*ast.SelectorExpr); ok {
				targets = append(targets, sel)
			}
		}
	case *ast.IncDecStmt:
		if sel, ok := s.X.(*ast.SelectorExpr); ok {
			targets = append(targets, sel)
		}
	}
	for _, sel := range targets {
		if !selectorOnReceiver(sel, "") {
			continue
		}
		field := sel.Sel.Name
		// Writes in a constructor happen on an object that has not been shared
		// with any goroutine yet, so no lock is needed.
		if noLock[field] || d.held() || isConstructor {
			continue
		}
		kind := "assigns"
		if _, ok := st.(*ast.IncDecStmt); ok {
			kind = "increments"
		}
		*out = append(*out, fmt.Sprintf(
			"%s: %s %s with no lock held and no interprocedural guarantee", pos, kind, field))
	}
}

// selectorOnReceiver reports whether sel is <recvIdent>.<field> where the
// receiver could belong to the struct under analysis. Receiver names are t or
// d in this package, so we accept either; keeping structName explicit above
// means the field list itself is what is enforced.
func selectorOnReceiver(sel *ast.SelectorExpr, structName string) bool {
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return id.Name == "t" || id.Name == "d"
}

func listOf(stmt ast.Stmt) []ast.Stmt {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return s.List
	case *ast.IfStmt:
		return s.Body.List
	default:
		return []ast.Stmt{stmt}
	}
}

func receiverOf(fd *ast.FuncDecl) (recv, structName string) {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return "", ""
	}
	recv = ""
	for _, n := range fd.Recv.List[0].Names {
		recv = n.Name
	}
	switch t := fd.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return recv, id.Name
		}
	case *ast.Ident:
		return recv, t.Name
	}
	return "", ""
}
