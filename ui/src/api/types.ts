// Mirrors api/v1alpha1. Only the fields the dashboard reads are declared; the
// admin API returns the full objects.

export interface ObjectMeta {
  name: string
  namespace?: string
  uid?: string
  resourceVersion?: string
  generation?: number
  creationTimestamp?: string
  labels?: Record<string, string>
  annotations?: Record<string, string>
}

export interface Condition {
  type: string
  status: 'True' | 'False' | 'Unknown'
  reason?: string
  message?: string
  lastTransitionTime?: string
  observedGeneration?: number
}

export interface SecretRef {
  name: string
}

export type Engine = 'Terraform' | 'OpenTofu'
export type TerragruntMode = 'Unit' | 'RunAll'
export type SyncStatus = 'Synced' | 'OutOfSync' | 'Unknown'

export interface TerragruntSpec {
  enabled?: boolean
  version?: string
  mode?: TerragruntMode
  excludedUnits?: string[]
}

export interface PlanSummary {
  toCreate: number
  toUpdate: number
  toDestroy: number
  toReplace: number
}

export interface VCSRepository {
  provider?: 'GitHub' | 'GitLab' | 'Gitea'
  url: string
  branch?: string
  path?: string
  secretRef?: SecretRef
}

export interface EnvSecretRef {
  name: string
  key?: string
  envPrefix?: string
}

// --- Universe -------------------------------------------------------------

export interface Universe {
  metadata: ObjectMeta
  spec: {
    displayName?: string
    description?: string
    suspended?: boolean
  }
  status?: {
    phase?: string
    namespace?: string
    galaxyCount?: number
    astralCount?: number
    flareCount?: number
    conditions?: Condition[]
  }
}

// --- Galaxy ---------------------------------------------------------------

export interface BackendConfig {
  type: 'S3' | 'GCS' | 'AzureBlob'
  bucket?: string
  container?: string
  storageAccount?: string
  prefix?: string
  region?: string
  secretRef?: SecretRef
}

export interface Galaxy {
  metadata: ObjectMeta
  spec: {
    universeRef: string
    displayName?: string
    description?: string
    defaultVCSProvider?: string
    defaultVCSURL?: string
    vcsSecretRef?: SecretRef
    backendConfig?: BackendConfig
    approvalPolicy?: 'Auto' | 'Manual' | 'RequiresReview'
    archived?: boolean
  }
  status?: {
    phase?: string
    astralCount?: number
    flareCount?: number
    conditions?: Condition[]
  }
}

// --- Astral ---------------------------------------------------------------

export interface DriftDetectionSpec {
  enabled?: boolean
  interval?: string
}

export interface AstralSpec {
  universeRef: string
  galaxyRef: string
  displayName?: string
  description?: string
  vcsRepository: VCSRepository
  engine?: Engine
  engineVersion?: string
  terragrunt?: TerragruntSpec
  variables?: Record<string, string>
  secrets?: EnvSecretRef[]
  backendSecretRef?: SecretRef
  workspaceName?: string
  enableLocking?: boolean
  autoApply?: boolean
  planOnPR?: boolean
  applyOnMerge?: boolean
  dependsOn?: string[]
  driftDetection?: DriftDetectionSpec
}

export type UnitResult = 'Succeeded' | 'Failed' | 'Skipped' | 'Excluded'

export interface UnitStatus {
  path: string
  syncStatus?: SyncStatus
  result?: UnitResult
  drift?: PlanSummary
  lastFlare?: string
  message?: string
}

export interface DriftStatus extends PlanSummary {
  detected: boolean
  lastDetected?: string
  lastChecked?: string
}

export interface FlareRef {
  name: string
  action?: FlareAction
  phase?: FlarePhase
  completionTime?: string
}

export interface AstralStatus {
  syncStatus?: SyncStatus
  activeFlare?: string
  lastFlareRef?: string
  lastSyncedRevision?: string
  lastSyncTime?: string
  units?: UnitStatus[]
  lastSuccessfulFlare?: string
  lastFlareStatus?: FlarePhase
  flareHistory?: FlareRef[]
  drift?: DriftStatus
  observedGeneration?: number
  conditions?: Condition[]
}

export interface Astral {
  apiVersion?: string
  kind?: string
  metadata: ObjectMeta
  spec: AstralSpec
  status?: AstralStatus
}

// --- Flare ----------------------------------------------------------------

export type FlareAction = 'Plan' | 'Apply' | 'Destroy' | 'Refresh'
export type FlarePhase =
  | 'Pending'
  | 'AwaitingApproval'
  | 'AwaitingConfirmation'
  | 'Running'
  | 'Succeeded'
  | 'Failed'
  | 'Cancelled'
export type FlareScope = 'All' | 'Unit'
export type FlareTrigger = 'Manual' | 'Schedule' | 'GitChange' | 'Drift'
export type FlareStep = 'GitFetch' | 'Init' | 'Plan' | 'Apply' | 'Report'
export type ChangeAction = 'Create' | 'Update' | 'Delete' | 'Replace'

export interface ResourceChange {
  address: string
  action: ChangeAction
  unit?: string
}

export interface FlareUnitResult {
  path: string
  result: UnitResult
  planSummary?: PlanSummary
  message?: string
}

export interface Flare {
  metadata: ObjectMeta
  spec: {
    universeRef: string
    galaxyRef: string
    astralRef: string
    action: FlareAction
    scope?: FlareScope
    unitPath?: string
    trigger?: FlareTrigger
    revision?: string
    planRef?: string
    confirmDestroy?: string
    approved?: boolean
    cancel?: boolean
    requestedBy?: string
  }
  status?: {
    phase?: FlarePhase
    step?: FlareStep
    jobName?: string
    attempt?: number
    revision?: string
    planSummary?: PlanSummary
    resourceChanges?: ResourceChange[]
    resourceChangesTruncated?: boolean
    pendingDestroy?: string[]
    unitResults?: FlareUnitResult[]
    message?: string
    startTime?: string
    completionTime?: string
    duration?: string
    errors?: string[]
    warnings?: string[]
    conditions?: Condition[]
  }
}

export interface List<T> {
  items: T[]
}

export interface FlareLog {
  available: boolean
  log: string
  message?: string
}

export interface AdminConfig {
  writesEnabled: boolean
}
