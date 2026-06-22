package minifier

import (
	"math"
	"strconv"
	"strings"

	"github.com/coalaura/lugo/ast"
	"github.com/coalaura/lugo/token"
)

type Optimizer struct {
	tree            *ast.Tree
	identMap        map[ast.NodeID]*LocalSymbol
	cacheGlobals    bool
	optimizeLoops   bool
	constantFolding bool
	globalThreshold int
	iteratorIndex   int
}

func NewOptimizer(tree *ast.Tree, identMap map[ast.NodeID]*LocalSymbol, cacheGlobals, optimizeLoops, constantFolding bool, globalThreshold int) *Optimizer {
	return &Optimizer{
		tree:            tree,
		identMap:        identMap,
		cacheGlobals:    cacheGlobals,
		optimizeLoops:   optimizeLoops,
		constantFolding: constantFolding,
		globalThreshold: globalThreshold,
	}
}

func (opt *Optimizer) Optimize() {
	if opt.constantFolding {
		opt.foldConstants(opt.tree.Root)
	}

	if opt.optimizeLoops {
		opt.optimizeIpairsLoops(opt.tree.Root)
	}

	if opt.cacheGlobals {
		opt.performGlobalCaching()
	}
}

func (opt *Optimizer) foldConstants(nodeID ast.NodeID) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	opt.foldConstants(node.Left)
	opt.foldConstants(node.Right)

	if node.Count > 0 && node.Kind != ast.KindBlock && node.Kind != ast.KindFile {
		for i := range node.Count {
			opt.foldConstants(opt.tree.ExtraList[node.Extra+uint32(i)])
		}
	}

	if node.Kind == ast.KindBinaryExpr {
		leftVal, leftOK := opt.parseNumber(node.Left)
		rightVal, rightOK := opt.parseNumber(node.Right)

		if leftOK && rightOK {
			op := token.Kind(node.Extra)

			var (
				resVal  float64
				isBool  bool
				resBool bool
				ok      = true
			)

			switch op {
			case token.Plus:
				resVal = leftVal + rightVal
			case token.Minus:
				resVal = leftVal - rightVal
			case token.Asterisk:
				resVal = leftVal * rightVal
			case token.Slash:
				if rightVal != 0 {
					resVal = leftVal / rightVal
				} else {
					ok = false
				}
			case token.FloorSlash:
				if rightVal != 0 {
					resVal = math.Floor(leftVal / rightVal)
				} else {
					ok = false
				}
			case token.Modulo:
				if rightVal != 0 {
					// emulate Lua modulo behavior
					resVal = leftVal - math.Floor(leftVal/rightVal)*rightVal
				} else {
					ok = false
				}
			case token.Caret:
				resVal = math.Pow(leftVal, rightVal)
			case token.BitOr:
				resVal = float64(int64(leftVal) | int64(rightVal))
			case token.BitAnd:
				if uint64(rightVal) == 0xFFFFFFFF {
					resVal = float64(uint32(int64(leftVal)))
				} else {
					resVal = float64(int64(leftVal) & int64(rightVal))
				}
			case token.BitXor:
				resVal = float64(int64(leftVal) ^ int64(rightVal))
			case token.ShiftLeft:
				resVal = float64(int64(leftVal) << uint64(rightVal))
			case token.ShiftRight:
				resVal = float64(int64(leftVal) >> uint64(rightVal))
			case token.Less:
				isBool = true
				resBool = leftVal < rightVal
			case token.LessEq:
				isBool = true
				resBool = leftVal <= rightVal
			case token.Greater:
				isBool = true
				resBool = leftVal > rightVal
			case token.GreaterEq:
				isBool = true
				resBool = leftVal >= rightVal
			case token.Eq:
				isBool = true
				resBool = leftVal == rightVal
			case token.NotEq:
				isBool = true
				resBool = leftVal != rightVal
			default:
				ok = false
			}

			if ok {
				if isBool {
					kind := ast.KindFalse

					if resBool {
						kind = ast.KindTrue
					}

					opt.tree.Nodes[nodeID] = ast.Node{
						Kind:   kind,
						Start:  node.Start,
						End:    node.End,
						Parent: node.Parent,
					}
				} else {
					var str string

					if resVal == math.Floor(resVal) && resVal >= math.MinInt64 && resVal <= math.MaxInt64 {
						str = strconv.FormatInt(int64(resVal), 10)
					} else {
						str = strconv.FormatFloat(resVal, 'g', -1, 64)
					}

					start := uint32(len(opt.tree.Source))
					opt.tree.Source = append(opt.tree.Source, []byte(str)...)
					end := uint32(len(opt.tree.Source))

					opt.tree.Nodes[nodeID] = ast.Node{
						Kind:   ast.KindNumber,
						Start:  start,
						End:    end,
						Parent: node.Parent,
					}
				}
			}
		}
	}
}

