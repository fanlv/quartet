interface IconProps {
  className?: string;
}

export function ConfigurationIcon({
  kind,
  className = 'model-tag-leading-icon',
}: IconProps & { kind: 'model' | 'mode' | 'thought' }) {
  if (kind === 'model') {
    return (
      <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <rect x="6" y="6" width="12" height="12" rx="2" />
        <rect x="9" y="9" width="6" height="6" rx="1" />
        <path d="M9 2v2M15 2v2M9 20v2M15 20v2M2 9h2M2 15h2M20 9h2M20 15h2" />
      </svg>
    );
  }
  if (kind === 'mode') {
    return (
      <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <circle cx="12" cy="4" r="2" />
        <circle cx="5" cy="18" r="2" />
        <circle cx="19" cy="18" r="2" />
        <path d="m10.8 5.8-4.6 10M13.2 5.8l4.6 10M7 18h10" strokeDasharray="2 2" />
      </svg>
    );
  }
  return (
    <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M9.5 4A2.5 2.5 0 0 0 7 6.5v.3A3 3 0 0 0 5 12a3 3 0 0 0 2 5.2v.3A2.5 2.5 0 0 0 9.5 20M14.5 4A2.5 2.5 0 0 1 17 6.5v.3a3 3 0 0 1 2 5.2 3 3 0 0 1-2 5.2v.3a2.5 2.5 0 0 1-2.5 2.5M9.5 4v16M14.5 4v16M7 9h2.5M14.5 9H17M7 15h2.5M14.5 15H17" />
    </svg>
  );
}

export function ClockIcon({ className = 'duration-badge-icon' }: IconProps) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v5l3 2" />
    </svg>
  );
}
