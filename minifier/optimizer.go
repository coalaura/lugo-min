package minifier

import (
	"bytes"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/coalaura/lugo/ast"
	"github.com/coalaura/lugo/token"
	"github.com/coalaura/lugo/utils"
)

var DefaultEventFunctions = []string{
	"RegisterNetEvent",
	"RegisterServerEvent",
	"RegisterClientEvent",
	"AddEventHandler",
	"TriggerEvent",
	"TriggerClientEvent",
	"TriggerServerEvent",
}

type EventState struct {
	Functions map[string]bool
	Map       map[string]string
	Counter   int
}

type OptimizerOptions struct {
	CacheGlobals        bool
	OptimizeLoops       bool
	ConstantFolding     bool
	CombineLocals       bool
	OptimizeTableInsert bool
	GlobalThreshold     int
	MaxLocals           int

	FoldGetHashKey    bool
	SimplifyCitizen   bool
	Fixpoint          bool
	FoldStringConcat  bool
	FoldUnary         bool
	FoldLogical       bool
	DeadCode          bool
	RenameCalls       map[string]string
	SkipEventContexts map[string]bool
}

type Optimizer struct {
	tree                *ast.Tree
	identMap            map[ast.NodeID]*LocalSymbol
	globalThreshold     int
	maxLocals           int
	iteratorIndex       int
	eventState          *EventState
	cacheGlobals        bool
	optimizeLoops       bool
	constantFolding     bool
	combineLocals       bool
	optimizeTableInsert bool

	foldGetHashKey    bool
	simplifyCitizen   bool
	fixpoint          bool
	foldStringConcat  bool
	foldUnary         bool
	foldLogical       bool
	deadCode          bool
	renameCalls       map[string]string
	skipEventContexts map[string]bool
}

func NewEventState(functions map[string]bool) *EventState {
	return &EventState{
		Functions: functions,
		Map:       make(map[string]string),
	}
}

func NewOptimizer(tree *ast.Tree, identMap map[ast.NodeID]*LocalSymbol, eventState *EventState, opts OptimizerOptions) *Optimizer {
	return &Optimizer{
		tree:                tree,
		identMap:            identMap,
		eventState:          eventState,
		cacheGlobals:        opts.CacheGlobals,
		optimizeLoops:       opts.OptimizeLoops,
		constantFolding:     opts.ConstantFolding,
		combineLocals:       opts.CombineLocals,
		optimizeTableInsert: opts.OptimizeTableInsert,
		globalThreshold:     opts.GlobalThreshold,
		maxLocals:           opts.MaxLocals,

		foldGetHashKey:    opts.FoldGetHashKey,
		simplifyCitizen:   opts.SimplifyCitizen,
		fixpoint:          opts.Fixpoint,
		foldStringConcat:  opts.FoldStringConcat,
		foldUnary:         opts.FoldUnary,
		foldLogical:       opts.FoldLogical,
		deadCode:          opts.DeadCode,
		renameCalls:       opts.RenameCalls,
		skipEventContexts: opts.SkipEventContexts,
	}
}

func (opt *Optimizer) safeLocalName(prefix string, index *int) string {
	var buf []byte

	for {
		buf = append(buf[:0], prefix...)
		buf = strconv.AppendInt(buf, int64(*index), 10)

		*index++

		if !bytes.Contains(opt.tree.Source, buf) {
			return string(buf)
		}
	}
}

func (opt *Optimizer) Optimize() {
	if opt.eventState != nil {
		opt.obfuscateEventNames()
	}

	if opt.simplifyCitizen {
		opt.simplifyCitizenCalls(opt.tree.Root)
	}

	if len(opt.renameCalls) > 0 {
		opt.renameCallTargets(opt.tree.Root)
	}

	needsFixpoint := opt.constantFolding || opt.foldGetHashKey || opt.foldStringConcat ||
		opt.foldUnary || opt.foldLogical || opt.deadCode

	if needsFixpoint {
		for iter := 0; iter < 100; iter++ {
			before := opt.treeSignature()

			if opt.constantFolding {
				opt.foldConstants(opt.tree.Root)
			}

			if opt.foldStringConcat {
				opt.foldStringConcats(opt.tree.Root)
			}

			if opt.foldUnary {
				opt.foldUnaryExprs(opt.tree.Root)
			}

			if opt.foldGetHashKey {
				opt.foldGetHashKeyCalls(opt.tree.Root)
			}

			if opt.foldLogical {
				opt.foldLogicalExprs(opt.tree.Root)
			}

			if opt.deadCode {
				opt.eliminateDeadCode(opt.tree.Root)
			}

			after := opt.treeSignature()

			if !opt.fixpoint || before == after {
				break
			}
		}
	}

	if opt.optimizeLoops {
		opt.optimizeIpairsLoops(opt.tree.Root)
	}

	if opt.optimizeTableInsert {
		opt.optimizeTableInsertCalls(opt.tree.Root)
	}

	if opt.cacheGlobals {
		opt.performGlobalCaching()
	}

	if opt.combineLocals {
		opt.combineLocalDeclarations(opt.tree.Root)
	}
}

