package mr

import (
	"errors"
	"log"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"sync"
	"time"
)

const (
	maxTaskTime = 20
	debug       = false
)

type node struct {
	data any
	prev *node
	next *node
}

type BlockQueue struct {
	head  *node
	count int
	cond  *sync.Cond
}

func NewBlockQueue() *BlockQueue {
	h := &node{}
	h.next = h
	h.prev = h
	return &BlockQueue{
		head: h,
		cond: sync.NewCond(&sync.Mutex{}),
	}
}

func (q *BlockQueue) PutFront(data any) {
	q.cond.L.Lock()
	n := &node{data: data}
	n.next = q.head.next
	n.prev = q.head
	q.head.next.prev = n
	q.head.next = n
	q.count++
	q.cond.Broadcast()
	q.cond.L.Unlock()
}

func (q *BlockQueue) PopBack() (any, error) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	if q.count == 0 {
		return nil, errors.New("empty queue")
	}
	n := q.head.prev
	n.prev.next = q.head
	q.head.prev = n.prev
	q.count--
	return n.data, nil
}

func (q *BlockQueue) Size() int {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	return q.count
}

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
	if !m.mapbool[data] {
		m.mapbool[data] = true
		m.count++
	}
}

func (m *MapSet) Has(data any) bool {
	return m.mapbool[data]
}

func (m *MapSet) Remove(data any) {
	if m.mapbool[data] {
		m.mapbool[data] = false
		m.count--
	}
}

func (m *MapSet) Size() int {
	return m.count
}

type MapTaskState struct {
	beginSecond int64
	workerId    int
	fileId      int
}

type ReduceTaskState struct {
	beginSecond int64
	workerId    int
	fileId      int
}

type Coordinator struct {
	// Your definitions here.
	fileNames []string
	nReduce   int

	stateMutex  sync.RWMutex
	curWorkerId int

	unIssuedMapTasks *BlockQueue
	issuedMapTasks   *MapSet
	issuedMapMutex   sync.Mutex

	unIssuedReduceTasks *BlockQueue
	issuedReduceTasks   *MapSet
	issuedReduceMutex   sync.Mutex

	// task states
	mapTasks    []MapTaskState
	reduceTasks []ReduceTaskState

	// states
	mapDone bool
	allDone bool
}

func (c *Coordinator) logPrintf(format string, vars ...any) {
	if !debug {
		return
	}
	log.Printf(format, vars...)
}

// Your code here -- RPC handlers for the worker to call.

type MapTaskArgs struct {
	// -1 if does not have one
	WorkerId int
}
type MapTaskReply struct {
	// worker passes this to the os package
	FileName string

	// marks a unique file for mapping
	// gives -1 for no more fileId
	FileId int

	// for reduce tasks
	NReduce int

	// assign worker id as this reply is the first sent to workers
	WorkerId int

	// whether this kind of tasks are all done
	// if not, and fileId is -1, the worker waits
	AllDone bool
}

func (c *Coordinator) mapDoneProcess(reply *MapTaskReply) {
	c.logPrintf("all map tasks complete, telling workers to switch to reduce mode\n")
	reply.FileId = -1
	reply.AllDone = true
}

