//  Copyright (c) 2014 Couchbase, Inc.
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the
//  License. You may obtain a copy of the License at
//    http://www.apache.org/licenses/LICENSE-2.0
//  Unless required by applicable law or agreed to in writing,
//  software distributed under the License is distributed on an "AS
//  IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
//  express or implied. See the License for the specific language
//  governing permissions and limitations under the License.

package cbgt

import (
	"fmt"
	"hash/crc32"
	"io"
	"sort"
	"strings"
	"sync/atomic"

	log "github.com/couchbase/clog"
	"github.com/couchbaselabs/blance"
)

// NOTE: You *must* update VERSION if the planning algorithm or config
// data schema changes, following semver rules.

// PlannerNOOP sends a synchronous NOOP request to the manager's planner, if any.
func (mgr *Manager) PlannerNOOP(msg string) {
	atomic.AddUint64(&mgr.stats.TotPlannerNOOP, 1)

	if mgr.tagsMap == nil || mgr.tagsMap["planner"] {
		syncWorkReq(mgr.plannerCh, WORK_NOOP, msg, nil)
	}
}

// PlannerKick synchronously kicks the manager's planner, if any.
func (mgr *Manager) PlannerKick(msg string) {
	atomic.AddUint64(&mgr.stats.TotPlannerKick, 1)

	if mgr.tagsMap == nil || mgr.tagsMap["planner"] {
		syncWorkReq(mgr.plannerCh, WORK_KICK, msg, nil)
	}
}

// PlannerLoop is the main loop for the planner.
func (mgr *Manager) PlannerLoop() {
	if mgr.cfg != nil { // Might be nil for testing.
		go func() {
			ec := make(chan CfgEvent)
			mgr.cfg.Subscribe(INDEX_DEFS_KEY, ec)
			mgr.cfg.Subscribe(CfgNodeDefsKey(NODE_DEFS_WANTED), ec)
			for e := range ec {
				atomic.AddUint64(&mgr.stats.TotPlannerSubscriptionEvent, 1)
				mgr.PlannerKick("cfg changed, key: " + e.Key)
			}
		}()
	}

	for m := range mgr.plannerCh {
		var err error
		if m.op == WORK_KICK {
			atomic.AddUint64(&mgr.stats.TotPlannerKickStart, 1)
			changed, err := mgr.PlannerOnce(m.msg)
			if err != nil {
				log.Printf("planner: PlannerOnce, err: %v", err)
				atomic.AddUint64(&mgr.stats.TotPlannerKickErr, 1)
				// Keep looping as perhaps it's a transient issue.
			} else {
				if changed {
					atomic.AddUint64(&mgr.stats.TotPlannerKickChanged, 1)
					mgr.JanitorKick("the plans have changed")
				}
				atomic.AddUint64(&mgr.stats.TotPlannerKickOk, 1)
			}
		} else if m.op == WORK_NOOP {
			atomic.AddUint64(&mgr.stats.TotPlannerNOOPOk, 1)
		} else {
			err = fmt.Errorf("planner: unknown op: %s, m: %#v", m.op, m)
			atomic.AddUint64(&mgr.stats.TotPlannerUnknownErr, 1)
		}
		if m.resCh != nil {
			if err != nil {
				m.resCh <- err
			}
			close(m.resCh)
		}
	}
}