func (opt *Optimizer) foldConstants(nodeID ast.NodeID) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	opt.foldConstants(node.Left)
	opt.foldConstants(node.Right)

	if node.Kind == ast.KindForIn && node.Extra != 0 {
		opt.foldConstants(ast.NodeID(node.Extra))
	}

	if node.Count > 0 && node.Kind != ast.KindFile {
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
				resVal = float64(int64(leftVal) & int64(rightVal))
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
					start := uint32(len(opt.tree.Source))

					if resVal == math.Floor(resVal) && resVal >= math.MinInt64 && resVal <= math.MaxInt64 {
						opt.tree.Source = strconv.AppendInt(opt.tree.Source, int64(resVal), 10)
					} else {
						opt.tree.Source = strconv.AppendFloat(opt.tree.Source, resVal, 'g', -1, 64)
					}

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

	src := utils.String(opt.tree.Source[node.Start:node.End])

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

	var loopIndexVarID ast.NodeID

	if indexName == "_" {
		loopIndexName := opt.safeLocalName("idx_", &opt.iteratorIndex)

		start := uint32(len(opt.tree.Source))
		opt.tree.Source = append(opt.tree.Source, loopIndexName...)
		end := uint32(len(opt.tree.Source))

		loopIndexVarID = opt.tree.AddNode(ast.Node{
			Kind:  ast.KindIdent,
			Start: start,
			End:   end,
		})
	} else {
		loopIndexVarID = indexVarID
	}

	iterName := opt.safeLocalName("iter_", &opt.iteratorIndex)

	iterStart := uint32(len(opt.tree.Source))
	opt.tree.Source = append(opt.tree.Source, iterName...)
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
	opt.tree.Source = append(opt.tree.Source, "1"...)
	startNumEnd := uint32(len(opt.tree.Source))

	startNumID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindNumber,
		Start: startNumStart,
		End:   startNumEnd,
	})

	hashStart := uint32(len(opt.tree.Source))
	opt.tree.Source = append(opt.tree.Source, '#')

	endExprID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindUnaryExpr,
		Start: hashStart,
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

		name := utils.String(opt.tree.Source[node.Start:node.End])

		// do not collect compiler-generated helper/iterator variables
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

		rightName := utils.String(opt.tree.Source[rightNode.Start:rightNode.End])

		return leftPath + "." + rightName, true
	}

	return "", false
}

func (opt *Optimizer) collectGlobals(nodeID ast.NodeID, inWriteContext bool, globalUses map[string][]ast.NodeID, isCallee bool) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	// only collect global paths if they are evaluated in a read context and are invoked directly as a function call
	if !inWriteContext && isCallee {
		if path, ok := opt.getGlobalPath(nodeID); ok {
			globalUses[path] = append(globalUses[path], nodeID)
			return
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
		opt.collectGlobals(node.Left, inWriteContext, globalUses, isCallee)
		opt.collectGlobals(node.Right, true, globalUses, false)
	case ast.KindIndexExpr:
		opt.collectGlobals(node.Left, inWriteContext, globalUses, isCallee)
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
		opt.tree.Source = append(opt.tree.Source, part...)
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

func (opt *Optimizer) countRootLocals() int {
	rootNode := opt.tree.Nodes[opt.tree.Root]
	rootBlockID := rootNode.Left
	if rootBlockID == ast.InvalidNode {
		return 0
	}

	rootBlock := opt.tree.Nodes[rootBlockID]
	count := 0

	for i := range rootBlock.Count {
		stmtID := opt.tree.ExtraList[rootBlock.Extra+uint32(i)]
		stmt := opt.tree.Nodes[stmtID]

		if stmt.Kind == ast.KindLocalAssign {
			if stmt.Left != ast.InvalidNode {
				count += int(opt.tree.Nodes[stmt.Left].Count)
			}
		} else if stmt.Kind == ast.KindLocalFunction {
			count++
		}
	}

	return count
}

func (opt *Optimizer) performGlobalCaching() {
	existingLocals := opt.countRootLocals()

	budget := opt.maxLocals - existingLocals
	if opt.maxLocals > 0 && budget <= 0 {
		return
	}

	globalUses := make(map[string][]ast.NodeID)

	opt.collectGlobals(opt.tree.Root, false, globalUses, false)

	var (
		localAssigns []ast.NodeID
		index        int
	)

	paths := make([]string, 0, len(globalUses))

	for path, uses := range globalUses {
		if len(uses) >= opt.globalThreshold {
			paths = append(paths, path)
		}
	}

	sort.Slice(paths, func(i, j int) bool {
		pathI := paths[i]
		pathJ := paths[j]
		usesI := len(globalUses[pathI])
		usesJ := len(globalUses[pathJ])

		scoreI := len(pathI) * usesI
		scoreJ := len(pathJ) * usesJ

		if scoreI != scoreJ {
			return scoreI > scoreJ
		}

		if usesI != usesJ {
			return usesI > usesJ
		}

		if len(pathI) != len(pathJ) {
			return len(pathI) > len(pathJ)
		}

		return pathI < pathJ
	})

	if opt.maxLocals > 0 && len(paths) > budget {
		paths = paths[:budget]
	}

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

		localVarName := opt.safeLocalName("_g", &index)

		varStart := uint32(len(opt.tree.Source))
		opt.tree.Source = append(opt.tree.Source, localVarName...)
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

func (opt *Optimizer) combineLocalDeclarations(nodeID ast.NodeID) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	if node.Kind == ast.KindBlock {
		var (
			newStmts []ast.NodeID
			changed  bool
			i        int
		)

		for i < int(node.Count) {
			childID := opt.tree.ExtraList[node.Extra+uint32(i)]
			childNode := opt.tree.Nodes[childID]

			opt.combineLocalDeclarations(childID)

			if childNode.Kind != ast.KindLocalAssign {
				newStmts = append(newStmts, childID)
				i++
				continue
			}

			group := []ast.NodeID{childID}
			declaredNames := make(map[string]bool)

			opt.collectDeclaredNames(childID, declaredNames)

			hasRHS := (childNode.Right != ast.InvalidNode)
			totalVariables := opt.getListNameCount(childNode.Left)

			j := i + 1

			for j < int(node.Count) {
				nextID := opt.tree.ExtraList[node.Extra+uint32(j)]
				nextNode := opt.tree.Nodes[nextID]

				opt.combineLocalDeclarations(nextID)

				nextNode = opt.tree.Nodes[nextID]

				if nextNode.Kind != ast.KindLocalAssign {
					break
				}

				nextLHSCount := opt.getListNameCount(nextNode.Left)
				if totalVariables+nextLHSCount > 100 {
					break
				}

				nextHasRHS := (nextNode.Right != ast.InvalidNode)
				if hasRHS != nextHasRHS {
					break
				}

				if hasRHS {
					prevID := group[len(group)-1]
					prevNode := opt.tree.Nodes[prevID]

					lhsCount := opt.getListNameCount(prevNode.Left)
					rhsCount := opt.getListExprCount(prevNode.Right)

					if lhsCount != rhsCount {
						break
					}

					if opt.hasDependency(nextNode.Right, declaredNames) {
						break
					}
				}

				totalVariables += nextLHSCount
				group = append(group, nextID)
				opt.collectDeclaredNames(nextID, declaredNames)
				j++
			}

			if len(group) > 1 {
				mergedID := opt.mergeLocalAssigns(group, hasRHS)
				newStmts = append(newStmts, mergedID)
				changed = true
				i = j
			} else {
				newStmts = append(newStmts, childID)
				i++
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
		opt.combineLocalDeclarations(node.Left)
		opt.combineLocalDeclarations(node.Right)

		if node.Count > 0 && node.Kind != ast.KindFile {
			for i := range node.Count {
				opt.combineLocalDeclarations(opt.tree.ExtraList[node.Extra+uint32(i)])
			}
		}
	}
}

func (opt *Optimizer) collectDeclaredNames(localAssignID ast.NodeID, dest map[string]bool) {
	node := opt.tree.Nodes[localAssignID]
	if node.Left == ast.InvalidNode {
		return
	}

	nameList := opt.tree.Nodes[node.Left]

	for k := range nameList.Count {
		identID := opt.tree.ExtraList[nameList.Extra+uint32(k)]
		identNode := opt.tree.Nodes[identID]

		name := string(opt.tree.Source[identNode.Start:identNode.End])
		dest[name] = true
	}
}

func (opt *Optimizer) getListNameCount(nodeID ast.NodeID) int {
	if nodeID == ast.InvalidNode {
		return 0
	}

	return int(opt.tree.Nodes[nodeID].Count)
}

func (opt *Optimizer) getListExprCount(nodeID ast.NodeID) int {
	if nodeID == ast.InvalidNode {
		return 0
	}

	return int(opt.tree.Nodes[nodeID].Count)
}

func (opt *Optimizer) mergeLocalAssigns(group []ast.NodeID, hasRHS bool) ast.NodeID {
	var lhsIdents []ast.NodeID

	for _, assignID := range group {
		assignNode := opt.tree.Nodes[assignID]
		nameListNode := opt.tree.Nodes[assignNode.Left]

		for k := range nameListNode.Count {
			lhsIdents = append(lhsIdents, opt.tree.ExtraList[nameListNode.Extra+uint32(k)])
		}
	}

	lhsExtraStart := uint32(len(opt.tree.ExtraList))
	opt.tree.ExtraList = append(opt.tree.ExtraList, lhsIdents...)

	firstNode := opt.tree.Nodes[group[0]]
	lastNode := opt.tree.Nodes[group[len(group)-1]]

	lhsNodeID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindNameList,
		Start: opt.tree.Nodes[lhsIdents[0]].Start,
		End:   opt.tree.Nodes[lhsIdents[len(lhsIdents)-1]].End,
		Extra: lhsExtraStart,
		Count: uint16(len(lhsIdents)),
	})

	rhsNodeID := ast.InvalidNode

	if hasRHS {
		var rhsExprs []ast.NodeID

		for _, assignID := range group {
			assignNode := opt.tree.Nodes[assignID]
			exprListNode := opt.tree.Nodes[assignNode.Right]

			for k := range exprListNode.Count {
				rhsExprs = append(rhsExprs, opt.tree.ExtraList[exprListNode.Extra+uint32(k)])
			}
		}

		rhsExtraStart := uint32(len(opt.tree.ExtraList))
		opt.tree.ExtraList = append(opt.tree.ExtraList, rhsExprs...)

		rhsNodeID = opt.tree.AddNode(ast.Node{
			Kind:  ast.KindExprList,
			Start: opt.tree.Nodes[rhsExprs[0]].Start,
			End:   opt.tree.Nodes[rhsExprs[len(rhsExprs)-1]].End,
			Extra: rhsExtraStart,
			Count: uint16(len(rhsExprs)),
		})
	}

	mergedID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindLocalAssign,
		Start: firstNode.Start,
		End:   lastNode.End,
		Left:  lhsNodeID,
		Right: rhsNodeID,
	})

	return mergedID
}

