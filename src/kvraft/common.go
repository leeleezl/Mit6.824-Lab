package kvraft

type OperationContext struct {
	MaxAppliedCommandId int64
	LastResponse        *CommandResponse
}

type Command struct {
	*CommandRequest
}

const (
	OK             = "OK"
	ErrNoKey       = "ErrNoKey"
	ErrWrongLeader = "ErrWrongLeader"
	ErrTimeout     = "ErrTimeout"
)

type Err string

type OperationOp string

const (
	OpPut    = "OpPut"
	OpAppend = "OpAppend"
	OpGet    = "OpGet"
)

type CommandRequest struct {
	Key       string
	Value     string
	Op        OperationOp
	ClientId  int64
	CommandId int64
}

type CommandResponse struct {
	Err   Err
	Value string
}

// Put or Append
// type PutAppendArgs struct {
// 	Key   string
// 	Value string
// 	Op    string // "Put" or "Append"
// 	// You'll have to add definitions here.
// 	// Field names must start with capital letters,
// 	// otherwise RPC will break.
// }

// type PutAppendReply struct {
// 	Err Err
// }

// type GetArgs struct {
// 	Key string
// 	// You'll have to add definitions here.
// }

// type GetReply struct {
// 	Err   Err
// 	Value string
// }
