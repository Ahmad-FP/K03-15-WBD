import React, { useEffect, useMemo, useRef, useState } from "react";
import { apiRequest, ApiError } from "../api";
import {
  ExecutionDetail,
  ExecutionSummary,
  MigrationItem,
  User,
} from "../types";

interface Props {
  dbId: number;
  canEdit: boolean;
  currentUser: User;
  onNotify: (msg: string, isError?: boolean) => void;
}

const TERMINAL_JOB_STATES = new Set(["done", "failed"]);
const TERMINAL_EXEC_STATES = new Set([
  "success",
  "failed",
  "rejected",
  "partial",
  "partially_reverted",
]);

function isExecutionTerminal(detail: ExecutionDetail): boolean {
  if (detail.job_status !== undefined && detail.job_status !== null) {
    return TERMINAL_JOB_STATES.has(detail.job_status);
  }
  return TERMINAL_EXEC_STATES.has(detail.status);
}

function fmtTime(iso?: string | null): string {
  if (!iso) return "-";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

export const MigrationsPanel: React.FC<Props> = ({
  dbId,
  canEdit,
  currentUser,
  onNotify,
}) => {
  const [migrations, setMigrations] = useState<MigrationItem[]>([]);
  const [selectedMid, setSelectedMid] = useState<number | null>(null);
  const [selectedMigrationDetail, setSelectedMigrationDetail] =
    useState<MigrationItem | null>(null);
  const [executions, setExecutions] = useState<ExecutionSummary[]>([]);
  const [hideRollback, setHideRollback] = useState<boolean>(false);

  const [selectedEid, setSelectedEid] = useState<number | null>(null);
  const [execDetail, setExecDetail] = useState<ExecutionDetail | null>(null);
  const [pollingActive, setPollingActive] = useState<boolean>(false);

  // Form definisi migration (create / edit)
  const [editingMid, setEditingMid] = useState<number | null>(null);
  const [name, setName] = useState("");
  const [sqlUp, setSqlUp] = useState("");
  const [sqlDown, setSqlDown] = useState("");
  const [irreversibleReason, setIrreversibleReason] = useState("");
  const [batchColumn, setBatchColumn] = useState("");
  const [batchSize, setBatchSize] = useState<number>(0);
  const [submittingForm, setSubmittingForm] = useState(false);

  const pollTimerRef = useRef<number | null>(null);
  const dbIdRef = useRef<number>(dbId);
  const hideRollbackRef = useRef<boolean>(hideRollback);
  dbIdRef.current = dbId;
  hideRollbackRef.current = hideRollback;

  const selectedMigration = useMemo(
    () =>
      selectedMigrationDetail?.id === selectedMid
        ? selectedMigrationDetail
        : migrations.find((m) => m.id === selectedMid) ?? null,
    [migrations, selectedMid, selectedMigrationDetail]
  );

  const loadMigrations = async (keepSelection = true) => {
    const reqDb = dbId;
    try {
      const list = await apiRequest<MigrationItem[]>(
        `/api/databases/${reqDb}/migrations`
      );
      if (dbIdRef.current !== reqDb) return;
      setMigrations(list);
      setSelectedMid((prev) =>
        keepSelection && prev !== null && list.some((m) => m.id === prev)
          ? prev
          : (list[0]?.id ?? null)
      );
    } catch (err) {
      if (dbIdRef.current === reqDb) {
        onNotify(
          err instanceof Error ? err.message : "Gagal memuat migrations",
          true
        );
      }
    }
  };

  const loadExecutions = async (
    hideRb = hideRollbackRef.current,
    resetSelection = false
  ) => {
    const reqDb = dbId;
    try {
      const list = await apiRequest<ExecutionSummary[]>(
        `/api/databases/${reqDb}/executions?hide_rollback=${hideRb ? "true" : "false"}`
      );
      if (dbIdRef.current !== reqDb) return;
      setExecutions(list);
      setSelectedEid((prev) => {
        if (resetSelection || prev === null) return list[0]?.id ?? null;
        return prev;
      });
    } catch (err) {
      if (dbIdRef.current === reqDb) {
        onNotify(
          err instanceof Error ? err.message : "Gagal memuat riwayat eksekusi",
          true
        );
      }
    }
  };

  const fetchExecutionDetail = async (
    eid: number,
    followLatest = false
  ): Promise<ExecutionDetail | null> => {
    const reqDb = dbId;
    try {
      let current = await apiRequest<ExecutionDetail>(
        `/api/databases/${reqDb}/executions/${eid}`
      );
      if (dbIdRef.current !== reqDb) return null;
      if (
        followLatest &&
        current.latest_execution_id &&
        current.latest_execution_id !== current.id
      ) {
        current = await apiRequest<ExecutionDetail>(
          `/api/databases/${reqDb}/executions/${current.latest_execution_id}`
        );
        if (dbIdRef.current !== reqDb) return null;
        setSelectedEid(current.id);
      }
      setExecDetail(current);

      if (isExecutionTerminal(current)) {
        setPollingActive(false);
        await loadExecutions(hideRollbackRef.current);
        await loadMigrations(true);
      }
      return current;
    } catch (err) {
      if (dbIdRef.current === reqDb) {
        setPollingActive(false);
        onNotify(
          err instanceof Error ? err.message : "Gagal memuat detail eksekusi",
          true
        );
      }
      return null;
    }
  };

  useEffect(() => {
    setSelectedMid(null);
    setSelectedMigrationDetail(null);
    setSelectedEid(null);
    setExecDetail(null);
    setPollingActive(false);
    resetForm();
    void loadMigrations(false);
    void loadExecutions(hideRollbackRef.current, true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dbId]);

  const didMountRef = useRef(false);
  useEffect(() => {
    if (!didMountRef.current) {
      didMountRef.current = true;
      return;
    }
    void loadExecutions(hideRollback);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hideRollback]);

  useEffect(() => {
    if (selectedMid === null) {
      setSelectedMigrationDetail(null);
      return;
    }
    let active = true;
    apiRequest<MigrationItem>(`/api/databases/${dbId}/migrations/${selectedMid}`)
      .then((res) => {
        if (active) setSelectedMigrationDetail(res);
      })
      .catch(() => {
        if (active) setSelectedMigrationDetail(null);
      });
    return () => {
      active = false;
    };
  }, [dbId, selectedMid, executions]);

  useEffect(() => {
    if (selectedEid !== null) {
      void fetchExecutionDetail(selectedEid, false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dbId, selectedEid]);

  useEffect(() => {
    if (pollTimerRef.current !== null) {
      window.clearInterval(pollTimerRef.current);
      pollTimerRef.current = null;
    }
    if (!pollingActive || selectedEid === null) return;

    pollTimerRef.current = window.setInterval(() => {
      void fetchExecutionDetail(selectedEid, true);
    }, 1200);

    return () => {
      if (pollTimerRef.current !== null) {
        window.clearInterval(pollTimerRef.current);
        pollTimerRef.current = null;
      }
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pollingActive, selectedEid, dbId]);

  const resetForm = () => {
    setEditingMid(null);
    setName("");
    setSqlUp("");
    setSqlDown("");
    setIrreversibleReason("");
    setBatchColumn("");
    setBatchSize(0);
  };

  const startEditMigration = (m: MigrationItem) => {
    if (m.immutable) {
      onNotify(
        "Migration bersifat immutable karena sudah pernah dieksekusi.",
        true
      );
      return;
    }
    setEditingMid(m.id);
    setName(m.name);
    setSqlUp(m.sql_up);
    setSqlDown(m.sql_down);
    setIrreversibleReason(m.irreversible_reason || "");
    setBatchColumn(m.batch_column || "");
    setBatchSize(m.batch_size || 0);
  };

  const handleSaveMigration = async (e: React.FormEvent) => {
    e.preventDefault();
    setSubmittingForm(true);
    try {
      const payload = {
        name: name.trim(),
        sql_up: sqlUp,
        sql_down: irreversibleReason.trim() ? "" : sqlDown,
        irreversible_reason: irreversibleReason.trim(),
        batch_column: batchSize > 0 ? batchColumn.trim() : "",
        batch_size: Math.max(0, Math.floor(Number(batchSize) || 0)),
      };
      if (editingMid !== null) {
        const updated = await apiRequest<MigrationItem>(
          `/api/databases/${dbId}/migrations/${editingMid}`,
          { method: "PUT", body: JSON.stringify(payload) }
        );
        onNotify(`Migration v${updated.version} diperbarui.`);
        setSelectedMid(updated.id);
      } else {
        const created = await apiRequest<MigrationItem>(
          `/api/databases/${dbId}/migrations`,
          { method: "POST", body: JSON.stringify(payload) }
        );
        onNotify(`Migration v${created.version} dibuat.`);
        setSelectedMid(created.id);
      }
      resetForm();
      await loadMigrations(true);
    } catch (err) {
      onNotify(
        err instanceof ApiError ? err.message : "Gagal menyimpan migration",
        true
      );
    } finally {
      setSubmittingForm(false);
    }
  };

  const handleDeleteMigration = async (m: MigrationItem) => {
    try {
      await apiRequest<void>(`/api/databases/${dbId}/migrations/${m.id}`, {
        method: "DELETE",
      });
      onNotify(`Migration v${m.version} dihapus.`);
      if (editingMid === m.id) resetForm();
      if (selectedMid === m.id) setSelectedMid(null);
      await loadMigrations(false);
    } catch (err) {
      onNotify(
        err instanceof Error ? err.message : "Gagal menghapus migration",
        true
      );
    }
  };

  const handleWorkflowRequest = async (
    path: string,
    label: string,
    startPolling = false
  ) => {
    try {
      const res = await apiRequest<{ execution_id?: number; job_id?: number }>(
        path,
        { method: "POST" }
      );
      onNotify(`${label} berhasil.`);
      await loadExecutions(hideRollbackRef.current);
      await loadMigrations(true);

      const targetEid = res?.execution_id ?? selectedEid;
      if (targetEid) {
        setSelectedEid(targetEid);
        const detail = await fetchExecutionDetail(targetEid, startPolling);
        if (startPolling && detail && !isExecutionTerminal(detail)) {
          setPollingActive(true);
        }
      }
    } catch (err) {
      onNotify(
        err instanceof Error ? err.message : `Gagal melakukan ${label}`,
        true
      );
    }
  };

  const handleDecision = async (
    eid: number,
    decision: "approved" | "rejected"
  ) => {
    try {
      await apiRequest<void>(
        `/api/databases/${dbId}/executions/${eid}/approve`,
        {
          method: "POST",
          body: JSON.stringify({ decision }),
        }
      );
      onNotify(
        decision === "approved"
          ? "Execution disetujui."
          : "Execution ditolak."
      );
      await fetchExecutionDetail(eid, false);
      await loadExecutions(hideRollbackRef.current);
    } catch (err) {
      onNotify(
        err instanceof Error
          ? err.message
          : "Gagal memproses keputusan approval",
        true
      );
    }
  };

  const isEditingImmutable =
    editingMid !== null &&
    migrations.find((m) => m.id === editingMid)?.immutable === true;

  const execMigration = execDetail
    ? migrations.find((m) => m.id === execDetail.migration_id) ?? null
    : null;
  const execRootId = execDetail
    ? execDetail.parent_execution_id ?? execDetail.request_id ?? execDetail.id
    : null;
  const isUnresolvedPartial =
    execDetail !== null &&
    (execDetail.status === "partial" ||
      execDetail.status === "partially_reverted") &&
    !executions.some((r) => {
      const rRoot = r.parent_execution_id ?? r.request_id ?? r.id;
      return (
        r.id > execDetail.id &&
        rRoot === execRootId &&
        (r.status === "success" ||
          r.status === "partial" ||
          r.status === "partially_reverted")
      );
    });
  const isExecReversible =
    execMigration !== null &&
    !execMigration.irreversible_reason &&
    Boolean(execMigration.sql_down.trim());
  const canRetry =
    execDetail !== null &&
    execDetail.status === "partial" &&
    isUnresolvedPartial;
  const canCompensate =
    execDetail !== null && isUnresolvedPartial && isExecReversible;

  return (
    <div className="stack-lg">
      {/* Editor Definisi Migration */}
      {canEdit && (
        <section className="card">
          <div className="card-header">
            <h3>
              {editingMid !== null
                ? `Edit Migration #${editingMid}`
                : "Buat Migration Baru"}
            </h3>
            {editingMid !== null && (
              <button
                type="button"
                className="btn-secondary"
                onClick={resetForm}
              >
                Batal Edit
              </button>
            )}
          </div>

          <form onSubmit={handleSaveMigration} className="form-grid">
            <label>
              Nama Migration
              <input
                type="text"
                required
                maxLength={100}
                disabled={isEditingImmutable}
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="contoh: backfill_items_status"
              />
            </label>

            <div className="form-row-2">
              <label>
                Batch Column (wajib jika batch_size &gt; 0)
                <input
                  type="text"
                  required={batchSize > 0}
                  disabled={isEditingImmutable || batchSize <= 0}
                  value={batchColumn}
                  onChange={(e) => setBatchColumn(e.target.value)}
                  placeholder="id"
                />
              </label>

              <label>
                Batch Size (0 = normal mode)
                <input
                  type="number"
                  min={0}
                  step={1}
                  disabled={isEditingImmutable}
                  value={batchSize}
                  onChange={(e) =>
                    setBatchSize(
                      Math.max(0, Math.floor(Number(e.target.value) || 0))
                    )
                  }
                />
              </label>
            </div>

            <label>
              SQL Up
              <textarea
                rows={5}
                required
                readOnly={isEditingImmutable}
                className="sql-editor"
                value={sqlUp}
                onChange={(e) => setSqlUp(e.target.value)}
                placeholder="UPDATE items SET status = 'migrated'..."
              />
            </label>

            <label>
              Irreversible Reason (kosongkan jika reversible / pakai SQL Down)
              <input
                type="text"
                disabled={isEditingImmutable}
                value={irreversibleReason}
                onChange={(e) => setIrreversibleReason(e.target.value)}
                placeholder="Contoh: data lama dihapus permanen"
              />
            </label>

            {!irreversibleReason.trim() && (
              <label>
                SQL Down (wajib jika reversible)
                <textarea
                  rows={5}
                  required={!irreversibleReason.trim()}
                  readOnly={isEditingImmutable}
                  className="sql-editor"
                  value={sqlDown}
                  onChange={(e) => setSqlDown(e.target.value)}
                  placeholder="UPDATE items SET status = 'pending'..."
                />
              </label>
            )}

            <div className="actions-row">
              <button
                type="submit"
                className="btn-primary"
                disabled={submittingForm || isEditingImmutable}
              >
                {submittingForm
                  ? "Menyimpan..."
                  : editingMid !== null
                  ? "Simpan Perubahan"
                  : "Simpan Migration"}
              </button>
            </div>
          </form>
        </section>
      )}

      {/* Daftar Migration & Aksi Submit / Rollback */}
      <section className="card">
        <div className="card-header">
          <h3>Daftar Migration</h3>
          <button
            type="button"
            className="btn-secondary"
            onClick={() => void loadMigrations(true)}
          >
            Refresh
          </button>
        </div>

        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Ver</th>
                <th>Nama</th>
                <th>Mode</th>
                <th>Reversibility</th>
                <th>Immutable</th>
                <th>Aksi</th>
              </tr>
            </thead>
            <tbody>
              {migrations.map((m) => {
                const isIrreversible = Boolean(m.irreversible_reason);
                return (
                  <tr
                    key={m.id}
                    className={selectedMid === m.id ? "row-active" : undefined}
                  >
                    <td>v{m.version}</td>
                    <td>
                      <button
                        type="button"
                        className="link-btn"
                        onClick={() => setSelectedMid(m.id)}
                      >
                        {m.name}
                      </button>
                    </td>
                    <td>
                      {m.batch_size > 0
                        ? `Batch (${m.batch_column}: ${m.batch_size})`
                        : "Normal"}
                    </td>
                    <td>
                      {isIrreversible
                        ? `Irreversible: ${m.irreversible_reason}`
                        : "Reversible"}
                    </td>
                    <td>{m.immutable ? "Read-only" : "Editable"}</td>
                    <td className="cell-actions">
                      {canEdit && (
                        <>
                          <button
                            type="button"
                            className="btn-primary btn-sm"
                            onClick={() =>
                              void handleWorkflowRequest(
                                `/api/databases/${dbId}/migrations/${m.id}/submit`,
                                `Submit v${m.version}`
                              )
                            }
                          >
                            Submit
                          </button>

                          {m.immutable && !isIrreversible && (
                            <button
                              type="button"
                              className="btn-secondary btn-sm"
                              onClick={() =>
                                void handleWorkflowRequest(
                                  `/api/databases/${dbId}/migrations/${m.id}/rollback`,
                                  `Rollback v${m.version}`
                                )
                              }
                            >
                              Rollback
                            </button>
                          )}

                          {!m.immutable && (
                            <>
                              <button
                                type="button"
                                className="btn-secondary btn-sm"
                                onClick={() => startEditMigration(m)}
                              >
                                Edit
                              </button>
                              <button
                                type="button"
                                className="btn-danger btn-sm"
                                onClick={() => void handleDeleteMigration(m)}
                              >
                                Hapus
                              </button>
                            </>
                          )}
                        </>
                      )}
                    </td>
                  </tr>
                );
              })}
              {migrations.length === 0 && (
                <tr>
                  <td colSpan={6} className="muted">
                    Belum ada definisi migration.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>

        {selectedMigration && (
          <div className="detail-box">
            <div className="card-header">
              <h4>
                SQL Preview — v{selectedMigration.version} (
                {selectedMigration.name})
              </h4>
              {canEdit && (
                <div className="actions-row">
                  {selectedMigration.can_forward && (
                    <button
                      type="button"
                      className="btn-primary btn-sm"
                      onClick={() =>
                        void handleWorkflowRequest(
                          `/api/databases/${dbId}/migrations/${selectedMigration.id}/submit`,
                          `Forward v${selectedMigration.version}`
                        )
                      }
                    >
                      Forward v{selectedMigration.version}
                    </button>
                  )}
                  {selectedMigration.can_rollback && (
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      onClick={() =>
                        void handleWorkflowRequest(
                          `/api/databases/${dbId}/migrations/${selectedMigration.id}/rollback`,
                          `Rollback v${selectedMigration.version}`
                        )
                      }
                    >
                      Rollback v{selectedMigration.version}
                    </button>
                  )}
                </div>
              )}
            </div>
            <div className="sql-scroll">
              <pre>
                <code>{selectedMigration.sql_up}</code>
              </pre>
            </div>
            {selectedMigration.sql_down && (
              <>
                <h5>SQL Down</h5>
                <div className="sql-scroll">
                  <pre>
                    <code>{selectedMigration.sql_down}</code>
                  </pre>
                </div>
              </>
            )}
          </div>
        )}
      </section>

      {/* Detail Execution, Approval per Group Leader, Enqueue & Live Polling */}
      {execDetail && (
        <section className="card">
          <div className="card-header">
            <div>
              <h3>
                Execution #{execDetail.id} (Latest #
                {execDetail.latest_execution_id})
              </h3>
              <p className="muted">
                m#{execDetail.migration_id} • v{execDetail.version} • {execDetail.operation} • attempt #
                {execDetail.attempt} • status:{" "}
                <strong>{execDetail.status}</strong>
                {execDetail.job_id ? ` • job #${execDetail.job_id}` : ""}
                {execDetail.job_status ? ` (${execDetail.job_status})` : ""}
              </p>
            </div>
            <div className="actions-row">
              {execDetail.latest_execution_id !== execDetail.id && (
                <button
                  type="button"
                  className="btn-secondary btn-sm"
                  onClick={() => setSelectedEid(execDetail.latest_execution_id)}
                >
                  Ke Attempt Terbaru (#{execDetail.latest_execution_id})
                </button>
              )}
              <button
                type="button"
                className="btn-secondary btn-sm"
                onClick={() => void fetchExecutionDetail(execDetail.id, false)}
              >
                Cek Status
              </button>
              <button
                type="button"
                className="btn-secondary btn-sm"
                onClick={() => setPollingActive((prev) => !prev)}
              >
                {pollingActive ? "Stop Polling" : "Live Poll"}
              </button>
            </div>
          </div>

          <div className="metrics-grid">
            <div>
              <span className="muted">Batches Committed</span>
              <div>
                <strong>{execDetail.batches_committed}</strong>
              </div>
            </div>
            <div>
              <span className="muted">Rows Processed</span>
              <div>
                <strong>{execDetail.rows_processed}</strong>
              </div>
            </div>
            <div>
              <span className="muted">Last Cursor</span>
              <div>
                <code>{execDetail.last_cursor || "-"}</code>
              </div>
            </div>
            <div>
              <span className="muted">Request / Parent / Reverted</span>
              <div>
                <code>
                  req:{execDetail.request_id ?? execDetail.id} / par:
                  {execDetail.parent_execution_id ?? "-"} / rev:
                  {execDetail.reverted_execution_id ?? "-"}
                </code>
              </div>
            </div>
            <div>
              <span className="muted">Dibuat / Mulai / Selesai</span>
              <div>
                <small>
                  {fmtTime(execDetail.created_at)} /{" "}
                  {fmtTime(execDetail.started_at)} /{" "}
                  {fmtTime(execDetail.finished_at)}
                </small>
              </div>
            </div>
          </div>

          {execDetail.error && (
            <div className="alert-error">Error: {execDetail.error}</div>
          )}

          {/* Tombol Workflow Execution */}
          {canEdit && (
            <div className="actions-row">
              {execDetail.status === "approved" && (
                <button
                  type="button"
                  className="btn-primary"
                  onClick={() =>
                    void handleWorkflowRequest(
                      `/api/databases/${dbId}/executions/${execDetail.id}/enqueue`,
                      `Enqueue #${execDetail.id}`,
                      true
                    )
                  }
                >
                  Enqueue
                </button>
              )}

              {(execDetail.status === "pending_review" ||
                execDetail.status === "approved") && (
                <button
                  type="button"
                  className="btn-danger"
                  onClick={() =>
                    void handleWorkflowRequest(
                      `/api/databases/${dbId}/executions/${execDetail.id}/cancel`,
                      `Cancel #${execDetail.id}`
                    )
                  }
                >
                  Cancel
                </button>
              )}

              {canRetry && (
                <button
                  type="button"
                  className="btn-primary"
                  onClick={() =>
                    void handleWorkflowRequest(
                      `/api/databases/${dbId}/executions/${execDetail.id}/retry`,
                      `Retry #${execDetail.id}`
                    )
                  }
                >
                  Retry
                </button>
              )}

              {canCompensate && (
                <button
                  type="button"
                  className="btn-secondary"
                  onClick={() =>
                    void handleWorkflowRequest(
                      `/api/databases/${dbId}/executions/${execDetail.id}/compensate`,
                      `Compensate #${execDetail.id}`
                    )
                  }
                >
                  Compensate
                </button>
              )}
            </div>
          )}

          {/* Approval per Group Leader */}
          <div className="subsection">
            <h4>Group Approvals</h4>
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>Group</th>
                    <th>Leader</th>
                    <th>Status</th>
                    <th>Decided By</th>
                    <th>Waktu</th>
                    <th>Aksi Leader</th>
                  </tr>
                </thead>
                <tbody>
                  {(execDetail.approvals || []).map((ap) => {
                    const isLeaderOfThisGroup =
                      ap.leader_id !== null && ap.leader_id === currentUser.id;
                    const canDecide =
                      execDetail.status === "pending_review" &&
                      ap.status === "pending" &&
                      isLeaderOfThisGroup;

                    return (
                      <tr key={ap.group_id}>
                        <td>{ap.group_name}</td>
                        <td>{ap.leader_username || "-"}</td>
                        <td>{ap.status}</td>
                        <td>{ap.decided_by_username || "-"}</td>
                        <td>{fmtTime(ap.decided_at)}</td>
                        <td className="cell-actions">
                          {canDecide ? (
                            <>
                              <button
                                type="button"
                                className="btn-primary btn-sm"
                                onClick={() =>
                                  void handleDecision(execDetail.id, "approved")
                                }
                              >
                                Approve
                              </button>
                              <button
                                type="button"
                                className="btn-danger btn-sm"
                                onClick={() =>
                                  void handleDecision(execDetail.id, "rejected")
                                }
                              >
                                Reject
                              </button>
                            </>
                          ) : (
                            <span className="muted">-</span>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>

          {execDetail.sql_snapshot && (
            <div className="subsection">
              <h4>SQL Snapshot</h4>
              <div className="sql-scroll">
                <pre>
                  <code>{execDetail.sql_snapshot}</code>
                </pre>
              </div>
            </div>
          )}
        </section>
      )}

      {/* Migration History + Hide Rollback Checkbox */}
      <section className="card">
        <div className="card-header">
          <h3>Migration History</h3>
          <label className="checkbox-inline">
            <input
              type="checkbox"
              checked={hideRollback}
              onChange={(e) => setHideRollback(e.target.checked)}
            />
            <span>Hide rollback</span>
          </label>
        </div>

        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Exec ID</th>
                <th>Migration</th>
                <th>Op</th>
                <th>Attempt</th>
                <th>Status</th>
                <th>Batches / Rows</th>
                <th>Requested By</th>
                <th>Waktu</th>
                <th>Detail</th>
              </tr>
            </thead>
            <tbody>
              {executions.map((ex) => (
                <tr
                  key={ex.id}
                  className={selectedEid === ex.id ? "row-active" : undefined}
                >
                  <td>#{ex.id}</td>
                  <td>
                    v{ex.version} — {ex.migration_name}
                  </td>
                  <td>{ex.operation}</td>
                  <td>#{ex.attempt}</td>
                  <td>
                    <strong>{ex.status}</strong>
                    {ex.reverted_execution_id
                      ? ` (reverts #${ex.reverted_execution_id})`
                      : ""}
                  </td>
                  <td>
                    {ex.batches_committed} / {ex.rows_processed}
                  </td>
                  <td>{ex.requested_by_username || "-"}</td>
                  <td>{fmtTime(ex.created_at)}</td>
                  <td>
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      onClick={() => {
                        setSelectedEid(ex.id);
                        setPollingActive(
                          ex.status === "pending" || ex.status === "running"
                        );
                      }}
                    >
                      Inspect
                    </button>
                  </td>
                </tr>
              ))}
              {executions.length === 0 && (
                <tr>
                  <td colSpan={9} className="muted">
                    Tidak ada riwayat eksekusi.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
};
