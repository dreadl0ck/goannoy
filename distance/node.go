package distance

import "github.com/mariotoffia/goannoy/vector"

type Node[TV VectorType] interface {
	GetVector() [vector.ANNOYLIB_V_ARRAY_SIZE]TV
	SetVector(v [vector.ANNOYLIB_V_ARRAY_SIZE]TV)
	GetChildren() [vector.NUM_CHILDREN]int32
	SetChildren(c [vector.NUM_CHILDREN]int32)
	GetNumberOfDescendants() int32
	SetNumberOfDescendants(n int32)
	// Normalize will normalize the vector
	Normalize(vectorLength int)
	// CopyNodeTo will copy this Node contents to dst Node
	CopyNodeTo(dst Node[TV], vectorLength int)
	// InitNode will initialize the node. Depending on the implementation
	// it will do different things.
	InitNode(vectorLength int)
	// Distance calculates the distance from this to the _to_ `Node`.
	Distance(to Node[TV], vectorLength int) TV
	IsDataPoint() bool
}

// NodeImpl base type for all nodes
type NodeImpl[TV VectorType] struct {
	nDescendants int32
	v            [vector.ANNOYLIB_V_ARRAY_SIZE]TV
	children     [vector.NUM_CHILDREN]int32
}

func (n *NodeImpl[TV]) GetVector() [vector.ANNOYLIB_V_ARRAY_SIZE]TV {
	return n.v
}

func (n *NodeImpl[TV]) SetVector(v [vector.ANNOYLIB_V_ARRAY_SIZE]TV) {
	n.v = v
}

func (n *NodeImpl[TV]) GetChildren() [vector.NUM_CHILDREN]int32 {
	return n.children
}

func (n *NodeImpl[TV]) SetChildren(c [vector.NUM_CHILDREN]int32) {
	n.children = c
}

func (n *NodeImpl[TV]) GetNumberOfDescendants() int32 {
	return n.nDescendants
}

func (n *NodeImpl[TV]) SetNumberOfDescendants(nDescendants int32) {
	n.nDescendants = nDescendants
}

func (n *NodeImpl[TV]) IsDataPoint() bool {
	return n.nDescendants == 1
}

func (n *NodeImpl[TV]) Normalize(vectorLength int) {
	norm := vector.GetNorm(n.v, vectorLength)

	if norm > 0 {
		l := len(n.v)
		for i := 0; i < l; i++ {
			n.v[i] /= norm
		}
	}
}
