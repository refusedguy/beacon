package mcp

import msdk "github.com/modelcontextprotocol/go-sdk/mcp"

var taskCreateTool = &msdk.Tool{
	Name:        "beacon_task_create",
	Description: "Create and publish a task card. Orchestrator only.",
}
