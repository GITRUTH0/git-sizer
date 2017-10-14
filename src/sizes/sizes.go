package sizes

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// A count of something, capped at math.MaxUint64.
type Count uint64

// The type of an object ("blob", "tree", "commit", "tag", "missing").
type Type string

// Return the sum of two Counts, capped at math.MaxUint64.
func addCapped(n1, n2 Count) Count {
	n := n1 + n2
	if n < n1 {
		// Overflow
		return math.MaxUint64
	}
	return n
}

type Repository struct {
	path string

	batchCommand      *exec.Cmd
	batchStdin        io.WriteCloser
	batchStdoutWriter io.ReadCloser
	batchStdout       *bufio.Reader

	checkCommand      *exec.Cmd
	checkStdin        io.WriteCloser
	checkStdoutWriter io.ReadCloser
	checkStdout       *bufio.Reader
}

func NewRepository(path string) (*Repository, error) {
	batchCommand := exec.Command("git", "-C", path, "cat-file", "--batch")
	batchStdin, err := batchCommand.StdinPipe()
	if err != nil {
		return nil, err
	}
	batchStdout, err := batchCommand.StdoutPipe()
	if err != nil {
		return nil, err
	}
	err = batchCommand.Start()
	if err != nil {
		return nil, err
	}

	checkCommand := exec.Command("git", "-C", path, "cat-file", "--batch-check")
	checkStdin, err := checkCommand.StdinPipe()
	if err != nil {
		return nil, err
	}
	checkStdout, err := checkCommand.StdoutPipe()
	if err != nil {
		return nil, err
	}
	err = checkCommand.Start()
	if err != nil {
		return nil, err
	}

	return &Repository{
		path: path,

		batchCommand:      batchCommand,
		batchStdin:        batchStdin,
		batchStdoutWriter: batchStdout,
		batchStdout:       bufio.NewReader(batchStdout),

		checkCommand:      checkCommand,
		checkStdin:        checkStdin,
		checkStdoutWriter: checkStdout,
		checkStdout:       bufio.NewReader(checkStdout),
	}, nil
}

type Oid [20]byte

func NewOid(s string) (Oid, error) {
	oidBytes, err := hex.DecodeString(s)
	if err != nil {
		return Oid{}, err
	}
	if len(oidBytes) != 20 {
		return Oid{}, errors.New("hex oid has the wrong length")
	}
	var oid Oid
	copy(oid[0:20], oidBytes)
	return oid, nil
}

func (oid Oid) String() string {
	return hex.EncodeToString(oid[:])
}

func (repo *Repository) ReadHeader(oid Oid) (Type, Count, error) {
	fmt.Fprintf(repo.checkStdin, "%s\n", oid)
	header, err := repo.checkStdout.ReadString('\n')
	if err != nil {
		return "missing", 0, err
	}
	header = header[:len(header)-1]
	words := strings.Split(header, " ")
	switch words[1] {
	case "missing":
		return "missing", 0, errors.New(fmt.Sprintf("missing object %s", oid))
	default:
		size, err := strconv.ParseUint(words[2], 10, 0)
		if err != nil {
			return "missing", 0, err
		}
		return Type(words[1]), Count(size), nil
	}
}

type Tree struct {
	data []byte
}

func (repo *Repository) ReadTree(oid Oid) (*Tree, error) {
	fmt.Fprintf(repo.batchStdin, "%s\n", oid)
	header, err := repo.batchStdout.ReadString('\n')
	if err != nil {
		return nil, err
	}
	header = header[:len(header)-1]
	words := strings.Split(header, " ")
	switch words[1] {
	case "missing":
		return nil, errors.New(fmt.Sprintf("missing object %s", oid))
	case "tree":
		size, err := strconv.ParseUint(words[2], 10, 0)
		if err != nil {
			return nil, err
		}
		// +1 for LF:
		data := make([]byte, size+1)
		rest := data
		for len(rest) > 0 {
			n, err := repo.batchStdout.Read(rest)
			if err != nil {
				return nil, err
			}
			rest = rest[n:]
		}
		// remove LF:
		data = data[:len(data)-1]
		return &Tree{data}, nil
	default:
		return nil, errors.New(fmt.Sprintf("unexpected type %s for object %s", words[1], oid))
	}
}