func (opt *Optimizer) hasDependency(nodeID ast.NodeID, declaredNames map[string]bool) bool {
	if nodeID == ast.InvalidNode {
		return false
	}

	node := opt.tree.Nodes[nodeID]

	if node.Kind == ast.KindIdent {
		if declaredNames[utils.String(opt.tree.Source[node.Start:node.End])] {
			return true
		}
	}

	if node.Left != ast.InvalidNode && opt.hasDependency(node.Left, declaredNames) {
		return true
	}

	if node.Right != ast.InvalidNode && opt.hasDependency(node.Right, declaredNames) {
		return true
	}

	if node.Count > 0 {
		for i := range node.Count {
			childID := opt.tree.ExtraList[node.Extra+uint32(i)]
			if opt.hasDependency(childID, declaredNames) {
				return true
			}
		}
	}

	if node.Kind == ast.KindForIn && node.Extra != 0 {
		if opt.hasDependency(ast.NodeID(node.Extra), declaredNames) {
			return true
		}
	}

	return false
}

func (opt *Optimizer) optimizeTableInsertCalls(nodeID ast.NodeID) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	if node.Kind == ast.KindBlock {
		for i := range node.Count {
			childID := opt.tree.ExtraList[node.Extra+uint32(i)]

			opt.optimizeTableInsertCalls(childID)

			if opt.isStatement(childID) {
				if tID, vID, ok := opt.isTableInsertTwoArgsCall(childID); ok {
					if opt.isSafeToDuplicate(tID) {
						opt.transformTableInsert(childID, tID, vID)
					}
				}
			}
		}
	} else {
		opt.optimizeTableInsertCalls(node.Left)
		opt.optimizeTableInsertCalls(node.Right)

		if node.Count > 0 && node.Kind != ast.KindFile {
			for i := range node.Count {
				opt.optimizeTableInsertCalls(opt.tree.ExtraList[node.Extra+uint32(i)])
			}
		}
	}
}

func (opt *Optimizer) isStatement(nodeID ast.NodeID) bool {
	if nodeID == ast.InvalidNode {
		return false
	}

	parentID := opt.tree.Nodes[nodeID].Parent
	if parentID == ast.InvalidNode {
		return false
	}

	return opt.tree.Nodes[parentID].Kind == ast.KindBlock
}

