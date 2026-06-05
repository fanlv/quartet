import { useTranslation } from 'react-i18next';
import './WelcomeHero.css';

interface WelcomeHeroProps {
  onSuggestionClick?: (text: string) => void;
  disabled?: boolean;
}

export function WelcomeHero({ onSuggestionClick, disabled }: WelcomeHeroProps) {
  const { t } = useTranslation();

  const suggestions = [
    { text: t('welcome.suggestions.weather'), emoji: '☀️' },
    { text: t('welcome.suggestions.aiProgress'), emoji: '🔍' },
    { text: t('welcome.suggestions.whatCanYouDo'), emoji: '🤔' },
    { text: t('welcome.suggestions.claudeProgress'), emoji: '❤️‍🔥' },
    { text: t('welcome.suggestions.hotNews'), emoji: '📰' },
  ];

  return (
    <div className="welcome-hero">
      <div className="welcome-hero-logo">🤖</div>
      <h1 className="welcome-hero-title">{t('welcome.title')}</h1>
      <p className="welcome-hero-subtitle">{t('welcome.subtitle')}</p>
      <div className="welcome-hero-suggestions">
        {suggestions.map((item, index) => (
          <span
            key={index}
            className="welcome-hero-tag"
            onClick={() => !disabled && onSuggestionClick?.(item.text)}
          >
            {item.emoji} {item.text}
          </span>
        ))}
      </div>
    </div>
  );
}
