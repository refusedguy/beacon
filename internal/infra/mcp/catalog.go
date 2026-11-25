package mcp

import msdk "github.com/modelcontextprotocol/go-sdk/mcp"

// toolRegistrations is the ordered list of tool registration functions.
// NewServer walks this list; each function owns exactly one tool. The list
// covers every entry in ToolCatalog, so a new tool needs one catalog entry and
// one matching registration here.
var toolRegistrations = []func(*msdk.Server, Services){
	registerDispatch,
	registerWatchPR,
	registerStatus,
	registerRecent,
	registerWait,
	registerControl,
	registerTaskCreate,
	registerTaskDelete,
	registerTaskUpdate,
	registerReconcile,
}

// catalogTool builds the SDK tool definition for a catalog entry. The name and
// description come from ToolCatalog, so the registered tools cannot drift from
// the source used to generate permissions.
func catalogTool(name string) *msdk.Tool {
	for _, spec := range ToolCatalog() {
		if spec.Name == name {
			return &msdk.Tool{Name: spec.Name, Description: spec.Description}
		}
	}
	panic("mcp: tool " + name + " is not in ToolCatalog")
}