func (opt *Optimizer) isTableInsertCallee(nodeID ast.NodeID) bool {
	if nodeID == ast.InvalidNode {
		return false
	}

	node := opt.tree.Nodes[nodeID]
	if node.Kind != ast.KindMemberExpr {
		return false
	}

	leftNode := opt.tree.Nodes[node.Left]
	if leftNode.Kind != ast.KindIdent {
		return false
	}

	if string(opt.tree.Source[leftNode.Start:leftNode.End]) != "table" {
		return false
	}

	if _, isLocal := opt.identMap[node.Left]; isLocal {
		return false
	}

	rightNode := opt.tree.Nodes[node.Right]
	if rightNode.Kind != ast.KindIdent {
		return false
	}

	if string(opt.tree.Source[rightNode.Start:rightNode.End]) != "insert" {
		return false
	}

	return true
}

func (opt *Optimizer) isTableInsertTwoArgsCall(nodeID ast.NodeID) (ast.NodeID, ast.NodeID, bool) {
	if nodeID == ast.InvalidNode {
		return ast.InvalidNode, ast.InvalidNode, false
	}

	node := opt.tree.Nodes[nodeID]
	if node.Kind != ast.KindCallExpr {
		return ast.InvalidNode, ast.InvalidNode, false
	}

	if !opt.isTableInsertCallee(node.Left) {
		return ast.InvalidNode, ast.InvalidNode, false
	}

	if node.Count != 2 {
		return ast.InvalidNode, ast.InvalidNode, false
	}

	tableArgID := opt.tree.ExtraList[node.Extra]
	valueArgID := opt.tree.ExtraList[node.Extra+1]

	return tableArgID, valueArgID, true
}

func (opt *Optimizer) isSafeToDuplicate(nodeID ast.NodeID) bool {
	if nodeID == ast.InvalidNode {
		return false
	}

	node := opt.tree.Nodes[nodeID]

	switch node.Kind {
	case ast.KindIdent:
		return true
	case ast.KindMemberExpr:
		return opt.isSafeToDuplicate(node.Left) && opt.tree.Nodes[node.Right].Kind == ast.KindIdent
	case ast.KindIndexExpr:
		rightNode := opt.tree.Nodes[node.Right]

		return opt.isSafeToDuplicate(node.Left) && (rightNode.Kind == ast.KindString || rightNode.Kind == ast.KindNumber)
	default:
		return false
	}
}

func (opt *Optimizer) cloneNode(nodeID ast.NodeID) ast.NodeID {
	if nodeID == ast.InvalidNode {
		return ast.InvalidNode
	}

	orig := opt.tree.Nodes[nodeID]

	clone := ast.Node{
		Kind:  orig.Kind,
		Start: orig.Start,
		End:   orig.End,
		Extra: orig.Extra,
		Count: orig.Count,
	}

	clone.Left = opt.cloneNode(orig.Left)
	clone.Right = opt.cloneNode(orig.Right)

	if orig.Count > 0 && orig.Kind != ast.KindBlock && orig.Kind != ast.KindFile {
		newExtraStart := uint32(len(opt.tree.ExtraList))

		for i := range orig.Count {
			childID := opt.tree.ExtraList[orig.Extra+uint32(i)]
			clonedChildID := opt.cloneNode(childID)
			opt.tree.ExtraList = append(opt.tree.ExtraList, clonedChildID)
		}

		clone.Extra = newExtraStart
	}

	clonedID := opt.tree.AddNode(clone)

	if orig.Kind == ast.KindIdent {
		if sym, ok := opt.identMap[nodeID]; ok {
			opt.identMap[clonedID] = sym
		}
	}

	return clonedID
}

func (opt *Optimizer) transformTableInsert(callNodeID, tID, vID ast.NodeID) {
	startNumStart := uint32(len(opt.tree.Source))
	opt.tree.Source = append(opt.tree.Source, "1"...)
	startNumEnd := uint32(len(opt.tree.Source))

	startNumID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindNumber,
		Start: startNumStart,
		End:   startNumEnd,
	})

	hashStart := uint32(len(opt.tree.Source))
	opt.tree.Source = append(opt.tree.Source, '#')

	tClone1 := opt.cloneNode(tID)
	lenExprID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindUnaryExpr,
		Start: hashStart,
		End:   opt.tree.Nodes[tClone1].End,
		Right: tClone1,
	})

	plusExprID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindBinaryExpr,
		Start: hashStart,
		End:   opt.tree.Nodes[startNumID].End,
		Left:  lenExprID,
		Right: startNumID,
		Extra: uint32(token.Plus),
	})

	tClone2 := opt.cloneNode(tID)
	indexExprID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindIndexExpr,
		Start: opt.tree.Nodes[tClone2].Start,
		End:   opt.tree.Nodes[plusExprID].End,
		Left:  tClone2,
		Right: plusExprID,
	})

	lhsExtraStart := uint32(len(opt.tree.ExtraList))
	opt.tree.ExtraList = append(opt.tree.ExtraList, indexExprID)
	lhsListID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindExprList,
		Start: opt.tree.Nodes[indexExprID].Start,
		End:   opt.tree.Nodes[indexExprID].End,
		Extra: lhsExtraStart,
		Count: 1,
	})

	rhsExtraStart := uint32(len(opt.tree.ExtraList))
	opt.tree.ExtraList = append(opt.tree.ExtraList, vID)
	rhsListID := opt.tree.AddNode(ast.Node{
		Kind:  ast.KindExprList,
		Start: opt.tree.Nodes[vID].Start,
		End:   opt.tree.Nodes[vID].End,
		Extra: rhsExtraStart,
		Count: 1,
	})

	opt.tree.Nodes[callNodeID] = ast.Node{
		Kind:   ast.KindAssign,
		Start:  opt.tree.Nodes[lhsListID].Start,
		End:    opt.tree.Nodes[rhsListID].End,
		Left:   lhsListID,
		Right:  rhsListID,
		Parent: opt.tree.Nodes[callNodeID].Parent,
	}

	opt.tree.Nodes[lhsListID].Parent = callNodeID
	opt.tree.Nodes[rhsListID].Parent = callNodeID
}

