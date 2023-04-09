package index

import (
	"fmt"
	"io"
	"os"
	"unsafe"

	"github.com/mariotoffia/goannoy/interfaces"
	"github.com/mariotoffia/goannoy/utils"
)

// AnnoyIndexImpl is the actual index for all vectors.
//
// A Note from the author:
//
// We use random projection to build a forest of binary trees of all items.
// Basically just split the hyperspace into two sides by a hyperplane,
// then recursively split each of those subtrees etc.
// We create a tree like this q times. The default q is determined automatically
// in such a way that we at most use 2x as much memory as the vectors take.

type AnnoyIndexImpl[
	TV interfaces.VectorType,
	TR interfaces.RandomTypes] struct {
	vectorLength int
	// nodeSize the the complete size of the node in bytes.
	nodeSize int
	// _n_items is how many nodes exists in the index.
	_n_items       int
	_nodes         unsafe.Pointer
	_n_nodes       int
	_nodes_size    int
	_roots         []int
	maxDescendants int
	random         interfaces.Random[TR]
	indexLoaded    bool
	indexBuilt     bool
	distance       interfaces.Distance[TV, TR]
	buildPolicy    interfaces.AnnoyIndexBuildPolicy
	allocator      Allocator
}

func NewAnnoyIndexImpl[
	TV interfaces.VectorType,
	TR interfaces.RandomTypes](
	vectorLength int,
	random interfaces.Random[TR],
	distance interfaces.Distance[TV, TR],
	buildPolicy interfaces.AnnoyIndexBuildPolicy,
	allocator Allocator,
) *AnnoyIndexImpl[TV, TR] {
	// Create a single node to query it for sizes
	node := distance.NewNodeFromGC(vectorLength)

	index := &AnnoyIndexImpl[TV, TR]{
		vectorLength:   vectorLength,                      // _f
		random:         random,                            // _seed
		nodeSize:       node.Size(vectorLength),           // _s
		maxDescendants: node.MaxNumChildren(vectorLength), // _K
		indexBuilt:     false,                             // _built
		distance:       distance,
		allocator:      allocator,
		buildPolicy:    buildPolicy,
	}

	return index
}

// VectorLength returns the vector length of the index.
func (idx *AnnoyIndexImpl[TV, TR]) VectorLength() int {
	return idx.vectorLength
}

func (idx *AnnoyIndexImpl[TV, TR]) GetItemVector(itemIndex int) []TV {
	if !idx.indexLoaded {
		panic("Can't get items from an unloaded index")
	}

	return idx.getNode(itemIndex).GetVector(idx.vectorLength)
}

// AddItem adds an item to the index. The ownership of the vector _v_ is taken
// by this function. The _itemIndex_ is a numbering index of the _v_ vector and
// *SHOULD* be incremental. If same _itemIndex_ is added twice, the last one
// will be the one in the index.
func (idx *AnnoyIndexImpl[TV, TR]) AddItem(itemIndex int, v []TV) {
	if idx.indexLoaded {
		panic("Can't add items to a loaded index")
	}

	// Ensure that we have enough memory for the new node
	idx.allocateSize(itemIndex+1, nil)

	// Map the node onto the memory
	node := idx.getNode(itemIndex)

	// Initialize the node with the vector
	node.SetNumberOfDescendants(1)
	node.SetVector(v)
	node.InitNode(idx.vectorLength)

	// Is new spot?
	if itemIndex >= idx._n_items {
		idx._n_items = itemIndex + 1
	}
}

func (idx *AnnoyIndexImpl[TV, TR]) Build(numberOfTrees, nThreads int) {
	if idx.indexLoaded {
		panic("Can't build a loaded index")
	}

	if idx.indexBuilt {
		panic("Index already built")
	}

	// Give the preprocessor a chance to process the nodes before building the index
	idx.distance.PreProcess(
		idx._nodes,
		idx._n_items,
		idx.vectorLength,
	)

	idx._n_nodes = idx._n_items

	idx.buildPolicy.Build(idx, numberOfTrees, nThreads)

	// Also, copy the roots into the last segment of the array
	// This way we can load them faster without reading the whole file
	idx.allocateSize(idx._n_nodes+len(idx._roots), nil)

	for i := 0; i < len(idx._roots); i++ {
		dst := idx.getNode(idx._n_nodes + i)
		src := idx.getNode(i)

		src.CopyNodeTo(
			dst,
			idx.vectorLength,
		)
	}

	idx._n_nodes += len(idx._roots)
	idx.indexBuilt = true
}

