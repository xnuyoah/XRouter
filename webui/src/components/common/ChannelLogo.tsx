import type { ComponentType, CSSProperties } from 'react';

import AgnesAI from '@lobehub/icons/es/AgnesAI';
import Anthropic from '@lobehub/icons/es/Anthropic';
import Antigravity from '@lobehub/icons/es/Antigravity';
import Claude from '@lobehub/icons/es/Claude';
import Codex from '@lobehub/icons/es/Codex';
import DeepSeek from '@lobehub/icons/es/DeepSeek';
import Devin from '@lobehub/icons/es/Devin';
import Exa from '@lobehub/icons/es/Exa';
import Firecrawl from '@lobehub/icons/es/Firecrawl';
import Gemini from '@lobehub/icons/es/Gemini';
import GLMV from '@lobehub/icons/es/GLMV';
import Google from '@lobehub/icons/es/Google';
import Kimi from '@lobehub/icons/es/Kimi';
import Meta from '@lobehub/icons/es/Meta';
import Minimax from '@lobehub/icons/es/Minimax';
import Mistral from '@lobehub/icons/es/Mistral';
import Moonshot from '@lobehub/icons/es/Moonshot';
import Nvidia from '@lobehub/icons/es/Nvidia';
import Ollama from '@lobehub/icons/es/Ollama';
import OpenAI from '@lobehub/icons/es/OpenAI';
import OpenRouter from '@lobehub/icons/es/OpenRouter';
import Qwen from '@lobehub/icons/es/Qwen';
import Stepfun from '@lobehub/icons/es/Stepfun';
import Tavily from '@lobehub/icons/es/Tavily';
import VertexAI from '@lobehub/icons/es/VertexAI';
import XAI from '@lobehub/icons/es/XAI';
import Zhipu from '@lobehub/icons/es/Zhipu';

import iflowLogo from '@/assets/icons/iflow.svg';
import anysearchLogo from '@/assets/providers/anysearch.png';
import tinyfishLogo from '@/assets/providers/tinyfish.svg';

import { channelLogoHue, channelLogoInitial, channelLogoKey } from './channelLogoUtils';
import styles from './ChannelLogo.module.scss';

type LogoIcon = ComponentType<{ size?: number | string }>;

const ICON_LOGOS: Record<string, LogoIcon> = {
  agnes: AgnesAI,
  anthropic: Anthropic,
  antigravity: Antigravity,
  claude: Claude.Color,
  codex: Codex.Color,
  deepseek: DeepSeek.Color,
  devin: Devin.Color,
  exa: Exa.Color,
  firecrawl: Firecrawl.Color,
  gemini: Gemini.Color,
  glm: GLMV.Color,
  google: Google.Color,
  kimi: Kimi.Color,
  meta: Meta.Color,
  minimax: Minimax.Color,
  mistral: Mistral,
  moonshot: Moonshot,
  nvidia: Nvidia.Color,
  ollama: Ollama,
  openai: OpenAI,
  openrouter: OpenRouter.Color,
  qwen: Qwen.Color,
  stepfun: Stepfun,
  tavily: Tavily.Color,
  vertexai: VertexAI.Color,
  xai: XAI,
  zhipu: Zhipu.Color,
};

const ASSET_LOGOS: Record<string, { src: string; monochrome?: boolean }> = {
  anysearch: { src: anysearchLogo, monochrome: true },
  iflow: { src: iflowLogo },
  tinyfish: { src: tinyfishLogo },
};

export interface ChannelLogoProps {
  value?: string | null;
  label?: string | null;
  size?: number;
  className?: string;
}

/**
 * Brand mark for an LLM provider or web-search channel.
 *
 * LobeHub supplies the model/provider brands; AnySearch and Tinyfish use their
 * official assets because LobeHub does not ship them. Unknown identifiers keep
 * a stable colored initial tile so new channels still render cleanly.
 */
export function ChannelLogo({ value, label, size = 18, className }: ChannelLogoProps) {
  const source = value || label || '';
  const key = channelLogoKey(source);
  const asset = ASSET_LOGOS[key];
  const Icon = ICON_LOGOS[key];
  const wrapperClass = className ? `${styles.logo} ${className}` : styles.logo;

  if (asset) {
    return (
      <span
        className={wrapperClass}
        style={{ width: size, height: size }}
        aria-hidden="true"
        data-channel-logo={key}
      >
        <img
          className={`${styles.image} ${asset.monochrome ? styles.monochrome : ''}`}
          src={asset.src}
          alt=""
          draggable={false}
        />
      </span>
    );
  }

  if (Icon) {
    return (
      <span
        className={wrapperClass}
        style={{ width: size, height: size }}
        aria-hidden="true"
        data-channel-logo={key}
      >
        <Icon size={Math.round(size * 0.9)} />
      </span>
    );
  }

  const fallbackStyle = {
    '--logo-hue': channelLogoHue(key || source),
    fontSize: Math.max(9, Math.round(size * 0.46)),
  } as CSSProperties;

  return (
    <span
      className={wrapperClass}
      style={{ width: size, height: size }}
      aria-hidden="true"
      data-channel-logo={key || 'unknown'}
    >
      <span className={styles.fallback} style={fallbackStyle}>
        {channelLogoInitial(source)}
      </span>
    </span>
  );
}