// PlannerOnce is the main body of a PlannerLoop.
func (mgr *Manager) PlannerOnce(reason string) (bool, error) {
	log.Printf("planner: awakes, reason: %s", reason)

	if mgr.cfg == nil { // Can occur during testing.
		return false, fmt.Errorf("planner: skipped due to nil cfg")
	}
	err := PlannerCheckVersion(mgr.cfg, mgr.version)
	if err != nil {
		return false, err
	}
	indexDefs, err := PlannerGetIndexDefs(mgr.cfg, mgr.version)
	if err != nil {
		return false, err
	}
	nodeDefs, err := PlannerGetNodeDefs(mgr.cfg, mgr.version, mgr.uuid)
	if err != nil {
		return false, err
	}
	planPIndexesPrev, cas, err :=
		PlannerGetPlanPIndexes(mgr.cfg, mgr.version)
	if err != nil {
		return false, err
	}

	planPIndexes, err := CalcPlan(indexDefs, nodeDefs,
		planPIndexesPrev, mgr.version, mgr.server)
	if err != nil {
		return false, fmt.Errorf("planner: CalcPlan, err: %v", err)
	}
	if SamePlanPIndexes(planPIndexes, planPIndexesPrev) {
		return false, nil
	}
	_, err = CfgSetPlanPIndexes(mgr.cfg, planPIndexes, cas)
	if err != nil {
		return false, fmt.Errorf("planner: could not save new plan,"+
			" perhaps a concurrent planner won, cas: %d, err: %v",
			cas, err)
	}
	return true, nil
}

// PlannerCheckVersion errors if a version string is too low.
func PlannerCheckVersion(cfg Cfg, version string) error {
	ok, err := CheckVersion(cfg, version)
	if err != nil {
		return fmt.Errorf("planner: CheckVersion err: %v", err)
	}
	if !ok {
		return fmt.Errorf("planner: version too low: %v", version)
	}
	return nil
}

// PlannerGetIndexDefs retrives index definitions from a Cfg.
func PlannerGetIndexDefs(cfg Cfg, version string) (*IndexDefs, error) {
	indexDefs, _, err := CfgGetIndexDefs(cfg)
	if err != nil {
		return nil, fmt.Errorf("planner: CfgGetIndexDefs err: %v", err)
	}
	if indexDefs == nil {
		return nil, fmt.Errorf("planner: ended since no IndexDefs")
	}
	if VersionGTE(version, indexDefs.ImplVersion) == false {
		return nil, fmt.Errorf("planner: indexDefs.ImplVersion: %s"+
			" > version: %s", indexDefs.ImplVersion, version)
	}
	return indexDefs, nil
}

// PlannerGetNodeDefs retrieves node definitions from a Cfg.
func PlannerGetNodeDefs(cfg Cfg, version, uuid string) (
	*NodeDefs, error) {
	nodeDefs, _, err := CfgGetNodeDefs(cfg, NODE_DEFS_WANTED)
	if err != nil {
		return nil, fmt.Errorf("planner: CfgGetNodeDefs err: %v", err)
	}
	if nodeDefs == nil {
		return nil, fmt.Errorf("planner: ended since no NodeDefs")
	}
	if VersionGTE(version, nodeDefs.ImplVersion) == false {
		return nil, fmt.Errorf("planner: nodeDefs.ImplVersion: %s"+
			" > version: %s", nodeDefs.ImplVersion, version)
	}
	nodeDef, exists := nodeDefs.NodeDefs[uuid]
	if !exists || nodeDef == nil {
		return nil, fmt.Errorf("planner: no NodeDef, uuid: %s", uuid)
	}
	if nodeDef.ImplVersion != version {
		return nil, fmt.Errorf("planner: ended since NodeDef, uuid: %s,"+
			" NodeDef.ImplVersion: %s != version: %s",
			uuid, nodeDef.ImplVersion, version)
	}
	if nodeDef.UUID != uuid {
		return nil, fmt.Errorf("planner: ended since NodeDef, uuid: %s,"+
			" NodeDef.UUID: %s != uuid: %s",
			uuid, nodeDef.UUID, uuid)
	}
	isPlanner := true
	if nodeDef.Tags != nil && len(nodeDef.Tags) > 0 {
		isPlanner = false
		for _, tag := range nodeDef.Tags {
			if tag == "planner" {
				isPlanner = true
			}
		}
	}
	if !isPlanner {
		return nil, fmt.Errorf("planner: ended since node, uuid: %s,"+
			" is not a planner, tags: %#v", uuid, nodeDef.Tags)
	}
	return nodeDefs, nil
}