type TreeEntry struct {
	Name     string
	Oid      Oid
	Type     Type
	Filemode uint
}

type TreeIter struct {
	// The as-yet-unread part of the tree's data.
	data []byte
}

func (tree *Tree) Iter() *TreeIter {
	return &TreeIter{
		data: tree.data,
	}
}

func (iter *TreeIter) NextEntry(entry *TreeEntry) (bool, error) {
	if len(iter.data) == 0 {
		return false, nil
	}

	spAt := bytes.IndexByte(iter.data, ' ')
	if spAt < 0 {
		return false, errors.New("failed to find SP after mode")
	}
	mode, err := strconv.ParseUint(string(iter.data[:spAt]), 8, 32)
	if err != nil {
		return false, err
	}
	entry.Filemode = uint(mode)

	iter.data = iter.data[spAt+1:]
	nulAt := bytes.IndexByte(iter.data, 0)
	if nulAt < 0 {
		return false, errors.New("failed to find NUL after filename")
	}

	entry.Name = string(iter.data[:nulAt])

	iter.data = iter.data[nulAt+1:]
	if len(iter.data) < 20 {
		return false, errors.New("tree entry ends unexpectedly")
	}

	copy(entry.Oid[0:20], iter.data[0:20])
	iter.data = iter.data[20:]

	return true, nil
}

type Size interface {
	fmt.Stringer
}

type BlobSize Count

func (s BlobSize) String() string {
	return fmt.Sprintf("blob_size=%d", Count(s))
}

type TreeSize struct {
	// The maximum depth of items starting at this object (including
	// this object).
	MaxDepth Count `json:"max_depth"`

	// The maximum length of any path relative to this object.
	MaxPathLength Count `json:"max_path_length"`

	// The total number of trees.
	TreeCount Count `json:"tree_count"`

	// The maximum number of entries an a tree.
	MaxTreeEntries Count `json:"max_tree_entries"`

	// The total number of blobs.
	BlobCount Count `json:"blob_count"`

	// The total size of all blobs.
	BlobSize Count `json:"blob_size"`

	// The total number of symbolic links.
	LinkCount Count `json:"link_count"`

	// The total number of submodules referenced.
	SubmoduleCount Count `json:"submodule_count"`
}

func (s *TreeSize) addDescendent(filename string, s2 TreeSize) {
	s.recordDepth(s2.MaxDepth)
	if s2.MaxPathLength > 0 {
		s.recordPathLength(addCapped(Count(len(filename))+1, s2.MaxPathLength))
	} else {
		s.recordPathLength(Count(len(filename)))
	}
	s.TreeCount = addCapped(s.TreeCount, s2.TreeCount)
	if s2.MaxTreeEntries > s.MaxTreeEntries {
		s.MaxTreeEntries = s2.MaxTreeEntries
	}
	s.BlobCount = addCapped(s.BlobCount, s2.BlobCount)
	s.BlobSize = addCapped(s.BlobSize, s2.BlobSize)
	s.LinkCount = addCapped(s.LinkCount, s2.LinkCount)
	s.SubmoduleCount = addCapped(s.SubmoduleCount, s2.SubmoduleCount)
}

// Set the object's MaxDepth to `max(s.MaxDepth, maxDepth)`.
func (s *TreeSize) recordDepth(maxDepth Count) {
	if maxDepth > s.MaxDepth {
		s.MaxDepth = maxDepth
	}
}

// Set the object's MaxPathLength to `max(s.MaxPathLength, pathLength)`.
func (s *TreeSize) recordPathLength(pathLength Count) {
	if pathLength > s.MaxPathLength {
		s.MaxPathLength = pathLength
	}
}

