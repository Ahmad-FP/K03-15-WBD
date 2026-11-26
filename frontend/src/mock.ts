type AnyRecord = Record<string, any>;

const currentUser = {
  id: 1,
  username: "admin",
  is_admin: true,
};

const databases: AnyRecord[] = [
  {
    id: 1,
    name: "sample_shop",
    host_label: "localhost:5432",
    access_level: "view_edit",
  },
];

const users: AnyRecord[] = [
  currentUser,
  { id: 2, username: "leader_sales", is_admin: false },
  { id: 3, username: "analyst", is_admin: false },
];

const groups: AnyRecord[] = [
  {
    id: 1,
    name: "Sales",
    description: "Sales team",
    leader_id: 2,
    leader_username: "leader_sales",
  },
];

const migrations: AnyRecord[] = [
  {
    id: 1,
    version: 1,
    name: "Create customers",
    sql_up: "CREATE TABLE customers (id bigserial PRIMARY KEY, name text NOT NULL);",
    sql_down: "DROP TABLE customers;",
    irreversible_reason: "",
    batch_column: "",
    batch_size: 0,
    immutable: true,
    can_rollback: true,
    can_forward: false,
    created_at: new Date().toISOString(),
  },
  {
    id: 2,
    version: 2,
    name: "Backfill customer status",
    sql_up:
      "WITH batch AS (SELECT id FROM customers WHERE ($1::bigint IS NULL OR id > $1::bigint) ORDER BY id LIMIT $2), upd AS (UPDATE customers SET status = 'active' FROM batch WHERE customers.id = batch.id RETURNING batch.id) SELECT id::text AS last_cursor FROM upd ORDER BY id",
    sql_down: "",
    irreversible_reason:
      "Rollback requires restoring customer state from an external snapshot.",
    batch_column: "id",
    batch_size: 1000,
    immutable: false,
    can_rollback: false,
    can_forward: false,
    created_at: new Date().toISOString(),
  },
];

const executions: AnyRecord[] = [
  {
    id: 5,
    request_id: 5,
    migration_id: 1,
    migration_name: "Create customers",
    version: 1,
    operation: "up",
    attempt: 1,
    status: "success",
    reverted_execution_id: null,
    parent_execution_id: null,
    batches_committed: 1,
    rows_processed: 0,
    error: "",
    requested_by_username: "admin",
    created_at: new Date().toISOString(),
    sql_snapshot: migrations[0].sql_up,
    latest_execution_id: 5,
    job_id: 10,
    job_status: "done",
    approvals: [
      {
        group_id: 1,
        group_name: "Sales",
        leader_id: 2,
        leader_username: "leader_sales",
        status: "approved",
        decided_by_username: "leader_sales",
      },
    ],
  },
];

let nextMigrationId = 3;
let nextExecutionId = 6;

function parseBody(raw: RequestInit["body"]): AnyRecord {
  if (!raw) return {};
  try {
    return JSON.parse(String(raw));
  } catch {
    return {};
  }
}

function findLeaderUsername(leaderId: unknown): string | null {
  if (leaderId === null || leaderId === undefined || leaderId === "") {
    return null;
  }
  const found = users.find((u) => u.id === Number(leaderId));
  return found ? found.username : null;
}

