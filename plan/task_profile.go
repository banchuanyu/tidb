// Copyright 2017 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package plan

import "github.com/pingcap/tidb/expression"

// taskProfile is a new version of `PhysicalPlanInfo`. It stores cost information for a task.
// A task may be CopTask, RootTask, MPPTask or a ParallelTask.
type taskProfile interface {
	attachPlan(p PhysicalPlan) taskProfile
	setCount(cnt uint64)
	count() uint64
	setCost(cost float64)
	cost() float64
	copy() taskProfile
}

// TODO: In future, we should split copTask to indexTask and tableTask.
// copTaskProfile is a profile for a task running a distributed kv store.
type copTaskProfile struct {
	indexPlan     PhysicalPlan
	tablePlan     PhysicalPlan
	cst           float64
	cnt           uint64
	addPlan2Index bool
}

func (t *copTaskProfile) setCount(cnt uint64) {
	t.cnt = cnt
}

func (t *copTaskProfile) count() uint64 {
	return t.cnt
}

func (t *copTaskProfile) setCost(cst float64) {
	t.cst = cst
}

func (t *copTaskProfile) cost() float64 {
	return t.cst
}

func (t *copTaskProfile) copy() taskProfile {
	return &copTaskProfile{
		indexPlan:     t.indexPlan,
		tablePlan:     t.tablePlan,
		cst:           t.cst,
		cnt:           t.cnt,
		addPlan2Index: t.addPlan2Index,
	}
}

func (t *copTaskProfile) attachPlan(p PhysicalPlan) taskProfile {
	nt := t.copy().(*copTaskProfile)
	if nt.addPlan2Index {
		p.SetChildren(nt.indexPlan)
		nt.indexPlan = p
	} else {
		p.SetChildren(nt.tablePlan)
		nt.tablePlan = p
	}
	return nt
}

func (t *copTaskProfile) finishTask() {
	t.cst += float64(t.cnt) * netWorkFactor
	if t.tablePlan != nil && t.indexPlan != nil {
		t.cst += float64(t.cnt) * netWorkFactor
	}
}

// rootTaskProfile is a profile running on tidb with single goroutine.
type rootTaskProfile struct {
	plan PhysicalPlan
	cst  float64
	cnt  uint64
}

func (t *rootTaskProfile) copy() taskProfile {
	return &rootTaskProfile{
		plan: t.plan,
		cst:  t.cst,
		cnt:  t.cnt,
	}
}

func (t *rootTaskProfile) attachPlan(p PhysicalPlan) taskProfile {
	nt := t.copy().(*rootTaskProfile)
	p.SetChildren(nt.plan)
	nt.plan = p
	return nt
}

func (t *rootTaskProfile) setCount(cnt uint64) {
	t.cnt = cnt
}

func (t *rootTaskProfile) count() uint64 {
	return t.cnt
}

func (t *rootTaskProfile) setCost(cst float64) {
	t.cst = cst
}

func (t *rootTaskProfile) cost() float64 {
	return t.cst
}

func (limit *Limit) attach2TaskProfile(profiles ...taskProfile) taskProfile {
	profile := profiles[0].attachPlan(limit)
	profile.setCount(limit.Count)
	return profile
}

func (sel *Selection) attach2TaskProfile(profiles ...taskProfile) taskProfile {
	profile := profiles[0].copy()
	switch t := profile.(type) {
	case *copTaskProfile:
		if t.addPlan2Index {
			var indexSel, tableSel *Selection
			if t.tablePlan != nil {
				indexSel, tableSel = sel.splitSelectionByIndexColumns(t.indexPlan.Schema())
			} else {
				indexSel = sel
			}
			if indexSel != nil {
				indexSel.SetChildren(t.indexPlan)
				t.indexPlan = indexSel
				t.cst += float64(t.cnt) * cpuFactor
			}
			if tableSel != nil {
				tableSel.SetChildren(t.tablePlan)
				t.tablePlan = tableSel
				t.addPlan2Index = false
				t.cst += float64(t.cnt) * cpuFactor
			}
		} else {
			sel.SetChildren(t.tablePlan)
			t.tablePlan = sel
			t.cst += float64(t.cnt) * cpuFactor
		}
		t.cnt = uint64(float64(t.cnt) * selectionFactor)
	case *rootTaskProfile:
		t.cst += float64(t.cnt) * cpuFactor
		t.cnt = uint64(float64(t.cnt) * selectionFactor)
	}
	return profile
}

func (sel *Selection) splitSelectionByIndexColumns(schema *expression.Schema) (indexSel *Selection, tableSel *Selection) {
	conditions := sel.Conditions
	var tableConds []expression.Expression
	for i := len(conditions) - 1; i >= 0; i-- {
		cols := expression.ExtractColumns(conditions[i])
		indices := schema.ColumnsIndices(cols)
		if indices == nil {
			tableConds = append(tableConds, conditions[i])
			conditions = append(conditions[:i], conditions[i+1:]...)
		}
	}
	if len(conditions) != 0 {
		indexSel = sel
	}
	if len(tableConds) != 0 {
		tableSel = &Selection{
			baseLogicalPlan: newBaseLogicalPlan(Sel, sel.allocator),
			Conditions:      tableConds,
		}
		tableSel.self = tableSel
		tableSel.initIDAndContext(sel.ctx)
	}
	return
}