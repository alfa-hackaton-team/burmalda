import { Component, type ReactNode } from 'react';

export class ErrorBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  render() {
    if (this.state.failed) {
      return (
        <main className="fatal-state" role="alert">
          <h1>Не удалось открыть сервис</h1>
          <p>Обновите страницу. Если ошибка повторится, попробуйте позже.</p>
          <button type="button" onClick={() => window.location.reload()}>
            Обновить страницу
          </button>
        </main>
      );
    }
    return this.props.children;
  }
}
