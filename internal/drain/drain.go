// Package drain learns log templates with Drain, the online log parser with a fixed-depth tree
// (Pinjia He, Jieming Zhu, Zibin Zheng and Michael R. Lyu, ICWS 2017).
//
// It follows the reference implementation in logpai/logparser step by step, including its
// handling of short messages and of full tree nodes, so that it groups messages exactly as the
// published Loghub results. A message is a list of tokens; the tree's first layer is the number
// of tokens, the next layers are the first tokens (tokens with digits share one wildcard branch),
// and a leaf holds the groups whose templates are compared with the message.
package drain

import "unicode"

// Wildcard marks the positions of a template where messages differ.
const Wildcard = "<*>"

// Options set how the tree is built and when a message joins a group.
type Options struct {
	// Depth counts the root and the length layer: 4 means that messages are routed by their
	// token count and their first two tokens. The reference implementation's default is 4.
	Depth int
	// Similarity is the share of a template's tokens a message must match to join its group.
	Similarity float64
	// MaxChildren caps the children of an inner node; later tokens share a wildcard child.
	MaxChildren int
}

// Cluster is one group of messages with the template they share.
type Cluster struct {
	ID       int      // 1, 2, 3... in the order the groups were created
	Template []string // the tokens all messages share, with Wildcard elsewhere
	Initial  []string // the tokens the group was created with; they place it in the tree
	N        int64    // messages in the group
}

type node struct {
	children map[string]*node
	clusters []*Cluster
}

// Parser learns templates from messages, one at a time.
type Parser struct {
	depth    int // the reference implementation's depth - 2
	st       float64
	maxChild int
	root     map[int]*node
	clusters []*Cluster
}

// New returns an empty parser. Zero options take the reference defaults: depth 4, similarity
// 0.4 and 100 children.
func New(o Options) *Parser {
	if o.Depth == 0 {
		o.Depth = 4
	}
	if o.Similarity == 0 {
		o.Similarity = 0.4
	}
	if o.MaxChildren == 0 {
		o.MaxChildren = 100
	}
	return &Parser{depth: o.Depth - 2, st: o.Similarity, maxChild: o.MaxChildren, root: map[int]*node{}}
}

// Clusters returns every group in the order it was created.
func (p *Parser) Clusters() []*Cluster { return p.clusters }

// Add puts a message into the group it matches, widening that group's template, or starts a new
// group. It reports whether the group is new.
func (p *Parser) Add(tokens []string) (*Cluster, bool) {
	if c := p.search(tokens); c != nil {
		for i, t := range tokens {
			if c.Template[i] != t {
				c.Template[i] = Wildcard
			}
		}
		c.N++
		return c, false
	}
	c := &Cluster{ID: len(p.clusters) + 1, Template: append([]string(nil), tokens...), Initial: append([]string(nil), tokens...), N: 1}
	p.clusters = append(p.clusters, c)
	p.attach(c.Initial, c)
	return c, true
}

// Restore re-creates a group learned earlier, such as one read back from a file. Groups must be
// restored in the order they were created, so that the tree is built exactly as it was.
func (p *Parser) Restore(initial, template []string, n int64) *Cluster {
	c := &Cluster{ID: len(p.clusters) + 1, Template: append([]string(nil), template...), Initial: append([]string(nil), initial...), N: n}
	p.clusters = append(p.clusters, c)
	p.attach(c.Initial, c)
	return c
}

func hasNumbers(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// search is the reference treeSearch: down the length layer and the first tokens, then the most
// similar group in the leaf.
func (p *Parser) search(seq []string) *Cluster {
	n := len(seq)
	parent, ok := p.root[n]
	if !ok {
		return nil
	}
	depth := 1
	for _, t := range seq {
		if depth >= p.depth || depth > n {
			break
		}
		if c, ok := parent.children[t]; ok {
			parent = c
		} else if c, ok := parent.children[Wildcard]; ok {
			parent = c
		} else {
			return nil
		}
		depth++
	}
	return p.fastMatch(parent.clusters, seq)
}

// attach is the reference addSeqToPrefixTree. As there, a message shorter than the tree's depth
// is never attached to a leaf, so each such message starts a group of its own.
func (p *Parser) attach(seq []string, c *Cluster) {
	n := len(seq)
	first, ok := p.root[n]
	if !ok {
		first = &node{}
		p.root[n] = first
	}
	parent := first
	depth := 1
	for _, t := range seq {
		if depth >= p.depth || depth > n {
			parent.clusters = append(parent.clusters, c)
			break
		}
		if child, ok := parent.children[t]; ok {
			parent = child
		} else if !hasNumbers(t) {
			if w, ok := parent.children[Wildcard]; ok {
				if len(parent.children) < p.maxChild {
					parent = parent.add(t)
				} else {
					parent = w
				}
			} else {
				switch {
				case len(parent.children)+1 < p.maxChild:
					parent = parent.add(t)
				case len(parent.children)+1 == p.maxChild:
					parent = parent.add(Wildcard)
				default:
					parent = parent.children[Wildcard]
				}
			}
		} else if w, ok := parent.children[Wildcard]; ok {
			parent = w
		} else {
			parent = parent.add(Wildcard)
		}
		depth++
	}
}

func (nd *node) add(t string) *node {
	if nd.children == nil {
		nd.children = map[string]*node{}
	}
	c := &node{}
	nd.children[t] = c
	return c
}

// fastMatch picks the most similar group; on a tie, the one with more wildcards.
func (p *Parser) fastMatch(cs []*Cluster, seq []string) *Cluster {
	maxSim, maxPar := -1.0, -1
	var best *Cluster
	for _, c := range cs {
		sim, par := seqDist(c.Template, seq)
		if sim > maxSim || (sim == maxSim && par > maxPar) {
			maxSim, maxPar, best = sim, par, c
		}
	}
	if maxSim >= p.st {
		return best
	}
	return nil
}

func seqDist(template, seq []string) (float64, int) {
	if len(template) == 0 {
		return 0, 0 // never happens: empty messages are never attached to a leaf
	}
	sim, par := 0, 0
	for i, t := range template {
		if t == Wildcard {
			par++
			continue
		}
		if t == seq[i] {
			sim++
		}
	}
	return float64(sim) / float64(len(template)), par
}
