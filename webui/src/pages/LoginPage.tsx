import React, { useEffect, useMemo, useState, useCallback } from 'react';
import { Navigate, useNavigate, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import {
  IconActivity,
  IconAlertTriangle,
  IconChevronDown,
  IconChevronUp,
  IconEye,
  IconEyeOff,
  IconKey,
  IconNetwork,
  IconShield,
  IconSlidersHorizontal,
} from '@/components/ui/icons';
import { useAuthStore, useLanguageStore, useNotificationStore, useThemeStore } from '@/stores';
import { detectApiBaseFromLocation, normalizeApiBase } from '@/utils/connection';
import { LANGUAGE_LABEL_KEYS, LANGUAGE_ORDER } from '@/utils/constants';
import { isSupportedLanguage } from '@/utils/language';
import { INLINE_LOGO_DARK, INLINE_LOGO_LIGHT } from '@/assets/logoInline';
import type { ApiError } from '@/types';
import { LegacyBackendError } from '@/services/api/legacyBackendProbe';
import styles from './LoginPage.module.scss';

/**
 * 将 API 错误转换为本地化的用户友好消息
 */
type RedirectState = { from?: { pathname?: string } };

function getLocalizedErrorMessage(error: unknown, t: (key: string) => string): string {
  if (error instanceof LegacyBackendError) return t('login.error_legacy_backend');
  const apiError = error as Partial<ApiError>;
  const status = typeof apiError.status === 'number' ? apiError.status : undefined;
  const code = typeof apiError.code === 'string' ? apiError.code : undefined;
  const message =
    error instanceof Error
      ? error.message
      : typeof apiError.message === 'string'
        ? apiError.message
        : typeof error === 'string'
          ? error
          : '';

  const withHttpStatus = (summary: string) => {
    if (!status) {
      return summary;
    }

    const genericAxiosMessage = `Request failed with status code ${status}`;
    const detail = message.trim();
    const backendDetail =
      detail && detail !== genericAxiosMessage
        ? ` (${t('login.error_backend_detail')}: ${detail})`
        : '';

    return `HTTP ${status}: ${summary}${backendDetail}`;
  };

  // 根据 HTTP 状态码判断
  if (status === 401) {
    return withHttpStatus(t('login.error_unauthorized'));
  }
  if (status === 403) {
    return withHttpStatus(t('login.error_forbidden'));
  }
  if (status === 404) {
    return withHttpStatus(t('login.error_not_found'));
  }
  if (status && status >= 500) {
    return withHttpStatus(t('login.error_server'));
  }

  // 根据 axios 错误码判断
  if (code === 'ECONNABORTED' || message.toLowerCase().includes('timeout')) {
    return t('login.error_timeout');
  }
  if (code === 'ERR_NETWORK' || message.toLowerCase().includes('network error')) {
    return t('login.error_network');
  }
  if (code === 'ERR_CERT_AUTHORITY_INVALID' || message.toLowerCase().includes('certificate')) {
    return t('login.error_ssl');
  }

  // 检查 CORS 错误
  if (message.toLowerCase().includes('cors') || message.toLowerCase().includes('cross-origin')) {
    return t('login.error_cors');
  }

  // 默认错误消息
  return withHttpStatus(t('login.error_invalid'));
}

export function LoginPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const { showNotification } = useNotificationStore();
  const language = useLanguageStore((state) => state.language);
  const setLanguage = useLanguageStore((state) => state.setLanguage);
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);
  const login = useAuthStore((state) => state.login);
  const restoreSession = useAuthStore((state) => state.restoreSession);
  const storedBase = useAuthStore((state) => state.apiBase);
  const storedKey = useAuthStore((state) => state.managementKey);
  const storedRememberPassword = useAuthStore((state) => state.rememberPassword);

  const [apiBase, setApiBase] = useState('');
  const [managementKey, setManagementKey] = useState('');
  const [showCustomBase, setShowCustomBase] = useState(false);
  const [showKey, setShowKey] = useState(false);
  const [rememberPassword, setRememberPassword] = useState(false);
  const [loading, setLoading] = useState(false);
  const [autoLoading, setAutoLoading] = useState(true);
  const [autoLoginSuccess, setAutoLoginSuccess] = useState(false);
  const [error, setError] = useState('');

  const detectedBase = useMemo(() => detectApiBaseFromLocation(), []);
  const languageOptions = useMemo(
    () =>
      LANGUAGE_ORDER.map((lang) => ({
        value: lang,
        label: t(LANGUAGE_LABEL_KEYS[lang]),
      })),
    [t]
  );
  const handleLanguageChange = useCallback(
    (selectedLanguage: string) => {
      if (!isSupportedLanguage(selectedLanguage)) {
        return;
      }
      setLanguage(selectedLanguage);
    },
    [setLanguage]
  );

  useEffect(() => {
    const init = async () => {
      try {
        const autoLoggedIn = await restoreSession();
        if (autoLoggedIn) {
          setAutoLoginSuccess(true);
          // 延迟跳转，让用户看到成功动画
          setTimeout(() => {
            const redirect = (location.state as RedirectState | null)?.from?.pathname || '/';
            navigate(redirect, { replace: true });
          }, 1500);
        } else {
          setApiBase(storedBase || detectedBase);
          setManagementKey(storedKey || '');
          setRememberPassword(storedRememberPassword || Boolean(storedKey));
        }
      } finally {
        // 自动登录成功时 showSplash 仍由 autoLoginSuccess 维持，可无条件结束 loading
        setAutoLoading(false);
      }
    };

    init();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleSubmit = useCallback(async () => {
    if (!managementKey.trim()) {
      setError(t('login.error_required'));
      return;
    }

    const baseToUse = apiBase ? normalizeApiBase(apiBase) : detectedBase;
    setLoading(true);
    setError('');
    try {
      await login({
        apiBase: baseToUse,
        managementKey: managementKey.trim(),
        rememberPassword,
      });
      showNotification(t('common.connected_status'), 'success');
      navigate('/', { replace: true });
    } catch (err: unknown) {
      const message = getLocalizedErrorMessage(err, t);
      setError(message);
      showNotification(`${t('notification.login_failed')}: ${message}`, 'error');
    } finally {
      setLoading(false);
    }
  }, [
    apiBase,
    detectedBase,
    login,
    managementKey,
    navigate,
    rememberPassword,
    showNotification,
    t,
  ]);

  const handleSubmitKeyDown = useCallback(
    (event: React.KeyboardEvent) => {
      if (event.key === 'Enter' && !loading) {
        event.preventDefault();
        handleSubmit();
      }
    },
    [loading, handleSubmit]
  );

  if (isAuthenticated && !autoLoading && !autoLoginSuccess) {
    const redirect = (location.state as RedirectState | null)?.from?.pathname || '/';
    return <Navigate to={redirect} replace />;
  }

  // 显示启动动画（自动登录中或自动登录成功）
  const showSplash = autoLoading || autoLoginSuccess;
  const logoSrc = resolvedTheme === 'dark' ? INLINE_LOGO_DARK : INLINE_LOGO_LIGHT;

  return (
    <div className={styles.container}>
      {showSplash ? (
        <div className={styles.splash}>
          <div className={styles.splashGrid} aria-hidden="true" />
          <div className={styles.splashOrb} aria-hidden="true" />
          <div className={styles.splashContent}>
            <span className={styles.splashLogoWrap}>
              <img src={logoSrc} alt="XRouter" className={styles.splashLogo} />
            </span>
            <span className={styles.splashEyebrow}>{t('splash.eyebrow')}</span>
            <h1 className={styles.splashTitle}>{t('splash.title')}</h1>
            <div className={styles.splashLoader}>
              <div className={styles.splashLoaderBar} />
            </div>
            <span className={styles.splashStatus}>
              {t(autoLoginSuccess ? 'splash.status_success' : 'splash.status')}
            </span>
          </div>
        </div>
      ) : (
        <div className={styles.shell}>
          <aside className={styles.brandPanel}>
            <div className={styles.brandGrid} aria-hidden="true" />
            <div className={styles.brandOrb} aria-hidden="true" />
            <div className={styles.brandTop}>
              <span className={styles.brandLogo}>
                <img src={logoSrc} alt="" />
              </span>
              <span className={styles.brandName}>XRouter</span>
              <span className={styles.versionBadge}>v8</span>
            </div>

            <div className={styles.brandCopy}>
              <span className={styles.eyebrow}>{t('login.brand_eyebrow')}</span>
              <h1>
                <span>{t('login.brand_title_line1')}</span>
                <span>{t('login.brand_title_line2')}</span>
              </h1>
              <p>{t('login.brand_description')}</p>
            </div>

            <ul className={styles.features}>
              <li>
                <span className={styles.featureIcon}><IconNetwork size={15} /></span>
                {t('login.feature_routing')}
              </li>
              <li>
                <span className={styles.featureIcon}><IconKey size={15} /></span>
                {t('login.feature_credentials')}
              </li>
              <li>
                <span className={styles.featureIcon}><IconActivity size={15} /></span>
                {t('login.feature_observability')}
              </li>
            </ul>

            <div className={styles.brandFooter}>
              <span className={styles.liveDot} />
              {t('login.brand_footer')}
            </div>
          </aside>

          <main className={styles.formPanel}>
            <div className={styles.formTop}>
              <div className={styles.mobileBrand}>
                <img src={logoSrc} alt="" />
                <span>XRouter</span>
              </div>
              <Select
                className={styles.languageSelect}
                value={language}
                options={languageOptions}
                onChange={handleLanguageChange}
                fullWidth={false}
                ariaLabel={t('language.switch')}
              />
            </div>

            <div className={styles.formBody}>
              <header className={styles.formHeader}>
                <span className={styles.eyebrow}>{t('login.form_eyebrow')}</span>
                <h2>{t('title.login')}</h2>
                <p>{t('login.subtitle')}</p>
              </header>

              <Input
                autoFocus
                label={t('login.management_key_label')}
                placeholder={t('login.management_key_placeholder')}
                type={showKey ? 'text' : 'password'}
                name="cpa-management-key"
                autoComplete="current-password"
                value={managementKey}
                onChange={(e) => setManagementKey(e.target.value)}
                onKeyDown={handleSubmitKeyDown}
                rightElement={
                  <button
                    type="button"
                    className="btn btn-ghost btn-sm"
                    onClick={() => setShowKey((prev) => !prev)}
                    aria-label={
                      showKey
                        ? t('login.hide_key', { defaultValue: '隐藏密钥' })
                        : t('login.show_key', { defaultValue: '显示密钥' })
                    }
                    title={
                      showKey
                        ? t('login.hide_key', { defaultValue: '隐藏密钥' })
                        : t('login.show_key', { defaultValue: '显示密钥' })
                    }
                  >
                    {showKey ? <IconEyeOff size={16} /> : <IconEye size={16} />}
                  </button>
                }
              />

              <div className={styles.optionsRow}>
                <SelectionCheckbox
                  checked={rememberPassword}
                  onChange={setRememberPassword}
                  ariaLabel={t('login.remember_password_label')}
                  label={t('login.remember_password_label')}
                  labelClassName={styles.toggleLabel}
                />
                <span className={styles.secureHint}>
                  <IconShield size={13} />
                  {t('login.security_note')}
                </span>
              </div>

              <Button className={styles.submitButton} fullWidth onClick={handleSubmit} loading={loading}>
                {loading ? t('login.submitting') : t('login.submit_button')}
              </Button>

              {error && (
                <div className={styles.errorBox}>
                  <IconAlertTriangle size={16} />
                  <span>{error}</span>
                </div>
              )}

              <div className={styles.advanced}>
                <button
                  type="button"
                  className={styles.connectionToggle}
                  onClick={() => setShowCustomBase((prev) => !prev)}
                  aria-expanded={showCustomBase}
                >
                  <IconSlidersHorizontal size={13} />
                  {t('login.connection_settings')}
                  {showCustomBase ? <IconChevronUp size={13} /> : <IconChevronDown size={13} />}
                </button>
                {showCustomBase && (
                  <div className={styles.advancedPanel}>
                    <Input
                      label={t('login.custom_connection_label')}
                      placeholder={t('login.custom_connection_placeholder')}
                      value={apiBase}
                      onChange={(e) => setApiBase(e.target.value)}
                    />
                    <p className={styles.connectionHint}>{t('login.custom_connection_hint')}</p>
                  </div>
                )}
              </div>
            </div>

            <footer className={styles.formFooter}>
              <span>XRouter</span>
              <span className={styles.footerDot} />
              <span>{t('login.brand_footer')}</span>
            </footer>
          </main>
        </div>
      )}
    </div>
  );
}
