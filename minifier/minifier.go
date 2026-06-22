package minifier

import (
	"bytes"

	"github.com/coalaura/lugo/ast"
	"github.com/coalaura/lugo/token"
)

type Minifier struct {
	Tree     *ast.Tree
	IdentMap map[ast.NodeID]*LocalSymbol
	Buf      bytes.Buffer
	LastChar byte
}

func NewMinifier(tree *ast.Tree, identMap map[ast.NodeID]*LocalSymbol) *Minifier {
	return &Minifier{
		Tree:     tree,
		IdentMap: identMap,
	}
}

func (m *Minifier) Minify() []byte {
	m.printNode(m.Tree.Root)

	return m.Buf.Bytes()
}

func (m *Minifier) Write(s string) {
	if len(s) == 0 {
		return
	}

	first := s[0]

	if (isIdentChar(m.LastChar) && isIdentChar(first)) || (m.LastChar == '-' && first == '-') {
		m.Buf.WriteByte(' ')
	}

	m.Buf.WriteString(s)

	m.LastChar = s[len(s)-1]
}

func (m *Minifier) WriteByte(b byte) {
	if (isIdentChar(m.LastChar) && isIdentChar(b)) || (m.LastChar == '-' && b == '-') {
		m.Buf.WriteByte(' ')
	}

	m.Buf.WriteByte(b)

	m.LastChar = b
}

func isIdentChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

func (m *Minifier) startsWithParen(nodeID ast.NodeID) bool {
	if nodeID == ast.InvalidNode {
		return false
	}

	node := m.Tree.Nodes[nodeID]

	switch node.Kind {
	case ast.KindParenExpr:
		return true
	case ast.KindCallExpr, ast.KindMethodCall, ast.KindIndexExpr, ast.KindMemberExpr:
		return m.startsWithParen(node.Left)
	}

	return false
}

func (m *Minifier) printNode(nodeID ast.NodeID) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := m.Tree.Nodes[nodeID]

	switch node.Kind {
	case ast.KindFile:
		m.printNode(node.Left)
	case ast.KindBlock:
		for i := range node.Count {
			childID := m.Tree.ExtraList[node.Extra+uint32(i)]

			if m.startsWithParen(childID) && m.Buf.Len() > 0 {
				m.WriteByte(';')
			}

			m.printNode(childID)
		}
	case ast.KindIdent:
		if sym, ok := m.IdentMap[nodeID]; ok {
			m.Write(sym.MinifiedName)
		} else {
			m.Write(string(m.Tree.Source[node.Start:node.End]))
		}
	case ast.KindNumber, ast.KindString:
		m.Write(string(m.Tree.Source[node.Start:node.End]))
	case ast.KindNil:
		m.Write("nil")
	case ast.KindTrue:
		m.Write("true")
	case ast.KindFalse:
		m.Write("false")
	case ast.KindVararg:
		m.Write("...")
	case ast.KindLocalAssign:
		m.Write("local")
		m.printNode(node.Left)

		if node.Right != ast.InvalidNode {
			m.WriteByte('=')
			m.printNode(node.Right)
		}
	case ast.KindAssign:
		m.printNode(node.Left)
		m.WriteByte('=')
		m.printNode(node.Right)
	case ast.KindNameList, ast.KindExprList:
		for i := range node.Count {
			if i > 0 {
				m.WriteByte(',')
			}

			m.printNode(m.Tree.ExtraList[node.Extra+uint32(i)])
		}
	case ast.KindBinaryExpr:
		m.printNode(node.Left)
		m.Write(operatorString(token.Kind(node.Extra)))
		m.printNode(node.Right)
	case ast.KindUnaryExpr:
		opChar := m.Tree.Source[node.Start]

		switch opChar {
		case '-':
			m.WriteByte('-')
		case '#':
			m.WriteByte('#')
		case '~':
			m.WriteByte('~')
		case 'n': // 'not'
			m.Write("not")
		}

		m.printNode(node.Right)
	case ast.KindParenExpr:
		m.WriteByte('(')
		m.printNode(node.Left)
		m.WriteByte(')')
	case ast.KindLocalFunction:
		m.Write("local function")
		m.printNode(node.Left)
		m.printFunctionBody(node.Right)
	case ast.KindFunctionStmt:
		m.Write("function")
		m.printNode(node.Left)
		m.printFunctionBody(node.Right)
	case ast.KindFunctionExpr:
		m.Write("function")
		m.printFunctionBody(nodeID)
	case ast.KindTableExpr:
		m.WriteByte('{')

		for i := range node.Count {
			if i > 0 {
				m.WriteByte(',')
			}

			m.printNode(m.Tree.ExtraList[node.Extra+uint32(i)])
		}

		m.WriteByte('}')
	case ast.KindRecordField:
		m.printNode(node.Left)
		m.WriteByte('=')
		m.printNode(node.Right)
	case ast.KindIndexField:
		m.WriteByte('[')
		m.printNode(node.Left)
		m.WriteByte(']')
		m.WriteByte('=')
		m.printNode(node.Right)
	case ast.KindIndexExpr:
		m.printNode(node.Left)
		m.WriteByte('[')
		m.printNode(node.Right)
		m.WriteByte(']')
	case ast.KindMemberExpr:
		m.printNode(node.Left)
		m.WriteByte('.')
		m.printNode(node.Right)
	case ast.KindMethodName:
		m.printNode(node.Left)
		m.WriteByte(':')
		m.printNode(node.Right)
	case ast.KindMethodCall:
		m.printNode(node.Left)
		m.WriteByte(':')
		m.printNode(node.Right)
		m.printCallArgs(node)
	case ast.KindCallExpr:
		m.printNode(node.Left)
		m.printCallArgs(node)
	case ast.KindReturn:
		m.Write("return")

		if node.Left != ast.InvalidNode {
			m.printNode(node.Left)
		}
	case ast.KindBreak:
		m.Write("break")
	case ast.KindLabel:
		m.Write("::")
		m.printNode(node.Left)
		m.Write("::")
	case ast.KindGoto:
		m.Write("goto")
		m.printNode(node.Left)
	case ast.KindDo:
		m.Write("do")
		m.printNode(node.Left)
		m.Write("end")
	case ast.KindWhile:
		m.Write("while")
		m.printNode(node.Left)
		m.Write("do")
		m.printNode(node.Right)
		m.Write("end")
	case ast.KindRepeat:
		m.Write("repeat")
		m.printNode(node.Left)
		m.Write("until")
		m.printNode(node.Right)
	case ast.KindIf:
		m.Write("if")
		m.printNode(node.Left)
		m.Write("then")
		m.printNode(node.Right)

		for i := range node.Count {
			m.printNode(m.Tree.ExtraList[node.Extra+uint32(i)])
		}

		m.Write("end")
	case ast.KindElseIf:
		m.Write("elseif")
		m.printNode(node.Left)
		m.Write("then")
		m.printNode(node.Right)
	case ast.KindElse:
		m.Write("else")
		m.printNode(node.Left)
	case ast.KindForNum:
		m.Write("for")
		m.printNode(node.Left)
		m.WriteByte('=')

		initExpr := m.Tree.ExtraList[node.Extra]
		limitExpr := m.Tree.ExtraList[node.Extra+1]

		m.printNode(initExpr)
		m.WriteByte(',')
		m.printNode(limitExpr)

		if node.Count == 3 {
			m.WriteByte(',')
			m.printNode(m.Tree.ExtraList[node.Extra+2])
		}

		m.Write("do")
		m.printNode(node.Right)
		m.Write("end")
	case ast.KindForIn:
		m.Write("for")
		m.printNode(node.Left)
		m.Write("in")
		m.printNode(ast.NodeID(node.Extra))
		m.Write("do")
		m.printNode(node.Right)
		m.Write("end")
	}
}