func (opt *Optimizer) parseNumber(nodeID ast.NodeID) (float64, bool) {
	if nodeID == ast.InvalidNode {
		return 0, false
	}

	node := opt.tree.Nodes[nodeID]
	if node.Kind != ast.KindNumber {
		return 0, false
	}

	src := string(opt.tree.Source[node.Start:node.End])

	if val, err := strconv.ParseInt(src, 0, 64); err == nil {
		return float64(val), true
	}

	if val, err := strconv.ParseUint(src, 0, 64); err == nil {
		return float64(val), true
	}

	if val, err := strconv.ParseFloat(src, 64); err == nil {
		return val, true
	}

	return 0, false
}

func (opt *Optimizer) optimizeIpairsLoops(nodeID ast.NodeID) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	if node.Kind == ast.KindBlock {
		var (
			newStmts []ast.NodeID
			changed  bool
		)

		for i := range node.Count {
			childID := opt.tree.ExtraList[node.Extra+uint32(i)]

			opt.optimizeIpairsLoops(childID)

			if opt.isIpairsLoop(childID) {
				cacheDecl, loopNode := opt.transformIpairsLoop(childID)

				newStmts = append(newStmts, cacheDecl, loopNode)

				changed = true
			} else {
				newStmts = append(newStmts, childID)
			}
		}

		if changed {
			extraStart := uint32(len(opt.tree.ExtraList))

			opt.tree.ExtraList = append(opt.tree.ExtraList, newStmts...)

			opt.tree.Nodes[nodeID].Extra = extraStart
			opt.tree.Nodes[nodeID].Count = uint16(len(newStmts))

			for _, child := range newStmts {
				opt.tree.Nodes[child].Parent = nodeID
			}
		}
	} else {
		opt.optimizeIpairsLoops(node.Left)
		opt.optimizeIpairsLoops(node.Right)

		if node.Count > 0 && node.Kind != ast.KindFile {
			for i := range node.Count {
				opt.optimizeIpairsLoops(opt.tree.ExtraList[node.Extra+uint32(i)])
			}
		}
	}
}

func (opt *Optimizer) isIpairsLoop(nodeID ast.NodeID) bool {
	node := opt.tree.Nodes[nodeID]
	if node.Kind != ast.KindForIn {
		return false
	}

	if node.Left == ast.InvalidNode {
		return false
	}

	nameList := opt.tree.Nodes[node.Left]
	if nameList.Kind != ast.KindNameList || nameList.Count != 2 {
		return false
	}

	exprListID := ast.NodeID(node.Extra)
	if exprListID == ast.InvalidNode {
		return false
	}

	exprList := opt.tree.Nodes[exprListID]
	if exprList.Kind != ast.KindExprList || exprList.Count != 1 {
		return false
	}

	callExprID := opt.tree.ExtraList[exprList.Extra]

	callExpr := opt.tree.Nodes[callExprID]
	if callExpr.Kind != ast.KindCallExpr {
		return false
	}

	if callExpr.Left == ast.InvalidNode {
		return false
	}

	baseNode := opt.tree.Nodes[callExpr.Left]
	if baseNode.Kind != ast.KindIdent {
		return false
	}

	baseName := string(opt.tree.Source[baseNode.Start:baseNode.End])
	if baseName != "ipairs" {
		return false
	}

	if callExpr.Count != 1 {
		return false
	}

	return true
}

