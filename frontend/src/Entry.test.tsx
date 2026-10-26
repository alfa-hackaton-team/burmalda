import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
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

  it('uses the server login and logout before showing real data', async () => {
    const user = userEvent.setup();
    let authenticated = false;
    const login = vi.fn(async () => {
      authenticated = true;
      return { user_id: 'server-user', expires_at: new Date().toISOString() };
    });
    const logout = vi.fn(async () => {
      authenticated = false;
      return { signed_out: true };
    });
    const api: Api = {
      ...createDemoApi(),
      login,
      logout,
      me: async () => {
        if (!authenticated)
          throw new ApiError(
            { code: 'UNAUTHENTICATED', message: 'Session required', retryable: false },
            401,
          );
        return { user_id: 'server-user', display_name: 'Тестовый пользователь' };
      },
    };
    render(<Entry api={api} demo={false} />);
    await user.click(screen.getAllByRole('button', { name: 'Открыть Бизнес-пульс' })[0]);
    const dialog = await screen.findByRole('dialog', { name: /Войти в Бизнес-пульс/ });
    await user.type(within(dialog).getByRole('textbox', { name: 'Логин' }), 'server.user');
    await user.type(within(dialog).getByLabelText('Пароль'), 'strong-password');
    await user.click(within(dialog).getByRole('button', { name: 'Войти' }));
    expect(login).toHaveBeenCalledWith('server.user', 'strong-password');
    expect(await screen.findByText('Тестовый пользователь')).toBeVisible();
    expect(window.location.pathname).toBe('/pulse');
    await user.click(screen.getByRole('button', { name: 'Выйти' }));
    expect(logout).toHaveBeenCalledOnce();
    expect(window.location.pathname).toBe('/');
  });

  it('registers against the server with a validated synthetic company', async () => {
    const user = userEvent.setup();
    let authenticated = false;
    const register = vi.fn(async () => ({ user_id: 'new-user' }));
    const api: Api = {
      ...createDemoApi(),
      me: async () => {
        if (!authenticated)
          throw new ApiError(
            { code: 'UNAUTHENTICATED', message: 'Session required', retryable: false },
            401,
          );
        return { user_id: 'new-user' };
      },
      register,
      registrationCompany: async () => ({
        company_uid: 'U_80',
        display_name: 'Компания U_80',
        industry: 'Металлы',
        city: 'Екатеринбург',
        region: '',
      }),
      login: async () => {
        authenticated = true;
        return { user_id: 'new-user', expires_at: new Date().toISOString() };
      },
    };
    render(<Entry api={api} demo={false} />);
    await user.click(screen.getAllByRole('button', { name: 'Открыть Бизнес-пульс' })[0]);
    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByRole('tab', { name: 'Регистрация' }));
    await user.type(within(dialog).getByRole('textbox', { name: 'Ваше имя' }), 'Мария Тестовая');
    await user.type(within(dialog).getByRole('textbox', { name: 'Логин' }), 'maria.test');
    await user.type(within(dialog).getByLabelText('Пароль'), 'long-secure-password');
    await user.type(within(dialog).getByLabelText('Повторите пароль'), 'long-secure-password');
    await user.type(
      within(dialog).getByRole('textbox', { name: 'ID компании из набора данных' }),
      'U_80',
    );
    await user.selectOptions(within(dialog).getByRole('combobox', { name: 'Должность' }), 'cfo');
    expect(await within(dialog).findByText(/Компания U_80.*Металлы/)).toBeVisible();
    await user.click(within(dialog).getByRole('button', { name: 'Зарегистрироваться' }));
    expect(register).toHaveBeenCalledWith({
      login: 'maria.test',
      password: 'long-secure-password',
      display_name: 'Мария Тестовая',
      company_uid: 'U_80',
      role_code: 'cfo',
    });
    expect(window.location.pathname).toBe('/pulse');
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
    expect(await screen.findByRole('textbox', { name: 'Логин' })).toBeVisible();
    expect(screen.queryByText('Деморежим · синтетические данные')).not.toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Закрыть окно входа' }));
    expect(window.location.pathname).toBe('/');
  });
});
