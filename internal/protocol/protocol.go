// Package protocol defines the JSON payloads exchanged between agents and the
// central server.
package protocol

import "github.com/whatsnewdock/whatsnewdock/internal/store"

// Report is the periodic snapshot an agent pushes to the server.
type Report struct {
	AgentName  string            `json:"agent_name"`
	Version    string            `json:"version"`
	Info       store.Server      `json:"info"`
	Stacks     []store.Stack     `json:"stacks"`
	Containers []store.Container `json:"containers"`
}

// Command is a queued action a server asks an agent to execute.
type Command struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"` // "update"
	ContainerID string `json:"container_id"`
	TargetImage string `json:"target_image"`
}

// CommandResult is the agent's reply after executing a command.
type CommandResult struct {
	Status  string `json:"status"` // "done" | "failed"
	Message string `json:"message"`
}