func (opt *Optimizer) transformIpairsLoop(nodeID ast.NodeID) (cacheDecl, loopNode ast.NodeID) {
	node := opt.tree.Nodes[nodeID]
	nameList := opt.tree.Nodes[node.Left]

	indexVarID := opt.tree.ExtraList[nameList.Extra]
	valueVarID := opt.tree.ExtraList[nameList.Extra+1]

	indexVar := opt.tree.Nodes[indexVarID]
	valueVar := opt.tree.Nodes[valueVarID]

	indexName := string(opt.tree.Source[indexVar.Start:indexVar.End])

	opt.iteratorIndex++
	hexIdx := strconv.FormatInt(int64(opt.iteratorIndex), 16)

	var loopIndexVarID ast.NodeID

	if indexName == "_" {
		loopIndexName := "idx_" + hexIdx

		start := uint32(len(opt.tree.Source))
		opt.tree.Source = append(opt.tree.Source, []byte(loopIndexName)...)
		end := uint32(len(opt.tree.Source))

		loopIndexVarID = opt.tree.AddNode(ast.Node{
			Kind:  ast.KindIdent,
			Start: start,
			End:   end,
		})
	} else {
		loopIndexVarID = indexVarID
	}

	iterName := "iter_" + hexIdx

	iterStart := uint32(len(opt.tree.Source))
	opt.tree.Source = append(opt.tree.Source, []byte(iterName)...)
	iterEnd := uint32(len(opt.tree.Source))

	cacheVarID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindIdent,
		Start: iterStart,
		End:   iterEnd,
	})

	exprListID := ast.NodeID(node.Extra)
	callExprID := opt.tree.ExtraList[opt.tree.Nodes[exprListID].Extra]
	callExpr := opt.tree.Nodes[callExprID]
	tableExprID := opt.tree.ExtraList[callExpr.Extra]

	cacheNameListExtra := uint32(len(opt.tree.ExtraList))
	opt.tree.ExtraList = append(opt.tree.ExtraList, cacheVarID)

	cacheNameListID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindNameList,
		Start: iterStart,
		End:   iterEnd,
		Extra: cacheNameListExtra,
		Count: 1,
	})

	cacheExprListExtra := uint32(len(opt.tree.ExtraList))
	opt.tree.ExtraList = append(opt.tree.ExtraList, tableExprID)

	cacheExprListID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindExprList,
		Start: opt.tree.Nodes[tableExprID].Start,
		End:   opt.tree.Nodes[tableExprID].End,
		Extra: cacheExprListExtra,
		Count: 1,
	})

	cacheDecl = opt.tree.AddNode(ast.Node{
		Kind:  ast.KindLocalAssign,
		Start: node.Start,
		End:   opt.tree.Nodes[tableExprID].End,
		Left:  cacheNameListID,
		Right: cacheExprListID,
	})

	startNumStart := uint32(len(opt.tree.Source))
	opt.tree.Source = append(opt.tree.Source, []byte("1")...)
	startNumEnd := uint32(len(opt.tree.Source))

	startNumID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindNumber,
		Start: startNumStart,
		End:   startNumEnd,
	})

	endExprID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindUnaryExpr,
		Start: startNumStart,
		End:   iterEnd,
		Right: cacheVarID, // #iter_X
	})

	extraStart := uint32(len(opt.tree.ExtraList))
	opt.tree.ExtraList = append(opt.tree.ExtraList, startNumID, endExprID)

	valueNameListExtra := uint32(len(opt.tree.ExtraList))
	opt.tree.ExtraList = append(opt.tree.ExtraList, valueVarID)

	valueNameListID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindNameList,
		Start: valueVar.Start,
		End:   valueVar.End,
		Extra: valueNameListExtra,
		Count: 1,
	})

	indexExprID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindIndexExpr,
		Start: iterStart,
		End:   opt.tree.Nodes[loopIndexVarID].End,
		Left:  cacheVarID,
		Right: loopIndexVarID,
	})

	valueExprListExtra := uint32(len(opt.tree.ExtraList))
	opt.tree.ExtraList = append(opt.tree.ExtraList, indexExprID)

	valueExprListID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindExprList,
		Start: iterStart,
		End:   opt.tree.Nodes[loopIndexVarID].End,
		Extra: valueExprListExtra,
		Count: 1,
	})

	valueDeclID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindLocalAssign,
		Start: valueVar.Start,
		End:   opt.tree.Nodes[loopIndexVarID].End,
		Left:  valueNameListID,
		Right: valueExprListID,
	})

	bodyBlockID := node.Right
	bodyBlock := opt.tree.Nodes[bodyBlockID]

	newBodyExtraStart := uint32(len(opt.tree.ExtraList))

	opt.tree.ExtraList = append(opt.tree.ExtraList, valueDeclID)

	for i := range bodyBlock.Count {
		opt.tree.ExtraList = append(opt.tree.ExtraList, opt.tree.ExtraList[bodyBlock.Extra+uint32(i)])
	}

	opt.tree.Nodes[bodyBlockID].Extra = newBodyExtraStart
	opt.tree.Nodes[bodyBlockID].Count = bodyBlock.Count + 1
	opt.tree.Nodes[valueDeclID].Parent = bodyBlockID

	opt.tree.Nodes[nodeID] = ast.Node{
		Kind:   ast.KindForNum,
		Start:  node.Start,
		End:    node.End,
		Left:   loopIndexVarID,
		Right:  bodyBlockID,
		Extra:  extraStart,
		Count:  2,
		Parent: node.Parent,
	}

	opt.tree.Nodes[loopIndexVarID].Parent = nodeID
	opt.tree.Nodes[startNumID].Parent = nodeID
	opt.tree.Nodes[endExprID].Parent = nodeID

	return cacheDecl, nodeID
}

