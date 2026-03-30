package mr

import (
	"errors"
	"sync"
)

type listNode struct {
	data any
	next *listNode
	prev *listNode
}

func (node *listNode) addBefore(data any) {
	prev := node.prev

	newnode := listNode{}
	newnode.data = data

	newnode.next = node
	node.prev = &newnode
	newnode.prev = prev
	prev.next = &newnode
}

func (node *listNode) addAfter(data any) {
	next := node.next

	newnode := listNode{}
	newnode.data = data

	newnode.prev = node
	node.next = &newnode
	newnode.next = next
	next.prev = &newnode
}

func (node *listNode) removeBefore() {
	prev := node.prev.prev
	node.prev = prev
	prev.next = node
}

func (node *listNode) removeAfter() {
	next := node.next.next
	node.next = next
	next.prev = node
}

type LinkedList struct {
	head  listNode
	count int
}

func (list *LinkedList) pushFront(data any) {
	list.head.addAfter(data)
	list.count++
}

func (list *LinkedList) pushBack(data any) {
	list.head.addBefore(data)
	list.count++
}

func (list *LinkedList) peekFront() (any, error) {
	if list.count == 0 {
		return nil, errors.New("peeking empty list")
	}
	return list.head.next.data, nil
}

func (list *LinkedList) peekBack() (any, error) {
	if list.count == 0 {
		return nil, errors.New("peeking empty list")
	}
	return list.head.prev.data, nil
}

func (list *LinkedList) popFront() (any, error) {
	if list.count == 0 {
		return nil, errors.New("popping empty list")
	}
	data := list.head.next.data
	list.head.removeAfter()
	list.count--
	return data, nil
}

func (list *LinkedList) popBack() (any, error) {
	if list.count == 0 {
		return nil, errors.New("popping empty list")
	}
	data := list.head.prev.data
	list.head.removeBefore()
	list.count--
	return data, nil
}

func NewLinkedList() *LinkedList {
	list := LinkedList{}
	list.count = 0
	list.head.next = &list.head
	list.head.prev = &list.head

	return &list
}

type BlockQueue struct {
	list *LinkedList
	cond *sync.Cond
}

func NewBlockQueue() *BlockQueue {
	queue := BlockQueue{}

	queue.list = NewLinkedList()
	queue.cond = sync.NewCond(new(sync.Mutex))

	return &queue
}

func (queue *BlockQueue) PutFront(data any) {
	queue.cond.L.Lock()
	queue.list.pushFront(data)
	queue.cond.L.Unlock()
	queue.cond.Broadcast()
}

func (queue *BlockQueue) PutBack(data any) {
	queue.cond.L.Lock()
	queue.list.pushBack(data)
	queue.cond.L.Unlock()
	queue.cond.Broadcast()
}

func (queue *BlockQueue) GetFront() (any, error) {
	queue.cond.L.Lock()
	for queue.list.count == 0 {
		queue.cond.Wait()
	}
	data, err := queue.list.popFront()
	queue.cond.L.Unlock()
	return data, err
}

func (queue *BlockQueue) GetBack() (any, error) {
	queue.cond.L.Lock()
	for queue.list.count == 0 {
		queue.cond.Wait()
	}
	data, err := queue.list.popBack()
	queue.cond.L.Unlock()
	return data, err
}

func (queue *BlockQueue) PopFront() (any, error) {
	queue.cond.L.Lock()
	data, err := queue.list.popFront()
	queue.cond.L.Unlock()
	return data, err
}

func (queue *BlockQueue) PopBack() (any, error) {
	queue.cond.L.Lock()
	data, err := queue.list.popBack()
	queue.cond.L.Unlock()
	return data, err
}

func (queue *BlockQueue) Size() int {
	queue.cond.L.Lock()
	ret := queue.list.count
	queue.cond.L.Unlock()
	return ret
}

func (queue *BlockQueue) PeekFront() (any, error) {
	queue.cond.L.Lock()
	for queue.list.count == 0 {
		queue.cond.Wait()
	}
	data, err := queue.list.peekFront()
	queue.cond.L.Unlock()
	return data, err
}

func (queue *BlockQueue) PeekBack() (any, error) {
	queue.cond.L.Lock()
	for queue.list.count == 0 {
		queue.cond.Wait()
	}
	data, err := queue.list.peekBack()
	queue.cond.L.Unlock()
	return data, err
}