// PlannerGetPlanPIndexes retrieves the planned pindexes from a Cfg.
func PlannerGetPlanPIndexes(cfg Cfg, version string) (
	*PlanPIndexes, uint64, error) {
	planPIndexesPrev, cas, err := CfgGetPlanPIndexes(cfg)
	if err != nil {
		return nil, 0, fmt.Errorf("planner: CfgGetPlanPIndexes err: %v", err)
	}
	if planPIndexesPrev == nil {
		planPIndexesPrev = NewPlanPIndexes(version)
	}
	if VersionGTE(version, planPIndexesPrev.ImplVersion) == false {
		return nil, 0, fmt.Errorf("planner: planPIndexesPrev.ImplVersion: %s"+
			" > version: %s", planPIndexesPrev.ImplVersion, version)
	}
	return planPIndexesPrev, cas, nil
}

// Split logical indexes into PIndexes and assign PIndexes to nodes.
func CalcPlan(indexDefs *IndexDefs, nodeDefs *NodeDefs,
	planPIndexesPrev *PlanPIndexes, version, server string) (
	*PlanPIndexes, error) {
	// This simple planner assigns at most MaxPartitionsPerPIndex
	// number of partitions onto a PIndex.  And then uses blance to
	// assign the PIndex to 1 or more nodes (based on NumReplicas).
	if indexDefs == nil || nodeDefs == nil {
		return nil, nil
	}

	nodeUUIDsAll, nodeUUIDsToAdd, nodeUUIDsToRemove,
		nodeWeights, nodeHierarchy :=
		CalcNodesLayout(indexDefs, nodeDefs, planPIndexesPrev)

	planPIndexes := NewPlanPIndexes(version)

	// Examine every indexDef...
	for _, indexDef := range indexDefs.IndexDefs {
		if indexDef.PlanParams.PlanFrozen {
			// If the planner is frozen, just copy over
			// the previous plan for this index.
			if planPIndexesPrev != nil {
				for planPIndexNamePrev, planPIndexPrev := range planPIndexesPrev.PlanPIndexes {
					if planPIndexPrev.IndexName == indexDef.Name &&
						planPIndexPrev.IndexUUID == indexDef.UUID {
						planPIndexes.PlanPIndexes[planPIndexNamePrev] =
							planPIndexPrev
					}
				}
			}
			continue
		}

		// Split each indexDef into 1 or more PlanPIndexes.
		pindexImplType, exists := PIndexImplTypes[indexDef.Type]
		if !exists ||
			pindexImplType == nil ||
			pindexImplType.New == nil ||
			pindexImplType.Open == nil {
			// Skip indexDef's with no instantiatable pindexImplType,
			// such as index aliases.
			continue
		}

		planPIndexesForIndex, err :=
			SplitIndexDefIntoPlanPIndexes(indexDef, server, planPIndexes)
		if err != nil {
			log.Printf("planner: could not SplitIndexDefIntoPlanPIndexes,"+
				" indexDef: %#v, server: %s, err: %v", indexDef, server, err)
			continue // Keep planning the other IndexDefs.
		}

		// Once we have a 1 or more PlanPIndexes for an IndexDef, use
		// blance to assign the PlanPIndexes to nodes, depending on
		// the numReplicas setting.
		warnings := BlancePlanPIndexes(indexDef,
			planPIndexesForIndex, planPIndexesPrev,
			nodeUUIDsAll, nodeUUIDsToAdd, nodeUUIDsToRemove,
			nodeWeights, nodeHierarchy)
		planPIndexes.Warnings[indexDef.Name] = warnings

		for _, warning := range warnings {
			log.Printf("planner: indexDef.Name: %s,"+
				" PlanNextMap warning: %s, indexDef: %#v",
				indexDef.Name, warning, indexDef)
		}
	}

	return planPIndexes, nil
}

