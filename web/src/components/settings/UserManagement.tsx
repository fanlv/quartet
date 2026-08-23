import { useCallback, useEffect, useMemo, useState } from 'react';
import type { AuthUser } from '../../auth';
import { useAuthPrincipal } from '../../auth';

interface Role { id: string; name: string; builtIn: boolean }
async function apiError(response: Response) { const raw = await response.text(); try { const body = JSON.parse(raw) as { msg?: string; error?: string }; return body.msg || body.error || raw; } catch { return raw; } }

export function UserManagement() {
  const principal = useAuthPrincipal();
  const canManage = principal?.permissions.includes('users.manage') ?? false;
  const canReadRoles = principal?.permissions.includes('roles.read') ?? false;
  const [users, setUsers] = useState<AuthUser[]>([]);
  const [roles, setRoles] = useState<Role[]>([]);
  const [selectedID, setSelectedID] = useState('');
  const [username, setUsername] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [password, setPassword] = useState('');
  const [roleIDs, setRoleIDs] = useState<string[]>(['member']);
  const [message, setMessage] = useState('');
  const selected = useMemo(() => users.find((user) => user.id === selectedID), [selectedID, users]);
  const selectedCanChange = canManage && selected?.status !== 'deleted';

  const load = useCallback(async () => {
    setMessage('');
    try {
      const [usersResponse, rolesResponse] = await Promise.all([fetch('/api/v1/users'), canReadRoles ? fetch('/api/v1/roles') : null]);
      if (!usersResponse.ok) throw new Error(await apiError(usersResponse));
      setUsers(((await usersResponse.json()) as { users: AuthUser[] }).users);
      if (rolesResponse) {
        if (!rolesResponse.ok) throw new Error(await apiError(rolesResponse));
        setRoles(((await rolesResponse.json()) as { roles: Role[] }).roles);
      }
    } catch (error) { setMessage(String(error)); }
  }, [canReadRoles]);
  useEffect(() => { void load(); }, [load]);
  useEffect(() => { if (selected) { setUsername(selected.username); setDisplayName(selected.displayName); setRoleIDs(selected.roleIds); } }, [selected]);

  const rolePicker = <div className="auth-admin-checks">{roles.map((role) => <label key={role.id}><input type="checkbox" data-role-id={role.id} disabled={selected ? !selectedCanChange : !canManage} checked={roleIDs.includes(role.id)} onChange={(event) => setRoleIDs((current) => event.target.checked ? [...current, role.id] : current.filter((id) => id !== role.id))} /> {role.name}</label>)}</div>;
  const create = async () => {
    const response = await fetch('/api/v1/users', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, displayName, password, roleIds: roleIDs }) });
    if (!response.ok) { setMessage(await apiError(response)); return; }
    setUsername(''); setDisplayName(''); setPassword(''); setRoleIDs(['member']); await load();
  };
  const update = async (status?: string) => {
    if (!selected) return;
    const response = await fetch(`/api/v1/users/${selected.id}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ version: selected.version, username, displayName, roleIds: roleIDs, ...(status ? { status } : {}) }) });
    if (!response.ok) { setMessage(await apiError(response)); return; }
    await load();
  };
  const remove = async () => { if (!selected || !window.confirm(`删除用户 ${selected.username}？`)) return; const response = await fetch(`/api/v1/users/${selected.id}`, { method: 'DELETE', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ version: selected.version }) }); if (!response.ok) { setMessage(await apiError(response)); return; } setSelectedID(''); await load(); };
  const resetPassword = async () => { if (!selected || !password) return; const response = await fetch(`/api/v1/users/${selected.id}/reset-password`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ version: selected.version, password }) }); if (!response.ok) { setMessage(await apiError(response)); return; } setPassword(''); setMessage('密码已重置，用户下次登录必须修改密码。'); await load(); };

  return <div className="auth-admin">
    <section className="settings-section"><h3 className="settings-section-title">用户管理</h3><p className="settings-section-desc">共享实例中的所有用户看到同一套业务数据，角色只控制功能权限。</p>{message && <pre className="auth-admin-message">{message}</pre>}<div className="auth-admin-list">{users.map((user) => <button key={user.id} className={selectedID === user.id ? 'active' : ''} onClick={() => setSelectedID(user.id)}><strong>{user.displayName}</strong><span>{user.username} · {user.status} · {user.roleIds.join(', ')}</span></button>)}</div></section>
    <section className="settings-section"><h3 className="settings-section-title">{selected ? `编辑 ${selected.username}` : '创建用户'}</h3><div className="settings-form-group"><label className="settings-label">用户名</label><input className="settings-input" data-testid="user-username-input" disabled={selected ? !selectedCanChange : !canManage} value={username} onChange={(event) => setUsername(event.target.value)} /></div><div className="settings-form-group"><label className="settings-label">显示名称</label><input className="settings-input" data-testid="user-display-name-input" disabled={selected ? !selectedCanChange : !canManage} value={displayName} onChange={(event) => setDisplayName(event.target.value)} /></div><div className="settings-form-group"><label className="settings-label">{selected ? '新临时密码（仅重置时使用）' : '临时密码'}</label><input type="password" className="settings-input" data-testid="user-password-input" disabled={selected ? !selectedCanChange : !canManage} value={password} onChange={(event) => setPassword(event.target.value)} /></div><div className="settings-form-group"><label className="settings-label">角色</label>{rolePicker}</div>{canManage && <div className="settings-btn-group">{selected ? <>{selectedCanChange && <><button className="settings-btn settings-btn-primary" onClick={() => void update()}>保存</button><button className="settings-btn settings-btn-secondary" onClick={() => void update(selected.status === 'active' ? 'disabled' : 'active')}>{selected.status === 'active' ? '停用' : '恢复'}</button><button className="settings-btn settings-btn-secondary" disabled={!password} onClick={() => void resetPassword()}>重置密码</button><button className="settings-btn settings-btn-danger" onClick={() => void remove()}>删除</button></>}<button className="settings-btn settings-btn-secondary" onClick={() => { setSelectedID(''); setUsername(''); setDisplayName(''); setPassword(''); setRoleIDs(['member']); }}>新建用户</button></> : <button className="settings-btn settings-btn-primary" data-testid="user-create-button" disabled={!username || !password || roleIDs.length === 0} onClick={() => void create()}>创建用户</button>}</div>}</section>
  </div>;
}