// Record that the object has a blob of the specified `size` as a
// direct descendant.
func (s *TreeSize) addBlob(filename string, size BlobSize) {
	s.recordDepth(1)
	s.recordPathLength(Count(len(filename)))
	s.BlobSize = addCapped(s.BlobSize, Count(size))
	s.BlobCount = addCapped(s.BlobCount, 1)
}

// Record that the object has a link as a direct descendant.
func (s *TreeSize) addLink(filename string) {
	s.recordDepth(1)
	s.recordPathLength(Count(len(filename)))
	s.LinkCount = addCapped(s.LinkCount, 1)
}

// Record that the object has a submodule as a direct descendant.
func (s *TreeSize) addSubmodule(filename string) {
	s.recordDepth(1)
	s.recordPathLength(Count(len(filename)))
	s.SubmoduleCount = addCapped(s.SubmoduleCount, 1)
}

func (s TreeSize) String() string {
	return fmt.Sprintf(
		"max_depth=%d, max_path_length=%d, tree_count=%d, max_tree_entries=%d, blob_count=%d, blob_size=%d, link_count=%d, submodule_count=%d",
		s.MaxDepth, s.MaxPathLength, s.TreeCount, s.MaxTreeEntries, s.BlobCount, s.BlobSize, s.LinkCount, s.SubmoduleCount,
	)
}

type ToDoList struct {
	list []Oid
}

func (t *ToDoList) Length() int {
	return len(t.list)
}

func (t *ToDoList) Push(oid Oid) {
	t.list = append(t.list, oid)
}

func (t *ToDoList) Peek() Oid {
	return t.list[len(t.list)-1]
}

func (t *ToDoList) Drop() {
	t.list = t.list[0 : len(t.list)-1]
}

func (t *ToDoList) Dump(w io.Writer) {
	fmt.Fprintf(w, "todo list has %d items\n", t.Length())
	for i, idString := range t.list {
		fmt.Fprintf(w, "%8d %s\n", i, idString)
	}
	fmt.Fprintf(w, "\n")
}

var NotYetKnown = errors.New("the size of an object is not yet known")

type SizeCache struct {
	repo *Repository

	// The (recursive) size of trees whose sizes have been computed so
	// far.
	treeSizes map[Oid]TreeSize

	// The size of blobs whose sizes have been looked up so far.
	blobSizes map[Oid]BlobSize

	// The OIDs of trees whose sizes are in the process of being
	// computed. This is, roughly, the call stack. As long as there
	// are no SHA-1 collisions, the size of this list is bounded by
	// the total number of direct non-blob referents in all unique
	// objects along a single lineage of descendants of the starting
	// point.
	todo ToDoList
}

func NewSizeCache(repo *Repository) (*SizeCache, error) {
	cache := &SizeCache{
		repo:      repo,
		treeSizes: make(map[Oid]TreeSize),
		blobSizes: make(map[Oid]BlobSize),
	}
	return cache, nil
}

func (cache *SizeCache) ObjectSize(oid Oid) (Type, Size, error) {
	objectType, objectSize, err := cache.repo.ReadHeader(oid)
	if err != nil {
		return "missing", nil, err
	}

	switch objectType {
	case "blob":
		blobSize := BlobSize(objectSize)
		cache.blobSizes[oid] = blobSize
		return "blob", blobSize, nil
	case "tree":
		treeSize, err := cache.TreeSize(oid)
		return "tree", treeSize, err
	case "commit":
		return "commit", nil, fmt.Errorf("object %v has unexpected type '%s'", oid, objectType)
	case "tag":
		return "tag", nil, fmt.Errorf("object %v has unexpected type '%s'", oid, objectType)
	default:
		panic(fmt.Sprintf("object %v has unknown type", oid))
	}
}