// CalcNodesLayout computes information about the nodes based on the
// index definitions, node definitions, and the current plan.
func CalcNodesLayout(indexDefs *IndexDefs, nodeDefs *NodeDefs,
	planPIndexesPrev *PlanPIndexes) (
	nodeUUIDsAll []string,
	nodeUUIDsToAdd []string,
	nodeUUIDsToRemove []string,
	nodeWeights map[string]int,
	nodeHierarchy map[string]string,
) {
	// Retrieve nodeUUID's, weights, and hierarchy from the current nodeDefs.
	nodeUUIDs := make([]string, 0)
	nodeWeights = make(map[string]int)
	nodeHierarchy = make(map[string]string)
	for _, nodeDef := range nodeDefs.NodeDefs {
		tags := StringsToMap(nodeDef.Tags)
		// Consider only nodeDef's that can support pindexes.
		if tags == nil || tags["pindex"] {
			nodeUUIDs = append(nodeUUIDs, nodeDef.UUID)

			if nodeDef.Weight > 0 {
				nodeWeights[nodeDef.UUID] = nodeDef.Weight
			}

			child := nodeDef.UUID
			for _, ancestor := range strings.Split(nodeDef.Container, "/") {
				if child != "" && ancestor != "" {
					nodeHierarchy[child] = ancestor
				}
				child = ancestor
			}
		}
	}

	// Retrieve nodeUUID's from the previous plan.
	nodeUUIDsPrev := make([]string, 0)
	if planPIndexesPrev != nil {
		for _, planPIndexPrev := range planPIndexesPrev.PlanPIndexes {
			for nodeUUIDPrev := range planPIndexPrev.Nodes {
				nodeUUIDsPrev = append(nodeUUIDsPrev, nodeUUIDPrev)
			}
		}
	}

	// Dedupe.
	nodeUUIDsPrev = StringsIntersectStrings(nodeUUIDsPrev, nodeUUIDsPrev)

	// Calculate node deltas (nodes added & nodes removed).
	nodeUUIDsAll = make([]string, 0)
	nodeUUIDsAll = append(nodeUUIDsAll, nodeUUIDs...)
	nodeUUIDsAll = append(nodeUUIDsAll, nodeUUIDsPrev...)
	nodeUUIDsAll = StringsIntersectStrings(nodeUUIDsAll, nodeUUIDsAll) // Dedupe.
	nodeUUIDsToAdd = StringsRemoveStrings(nodeUUIDsAll, nodeUUIDsPrev)
	nodeUUIDsToRemove = StringsRemoveStrings(nodeUUIDsAll, nodeUUIDs)

	sort.Strings(nodeUUIDsAll)
	sort.Strings(nodeUUIDsToAdd)
	sort.Strings(nodeUUIDsToRemove)

	return nodeUUIDsAll, nodeUUIDsToAdd, nodeUUIDsToRemove,
		nodeWeights, nodeHierarchy
}

