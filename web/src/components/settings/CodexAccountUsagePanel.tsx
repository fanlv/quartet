import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import type { CodexAccountUsageResponse, CodexResetCard } from '../../api/codexAccounts';
import type { CodexUsage } from '../../utils/agentUsage';

const usageKey = 'settings.codexAccounts';

function windowLabel(seconds: number, t: TFunction) {
  const units = [[86400, 'Days'], [3600, 'Hours'], [60, 'Minutes'], [1, 'Seconds']] as const;
  const unit = units.find(([size]) => seconds > 0 && seconds % size === 0);
  return unit ? t(`${usageKey}.window${unit[1]}`, { count: seconds / unit[0] }) : t(`${usageKey}.window`);
}

function QuotaMeter({ label, usedPercent, value }: { label: string; usedPercent?: number; value?: string }) {
  const { t, i18n } = useTranslation();
  const remaining = usedPercent == null ? undefined : Math.max(0, Math.min(100, 100 - usedPercent));
  const remainingLabel = remaining == null ? undefined : t(`${usageKey}.remainingPercent`, {
    percent: new Intl.NumberFormat(i18n.resolvedLanguage, { style: 'percent', maximumFractionDigits: 1 }).format(remaining / 100),
  });

  return <div className="codex-usage-window">
    <div className="codex-quota-caption"><span>{label}</span><strong>{value ?? remainingLabel}</strong></div>
    {remaining != null && <meter min={0} max={100} value={remaining} aria-label={label} aria-valuetext={remainingLabel}
      className={remaining <= 20 ? 'low-quota' : undefined} />}
  </div>;
}

function spendAmount(value: number, unit: string | undefined, locale: string | undefined, t: TFunction) {
  const kind = unit?.toLowerCase();
  const amount = new Intl.NumberFormat(locale, kind === 'usd'
    ? { style: 'currency', currency: 'USD', currencyDisplay: 'narrowSymbol' }
    : { maximumFractionDigits: 2 }).format(value);
  const suffix = kind === 'credit' || kind === 'credits' ? t('agentUsage.codexCredits') : unit;
  return kind === 'usd' || !suffix ? amount : `${amount} ${suffix}`;
}

export function CodexAccountQuota({ usage }: { usage: CodexUsage }) {
  const { t, i18n } = useTranslation();
  const spend = usage.spend_limit;
  if (!usage.primary_window && !usage.secondary_window && !spend) {
    return <span className="codex-account-detail">{t('agentUsage.codexUsageUnavailable')}</span>;
  }

  let spendLabel: string | undefined;
  if (spend) {
    const amount = (value: number) => spendAmount(value, spend.unit, i18n.resolvedLanguage, t);
    if (spend.reached) spendLabel = t('agentUsage.codexSpendReached');
    else if (spend.remaining != null) spendLabel = t('agentUsage.codexSpendRemaining', { amount: amount(spend.remaining) });
    else if (spend.used_percent == null && spend.limit != null) spendLabel = t('agentUsage.codexSpendCap', { amount: amount(spend.limit) });
    else if (spend.used_percent == null && spend.used != null) spendLabel = t('agentUsage.codexSpendUsed', { amount: amount(spend.used) });
  }

  return <div className="codex-usage-grid">
    {[usage.primary_window, usage.secondary_window].map((window, index) => window && (
      <QuotaMeter key={index} label={windowLabel(window.limit_window_seconds, t)} usedPercent={window.used_percent} />
    ))}
    {spend && <QuotaMeter label={t(`${usageKey}.spend`)} usedPercent={spend.used_percent} value={spendLabel} />}
  </div>;
}

interface CodexAccountUsagePanelProps {
  snapshot: CodexAccountUsageResponse;
  formatTime: (unix: number) => string;
  disabled: boolean;
  onRedeem?: (card: CodexResetCard) => void;
}

export function CodexAccountUsagePanel({ snapshot, formatTime, disabled, onRedeem }: CodexAccountUsagePanelProps) {
  const { t, i18n } = useTranslation();
  const usage = snapshot.usage;
  const spend = usage.spend_limit;
  const cards = snapshot.reset_cards ?? [];
  const resetTime = (unix: number) => t('agentUsage.resetAt', { time: formatTime(unix) });
  const amount = (value: number) => spendAmount(value, spend?.unit, i18n.resolvedLanguage, t);
  const spendDetails = [
    spend?.used != null && t('agentUsage.codexSpendUsed', { amount: amount(spend.used) }),
    spend?.limit != null && t('agentUsage.codexSpendCap', { amount: amount(spend.limit) }),
    spend?.remaining != null && t('agentUsage.codexSpendRemaining', { amount: amount(spend.remaining) }),
    !!spend?.reset_at && resetTime(spend.reset_at),
  ].filter(Boolean);

  return <>
    {[usage.primary_window, usage.secondary_window].map((window, index) => window && window.reset_at > 0 && (
      <p key={index}>{windowLabel(window.limit_window_seconds, t)} · {resetTime(window.reset_at)}</p>
    ))}
    {spendDetails.length > 0 && <p>{t('agentUsage.codexSpendLimit')} · {spendDetails.join(' · ')}</p>}
    {onRedeem && <div className="codex-reset-cards">
      <div className="codex-account-row">
        <strong>{t(`${usageKey}.resetCards`)}</strong>
        <span className="codex-account-detail">{t(`${usageKey}.resetCardCount`, { count: usage.reset_credits })}</span>
      </div>
      {cards.map((card) => {
        const expiry = card.expires_at
          ? t('agentUsage.creditExpiry', { time: formatTime(card.expires_at) })
          : t(`${usageKey}.expiryUnknown`);
        return <div key={card.id} className="codex-account-row codex-reset-card">
          <span>{expiry}</span>
          <button type="button" disabled={disabled} aria-label={t(`${usageKey}.useResetWithExpiry`, { expiry })}
            onClick={() => onRedeem(card)}>{t(`${usageKey}.useReset`)}</button>
        </div>;
      })}
      {cards.length < usage.reset_credits && !snapshot.reset_cards_error && (
        <p className="codex-account-hint">{t(`${usageKey}.resetDetailsUnavailable`)}</p>
      )}
    </div>}
  </>;
}