func (opt *Optimizer) obfuscateEventNames() {
	opt.collectEventNames(opt.tree.Root)
	opt.replaceEventStrings(opt.tree.Root, false)
}

func (opt *Optimizer) collectEventNames(nodeID ast.NodeID) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	if node.Kind == ast.KindCallExpr || node.Kind == ast.KindMethodCall {
		if callName, ok := opt.getCallName(nodeID); ok && opt.eventState.Functions[callName] {
			if node.Count >= 1 {
				argID := opt.tree.ExtraList[node.Extra]
				argNode := opt.tree.Nodes[argID]

				if argNode.Kind == ast.KindString && argNode.End > argNode.Start+1 && opt.tree.Source[argNode.Start] != '[' {
					originalBytes := opt.tree.Source[argNode.Start+1 : argNode.End-1]

					if _, exists := opt.eventState.Map[string(originalBytes)]; !exists {
						opt.eventState.Map[string(originalBytes)] = opt.nextEventName()
					}
				}
			}
		}
	}

	if node.Kind == ast.KindBlock {
		for i := range node.Count {
			opt.collectEventNames(opt.tree.ExtraList[node.Extra+uint32(i)])
		}
	} else {
		opt.collectEventNames(node.Left)
		opt.collectEventNames(node.Right)

		if node.Count > 0 && node.Kind != ast.KindFile {
			for i := range node.Count {
				opt.collectEventNames(opt.tree.ExtraList[node.Extra+uint32(i)])
			}
		}
	}
}

func (opt *Optimizer) replaceEventStrings(nodeID ast.NodeID, skip bool) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	if !skip && (node.Kind == ast.KindCallExpr || node.Kind == ast.KindMethodCall) {
		if callName, ok := opt.getCallName(nodeID); ok && opt.skipEventContexts[callName] {
			skip = true
		}
	}

	if node.Kind == ast.KindString && node.End > node.Start+1 && opt.tree.Source[node.Start] != '[' {
		if !skip {
			if minified, ok := opt.eventState.Map[string(opt.tree.Source[node.Start+1:node.End-1])]; ok {
				newStr := `"` + minified + `"`
				start := uint32(len(opt.tree.Source))
				opt.tree.Source = append(opt.tree.Source, newStr...)
				end := uint32(len(opt.tree.Source))

				opt.tree.Nodes[nodeID] = ast.Node{
					Kind:   ast.KindString,
					Start:  start,
					End:    end,
					Parent: node.Parent,
				}
			}
		}

		return
	}

	if node.Kind == ast.KindBlock {
		for i := range node.Count {
			opt.replaceEventStrings(opt.tree.ExtraList[node.Extra+uint32(i)], skip)
		}
	} else {
		opt.replaceEventStrings(node.Left, skip)
		opt.replaceEventStrings(node.Right, skip)

		if node.Count > 0 && node.Kind != ast.KindFile {
			for i := range node.Count {
				opt.replaceEventStrings(opt.tree.ExtraList[node.Extra+uint32(i)], skip)
			}
		}
	}
}

func (opt *Optimizer) getCallName(nodeID ast.NodeID) (string, bool) {
	if nodeID == ast.InvalidNode {
		return "", false
	}

	node := opt.tree.Nodes[nodeID]

	switch node.Kind {
	case ast.KindCallExpr:
		calleeID := node.Left
		callee := opt.tree.Nodes[calleeID]

		if callee.Kind == ast.KindIdent {
			// skip locals to avoid false positives
			if _, isLocal := opt.identMap[calleeID]; isLocal {
				return "", false
			}

			return utils.String(opt.tree.Source[callee.Start:callee.End]), true
		}

		// match by name without local checks
		return opt.getCalleePath(calleeID)
	case ast.KindMethodCall:
		leftPath, ok := opt.getCalleePath(node.Left)
		if !ok {
			return "", false
		}

		rightNode := opt.tree.Nodes[node.Right]
		if rightNode.Kind != ast.KindIdent {
			return "", false
		}

		rightName := utils.String(opt.tree.Source[rightNode.Start:rightNode.End])

		return leftPath + ":" + rightName, true
	}

	return "", false
}

func (opt *Optimizer) getCalleePath(nodeID ast.NodeID) (string, bool) {
	if nodeID == ast.InvalidNode {
		return "", false
	}

	node := opt.tree.Nodes[nodeID]

	switch node.Kind {
	case ast.KindIdent:
		return utils.String(opt.tree.Source[node.Start:node.End]), true
	case ast.KindMemberExpr:
		leftPath, ok := opt.getCalleePath(node.Left)
		if !ok {
			return "", false
		}

		rightNode := opt.tree.Nodes[node.Right]
		if rightNode.Kind != ast.KindIdent {
			return "", false
		}

		rightName := utils.String(opt.tree.Source[rightNode.Start:rightNode.End])

		return leftPath + "." + rightName, true
	}

	return "", false
}

func (opt *Optimizer) nextEventName() string {
	for {
		name := getMinifiedName(opt.eventState.Counter)
		opt.eventState.Counter++

		collision := false
		for _, existing := range opt.eventState.Map {
			if existing == name {
				collision = true
				break
			}
		}

		if !collision {
			return name
		}
	}
}

func (opt *Optimizer) treeSignature() uint64 {
	var h uint64

	for i := range opt.tree.Nodes {
		n := &opt.tree.Nodes[i]

		h = h*31 + uint64(n.Kind)
		h = h*31 + uint64(n.Left)
		h = h*31 + uint64(n.Right)
		h = h*31 + uint64(n.Extra)
		h = h*31 + uint64(n.Count)
	}

	return h
}