// Split an IndexDef into 1 or more PlanPIndex'es, assigning data
// source partitions from the IndexDef to a PlanPIndex based on
// modulus of MaxPartitionsPerPIndex.
//
// NOTE: If MaxPartitionsPerPIndex isn't a clean divisor of the total
// number of data source partitions (like 1024 split into clumps of
// 10), then one PIndex assigned to the remainder will be smaller than
// the other PIndexes (such as having only a remainder of 4 partitions
// rather than the usual 10 partitions per PIndex).
func SplitIndexDefIntoPlanPIndexes(indexDef *IndexDef, server string,
	planPIndexesOut *PlanPIndexes) (
	map[string]*PlanPIndex, error) {
	maxPartitionsPerPIndex := indexDef.PlanParams.MaxPartitionsPerPIndex

	sourcePartitionsArr, err := DataSourcePartitions(indexDef.SourceType,
		indexDef.SourceName, indexDef.SourceUUID, indexDef.SourceParams,
		server)
	if err != nil {
		return nil, fmt.Errorf("planner: could not get partitions,"+
			" indexDef: %#v, server: %s, err: %v", indexDef, server, err)
	}

	planPIndexesForIndex := map[string]*PlanPIndex{}

	addPlanPIndex := func(sourcePartitionsCurr []string) {
		sourcePartitions := strings.Join(sourcePartitionsCurr, ",")

		planPIndex := &PlanPIndex{
			Name:             PlanPIndexName(indexDef, sourcePartitions),
			UUID:             NewUUID(),
			IndexType:        indexDef.Type,
			IndexName:        indexDef.Name,
			IndexUUID:        indexDef.UUID,
			IndexParams:      indexDef.Params,
			SourceType:       indexDef.SourceType,
			SourceName:       indexDef.SourceName,
			SourceUUID:       indexDef.SourceUUID,
			SourceParams:     indexDef.SourceParams,
			SourcePartitions: sourcePartitions,
			Nodes:            make(map[string]*PlanPIndexNode),
		}

		planPIndexesOut.PlanPIndexes[planPIndex.Name] = planPIndex

		planPIndexesForIndex[planPIndex.Name] = planPIndex
	}

	sourcePartitionsCurr := []string{}
	for _, sourcePartition := range sourcePartitionsArr {
		sourcePartitionsCurr = append(sourcePartitionsCurr, sourcePartition)
		if maxPartitionsPerPIndex > 0 &&
			len(sourcePartitionsCurr) >= maxPartitionsPerPIndex {
			addPlanPIndex(sourcePartitionsCurr)
			sourcePartitionsCurr = []string{}
		}
	}

	if len(sourcePartitionsCurr) > 0 || // Assign any leftover partitions.
		len(planPIndexesForIndex) <= 0 { // Assign at least 1 PlanPIndex.
		addPlanPIndex(sourcePartitionsCurr)
	}

	return planPIndexesForIndex, nil
}

// BlancePartitionModel returns a blance library PartitionModel and
// model constraints based on an input index definition.
func BlancePartitionModel(indexDef *IndexDef) (
	model blance.PartitionModel,
	modelConstraints map[string]int,
) {
	// We're using multiple model states to better utilize blance's
	// node hierarchy features (shelf/rack/zone/row awareness).
	return blance.PartitionModel{
		"primary": &blance.PartitionModelState{
			Priority:    0,
			Constraints: 1,
		},
		"replica": &blance.PartitionModelState{
			Priority:    1,
			Constraints: indexDef.PlanParams.NumReplicas,
		},
	}, map[string]int(nil)
}

