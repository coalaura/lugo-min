package minifier

import (
	"github.com/coalaura/lugo/ast"
)

const Alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

var luaKeywords = map[string]bool{
	"and": true, "break": true, "do": true, "else": true, "elseif": true,
	"end": true, "false": true, "for": true, "function": true, "goto": true,
	"if": true, "in": true, "local": true, "nil": true, "not": true,
	"or": true, "repeat": true, "return": true, "then": true, "true": true,
	"until": true, "while": true,
}

var standardGlobals = map[string]bool{
	"_G": true, "_VERSION": true, "assert": true, "collectgarbage": true,
	"dofile": true, "error": true, "getmetatable": true, "ipairs": true,
	"next": true, "pairs": true, "pcall": true, "print": true,
	"rawequal": true, "rawget": true, "rawlen": true, "rawset": true,
	"select": true, "setmetatable": true, "tonumber": true, "tostring": true,
	"type": true, "xpcall": true, "require": true, "module": true,
	"package": true, "coroutine": true, "debug": true, "io": true,
	"math": true, "os": true, "string": true, "table": true, "utf8": true,
	"self": true,
}

type LocalSymbol struct {
	Name         string
	MinifiedName string
}

type Scope struct {
	Parent *Scope
	Vars   map[string]*LocalSymbol
}

type Resolver struct {
	Tree               *ast.Tree
	IdentMap           map[ast.NodeID]*LocalSymbol
	ScopeChain         *Scope
	ReferencedGlobals  map[string]bool
	ReservedNames      map[string]bool
	RenameLocals       bool
	NoShadowAllGlobals bool
	NoShadowRefGlobals bool
	collectMode        bool
}

func NewResolver(tree *ast.Tree, renameLocals bool) *Resolver {
	return &Resolver{
		Tree:               tree,
		IdentMap:           make(map[ast.NodeID]*LocalSymbol),
		RenameLocals:       renameLocals,
		ReferencedGlobals:  make(map[string]bool),
		ReservedNames:      make(map[string]bool),
		NoShadowRefGlobals: true,
	}
}

func (r *Resolver) Resolve() {
	if r.RenameLocals && r.NoShadowRefGlobals {
		r.collectMode = true
		r.walk(r.Tree.Root)
		r.collectMode = false

		r.ScopeChain = nil

		clear(r.IdentMap)
	}

	r.walk(r.Tree.Root)
}

func (r *Resolver) pushScope() {
	r.ScopeChain = &Scope{
		Parent: r.ScopeChain,
		Vars:   make(map[string]*LocalSymbol),
	}
}

func (r *Resolver) popScope() {
	if r.ScopeChain != nil {
		r.ScopeChain = r.ScopeChain.Parent
	}
}

func (r *Resolver) declare(identID ast.NodeID) {
	if identID == ast.InvalidNode {
		return
	}

	node := r.Tree.Nodes[identID]

	if node.Kind != ast.KindIdent {
		return
	}

	name := string(r.Tree.Source[node.Start:node.End])

	sym := &LocalSymbol{Name: name}

	if r.RenameLocals && !r.collectMode {
		sym.MinifiedName = r.nextAvailableName()
	} else {
		sym.MinifiedName = name
	}

	r.ScopeChain.Vars[name] = sym
	r.IdentMap[identID] = sym
}

func (r *Resolver) declareName(name string) {
	sym := &LocalSymbol{Name: name}

	if r.RenameLocals && !r.collectMode {
		sym.MinifiedName = r.nextAvailableName()
	} else {
		sym.MinifiedName = name
	}

	r.ScopeChain.Vars[name] = sym
}

func (r *Resolver) lookup(name string) *LocalSymbol {
	for s := r.ScopeChain; s != nil; s = s.Parent {
		if sym, ok := s.Vars[name]; ok {
			return sym
		}
	}

	return nil
}

func (r *Resolver) nextAvailableName() string {
	var idx int

	for {
		name := getMinifiedName(idx)

		idx++

		if luaKeywords[name] {
			continue
		}

		if r.NoShadowAllGlobals && standardGlobals[name] {
			continue
		}

		if r.ReservedNames[name] {
			continue
		}

		if r.NoShadowRefGlobals && r.ReferencedGlobals[name] {
			continue
		}

		var used bool

		for s := r.ScopeChain; s != nil; s = s.Parent {
			for _, sym := range s.Vars {
				if sym.MinifiedName == name {
					used = true

					break
				}
			}

			if used {
				break
			}
		}

		if !used {
			return name
		}
	}
}

func getMinifiedName(index int) string {
	var buf []byte

	for {
		buf = append(buf, Alphabet[index%len(Alphabet)])

		index /= len(Alphabet)
		if index == 0 {
			break
		}
	}

	// reverse back to correct reading order
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}

	return string(buf)
}

