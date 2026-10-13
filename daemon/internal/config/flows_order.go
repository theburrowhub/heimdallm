package config

import "slices"

// TopoAgentOrder lists the distinct agents of the nodes reachable from
// Start, in topological order (Kahn's algorithm, ties broken by node id) so
// listings that need "every agent this flow can pick" are deterministic.
func TopoAgentOrder(f FlowConfig) []string {
	reach := reachable(f)
	indeg := map[string]int{}
	for id := range reach {
		indeg[id] += 0
		for _, e := range f.Nodes[id].Edges() {
			if reach[e] {
				indeg[e]++
			}
		}
	}
	var ready []string
	for id, d := range indeg {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	var out []string
	seen := map[string]bool{}
	for len(ready) > 0 {
		slices.Sort(ready)
		id := ready[0]
		ready = ready[1:]
		n := f.Nodes[id]
		if n.Type == NodeAgent && !seen[n.Agent] {
			seen[n.Agent] = true
			out = append(out, n.Agent)
		}
		for _, e := range n.Edges() {
			if !reach[e] {
				continue
			}
			indeg[e]--
			if indeg[e] == 0 {
				ready = append(ready, e)
			}
		}
	}
	return out
}
