import { useState } from 'react';
import { setAuthPrincipal, useAuthPrincipal } from '../../auth';

async function responseError(response: Response): Promise<string> {
  const raw = await response.text();
  try {
    const body = JSON.parse(raw) as { msg?: string; error?: string };
    return body.msg || body.error || raw;
  } catch {
    return raw;
  }
}

export function AccountSettings() {
  const principal = useAuthPrincipal();
  const [displayName, setDisplayName] = useState(principal?.user.displayName ?? '');
  const [currentPassword, setCurrentPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);

  const saveProfile = async () => {
    setBusy(true);
    setMessage('');
    try {
      const response = await fetch('/api/v1/auth/me', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ displayName }),
      });
      if (!response.ok) throw new Error(await responseError(response));
      const body = await response.json() as { user: NonNullable<typeof principal>['user'] };
      if (principal) setAuthPrincipal({ ...principal, user: body.user });
      setMessage('个人资料已保存。');
    } catch (error) {
      setMessage(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(false);
    }
  };

  const changePassword = async () => {
    setBusy(true);
    setMessage('');
    try {
      const response = await fetch('/api/v1/auth/password', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ currentPassword, newPassword }),
      });
      if (!response.ok) throw new Error(await responseError(response));
      setAuthPrincipal(await response.json());
      setCurrentPassword('');
      setNewPassword('');
      setMessage('密码已修改，其他登录会话已退出。');
    } catch (error) {
      setMessage(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(false);
    }
  };

  const logout = async () => {
    setBusy(true);
    try {
      await fetch('/api/v1/auth/logout', { method: 'POST' });
    } finally {
      setAuthPrincipal(null);
      window.location.reload();
    }
  };

  return (
    <div className="account-settings">
      <section className="settings-section">
        <h3 className="settings-section-title">当前账号</h3>
        <p className="settings-section-desc">登录名：{principal?.user.username ?? '—'} · 角色：{principal?.user.roleIds.join(', ') || '—'}</p>
        <div className="settings-form-group">
          <label className="settings-label">显示名称</label>
          <input className="settings-input" value={displayName} onChange={(event) => setDisplayName(event.target.value)} />
        </div>
        <button className="settings-btn settings-btn-primary" disabled={busy} onClick={() => void saveProfile()}>保存个人资料</button>
      </section>
      <section className="settings-section">
        <h3 className="settings-section-title">修改密码</h3>
        <div className="settings-form-group"><label className="settings-label">当前密码</label><input type="password" className="settings-input" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} /></div>
        <div className="settings-form-group"><label className="settings-label">新密码</label><input type="password" className="settings-input" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} /></div>
        <div className="settings-btn-group"><button className="settings-btn settings-btn-primary" disabled={busy || !currentPassword || !newPassword} onClick={() => void changePassword()}>修改密码</button><button className="settings-btn settings-btn-danger" disabled={busy} onClick={() => void logout()}>退出登录</button></div>
        {message && <pre className="auth-admin-message">{message}</pre>}
      </section>
    </div>
  );
}