func (m *Minifier) printFunctionBody(funcID ast.NodeID) {
	if funcID == ast.InvalidNode {
		return
	}

	node := m.Tree.Nodes[funcID]

	m.WriteByte('(')

	for i := range node.Count {
		if i > 0 {
			m.WriteByte(',')
		}

		m.printNode(m.Tree.ExtraList[node.Extra+uint32(i)])
	}

	m.WriteByte(')')
	m.printNode(node.Right) // block
	m.Write("end")
}

func (m *Minifier) printCallArgs(node ast.Node) {
	// omitting parentheses for singular string or table literals.
	if node.Count == 1 {
		argID := m.Tree.ExtraList[node.Extra]
		argNode := m.Tree.Nodes[argID]

		if argNode.Kind == ast.KindString || argNode.Kind == ast.KindTableExpr {
			m.printNode(argID)

			return
		}
	}

	m.WriteByte('(')

	for i := range node.Count {
		if i > 0 {
			m.WriteByte(',')
		}

		m.printNode(m.Tree.ExtraList[node.Extra+uint32(i)])
	}

	m.WriteByte(')')
}

func operatorString(k token.Kind) string {
	switch k {
	case token.Plus:
		return "+"
	case token.Minus:
		return "-"
	case token.Asterisk:
		return "*"
	case token.Slash:
		return "/"
	case token.FloorSlash:
		return "//"
	case token.Modulo:
		return "%"
	case token.Caret:
		return "^"
	case token.Concat:
		return ".."
	case token.Eq:
		return "=="
	case token.NotEq:
		return "~="
	case token.Less:
		return "<"
	case token.LessEq:
		return "<="
	case token.Greater:
		return ">"
	case token.GreaterEq:
		return ">="
	case token.And:
		return "and"
	case token.Or:
		return "or"
	case token.BitAnd:
		return "&"
	case token.BitOr:
		return "|"
	case token.BitXor:
		return "~"
	case token.ShiftLeft:
		return "<<"
	case token.ShiftRight:
		return ">>"
	default:
		return ""
	}
}