export async function mockApi<T>(
  path: string,
  init: RequestInit = {}
): Promise<T> {
  const method = (init.method || "GET").toUpperCase();
  const body = parseBody(init.body);
  const cleanPath = path.split("?")[0];

  const ok = <R>(value: R): Promise<T> => Promise.resolve(value as unknown as T);
  const notFound = (): Promise<T> =>
    Promise.reject(new Error(`Mock endpoint belum tersedia: ${method} ${path}`));

  // Auth
  if (cleanPath === "/api/auth/me") return ok(currentUser);
  if (cleanPath === "/api/auth/login") return ok(currentUser);
  if (cleanPath === "/api/auth/logout") return ok(undefined);

  // Users
  if (cleanPath === "/api/users" && method === "GET") return ok(users);
  if (cleanPath === "/api/users" && method === "POST") {
    const newUser = {
      id: users.length + 1,
      is_admin: false,
      ...body,
    };
    users.push(newUser);
    return ok(newUser);
  }

  // Groups
  if (cleanPath === "/api/groups" && method === "GET") return ok(groups);
  if (cleanPath === "/api/groups" && method === "POST") {
    const leaderId = body.leader_id ?? null;
    const newGroup = {
      id: groups.length + 1,
      description: "",
      ...body,
      leader_id: leaderId,
      leader_username: findLeaderUsername(leaderId),
    };
    groups.push(newGroup);
    return ok(newGroup);
  }

  const groupMatch = cleanPath.match(
    /^\/api\/groups\/(\d+)(?:\/members(?:\/(\d+))?)?$/
  );
  if (groupMatch) {
    const gid = Number(groupMatch[1]);
    const group = groups.find((g) => g.id === gid);

    if (cleanPath.endsWith("/members") && method === "GET") {
      return ok(users.filter((u) => u.id === 2));
    }
    if (cleanPath.endsWith("/members") && method === "POST") {
      return ok(undefined);
    }
    if (groupMatch[2] && method === "DELETE") {
      return ok(undefined);
    }
    if (method === "PUT" && group) {
      Object.assign(group, body);
      group.leader_username = findLeaderUsername(group.leader_id);
      return ok(group);
    }
    if (method === "DELETE" && group) {
      const idx = groups.indexOf(group);
      if (idx !== -1) groups.splice(idx, 1);
      return ok(undefined);
    }
  }

  // Admin DB endpoints
  if ((cleanPath === "/api/databases" || cleanPath === "/api/db") && method === "GET") {
    return ok(databases);
  }
  if (cleanPath === "/api/db" && method === "POST") {
    const newDb = { id: databases.length + 1, ...body };
    databases.push(newDb);
    return ok(newDb);
  }

  const adminDbMatch = cleanPath.match(
    /^\/api\/db\/(\d+)(?:\/(access)(?:\/(\d+))?|\/test)?$/
  );
  if (adminDbMatch) {
    const dbId = Number(adminDbMatch[1]);
    if (cleanPath.endsWith("/test")) return ok({ status: "ok" });
    if (adminDbMatch[2]) {
      if (method === "GET") {
        return ok([
          { group_id: 1, group_name: "Sales", access_level: "view_edit" },
        ]);
      }
      return ok(undefined);
    }
    if (method === "PUT") {
      return ok(databases.find((d) => d.id === dbId) ?? null);
    }
    if (method === "DELETE") return ok(undefined);
  }

  // Database detail, migrations, executions & tables
  const dbMatch = cleanPath.match(/^\/api\/databases\/(\d+)(?:\/(.*))?$/);
  if (dbMatch) {
    const dbId = Number(dbMatch[1]);
    const rest = dbMatch[2] || "";

    if (!rest) {
      return ok({
        id: dbId,
        name: "sample_shop",
        can_edit: true,
        schemas: [
          {
            name: "public",
            tables: [
              { name: "customers", kind: "table" },
              { name: "orders", kind: "table" },
            ],
          },
        ],
      });
    }

    if (rest === "migrations" && method === "GET") return ok(migrations);
    if (rest === "migrations" && method === "POST") {
      const maxVersion =
        migrations.length > 0
          ? Math.max(...migrations.map((m) => Number(m.version) || 0))
          : 0;
      const newMigration = {
        id: nextMigrationId++,
        version: maxVersion + 1,
        irreversible_reason: "",
        batch_column: "",
        batch_size: 0,
        sql_down: "",
        ...body,
        immutable: false,
        created_at: new Date().toISOString(),
      };
      migrations.push(newMigration);
      return ok(newMigration);
    }

    const migrationMatch = rest.match(
      /^migrations\/(\d+)(?:\/(submit|rollback))?$/
    );
    if (migrationMatch) {
      const migId = Number(migrationMatch[1]);
      const action = migrationMatch[2];
      const item = migrations.find((m) => m.id === migId);
      if (!item) return notFound();

      if (action) {
        const execId = nextExecutionId++;
        const operation =
          action === "rollback" ? "rollback" : body.operation || "up";
        const newExec = {
          id: execId,
          request_id: execId,
          migration_id: item.id,
          migration_name: item.name,
          version: item.version,
          operation,
          attempt: 1,
          status: "pending_review",
          reverted_execution_id: null,
          parent_execution_id: null,
          batches_committed: 0,
          rows_processed: 0,
          error: "",
          requested_by_username: currentUser.username,
          created_at: new Date().toISOString(),
          sql_snapshot:
            operation === "rollback" ? item.sql_down : item.sql_up,
          latest_execution_id: execId,
          approvals: [
            {
              group_id: 1,
              group_name: "Sales",
              leader_id: 2,
              leader_username: "leader_sales",
              status: "pending",
              decided_by_username: null,
            },
          ],
        };
        executions.unshift(newExec);
        return ok({ execution_id: newExec.id });
      }

      if (method === "GET") return ok(item);
      if (method === "PUT") {
        Object.assign(item, body);
        return ok(item);
      }
      if (method === "DELETE") {
        const idx = migrations.indexOf(item);
        if (idx !== -1) migrations.splice(idx, 1);
        return ok(undefined);
      }
    }

    if (rest === "executions" && method === "GET") return ok(executions);

    const execMatch = rest.match(
      /^executions\/(\d+)(?:\/(approve|enqueue|cancel|retry|compensate))?$/
    );
    if (execMatch) {
      const execId = Number(execMatch[1]);
      const action = execMatch[2];
      const item = executions.find((e) => e.id === execId);
      if (!item) return notFound();

      if (action) {
        if (action === "approve") {
          const isRejected = body.decision === "rejected";
          if (Array.isArray(item.approvals) && item.approvals[0]) {
            item.approvals[0].status = isRejected ? "rejected" : "approved";
            item.approvals[0].decided_by_username = currentUser.username;
          }
          item.status = isRejected ? "rejected" : "approved";
        } else if (action === "enqueue") {
          item.status = "running";
          item.job_status = "running";
          item.job_id = 10;
        } else if (action === "cancel") {
          item.status = "rejected";
        } else if (action === "retry" || action === "compensate") {
          item.status = "pending_review";
        }
        return ok(undefined);
      }

      return ok(item);
    }

    const tableMatch = rest.match(
      /^tables\/([^/]+)\/([^/]+)(?:\?page=(\d+))?$/
    );
    if (tableMatch) {
      const queryPage =
        Number(tableMatch[3]) ||
        Number(new URLSearchParams(path.split("?")[1] || "").get("page")) ||
        1;
      return ok({
        schema: decodeURIComponent(tableMatch[1]),
        table: decodeURIComponent(tableMatch[2]),
        kind: "table",
        columns: [
          { name: "id", type: "bigint", nullable: false },
          { name: "name", type: "text", nullable: false },
        ],
        constraints: [],
        indexes: [],
        data: {
          columns: ["id", "name"],
          rows: [
            [
              { Text: "1", Null: false },
              { Text: "Alice", Null: false },
            ],
            [
              { Text: "2", Null: false },
              { Text: "Bob", Null: false },
            ],
          ],
          has_next: false,
        },
        page: queryPage,
      });
    }
  }

  return notFound();
}