import React, { useCallback, useEffect, useState } from "react";
import { apiRequest, ApiError, setUnauthorizedHandler } from "./api";
import { DatabaseSummary, User } from "./types";
import { ViewerPanel } from "./components/ViewerPanel";
import { MigrationsPanel } from "./components/MigrationsPanel";
import { AdminPanel } from "./components/AdminPanel";

type ActiveTab = "viewer" | "migrations" | "admin";

export default function App() {
  const [user, setUser] = useState<User | null>(null);
  const [checkingAuth, setCheckingAuth] = useState<boolean>(true);

  // Login state
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [authError, setAuthError] = useState<string | null>(null);

  // Databases & navigation
  const [databases, setDatabases] = useState<DatabaseSummary[]>([]);
  const [selectedDbId, setSelectedDbId] = useState<number | null>(null);
  const [tab, setTab] = useState<ActiveTab>("migrations");
  const [toast, setToast] = useState<{ text: string; isError: boolean } | null>(
    null
  );

  const notify = useCallback((text: string, isError = false) => {
    setToast({ text, isError });
    window.setTimeout(() => {
      setToast((prev) => (prev?.text === text ? null : prev));
    }, 4500);
  }, []);

  const refreshDatabases = useCallback(async (): Promise<DatabaseSummary[]> => {
    const dbs = await apiRequest<DatabaseSummary[]>("/api/databases");
    setDatabases(dbs);
    setSelectedDbId((prev) =>
      prev && dbs.some((d) => d.id === prev) ? prev : (dbs[0]?.id ?? null)
    );
    return dbs;
  }, []);

  useEffect(() => {
    setUnauthorizedHandler(() => {
      setUser((prev) => {
        if (prev !== null) {
          setAuthError("Sesi berakhir atau dicabut, silakan masuk kembali.");
        }
        return null;
      });
      setDatabases([]);
      setSelectedDbId(null);
      setTab("migrations");
    });
    return () => setUnauthorizedHandler(null);
  }, []);

  useEffect(() => {
    let active = true;
    const loadSessionAndDbs = async () => {
      setCheckingAuth(true);
      try {
        const me = await apiRequest<User>("/api/auth/me");
        if (!active) return;
        setUser(me);
        try {
          const dbs = await refreshDatabases();
          if (active && me.is_admin && dbs.length === 0) {
            setTab("admin");
          }
        } catch (err) {
          if (active && !(err instanceof ApiError && err.status === 401)) {
            notify(
              err instanceof Error ? err.message : "Gagal memuat daftar database",
              true
            );
          }
        }
      } catch (err) {
        if (active && err instanceof ApiError && err.status === 401) {
          setUser(null);
        } else if (active) {
          setUser(null);
        }
      } finally {
        if (active) setCheckingAuth(false);
      }
    };
    void loadSessionAndDbs();
    return () => {
      active = false;
    };
  }, [notify, refreshDatabases]);

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault();
    setAuthError(null);
    try {
      const loggedIn = await apiRequest<User>("/api/auth/login", {
        method: "POST",
        body: JSON.stringify({ username, password }),
      });
      setUser(loggedIn);
      setPassword("");
      const dbs = await refreshDatabases();
      setTab(loggedIn.is_admin && dbs.length === 0 ? "admin" : "migrations");
    } catch (err) {
      setAuthError(err instanceof Error ? err.message : "Login gagal");
    }
  };

  const handleLogout = async () => {
    try {
      await apiRequest<void>("/api/auth/logout", { method: "POST" });
    } catch {
      // tetap bersihkan state lokal bila sesi sudah berakhir
    } finally {
      setUser(null);
      setDatabases([]);
      setSelectedDbId(null);
      setTab("migrations");
      setAuthError(null);
    }
  };

  if (checkingAuth) {
    return (
      <div className="page-center">
        <div className="card">Memeriksa sesi...</div>
      </div>
    );
  }

  if (!user) {
    return (
      <div className="page-center">
        <form onSubmit={handleLogin} className="card login-card">
          <h2>Login</h2>
          <p className="muted">Masuk untuk mengakses database &amp; migration workflow.</p>
          {authError && <div className="alert-error">{authError}</div>}
          <label>
            Username
            <input
              type="text"
              required
              autoFocus
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          </label>
          <label>
            Password
            <input
              type="password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </label>
          <button type="submit" className="btn-primary">
            Masuk
          </button>
        </form>
      </div>
    );
  }

  const activeDb = databases.find((d) => d.id === selectedDbId) ?? null;
  const canEdit = activeDb?.access_level === "view_edit";

  return (
    <div className="app-shell">
      <header className="topbar">
        <div className="topbar-left">
          <strong>WBD Migration Console</strong>
          <label className="db-picker">
            <span>Database:</span>
            <select
              value={selectedDbId ?? ""}
              onChange={(e) => setSelectedDbId(Number(e.target.value) || null)}
            >
              {databases.map((db) => (
                <option key={db.id} value={db.id}>
                  {db.name} ({db.access_level})
                </option>
              ))}
              {databases.length === 0 && <option value="">Tidak ada DB</option>}
            </select>
          </label>
        </div>

        <div className="topbar-right">
          <span className="muted">
            {user.username}
            {user.is_admin ? " • admin" : ""}
          </span>
          <button
            type="button"
            className="btn-secondary btn-sm"
            onClick={() => void handleLogout()}
          >
            Logout
          </button>
        </div>
      </header>

      <nav className="tabs">
        <button
          type="button"
          className={tab === "migrations" ? "tab active" : "tab"}
          onClick={() => setTab("migrations")}
        >
          Migrations &amp; History
        </button>
        <button
          type="button"
          className={tab === "viewer" ? "tab active" : "tab"}
          onClick={() => setTab("viewer")}
        >
          Data Viewer
        </button>
        {user.is_admin && (
          <button
            type="button"
            className={tab === "admin" ? "tab active" : "tab"}
            onClick={() => setTab("admin")}
          >
            Admin
          </button>
        )}
      </nav>

      {toast && (
        <div className={toast.isError ? "toast toast-error" : "toast toast-ok"}>
          {toast.text}
        </div>
      )}

      <main className="main-content">
        {tab === "admin" && user.is_admin ? (
          <AdminPanel
            onNotify={notify}
            onDatabasesChanged={() => {
              void refreshDatabases().catch(() => {});
            }}
          />
        ) : activeDb ? (
          tab === "viewer" ? (
            <ViewerPanel dbId={activeDb.id} onNotify={notify} />
          ) : (
            <MigrationsPanel
              dbId={activeDb.id}
              canEdit={canEdit}
              currentUser={user}
              onNotify={notify}
            />
          )
        ) : (
          <div className="card muted">
            Belum ada database yang dapat diakses oleh akun ini.
          </div>
        )}
      </main>
    </div>
  );
}