func (r *Resolver) walk(nodeID ast.NodeID) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := r.Tree.Nodes[nodeID]

	switch node.Kind {
	case ast.KindFile:
		r.pushScope()
		r.walk(node.Left)
		r.popScope()
	case ast.KindBlock:
		for i := range node.Count {
			r.walk(r.Tree.ExtraList[node.Extra+uint32(i)])
		}
	case ast.KindDo:
		r.pushScope()
		r.walk(node.Left)
		r.popScope()
	case ast.KindWhile:
		r.walk(node.Left) // condition
		r.pushScope()
		r.walk(node.Right) // block
		r.popScope()
	case ast.KindRepeat:
		r.pushScope()
		r.walk(node.Left)  // block
		r.walk(node.Right) // condition
		r.popScope()
	case ast.KindIf:
		r.walk(node.Left) // condition

		r.pushScope()
		r.walk(node.Right) // then block
		r.popScope()

		for i := range node.Count {
			r.walk(r.Tree.ExtraList[node.Extra+uint32(i)])
		}
	case ast.KindElseIf:
		r.walk(node.Left) // condition

		r.pushScope()
		r.walk(node.Right) // block
		r.popScope()
	case ast.KindElse:
		r.pushScope()
		r.walk(node.Left) // block
		r.popScope()
	case ast.KindForNum:
		for i := range node.Count {
			r.walk(r.Tree.ExtraList[node.Extra+uint32(i)])
		}

		r.pushScope()
		r.declare(node.Left) // loop variable
		r.walk(node.Right)
		r.popScope()
	case ast.KindForIn:
		r.walk(ast.NodeID(node.Extra)) // rhs exprlist

		r.pushScope()

		if node.Left != ast.InvalidNode {
			nameListNode := r.Tree.Nodes[node.Left]

			for i := range nameListNode.Count {
				identID := r.Tree.ExtraList[nameListNode.Extra+uint32(i)]

				r.declare(identID)
			}
		}

		r.walk(node.Right)

		r.popScope()
	case ast.KindLocalAssign:
		r.walk(node.Right) // rhs evaluated first in active parent scope

		if node.Left != ast.InvalidNode {
			nameListNode := r.Tree.Nodes[node.Left]

			for i := range nameListNode.Count {
				identID := r.Tree.ExtraList[nameListNode.Extra+uint32(i)]

				r.declare(identID)
			}
		}
	case ast.KindLocalFunction:
		r.declare(node.Left) // declared in outer scope first to support recursion
		r.walk(node.Right)
	case ast.KindFunctionStmt:
		var isMethod bool

		if node.Left != ast.InvalidNode {
			nameNode := r.Tree.Nodes[node.Left]
			if nameNode.Kind == ast.KindMethodName {
				isMethod = true

				r.walk(nameNode.Left) // walk receiver only, not the method name property
			} else {
				r.walk(node.Left)
			}
		}

		r.walkFunctionBody(node.Right, isMethod)
	case ast.KindFunctionExpr:
		r.walkFunctionBody(nodeID, false)
	case ast.KindIdent:
		name := string(r.Tree.Source[node.Start:node.End])

		sym := r.lookup(name)
		if sym != nil {
			r.IdentMap[nodeID] = sym
		} else if r.collectMode {
			r.ReferencedGlobals[name] = true
		}
	case ast.KindRecordField:
		// left is a key identifier (not a variable lookup!). only walk value (right).
		r.walk(node.Right)
	case ast.KindMemberExpr:
		// right is a property identifier. only walk object (left).
		r.walk(node.Left)
	case ast.KindMethodCall:
		// right is a property identifier. walk receiver (left) and arguments.
		r.walk(node.Left)

		for i := range node.Count {
			r.walk(r.Tree.ExtraList[node.Extra+uint32(i)])
		}
	default:
		r.walk(node.Left)
		r.walk(node.Right)

		if node.Count > 0 && node.Kind != ast.KindBlock && node.Kind != ast.KindFile {
			for i := range node.Count {
				r.walk(r.Tree.ExtraList[node.Extra+uint32(i)])
			}
		}
	}
}

func (r *Resolver) walkFunctionBody(funcID ast.NodeID, isMethod bool) {
	if funcID == ast.InvalidNode {
		return
	}

	node := r.Tree.Nodes[funcID]

	r.pushScope()

	if isMethod {
		r.declareName("self") // inject implicit "self" parameter
	}

	for i := range node.Count {
		paramID := r.Tree.ExtraList[node.Extra+uint32(i)]

		r.declare(paramID)
	}

	r.walk(node.Right) // body
	r.popScope()
}
