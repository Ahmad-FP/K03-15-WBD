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

  useEffect(() => {
    let active = true;
    setDetail(null);
    setSelectedSchema("");
    setSelectedTable("");
    setTableData(null);
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
        if (active) {
          onNotify(err instanceof Error ? err.message : "Gagal memuat skema DB", true);
        }
      });

    return () => {
      active = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dbId]);

  useEffect(() => {
    if (!selectedSchema || !selectedTable) return;
    apiRequest<TableViewResponse>(
      `/api/databases/${dbId}/tables/${encodeURIComponent(
        selectedSchema
      )}/${encodeURIComponent(selectedTable)}?page=${page}`
    )
      .then((res) => setTableData(res))
      .catch((err) =>
        onNotify(err instanceof Error ? err.message : "Gagal memuat data tabel", true)
      );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dbId, selectedSchema, selectedTable, page]);

  const currentSchemaObj = detail?.schemas.find((s) => s.name === selectedSchema);

  const columnNames: string[] = Array.isArray(tableData?.columns)
    ? tableData.columns.map((c) => (typeof c === "string" ? c : c.name))
    : [];

  return (
    <div className="stack-lg">
      <section className="card">
        <h3>Schema &amp; Table Browser</h3>
        <div className="form-row-3">
          <label>
            Schema
            <select
              value={selectedSchema}
              onChange={(e) => {
                setSelectedSchema(e.target.value);
                setPage(1);
                const sch = detail?.schemas.find((s) => s.name === e.target.value);
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
            Table
            <select
              value={selectedTable}
              onChange={(e) => {
                setSelectedTable(e.target.value);
                setPage(1);
              }}
            >
              {(currentSchemaObj?.tables ?? []).map((t) => (
                <option key={t.name} value={t.name}>
                  {t.name}
                </option>
              ))}
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
              onClick={() => setPage((p) => p + 1)}
            >
              Next Page
            </button>
          </div>
        </div>
      </section>

      {tableData && (
        <section className="card">
          <h4>
            {tableData.schema}.{tableData.table} (Page {tableData.page})
          </h4>
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  {columnNames.map((col) => (
                    <th key={col}>{col}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {(tableData.data ?? []).map((row, rIdx) => {
                  const isArr = Array.isArray(row);
                  return (
                    <tr key={rIdx}>
                      {columnNames.map((col, cIdx) => {
                        const val = isArr
                          ? row[cIdx]
                          : (row as Record<string, unknown>)[col];
                        return (
                          <td key={col}>
                            {val === null || val === undefined
                              ? "NULL"
                              : typeof val === "object"
                              ? JSON.stringify(val)
                              : String(val)}
                          </td>
                        );
                      })}
                    </tr>
                  );
                })}
                {(tableData.data ?? []).length === 0 && (
                  <tr>
                    <td colSpan={Math.max(1, columnNames.length)} className="muted">
                      Tidak ada baris data.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </section>
      )}
    </div>
  );
};