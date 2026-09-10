export interface Server {
  id: string
  name: string
  is_local: boolean
  online: boolean
  last_seen: string
  docker_version: string
  os: string
  arch: string
  cpus: number
  memory_bytes: number
  labels: string[]
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