func (opt *Optimizer) getGlobalPath(nodeID ast.NodeID) (string, bool) {
	if nodeID == ast.InvalidNode {
		return "", false
	}

	node := opt.tree.Nodes[nodeID]

	if node.Kind == ast.KindIdent {
		if _, isLocal := opt.identMap[nodeID]; isLocal {
			return "", false
		}

		name := string(opt.tree.Source[node.Start:node.End])

		// Do not collect compiler-generated helper/iterator variables
		if strings.HasPrefix(name, "iter_") || strings.HasPrefix(name, "idx_") || strings.HasPrefix(name, "_g") {
			return "", false
		}

		return name, true
	}

	if node.Kind == ast.KindMemberExpr {
		leftPath, ok := opt.getGlobalPath(node.Left)
		if !ok {
			return "", false
		}

		rightNode := opt.tree.Nodes[node.Right]
		if rightNode.Kind != ast.KindIdent {
			return "", false
		}

		rightName := string(opt.tree.Source[rightNode.Start:rightNode.End])

		return leftPath + "." + rightName, true
	}

	return "", false
}

func (opt *Optimizer) collectGlobals(nodeID ast.NodeID, inWriteContext bool, globalUses map[string][]ast.NodeID, isCallee bool) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	if !inWriteContext && isCallee {
		if path, ok := opt.getGlobalPath(nodeID); ok {
			globalUses[path] = append(globalUses[path], nodeID)
		}
	}

	switch node.Kind {
	case ast.KindFile:
		opt.collectGlobals(node.Left, false, globalUses, false)
	case ast.KindBlock:
		for i := range node.Count {
			opt.collectGlobals(opt.tree.ExtraList[node.Extra+uint32(i)], false, globalUses, false)
		}
	case ast.KindAssign:
		opt.collectGlobals(node.Left, true, globalUses, false)
		opt.collectGlobals(node.Right, false, globalUses, false)
	case ast.KindLocalAssign:
		opt.collectGlobals(node.Left, true, globalUses, false)
		opt.collectGlobals(node.Right, false, globalUses, false)
	case ast.KindLocalFunction:
		opt.collectGlobals(node.Left, true, globalUses, false)
		opt.collectGlobals(node.Right, false, globalUses, false)
	case ast.KindFunctionStmt:
		opt.collectGlobals(node.Left, true, globalUses, false)
		opt.collectGlobals(node.Right, false, globalUses, false)
	case ast.KindFunctionExpr:
		opt.collectGlobals(node.Right, false, globalUses, false)
	case ast.KindRecordField:
		opt.collectGlobals(node.Left, true, globalUses, false)
		opt.collectGlobals(node.Right, false, globalUses, false)
	case ast.KindIndexField:
		opt.collectGlobals(node.Left, false, globalUses, false)
		opt.collectGlobals(node.Right, false, globalUses, false)
	case ast.KindMemberExpr:
		opt.collectGlobals(node.Left, inWriteContext, globalUses, false)
		opt.collectGlobals(node.Right, true, globalUses, false)
	case ast.KindIndexExpr:
		opt.collectGlobals(node.Left, inWriteContext, globalUses, false)
		opt.collectGlobals(node.Right, false, globalUses, false)
	case ast.KindMethodCall:
		opt.collectGlobals(node.Left, inWriteContext, globalUses, false)
		opt.collectGlobals(node.Right, true, globalUses, false)

		for i := range node.Count {
			opt.collectGlobals(opt.tree.ExtraList[node.Extra+uint32(i)], false, globalUses, false)
		}
	case ast.KindCallExpr:
		opt.collectGlobals(node.Left, false, globalUses, true)

		for i := range node.Count {
			opt.collectGlobals(opt.tree.ExtraList[node.Extra+uint32(i)], false, globalUses, false)
		}
	case ast.KindBinaryExpr:
		opt.collectGlobals(node.Left, false, globalUses, false)
		opt.collectGlobals(node.Right, false, globalUses, false)
	case ast.KindUnaryExpr:
		opt.collectGlobals(node.Right, false, globalUses, false)
	case ast.KindParenExpr:
		opt.collectGlobals(node.Left, inWriteContext, globalUses, isCallee)
	case ast.KindIf:
		opt.collectGlobals(node.Left, false, globalUses, false)
		opt.collectGlobals(node.Right, false, globalUses, false)

		for i := range node.Count {
			opt.collectGlobals(opt.tree.ExtraList[node.Extra+uint32(i)], false, globalUses, false)
		}
	case ast.KindElseIf:
		opt.collectGlobals(node.Left, false, globalUses, false)
		opt.collectGlobals(node.Right, false, globalUses, false)
	case ast.KindElse:
		opt.collectGlobals(node.Left, false, globalUses, false)
	case ast.KindWhile:
		opt.collectGlobals(node.Left, false, globalUses, false)
		opt.collectGlobals(node.Right, false, globalUses, false)
	case ast.KindRepeat:
		opt.collectGlobals(node.Left, false, globalUses, false)
		opt.collectGlobals(node.Right, false, globalUses, false)
	case ast.KindForNum:
		opt.collectGlobals(node.Left, true, globalUses, false)
		opt.collectGlobals(node.Right, false, globalUses, false)

		for i := range node.Count {
			opt.collectGlobals(opt.tree.ExtraList[node.Extra+uint32(i)], false, globalUses, false)
		}
	case ast.KindForIn:
		opt.collectGlobals(node.Left, true, globalUses, false)
		opt.collectGlobals(node.Right, false, globalUses, false)
		opt.collectGlobals(ast.NodeID(node.Extra), false, globalUses, false)
	case ast.KindDo:
		opt.collectGlobals(node.Left, false, globalUses, false)
	case ast.KindReturn:
		opt.collectGlobals(node.Left, false, globalUses, false)
	case ast.KindExprList, ast.KindNameList:
		for i := range node.Count {
			opt.collectGlobals(opt.tree.ExtraList[node.Extra+uint32(i)], inWriteContext, globalUses, false)
		}
	case ast.KindTableExpr:
		for i := range node.Count {
			opt.collectGlobals(opt.tree.ExtraList[node.Extra+uint32(i)], false, globalUses, false)
		}
	default:
		opt.collectGlobals(node.Left, inWriteContext, globalUses, false)
		opt.collectGlobals(node.Right, inWriteContext, globalUses, false)

		if node.Count > 0 {
			for i := range node.Count {
				opt.collectGlobals(opt.tree.ExtraList[node.Extra+uint32(i)], inWriteContext, globalUses, false)
			}
		}
	}
}

