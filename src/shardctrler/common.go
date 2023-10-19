package shardctrler

import "time"

//
// Shard controler: assigns shards to replication groups.
//
// RPC interface:
// Join(servers) -- add a set of groups (gid -> server-list mapping).
// Leave(gids) -- delete a set of groups.
// Move(shard, gid) -- hand off one shard from current owner to gid.
// Query(num) -> fetch Config # num, or latest config if num==-1.
//
// A Config (configuration) describes a set of replica groups, and the
// replica group responsible for each shard. Configs are numbered. Config
// #0 is the initial configuration, with no groups and all shards
// assigned to group 0 (the invalid group).
//
// You will need to add fields to the RPC argument structs.
//

// The number of shards.
const NShards = 10
const ExecuteTimeout = 500 * time.Millisecond

// A configuration -- an assignment of shards to groups.
// Please don't change this.
type Config struct {
	Num    int              // config number
	Shards [NShards]int     // shard -> gid
	Groups map[int][]string // gid -> servers[]
}

func DefaultConfig() Config {
	return Config{Groups: make(map[int][]string)}
}

type Command struct {
	*CommandRequest
}

type OperationContext struct {
	MaxAppliedCommandId int64
	LastResponse        *CommandResponse
}

type Err uint8

const (
	OK Err = iota
	ErrWrongLeader
	ErrTimeout
)

type OperationOp uint8

const (
	OpJoin OperationOp = iota
	OpLeave
	OpMove
	OpQuery
)

type CommandRequest struct {
	Servers   map[int][]string
	GIDs      []int
	Shard     int
	GID       int
	Num       int
	Op        OperationOp
	ClientId  int64
	CommandId int64
}

type CommandResponse struct {
	Err    Err
	Config Config
}
