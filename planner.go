package weirollgo

import (
	"encoding/hex"
	"fmt"

	"github.com/umbracle/ethgo"
	"github.com/umbracle/ethgo/abi"
)

type Planner struct {
	Commands []*Command
}

func NewPlanner() *Planner {
	p := &Planner{
		Commands: []*Command{},
	}
	return p
}

type Contract struct {
	addr ethgo.Address
	abi  *abi.ABI
}

func NewContract(addr ethgo.Address, abi *abi.ABI) *Contract {
	return &Contract{addr: addr, abi: abi}
}

func (c *Contract) Call(methodName string, args ...interface{}) *Command {
	cmd, err := c.CallErr(methodName, args...)
	if err != nil {
		panic(err)
	}
	return cmd
}

func (c *Contract) CallErr(methodName string, args ...interface{}) (*Command, error) {
	method := c.abi.GetMethod(methodName)
	if method == nil {
		return nil, fmt.Errorf("method not found")
	}
	cmd := &Command{
		Address: c.addr,
		Method:  method,
		Args:    args,
	}
	return cmd, nil
}

type ReturnValue struct {
	c *Command
}

func (p *Planner) Add(c *Command) *ReturnValue {
	p.Commands = append(p.Commands, c)
	return &ReturnValue{c: c}
}

type Plan struct {
	Commands [][32]byte
	State    [][]byte
}

type ValueI interface {
	GetSlot() uint64
}

type ReturnValue2 struct {
	Slot uint64
}

func (r *ReturnValue2) GetSlot() uint64 {
	return r.Slot
}

type LiteralValue struct {
	Value []byte
	Slot  uint64
}

func (l *LiteralValue) GetSlot() uint64 {
	return l.Slot
}

type command2 struct {
	args []interface{}

	ret *ReturnValue2

	impl *Command
}

func (p *Planner) Plan() (*Plan, error) {
	// decode state

	cmds := [][32]byte{}
	state := [][]byte{}

	reserveState := func(val []byte) uint64 {
		slot := len(state)
		state = append(state, val)

		return uint64(slot)
	}

	dag := newDag()

	cmds2 := []*command2{}
	cmds2Map := map[*Command]*command2{}

	// Step 1: Build a Dag graph to represent the liveness of the arguments
	// among the commands.

	litMap := map[string]*LiteralValue{}
	for _, c := range p.Commands {
		args := []interface{}{}
		for indx, arg := range c.Args {
			abiElems := c.Method.Inputs.TupleElems()

			var slotArg interface{}
			if ret, ok := arg.(*ReturnValue); ok {
				retCmd := cmds2Map[ret.c]

				if retCmd.ret == nil {
					// Up until this point the return value was orphan, meaning
					// that no-one was using it. Thus, it did not make sense to
					// allocate any slot space.

					// create a new vertex for the return value
					retCmd.ret = &ReturnValue2{}
					dag.addVertex(retCmd.ret)

					// link up the dependency between the inputs of the return
					// command and the return value
					for _, arg := range retCmd.args {
						dag.addEdge(edge{
							Src: arg,
							Dst: retCmd.ret,
						})
					}
				}
				slotArg = retCmd.ret

			} else {
				// literal value, encode it as bytes and
				// create a new vertex
				value, err := abiElems[indx].Elem.Encode(arg)
				if err != nil {
					return nil, err
				}

				valueStr := hex.EncodeToString(value)
				lit, ok := litMap[valueStr]
				if !ok {
					// add the new entry on the dag for the new literal
					lit = &LiteralValue{
						Value: value,
					}
					litMap[valueStr] = lit
					dag.addVertex(lit)
				}
				slotArg = lit
			}
			args = append(args, slotArg)
		}

		cmd := &command2{
			args: args,
			impl: c,
		}
		cmds2 = append(cmds2, cmd)
		cmds2Map[c] = cmd
	}

	// Step 2: Compute the slot assignments from the dag.

	// Allocate all the literal input arguments deterministically.
	allocLitCache := map[*LiteralValue]struct{}{}
	for _, cmd := range cmds2 {
		for _, arg := range cmd.args {
			if lit, ok := arg.(*LiteralValue); ok {
				if _, ok := allocLitCache[lit]; !ok {
					lit.Slot = reserveState(lit.Value)
					allocLitCache[lit] = struct{}{}
				}
			}
		}
	}

	freeSlots := []uint64{}
	getReservedSlot := func() uint64 {
		if len(freeSlots) == 0 {
			return reserveState([]byte{0x0})
		}

		slot := freeSlots[0]
		freeSlots = freeSlots[1:]
		return slot
	}

	doneMap := map[interface{}]struct{}{}

	// Allocate slots for command return values (if any)
	for _, cmd := range cmds2 {
		if cmd.ret != nil {
			// pick a slot from the free slot list and assign it
			cmd.ret.Slot = getReservedSlot()

			// resolve all the input values of the cmd. If all their
			// outbounds are done (except this command itself). That value
			// is elegible for garbage collection after this command.
			gcSlots := []uint64{}
			for _, v := range dag.getInbound(cmd.ret) {
				isDone := true
				for _, out := range dag.getOutbound(v) {
					if out != cmd.ret {
						if _, ok := doneMap[out]; !ok {
							isDone = false
						}
					}
				}
				if isDone {
					gcSlots = append(gcSlots, v.(ValueI).GetSlot())
				}
			}
			freeSlots = append(freeSlots, gcSlots...)

			// consider this state done
			doneMap[cmd.ret] = struct{}{}
		}
	}

	// Step 3: Build the commands

	for _, c := range cmds2 {
		cmd := []byte{}

		// function selector (x)
		cmd = append(cmd, c.impl.Method.ID()...)

		// flags
		cmd = append(cmd, 0x00)

		// empty input arguments (one argument from state, the other empty)
		for _, slot := range c.args {
			cmd = append(cmd, byte(slot.(ValueI).GetSlot()))
		}
		for i := len(c.args); i < 6; i++ {
			cmd = append(cmd, 0xff)
		}

		ret := uint64(0xff)
		if c.ret != nil {
			ret = c.ret.Slot
		}

		// return value (x)
		cmd = append(cmd, byte(ret))

		// address (x)
		cmd = append(cmd, c.impl.Address.Bytes()...)

		realCmd := [32]byte{}
		copy(realCmd[:], cmd[:])
		cmds = append(cmds, realCmd)
	}

	return &Plan{Commands: cmds, State: state}, nil
}

