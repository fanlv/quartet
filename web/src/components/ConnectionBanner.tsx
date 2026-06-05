import { useEffect, useState } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { useConnectionStatus } from '../contexts/ConnectionStatus';
import './ConnectionBanner.css';

export function ConnectionBanner() {
  const { t } = useTranslation();
  const { connected } = useConnectionStatus();
  const [showRecovered, setShowRecovered] = useState(false);
  const [hasBeenDisconnected, setHasBeenDisconnected] = useState(false);
  const [prevConnected, setPrevConnected] = useState(connected);

  // Detect the connected-edge during render (not in an effect) so we can
  // flip the "recovered" banner without causing a cascading re-render.
  if (prevConnected !== connected) {
    setPrevConnected(connected);
    if (!connected) {
      setHasBeenDisconnected(true);
      setShowRecovered(false);
    } else if (hasBeenDisconnected) {
      setShowRecovered(true);
    }
  }

  useEffect(() => {
    if (!showRecovered) return;
    const timer = setTimeout(() => {
      setShowRecovered(false);
      setHasBeenDisconnected(false);
    }, 3000);
    return () => clearTimeout(timer);
  }, [showRecovered]);

  if (connected && !showRecovered) return null;

  return createPortal(
    <div
      className={`connection-banner ${connected ? 'recovered' : 'disconnected'}`}
      data-testid="connection-banner"
      data-connection-state={connected ? 'recovered' : 'disconnected'}
    >
      {connected ? (
        <span className="connection-banner-text" data-testid="connection-banner-text">
          <svg className="connection-banner-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
            <path d="M20 6L9 17l-5-5" />
          </svg>
          {t('connection.restored')}
        </span>
      ) : (
        <span className="connection-banner-text" data-testid="connection-banner-text">
          <svg className="connection-banner-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
            <circle cx="12" cy="12" r="10" />
            <line x1="12" y1="8" x2="12" y2="12" />
            <line x1="12" y1="16" x2="12.01" y2="16" />
          </svg>
          {t('connection.disconnected')}
        </span>
      )}
    </div>,
    document.body
  );
}
