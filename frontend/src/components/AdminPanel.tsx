import React, { useEffect, useState } from "react";
import { apiRequest } from "../api";
import {
  DatabaseAccessGrant,
  Group,
  GroupMember,
  ManagedDatabase,
  User,
} from "../types";

interface Props {
  onNotify: (msg: string, isError?: boolean) => void;
  onDatabasesChanged?: () => void;
}

export const AdminPanel: React.FC<Props> = ({
  onNotify,
  onDatabasesChanged,
}) => {
  const [users, setUsers] = useState<User[]>([]);
  const [groups, setGroups] = useState<Group[]>([]);
  const [selectedGroupId, setSelectedGroupId] = useState<number | null>(null);
  const [members, setMembers] = useState<GroupMember[]>([]);

  // Managed databases & access control
  const [managedDbs, setManagedDbs] = useState<ManagedDatabase[]>([]);
  const [selectedDbId, setSelectedDbId] = useState<number | null>(null);
  const [accessGrants, setAccessGrants] = useState<DatabaseAccessGrant[]>([]);
  const [editingDbId, setEditingDbId] = useState<number | null>(null);
  const [dbName, setDbName] = useState("");
  const [dbHostLabel, setDbHostLabel] = useState("");
  const [dbUrl, setDbUrl] = useState("");
  const [accessGroupId, setAccessGroupId] = useState<string>("");
  const [accessLevel, setAccessLevel] = useState<"view_only" | "view_edit">(
    "view_only"
  );

  // Form group (create & edit leader)
  const [editingGroupId, setEditingGroupId] = useState<number | null>(null);
  const [groupName, setGroupName] = useState("");
  const [groupDesc, setGroupDesc] = useState("");
  const [leaderId, setLeaderId] = useState<string>("");

  // Form user baru & reset password
  const [newUsername, setNewUsername] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [newIsAdmin, setNewIsAdmin] = useState(false);
  const [resetUserId, setResetUserId] = useState<number | null>(null);
  const [resetPassword, setResetPassword] = useState("");

  // Add member
  const [memberUserId, setMemberUserId] = useState<string>("");

  const loadAll = async () => {
    try {
      const [uList, gList, dList] = await Promise.all([
        apiRequest<User[]>("/api/users"),
        apiRequest<Group[]>("/api/groups"),
        apiRequest<ManagedDatabase[]>("/api/db"),
      ]);
      setUsers(uList);
      setGroups(gList);
      setManagedDbs(dList);
      setSelectedGroupId((prev) =>
        prev !== null && gList.some((g) => g.id === prev)
          ? prev
          : (gList[0]?.id ?? null)
      );
      setSelectedDbId((prev) =>
        prev !== null && dList.some((d) => d.id === prev)
          ? prev
          : (dList[0]?.id ?? null)
      );
    } catch (err) {
      onNotify(
        err instanceof Error ? err.message : "Gagal memuat data admin",
        true
      );
    }
  };

  const loadMembers = async (gid: number) => {
    try {
      const list = await apiRequest<GroupMember[]>(
        `/api/groups/${gid}/members`
      );
      setMembers(list);
    } catch (err) {
      onNotify(
        err instanceof Error ? err.message : "Gagal memuat anggota group",
        true
      );
    }
  };

  const loadAccess = async (did: number) => {
    try {
      const list = await apiRequest<DatabaseAccessGrant[]>(
        `/api/db/${did}/access`
      );
      setAccessGrants(list);
    } catch (err) {
      onNotify(
        err instanceof Error ? err.message : "Gagal memuat akses database",
        true
      );
    }
  };

  useEffect(() => {
    void loadAll();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (selectedGroupId !== null) {
      void loadMembers(selectedGroupId);
    } else {
      setMembers([]);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedGroupId]);

  useEffect(() => {
    if (selectedDbId !== null) {
      void loadAccess(selectedDbId);
    } else {
      setAccessGrants([]);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedDbId]);

  const resetDbForm = () => {
    setEditingDbId(null);
    setDbName("");
    setDbHostLabel("");
    setDbUrl("");
  };

  const startEditDb = (d: ManagedDatabase) => {
    setEditingDbId(d.id);
    setDbName(d.name);
    setDbHostLabel(d.host_label || "");
    setDbUrl("");
  };

  const handleSaveDb = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      const payload = {
        name: dbName.trim(),
        host_label: dbHostLabel.trim(),
        connection_url: dbUrl.trim(),
      };
      if (editingDbId !== null) {
        await apiRequest<ManagedDatabase>(`/api/db/${editingDbId}`, {
          method: "PUT",
          body: JSON.stringify(payload),
        });
        onNotify("Database diperbarui.");
      } else {
        const created = await apiRequest<ManagedDatabase>("/api/db", {
          method: "POST",
          body: JSON.stringify(payload),
        });
        setSelectedDbId(created.id);
        onNotify("Database baru didaftarkan.");
      }
      resetDbForm();
      await loadAll();
      onDatabasesChanged?.();
    } catch (err) {
      onNotify(
        err instanceof Error ? err.message : "Gagal menyimpan database",
        true
      );
    }
  };

  const handleDeleteDb = async (d: ManagedDatabase) => {
    try {
      await apiRequest<void>(`/api/db/${d.id}`, { method: "DELETE" });
      onNotify(`Database "${d.name}" dihapus.`);
      if (editingDbId === d.id) resetDbForm();
      await loadAll();
      onDatabasesChanged?.();
    } catch (err) {
      onNotify(
        err instanceof Error ? err.message : "Gagal menghapus database",
        true
      );
    }
  };

  const handleTestDb = async (d: ManagedDatabase) => {
    try {
      const res = await apiRequest<{ message?: string }>(
        `/api/db/${d.id}/test`,
        { method: "POST" }
      );
      onNotify(`${d.name}: ${res?.message || "koneksi berhasil"}`);
    } catch (err) {
      onNotify(
        err instanceof Error ? `${d.name}: ${err.message}` : "Test koneksi gagal",
        true
      );
    }
  };

  const handleSetAccess = async (e: React.FormEvent) => {
    e.preventDefault();
    if (selectedDbId === null || !accessGroupId) return;
    try {
      await apiRequest<void>(
        `/api/db/${selectedDbId}/access/${Number(accessGroupId)}`,
        {
          method: "PUT",
          body: JSON.stringify({ access_level: accessLevel }),
        }
      );
      setAccessGroupId("");
      onNotify("Akses database disimpan.");
      await loadAccess(selectedDbId);
      onDatabasesChanged?.();
    } catch (err) {
      onNotify(
        err instanceof Error ? err.message : "Gagal mengatur akses database",
        true
      );
    }
  };

  const handleRevokeAccess = async (groupId: number) => {
    if (selectedDbId === null) return;
    try {
      await apiRequest<void>(`/api/db/${selectedDbId}/access/${groupId}`, {
        method: "DELETE",
      });
      onNotify("Akses database dicabut.");
      await loadAccess(selectedDbId);
      onDatabasesChanged?.();
    } catch (err) {
      onNotify(
        err instanceof Error ? err.message : "Gagal mencabut akses database",
        true
      );
    }
  };

  const resetGroupForm = () => {
    setEditingGroupId(null);
    setGroupName("");
    setGroupDesc("");
    setLeaderId("");
  };

  const startEditGroup = (g: Group) => {
    setEditingGroupId(g.id);
    setGroupName(g.name);
    setGroupDesc(g.description || "");
    setLeaderId(
      g.leader_id !== null && g.leader_id !== undefined
        ? String(g.leader_id)
        : ""
    );
  };

  const handleSaveGroup = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      const payload = {
        name: groupName.trim(),
        description: groupDesc.trim(),
        leader_id: leaderId ? Number(leaderId) : null,
      };
      if (editingGroupId !== null) {
        await apiRequest<Group>(`/api/groups/${editingGroupId}`, {
          method: "PUT",
          body: JSON.stringify(payload),
        });
        onNotify("Group & Group Leader diperbarui.");
      } else {
        await apiRequest<Group>("/api/groups", {
          method: "POST",
          body: JSON.stringify(payload),
        });
        onNotify("Group baru dibuat.");
      }
      resetGroupForm();
      await loadAll();
      if (selectedGroupId !== null) {
        await loadMembers(selectedGroupId);
      }
      onDatabasesChanged?.();
    } catch (err) {
      onNotify(
        err instanceof Error ? err.message : "Gagal menyimpan group",
        true
      );
    }
  };

  const handleDeleteGroup = async (g: Group) => {
    try {
      await apiRequest<void>(`/api/groups/${g.id}`, { method: "DELETE" });
      onNotify(`Group "${g.name}" dihapus.`);
      if (editingGroupId === g.id) resetGroupForm();
      await loadAll();
      if (selectedDbId !== null) await loadAccess(selectedDbId);
      onDatabasesChanged?.();
    } catch (err) {
      onNotify(
        err instanceof Error ? err.message : "Gagal menghapus group",
        true
      );
    }
  };

  const handleCreateUser = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      await apiRequest<User>("/api/users", {
        method: "POST",
        body: JSON.stringify({
          username: newUsername.trim(),
          password: newPassword,
          is_admin: newIsAdmin,
        }),
      });
      setNewUsername("");
      setNewPassword("");
      setNewIsAdmin(false);
      onNotify("User baru ditambahkan.");
      await loadAll();
    } catch (err) {
      onNotify(err instanceof Error ? err.message : "Gagal membuat user", true);
    }
  };

  const handleToggleAdmin = async (u: User) => {
    try {
      await apiRequest<void>(`/api/users/${u.id}/admin`, {
        method: "PUT",
        body: JSON.stringify({ is_admin: !u.is_admin }),
      });
      onNotify(
        `Role "${u.username}" diubah ke ${!u.is_admin ? "Admin" : "User"}.`
      );
      await loadAll();
    } catch (err) {
      onNotify(
        err instanceof Error ? err.message : "Gagal mengubah status admin",
        true
      );
    }
  };

  const handleResetPassword = async (e: React.FormEvent) => {
    e.preventDefault();
    if (resetUserId === null || !resetPassword) return;
    try {
      await apiRequest<void>(`/api/users/${resetUserId}/password`, {
        method: "PUT",
        body: JSON.stringify({ password: resetPassword }),
      });
      onNotify(`Password user #${resetUserId} diperbarui.`);
      setResetUserId(null);
      setResetPassword("");
    } catch (err) {
      onNotify(
        err instanceof Error ? err.message : "Gagal mengubah password",
        true
      );
    }
  };

  const handleDeleteUser = async (u: User) => {
    try {
      await apiRequest<void>(`/api/users/${u.id}`, { method: "DELETE" });
      onNotify(`User "${u.username}" dihapus.`);
      if (resetUserId === u.id) {
        setResetUserId(null);
        setResetPassword("");
      }
      await loadAll();
      if (selectedGroupId !== null) await loadMembers(selectedGroupId);
      onDatabasesChanged?.();
    } catch (err) {
      onNotify(
        err instanceof Error ? err.message : "Gagal menghapus user",
        true
      );
    }
  };

  const handleAddMember = async (e: React.FormEvent) => {
    e.preventDefault();
    if (selectedGroupId === null || !memberUserId) return;
    try {
      await apiRequest<void>(`/api/groups/${selectedGroupId}/members`, {
        method: "POST",
        body: JSON.stringify({ user_id: Number(memberUserId) }),
      });
      setMemberUserId("");
      onNotify("Anggota ditambahkan ke group.");
      await loadMembers(selectedGroupId);
      onDatabasesChanged?.();
    } catch (err) {
      onNotify(
        err instanceof Error ? err.message : "Gagal menambah anggota",
        true
      );
    }
  };

  const handleRemoveMember = async (userId: number) => {
    if (selectedGroupId === null) return;
    try {
      await apiRequest<void>(
        `/api/groups/${selectedGroupId}/members/${userId}`,
        { method: "DELETE" }
      );
      onNotify("Anggota dihapus dari group.");
      await loadMembers(selectedGroupId);
      await loadAll();
      onDatabasesChanged?.();
    } catch (err) {
      onNotify(
        err instanceof Error ? err.message : "Gagal menghapus anggota",
        true
      );
    }
  };

  return (
    <div className="stack-lg">
      {/* Managed Databases & Group Access Control */}
      <section className="card">
        <div className="card-header">
          <h3>Databases &amp; Access Control</h3>
          {editingDbId !== null && (
            <button
              type="button"
              className="btn-secondary"
              onClick={resetDbForm}
            >
              Batal Edit
            </button>
          )}
        </div>

        <form onSubmit={handleSaveDb} className="form-grid">
          <div className="form-row-3">
            <label>
              Nama Database
              <input
                type="text"
                required
                value={dbName}
                onChange={(e) => setDbName(e.target.value)}
                placeholder="app-production"
              />
            </label>
            <label>
              Host Label
              <input
                type="text"
                value={dbHostLabel}
                onChange={(e) => setDbHostLabel(e.target.value)}
                placeholder="db-primary:5432"
              />
            </label>
            <label>
              Connection URL (PostgreSQL)
              <input
                type="password"
                required
                value={dbUrl}
                onChange={(e) => setDbUrl(e.target.value)}
                placeholder="postgres://user:pass@host:5432/dbname?sslmode=disable"
              />
            </label>
          </div>
          <div className="actions-row">
            <button type="submit" className="btn-primary">
              {editingDbId !== null ? "Update Database" : "Daftarkan Database"}
            </button>
          </div>
        </form>

        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>ID</th>
                <th>Nama</th>
                <th>Host Label</th>
                <th>Aksi</th>
              </tr>
            </thead>
            <tbody>
              {managedDbs.map((d) => (
                <tr
                  key={d.id}
                  className={selectedDbId === d.id ? "row-active" : undefined}
                >
                  <td>#{d.id}</td>
                  <td>
                    <button
                      type="button"
                      className="link-btn"
                      onClick={() => setSelectedDbId(d.id)}
                    >
                      {d.name}
                    </button>
                  </td>
                  <td>{d.host_label || "-"}</td>
                  <td className="cell-actions">
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      onClick={() => void handleTestDb(d)}
                    >
                      Test
                    </button>
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      onClick={() => startEditDb(d)}
                    >
                      Edit
                    </button>
                    <button
                      type="button"
                      className="btn-danger btn-sm"
                      onClick={() => void handleDeleteDb(d)}
                    >
                      Hapus
                    </button>
                  </td>
                </tr>
              ))}
              {managedDbs.length === 0 && (
                <tr>
                  <td colSpan={4} className="muted">
                    Belum ada database terdaftar.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>

        {selectedDbId !== null && (
          <div className="subsection">
            <h4>Akses Group untuk Database #{selectedDbId}</h4>
            <form onSubmit={handleSetAccess} className="actions-row">
              <select
                value={accessGroupId}
                onChange={(e) => setAccessGroupId(e.target.value)}
                required
              >
                <option value="">-- Pilih Group --</option>
                {groups.map((g) => (
                  <option key={g.id} value={g.id}>
                    {g.name}
                  </option>
                ))}
              </select>
              <select
                value={accessLevel}
                onChange={(e) =>
                  setAccessLevel(
                    e.target.value as "view_only" | "view_edit"
                  )
                }
              >
                <option value="view_only">view_only</option>
                <option value="view_edit">view_edit</option>
              </select>
              <button type="submit" className="btn-primary btn-sm">
                Set Akses
              </button>
            </form>

            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>Group</th>
                    <th>Access Level</th>
                    <th>Aksi</th>
                  </tr>
                </thead>
                <tbody>
                  {accessGrants.map((a) => (
                    <tr key={a.group_id}>
                      <td>
                        {a.group_name} (#{a.group_id})
                      </td>
                      <td>{a.access_level}</td>
                      <td>
                        <button
                          type="button"
                          className="btn-danger btn-sm"
                          onClick={() => void handleRevokeAccess(a.group_id)}
                        >
                          Cabut
                        </button>
                      </td>
                    </tr>
                  ))}
                  {accessGrants.length === 0 && (
                    <tr>
                      <td colSpan={3} className="muted">
                        Belum ada group yang diberi akses.
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </div>
        )}
      </section>

      {/* Groups & Group Leader Picker */}
      <section className="card">
        <div className="card-header">
          <h3>Groups &amp; Group Leader</h3>
          {editingGroupId !== null && (
            <button
              type="button"
              className="btn-secondary"
              onClick={resetGroupForm}
            >
              Batal Edit
            </button>
          )}
        </div>

        <form onSubmit={handleSaveGroup} className="form-grid">
          <div className="form-row-3">
            <label>
              Nama Group
              <input
                type="text"
                required
                value={groupName}
                onChange={(e) => setGroupName(e.target.value)}
                placeholder="platform-team"
              />
            </label>

            <label>
              Deskripsi
              <input
                type="text"
                value={groupDesc}
                onChange={(e) => setGroupDesc(e.target.value)}
                placeholder="Deskripsi tim"
              />
            </label>

            <label>
              Group Leader
              <select
                value={leaderId}
                onChange={(e) => setLeaderId(e.target.value)}
              >
                <option value="">-- Tanpa Leader --</option>
                {users.map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.username} (#{u.id})
                  </option>
                ))}
              </select>
            </label>
          </div>

          <div className="actions-row">
            <button type="submit" className="btn-primary">
              {editingGroupId !== null ? "Update Group" : "Tambah Group"}
            </button>
          </div>
        </form>

        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>ID</th>
                <th>Nama</th>
                <th>Deskripsi</th>
                <th>Group Leader</th>
                <th>Aksi</th>
              </tr>
            </thead>
            <tbody>
              {groups.map((g) => (
                <tr
                  key={g.id}
                  className={
                    selectedGroupId === g.id ? "row-active" : undefined
                  }
                >
                  <td>#{g.id}</td>
                  <td>
                    <button
                      type="button"
                      className="link-btn"
                      onClick={() => setSelectedGroupId(g.id)}
                    >
                      {g.name}
                    </button>
                  </td>
                  <td>{g.description || "-"}</td>
                  <td>
                    {g.leader_username
                      ? `${g.leader_username} (#${g.leader_id})`
                      : "-"}
                  </td>
                  <td className="cell-actions">
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      onClick={() => startEditGroup(g)}
                    >
                      Edit Leader
                    </button>
                    <button
                      type="button"
                      className="btn-danger btn-sm"
                      onClick={() => void handleDeleteGroup(g)}
                    >
                      Hapus
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        {selectedGroupId !== null && (
          <div className="subsection">
            <h4>Anggota Group #{selectedGroupId}</h4>
            <form onSubmit={handleAddMember} className="actions-row">
              <select
                value={memberUserId}
                onChange={(e) => setMemberUserId(e.target.value)}
                required
              >
                <option value="">-- Pilih User --</option>
                {users.map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.username}
                  </option>
                ))}
              </select>
              <button type="submit" className="btn-primary btn-sm">
                Tambah Member
              </button>
            </form>

            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>User ID</th>
                    <th>Username</th>
                    <th>Aksi</th>
                  </tr>
                </thead>
                <tbody>
                  {members.map((m) => (
                    <tr key={m.id}>
                      <td>#{m.id}</td>
                      <td>{m.username}</td>
                      <td>
                        <button
                          type="button"
                          className="btn-danger btn-sm"
                          onClick={() => void handleRemoveMember(m.id)}
                        >
                          Keluarkan
                        </button>
                      </td>
                    </tr>
                  ))}
                  {members.length === 0 && (
                    <tr>
                      <td colSpan={3} className="muted">
                        Belum ada anggota.
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </div>
        )}
      </section>

      {/* Users Management */}
      <section className="card">
        <h3>Users</h3>
        <form onSubmit={handleCreateUser} className="form-row-3">
          <label>
            Username
            <input
              type="text"
              required
              value={newUsername}
              onChange={(e) => setNewUsername(e.target.value)}
            />
          </label>
          <label>
            Password
            <input
              type="password"
              required
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
            />
          </label>
          <label className="checkbox-inline align-end">
            <input
              type="checkbox"
              checked={newIsAdmin}
              onChange={(e) => setNewIsAdmin(e.target.checked)}
            />
            <span>Platform Admin</span>
          </label>
          <div className="actions-row">
            <button type="submit" className="btn-primary">
              Buat User
            </button>
          </div>
        </form>

        {resetUserId !== null && (
          <form
            onSubmit={handleResetPassword}
            className="actions-row subsection"
          >
            <span>Password baru untuk User #{resetUserId}:</span>
            <input
              type="password"
              required
              value={resetPassword}
              onChange={(e) => setResetPassword(e.target.value)}
              placeholder="Password baru"
            />
            <button type="submit" className="btn-primary btn-sm">
              Simpan
            </button>
            <button
              type="button"
              className="btn-secondary btn-sm"
              onClick={() => {
                setResetUserId(null);
                setResetPassword("");
              }}
            >
              Batal
            </button>
          </form>
        )}

        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>ID</th>
                <th>Username</th>
                <th>Role</th>
                <th>Aksi</th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.id}>
                  <td>#{u.id}</td>
                  <td>{u.username}</td>
                  <td>{u.is_admin ? "Admin" : "User"}</td>
                  <td className="cell-actions">
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      onClick={() => void handleToggleAdmin(u)}
                    >
                      {u.is_admin ? "Cabut Admin" : "Jadikan Admin"}
                    </button>
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      onClick={() => {
                        setResetUserId(u.id);
                        setResetPassword("");
                      }}
                    >
                      Reset Password
                    </button>
                    <button
                      type="button"
                      className="btn-danger btn-sm"
                      onClick={() => void handleDeleteUser(u)}
                    >
                      Hapus
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
};
