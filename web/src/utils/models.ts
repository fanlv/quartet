export const PROVIDER_ICONS: Record<string, string> = {
  ark: 'https://upload-images.jianshu.io/upload_images/12321605-0ece441a9983a40d.png?imageMogr2/auto-orient/strip%7CimageView2/2/w/1240',
  openai: 'https://upload-images.jianshu.io/upload_images/12321605-91a8106e59f7126f.png?imageMogr2/auto-orient/strip%7CimageView2/2/w/1240',
  claude: 'https://upload-images.jianshu.io/upload_images/12321605-2fc28d63c089a216.png?imageMogr2/auto-orient/strip%7CimageView2/2/w/1240',
  deepseek: 'https://upload-images.jianshu.io/upload_images/12321605-6a3bdc5e184a6e04.png?imageMogr2/auto-orient/strip%7CimageView2/2/w/1240',
  gemini: 'https://upload-images.jianshu.io/upload_images/12321605-21f811ad1bed58bd.png?imageMogr2/auto-orient/strip%7CimageView2/2/w/1240',
  ollama: 'https://upload-images.jianshu.io/upload_images/12321605-ee4bd5afa8598a64.png?imageMogr2/auto-orient/strip%7CimageView2/2/w/1240',
  qwen: 'https://upload-images.jianshu.io/upload_images/12321605-2763958be48a880a.png?imageMogr2/auto-orient/strip%7CimageView2/2/w/1240',
};

export interface ModelInfo {
  id: number;
  display_name: string;
  model_class: string;
}

export interface ProviderModelList {
  provider: {
    name: string;
    icon_url: string;
    model_class: string;
  };
  model_list: ModelInfo[];
}

export type ModelOption = { model: ModelInfo; providerName: string; iconUrl: string };

export async function fetchModelOptions(lang?: string): Promise<ModelOption[]> {
  try {
    const resolvedLang = (lang || localStorage.getItem('quartet-language') || 'en').startsWith('zh') ? 'zh' : 'en';
    const res = await fetch(`/api/v1/config/model/list?lang=${resolvedLang}`);
    const data = await res.json().catch(() => null);
    if (!data || data.code !== 0 || !data.provider_model_list) {
      return [];
    }
    const allModels: ModelOption[] = [];
    for (const provider of data.provider_model_list as ProviderModelList[]) {
      if (provider.model_list) {
        for (const m of provider.model_list) {
          allModels.push({
            model: m,
            providerName: provider.provider.name,
            iconUrl: PROVIDER_ICONS[provider.provider.model_class] || provider.provider.icon_url,
          });
        }
      }
    }
    return allModels;
  } catch (err) {
    console.error('Failed to fetch models:', err);
    return [];
  }
}
