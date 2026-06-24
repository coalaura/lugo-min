package minifier

import (
	"github.com/coalaura/lugo/ast"
	"github.com/coalaura/lugo/utils"
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

var minifiedNamesCache [1024]string

type LocalSymbol struct {
	Name         string
	MinifiedName string
}

type LocalSymbolEntry struct {
	Name string
	Sym  *LocalSymbol
}

type Resolver struct {
	Tree               *ast.Tree
	IdentMap           map[ast.NodeID]*LocalSymbol
	ReferencedGlobals  map[string]bool
	ReservedNames      map[string]bool
	RenameLocals       bool
	NoShadowAllGlobals bool
	NoShadowRefGlobals bool
	collectMode        bool

	scopes     []int
	vars       []LocalSymbolEntry
	activeUsed map[string]uint32
}

func NewResolver(tree *ast.Tree, renameLocals bool) *Resolver {
	return &Resolver{
		Tree:               tree,
		IdentMap:           make(map[ast.NodeID]*LocalSymbol),
		RenameLocals:       renameLocals,
		ReferencedGlobals:  make(map[string]bool),
		ReservedNames:      make(map[string]bool),
		NoShadowRefGlobals: true,
		activeUsed:         make(map[string]uint32),
		vars:               make([]LocalSymbolEntry, 0, 128),
		scopes:             make([]int, 0, 32),
	}
}

func (r *Resolver) Resolve() {
	if r.RenameLocals && r.NoShadowRefGlobals {
		r.collectMode = true
		r.walk(r.Tree.Root)
		r.collectMode = false

		r.scopes = r.scopes[:0]
		r.vars = r.vars[:0]
		clear(r.activeUsed)

		clear(r.IdentMap)
	}

	r.walk(r.Tree.Root)
}

func (r *Resolver) pushScope() {
	r.scopes = append(r.scopes, len(r.vars))
}

func (r *Resolver) popScope() {
	startIdx := r.scopes[len(r.scopes)-1]
	r.scopes = r.scopes[:len(r.scopes)-1]

	for i := startIdx; i < len(r.vars); i++ {
		sym := r.vars[i].Sym
		r.activeUsed[sym.MinifiedName]--
	}
	r.vars = r.vars[:startIdx]
}

func (r *Resolver) declare(identID ast.NodeID) {
	if identID == ast.InvalidNode {
		return
	}

	node := r.Tree.Nodes[identID]

	if node.Kind != ast.KindIdent {
		return
	}

	name := utils.String(r.Tree.Source[node.Start:node.End])

	sym := &LocalSymbol{Name: name}

	if r.RenameLocals && !r.collectMode {
		sym.MinifiedName = r.nextAvailableName()
	} else {
		sym.MinifiedName = name
	}

	r.vars = append(r.vars, LocalSymbolEntry{Name: name, Sym: sym})
	r.activeUsed[sym.MinifiedName]++
	r.IdentMap[identID] = sym
}

func (r *Resolver) declareName(name string) {
	sym := &LocalSymbol{Name: name}

	if r.RenameLocals && !r.collectMode {
		sym.MinifiedName = r.nextAvailableName()
	} else {
		sym.MinifiedName = name
	}

	r.vars = append(r.vars, LocalSymbolEntry{Name: name, Sym: sym})
	r.activeUsed[sym.MinifiedName]++
}

func (r *Resolver) lookup(name string) *LocalSymbol {
	for i := len(r.vars) - 1; i >= 0; i-- {
		if r.vars[i].Name == name {
			return r.vars[i].Sym
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

		if r.activeUsed[name] > 0 {
			continue
		}

		return name
	}
}

func init() {
	for i := range minifiedNamesCache {
		var buf [8]byte

		idx := 8
		val := i

		for {
			idx--
			buf[idx] = Alphabet[val%len(Alphabet)]

			val /= len(Alphabet)
			if val == 0 {
				break
			}
		}

		minifiedNamesCache[i] = string(buf[idx:])
	}
}

func getMinifiedName(index int) string {
	if index < len(minifiedNamesCache) {
		return minifiedNamesCache[index]
	}

	var buf [8]byte

	i := 8

	for {
		i--

		buf[i] = Alphabet[index%len(Alphabet)]

		index /= len(Alphabet)
		if index == 0 {
			break
		}
	}

	return string(buf[i:])
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
		b := r.Tree.Source[node.Start:node.End]

		sym := r.lookup(utils.String(b))
		if sym != nil {
			r.IdentMap[nodeID] = sym
		} else if r.collectMode {
			r.ReferencedGlobals[string(b)] = true
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