// BlancePlanPIndexes invokes the blance library's generic
// PlanNextMap() algorithm to create a new pindex layout plan.
func BlancePlanPIndexes(indexDef *IndexDef,
	planPIndexesForIndex map[string]*PlanPIndex,
	planPIndexesPrev *PlanPIndexes,
	nodeUUIDsAll []string,
	nodeUUIDsToAdd []string,
	nodeUUIDsToRemove []string,
	nodeWeights map[string]int,
	nodeHierarchy map[string]string) []string {
	model, modelConstraints := BlancePartitionModel(indexDef)

	// First, reconstruct previous blance map from planPIndexesPrev.
	blancePrevMap := blance.PartitionMap{}
	for _, planPIndex := range planPIndexesForIndex {
		blancePartition := &blance.Partition{
			Name:         planPIndex.Name,
			NodesByState: map[string][]string{},
		}
		blancePrevMap[planPIndex.Name] = blancePartition
		if planPIndexesPrev != nil {
			planPIndexPrev, exists :=
				planPIndexesPrev.PlanPIndexes[planPIndex.Name]
			if exists && planPIndexPrev != nil {
				// Sort by planPIndexNode.Priority for stability.
				planPIndexNodeRefs := PlanPIndexNodeRefs{}
				for nodeUUIDPrev, planPIndexNode := range planPIndexPrev.Nodes {
					planPIndexNodeRefs =
						append(planPIndexNodeRefs, &PlanPIndexNodeRef{
							UUID: nodeUUIDPrev,
							Node: planPIndexNode,
						})
				}
				sort.Sort(planPIndexNodeRefs)

				for _, planPIndexNodeRef := range planPIndexNodeRefs {
					state := "replica"
					if planPIndexNodeRef.Node.Priority <= 0 {
						state = "primary"
					}
					blancePartition.NodesByState[state] =
						append(blancePartition.NodesByState[state],
							planPIndexNodeRef.UUID)
				}
			}
		}
	}

	// TODO: Leverage blance's partition weight & state stickiness features.
	partitionWeights := map[string]int(nil)
	stateStickiness := map[string]int(nil)

	blanceNextMap, warnings := blance.PlanNextMap(blancePrevMap,
		nodeUUIDsAll, nodeUUIDsToRemove, nodeUUIDsToAdd,
		model, modelConstraints,
		partitionWeights,
		stateStickiness,
		nodeWeights,
		nodeHierarchy,
		indexDef.PlanParams.HierarchyRules)

	for planPIndexName, blancePartition := range blanceNextMap {
		planPIndex := planPIndexesForIndex[planPIndexName]
		planPIndex.Nodes = map[string]*PlanPIndexNode{}
		for _, nodeUUID := range blancePartition.NodesByState["primary"] {
			canRead := true
			canWrite := true
			nodePlanParam :=
				GetNodePlanParam(indexDef.PlanParams.NodePlanParams,
					nodeUUID, indexDef.Name, planPIndexName)
			if nodePlanParam != nil {
				canRead = nodePlanParam.CanRead
				canWrite = nodePlanParam.CanWrite
			}

			planPIndex.Nodes[nodeUUID] = &PlanPIndexNode{
				CanRead:  canRead,
				CanWrite: canWrite,
				Priority: 0,
			}
		}
		for i, nodeUUID := range blancePartition.NodesByState["replica"] {
			canRead := true
			canWrite := true
			nodePlanParam :=
				GetNodePlanParam(indexDef.PlanParams.NodePlanParams,
					nodeUUID, indexDef.Name, planPIndexName)
			if nodePlanParam != nil {
				canRead = nodePlanParam.CanRead
				canWrite = nodePlanParam.CanWrite
			}

			planPIndex.Nodes[nodeUUID] = &PlanPIndexNode{
				CanRead:  canRead,
				CanWrite: canWrite,
				Priority: i + 1,
			}
		}
	}

	return warnings
}

// NOTE: PlanPIndex.Name must be unique across the cluster and ideally
// functionally based off of the indexDef so that the SamePlanPIndex()
// comparison works even if concurrent planners are racing to
// calculate plans.
//
// NOTE: We can't use sourcePartitions directly as part of a
// PlanPIndex.Name suffix because in vbucket/hash partitioning the
// string would be too long -- since PIndexes might use
// PlanPIndex.Name for filesystem paths.
func PlanPIndexName(indexDef *IndexDef, sourcePartitions string) string {
	h := crc32.NewIEEE()
	io.WriteString(h, sourcePartitions)
	return indexDef.Name + "_" + indexDef.UUID + "_" +
		fmt.Sprintf("%x", h.Sum32())
}

// --------------------------------------------------------

// PlanPIndexNodeRef represents an assignment of a pindex to a node.
type PlanPIndexNodeRef struct {
	UUID string
	Node *PlanPIndexNode
}

// PlanPIndexNodeRefs represents assignments of pindexes to nodes.
type PlanPIndexNodeRefs []*PlanPIndexNodeRef

func (pms PlanPIndexNodeRefs) Len() int {
	return len(pms)
}

func (pms PlanPIndexNodeRefs) Less(i, j int) bool {
	return pms[i].Node.Priority < pms[j].Node.Priority
}

func (pms PlanPIndexNodeRefs) Swap(i, j int) {
	pms[i], pms[j] = pms[j], pms[i]
}