type CommandType string

const (
	CallCommandType    CommandType = "CALL"
	RawCallCommandType CommandType = "RAWCALL"
	SubPlanCommandType CommandType = "SUBPLAN"
)

type Command struct {
	Type CommandType

	Address ethgo.Address
	Method  *abi.Method
	Args    []interface{}
}

func (c *Command) Delegate() *Command {
	return c
}

func newDag() *dag {
	return &dag{
		vertex:   set{},
		inbound:  set{},
		outbound: set{},
	}
}

type dag struct {
	vertex set

	inbound  set
	outbound set
}

// AddVertex adds a new vertex on the DAG
func (d *dag) addVertex(v vertex) {
	d.vertex.add(v)
}

func (d *dag) getInbound(v vertex) (res []vertex) {
	vals, ok := d.inbound[v]
	if !ok {
		return
	}
	for k := range vals.(set) {
		res = append(res, k)
	}
	return
}

func (d *dag) getOutbound(v vertex) (res []vertex) {
	vals, ok := d.outbound[v]
	if !ok {
		return
	}
	for k := range vals.(set) {
		res = append(res, k)
	}
	return
}

// AddEdge adds a new edge on the DAG
func (d *dag) addEdge(e edge) {
	if s, ok := d.inbound[e.Dst]; ok && s.(set).include(e.Src) {
		return
	}

	s, ok := d.inbound[e.Dst]
	if !ok {
		s = set{}
		d.inbound[e.Dst] = s
	}
	s.(set).add(e.Src)

	s, ok = d.outbound[e.Src]
	if !ok {
		s = set{}
		d.outbound[e.Src] = s
	}
	s.(set).add(e.Dst)
}

// Hashable is the interface implemented by vertex objects
// that have a hash representation
type hashable interface {
	Hash() interface{}
}

// Vertex is a vertex in the graph
type vertex interface{}

// Edge is an edge between two vertex of the graph
type edge struct {
	Src vertex
	Dst vertex
}

type set map[interface{}]interface{}

func (s set) add(v vertex) {
	k := v
	if h, ok := v.(hashable); ok {
		k = h.Hash()
	}
	if _, ok := s[k]; !ok {
		s[k] = struct{}{}
	}
}

func (s set) include(v vertex) bool {
	k := v
	if h, ok := v.(hashable); ok {
		k = h.Hash()
	}
	_, ok := s[k]
	return ok
}