func (opt *Optimizer) foldStringConcats(nodeID ast.NodeID) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	if node.Kind == ast.KindBlock {
		for i := range node.Count {
			opt.foldStringConcats(opt.tree.ExtraList[node.Extra+uint32(i)])
		}

		return
	}

	opt.foldStringConcats(node.Left)
	opt.foldStringConcats(node.Right)

	if node.Kind == ast.KindForIn && node.Extra != 0 {
		opt.foldStringConcats(ast.NodeID(node.Extra))
	}

	if node.Count > 0 && node.Kind != ast.KindFile {
		for i := range node.Count {
			opt.foldStringConcats(opt.tree.ExtraList[node.Extra+uint32(i)])
		}
	}

	if node.Kind == ast.KindBinaryExpr && token.Kind(node.Extra) == token.Concat {
		leftNode := opt.tree.Nodes[node.Left]
		rightNode := opt.tree.Nodes[node.Right]

		if leftNode.Kind == ast.KindString && rightNode.Kind == ast.KindString {
			opt.tryFoldStringConcat(nodeID, node)
		}
	}
}

func (opt *Optimizer) tryFoldStringConcat(nodeID ast.NodeID, node ast.Node) bool {
	leftNode := opt.tree.Nodes[node.Left]
	rightNode := opt.tree.Nodes[node.Right]

	leftRaw := opt.tree.Source[leftNode.Start:leftNode.End]
	rightRaw := opt.tree.Source[rightNode.Start:rightNode.End]

	if len(leftRaw) < 2 || len(rightRaw) < 2 {
		return false
	}

	leftQuote := leftRaw[0]
	rightQuote := rightRaw[0]

	if (leftQuote != '"' && leftQuote != '\'') || (rightQuote != '"' && rightQuote != '\'') {
		return false
	}

	if leftQuote != rightQuote {
		return false
	}

	if bytes.IndexByte(leftRaw, '\\') != -1 || bytes.IndexByte(rightRaw, '\\') != -1 {
		return false
	}

	start := uint32(len(opt.tree.Source))
	opt.tree.Source = append(opt.tree.Source, leftRaw[:len(leftRaw)-1]...)
	opt.tree.Source = append(opt.tree.Source, rightRaw[1:]...)
	end := uint32(len(opt.tree.Source))

	opt.tree.Nodes[nodeID] = ast.Node{
		Kind:   ast.KindString,
		Start:  start,
		End:    end,
		Parent: node.Parent,
	}

	return true
}

func (opt *Optimizer) foldUnaryExprs(nodeID ast.NodeID) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	if node.Kind == ast.KindBlock {
		for i := range node.Count {
			opt.foldUnaryExprs(opt.tree.ExtraList[node.Extra+uint32(i)])
		}

		return
	}

	opt.foldUnaryExprs(node.Left)
	opt.foldUnaryExprs(node.Right)

	if node.Kind == ast.KindForIn && node.Extra != 0 {
		opt.foldUnaryExprs(ast.NodeID(node.Extra))
	}

	if node.Count > 0 && node.Kind != ast.KindFile {
		for i := range node.Count {
			opt.foldUnaryExprs(opt.tree.ExtraList[node.Extra+uint32(i)])
		}
	}

	if node.Kind == ast.KindUnaryExpr {
		opt.tryFoldUnaryExpr(nodeID, node)
	}
}

