package loop

// reservedAgentBinVolume is the volume name agents.yml may declare to
// request a bind-mount of the runner's embedded agent-binary directory
// (see mapVolumes / orchestratorVolumes). Kept in its own file so the
// constant survives session.go's deletion in the runner-IPC purge.
//
// The server's hidden _agent workflow factory (apps/hangrix/internal/
// modules/workflow/service/agent_run.go) injects a volume with this
// name into every agent-spawned workflow_job; the runner then maps it
// to a read-only bind mount of agentbin.Extract's parent directory
// instead of a named Docker volume.
const reservedAgentBinVolume = "hangrix-agent"
