import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { Entry } from './Entry';
import { ApiError, type Api } from './api';
import { createDemoApi } from './demo';
import { saveDemoSession, scopedDemoApi } from './demo-auth';

afterEach(() => {
  window.history.replaceState(null, '', '/');
  sessionStorage.clear();
});

describe('entry and authorization', () => {
  it('requires a test login and scopes the service to the chosen company and position', async () => {
    const user = userEvent.setup();
    render(<Entry api={createDemoApi()} demo />);
    await user.click(screen.getAllByRole('button', { name: 'Открыть Бизнес-пульс' })[0]);
    expect(screen.getByRole('dialog', { name: /Войти в Бизнес-пульс/ })).toBeVisible();
    await user.type(screen.getByRole('textbox', { name: 'Логин' }), 'demo.logistics');
    await user.type(screen.getByLabelText('Пароль'), 'wrong');
    await user.selectOptions(screen.getByRole('combobox', { name: 'Компания' }), 'U_76');
    await user.selectOptions(screen.getByRole('combobox', { name: 'Должность' }), 'ceo');
    await user.click(screen.getByRole('button', { name: 'Войти в демо' }));
    expect(screen.getByRole('alert')).toHaveTextContent('Проверьте логин');
    expect(window.location.pathname).toBe('/');
    await user.clear(screen.getByLabelText('Пароль'));
    await user.type(screen.getByLabelText('Пароль'), 'Logistics2026!');
    await user.click(screen.getByRole('button', { name: 'Войти в демо' }));
    expect(window.location.pathname).toBe('/pulse');
    expect(await screen.findByText('Деморежим · синтетические данные')).toBeVisible();
    expect(await screen.findByRole('combobox', { name: 'Компания и ракурс анализа' })).toHaveValue(
      'demo-logistics-ceo',
    );
    expect(
      screen
        .getByRole('combobox', { name: 'Компания и ракурс анализа' })
        .querySelectorAll('option'),
    ).toHaveLength(1);
    expect(sessionStorage.getItem('burmalda.demo-auth.v1')).not.toContain('Logistics2026!');
    await user.click(screen.getByRole('button', { name: 'Выйти' }));
    expect(window.location.pathname).toBe('/');
    expect(sessionStorage.getItem('burmalda.demo-auth.v1')).toBeNull();
  });

  it('guards a direct demo URL without a test login', async () => {
    window.history.replaceState(null, '', '/pulse');
    render(<Entry api={createDemoApi()} demo />);
    expect(screen.getByRole('dialog', { name: /Войти в Бизнес-пульс/ })).toBeVisible();
    expect(screen.queryByText('Деморежим · синтетические данные')).not.toBeInTheDocument();
  });

  it('restores only the selected demo context after a reload', async () => {
    const session = { login: 'demo.tech', companyId: 'U_67', positionId: 'operations' };
    saveDemoSession(session);
    window.history.replaceState(null, '', '/pulse');
    render(<Entry api={createDemoApi()} demo />);
    expect(await screen.findByRole('combobox', { name: 'Компания и ракурс анализа' })).toHaveValue(
      'demo-technology-operations',
    );
    expect(() => scopedDemoApi(createDemoApi(), session).feed('demo-logistics-cfo')).toThrow(
      'Этот демоконтекст недоступен.',
    );
  });

  it('does not offer a fake bank login when the API has no session', async () => {
    const user = userEvent.setup();
    const api: Api = {
      ...createDemoApi(),
      me: async () => {
        throw new ApiError(
          { code: 'UNAUTHENTICATED', message: 'Session required', retryable: false },
          401,
        );
      },
    };
    render(<Entry api={api} demo={false} authStartUrl="/auth/start" />);
    await user.click(screen.getAllByRole('button', { name: 'Открыть Бизнес-пульс' })[0]);
    expect(await screen.findByText('Нужен вход в Альфа-Бизнес')).toBeVisible();
    expect(screen.getByRole('link', { name: 'Продолжить к авторизации' })).toHaveAttribute(
      'href',
      `${window.location.origin}/auth/start`,
    );
    expect(screen.queryByLabelText(/пароль|код из смс/i)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Перейти к аналитике' })).not.toBeInTheDocument();
  });

  it('opens the real service only after the server confirms a session', async () => {
    const user = userEvent.setup();
    render(<Entry api={createDemoApi()} demo={false} />);
    await user.click(screen.getAllByRole('button', { name: 'Открыть Бизнес-пульс' })[0]);
    expect(await screen.findByText('Сессия подтверждена')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Перейти к аналитике' }));
    expect(window.location.pathname).toBe('/pulse');
    expect(await screen.findByRole('link', { name: 'Бизнес-пульс' })).toBeVisible();
  });

  it('guards a direct service URL with the existing server session', async () => {
    window.history.replaceState(null, '', '/pulse');
    const api: Api = {
      ...createDemoApi(),
      me: async () => {
        throw new ApiError(
          { code: 'UNAUTHENTICATED', message: 'Session required', retryable: false },
          401,
        );
      },
    };
    render(<Entry api={api} demo={false} />);
    expect(await screen.findByText('Нужен вход в Альфа-Бизнес')).toBeVisible();
    expect(screen.queryByText('Деморежим · синтетические данные')).not.toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Закрыть окно входа' }));
    expect(window.location.pathname).toBe('/');
  });
});