func (c *Coordinator) GiveMapTask(args *MapTaskArgs, reply *MapTaskReply) error {
	if args.WorkerId == -1 {
		// simply allocate
		c.stateMutex.Lock()
		reply.WorkerId = c.curWorkerId
		c.curWorkerId++
		c.stateMutex.Unlock()
	} else {
		reply.WorkerId = args.WorkerId
	}
	c.logPrintf("worker %v asks for a map task\n", reply.WorkerId)

	c.issuedMapMutex.Lock()

	c.stateMutex.RLock()
	mapDone := c.mapDone
	c.stateMutex.RUnlock()
	if mapDone {
		c.issuedMapMutex.Unlock()
		c.mapDoneProcess(reply)
		return nil
	}

	if c.unIssuedMapTasks.Size() == 0 && c.issuedMapTasks.Size() == 0 {
		c.stateMutex.Lock()
		alreadyDone := c.mapDone
		if !alreadyDone {
			c.mapDone = true
		}
		c.stateMutex.Unlock()
		c.issuedMapMutex.Unlock()
		c.mapDoneProcess(reply)
		if !alreadyDone {
			c.prepareAllReduceTasks()
		}
		return nil
	}
	c.logPrintf("%v unissued map tasks %v issued map tasks at hand\n", c.unIssuedMapTasks.Size(), c.issuedMapTasks.Size())
	c.issuedMapMutex.Unlock() // release lock to allow unissued update
	curTime := getNowTimeSecond()
	ret, err := c.unIssuedMapTasks.PopBack()
	var fileId int
	if err != nil {
		c.logPrintf("no map task yet, let worker wait...\n")
		fileId = -1
	} else {
		fileId = ret.(int)
		c.issuedMapMutex.Lock()
		reply.FileName = c.fileNames[fileId]
		c.mapTasks[fileId].beginSecond = curTime
		c.mapTasks[fileId].workerId = reply.WorkerId
		c.issuedMapTasks.Insert(fileId)
		c.issuedMapMutex.Unlock()
		c.logPrintf("giving map task %v on file %v at second %v\n", fileId, reply.FileName, curTime)
	}

	reply.FileId = fileId
	reply.AllDone = false
	reply.NReduce = c.nReduce

	return nil
}

type MapTaskJoinArgs struct {
	FileId   int
	WorkerId int
}

type MapTaskJoinReply struct {
	Accept bool
}

func getNowTimeSecond() int64 {
	return time.Now().UnixNano() / int64(time.Second)
}

func (c *Coordinator) JoinMapTask(args *MapTaskJoinArgs, reply *MapTaskJoinReply) error {
	// check current time for whether the worker has timed out
	c.logPrintf("got join request from worker %v on file %v %v\n", args.WorkerId, args.FileId, c.fileNames[args.FileId])

	// log.Println("locking issuedMutex...")
	c.issuedMapMutex.Lock()

	curTime := getNowTimeSecond()
	taskTime := c.mapTasks[args.FileId].beginSecond
	if !c.issuedMapTasks.Has(args.FileId) {
		c.logPrintf("task abandoned or does not exists, ignoring...\n")
		// log.Println("unlocking issuedMutex...")
		c.issuedMapMutex.Unlock()
		reply.Accept = false
		return nil
	}
	if c.mapTasks[args.FileId].workerId != args.WorkerId {
		c.logPrintf("map task belongs to worker %v not this %v, ignoring...", c.mapTasks[args.FileId].workerId, args.WorkerId)
		c.issuedMapMutex.Unlock()
		reply.Accept = false
		return nil
	}
	if curTime-taskTime > maxTaskTime {
		c.logPrintf("task exceeds max wait time, abadoning...\n")
		reply.Accept = false
		c.issuedMapTasks.Remove(args.FileId)
		c.unIssuedMapTasks.PutFront(args.FileId)
	} else {
		c.logPrintf("task within max wait time, accepting...\n")
		reply.Accept = true
		c.issuedMapTasks.Remove(args.FileId)
	}

	// log.Println("unlocking issuedMutex...")
	c.issuedMapMutex.Unlock()
	return nil
}

type ReduceTaskArgs struct {
	WorkerId int
}

type ReduceTaskReply struct {
	RIndex    int
	NReduce   int
	FileCount int
	AllDone   bool
}

func (c *Coordinator) prepareAllReduceTasks() {
	for i := range c.nReduce {
		c.logPrintf("putting %vth reduce task into channel\n", i)
		c.unIssuedReduceTasks.PutFront(i)
	}
}