// ThreadBuild is called from the build policy to build the index.
func (idx *AnnoyIndexImpl[TV, TR]) ThreadBuild(
	treesPerThread, threadIdx int,
	threadedBuildPolicy interfaces.AnnoyIndexBuildPolicy,
) {
	rnd := idx.random.CloneAndReset()

	// Each thread needs its own seed, otherwise each thread would be building the same tree(s)
	rnd.SetSeed(rnd.GetSeed() + TR(threadIdx))

	var threadRoots []int

	for {
		if treesPerThread == -1 {
			threadedBuildPolicy.LockNNodes()
			if idx._n_nodes >= 2*idx._n_items {
				threadedBuildPolicy.UnlockNNodes()
				break
			}
			threadedBuildPolicy.UnlockNNodes()
		} else {
			if len(threadRoots) >= treesPerThread {
				break
			}
		}

		var indices []int

		threadedBuildPolicy.LockSharedNodes()

		for i := 0; i < idx._n_items; i++ {
			node := idx.getNode(i)

			if node.GetNumberOfDescendants() >= 1 {
				indices = append(indices, i)
			}
		}

		threadedBuildPolicy.UnlockSharedNodes()

		threadRoots = append(
			threadRoots,
			idx.makeTree(indices, true, rnd, threadedBuildPolicy),
		)
	}

	threadedBuildPolicy.LockRoots()
	idx._roots = append(idx._roots, threadRoots...)
	threadedBuildPolicy.UnlockRoots()
}

func (idx *AnnoyIndexImpl[TV, TR]) Save(fileName string) {
	if !idx.indexBuilt {
		panic("Can't save an index that hasn't been built")
	}

	file, err := os.Create(fileName)
	if err != nil {
		panic(err)
	}

	defer file.Close()

	data := unsafe.Slice((*byte)(idx._nodes), idx._n_nodes*idx.nodeSize)

	_, err = file.Write(data)

	if err != nil {
		panic(err)
	}

	idx.unload()
	idx.Load(fileName)
}

func (idx *AnnoyIndexImpl[TV, TR]) Load(fileName string) {
	file, err := os.Open(fileName)
	if err != nil {
		panic(err)
	}

	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		panic(err)
	}

	fileSize := fileInfo.Size()

	if fileSize%int64(idx.nodeSize) != 0 {
		panic("File size is not a multiple of node size")
	}

	// TODO: Use mmap instead
	idx.allocateSize(int(fileSize)/idx.nodeSize, nil)

	idx._roots = nil
	idx._n_nodes = int(fileSize) / idx.nodeSize

	data := unsafe.Slice((*byte)(idx._nodes), idx._nodes_size*idx.nodeSize)

	var bytesRead int64
	for bytesRead < fileSize {
		n, err := io.ReadFull(file, data[bytesRead:])
		if err != nil && err != io.ErrUnexpectedEOF {
			panic(fmt.Sprintf("Failed to read file: %v", err))
		}
		bytesRead += int64(n)
	}

	if err != nil {
		panic(err)
	}

	m := -1

	for i := idx._n_nodes - 1; i >= 0; i++ {

		n := idx.getNode(i)
		k := n.GetNumberOfDescendants()

		if m == -1 || k == m {
			idx._roots = append(idx._roots, i)
			m = k
		} else {
			break
		}
	}

	// hacky fix: since the last root precedes the copy of all roots, delete it
	if len(idx._roots) > 1 {
		fn := idx.getNode(idx._roots[0])
		ln := idx.getNode(idx._roots[len(idx._roots)-1])

		if fn.GetChildren()[0] == ln.GetChildren()[0] {
			idx._roots = idx._roots[:len(idx._roots)-1]
		}
	}

	idx.indexBuilt = true
	idx.indexLoaded = true
	idx._n_items = m
}

func (idx *AnnoyIndexImpl[TV, TR]) unload() {
	idx.allocator.Free()
	idx.reinitialize()
}

func (idx *AnnoyIndexImpl[TV, TR]) getNode(index int) interfaces.Node[TV] {
	return idx.distance.MapNodeToMemory(idx._nodes, index, idx.vectorLength)
}

