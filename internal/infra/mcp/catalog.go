package mcp

import msdk "github.com/modelcontextprotocol/go-sdk/mcp"

// catalogTool builds the SDK tool definition for a catalog entry. The name and
// description come from the one registration table, so the registered tools
// cannot drift from the source used to generate permissions.
func catalogTool(name string) *msdk.Tool {
	for _, spec := range toolSpecs {
		if spec.Name == name {
			return &msdk.Tool{Name: spec.Name, Description: spec.Description}
		}
	}
	panic("mcp: tool " + name + " is not in ToolCatalog")
}
