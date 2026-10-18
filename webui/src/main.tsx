import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '@/styles/global.scss';
import { INLINE_LOGO_DARK, INLINE_LOGO_LIGHT } from '@/assets/logoInline';
import { useThemeStore } from '@/stores';
import App from './App.tsx';

document.title = 'XRouter';
document.documentElement.setAttribute('translate', 'no');
document.documentElement.classList.add('notranslate');

const applyFavicon = (resolvedTheme: 'light' | 'dark') => {
  const href = resolvedTheme === 'dark' ? INLINE_LOGO_DARK : INLINE_LOGO_LIGHT;
  const faviconEl = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
  if (faviconEl) {
    faviconEl.href = href;
    faviconEl.type = 'image/svg+xml';
    return;
  }
  const newFavicon = document.createElement('link');
  newFavicon.rel = 'icon';
  newFavicon.type = 'image/svg+xml';
  newFavicon.href = href;
  document.head.appendChild(newFavicon);
};

applyFavicon(useThemeStore.getState().resolvedTheme);
useThemeStore.subscribe((state) => applyFavicon(state.resolvedTheme));

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>
);