func (idx *AnnoyIndexImpl[TV, TR]) makeTree(
	indices []int, isRoot bool,
	rnd interfaces.Random[TR],
	threadedBuildPolicy interfaces.AnnoyIndexBuildPolicy,
) int {

	if len(indices) == 1 && !isRoot {
		return indices[0]
	}

	if len(indices) <= idx.maxDescendants &&
		(!isRoot || idx._n_items <= idx.maxDescendants || len(indices) == 1) {
		// Ensure we have memory for the new node
		threadedBuildPolicy.LockNNodes()
		idx.allocateSize(idx._n_nodes+1, threadedBuildPolicy)

		item := idx._n_nodes
		idx._n_nodes++
		threadedBuildPolicy.UnlockNNodes()

		threadedBuildPolicy.LockSharedNodes()

		m := idx.getNode(item)

		if isRoot {
			m.SetNumberOfDescendants(idx._n_items)
		} else {
			m.SetNumberOfDescendants(len(indices))
		}

		if len(indices) > 0 {
			children := make([]int, len(indices))
			copy(children, indices)

			m.SetChildren(children)
		}

		threadedBuildPolicy.UnlockSharedNodes()

		return item
	}

	threadedBuildPolicy.LockSharedNodes()

	var children []interfaces.Node[TV]

	for _, j := range indices {
		// TODO: original code did a check: Node* n = _get(j); if (n) {...}
		n := idx.getNode(j)
		children = append(children, n)
	}

	children_indices := [2][]int{}
	data := make([]byte, idx.nodeSize) // Need it since, gc won't remove it until scope end

	m := idx.distance.MapNodeToMemory(
		unsafe.Pointer(unsafe.SliceData(data)), 0, idx.vectorLength,
	)

	for attempt := 0; attempt < 3; attempt++ {
		children_indices[0] = nil
		children_indices[1] = nil

		idx.distance.CreateSplit(
			children,
			idx.vectorLength,
			idx.nodeSize,
			idx.random, m,
		)

		for _, j := range indices {
			// TODO: original code did a check: Node* n = _get(j); if (n) {...}
			n := idx.getNode(j)

			side := idx.distance.Side(
				m,
				n.GetVector(idx.vectorLength),
				idx.random,
				idx.vectorLength,
			)

			children_indices[side] = append(children_indices[side], j)
		}

		if idx.splitImbalance(
			children_indices[0],
			children_indices[1]) < 0.95 {
			break
		}
	}

	threadedBuildPolicy.UnlockSharedNodes()

	// If we didn't find a hyperplane, just randomize sides as a last option
	for {
		if idx.splitImbalance(children_indices[0], children_indices[1]) <= 0.99 {
			break
		}

		children_indices[0] = nil
		children_indices[1] = nil

		// Set the vector to 0.0
		m.SetVector(make([]TV, idx.vectorLength))

		for _, j := range indices {
			// Just randomize...
			side := idx.random.NextSide()
			children_indices[side] = append(children_indices[side], j)
		}
	}

	if isRoot {
		m.SetNumberOfDescendants(idx._n_items)
	} else {
		m.SetNumberOfDescendants(len(indices))
	}

	var flip int
	if len(children_indices[interfaces.SideLeft]) > len(children_indices[interfaces.SideRight]) {
		flip = 1
	}

	child_first := make([]int, 2)

	for side := 0; side < 2; side++ {
		// run makeTree for the smallest child first (for cache locality)
		flip_side := side ^ flip

		child_first[flip_side] = idx.makeTree(
			children_indices[flip_side],
			false,
			rnd,
			threadedBuildPolicy,
		)
	}

	m.SetChildren(child_first)

	idx.buildPolicy.LockNNodes()
	idx.allocateSize(idx._n_nodes+1, threadedBuildPolicy)
	item := idx._n_nodes
	idx._n_nodes++
	idx.buildPolicy.UnlockNNodes()

	idx.buildPolicy.LockSharedNodes()
	dst := idx.getNode(item)

	m.CopyNodeTo(dst, idx.vectorLength)
	idx.buildPolicy.UnlockSharedNodes()

	return item
}

func (idx *AnnoyIndexImpl[TV, TR]) splitImbalance(
	left_indices, right_indices []int) float64 {
	ls := float64(len(left_indices))
	rs := float64(len(right_indices))

	f := ls / (ls + rs + 1e-9) // Avoid 0/0
	return utils.MaxFloat64(f, 1-f)
}

func (idx *AnnoyIndexImpl[TV, TR]) allocateSize(
	size int,
	threadedBuildPolicy interfaces.AnnoyIndexBuildPolicy,
) {
	const reallocation_factor = float64(1.3)

	if size > idx._nodes_size {

		if threadedBuildPolicy != nil {
			threadedBuildPolicy.LockNodes()
		}

		new_node_size := utils.Max(size, int(float64(idx._nodes_size+1)*reallocation_factor))
		idx._nodes = idx.allocator.Reallocate(new_node_size * idx.nodeSize)
		idx._nodes_size = new_node_size

		if threadedBuildPolicy != nil {
			threadedBuildPolicy.UnlockNodes()
		}
	}
}

func (idx *AnnoyIndexImpl[TV, TR]) reinitialize() {
	idx._nodes = nil
	idx.indexLoaded = false
	idx._n_items = 0
	idx._n_nodes = 0
	idx._nodes_size = 0
	idx.random = idx.random.CloneAndReset()
	idx._roots = nil
}
