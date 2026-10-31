import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { chatAPI, type ChatSettings } from "../../types/personalChat";
import { PersonalChatPreferences } from "../PersonalChat";

export function PersonalChatSettings() {
  const { t } = useTranslation();
  const [settings, setSettings] = useState<ChatSettings>();
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    chatAPI<{ settings: ChatSettings }>(
      "/settings",
      undefined,
      undefined,
      controller.signal,
    )
      .then((r) => setSettings(r.settings))
      .catch((e) => {
        if (!controller.signal.aborted) setError(String(e));
      });
    return () => controller.abort();
  }, []);
  return (
    <section className="settings-section">
      <h3 className="settings-section-title">{t("personal.chatSettings")}</h3>
      {error && (
        <pre className="personal-error" role="alert">
          {error}
        </pre>
      )}
      {settings && (
        <PersonalChatPreferences
          key={JSON.stringify(settings)}
          settings={settings}
          onSave={async (value) => {
            try {
              const r = await chatAPI<{ settings: ChatSettings }>(
                "/settings",
                value,
                "PUT",
              );
              setSettings(r.settings);
              setError("");
            } catch (e) {
              setError(String(e));
              throw e;
            }
          }}
        />
      )}
    </section>
  );
}