func (c *Coordinator) GiveReduceTask(args *ReduceTaskArgs, reply *ReduceTaskReply) error {
	c.logPrintf("worker %v asking for a reduce task\n", args.WorkerId)
	c.issuedReduceMutex.Lock()

	if c.unIssuedReduceTasks.Size() == 0 && c.issuedReduceTasks.Size() == 0 {
		c.logPrintf("all reduce tasks complete, telling workers to terminate\n")
		c.issuedReduceMutex.Unlock()
		c.stateMutex.Lock()
		c.allDone = true
		c.stateMutex.Unlock()
		reply.RIndex = -1
		reply.AllDone = true
		return nil
	}
	c.logPrintf("%v unissued reduce tasks %v issued reduce tasks at hand\n", c.unIssuedReduceTasks.Size(), c.issuedReduceTasks.Size())
	c.issuedReduceMutex.Unlock() // release lock to allow unissued update
	curTime := getNowTimeSecond()
	ret, err := c.unIssuedReduceTasks.PopBack()
	var rindex int
	if err != nil {
		c.logPrintf("no reduce task yet, let worker wait...\n")
		rindex = -1
	} else {
		rindex = ret.(int)
		c.issuedReduceMutex.Lock()
		c.reduceTasks[rindex].beginSecond = curTime
		c.reduceTasks[rindex].workerId = args.WorkerId
		c.issuedReduceTasks.Insert(rindex)
		c.issuedReduceMutex.Unlock()
		c.logPrintf("giving reduce task %v at second %v\n", rindex, curTime)
	}

	reply.RIndex = rindex
	reply.AllDone = false
	reply.NReduce = c.nReduce
	reply.FileCount = len(c.fileNames)

	return nil
}

type ReduceTaskJoinArgs struct {
	WorkerId int
	RIndex   int
}

type ReduceTaskJoinReply struct {
	Accept bool
}

func (c *Coordinator) JoinReduceTask(args *ReduceTaskJoinArgs, reply *ReduceTaskJoinReply) error {
	// check current time for whether the worker has timed out
	c.logPrintf("got join request from worker %v on reduce task %v\n", args.WorkerId, args.RIndex)

	// log.Println("locking issuedMutex...")
	c.issuedReduceMutex.Lock()

	curTime := getNowTimeSecond()
	taskTime := c.reduceTasks[args.RIndex].beginSecond
	if !c.issuedReduceTasks.Has(args.RIndex) {
		c.logPrintf("task abandoned or does not exists, ignoring...\n")
		// log.Println("unlocking issuedMutex...")
		c.issuedReduceMutex.Unlock()
		return nil
	}
	if c.reduceTasks[args.RIndex].workerId != args.WorkerId {
		c.logPrintf("reduce task belongs to worker %v not this %v, ignoring...", c.reduceTasks[args.RIndex].workerId, args.WorkerId)
		c.issuedReduceMutex.Unlock()
		reply.Accept = false
		return nil
	}
	if curTime-taskTime > maxTaskTime {
		c.logPrintf("task exceeds max wait time, abadoning...\n")
		reply.Accept = false
		c.issuedReduceTasks.Remove(args.RIndex)
		c.unIssuedReduceTasks.PutFront(args.RIndex)
	} else {
		c.logPrintf("task within max wait time, accepting...\n")
		reply.Accept = true
		c.issuedReduceTasks.Remove(args.RIndex)
	}

	// log.Println("unlocking issuedMutex...")
	c.issuedReduceMutex.Unlock()
	return nil
}

func (m *MapSet) removeTimeoutMapTasks(c *Coordinator, mapTasks []MapTaskState, unIssuedMapTasks *BlockQueue) {
	for fileId, issued := range m.mapbool {
		now := getNowTimeSecond()
		if issued {
			if now-mapTasks[fileId.(int)].beginSecond > maxTaskTime {
				c.logPrintf("worker %v on file %v abandoned due to timeout\n", mapTasks[fileId.(int)].workerId, fileId)
				m.mapbool[fileId.(int)] = false
				m.count--
				unIssuedMapTasks.PutFront(fileId.(int))
			}
		}
	}
}

