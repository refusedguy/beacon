package mcp

import msdk "github.com/modelcontextprotocol/go-sdk/mcp"

// toolRegistrations is the ordered list of tool registration functions.
// NewServer walks this list; each function owns exactly one tool.
var toolRegistrations = []func(*msdk.Server, Services){
	registerDispatch,
	registerWatchPR,
	registerStatus,
	registerRecent,
	registerWait,
	registerControl,
	registerTaskCreate,
	registerTaskDelete,
}
