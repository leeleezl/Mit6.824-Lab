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
