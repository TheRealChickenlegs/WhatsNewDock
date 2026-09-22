export type ServerKind = 'local' | 'agent' | 'direct'

export interface Server {
  id: string
  name: string
  is_local: boolean
  /** How the host is reached: this instance, an agent, or a direct endpoint. */
  kind: ServerKind
  online: boolean
  last_seen: string
  docker_version: string
  os: string
  arch: string
  cpus: number
  memory_bytes: number
  labels: string[]
  /** Direct endpoints only: the Docker Engine API URL. */
  docker_host?: string
  /** Direct endpoints only: whether TLS material is configured. */
  tls?: boolean
  /** Swarm position: none | worker | manager. */
  swarm_role?: string
  /** True once the services API is reachable, i.e. swarm updates are opted in. */
  swarm_services?: boolean
  /** Agents only: the build the agent last reported. */
  agent_version?: string
  agent_image?: string
  /** current | behind | unknown — how the agent compares with this deployment. */
  agent_build?: 'current' | 'behind' | 'unknown' | ''
}

/** One agent's progress through an orchestrated self-update. */
export interface AgentUpdate {
  server_id: string
  name: string
  from?: string
  to?: string
  status: 'queued' | 'updated' | 'failed' | 'skipped'
  message?: string
}

// CheckStatus is the state of the current or most recent update check.
export interface CheckStatus {
  running: boolean
  trigger?: string
  updates_available?: number
  last_error?: string
  started_at?: string
  finished_at?: string
}

export interface SelfUpdateRun {
  id: string
  started_at: string
  phase: 'agents' | 'controller' | 'done' | 'failed'
  kind?: string
  target?: string
  message?: string
  error?: string
  agents: AgentUpdate[]
  finished: boolean
}

export interface SelfUpdateRunResponse {
  active: boolean
  run?: SelfUpdateRun
}

export interface Stack {
  id: string
  server_id: string
  name: string
  kind: string
  count: number
}

export interface Update {
  id: string
  container_id: string
  repo_key: string
  current_tag: string
  latest_tag: string
  versions_behind: number
  source: string
  source_url: string
  checked_at: string
}

export interface Container {
  id: string
  server_id: string
  stack_id: string
  stack_name: string
  docker_id: string
  name: string
  image: string
  image_name: string
  image_tag: string
  image_digest: string
  registry: string
  repository: string
  state: string
  status: string
  running: boolean
  restart_policy: string
  compose_service: string
  created_at: string
  started_at: string
  labels: Record<string, string>
  ports: string[]
  pinned: boolean
  /** Podman Quadlet: the systemd unit that owns this container. */
  systemd_unit?: string
  /** True when the container is managed by systemd and must be updated there. */
  managed: boolean
  /** Swarm membership, present when the container is a service task. */
  swarm_service_id?: string
  swarm_service_name?: string
  swarm_task_id?: string
  swarm_node_id?: string
  updated_at: string
  server_name: string
  update: Update | null
}

export interface Release {
  id: string
  repo_key: string
  tag: string
  title: string
  body: string
  url: string
  published_at: string
  prerelease: boolean
}

export interface ReleasesResponse {
  container: Container
  update: Update | null
  releases: Release[]
}

export interface Overview {
  servers: number
  servers_online: number
  stacks: number
  containers: number
  containers_running: number
  updates_available: number
  versions_behind_total: number
}

export interface Me {
  authenticated: boolean
  username?: string
  role?: string
  source?: string
}

export interface AuthConfig {
  auth_mode: string
  oidc_enabled: boolean
}

export interface User {
  id: string
  username: string
  role: string
  created_at: string
}

export interface AuditEvent {
  id: number
  timestamp: string
  kind: string
  actor: string
  server_id: string
  container_id: string
  message: string
}

export interface UpdateProgress {
  container_id: string
  name: string
  status: string // queued | pulling | recreating | done | failed
  progress: number // 0-100, or -1 when indeterminate
  message: string
}

/** What the app knows about its own newer releases. */
export interface SelfUpdate {
  current: string
  latest: string
  /** Which signal found the update: a newer release, or a rebuilt tag. */
  kind?: 'release' | 'build'
  /** The image reference the update would deploy. */
  target?: string
  /** The image reference this container is running. */
  image?: string
  current_digest?: string
  latest_digest?: string
  update_available: boolean
  url?: string
  name?: string
  published_at?: string
  checked_at?: string
  error?: string
  enabled: boolean
  interval: string
  repo: string
  /** False when the app cannot identify or reach its own container. */
  can_apply: boolean
  current_image?: string
}

export interface Settings {
  changelog_count: number
  include_prereleases: boolean
  ignore_images: string[]
  update_interval: string
  github_token_set: boolean
  gitlab_token_set: boolean
  auth_mode: string
  oidc_enabled: boolean
  oidc_issuer: string
  enable_recreate: boolean
  base_url: string
  self_update: boolean
  self_update_interval: string
  self_update_repo: string
}

export interface AuthSettings {
  mode: string
  session_ttl: string
  oidc: {
    issuer_url: string
    client_id: string
    client_secret_set: boolean
    redirect_url: string
    scopes: string[]
    username_claim: string
    default_role: string
  }
}

export interface AuthSettingsUpdate {
  mode: string
  session_ttl: string
  oidc: {
    issuer_url: string
    client_id: string
    client_secret: string
    redirect_url: string
    scopes: string[]
    username_claim: string
    default_role: string
  }
}
