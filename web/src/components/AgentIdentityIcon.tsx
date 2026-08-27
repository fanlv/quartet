import { useEffect, useMemo, useState } from 'react';
import { isImageUrl, resolveIconSrc, type IconShareInfo } from '../utils/url';
import './AgentIdentityIcon.css';

interface AgentIdentityIconProps {
  iconUrl?: string;
  displayName?: string;
  shareInfo?: IconShareInfo | null;
  className?: string;
}

export function AgentIdentityIcon({ iconUrl, displayName, shareInfo, className = '' }: AgentIdentityIconProps) {
  const src = useMemo(() => resolveIconSrc(iconUrl, shareInfo), [iconUrl, shareInfo]);
  const [failedSrc, setFailedSrc] = useState('');
  useEffect(() => setFailedSrc(''), [src]);

  const fallback = Array.from(displayName?.trim() || '')[0]?.toUpperCase() || '✦';
  const classes = `agent-identity-icon ${className}`.trim();

  if (iconUrl && !isImageUrl(iconUrl)) {
    return <span className={`${classes} agent-identity-icon-emoji`} aria-hidden>{iconUrl}</span>;
  }
  if (src && failedSrc !== src) {
    return (
      <img
        src={src}
        alt=""
        className={classes}
        referrerPolicy="no-referrer"
        onError={() => setFailedSrc(src)}
      />
    );
  }
  return <span className={`${classes} agent-identity-icon-fallback`} aria-hidden>{fallback}</span>;
}