func (cache *SizeCache) BlobSize(oid Oid) (BlobSize, error) {
	size, ok := cache.blobSizes[oid]
	if !ok {
		objectType, objectSize, err := cache.repo.ReadHeader(oid)
		if err != nil {
			return 0, err
		}
		if objectType != "blob" {
			return 0, fmt.Errorf("object %s is a %s, not a blob", oid, objectType)
		}
		size = BlobSize(objectSize)
		cache.blobSizes[oid] = size
	}
	return size, nil
}

func (cache *SizeCache) TreeSize(oid Oid) (TreeSize, error) {
	s, ok := cache.treeSizes[oid]
	if ok {
		return s, nil
	}

	cache.todo.Push(oid)
	err := cache.fill()
	if err != nil {
		return TreeSize{}, err
	}

	// Now the size should be in the cache:
	s, ok = cache.treeSizes[oid]
	if ok {
		return s, nil
	}
	panic("queueTree() didn't fill tree")
}

// Compute the sizes of any trees listed in `cache.todo`. This might
// involve computing the sizes of referred-to objects. Do this without
// recursion to avoid unlimited stack growth.
func (cache *SizeCache) fill() error {
	for cache.todo.Length() != 0 {
		oid := cache.todo.Peek()

		// See if the object's size has been computed since it was
		// enqueued. This can happen if it is used in multiple places
		// in the ancestry graph.
		_, ok := cache.treeSizes[oid]
		if ok {
			cache.todo.Drop()
			continue
		}

		s, err := cache.queueTree(oid)
		if err == nil {
			cache.treeSizes[oid] = s
			cache.todo.Drop()
		} else if err == NotYetKnown {
			// Let loop continue (the tree's constituents were
			// added to todo by `queueTree()`).
		} else {
			return err
		}
	}
	return nil
}

// Compute and return the size of `tree` if we already know the size
// of its constituents. If the constituents' sizes are not yet known
// but believed to be computable, add any unknown constituents to
// `todo` and return an `NotYetKnown` error. If another error occurred
// while looking up an object, return that error. `tree` is not
// already in the cache.
func (cache *SizeCache) queueTree(oid Oid) (TreeSize, error) {
	var err error

	tree, err := cache.repo.ReadTree(oid)
	if err != nil {
		return TreeSize{}, err
	}

	ok := true

	entryCount := Count(0)

	// First accumulate all of the sizes (including maximum depth) for
	// all descendants:
	size := TreeSize{
		TreeCount: 1,
	}

	var entry TreeEntry

	iter := tree.Iter()

	for {
		entryOk, err := iter.NextEntry(&entry)
		if err != nil {
			return TreeSize{}, err
		}
		if !entryOk {
			break
		}
		entryCount += 1

		switch {
		case entry.Filemode&0170000 == 0040000:
			// Tree
			subsize, subok := cache.treeSizes[entry.Oid]
			if subok {
				if ok {
					size.addDescendent(entry.Name, subsize)
				}
			} else {
				ok = false
				// Schedule this one to be computed:
				cache.todo.Push(entry.Oid)
			}

		case entry.Filemode&0170000 == 0160000:
			// Commit
			if ok {
				size.addSubmodule(entry.Name)
			}

		case entry.Filemode&0170000 == 0120000:
			// Symlink
			if ok {
				size.addLink(entry.Name)
			}

		default:
			// Blob
			blobSize, blobOk := cache.blobSizes[entry.Oid]
			if blobOk {
				if ok {
					size.addBlob(entry.Name, blobSize)
				}
			} else {
				blobSize, err := cache.BlobSize(entry.Oid)
				if err != nil {
					return TreeSize{}, err
				}
				size.addBlob(entry.Name, blobSize)
			}
		}
	}

	if !ok {
		return TreeSize{}, NotYetKnown
	}

	// Now add one to the depth and to the tree count to account for
	// this tree itself:
	size.MaxDepth = addCapped(size.MaxDepth, 1)
	if entryCount > size.MaxTreeEntries {
		size.MaxTreeEntries = entryCount
	}
	return size, nil
}