import React, { useEffect, useState } from "react";
import { apiRequest } from "../api";
import { DatabaseDetail, TableViewResponse } from "../types";

interface Props {
  dbId: number;
  onNotify: (msg: string, isError?: boolean) => void;
}

export const ViewerPanel: React.FC<Props> = ({ dbId, onNotify }) => {
  const [detail, setDetail] = useState<DatabaseDetail | null>(null);
  const [selectedSchema, setSelectedSchema] = useState<string>("");
  const [selectedTable, setSelectedTable] = useState<string>("");
  const [page, setPage] = useState<number>(1);
  const [tableData, setTableData] = useState<TableViewResponse | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [reloadKey, setReloadKey] = useState<number>(0);

  useEffect(() => {
    let active = true;
    setDetail(null);
    setSelectedSchema("");
    setSelectedTable("");
    setTableData(null);
    setLoadError(null);
    setPage(1);

    apiRequest<DatabaseDetail>(`/api/databases/${dbId}`)
      .then((res) => {
        if (!active) return;
        setDetail(res);
        const firstSchema = res.schemas?.[0];
        if (firstSchema) {
          setSelectedSchema(firstSchema.name);
          const firstTbl = firstSchema.tables?.[0]?.name;
          if (firstTbl) setSelectedTable(firstTbl);
        }
      })
      .catch((err) => {
        if (!active) return;
        const msg = err instanceof Error ? err.message : "Gagal memuat skema DB";
        setLoadError(msg);
        onNotify(msg, true);
      });

    return () => {
      active = false;
    };
  }, [dbId, reloadKey, onNotify]);

  useEffect(() => {
    if (!selectedSchema || !selectedTable) {
      setTableData(null);
      return;
    }
    let active = true;
    setLoadError(null);
    apiRequest<TableViewResponse>(
      `/api/databases/${dbId}/tables/${encodeURIComponent(
        selectedSchema
      )}/${encodeURIComponent(selectedTable)}?page=${page}`
    )
      .then((res) => {
        if (active) setTableData(res);
      })
      .catch((err) => {
        if (!active) return;
        const msg =
          err instanceof Error ? err.message : "Gagal memuat data tabel";
        setLoadError(msg);
        onNotify(msg, true);
      });
    return () => {
      active = false;
    };
  }, [dbId, selectedSchema, selectedTable, page, reloadKey, onNotify]);

  const currentSchemaObj = detail?.schemas.find(
    (s) => s.name === selectedSchema
  );
  const columns = tableData?.columns ?? [];
  const constraints = tableData?.constraints ?? [];
  const indexes = tableData?.indexes ?? [];

  return (
    <div className="stack-lg">
      <section className="card">
        <h3>Schema &amp; Table Browser</h3>
        {loadError && (
          <div className="alert-error actions-row">
            <span>{loadError}</span>
            <button
              type="button"
              className="btn-secondary btn-sm"
              onClick={() => setReloadKey((k) => k + 1)}
            >
              Coba Lagi
            </button>
          </div>
        )}
        <div className="form-row-3">
          <label>
            Schema
            <select
              value={selectedSchema}
              onChange={(e) => {
                setSelectedSchema(e.target.value);
                setPage(1);
                const sch = detail?.schemas.find(
                  (s) => s.name === e.target.value
                );
                setSelectedTable(sch?.tables?.[0]?.name ?? "");
              }}
            >
              {(detail?.schemas ?? []).map((s) => (
                <option key={s.name} value={s.name}>
                  {s.name}
                </option>
              ))}
            </select>
          </label>

          <label>
            Table / View
            <select
              value={selectedTable}
              onChange={(e) => {
                setSelectedTable(e.target.value);
                setPage(1);
              }}
            >
              {(currentSchemaObj?.tables ?? []).map((t) => (
                <option key={t.name} value={t.name}>
                  {t.kind ? `${t.name} (${t.kind})` : t.name}
                </option>
              ))}
              {(currentSchemaObj?.tables ?? []).length === 0 && (
                <option value="">Tidak ada tabel</option>
              )}
            </select>
          </label>

          <div className="actions-row align-end">
            <button
              type="button"
              className="btn-secondary"
              disabled={page <= 1}
              onClick={() => setPage((p) => Math.max(1, p - 1))}
            >
              Prev Page
            </button>
            <span>Page {page}</span>
            <button
              type="button"
              className="btn-secondary"
              disabled={!tableData?.has_next}
              onClick={() => setPage((p) => p + 1)}
            >
              Next Page
            </button>
          </div>
        </div>
      </section>

      {tableData && (
        <section className="card stack-lg">
          <h4>
            {tableData.schema}.{tableData.table}
            {tableData.kind ? ` (${tableData.kind})` : ""} — Page{" "}
            {tableData.page}
          </h4>

          <div>
            <h5>Columns ({columns.length})</h5>
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Type</th>
                    <th>Nullable</th>
                    <th>Default</th>
                  </tr>
                </thead>
                <tbody>
                  {columns.map((col) => (
                    <tr key={col.name}>
                      <td>{col.name}</td>
                      <td>
                        <code>{col.type}</code>
                      </td>
                      <td>{col.nullable ? "YES" : "NO"}</td>
                      <td>
                        <code>{col.default || "-"}</code>
                      </td>
                    </tr>
                  ))}
                  {columns.length === 0 && (
                    <tr>
                      <td colSpan={4} className="muted">
                        Tidak ada kolom.
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </div>

          <div>
            <h5>Constraints ({constraints.length})</h5>
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Type</th>
                    <th>Definition</th>
                  </tr>
                </thead>
                <tbody>
                  {constraints.map((con) => (
                    <tr key={con.name}>
                      <td>{con.name}</td>
                      <td>{con.type}</td>
                      <td>
                        <code>{con.definition}</code>
                      </td>
                    </tr>
                  ))}
                  {constraints.length === 0 && (
                    <tr>
                      <td colSpan={3} className="muted">
                        Tidak ada constraint.
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </div>

          <div>
            <h5>Indexes ({indexes.length})</h5>
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Unique</th>
                    <th>Primary</th>
                    <th>Definition</th>
                  </tr>
                </thead>
                <tbody>
                  {indexes.map((idx) => (
                    <tr key={idx.name}>
                      <td>{idx.name}</td>
                      <td>{idx.unique ? "YES" : "NO"}</td>
                      <td>{idx.primary ? "YES" : "NO"}</td>
                      <td>
                        <code>{idx.definition}</code>
                      </td>
                    </tr>
                  ))}
                  {indexes.length === 0 && (
                    <tr>
                      <td colSpan={4} className="muted">
                        Tidak ada index.
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </div>

          <div>
            <h5>Data</h5>
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    {columns.map((col) => (
                      <th key={col.name}>{col.name}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {(tableData.data ?? []).map((row, rIdx) => (
                    <tr key={rIdx}>
                      {columns.map((col, cIdx) => {
                        const val = row[cIdx];
                        return (
                          <td key={col.name}>
                            {val === null || val === undefined
                              ? "NULL"
                              : typeof val === "object"
                              ? JSON.stringify(val)
                              : String(val)}
                          </td>
                        );
                      })}
                    </tr>
                  ))}
                  {(tableData.data ?? []).length === 0 && (
                    <tr>
                      <td colSpan={Math.max(1, columns.length)} className="muted">
                        Tidak ada baris data.
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </div>
        </section>
      )}
    </div>
  );
};
