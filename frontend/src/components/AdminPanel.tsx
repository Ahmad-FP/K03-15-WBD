import React, { useEffect, useState } from "react";
import { apiRequest } from "../api";
import { Group, GroupMember, User } from "../types";

interface Props {
  onNotify: (msg: string, isError?: boolean) => void;
}

export const AdminPanel: React.FC<Props> = ({ onNotify }) => {
  const [users, setUsers] = useState<User[]>([]);
  const [groups, setGroups] = useState<Group[]>([]);
  const [selectedGroupId, setSelectedGroupId] = useState<number | null>(null);
  const [members, setMembers] = useState<GroupMember[]>([]);

  // Form group (create & edit leader)
  const [editingGroupId, setEditingGroupId] = useState<number | null>(null);
  const [groupName, setGroupName] = useState("");
  const [groupDesc, setGroupDesc] = useState("");
  const [leaderId, setLeaderId] = useState<string>("");

  // Form user baru
  const [newUsername, setNewUsername] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [newIsAdmin, setNewIsAdmin] = useState(false);

  // Add member
  const [memberUserId, setMemberUserId] = useState<string>("");

  const loadAll = async () => {
    try {
      const [uList, gList] = await Promise.all([
        apiRequest<User[]>("/api/users"),
        apiRequest<Group[]>("/api/groups"),
      ]);
      setUsers(uList);
      setGroups(gList);
      if (gList.length > 0 && selectedGroupId === null) {
        setSelectedGroupId(gList[0].id);
      }
    } catch (err) {
      onNotify(err instanceof Error ? err.message : "Gagal memuat data admin", true);
    }
  };

  const loadMembers = async (gid: number) => {
    try {
      const list = await apiRequest<GroupMember[]>(`/api/groups/${gid}/members`);
      setMembers(list);
    } catch (err) {
      onNotify(err instanceof Error ? err.message : "Gagal memuat anggota group", true);
    }
  };

  useEffect(() => {
    void loadAll();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (selectedGroupId !== null) {
      void loadMembers(selectedGroupId);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedGroupId]);

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
    setLeaderId(g.leader_id !== null && g.leader_id !== undefined ? String(g.leader_id) : "");
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
    } catch (err) {
      onNotify(err instanceof Error ? err.message : "Gagal menyimpan group", true);
    }
  };

  const handleDeleteGroup = async (g: Group) => {
    try {
      await apiRequest<void>(`/api/groups/${g.id}`, { method: "DELETE" });
      onNotify(`Group "${g.name}" dihapus.`);
      if (selectedGroupId === g.id) {
        setSelectedGroupId(null);
        setMembers([]);
      }
      await loadAll();
    } catch (err) {
      onNotify(err instanceof Error ? err.message : "Gagal menghapus group", true);
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
    } catch (err) {
      onNotify(err instanceof Error ? err.message : "Gagal menambah anggota", true);
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
    } catch (err) {
      onNotify(err instanceof Error ? err.message : "Gagal menghapus anggota", true);
    }
  };

  return (
    <div className="stack-lg">
      {/* Groups & Group Leader Picker */}
      <section className="card">
        <div className="card-header">
          <h3>Groups &amp; Group Leader</h3>
          {editingGroupId !== null && (
            <button type="button" className="btn-secondary" onClick={resetGroupForm}>
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
                  className={selectedGroupId === g.id ? "row-active" : undefined}
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

        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>ID</th>
                <th>Username</th>
                <th>Role</th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.id}>
                  <td>#{u.id}</td>
                  <td>{u.username}</td>
                  <td>{u.is_admin ? "Admin" : "User"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
};