func (opt *Optimizer) buildGlobalPathNode(path string) ast.NodeID {
	parts := strings.Split(path, ".")

	var current ast.NodeID

	for i, part := range parts {
		start := uint32(len(opt.tree.Source))
		opt.tree.Source = append(opt.tree.Source, []byte(part)...)
		end := uint32(len(opt.tree.Source))

		ident := opt.tree.AddNode(ast.Node{
			Kind:  ast.KindIdent,
			Start: start,
			End:   end,
		})

		if i == 0 {
			current = ident
		} else {
			current = opt.tree.AddNode(ast.Node{
				Kind:  ast.KindMemberExpr,
				Start: opt.tree.Nodes[current].Start,
				End:   end,
				Left:  current,
				Right: ident,
			})
		}
	}

	return current
}

func (opt *Optimizer) markInvalid(nodeID ast.NodeID) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	opt.markInvalid(node.Left)
	opt.markInvalid(node.Right)

	if node.Count > 0 && node.Kind != ast.KindBlock && node.Kind != ast.KindFile {
		for i := range node.Count {
			opt.markInvalid(opt.tree.ExtraList[node.Extra+uint32(i)])
		}
	}

	opt.tree.Nodes[nodeID].Kind = ast.KindInvalid
}

func (opt *Optimizer) performGlobalCaching() {
	globalUses := make(map[string][]ast.NodeID)

	opt.collectGlobals(opt.tree.Root, false, globalUses, false)

	var (
		localAssigns []ast.NodeID
		index        int
		paths        []string
	)

	for path, uses := range globalUses {
		if len(uses) >= opt.globalThreshold {
			paths = append(paths, path)
		}
	}

	importSort(paths)

	for _, path := range paths {
		uses := globalUses[path]

		var activeUses []ast.NodeID
		for _, u := range uses {
			if opt.tree.Nodes[u].Kind != ast.KindInvalid {
				activeUses = append(activeUses, u)
			}
		}

		if len(activeUses) < opt.globalThreshold {
			continue
		}

		localVarName := "_g" + strconv.Itoa(index)

		index++

		varStart := uint32(len(opt.tree.Source))
		opt.tree.Source = append(opt.tree.Source, []byte(localVarName)...)
		varEnd := uint32(len(opt.tree.Source))

		identID := opt.tree.AddNode(ast.Node{
			Kind:  ast.KindIdent,
			Start: varStart,
			End:   varEnd,
		})

		nameListExtra := uint32(len(opt.tree.ExtraList))
		opt.tree.ExtraList = append(opt.tree.ExtraList, identID)

		nameListID := opt.tree.AddNode(ast.Node{
			Kind:  ast.KindNameList,
			Start: varStart,
			End:   varEnd,
			Extra: nameListExtra,
			Count: 1,
		})

		rhsID := opt.buildGlobalPathNode(path)

		exprListExtra := uint32(len(opt.tree.ExtraList))
		opt.tree.ExtraList = append(opt.tree.ExtraList, rhsID)

		exprListID := opt.tree.AddNode(ast.Node{
			Kind:  ast.KindExprList,
			Start: opt.tree.Nodes[rhsID].Start,
			End:   opt.tree.Nodes[rhsID].End,
			Extra: exprListExtra,
			Count: 1,
		})

		localAssignID := opt.tree.AddNode(ast.Node{
			Kind:  ast.KindLocalAssign,
			Start: varStart,
			End:   opt.tree.Nodes[rhsID].End,
			Left:  nameListID,
			Right: exprListID,
		})

		localAssigns = append(localAssigns, localAssignID)

		for _, nodeID := range activeUses {
			parent := opt.tree.Nodes[nodeID].Parent

			node := opt.tree.Nodes[nodeID]
			opt.markInvalid(node.Left)
			opt.markInvalid(node.Right)
			if node.Count > 0 && node.Kind != ast.KindBlock && node.Kind != ast.KindFile {
				for i := range node.Count {
					opt.markInvalid(opt.tree.ExtraList[node.Extra+uint32(i)])
				}
			}

			opt.tree.Nodes[nodeID] = ast.Node{
				Kind:   ast.KindIdent,
				Start:  varStart,
				End:    varEnd,
				Parent: parent,
			}
		}
	}

	if len(localAssigns) > 0 {
		rootNode := opt.tree.Nodes[opt.tree.Root]

		rootBlockID := rootNode.Left
		if rootBlockID != ast.InvalidNode {
			rootBlock := opt.tree.Nodes[rootBlockID]

			var newStmts []ast.NodeID

			newStmts = append(newStmts, localAssigns...)

			for i := range rootBlock.Count {
				newStmts = append(newStmts, opt.tree.ExtraList[rootBlock.Extra+uint32(i)])
			}

			newExtraStart := uint32(len(opt.tree.ExtraList))
			opt.tree.ExtraList = append(opt.tree.ExtraList, newStmts...)

			opt.tree.Nodes[rootBlockID].Extra = newExtraStart
			opt.tree.Nodes[rootBlockID].Count = uint16(len(newStmts))

			for _, stmtID := range newStmts {
				opt.tree.Nodes[stmtID].Parent = rootBlockID
			}
		}
	}
}

func importSort(slice []string) {
	for i := 1; i < len(slice); i++ {
		j := i

		for j > 0 {
			if len(slice[j-1]) < len(slice[j]) || (len(slice[j-1]) == len(slice[j]) && slice[j-1] < slice[j]) {
				slice[j-1], slice[j] = slice[j], slice[j-1]

				j--
			} else {
				break
			}
		}
	}
}
