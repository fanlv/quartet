export interface AuthUser {
  id: string;
  username: string;
  displayName: string;
  roleIds: string[];
  status: string;
  mustChangePassword: boolean;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface AuthPrincipal {
  user: AuthUser;
  permissions: string[];
  csrfToken: string;
}

export const AUTH_CHANGED_EVENT = 'quartet:auth-changed';
export const AUTH_EXPIRED_EVENT = 'quartet:auth-expired';

let currentPrincipal: AuthPrincipal | null = null;

export function setAuthPrincipal(principal: AuthPrincipal | null) {
  currentPrincipal = principal;
  window.dispatchEvent(new CustomEvent(AUTH_CHANGED_EVENT, { detail: principal }));
}

export function getAuthPrincipal(): AuthPrincipal | null {
  return currentPrincipal;
}

export function getCSRFToken(): string {
  return currentPrincipal?.csrfToken ?? '';
}

export function hasPermission(permission: string): boolean {
  return currentPrincipal?.permissions.includes(permission) ?? false;
}
import { useEffect, useState } from 'react';


export function useAuthPrincipal(): AuthPrincipal | null {
  const [principal, setPrincipal] = useState<AuthPrincipal | null>(() => currentPrincipal);
  useEffect(() => {
    const update = (event: Event) => setPrincipal((event as CustomEvent<AuthPrincipal | null>).detail);
    window.addEventListener(AUTH_CHANGED_EVENT, update);
    return () => window.removeEventListener(AUTH_CHANGED_EVENT, update);
  }, []);
  return principal;
}

