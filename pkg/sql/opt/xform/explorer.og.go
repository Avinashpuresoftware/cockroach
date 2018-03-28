// Code generated by optgen; DO NOT EDIT.

package xform

import (
	"github.com/cockroachdb/cockroach/pkg/sql/opt"
	"github.com/cockroachdb/cockroach/pkg/sql/opt/memo"
)

func (_e *explorer) exploreExpr(_state *exploreState, _eid memo.ExprID) (_fullyExplored bool) {
	_expr := _e.mem.Expr(_eid)
	switch _expr.Operator() {
	case opt.ScanOp:
		return _e.exploreScan(_state, _eid)
	}

	// No rules for other operator types.
	return true
}

func (_e *explorer) exploreScan(_state *exploreState, _eid memo.ExprID) (_fullyExplored bool) {
	_scanExpr := _e.mem.Expr(_eid).AsScan()
	_fullyExplored = true

	// [GenerateIndexScans]
	{
		if _eid.Expr >= _state.start {
			def := _scanExpr.Def()
			if _e.isPrimaryScan(def) {
				if _e.o.onRuleMatch == nil || _e.o.onRuleMatch(opt.GenerateIndexScans) {
					exprs := _e.generateIndexScans(def)
					for i := range exprs {
						_e.mem.MemoizeDenormExpr(_eid.Group, exprs[i])
					}
				}
			}
		}
	}

	return _fullyExplored
}