func (opt *Optimizer) tryFoldUnaryExpr(nodeID ast.NodeID, node ast.Node) {
	operandNode := opt.tree.Nodes[node.Right]
	opChar := opt.tree.Source[node.Start]

	switch opChar {
	case '-':
		if operandNode.Kind == ast.KindNumber {
			val, ok := opt.parseNumber(node.Right)

			if ok {
				resVal := -val
				start := uint32(len(opt.tree.Source))

				if resVal == math.Floor(resVal) && resVal >= math.MinInt64 && resVal <= math.MaxInt64 {
					opt.tree.Source = strconv.AppendInt(opt.tree.Source, int64(resVal), 10)
				} else {
					opt.tree.Source = strconv.AppendFloat(opt.tree.Source, resVal, 'g', -1, 64)
				}

				end := uint32(len(opt.tree.Source))

				opt.tree.Nodes[nodeID] = ast.Node{
					Kind:   ast.KindNumber,
					Start:  start,
					End:    end,
					Parent: node.Parent,
				}
			}
		}

		if operandNode.Kind == ast.KindUnaryExpr && opt.tree.Source[operandNode.Start] == '-' {
			innerNode := opt.tree.Nodes[operandNode.Right]

			opt.tree.Nodes[nodeID] = ast.Node{
				Kind:   innerNode.Kind,
				Start:  innerNode.Start,
				End:    innerNode.End,
				Left:   innerNode.Left,
				Right:  innerNode.Right,
				Extra:  innerNode.Extra,
				Count:  innerNode.Count,
				Parent: node.Parent,
			}
		}
	case 'n':
		if operandNode.Kind == ast.KindTrue {
			opt.tree.Nodes[nodeID] = ast.Node{
				Kind:   ast.KindFalse,
				Parent: node.Parent,
			}
		} else if operandNode.Kind == ast.KindFalse {
			opt.tree.Nodes[nodeID] = ast.Node{
				Kind:   ast.KindTrue,
				Parent: node.Parent,
			}
		} else if operandNode.Kind == ast.KindNil {
			opt.tree.Nodes[nodeID] = ast.Node{
				Kind:   ast.KindTrue,
				Parent: node.Parent,
			}
		}
	case '#':
		if operandNode.Kind == ast.KindString && operandNode.End > operandNode.Start+1 {
			quote := opt.tree.Source[operandNode.Start]

			if quote == '"' || quote == '\'' {
				content := opt.tree.Source[operandNode.Start+1 : operandNode.End-1]

				if bytes.IndexByte(content, '\\') == -1 {
					start := uint32(len(opt.tree.Source))
					opt.tree.Source = strconv.AppendInt(opt.tree.Source, int64(len(content)), 10)
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

func (opt *Optimizer) foldGetHashKeyCalls(nodeID ast.NodeID) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	if node.Kind == ast.KindBlock {
		for i := range node.Count {
			opt.foldGetHashKeyCalls(opt.tree.ExtraList[node.Extra+uint32(i)])
		}

		return
	}

	opt.foldGetHashKeyCalls(node.Left)
	opt.foldGetHashKeyCalls(node.Right)

	if node.Kind == ast.KindForIn && node.Extra != 0 {
		opt.foldGetHashKeyCalls(ast.NodeID(node.Extra))
	}

	if node.Count > 0 && node.Kind != ast.KindFile {
		for i := range node.Count {
			opt.foldGetHashKeyCalls(opt.tree.ExtraList[node.Extra+uint32(i)])
		}
	}

	if node.Kind == ast.KindCallExpr && node.Left != ast.InvalidNode {
		calleeNode := opt.tree.Nodes[node.Left]

		if calleeNode.Kind == ast.KindIdent {
			if _, isLocal := opt.identMap[node.Left]; !isLocal {
				if string(opt.tree.Source[calleeNode.Start:calleeNode.End]) == "GetHashKey" && node.Count == 1 {
					argID := opt.tree.ExtraList[node.Extra]
					argNode := opt.tree.Nodes[argID]

					if argNode.Kind == ast.KindString && argNode.End > argNode.Start+1 {
						quote := opt.tree.Source[argNode.Start]

						if quote == '"' || quote == '\'' {
							content := opt.tree.Source[argNode.Start+1 : argNode.End-1]
							hash := joaat(content)

							start := uint32(len(opt.tree.Source))
							opt.tree.Source = strconv.AppendInt(opt.tree.Source, int64(hash), 10)
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
	}
}

func (opt *Optimizer) foldLogicalExprs(nodeID ast.NodeID) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	if node.Kind == ast.KindBlock {
		for i := range node.Count {
			opt.foldLogicalExprs(opt.tree.ExtraList[node.Extra+uint32(i)])
		}

		return
	}

	opt.foldLogicalExprs(node.Left)
	opt.foldLogicalExprs(node.Right)

	if node.Kind == ast.KindForIn && node.Extra != 0 {
		opt.foldLogicalExprs(ast.NodeID(node.Extra))
	}

	if node.Count > 0 && node.Kind != ast.KindFile {
		for i := range node.Count {
			opt.foldLogicalExprs(opt.tree.ExtraList[node.Extra+uint32(i)])
		}
	}

	if node.Kind == ast.KindBinaryExpr {
		op := token.Kind(node.Extra)
		leftNode := opt.tree.Nodes[node.Left]

		switch op {
		case token.And:
			if leftNode.Kind == ast.KindTrue {
				rightNode := opt.tree.Nodes[node.Right]

				opt.tree.Nodes[nodeID] = ast.Node{
					Kind:   rightNode.Kind,
					Start:  rightNode.Start,
					End:    rightNode.End,
					Left:   rightNode.Left,
					Right:  rightNode.Right,
					Extra:  rightNode.Extra,
					Count:  rightNode.Count,
					Parent: node.Parent,
				}
			} else if leftNode.Kind == ast.KindFalse && opt.isSideEffectFree(node.Right) {
				opt.tree.Nodes[nodeID] = ast.Node{
					Kind:   ast.KindFalse,
					Parent: node.Parent,
				}
			}
		case token.Or:
			if leftNode.Kind == ast.KindFalse {
				rightNode := opt.tree.Nodes[node.Right]

				opt.tree.Nodes[nodeID] = ast.Node{
					Kind:   rightNode.Kind,
					Start:  rightNode.Start,
					End:    rightNode.End,
					Left:   rightNode.Left,
					Right:  rightNode.Right,
					Extra:  rightNode.Extra,
					Count:  rightNode.Count,
					Parent: node.Parent,
				}
			} else if leftNode.Kind == ast.KindTrue && opt.isSideEffectFree(node.Right) {
				opt.tree.Nodes[nodeID] = ast.Node{
					Kind:   ast.KindTrue,
					Parent: node.Parent,
				}
			}
		}
	}
}

func (opt *Optimizer) isSideEffectFree(nodeID ast.NodeID) bool {
	if nodeID == ast.InvalidNode {
		return true
	}

	node := opt.tree.Nodes[nodeID]

	switch node.Kind {
	case ast.KindNumber, ast.KindString, ast.KindNil, ast.KindTrue, ast.KindFalse, ast.KindVararg:
		return true
	case ast.KindIdent:
		return true
	case ast.KindMemberExpr:
		return opt.isSideEffectFree(node.Left)
	case ast.KindParenExpr:
		return opt.isSideEffectFree(node.Left)
	case ast.KindBinaryExpr:
		return opt.isSideEffectFree(node.Left) && opt.isSideEffectFree(node.Right)
	case ast.KindUnaryExpr:
		return opt.isSideEffectFree(node.Right)
	case ast.KindTableExpr:
		for i := range node.Count {
			if !opt.isSideEffectFree(opt.tree.ExtraList[node.Extra+uint32(i)]) {
				return false
			}
		}

		return true
	default:
		return false
	}
}

func (opt *Optimizer) hasGotoOrLabel(nodeID ast.NodeID) bool {
	if nodeID == ast.InvalidNode {
		return false
	}

	node := opt.tree.Nodes[nodeID]

	if node.Kind == ast.KindGoto || node.Kind == ast.KindLabel {
		return true
	}

	if opt.hasGotoOrLabel(node.Left) || opt.hasGotoOrLabel(node.Right) {
		return true
	}

	if node.Kind == ast.KindForIn && node.Extra != 0 {
		if opt.hasGotoOrLabel(ast.NodeID(node.Extra)) {
			return true
		}
	}

	for i := range node.Count {
		if opt.hasGotoOrLabel(opt.tree.ExtraList[node.Extra+uint32(i)]) {
			return true
		}
	}

	return false
}

func (opt *Optimizer) eliminateDeadCode(nodeID ast.NodeID) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	if node.Kind == ast.KindBlock {
		hasJump := false

		for i := range node.Count {
			if opt.hasGotoOrLabel(opt.tree.ExtraList[node.Extra+uint32(i)]) {
				hasJump = true
				break
			}
		}

		var newStmts []ast.NodeID
		changed := false
		unreachable := false

		for i := range node.Count {
			childID := opt.tree.ExtraList[node.Extra+uint32(i)]

			opt.eliminateDeadCode(childID)

			if unreachable && !hasJump {
				changed = true
				continue
			}

			childNode := opt.tree.Nodes[childID]

			if childNode.Kind == ast.KindIf {
				condNode := opt.tree.Nodes[childNode.Left]

				if condNode.Kind == ast.KindTrue {
					blockNode := opt.tree.Nodes[childNode.Right]

					for j := range blockNode.Count {
						stmtID := opt.tree.ExtraList[blockNode.Extra+uint32(j)]
						newStmts = append(newStmts, stmtID)
						opt.tree.Nodes[stmtID].Parent = nodeID
					}

					changed = true
					continue
				}

				if condNode.Kind == ast.KindFalse {
					if childNode.Count == 0 {
						changed = true
						continue
					}

					if childNode.Count == 1 {
						elseID := opt.tree.ExtraList[childNode.Extra]
						elseNode := opt.tree.Nodes[elseID]

						if elseNode.Kind == ast.KindElse {
							blockNode := opt.tree.Nodes[elseNode.Left]

							for j := range blockNode.Count {
								stmtID := opt.tree.ExtraList[blockNode.Extra+uint32(j)]
								newStmts = append(newStmts, stmtID)
								opt.tree.Nodes[stmtID].Parent = nodeID
							}

							changed = true
							continue
						}
					}
				}
			}

			if childNode.Kind == ast.KindWhile {
				condNode := opt.tree.Nodes[childNode.Left]

				if condNode.Kind == ast.KindFalse {
					changed = true
					continue
				}
			}

			newStmts = append(newStmts, childID)

			if childNode.Kind == ast.KindReturn || childNode.Kind == ast.KindBreak {
				unreachable = true
			}
		}

		if changed {
			extraStart := uint32(len(opt.tree.ExtraList))
			opt.tree.ExtraList = append(opt.tree.ExtraList, newStmts...)

			opt.tree.Nodes[nodeID].Extra = extraStart
			opt.tree.Nodes[nodeID].Count = uint16(len(newStmts))

			for _, stmtID := range newStmts {
				opt.tree.Nodes[stmtID].Parent = nodeID
			}
		}
	} else {
		opt.eliminateDeadCode(node.Left)
		opt.eliminateDeadCode(node.Right)

		if node.Kind == ast.KindForIn && node.Extra != 0 {
			opt.eliminateDeadCode(ast.NodeID(node.Extra))
		}

		if node.Count > 0 && node.Kind != ast.KindFile {
			for i := range node.Count {
				opt.eliminateDeadCode(opt.tree.ExtraList[node.Extra+uint32(i)])
			}
		}
	}
}

func (opt *Optimizer) simplifyCitizenCalls(nodeID ast.NodeID) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	if node.Kind == ast.KindBlock {
		for i := range node.Count {
			opt.simplifyCitizenCalls(opt.tree.ExtraList[node.Extra+uint32(i)])
		}

		return
	}

	opt.simplifyCitizenCalls(node.Left)
	opt.simplifyCitizenCalls(node.Right)

	if node.Kind == ast.KindForIn && node.Extra != 0 {
		opt.simplifyCitizenCalls(ast.NodeID(node.Extra))
	}

	if node.Count > 0 && node.Kind != ast.KindFile {
		for i := range node.Count {
			opt.simplifyCitizenCalls(opt.tree.ExtraList[node.Extra+uint32(i)])
		}
	}

	if node.Kind == ast.KindCallExpr && node.Left != ast.InvalidNode {
		calleeNode := opt.tree.Nodes[node.Left]

		if calleeNode.Kind == ast.KindMemberExpr {
			baseNode := opt.tree.Nodes[calleeNode.Left]
			rightNode := opt.tree.Nodes[calleeNode.Right]

			if baseNode.Kind == ast.KindIdent && rightNode.Kind == ast.KindIdent {
				if _, isLocal := opt.identMap[calleeNode.Left]; !isLocal {
					if string(opt.tree.Source[baseNode.Start:baseNode.End]) == "Citizen" {
						rightName := utils.String(opt.tree.Source[rightNode.Start:rightNode.End])

						if rightName == "Wait" || rightName == "CreateThread" {
							opt.tree.Nodes[node.Left] = ast.Node{
								Kind:   ast.KindIdent,
								Start:  rightNode.Start,
								End:    rightNode.End,
								Parent: nodeID,
							}
						}
					}
				}
			}
		}
	}
}

func (opt *Optimizer) renameCallTargets(nodeID ast.NodeID) {
	if nodeID == ast.InvalidNode {
		return
	}

	node := opt.tree.Nodes[nodeID]

	if node.Kind == ast.KindBlock {
		for i := range node.Count {
			opt.renameCallTargets(opt.tree.ExtraList[node.Extra+uint32(i)])
		}

		return
	}

	opt.renameCallTargets(node.Left)
	opt.renameCallTargets(node.Right)

	if node.Kind == ast.KindForIn && node.Extra != 0 {
		opt.renameCallTargets(ast.NodeID(node.Extra))
	}

	if node.Count > 0 && node.Kind != ast.KindFile {
		for i := range node.Count {
			opt.renameCallTargets(opt.tree.ExtraList[node.Extra+uint32(i)])
		}
	}

	if node.Kind == ast.KindCallExpr && node.Left != ast.InvalidNode {
		calleeNode := opt.tree.Nodes[node.Left]

		if calleeNode.Kind == ast.KindIdent {
			if _, isLocal := opt.identMap[node.Left]; !isLocal {
				if newName, ok := opt.renameCalls[utils.String(opt.tree.Source[calleeNode.Start:calleeNode.End])]; ok {
					start := uint32(len(opt.tree.Source))
					opt.tree.Source = append(opt.tree.Source, newName...)
					end := uint32(len(opt.tree.Source))

					opt.tree.Nodes[node.Left] = ast.Node{
						Kind:   ast.KindIdent,
						Start:  start,
						End:    end,
						Parent: nodeID,
					}
				}
			}
		}
	}
}

func joaat(b []byte) int32 {
	var h uint32

	for i := 0; i < len(b); i++ {
		c := b[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}

		h += uint32(c)
		h += h << 10
		h ^= h >> 6
	}

	h += h << 3
	h ^= h >> 11
	h += h << 15

	return int32(h)
}
