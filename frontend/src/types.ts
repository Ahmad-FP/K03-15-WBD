export type ExecutionStatus =
  | "pending_review"
  | "approved"
  | "rejected"
  | "pending"
  | "running"
  | "success"
  | "failed"
  | "partial"
  | "partially_reverted";

export type ExecutionOperation =
  | "up"
  | "rollback"
  | "forward"
  | "retry"
  | "compensate";

export type ApprovalStatus = "pending" | "approved" | "rejected";
export type JobStatus = "pending" | "running" | "done" | "failed";

export interface User {
  id: number;
  username: string;
  is_admin: boolean;
}

export interface Group {
  id: number;
  name: string;
  description: string;
  leader_id: number | null;
  leader_username?: string | null;
}

export interface GroupMember {
  id: number;
  username: string;
}

export interface ManagedDatabase {
  id: number;
  name: string;
  host_label: string;
  created_at: string;
}

export interface DatabaseAccessGrant {
  group_id: number;
  group_name: string;
  access_level: "view_only" | "view_edit";
}

export interface DatabaseSummary {
  id: number;
  name: string;
  host_label: string;
  access_level: "view_only" | "view_edit" | string;
}

export interface DatabaseDetail {
  id: number;
  name: string;
  can_edit: boolean;
  schemas: Array<{
    name: string;
    tables: Array<{ name: string; kind?: string }>;
  }>;
}

export interface TableColumn {
  name: string;
  type: string;
  nullable: boolean;
  default: string;
}

export interface TableConstraint {
  name: string;
  type: string;
  definition: string;
}

export interface TableIndex {
  name: string;
  definition: string;
  unique: boolean;
  primary: boolean;
}

export interface TableViewResponse {
  schema: string;
  table: string;
  kind?: string;
  columns: TableColumn[];
  constraints: TableConstraint[];
  indexes: TableIndex[];
  data: unknown[][];
  has_next: boolean;
  page: number;
}

export interface MigrationItem {
  id: number;
  version: number;
  name: string;
  sql_up: string;
  sql_down: string;
  irreversible_reason: string;
  batch_column: string;
  batch_size: number;
  immutable: boolean;
  created_by_username?: string;
  created_at?: string;
  updated_at?: string;
  can_rollback?: boolean;
  can_forward?: boolean;
}

export interface ExecutionSummary {
  id: number;
  request_id: number | null;
  migration_id: number;
  migration_name: string;
  version: number;
  operation: ExecutionOperation;
  attempt: number;
  status: ExecutionStatus;
  reverted_execution_id: number | null;
  parent_execution_id: number | null;
  batches_committed: number;
  rows_processed: number;
  error: string | null;
  requested_by_username?: string;
  created_at?: string;
  started_at?: string | null;
  finished_at?: string | null;
}

export interface ExecutionApproval {
  group_id: number;
  group_name: string;
  leader_id: number | null;
  leader_username: string | null;
  status: ApprovalStatus;
  decided_by_username?: string | null;
  decided_at?: string | null;
}

export interface ExecutionDetail extends ExecutionSummary {
  sql_snapshot?: string;
  last_cursor?: string | null;
  latest_execution_id: number;
  job_id?: number | null;
  job_status?: JobStatus | null;
  approvals: ExecutionApproval[];
}