func (m *MapSet) removeTimeoutReduceTasks(c *Coordinator, reduceTasks []ReduceTaskState, unIssuedReduceTasks *BlockQueue) {
	for fileId, issued := range m.mapbool {
		now := getNowTimeSecond()
		if issued {
			if now-reduceTasks[fileId.(int)].beginSecond > maxTaskTime {
				c.logPrintf("worker %v on file %v abandoned due to timeout\n", reduceTasks[fileId.(int)].workerId, fileId)
				m.mapbool[fileId.(int)] = false
				m.count--
				unIssuedReduceTasks.PutFront(fileId.(int))
			}
		}
	}
}

func (c *Coordinator) removeTimeoutTasks() {
	c.logPrintf("removing timeout maptasks...\n")
	c.issuedMapMutex.Lock()
	c.issuedMapTasks.removeTimeoutMapTasks(c, c.mapTasks, c.unIssuedMapTasks)
	c.issuedMapMutex.Unlock()
	c.issuedReduceMutex.Lock()
	c.issuedReduceTasks.removeTimeoutReduceTasks(c, c.reduceTasks, c.unIssuedReduceTasks)
	c.issuedReduceMutex.Unlock()
}

func (c *Coordinator) loopRemoveTimeoutMapTasks() {
	for true {
		time.Sleep(2 * 1000 * time.Millisecond)
		c.removeTimeoutTasks()
	}
}

// an example RPC handler.
//
// the RPC argument and reply types are defined in rpc.go.
func (c *Coordinator) Example(args *ExampleArgs, reply *ExampleReply) error {
	reply.Y = args.X + 1
	return nil
}

// start a thread that listens for RPCs from worker.go
func (c *Coordinator) server() {
	// log.Println("111...")
	rpc.Register(c)
	// log.Println("222...")
	rpc.HandleHTTP()
	//l, e := net.Listen("tcp", ":1234")
	// log.Println("333...")
	sockname := coordinatorSock()
	// log.Println("444...")
	os.Remove(sockname)
	l, e := net.Listen("unix", sockname)
	if e != nil {
		log.Fatal("listen error:", e)
	}
	go http.Serve(l, nil)
	// log.Println("listen started...")
}

// main/mrcoordinator.go calls Done() periodically to find out
// if the entire job has finished.
func (c *Coordinator) Done() bool {
	// ret := false

	// Your code here.

	// return ret

	c.stateMutex.RLock()
	allDone := c.allDone
	c.stateMutex.RUnlock()

	if allDone {
		c.logPrintf("asked whether i am done, replying yes...\n")
	} else {
		c.logPrintf("asked whether i am done, replying no...\n")
	}

	return allDone
}

// create a Coordinator.
// main/mrcoordinator.go calls this function.
// nReduce is the number of reduce tasks to use.
func MakeCoordinator(files []string, nReduce int) *Coordinator {
	c := Coordinator{}

	// Your code here.

	log.SetPrefix("coordinator: ")
	c.logPrintf("making coordinator\n")

	c.fileNames = files
	c.nReduce = nReduce
	c.curWorkerId = 0
	c.mapTasks = make([]MapTaskState, len(files))
	c.reduceTasks = make([]ReduceTaskState, nReduce)
	c.unIssuedMapTasks = NewBlockQueue()
	c.issuedMapTasks = NewMapSet()
	c.unIssuedReduceTasks = NewBlockQueue()
	c.issuedReduceTasks = NewMapSet()
	c.allDone = false
	c.mapDone = false

	// start a thread that listens for RPCs from worker.go
	c.server()
	c.logPrintf("listening started...\n")
	// starts a thread that abandons timeout tasks
	go c.loopRemoveTimeoutMapTasks()

	// all are unissued map tasks
	// send to channel after everything else initializes
	c.logPrintf("file count %d\n", len(files))
	for i := range len(files) {
		c.logPrintf("sending %vth file map task to channel\n", i)
		c.unIssuedMapTasks.PutFront(i)
	}

	return &c
}
