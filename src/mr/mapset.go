package mr

type MapSet struct {
	mapbool map[any]bool
	count   int
}

func NewMapSet() *MapSet {
	m := MapSet{}
	m.mapbool = make(map[any]bool)
	m.count = 0
	return &m
}

func (m *MapSet) Insert(data any) {
	m.mapbool[data] = true
	m.count++
}

func (m *MapSet) Has(data any) bool {
	return m.mapbool[data]
}

func (m *MapSet) Remove(data any) {
	m.mapbool[data] = false
	m.count--
}

func (m *MapSet) Size() int {
	return m.count
}
