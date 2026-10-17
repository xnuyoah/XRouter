/**
 * 通用类型定义
 */

export type Theme = 'light' | 'dark';

export type Language = 'zh-CN' | 'en';

export type NotificationType = 'info' | 'success' | 'warning' | 'error';

export interface Notification {
  id: string;
  message: string;
  type: NotificationType;
  duration?: number;
}
